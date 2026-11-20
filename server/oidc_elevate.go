package server

import (
	"context"
	"crypto/subtle"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/joyautomation/nautilus/audit"
	"github.com/joyautomation/nautilus/internal/oidcseam"
)

// Elevation at the OIDC provider (#708 PR 6b, docs/design/identity-audit.md
// §2): an OIDC session holds the elevated permissions only within
// auth.elevation of when the person last entered credentials at the
// provider. There is no password to confirm here, so the person confirms
// there: GET /auth/oidc/start?elevate=1 sends the browser to the provider
// with prompt=login and max_age=0, and the answer refreshes THAT session
// (it starts none).
//
// It runs in two steps, because the provider sends the browser back by a
// cross-site navigation, which carries no SameSite=Strict session cookie:
//
//  1. GET /auth/oidc/callback (cross-site): the flow (one use, bound to the
//     browser by its cookie, to the session that started it by its hash,
//     and to the nonce) is checked, the code redeemed, and the answer
//     judged: the same account (issuer, sub) as the session's, the same
//     groups, and an auth_time no older than the flow's start. A verified
//     answer is parked under a one-use ticket and the browser is moved on
//     by a page of this origin (a same-site navigation).
//  2. GET /auth/oidc/elevated?t= (same-site): the session cookie now comes
//     along, and must name the session the flow was started from; only
//     then is its authAt set.
//
// The flow dies with its session: one signed out, expired or revoked at
// either step is refused. Every refusal is journalled as auth.elevate,
// method oidc, under the session's principal.

// oidcElevatedPath is the second step's route.
const oidcElevatedPath = "/auth/oidc/elevated"

// OIDCElevateURL is where a browser goes to confirm its OIDC sign-in at
// the provider. The HMI kit adds next= (the page to come back to).
const OIDCElevateURL = oidcStartPath + "?elevate=1"

// ElevationAtProvider begins the message of a refusal for want of a fresh
// OIDC sign-in: the session holds the permission, but the person last
// entered credentials at the provider longer ago than auth.elevation. Its
// 401 carries "elevate": "oidc" and "url": OIDCElevateURL (WriteRefusal);
// the HMI kit sends the page there.
const ElevationAtProvider = "confirm your sign-in at the provider to do this"

// oidcTicketLife is how long a verified confirmation waits for its second
// step: the hop page moves on at once.
const oidcTicketLife = time.Minute

// oidcTicket is a confirmation verified at the callback, waiting for the
// same-site step that applies it.
type oidcTicket struct {
	session   string // the session's hash
	principal string
	state     string // the flow's state: its binding cookie's name
	binding   string
	issuer    string
	sub       string
	authAt    time.Time
	authTime  time.Time // auth_time as the token said
	next      string
	at        time.Time
}

type oidcTickets struct {
	now func() time.Time
	mu  sync.Mutex
	m   map[string]oidcTicket
}

func newOIDCTickets() *oidcTickets { return &oidcTickets{now: time.Now, m: map[string]oidcTicket{}} }

// put parks t under a fresh ticket; "" when the table is full.
func (f *oidcTickets) put(t oidcTicket) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := f.now()
	for k, o := range f.m {
		if now.Sub(o.at) >= oidcTicketLife {
			delete(f.m, k)
		}
	}
	if len(f.m) >= maxOIDCFlows {
		return ""
	}
	id := randomID()
	t.at = now
	f.m[id] = t
	return id
}

