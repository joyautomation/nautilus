package server

import (
	"context"
	"encoding/json"
	"errors"
	"html/template"
	"math"
	"mime"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/joyautomation/nautilus/audit"
	"github.com/joyautomation/nautilus/auth/users"
)

// Browser sign-in (#708 PR 5b-1, docs/design/identity-audit.md §2):
// GET /signin is a small form, POST /api/signin checks a local user's
// password and starts a session, POST /api/signout ends it.
//
// Every refusal of a name and password (a wrong password, an unknown or
// disabled user, a box user the auth: block does not name) gets the same
// message and costs the same (one argon2id verification); the journal
// records which it was. Attempts are throttled per client address (an
// IPv6 client by its /64) and per name before any hashing, and at most two
// verifications (64 MiB each) run at once: a controller also scans. No
// account is ever disabled by failures, but a flood of guesses at one
// name from many addresses keeps that name's bucket empty, and so keeps
// its owner from signing in, for as long as the flood lasts.
//
// Sign-in and sign-out sit outside the audited seam (they journal
// themselves, auth.signin and auth.signout) and work while the journal is
// down: they change no process state, and a supervisor must be able to
// sign in to deal with the outage.

// signInRefused is what every refused name and password is told.
const signInRefused = "sign-in refused: the user name or password is wrong"

// maxSignInBody is the most a sign-in request may send.
const maxSignInBody = 8 << 10

// signInHashing is how many argon2id verifications run at once.
const signInHashing = 2

// signInQueue is how long a request waits for one.
var signInQueue = 10 * time.Second

// signInGate is sign-in's throttling: a token bucket per client address
// (the tight one) and per name (looser, so a flood against one name from
// one address cannot keep its owner out from another), and the slots for
// the password hash.
type signInGate struct {
	addrs, names *limiter
	// sessions is re-authentication's (POST /api/elevate) bucket per
	// session: only the cookie's holder spends it.
	sessions *limiter
	// oidc is the OIDC sign-in's start, per client address: it costs no
	// hashing, so it is looser than a password's, but a client must not
	// fill the table of sign-ins under way.
	oidc    *limiter
	hashing chan struct{}
}

func newSignInGate() *signInGate {
	return &signInGate{
		// 10 attempts at once, then one each 6 s.
		addrs: newLimiter(10, 6*time.Second),
		// 20 at once, then one a second.
		names: newLimiter(20, time.Second),
		// 5 at once (the wrong passwords that end a session), then one
		// each 6 s.
		sessions: newLimiter(maxElevateFails, 6*time.Second),
		// 30 at once, then one a second.
		oidc:    newLimiter(30, time.Second),
		hashing: make(chan struct{}, signInHashing),
	}
}

// limiter is a token bucket per key. Its map is swept of buckets that
// have filled up again (they say nothing a new one would not), so keys a
// client makes up cannot grow it for long.
type limiter struct {
	burst float64
	every time.Duration // one token back per every
	now   func() time.Time

	mu sync.Mutex
	b  map[string]*bucket
}

type bucket struct {
	tokens float64
	at     time.Time
}

// A limiter past limiterSweep buckets looks at sweepBatch of them (Go's
// map order is random) at each new key and drops the full ones: a bounded
// cost per attempt, never a sweep of the whole map. Past limiterMax it
// drops one it looked at even if not full (an attacker with that many
// addresses gains little from one reset), so memory stays bounded.
const (
	limiterSweep = 4096
	limiterMax   = 4 * limiterSweep
	sweepBatch   = 32
)

func newLimiter(burst int, every time.Duration) *limiter {
	return &limiter{burst: float64(burst), every: every, now: time.Now, b: map[string]*bucket{}}
}

func (l *limiter) refill(b *bucket, now time.Time) {
	b.tokens = math.Min(l.burst, b.tokens+float64(now.Sub(b.at))/float64(l.every))
	b.at = now
}

// take spends one of key's tokens; when there is none it says how long
// until there will be.
func (l *limiter) take(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b, ok := l.b[key]
	if !ok {
		if len(l.b) >= limiterSweep {
			l.sweepSome(now)
		}
		b = &bucket{tokens: l.burst, at: now}
		l.b[key] = b
	}
	l.refill(b, now)
	if b.tokens < 1 {
		return false, time.Duration((1 - b.tokens) * float64(l.every))
	}
	b.tokens--
	return true, 0
}

