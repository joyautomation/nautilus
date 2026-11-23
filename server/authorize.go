package server

import (
	"context"
	"crypto/subtle"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/joyautomation/nautilus/audit"
	"github.com/joyautomation/nautilus/auth"
	"github.com/joyautomation/nautilus/auth/users"
)

// The permission names (#708, docs/design/identity-audit.md §2): what
// authorize is asked whether a request may do. A route asks for the one
// its action needs, naming the tag (or other target) it acts on when it
// has one, so that areas can scope it.
const (
	PermWrite      = auth.Write        // tag writes
	PermAck        = auth.Ack          // alarm acknowledgement
	PermShelve     = auth.Shelve       // alarm shelving
	PermOOS        = auth.OutOfService // equipment out of service (#715)
	PermNote       = auth.Note         // operator notes (#720)
	PermSubstitute = auth.Substitute   // #707's substitutes
	PermForce      = auth.Force        // forces, set and cleared
	PermCheckout   = auth.Checkout     // I/O checkout results (#719)
	PermSignoff    = auth.Signoff      // sealing a checkout run (#719)
	PermSFC        = auth.SFC          // SFC step and transition commands
	PermEdit       = auth.Edit         // online edits and rollbacks
	PermActivate   = auth.Activate     // activating a commit
	PermDeploy     = auth.Deploy       // a Logix or Siemens download
	PermConfig     = auth.Config       // live I/O configuration (#706, #711)
	PermAudit      = auth.Audit        // reading the audit journal
	PermRedundancy = auth.Redundancy   // the redundant pair's operator commands (#1029 3c-3)
)

// actionPerm is the permission each journalled action needs. It is kept
// apart from actionClass (what happens while the journal is down): a
// route's action is in both, and TestEveryActionHasAPermission fails on
// one missing here. With an auth: block, an action missing here is
// refused.
var actionPerm = map[string]string{
	"tag.write":             PermWrite,
	"push.subscribe":        auth.Read, // any person who may read may be paged; read-gated per alarm at send
	"push.unsubscribe":      auth.Read,
	"camera.view":           auth.Camera, // watching a camera live (#724)
	"camera.snapshot-view":  auth.Camera, // looking at a picture kept for an alarm (#724)
	"camera.snapshot-purge": PermConfig,  // dropping the pictures kept for alarms (#724)
	"alarm.ack":             PermAck,
	"alarm.shelve":          PermShelve,
	"alarm.unshelve":        PermShelve,
	"alarm.out-of-service":  PermOOS,
	// Back in service shows alarms again: whoever may acknowledge them.
	"alarm.in-service":         PermAck,
	"sfc.step":                 PermSFC,
	"sfc.transition":           PermSFC,
	"force.set":                PermForce,
	"force.clear":              PermForce,
	"force.clear-all":          PermForce,
	"program.edit":             PermEdit,
	"program.rollback":         PermEdit,
	"program.activate":         PermActivate,
	"push.confirm-revocations": PermConfig,
	"checkout.begin":           PermCheckout,
	"checkout.result":          PermCheckout,
	"checkout.abandon":         PermCheckout,
	"checkout.seal":            PermSignoff,
	// Forcing an output is a force: its permission, elevation included, and
	// the checkout permission besides (the handler asks for both).
	"checkout.force":       PermForce,
	"checkout.release":     PermCheckout,
	"note.add":             PermNote,
	"note.revise":          PermNote,
	"note.retract":         PermNote,
	"config.stage":         PermConfig,
	"config.validate":      PermConfig,
	"config.apply":         PermConfig,
	"config.commit":        PermConfig,
	"param.commit":         PermConfig,
	"param.import":         PermConfig,
	"config.revert.cancel": PermConfig,
	// A parameter written to a device on an IO-Link port is a change to a
	// device, like a configuration apply (#705): the config permission.
	"param.write":      PermConfig,
	"substitute.start": PermSubstitute,
	"substitute.stop":  PermSubstitute,
	"substitute.ack":   PermSubstitute,
	// The redundant pair's operator commands (#1029 3c-3).
	"redundancy.clear-duplicate": PermRedundancy,
	"redundancy.commission":      PermRedundancy,
	"redundancy.recommission":    PermRedundancy,
	"redundancy.vouch":           PermRedundancy,
}

