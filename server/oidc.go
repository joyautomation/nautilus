package server

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/joyautomation/nautilus/audit"
	"github.com/joyautomation/nautilus/auth"
)

// OIDC sign-in (#708 PR 6a, docs/design/identity-audit.md §3): a browser
// signs in at the auth: block's OpenID Connect provider by the
// authorization-code flow with PKCE. GET /auth/oidc/start sends it to the
// provider; GET /auth/oidc/callback takes the provider's answer, and on
// success starts a session as oidc:<name>.
//
// The name (the username claim) is a label a provider lets people change,
// so it never decides alone: every name is pinned, on its first sign-in,
// to the account it came from, (issuer, sub), in the state directory
// (users.PinsName). A sign-in by another account under a pinned name is
// refused until naut users unpin. Groups grant by id, never by display
// name.
//
// The callback answers 200 with a page that moves the browser on, not a
// redirect: the provider sent the browser here cross-site, and a browser
// does not send a SameSite=Strict cookie set on a cross-site redirect
// chain with the request it redirects to. A page of this origin that
// navigates is a same-site navigation, so the session cookie goes with it.

// OIDC is the seam to an OpenID Connect provider: naut run hands the
// server auth/oidc's client, so the server links no OIDC module. The
// server keeps the flow's state (state, nonce, PKCE verifier), the
// sessions and every decision; an OIDC only talks to the provider.
type OIDC interface {
	// Provider is the provider's name for the sign-in page's button
	// ("Sign in with <Provider>").
	Provider() string
	// AuthCodeURL is the provider's authorization endpoint for one
	// sign-in: client id, redirect URI, scopes, state, nonce, and the
	// S256 challenge of the verifier. It may need the provider's
	// discovery document, so it can fail (ErrOIDCUnreachable).
	AuthCodeURL(ctx context.Context, req OIDCAuthRequest) (string, error)
	// Exchange redeems code at the token endpoint, with the verifier and
	// the redirect URI the sign-in started with, and verifies the ID
	// token: its signature against the provider's keys, its issuer, its
	// audience (the client id) and its expiry. It does not check the
	// nonce; the server does, against the one it sent.
	Exchange(ctx context.Context, redirectURI, code, verifier string) (OIDCIdentity, error)
}

// OIDCAuthRequest is one sign-in's parameters for OIDC.AuthCodeURL.
type OIDCAuthRequest struct {
	RedirectURI string
	State       string
	Nonce       string
	Verifier    string // the PKCE verifier; the URL carries its S256 challenge
}

// OIDCIdentity is what a verified ID token says.
type OIDCIdentity struct {
	Issuer  string
	Subject string // sub: the account, stable at its issuer
	// Name is the username claim's value as given, and NameClaim the
	// claim it was read from (for a refusal that names it).
	Name      string
	NameClaim string
	Nonce     string
	// Groups are the groups claim's ids. GroupsOverage says the provider
	// left them out for having too many (Entra's "overage": a pointer to
	// the Graph API instead of the list).
	Groups        []string
	GroupsOverage bool
	// AuthTime is auth_time, when the person last entered credentials at
	// the provider; zero when the token has none. An OIDC session holds the
	// elevated permissions only within auth.elevation of it.
	AuthTime time.Time
}

// ErrOIDCUnreachable marks an OIDC error that is the provider being out
// of reach (discovery, keys or the token endpoint), not a refusal: the
// sign-in page says to try again, and that local users still can.
var ErrOIDCUnreachable = errors.New("the OIDC provider cannot be reached")

// The OIDC routes.
const (
	oidcStartPath    = "/auth/oidc/start"
	oidcCallbackPath = "/auth/oidc/callback"
)

// oidcClockSkew is how far ahead of this controller's clock an ID token's
// auth_time may be and still count (as now): the provider's clock and
// ours differ a little. Further ahead, it does not count at all.
const oidcClockSkew = 2 * time.Minute

// oidcFlowLife is how long a started sign-in may take at the provider.
const oidcFlowLife = 10 * time.Minute

// maxOIDCFlows bounds the sign-ins under way: start is anonymous, so a
// client must not grow the table without end (its address is throttled
// too).
const maxOIDCFlows = 4096