// take hands over the ticket, once; an expired one is none.
func (f *oidcTickets) take(id string) (oidcTicket, bool) {
	if id == "" {
		return oidcTicket{}, false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.m[id]
	if !ok {
		return oidcTicket{}, false
	}
	delete(f.m, id)
	if f.now().Sub(t.at) >= oidcTicketLife {
		return oidcTicket{}, false
	}
	return t, true
}

// navigationRefusal is the refusal of a request to start a flow that is
// not a browser opening a page: an image or a fetch another page makes
// (Sec-Fetch-Mode no-cors, cors) would spend the flows under way, and a
// frame (Sec-Fetch-Dest iframe) would hide the provider's page inside
// another site's. A client that sends no such header (an older browser, a
// script) is let through. "" when allowed.
func navigationRefusal(r *http.Request) (msg, reason string) {
	if m := r.Header.Get("Sec-Fetch-Mode"); m != "" && m != "navigate" {
		return "a sign-in starts only from a link the browser opens (Sec-Fetch-Mode: navigate)", "not a navigation"
	}
	if d := r.Header.Get("Sec-Fetch-Dest"); d != "" && d != "document" {
		return "a sign-in starts only in the browser's own window, not inside a frame (Sec-Fetch-Dest: document)", "not a document"
	}
	return "", ""
}

// sameSiteRequest reports a request this controller's own pages made (or
// the person typed): Sec-Fetch-Site same-origin or none, when the browser
// says. A client that does not say is let through: the session cookie is
// SameSite=Strict, so a cross-site navigation carries none anyway.
func sameSiteRequest(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "", "same-origin", "none":
		return true
	}
	return false
}

// liveSession is the session hash names, still live and still standing
// (sessionStands): a flow dies with its session.
func (s *Server) liveSession(hash string) (session, bool) {
	se, ok := s.sessions.byHashLive(hash)
	if !ok || !s.sessionStands(se) {
		return session{}, false
	}
	return se, true
}

// sameGroups reports whether a and b hold the same group ids, in any
// order.
func sameGroups(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	x, y := append([]string{}, a...), append([]string{}, b...)
	sort.Strings(x)
	sort.Strings(y)
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}