// presentedToken is the credential a request carries, in either
// "Authorization: Bearer <t>" or "X-Nautilus-Token: <t>" form, or "".
func presentedToken(r *http.Request) string {
	got := r.Header.Get("X-Nautilus-Token")
	if got == "" {
		// The scheme is case-insensitive (RFC 9110 §11.1): "bearer x"
		// is the same credential as "Bearer x".
		if scheme, cred, ok := strings.Cut(r.Header.Get("Authorization"), " "); ok && strings.EqualFold(scheme, "Bearer") {
			got = strings.TrimSpace(cred)
		}
	}
	return got
}

// tokenMatches reports whether the request carries token. Comparison is
// constant-time.
func tokenMatches(r *http.Request, token string) bool {
	got := presentedToken(r)
	return got != "" && token != "" && subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1
}

// credential is who a request's credential names: NAUTILUS_TOKEN's
// principal, a service token's, or anonymous. presented is whether it
// carried one at all, valid whether that one is known. Every check that
// trusts a credential (the Host rule, CORS, the journal's principal,
// authorize) asks here, so a service token is honoured everywhere
// NAUTILUS_TOKEN is.
func (s *Server) credential(r *http.Request) (pr auth.Principal, presented, valid bool) {
	got := presentedToken(r)
	if got == "" {
		return auth.Anonymous, false, true
	}
	if s.authToken != "" && subtle.ConstantTimeCompare([]byte(got), []byte(s.authToken)) == 1 {
		return auth.Token, true, true
	}
	// Every secret is compared, whichever matches, so the time taken does
	// not say which name a guess came close to.
	name := ""
	for n, secret := range s.services {
		if subtle.ConstantTimeCompare([]byte(got), []byte(secret)) == 1 {
			name = n
		}
	}
	if name != "" {
		return auth.Service(name), true, true
	}
	// A personal access token names its user (#708 PR 5a): found by its
	// id, its secret compared in constant time; a disabled user's or a
	// revoked one is no credential.
	if s.users != nil && users.IsToken(got) {
		if user, ok := s.users.TokenUser(got); ok {
			return auth.User(user), true, true
		}
	}
	return auth.Anonymous, true, false
}

// validCredential reports whether r carries a credential the server
// knows: NAUTILUS_TOKEN, a service token or a personal access token.
func (s *Server) validCredential(r *http.Request) bool {
	_, presented, valid := s.credential(r)
	return presented && valid
}

// actor is who a request acts as: its bearer credential when it presents
// one, else the browser session its cookie names, else anonymous.
type actor struct {
	pr auth.Principal
	// invalid: it presented a bearer credential nobody knows (a 401,
	// never a fall back to the cookie).
	invalid bool
	// session is set when the principal comes from the sign-in cookie.
	session *session
	// at is when it was resolved: a session's elevation is judged at that
	// instant, so the seam's decision and the handler's agree.
	at time.Time
}

// actorOf resolves r's principal for authorization, the journal and
// /api/me. The cookie is consulted only here, never by credential: it
// does not pass the Host rule, CORS or the preflight (design §2, gate 1),
// which ask validCredential.
//
// Behind the audit seam it is resolved once (withActor): the entry names
// who was authorized even if the session ends while the handler runs.
func (s *Server) actorOf(r *http.Request) actor {
	if a, ok := r.Context().Value(actorKey{}).(actor); ok {
		return a
	}
	return s.resolveActor(r)
}

// resolveActor is actorOf without the value withActor fixed: who r acts
// as now (an open stream re-checks by it).
func (s *Server) resolveActor(r *http.Request) actor {
	pr, presented, valid := s.credential(r)
	if presented {
		if !valid {
			return actor{pr: auth.Anonymous, invalid: true}
		}
		return actor{pr: pr}
	}
	if se, ok := s.sessionOf(r); ok {
		return actor{pr: se.principal(), session: &se, at: s.sessions.clock()}
	}
	return actor{pr: auth.Anonymous}
}

type actorKey struct{}

// withActor fixes r's actor for the rest of the request.
func (s *Server) withActor(r *http.Request) *http.Request {
	if _, ok := r.Context().Value(actorKey{}).(actor); ok {
		return r
	}
	return r.WithContext(context.WithValue(r.Context(), actorKey{}, s.actorOf(r)))
}