// The provider's request and timeouts: a slow provider must not hold a
// request for long.
var (
	oidcStartTimeout    = 10 * time.Second
	oidcExchangeTimeout = 15 * time.Second
)

// The cookie that binds a sign-in under way to the browser that started
// it: HttpOnly, SameSite=Lax (the callback is a cross-site top-level GET,
// to which a Strict cookie is not sent), ten minutes, used once.
const (
	oidcSecureCookie = "__Host-nautilus_oidc"
	oidcPlainCookie  = "nautilus_oidc"
)

// Limits on what the groups claim may hold.
const (
	maxOIDCGroups  = 500
	maxOIDCGroupID = 256
)

// oidcFlow is one sign-in under way, kept here until its callback, by its
// state.
type oidcFlow struct {
	nonce    string
	verifier string
	redirect string // the redirect URI the sign-in started with
	next     string // where the browser goes afterwards
	binding  string // hex SHA-256 of the binding cookie's value
	at       time.Time
}

type oidcFlows struct {
	now func() time.Time
	mu  sync.Mutex
	m   map[string]oidcFlow
}

func newOIDCFlows() *oidcFlows { return &oidcFlows{now: time.Now, m: map[string]oidcFlow{}} }

// put keeps a sign-in under way; false when too many are.
func (f *oidcFlows) put(state string, fl oidcFlow) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := f.now()
	if len(f.m) >= maxOIDCFlows {
		for k, o := range f.m {
			if now.Sub(o.at) >= oidcFlowLife {
				delete(f.m, k)
			}
		}
		if len(f.m) >= maxOIDCFlows {
			return false
		}
	}
	fl.at = now
	f.m[state] = fl
	return true
}

// take hands over the sign-in state names, once: it is gone afterwards,
// whatever the callback then decides. An expired one is no sign-in.
func (f *oidcFlows) take(state string) (oidcFlow, bool) {
	if state == "" {
		return oidcFlow{}, false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	fl, ok := f.m[state]
	if !ok {
		return oidcFlow{}, false
	}
	delete(f.m, state)
	if f.now().Sub(fl.at) >= oidcFlowLife {
		return oidcFlow{}, false
	}
	return fl, true
}

func hashHex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func (s *Server) oidcCookieName(r *http.Request) string {
	if s.secure(r) {
		return oidcSecureCookie
	}
	return oidcPlainCookie
}

func (s *Server) clearOIDCCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: s.oidcCookieName(r), Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: s.secure(r), SameSite: http.SameSiteLaxMode})
}