// sweepSome drops the full buckets among sweepBatch of them, and one of
// those looked at whatever its state while the map is at limiterMax.
func (l *limiter) sweepSome(now time.Time) {
	n := 0
	for k, o := range l.b {
		if n++; n > sweepBatch {
			break
		}
		if l.refill(o, now); o.tokens >= l.burst || len(l.b) >= limiterMax {
			delete(l.b, k)
		}
	}
}

// addrKey is the throttle's key for a client address: an IPv4 address as
// it is, an IPv6 one by its /64 (one host is handed a whole /64, and would
// otherwise rotate through it).
func addrKey(addr string) string {
	a, err := netip.ParseAddr(addr)
	if err != nil {
		return addr
	}
	if a = a.Unmap(); a.Is6() {
		p, _ := a.WithZone("").Prefix(64)
		return p.String()
	}
	return a.String()
}

// allow is the throttle for an attempt from addr as name: the address is
// charged first, so attempts the address is refused never reach the
// name's bucket. A name not in a user name's form shares one bucket.
func (g *signInGate) allow(addr, name string) (bool, time.Duration) {
	if ok, wait := g.addrs.take(addrKey(addr)); !ok {
		return false, wait
	}
	key := strings.ToLower(name)
	if users.ValidName(key) != nil {
		key = ""
	}
	return g.names.take(key)
}

// hash takes one of the verification slots, waiting while the client is
// there and for signInQueue at most: false when it got none.
func (g *signInGate) hash(ctx context.Context) (release func(), ok bool) {
	t := time.NewTimer(signInQueue)
	defer t.Stop()
	select {
	case g.hashing <- struct{}{}:
		return func() { <-g.hashing }, true
	case <-ctx.Done():
	case <-t.C:
	}
	return nil, false
}

// signInTransport reports whether a password may be sent on r: over TLS
// (the controller's own, or a trusted proxy's, secure), or straight from
// this machine's loopback with no forwarding header at all (a proxy on
// the same machine also connects from 127.0.0.1, and what it forwards may
// have come over plain HTTP from anywhere).
func (s *Server) signInTransport(r *http.Request) bool {
	if s.secure(r) {
		return true
	}
	a, err := netip.ParseAddr(hostOf(r.RemoteAddr))
	if err != nil || !a.Unmap().IsLoopback() {
		return false
	}
	for k := range r.Header {
		if strings.HasPrefix(k, "X-Forwarded-") || k == "Forwarded" {
			return false
		}
	}
	return true
}

// safeNext is where a sign-in sends the browser: a path on this
// controller, never another site ("//evil", "/\evil", "https://…"); "/"
// otherwise.
func safeNext(next string) string {
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.ContainsAny(next, "\\\r\n\t") {
		return "/"
	}
	u, err := url.Parse(next)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || u.Opaque != "" {
		return "/"
	}
	return next
}

// signInRequest is POST /api/signin's body, JSON or a form.
type signInRequest struct {
	Name     string `json:"name"`
	Password string `json:"password"`
	Next     string `json:"next"`
}

// isForm reports a request sent by an HTML form: it is answered with a
// page or a redirect, not JSON.
func isForm(r *http.Request) bool {
	mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return mt == "application/x-www-form-urlencoded" || mt == "multipart/form-data"
}

func readSignIn(w http.ResponseWriter, r *http.Request) (signInRequest, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxSignInBody)
	if !isForm(r) {
		var req signInRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		return req, err
	}
	if err := r.ParseForm(); err != nil {
		return signInRequest{}, err
	}
	return signInRequest{Name: r.PostForm.Get("name"), Password: r.PostForm.Get("password"), Next: r.PostForm.Get("next")}, nil
}

// errNoGrant is a box user (in the users file, right password) the auth:
// block grants nothing: refused like a wrong password.
var errNoGrant = errors.New("the auth: block grants this user nothing (auth.users)")