// oidcElevateStart is GET /auth/oidc/start?elevate=1&next=: an OIDC
// session's confirmation at the provider.
func (s *Server) oidcElevateStart(w http.ResponseWriter, r *http.Request, next string) {
	a := s.resolveActor(r)
	e := audit.Entry{User: a.pr.String(), Source: clientSource(r), Action: "auth.elevate", From: s.clientAddr(r), Result: audit.ResultRefused}
	if a.session != nil {
		e.Session = a.session.hash
	}
	refuse := func(status int, msg, reason string) {
		e.Detail = map[string]any{"status": status, "reason": reason, "method": "oidc"}
		s.audit.record(e)
		s.elevatePage(w, status, msg, next)
	}
	if msg, reason := navigationRefusal(r); msg != "" {
		refuse(http.StatusForbidden, msg, reason)
		return
	}
	// Only this controller's pages start a confirmation: another site's
	// link must not send a signed-in person to the provider's login.
	if !sameSiteRequest(r) {
		refuse(http.StatusForbidden, "a confirmation at the provider starts only from this controller's own pages", "cross-site")
		return
	}
	if !s.signInTransport(r) {
		refuse(http.StatusForbidden, "OIDC sign-in needs HTTPS: set server.tls, or name the TLS proxy in server.trusted-proxies (plain HTTP is allowed only from this machine's loopback)", "not over TLS")
		return
	}
	if presentedToken(r) != "" {
		refuse(http.StatusBadRequest, "a token always counts as elevated: a confirmation is for a signed-in browser", "bearer credential")
		return
	}
	if a.session == nil {
		// Never a plain sign-in instead: a link from another site carries
		// no sign-in, and must not turn into one.
		refuse(http.StatusUnauthorized, "not signed in on this browser: sign in first (/signin), then do the action again", "no session")
		return
	}
	se := *a.session
	if !se.oidc {
		refuse(http.StatusBadRequest, "this is a local user's sign-in: confirm your password where the HMI asks for it (POST /api/elevate)", "not an oidc session")
		return
	}
	if !s.holdsElevated(a) {
		refuse(http.StatusForbidden, "nothing to confirm: "+se.key()+" holds none of the elevated permissions", "no elevated permission")
		return
	}
	redirect, ok := s.oidcRedirectURI(r)
	if !ok {
		refuse(http.StatusForbidden, "OIDC sign-in from this address needs a host name the controller trusts: add it to NAUTILUS_ALLOWED_HOSTS (or server.allowed-hosts), or set auth.oidc.redirect-url", "untrusted host")
		return
	}
	// With redirect-url set, the provider sends the browser back there:
	// a confirmation started at another origin would come back where this
	// browser's sign-in cookie does not exist.
	if s.oidcRedirect != "" {
		u, err := url.Parse(redirect)
		scheme := "http"
		if s.secure(r) {
			scheme = "https"
		}
		if err != nil || u.Scheme != scheme || !strings.EqualFold(withPort(u.Host, u.Scheme), withPort(r.Host, u.Scheme)) {
			origin := redirect
			if err == nil {
				origin = u.Scheme + "://" + u.Host
			}
			refuse(http.StatusForbidden, "this controller's provider sends the browser back to "+origin+" (auth.oidc.redirect-url): open the HMI there, sign in, and do the action again", "not at redirect-url's origin")
			return
		}
	}
	// Throttled by the session, as a password's re-authentication is: only
	// the cookie's holder can spend its bucket.
	if ok, _ := s.signin.sessions.take(se.hash); !ok {
		refuse(http.StatusTooManyRequests, "too many confirmations from this sign-in: try again in a minute", "throttled")
		return
	}
	state, binding := randomID(), randomID()
	fl := oidcFlow{
		nonce: randomID(), verifier: randomID(), redirect: redirect, next: next, binding: hashHex(binding),
		// Capped per session, not per address: a sign-in started by
		// anyone at the same address does not evict it.
		addr:    "session:" + se.hash,
		elevate: true, session: se.hash, principal: se.key(), issuer: se.issuer, sub: se.sub, groups: se.groups,
	}
	if !s.oidcFlows.put(state, fl) {
		refuse(http.StatusServiceUnavailable, "too many sign-ins are under way: try again in a few minutes", "busy")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), oidcStartTimeout)
	defer cancel()
	to, err := s.oidc.AuthCodeURL(ctx, oidcseam.AuthRequest{RedirectURI: redirect, State: state, Nonce: fl.nonce, Verifier: fl.verifier, Login: true})
	if err != nil {
		s.oidcFlows.take(state)
		refuse(http.StatusServiceUnavailable, s.oidcUnreachable(), err.Error())
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: s.oidcCookieName(r, state), Value: binding, Path: "/", MaxAge: int(oidcFlowLife.Seconds()),
		HttpOnly: true, Secure: s.secure(r), SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, to, http.StatusSeeOther)
}