// sessionOf is the live session r's cookie names, still standing: a
// local user's still in the users file, enabled, on the password it was
// proved with, an OIDC name's still pinned to the account it was proved
// by; either granted something by the auth: block as it is now, and not
// signed out (naut users signout) since it began. An unknown or expired
// cookie (a restart forgot it) is no session, not an error: the request
// is anonymous.
func (s *Server) sessionOf(r *http.Request) (session, bool) {
	if s.sessions == nil {
		return session{}, false
	}
	for _, c := range r.CookiesNamed(s.cookieName(r)) {
		se, ok := s.sessions.lookup(c.Value)
		if !ok {
			continue
		}
		if !s.sessionStands(se) {
			return session{}, false
		}
		return se, true
	}
	return session{}, false
}

// sessionStands is sessionOf's recheck of a live session against the
// users files and the policy as they are now. Every doubt is a no: a file
// that cannot be read ends every session's acting.
func (s *Server) sessionStands(se session) bool {
	if at, out, err := s.users.SignedOut(se.key()); err != nil || out && !se.created.After(at) {
		return false
	}
	if se.oidc {
		if s.oidc == nil || !s.policy.OIDC() {
			return false
		}
		pin, ok, err := s.users.Pin(se.user)
		if err != nil || !ok || !pin.Same(se.issuer, se.sub) {
			return false
		}
		return s.policy.HasGrants(se.principal())
	}
	u, known := s.users.Current().Users[se.user]
	return known && !u.Disabled && credPrint(u.Password) == se.cred && s.policy.HasUser(se.user)
}

// cookieName is the sign-in cookie's name on r's transport: the __Host-
// one over TLS. Only that one is read: a plain-HTTP page on the same host
// cannot plant a cookie a secure request would take.
func (s *Server) cookieName(r *http.Request) string {
	if s.secure(r) {
		return secureCookie
	}
	return plainCookie
}

// hasSessionCookie reports whether r carries a sign-in cookie at all, by
// either name, valid or not: what a standby, which holds no sessions, can
// see.
func hasSessionCookie(r *http.Request) bool {
	return len(r.CookiesNamed(secureCookie)) > 0 || len(r.CookiesNamed(plainCookie)) > 0
}

// ownOrigin reports a browser request from this controller's own page: an
// Origin is present and is this controller's origin as the request
// reached it: https when secure(r), else http, and the Host, ports
// compared with the scheme's default filled in. It is stricter than
// sameOrigin, which lets a request with no Origin through and ignores the
// scheme: a cookie is sent by any page on the same host (SameSite ignores
// the port, and a browser without schemeful same-site the scheme too), so
// an http page an on-path attacker answers must not write over https.
func (s *Server) ownOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return false
	}
	u, err := url.Parse(o)
	if err != nil || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") {
		return false
	}
	scheme := "http"
	if s.secure(r) {
		scheme = "https"
	}
	return u.Scheme == scheme && strings.EqualFold(withPort(u.Host, scheme), withPort(r.Host, scheme))
}

// withPort is host[:port] with the scheme's default port when it has
// none, so "plc" and "plc:443" are the same https origin.
func withPort(host, scheme string) string {
	if _, _, err := net.SplitHostPort(host); err == nil {
		return host
	}
	port := "80"
	if scheme == "https" {
		port = "443"
	}
	return net.JoinHostPort(strings.TrimSuffix(strings.TrimPrefix(host, "["), "]"), port)
}

// cookieRefused is the refusal for a cookie-authenticated request that
// changes something from anywhere but this controller's own page (#812):
// 0 when it is allowed.
func (s *Server) cookieRefused(r *http.Request, a actor) (int, string) {
	if a.session == nil || r.Method == http.MethodGet || r.Method == http.MethodHead || s.ownOrigin(r) {
		return 0, ""
	}
	return http.StatusForbidden, "a signed-in browser may change something only from this controller's own page (the request's Origin must be this controller's scheme and host)"
}

// ElevationNeeded begins the message of a refusal for want of a fresh
// password (#708 PR 5c): a browser sign-in holds the permission, but it is
// an elevated one and the password was last proved longer ago than
// auth.elevation. Its 401 carries "elevate": true (WriteRefusal), and the
// HMI asks for the password (POST /api/elevate) and sends the request
// again.
const ElevationNeeded = "confirm your password to do this"