// handleSignIn is POST /api/signin.
func (s *Server) handleSignIn(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if s.sessions == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "sign-in needs an auth: block that grants users roles (auth.users) and a users file (naut users add)"})
		return
	}
	e := audit.Entry{User: "anonymous", Source: clientSource(r), Action: "auth.signin", From: s.clientAddr(r), Result: audit.ResultRefused}
	form := isForm(r)
	refuse := func(status int, msg, reason string, next string) {
		e.Detail = map[string]any{"status": status, "reason": reason}
		s.audit.record(e)
		if form {
			s.signInPage(w, r, status, msg, next)
			return
		}
		writeJSON(w, status, map[string]string{"error": msg})
	}
	// A browser's sign-in comes from this controller's own page: a form
	// on another site must not sign the operator in as someone else.
	if !s.ownOrigin(r) {
		refuse(http.StatusForbidden, "sign-in is accepted only from this controller's own sign-in page (the request's Origin must be this controller's scheme and host)", "cross-origin", "/")
		return
	}
	if !s.signInTransport(r) {
		refuse(http.StatusForbidden, "password sign-in needs HTTPS: set server.tls, or name the TLS proxy in server.trusted-proxies (plain HTTP is allowed only from this machine's loopback)", "not over TLS", "/")
		return
	}
	req, err := readSignIn(w, r)
	next := safeNext(req.Next)
	if err != nil {
		refuse(http.StatusBadRequest, "reading the sign-in: "+err.Error(), "unreadable request", next)
		return
	}
	name := strings.ToLower(strings.TrimSpace(req.Name))
	if users.ValidName(name) == nil {
		e.Target = "user:" + name
	}
	cred, refusal := s.checkPassword(r.Context(), s.clientAddr(r), name, req.Password)
	switch {
	case refusal.wait > 0:
		secs := int(math.Ceil(refusal.wait.Seconds()))
		w.Header().Set("Retry-After", strconv.Itoa(secs))
		refuse(http.StatusTooManyRequests, "too many sign-in attempts: try again in "+strconv.Itoa(secs)+" s", refusal.reason, next)
		return
	case refusal.busy:
		refuse(http.StatusServiceUnavailable, "the controller is busy checking other sign-ins: try again", refusal.reason, next)
		return
	case refusal.reason != "":
		refuse(http.StatusUnauthorized, signInRefused, refusal.reason, next)
		return
	}
	// A fresh id at every sign-in; the session the browser held, if any,
	// ends (no fixation, and none left behind).
	cookie := s.cookieName(r)
	for _, c := range r.CookiesNamed(cookie) {
		s.sessions.end(c.Value)
	}
	id, se := s.sessions.open(name, cred)
	http.SetCookie(w, &http.Cookie{
		Name:     cookie,
		Value:    id,
		Path:     "/",
		Expires:  se.expires,
		MaxAge:   int(se.expires.Sub(se.created).Seconds()),
		HttpOnly: true,
		Secure:   s.secure(r),
		SameSite: http.SameSiteStrictMode,
	})
	e.User, e.Target, e.Session, e.Result = "user:"+name, "", se.hash, audit.ResultOK
	e.Detail = map[string]any{"expires": se.expires.UTC().Format(time.RFC3339)}
	s.audit.record(e)
	if form {
		http.Redirect(w, r, next, http.StatusSeeOther)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": name, "expires": se.expires.UTC()})
}

// passwordRefusal is why checkPassword refused: reason for the journal,
// and whether it was the throttle (wait, how long until another attempt
// would be taken) or a full set of hashing slots (busy). The zero value
// is no refusal.
type passwordRefusal struct {
	reason string
	wait   time.Duration
	busy   bool
}

// checkPassword is every password sign-in's check, whatever the surface
// (POST /api/signin, an OPC UA UserName token): the throttle by address
// and by name, one of the hashing slots, one argon2id verification for
// every outcome and the grant checked after it, so the time taken says
// nothing about the name. name is already lowercased. It returns the
// credential a session remembers (the fingerprint of the hash the
// password was proved against, which sessionOf and Surface.Holds recheck).
func (s *Server) checkPassword(ctx context.Context, from, name, password string) (string, passwordRefusal) {
	if ok, wait := s.signin.allow(from, name); !ok {
		return "", passwordRefusal{reason: "throttled", wait: max(wait, time.Millisecond)}
	}
	release, ok := s.signin.hash(ctx)
	if !ok {
		return "", passwordRefusal{reason: "busy", busy: true}
	}
	file := s.users.Current()
	err := file.Authenticate(name, password)
	release()
	if err == nil && !s.policy.HasUser(name) {
		err = errNoGrant
	}
	if err != nil {
		reason := err.Error()
		if errors.Is(err, users.ErrNoUser) && s.users.Err() != nil {
			reason = "the users file cannot be read"
		}
		return "", passwordRefusal{reason: reason}
	}
	return credPrint(file.Users[name].Password), passwordRefusal{}
}