// oidcElevateCallback is the callback of a confirmation (step 1): fl was
// taken by its state.
func (s *Server) oidcElevateCallback(w http.ResponseWriter, r *http.Request, state string, fl oidcFlow) {
	e := audit.Entry{User: fl.principal, Session: fl.session, Source: clientSource(r), Action: "auth.elevate", From: s.clientAddr(r), Result: audit.ResultRefused}
	detail := map[string]any{"method": "oidc"}
	refuse := func(status int, msg, reason string) {
		detail["status"], detail["reason"] = status, reason
		e.Detail = detail
		s.audit.record(e)
		s.clearOIDCCookie(w, r, state)
		s.elevatePage(w, status, msg, fl.next)
	}
	if !s.oidcBound(r, state, fl.binding) {
		refuse(http.StatusBadRequest, "this confirmation was started in another browser, or its cookie was blocked: do the action again", "binding cookie missing or wrong")
		return
	}
	if _, ok := s.liveSession(fl.session); !ok {
		refuse(http.StatusUnauthorized, "the sign-in this confirmation was for has ended (signed out, expired or revoked): sign in again (/signin)", "session ended")
		return
	}
	q := r.URL.Query()
	if code := q.Get("error"); code != "" {
		refuse(http.StatusUnauthorized, "the provider did not confirm your sign-in ("+cleanCode(code)+"): you are still signed in, without the elevated permissions", "provider error: "+cleanCode(code))
		return
	}
	if q.Get("code") == "" {
		refuse(http.StatusBadRequest, "the provider's answer carries no code: do the action again", "no code")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), oidcExchangeTimeout)
	defer cancel()
	id, err := s.oidc.Exchange(ctx, fl.redirect, q.Get("code"), fl.verifier)
	if err != nil {
		if errors.Is(err, oidcseam.ErrUnreachable) {
			refuse(http.StatusServiceUnavailable, s.oidcUnreachable(), err.Error())
			return
		}
		refuse(http.StatusUnauthorized, "the provider's answer did not verify: do the action again", err.Error())
		return
	}
	if id.Nonce == "" || subtle.ConstantTimeCompare([]byte(id.Nonce), []byte(fl.nonce)) != 1 {
		refuse(http.StatusUnauthorized, "the provider's answer did not verify: do the action again", "nonce mismatch")
		return
	}
	if id.Issuer != fl.issuer || id.Subject != fl.sub {
		detail["issuer"], detail["subject"] = id.Issuer, id.Subject
		refuse(http.StatusForbidden, "the credentials entered at "+s.oidc.Provider()+" are another account's than this sign-in's ("+fl.principal+"): confirm with the account you signed in with, or sign out and sign in as the other", "another account")
		return
	}
	// OIDC Core §3.1.2.1: with max_age asked for, auth_time is REQUIRED.
	// Its absence is the provider's error, never "not fresh" in silence.
	if id.AuthTime.IsZero() {
		refuse(http.StatusBadGateway, "the provider's ID token carries no auth_time, which OpenID Connect requires when a re-authentication is asked for (max_age): have "+s.oidc.Provider()+" put auth_time in the ID token (on Entra, add the optional claim auth_time to the app registration's ID token)", "no auth_time")
		return
	}
	detail["authTime"] = id.AuthTime.UTC().Format(time.RFC3339)
	now := s.sessions.clock()
	if id.AuthTime.After(now.Add(oidcClockSkew)) {
		refuse(http.StatusUnauthorized, "the provider's auth_time is ahead of this controller's clock by more than "+shortDuration(oidcClockSkew)+": check both clocks (NTP), then do the action again", "auth_time ahead")
		return
	}
	if id.AuthTime.Before(fl.at.Add(-oidcClockSkew)) {
		refuse(http.StatusUnauthorized, s.oidc.Provider()+" did not ask for your credentials again (its auth_time is from before this confirmation began): it ignored prompt=login and max_age=0. Check the client's settings at the provider", "auth_time before start")
		return
	}
	if !sameGroups(id.Groups, fl.groups) {
		detail["groups"] = len(id.Groups)
		refuse(http.StatusForbidden, "your groups at "+s.oidc.Provider()+" have changed since you signed in: sign in again (/signin)", "groups changed")
		return
	}
	authAt := id.AuthTime
	if authAt.After(now) {
		authAt = now
	}
	t := s.oidcTickets.put(oidcTicket{session: fl.session, principal: fl.principal, state: state, binding: fl.binding,
		issuer: fl.issuer, sub: fl.sub, authAt: authAt, authTime: id.AuthTime, next: fl.next})
	if t == "" {
		refuse(http.StatusServiceUnavailable, "too many sign-ins are under way: try again in a few minutes", "busy")
		return
	}
	// The binding cookie stays for the second step.
	oidcHop(w, "Confirmed at "+s.oidc.Provider()+".", oidcElevatedPath+"?t="+url.QueryEscape(t))
}