// oidcRedirectURI is the redirect URI for a sign-in started by r:
// auth.oidc.redirect-url when set, else this controller's callback at the
// scheme and Host r came by, but only for a Host the Host rule trusts
// (localhost, an IP address, NAUTILUS_ALLOWED_HOSTS): a Host is whatever
// the client says, and the provider's check of its registered URIs is a
// backstop, not the guard.
func (s *Server) oidcRedirectURI(r *http.Request) (string, bool) {
	if s.oidcRedirect != "" {
		return s.oidcRedirect, true
	}
	if r.Host == "" || !hostTrusted(s.allowHosts, r.Host) {
		return "", false
	}
	scheme := "http"
	if s.secure(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host + oidcCallbackPath, true
}

// OIDCRedirectURIs are the redirect URIs to register at the provider, for
// naut run to say at the start: auth.oidc.redirect-url when set, else one
// per name in the allowed hosts, with the scheme (by whether the
// controller serves TLS) and the port it listens on, unless that is the
// scheme's default: the URI a browser's sign-in then sends, exactly.
// Empty when the controller has no OIDC sign-in.
func (s *Server) OIDCRedirectURIs(tls bool, port string) []string {
	if s.oidc == nil {
		return nil
	}
	if s.oidcRedirect != "" {
		return []string{s.oidcRedirect}
	}
	scheme := "http"
	if tls {
		scheme = "https"
	}
	if port == "" || scheme == "https" && port == "443" || scheme == "http" && port == "80" {
		port = ""
	} else {
		port = ":" + port
	}
	var out []string
	for h := range s.allowHosts {
		out = append(out, scheme+"://"+h+port+oidcCallbackPath)
	}
	return out
}

// handleOIDCStart is GET /auth/oidc/start?next=: it sends the browser to
// the provider.
func (s *Server) handleOIDCStart(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	next := safeNext(r.URL.Query().Get("next"))
	if s.oidc == nil {
		s.signInPage(w, r, http.StatusNotFound, "OIDC sign-in is not set up on this controller (auth.oidc).", next)
		return
	}
	e := audit.Entry{User: "anonymous", Source: clientSource(r), Action: "auth.signin", From: s.clientAddr(r), Result: audit.ResultRefused}
	refuse := func(status int, msg, reason string) {
		e.Detail = map[string]any{"status": status, "reason": reason, "method": "oidc"}
		s.audit.record(e)
		s.signInPage(w, r, status, msg, next)
	}
	if _, standby := s.standbyLeader(); standby {
		s.signInPage(w, r, http.StatusServiceUnavailable, "", next)
		return
	}
	if !s.signInTransport(r) {
		refuse(http.StatusForbidden, "OIDC sign-in needs HTTPS: set server.tls (naut auth init writes server.tls: self-signed), or name the TLS proxy in server.trusted-proxies (plain HTTP is allowed only from this machine's loopback)", "not over TLS")
		return
	}
	if ok, _ := s.signin.oidc.take(addrKey(s.clientAddr(r))); !ok {
		refuse(http.StatusTooManyRequests, "too many sign-in attempts from this address: try again in a minute", "throttled")
		return
	}
	redirect, ok := s.oidcRedirectURI(r)
	if !ok {
		refuse(http.StatusForbidden, "OIDC sign-in from this address needs a host name the controller trusts: add it to NAUTILUS_ALLOWED_HOSTS (or server.allowed-hosts), or set auth.oidc.redirect-url", "untrusted host")
		return
	}
	// With redirect-url set, the sign-in must start at that origin: the
	// binding cookie is set here and read at the callback.
	if u, err := url.Parse(redirect); err == nil && s.oidcRedirect != "" {
		scheme := "http"
		if s.secure(r) {
			scheme = "https"
		}
		if u.Scheme != scheme || !strings.EqualFold(withPort(u.Host, u.Scheme), withPort(r.Host, scheme)) {
			http.Redirect(w, r, u.Scheme+"://"+u.Host+oidcStartPath+"?next="+url.QueryEscape(next), http.StatusSeeOther)
			return
		}
	}
	state, binding := randomID(), randomID()
	fl := oidcFlow{nonce: randomID(), verifier: randomID(), redirect: redirect, next: next, binding: hashHex(binding)}
	if !s.oidcFlows.put(state, fl) {
		refuse(http.StatusServiceUnavailable, "too many sign-ins are under way: try again in a few minutes", "busy")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), oidcStartTimeout)
	defer cancel()
	to, err := s.oidc.AuthCodeURL(ctx, OIDCAuthRequest{RedirectURI: redirect, State: state, Nonce: fl.nonce, Verifier: fl.verifier})
	if err != nil {
		s.oidcFlows.take(state)
		refuse(http.StatusServiceUnavailable, s.oidcUnreachable(), err.Error())
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     s.oidcCookieName(r),
		Value:    binding,
		Path:     "/",
		MaxAge:   int(oidcFlowLife.Seconds()),
		HttpOnly: true,
		Secure:   s.secure(r),
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, to, http.StatusSeeOther)
}

func (s *Server) oidcUnreachable() string {
	msg := "the sign-in provider (" + s.oidc.Provider() + ") cannot be reached from this controller: try again shortly"
	if s.policy.HasUsers() {
		msg += "; local users can still sign in with a password"
	}
	return msg
}