// elevationRefused is the refusal of an actor that holds perm but may not
// use it yet: a browser session whose password is older than
// auth.elevation, for an elevated permission. A bearer credential
// (NAUTILUS_TOKEN, a service token, a personal access token) is presented
// with every request, and always counts as fresh. 0 when allowed.
func (s *Server) elevationRefused(a actor, perm string) (int, string) {
	if a.session == nil || !auth.Elevated(perm) {
		return 0, ""
	}
	if a.at.Before(s.elevatedUntil(*a.session)) {
		return 0, ""
	}
	if a.session.oidc {
		// Not ElevationNeeded: the HMI must not ask for a password that
		// does not exist. It sends the page to the provider instead.
		return http.StatusUnauthorized, fmt.Sprintf("%s: %s needs credentials entered at the provider within the last %s (auth.elevation). The HMI sends you there (GET %s). Signing in again here does not help while the provider still holds your sign-in there: sign out at the provider first", ElevationAtProvider, perm, shortDuration(s.policy.Elevation()), OIDCElevateURL)
	}
	return http.StatusUnauthorized, fmt.Sprintf("%s: sign in again (/signin), or enter your password where the HMI asks for it (POST /api/elevate). %s needs a password entered within the last %s (auth.elevation)", ElevationNeeded, perm, shortDuration(s.policy.Elevation()))
}

// shortDuration is d as a person reads it: "15m", "1h", "1h30m", not
// "15m0s".
func shortDuration(d time.Duration) string {
	s := d.String()
	if d%time.Minute == 0 {
		s = strings.TrimSuffix(s, "0s")
		if d%time.Hour == 0 {
			s = strings.TrimSuffix(s, "0m")
		}
	}
	return s
}

// elevatedUntil is when se's password stops counting for an elevated
// permission: auth.elevation after it was last proved, and never past the
// session's end.
func (s *Server) elevatedUntil(se session) time.Time {
	until := se.authAt.Add(s.policy.Elevation())
	if se.expires.Before(until) {
		return se.expires
	}
	return until
}

// WriteRefusal answers a refusal from Authorize as the server's own
// routes do: {"error": msg}, plus "elevate": true when it is for want of a
// fresh password, so the HMI kit knows to ask for it, or "elevate": "oidc"
// and "url" when it is for want of a fresh sign-in at the OIDC provider,
// so the kit sends the page there. A facade that adds an endpoint answers
// its refusals with it.
func WriteRefusal(w http.ResponseWriter, code int, msg string) {
	if code == http.StatusUnauthorized && strings.HasPrefix(msg, ElevationAtProvider) {
		writeOIDCElevate(w, msg)
		return
	}
	body := map[string]any{"error": msg}
	if code == http.StatusUnauthorized && strings.HasPrefix(msg, ElevationNeeded) {
		body["elevate"] = true
	}
	writeJSON(w, code, body)
}

// writeOIDCElevate is the 401 that sends an OIDC session to the provider
// to confirm its sign-in: "elevate": "oidc" and the URL to open (the kit
// adds next=). A kit too old to know it reads only the message, which
// says the same in words.
func writeOIDCElevate(w http.ResponseWriter, msg string) {
	writeJSON(w, http.StatusUnauthorized, map[string]any{"error": msg, "elevate": "oidc", "url": OIDCElevateURL})
}

// credentialKind is how a request's principal proved itself, for the
// journal's entry of an elevated action: "session" (a browser sign-in's
// fresh password), "pat" (a personal access token), "token"
// (NAUTILUS_TOKEN), "service" (a service token) or "anonymous".
func credentialKind(a actor) string {
	switch {
	case a.session != nil:
		return "session"
	case a.pr.Kind == auth.KindUser:
		return "pat"
	}
	return a.pr.Kind
}

// signInHint ends a 401 for a request with no credential.
const signInHint = ": sign in, or send a token (Authorization: Bearer …)"

// credentialsConfigured reports whether any credential exists: a request
// may then come from any origin or Host with it.
func (s *Server) credentialsConfigured() bool {
	return s.authToken != "" || len(s.services) > 0 || s.users != nil
}