// handleOIDCElevated is GET /auth/oidc/elevated?t=: a confirmation's
// second step, same-site, with the session cookie.
func (s *Server) handleOIDCElevated(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if s.oidc == nil {
		s.elevatePage(w, http.StatusNotFound, "OIDC sign-in is not set up on this controller (auth.oidc).", "/")
		return
	}
	t, ok := s.oidcTickets.take(r.URL.Query().Get("t"))
	e := audit.Entry{User: t.principal, Session: t.session, Source: clientSource(r), Action: "auth.elevate", From: s.clientAddr(r), Result: audit.ResultRefused}
	next := t.next
	if !ok {
		a := s.resolveActor(r)
		e.User, next = a.pr.String(), "/"
		if a.session != nil {
			e.Session = a.session.hash
		}
	}
	refuse := func(status int, msg, reason string) {
		e.Detail = map[string]any{"status": status, "reason": reason, "method": "oidc"}
		s.audit.record(e)
		if ok {
			s.clearOIDCCookie(w, r, t.state)
		}
		s.elevatePage(w, status, msg, next)
	}
	if !ok {
		refuse(http.StatusBadRequest, "this confirmation has expired or was already used: do the action again", "unknown or expired ticket")
		return
	}
	if !sameSiteRequest(r) {
		refuse(http.StatusForbidden, "a confirmation completes only on this controller's own page", "cross-site")
		return
	}
	if !s.signInTransport(r) {
		refuse(http.StatusForbidden, "OIDC sign-in needs HTTPS: set server.tls, or name the TLS proxy in server.trusted-proxies", "not over TLS")
		return
	}
	if !s.oidcBound(r, t.state, t.binding) {
		refuse(http.StatusBadRequest, "this confirmation was started in another browser, or its cookie was blocked: do the action again", "binding cookie missing or wrong")
		return
	}
	if _, live := s.liveSession(t.session); !live {
		refuse(http.StatusUnauthorized, "the sign-in this confirmation was for has ended (signed out, expired or revoked): sign in again (/signin)", "session ended")
		return
	}
	a := s.resolveActor(r)
	if a.session == nil || a.session.hash != t.session {
		refuse(http.StatusForbidden, "this confirmation was for another sign-in than this browser's: do the action again", "another session")
		return
	}
	se, ok := s.sessions.elevatedOIDC(t.session, t.issuer, t.sub, t.authAt)
	if !ok || !s.sessionStands(se) {
		refuse(http.StatusUnauthorized, "the sign-in this confirmation was for has ended (signed out, expired or revoked): sign in again (/signin)", "session ended")
		return
	}
	s.clearOIDCCookie(w, r, t.state)
	until := s.elevatedUntil(se).UTC()
	e.Result = audit.ResultOK
	e.Detail = map[string]any{"method": "oidc", "authTime": t.authTime.UTC().Format(time.RFC3339), "elevatedUntil": until.Format(time.RFC3339)}
	s.audit.record(e)
	http.Redirect(w, r, t.next, http.StatusSeeOther)
}

// oidcBound reports whether r carries the binding cookie of the flow
// state names.
func (s *Server) oidcBound(r *http.Request, state, binding string) bool {
	bound := false
	for _, c := range r.CookiesNamed(s.oidcCookieName(r, state)) {
		if subtle.ConstantTimeCompare([]byte(hashHex(c.Value)), []byte(binding)) == 1 {
			bound = true
		}
	}
	return bound
}

// elevatePage is a confirmation's refusal, as a page: the browser is
// still signed in, and goes back to where it was.
func (s *Server) elevatePage(w http.ResponseWriter, status int, msg, next string) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'; base-uri 'none'")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(status)
	elevateTemplate.Execute(w, struct{ Error, Next string }{msg, next})
}

var elevateTemplate = template.Must(template.New("elevate").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Not confirmed · nautilus</title>
<style>:root{color-scheme:light dark;font-family:system-ui,sans-serif}body{margin:0;min-height:100vh;display:grid;place-items:center;background:Canvas;color:CanvasText}main{width:min(28rem,100% - 2rem)}h1{font-size:1.25rem}.err{color:#c62828}</style>
</head><body><main><h1>Not confirmed</h1>
<p class="err" role="alert">{{.Error}}</p>
<p><a href="{{.Next}}">Back</a></p></main></body></html>
`))
