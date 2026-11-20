package server

import (
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/joyautomation/nautilus/audit"
	"github.com/joyautomation/nautilus/auth"
)

// Elevation (#708 PR 5c, docs/design/identity-audit.md §2): a browser
// sign-in holds the elevated permissions (force, edit, activate, deploy,
// config, users, override) only for auth.elevation after its password was
// last proved. POST /api/elevate proves it again, for this session alone.
//
// It is checked like a sign-in (this controller's own page, over TLS or
// from loopback, one argon2id verification in the same two slots, one
// message for every wrong password) but throttled by the session, not the
// address: only a cookie's holder can spend its bucket, and a thief with
// the cookie gains nothing by rotating addresses. Each refused password
// also spends a token of the user's sign-in name bucket, so this is not a
// second budget of guesses, and after maxElevateFails in a row the session
// is ended. It sits outside the audited seam (it journals itself,
// auth.elevate) and works while the journal is down: it changes no
// process state.

// elevateRefused is what every wrong password is told.
const elevateRefused = "re-authentication refused: the password is wrong"

// handleElevate is POST /api/elevate: {"password": "…"}.
func (s *Server) handleElevate(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if s.sessions == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "elevation is for a browser sign-in, and sign-in needs an auth: block that grants users roles (auth.users) and a users file (naut users add)"})
		return
	}
	a := s.resolveActor(r)
	e := audit.Entry{User: a.pr.String(), Source: clientSource(r), Action: "auth.elevate", From: s.clientAddr(r), Result: audit.ResultRefused}
	if a.session != nil {
		e.Session = a.session.hash
	}
	refuse := func(status int, msg, reason string, extra map[string]any) {
		e.Detail = map[string]any{"status": status, "reason": reason}
		for k, v := range extra {
			e.Detail[k] = v
		}
		s.audit.record(e)
		writeJSON(w, status, map[string]string{"error": msg})
	}
	if !s.ownOrigin(r) {
		refuse(http.StatusForbidden, "re-authentication is accepted only from this controller's own page (the request's Origin must be this controller's scheme and host)", "cross-origin", nil)
		return
	}
	if !s.signInTransport(r) {
		refuse(http.StatusForbidden, "a password needs HTTPS: set server.tls, or name the TLS proxy in server.trusted-proxies (plain HTTP is allowed only from this machine's loopback)", "not over TLS", nil)
		return
	}
	if presentedToken(r) != "" {
		refuse(http.StatusBadRequest, "a token always counts as elevated: re-authentication is for a signed-in browser", "bearer credential", nil)
		return
	}
	if a.session == nil {
		refuse(http.StatusUnauthorized, "not signed in: sign in first (/signin)", "no session", nil)
		return
	}
	se := *a.session
	// A user who holds no elevated permission has nothing to confirm:
	// refused before any hashing, so a viewer's sessions cannot keep the
	// password-hash slots busy (#1088 review).
	if !s.holdsElevated(a) {
		refuse(http.StatusForbidden, "nothing to confirm: "+se.user+" holds none of the elevated permissions ("+strings.Join(auth.ElevatedPermissions, ", ")+")", "no elevated permission", nil)
		return
	}
	if se.oidc {
		// A password POSTed here proves nothing about an OIDC account: the
		// confirmation is at the provider (6b, oidc_elevate.go). The answer
		// says where, as a stale elevation's refusal does; nothing was
		// attempted, so nothing is journalled.
		writeOIDCElevate(w, ElevationAtProvider+": your sign-in is from "+s.oidc.Provider()+", so you confirm it there, not with a password here (GET "+OIDCElevateURL+")")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxSignInBody)
	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		refuse(http.StatusBadRequest, "reading the re-authentication: "+err.Error(), "unreadable request", nil)
		return
	}
	if ok, wait := s.signin.sessions.take(se.hash); !ok {
		secs := int(math.Ceil(wait.Seconds()))
		w.Header().Set("Retry-After", strconv.Itoa(secs))
		refuse(http.StatusTooManyRequests, "too many attempts: try again in "+strconv.Itoa(secs)+" s", "throttled", nil)
		return
	}
	release, ok := s.signin.hash(r.Context())
	if !ok {
		refuse(http.StatusServiceUnavailable, "the controller is busy checking other passwords: try again", "busy", nil)
		return
	}
	file := s.users.Current()
	err := file.Authenticate(se.user, req.Password)
	release()
	var proved session
	if err == nil {
		// The password proved is the one the session was opened with: a
		// session whose user's password changed acts no more (sessionOf),
		// and is never refreshed here either.
		var still bool
		if proved, still = s.sessions.elevated(se.hash, credPrint(file.Users[se.user].Password)); !still {
			err = errors.New("the session ended, or its user's password changed, during the check")
		}
	}
	if err != nil {
		// Each wrong password is charged to the name as a sign-in's is.
		s.signin.names.take(se.user)
		fails, ended := s.sessions.elevateFailed(se.hash)
		extra := map[string]any{"fails": fails}
		msg := elevateRefused
		if ended {
			extra["sessionEnded"] = true
			msg = elevateRefused + "; after " + strconv.Itoa(maxElevateFails) + " wrong passwords in a row this sign-in has ended: sign in again"
			http.SetCookie(w, &http.Cookie{Name: s.cookieName(r), Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: s.secure(r), SameSite: http.SameSiteStrictMode})
		}
		refuse(http.StatusUnauthorized, msg, err.Error(), extra)
		if ended {
			// Its own entry, never folded into a run of refusals (the
			// journal coalesces those).
			slog.Warn("auth: a browser session ended after wrong passwords at re-authentication", "user", se.user, "from", s.clientAddr(r))
			s.audit.record(audit.Entry{User: "user:" + se.user, Source: clientSource(r), Action: "auth.session-ended",
				From: s.clientAddr(r), Session: se.hash, Result: audit.ResultOK,
				Detail: map[string]any{"reason": strconv.Itoa(maxElevateFails) + " wrong passwords in a row at re-authentication (POST /api/elevate)"}})
		}
		return
	}
	until := s.elevatedUntil(proved).UTC()
	e.Result = audit.ResultOK
	e.Detail = map[string]any{"elevatedUntil": until.Format(time.RFC3339)}
	s.audit.record(e)
	writeJSON(w, http.StatusOK, map[string]any{"user": se.user, "elevatedUntil": until})
}

// holdsElevated reports whether a's principal holds any elevated
// permission, anywhere.
func (s *Server) holdsElevated(a actor) bool {
	for _, p := range auth.ElevatedPermissions {
		if s.policy.Holds(a.pr, p) {
			return true
		}
	}
	return false
}