// handleSignOut is POST /api/signout: it ends the session the cookie
// names (revoked here, not just forgotten by the browser) and clears the
// cookie.
func (s *Server) handleSignOut(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !s.ownOrigin(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "sign-out is accepted only from this controller's own page (the request's Origin must be this controller's scheme and host)"})
		return
	}
	ended := false
	cookie := s.cookieName(r)
	if s.sessions != nil {
		for _, c := range r.CookiesNamed(cookie) {
			se, ok := s.sessions.signOut(c.Value)
			if !ok {
				continue
			}
			ended = true
			s.audit.record(audit.Entry{User: se.key(), Source: clientSource(r), Action: "auth.signout",
				From: s.clientAddr(r), Session: se.hash, Result: audit.ResultOK})
		}
	}
	http.SetCookie(w, &http.Cookie{Name: cookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: s.secure(r), SameSite: http.SameSiteStrictMode})
	if isForm(r) {
		http.Redirect(w, r, "/signin", http.StatusSeeOther)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"signedOut": ended})
}

// handleSignInPage is GET /signin.
func (s *Server) handleSignInPage(w http.ResponseWriter, r *http.Request) {
	s.signInPage(w, r, http.StatusOK, "", safeNext(r.URL.Query().Get("next")))
}

// signInPageData is what the sign-in page shows.
type signInPageData struct {
	Error  string
	Next   string
	Notice string // why sign-in will not work here, when it will not
	Leader string // on a standby: the leader's sign-in page, when configured
	// Form is whether the password form is shown: never where sign-in
	// would refuse it (a password typed over plain HTTP is on the wire
	// before any refusal), nor where no local user is granted anything.
	Form bool
	// Provider and OIDCStart are the OIDC sign-in's button, when the
	// controller has one: "Sign in with <Provider>", a link to OIDCStart.
	Provider  string
	OIDCStart string
}

func (s *Server) signInPage(w http.ResponseWriter, r *http.Request, status int, msg, next string) {
	d := signInPageData{Error: msg, Next: next}
	switch leader, standby := s.standbyLeader(); {
	case s.sessions == nil:
		d.Notice = "Sign-in is not set up on this controller: its auth: block grants no user a role, or it has no users file."
	case !s.signInTransport(r):
		d.Notice = "Sign-in needs HTTPS. Reach this controller over https:// (server.tls, or a TLS proxy named in server.trusted-proxies)."
	case standby:
		d.Notice = "This controller is the standby. Open the leader to sign in."
		if leader != "" {
			d.Leader = leader + "/signin"
		}
	default:
		d.Form = s.policy.HasUsers()
		if s.oidc != nil {
			d.Provider, d.OIDCStart = s.oidc.Provider(), oidcStartPath+"?next="+url.QueryEscape(next)
		}
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	// The page runs no script and is never framed (no clickjacking of the
	// password box); it posts only to this controller.
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
	h.Set("X-Frame-Options", "DENY")
	// Not no-referrer: under it a browser sends "Origin: null" with the
	// form's POST, which sign-in refuses.
	h.Set("Referrer-Policy", "same-origin")
	w.WriteHeader(status)
	signInTemplate.Execute(w, d)
}

var signInTemplate = template.Must(template.New("signin").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Sign in · nautilus</title>
<style>
:root{color-scheme:light dark;font-family:system-ui,sans-serif}
body{margin:0;min-height:100vh;display:grid;place-items:center;background:Canvas;color:CanvasText}
main{width:min(22rem,100% - 2rem)}
h1{font-size:1.25rem;margin:0 0 1rem}
label{display:block;margin:.75rem 0 .25rem;font-size:.9rem}
input{box-sizing:border-box;width:100%;padding:.5rem;font:inherit}
button{margin-top:1rem;width:100%;padding:.6rem;font:inherit;cursor:pointer}
p{margin:.75rem 0;font-size:.9rem}
.err{color:#c62828}
.or{text-align:center;opacity:.7}
.btn{display:block;box-sizing:border-box;margin-top:1rem;width:100%;padding:.6rem;text-align:center;border:1px solid currentColor;border-radius:2px;color:inherit;text-decoration:none}
</style></head>
<body><main>
<h1>Sign in</h1>
{{if .Notice}}<p>{{.Notice}}{{if .Leader}} <a href="{{.Leader}}">Go to the leader</a>{{end}}</p>{{end}}
{{if .Error}}<p class="err" role="alert">{{.Error}}</p>{{end}}
{{if .Form}}<form method="post" action="/api/signin">
<input type="hidden" name="next" value="{{.Next}}">
<label for="name">User name</label>
<input id="name" name="name" autocomplete="username" autocapitalize="none" spellcheck="false" required autofocus>
<label for="password">Password</label>
<input id="password" name="password" type="password" autocomplete="current-password" required>
<button type="submit">Sign in</button>
</form>{{end}}
{{if .OIDCStart}}{{if .Form}}<p class="or">or</p>{{end}}<a class="btn" href="{{.OIDCStart}}">Sign in with {{.Provider}}</a>{{end}}
</main></body></html>
`))