// journalUsersChange records each change naut users made to the users
// file, by the operating-system user who made it ("os:<name>"), never a
// hash or a secret.
//
// A change that takes a user's password away (disabled, a new password,
// removed) ends that user's browser sessions at once, and its entry says
// how many. It is done here, in the one OnChange callback, so a change is
// never journalled without its revocation.
func (s *Server) journalUsersChange(by string, changes []users.Change) {
	who := "os:unknown"
	if by != "" {
		who = "os:" + by
	}
	for _, c := range changes {
		detail := map[string]any{"change": c.What}
		target := "user:" + c.User
		if c.Principal != "" {
			target = c.Principal
		}
		switch c.What {
		case "disabled", "password", "removed", "signed out", "unpinned", "pin changed":
			if s.sessions != nil {
				if n := s.sessions.endPrincipal(target); n > 0 {
					detail["sessions"] = n
				}
			}
		}
		if c.Token != "" {
			detail["token"] = c.Token
		}
		if c.Stopped {
			// Found at the start: made while the controller was stopped.
			detail["while"] = "stopped"
		}
		s.audit.record(audit.Entry{
			User:   who,
			Source: audit.SourceCLI,
			Action: "users.change",
			Target: target,
			Detail: detail,
			Result: audit.ResultOK,
		})
	}
}

// authorize reports whether r may do perm to target: 0 means allowed,
// otherwise the status and message to refuse with.
//
// It is the one authorization seam a route calls, once per request:
// decode the body, call authorize with the target it names, and only then
// report a decode error (the audit seam gives a request it refused an
// empty body, and its refusal must be the 401, not a 400).
//
// Without an auth: block it is the write policy of before (#811, #815):
// the token when one is set, else a same-origin request; perm and target
// are not consulted. With one, the request's principal must hold perm,
// and, for an area-scoped permission, over target (auth.Policy.Can): no
// credential is a 401, a principal without the permission a 403.
func (s *Server) authorize(r *http.Request, perm, target string) (int, string) {
	if s.policy == nil {
		return s.authorizeWrite(r)
	}
	a := s.actorOf(r)
	if a.invalid {
		return http.StatusUnauthorized, "invalid credential"
	}
	if code, msg := s.cookieRefused(r, a); code != 0 {
		return code, msg
	}
	d := s.policy.Can(a.pr, perm, target)
	if d.Allowed {
		return s.elevationRefused(a, perm)
	}
	if a.pr.Kind == auth.KindAnonymous {
		return http.StatusUnauthorized, d.Reason + signInHint
	}
	return http.StatusForbidden, d.Reason
}

// authorizeEach is authorize over each target a request acts on (the
// substitutes an ack names). With none, nothing is acted on: under a
// policy the seam has already checked the principal holds perm, and the
// handler answers the empty request itself; without one it is the write
// rule, as authorize.
func (s *Server) authorizeEach(r *http.Request, perm string, targets []string) (int, string) {
	if len(targets) == 0 {
		if s.policy != nil && decidedBySeam(r) {
			return 0, ""
		}
		return s.authorize(r, perm, "")
	}
	for _, t := range targets {
		if code, msg := s.authorize(r, perm, t); code != 0 {
			return code, msg
		}
	}
	return 0, ""
}

// Authorize applies the server's authorization to a request another
// handler serves, so a facade that adds an endpoint keeps the same rule:
// perm is what the endpoint does (PermEdit, PermDeploy, …). 0 means
// allowed.
func (s *Server) Authorize(r *http.Request, perm, target string) (int, string) {
	return s.authorize(r, perm, target)
}