// handleOIDCCallback is GET /auth/oidc/callback: the provider's answer.
func (s *Server) handleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	// The URL carries the code: no page this one leads to may see it.
	w.Header().Set("Referrer-Policy", "no-referrer")
	if s.oidc == nil {
		s.signInPage(w, r, http.StatusNotFound, "OIDC sign-in is not set up on this controller (auth.oidc).", "/")
		return
	}
	e := audit.Entry{User: "anonymous", Source: clientSource(r), Action: "auth.signin", From: s.clientAddr(r), Result: audit.ResultRefused}
	next := "/"
	detail := map[string]any{"method": "oidc"}
	refuse := func(status int, msg, reason string) {
		detail["status"], detail["reason"] = status, reason
		e.Detail = detail
		s.audit.record(e)
		s.clearOIDCCookie(w, r)
		s.signInPage(w, r, status, msg, next)
	}
	if !s.signInTransport(r) {
		refuse(http.StatusForbidden, "OIDC sign-in needs HTTPS: set server.tls, or name the TLS proxy in server.trusted-proxies", "not over TLS")
		return
	}
	q := r.URL.Query()
	fl, ok := s.oidcFlows.take(q.Get("state"))
	if !ok {
		refuse(http.StatusBadRequest, "this sign-in has expired or was already used: start it again", "unknown or expired state")
		return
	}
	next = fl.next
	bound := false
	for _, c := range r.CookiesNamed(s.oidcCookieName(r)) {
		if subtle.ConstantTimeCompare([]byte(hashHex(c.Value)), []byte(fl.binding)) == 1 {
			bound = true
		}
	}
	if !bound {
		refuse(http.StatusBadRequest, "this sign-in was started in another browser, or its cookie was blocked: start it again here", "binding cookie missing or wrong")
		return
	}
	if code := q.Get("error"); code != "" {
		refuse(http.StatusUnauthorized, "the sign-in provider refused the sign-in ("+cleanCode(code)+")", "provider error: "+cleanCode(code))
		return
	}
	if q.Get("code") == "" {
		refuse(http.StatusBadRequest, "the provider's answer carries no code: start the sign-in again", "no code")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), oidcExchangeTimeout)
	defer cancel()
	id, err := s.oidc.Exchange(ctx, fl.redirect, q.Get("code"), fl.verifier)
	if err != nil {
		if errors.Is(err, ErrOIDCUnreachable) {
			refuse(http.StatusServiceUnavailable, s.oidcUnreachable(), err.Error())
			return
		}
		refuse(http.StatusUnauthorized, "the sign-in provider's answer did not verify: start the sign-in again", err.Error())
		return
	}
	if id.Nonce == "" || subtle.ConstantTimeCompare([]byte(id.Nonce), []byte(fl.nonce)) != 1 {
		refuse(http.StatusUnauthorized, "the sign-in provider's answer did not verify: start the sign-in again", "nonce mismatch")
		return
	}
	if id.Issuer == "" || id.Subject == "" {
		refuse(http.StatusUnauthorized, "the sign-in provider's answer did not verify: start the sign-in again", "no issuer or no subject")
		return
	}
	detail["issuer"], detail["subject"] = id.Issuer, id.Subject
	name := strings.ToLower(strings.TrimSpace(id.Name))
	if name == "" {
		refuse(http.StatusForbidden, "the provider's ID token has no "+id.NameClaim+" claim to name you by: set auth.oidc.username-claim to a claim it sends (preferred_username, email)", "no username claim "+id.NameClaim)
		return
	}
	if err := auth.ValidOIDCName(name); err != nil {
		refuse(http.StatusForbidden, "the provider names you in a form this controller cannot use ("+err.Error()+"): set auth.oidc.username-claim to a claim like email", "username claim: "+err.Error())
		return
	}
	e.Target = "oidc:" + name
	if reason := checkGroups(id.Groups); reason != "" {
		refuse(http.StatusForbidden, "the provider's groups claim cannot be used ("+reason+")", "groups claim: "+reason)
		return
	}
	pr := auth.OIDC(name, id.Groups)
	detail["groups"] = len(id.Groups)
	if !s.policy.HasGrants(pr) {
		if id.GroupsOverage {
			detail["overage"] = true
			refuse(http.StatusForbidden, "signed in as "+name+", but the provider left your groups out of the token (you are in too many: Entra's groups overage). Have the app registration emit only the groups assigned to the application (Token configuration, groups claim), or grant the name under auth.oidc.users", "groups overage")
			return
		}
		msg := "signed in at " + s.oidc.Provider() + " as " + name + ", but none of your groups map to a role here: add one of their ids under auth.oidc.groups, or the name under auth.oidc.users"
		if len(id.Groups) == 0 {
			msg = "signed in at " + s.oidc.Provider() + " as " + name + ", but the token carries no groups and the name has no grant: add a group id under auth.oidc.groups (and have the provider send the groups claim), or the name under auth.oidc.users"
		} else {
			msg += " (your group ids: " + strings.Join(firstN(id.Groups, 20), ", ") + ")"
		}
		refuse(http.StatusForbidden, msg, "no grant")
		return
	}
	pin, fresh, err := s.users.PinFirst(name, id.Issuer, id.Subject, s.sessions.clock())
	if err != nil {
		refuse(http.StatusServiceUnavailable, "the controller cannot record which account "+name+" is ("+err.Error()+"): see its state directory", "pin: "+err.Error())
		return
	}
	if !pin.Same(id.Issuer, id.Subject) {
		refuse(http.StatusForbidden, "oidc:"+name+" is pinned to another account at this provider; if this is a re-created account, run naut users unpin "+name+" on the controller", "pinned to another account")
		return
	}
	// A fresh id at every sign-in; the session the browser held, if any,
	// ends (no fixation, and none left behind).
	cookie := s.cookieName(r)
	for _, c := range r.CookiesNamed(cookie) {
		s.sessions.end(c.Value)
	}
	// Fresh from auth_time (read from the verified token): when the person
	// last entered credentials at the provider. Within oidcClockSkew ahead
	// it counts as now (start clamps it); further ahead, or absent, the
	// session holds no elevated permission.
	authAt := id.AuthTime
	if authAt.After(s.sessions.clock().Add(oidcClockSkew)) {
		detail["authTimeAhead"] = true
		authAt = time.Time{}
	}
	sid, se := s.sessions.start(session{user: name, oidc: true, issuer: id.Issuer, sub: id.Subject, groups: pr.Groups(), authAt: authAt})
	s.clearOIDCCookie(w, r)
	http.SetCookie(w, &http.Cookie{
		Name:     cookie,
		Value:    sid,
		Path:     "/",
		Expires:  se.expires,
		MaxAge:   int(se.expires.Sub(se.created).Seconds()),
		HttpOnly: true,
		Secure:   s.secure(r),
		SameSite: http.SameSiteStrictMode,
	})
	delete(detail, "status")
	detail["pinned"] = fresh
	if !id.AuthTime.IsZero() {
		detail["authTime"] = id.AuthTime.UTC().Format(time.RFC3339)
	}
	detail["expires"] = se.expires.UTC().Format(time.RFC3339)
	e.User, e.Target, e.Session, e.Result, e.Detail = "oidc:"+name, "", se.hash, audit.ResultOK, detail
	s.audit.record(e)
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'; base-uri 'none'")
	h.Set("X-Frame-Options", "DENY")
	w.WriteHeader(http.StatusOK)
	oidcHopTemplate.Execute(w, struct{ Name, Next string }{name, fl.next})
}