// preAuthorize is the audit seam's check, before it reads a body: may r
// do action at all? Without an auth: block, the write policy; with one,
// the principal must hold the action's permission somewhere (the handler
// checks the target it names). An action with no permission is refused.
//
// known reports, for a refusal, that it came from a principal the
// controller knows (a valid credential, from its own page when it is a
// session) and was refused for what it may do: by role, or for want of a
// fresh password. The seam then reads the body for the entry's target, so
// the journal says what a named person tried to change. A request with no
// credential, a bad one or from another site is journalled without it.
func (s *Server) preAuthorize(r *http.Request, action string) (code int, msg string, known bool) {
	if s.policy == nil {
		code, msg = s.authorizeWrite(r)
		return code, msg, false
	}
	perm, ok := actionPerm[action]
	if !ok {
		return http.StatusForbidden, fmt.Sprintf("%s needs a permission nobody has been given (the server does not map it)", action), false
	}
	a := s.actorOf(r)
	if a.invalid {
		return http.StatusUnauthorized, "invalid credential", false
	}
	if code, msg := s.cookieRefused(r, a); code != 0 {
		return code, msg, false
	}
	if s.policy.Holds(a.pr, perm) {
		code, msg = s.elevationRefused(a, perm)
		return code, msg, code != 0
	}
	reason := s.policy.Can(a.pr, perm, auth.All).Reason
	if a.pr.Kind == auth.KindAnonymous {
		return http.StatusUnauthorized, reason + signInHint, false
	}
	return http.StatusForbidden, reason, true
}

// seamDecided marks a request the audit seam has authorized for its
// action: the handlers' own first check (authorizeWrite) then passes, and
// what is left for them is the target (authorize, after decoding).
type seamDecided struct{}

func withSeamDecision(r *http.Request) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), seamDecided{}, true))
}

func decidedBySeam(r *http.Request) bool {
	ok, _ := r.Context().Value(seamDecided{}).(bool)
	return ok
}

// principalOf is the journal's name for who r acts as: "anonymous",
// "token", "token:<name>" or "user:<name>" (a personal access token, or a
// signed-in browser). An unknown credential is anonymous; nothing the
// client names (an alarm's "by") is ever taken as the principal.
func (s *Server) principalOf(r *http.Request) string {
	return s.actorOf(r).pr.String()
}

// sessionOfEntry is the journal's "session" for r: the hash of the
// sign-in it acts by, or "" for a bearer credential or none.
func (s *Server) sessionOfEntry(r *http.Request) string {
	if a := s.actorOf(r); a.session != nil {
		return a.session.hash
	}
	return ""
}

// meResponse is GET /api/me: who the request acts as, and what it may do.
// Without an auth: block, Auth is false and only the principal is said:
// writes follow the token-or-same-origin rule, not roles.
type meResponse struct {
	Auth bool `json:"auth"`
	// Secure says the request came over TLS (the controller's own, or a
	// trusted proxy's): where password sign-in and Secure cookies work.
	Secure bool `json:"secure"`
	auth.Me
	// Session is the browser sign-in the request acts by, when it does.
	Session *meSession `json:"session,omitempty"`
	// Elevated lists the permissions a browser sign-in holds only while
	// its password is fresh (session.elevatedUntil); a token's always are.
	Elevated []string `json:"elevated,omitempty"`
	// SignIn says this controller offers browser sign-in (/signin): an
	// auth: block that grants users roles, and a users file. A facade, or a
	// project without users, has no sign-in page to send anyone to.
	SignIn bool `json:"signIn"`
	// Now is the controller's clock, so a page judges elevatedUntil and
	// expires against it rather than the browser's.
	Now time.Time `json:"now"`
	// SignInMethods are the ways /signin offers: "password" (local users)
	// and "oidc" (Provider). Empty where sign-in is not offered.
	SignInMethods []string `json:"signInMethods,omitempty"`
	// Provider is the OIDC provider's name ("Sign in with <Provider>"),
	// when the controller has one.
	Provider string `json:"provider,omitempty"`
}

// meSession is /api/me's account of a browser sign-in. The id never
// appears: the cookie is HttpOnly.
type meSession struct {
	Expires time.Time `json:"expires"`
	// Local: the operator log has not committed this sign-in, so it is
	// valid on this controller only and is lost if the other one leads
	// first (#716). The account chip says so.
	Local bool `json:"local,omitempty"`
	// ElevatedUntil is when its password stops counting for the elevated
	// permissions (auth.elevation): past it the HMI asks for the password
	// before an elevated action.
	ElevatedUntil time.Time `json:"elevatedUntil"`
	// Method is how it was proved, and so how it is confirmed again:
	// "password" (POST /api/elevate) or "oidc" (at the provider, by
	// opening OIDCElevateURL).
	Method string `json:"method"`
}

// handleMe is GET /api/me, for the HMI kit's role-aware controls (#708 PR
// 7): the principal, its roles, and its permissions on the whole
// controller ("*") and per area. It is a read: it never refuses, and an
// unknown credential is answered as anonymous.
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	a := s.actorOf(r)
	// It answers by the request's cookie: no cache may hand it to another.
	w.Header().Set("Cache-Control", "no-store")
	now := time.Now().UTC()
	if s.policy == nil {
		writeJSON(w, http.StatusOK, meResponse{Secure: s.secure(r), Now: now, Me: auth.Me{Principal: a.pr.String(), Roles: []string{}, Permissions: map[string][]string{}}})
		return
	}
	me := meResponse{Auth: true, Secure: s.secure(r), Me: s.policy.Describe(a.pr), Elevated: s.elevatedNames(), SignIn: s.sessions != nil, Now: now}
	me.Permissions = s.visiblePermissions(me.Permissions)
	if s.sessions != nil {
		if s.policy.HasUsers() {
			me.SignInMethods = append(me.SignInMethods, "password")
		}
		if s.oidc != nil {
			me.SignInMethods = append(me.SignInMethods, "oidc")
		}
	}
	if s.oidc != nil {
		me.Provider = s.oidc.Provider()
	}
	if a.session != nil {
		me.Session = &meSession{Expires: a.session.expires.UTC(), ElevatedUntil: s.elevatedUntil(*a.session).UTC(), Method: "password", Local: a.session.local}
		if a.session.oidc {
			me.Session.Method = "oidc"
		}
	}
	writeJSON(w, http.StatusOK, me)
}

// journalUsersHealth records the users file becoming unreadable (no
// local user can authenticate until it is fixed; the system alarm
// nautilus.users-file is up meanwhile) and readable again.
func (s *Server) journalUsersHealth(err error) {
	e := audit.Entry{User: "system", Source: audit.SourceSystem, Target: s.users.Path()}
	if err != nil {
		slog.Error("users file: no local user can authenticate until it is fixed", "err", err)
		e.Action, e.Result, e.Detail = "users.unavailable", audit.ResultError, map[string]any{"reason": err.Error()}
	} else {
		slog.Info("users file: readable again", "path", s.users.Path())
		e.Action, e.Result = "users.recovered", audit.ResultOK
	}
	s.audit.record(e)
}

// UsersFileDown reports whether the controller's users file cannot be
// read: no local user can authenticate (the nautilus.users-file system
// alarm).
func (s *Server) UsersFileDown() bool { return s.users != nil && s.users.Err() != nil }

// checkoutEnabled reports whether the project turned the I/O checkout on
// (#719): without it nothing about it is shown anywhere.
func (s *Server) checkoutEnabled() bool { return s.shown[auth.Checkout] }

// checkoutPermission is a permission that exists only for a project that
// uses the checkout.
func checkoutPermission(p string) bool { return p == auth.Checkout || p == auth.Signoff }

// elevatedNames is auth.ElevatedPermissions as this project shows them.
func (s *Server) elevatedNames() []string {
	if s.checkoutEnabled() {
		return auth.ElevatedPermissions
	}
	out := make([]string, 0, len(auth.ElevatedPermissions))
	for _, p := range auth.ElevatedPermissions {
		if !checkoutPermission(p) {
			out = append(out, p)
		}
	}
	return out
}

// permissionShown reports whether /api/me lists p: the checkout's two exist
// only for a project that uses the checkout, and camera only for one with
// cameras (#724); the roles still define them, so turning the feature on needs
// no role edit.
func (s *Server) permissionShown(p string) bool {
	switch {
	case checkoutPermission(p):
		return s.checkoutEnabled()
	case p == auth.Camera:
		return s.shown[auth.Camera]
	}
	return true
}

// visiblePermissions is a principal's permissions as /api/me shows them: the
// ones of a feature the project does not use are left out.
func (s *Server) visiblePermissions(in map[string][]string) map[string][]string {
	if s.checkoutEnabled() && s.shown[auth.Camera] {
		return in
	}
	out := make(map[string][]string, len(in))
	for scope, perms := range in {
		kept := make([]string, 0, len(perms))
		for _, p := range perms {
			if s.permissionShown(p) {
				kept = append(kept, p)
			}
		}
		if len(kept) > 0 {
			out[scope] = kept
		}
	}
	return out
}