// checkGroups refuses a groups claim no grant could be read from safely:
// too many, an id too long, empty or holding a control character.
func checkGroups(groups []string) string {
	if len(groups) > maxOIDCGroups {
		return "more than " + strconv.Itoa(maxOIDCGroups) + " groups"
	}
	for _, g := range groups {
		if g == "" || len(g) > maxOIDCGroupID || strings.ContainsFunc(g, func(r rune) bool { return r < ' ' || r == 0x7f }) {
			return "a group id that is empty, too long or holds a control character"
		}
	}
	return ""
}

func firstN(s []string, n int) []string {
	if len(s) > n {
		return append(append([]string{}, s[:n]...), "…")
	}
	return s
}

// cleanCode keeps a provider's error code fit to show and journal: an
// OAuth error code is letters and '_'.
func cleanCode(code string) string {
	var b strings.Builder
	for _, r := range code {
		if b.Len() >= 64 {
			break
		}
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "unknown_error"
	}
	return b.String()
}

// oidcHopTemplate is the callback's answer: a page of this origin that
// moves the browser on (a same-site navigation, which carries the session
// cookie), without script.
var oidcHopTemplate = template.Must(template.New("hop").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<meta http-equiv="refresh" content="0;url={{.Next}}">
<title>Signed in · nautilus</title>
<style>:root{color-scheme:light dark;font-family:system-ui,sans-serif}body{margin:0;min-height:100vh;display:grid;place-items:center;background:Canvas;color:CanvasText}</style>
</head><body><main><p>Signed in as {{.Name}}. <a href="{{.Next}}">Continue</a></p></main></body></html>
`))
