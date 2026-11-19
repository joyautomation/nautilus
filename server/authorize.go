package server

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net/http"
	"strings"

	"github.com/joyautomation/nautilus/auth"
)

// The permission names (#708, docs/design/identity-audit.md §2): what
// authorize is asked whether a request may do. A route asks for the one
// its action needs, naming the tag (or other target) it acts on when it
// has one, so that areas can scope it.
const (
	PermWrite      = auth.Write      // tag writes
	PermAck        = auth.Ack        // alarm acknowledgement
	PermShelve     = auth.Shelve     // alarm shelving
	PermSubstitute = auth.Substitute // #707's substitutes
	PermForce      = auth.Force      // forces, set and cleared
	PermSFC        = auth.SFC        // SFC step and transition commands
	PermEdit       = auth.Edit       // online edits and rollbacks
	PermActivate   = auth.Activate   // activating a commit
	PermDeploy     = auth.Deploy     // a Logix or Siemens download
	PermConfig     = auth.Config     // live I/O configuration (#706, #711)
	PermAudit      = auth.Audit      // reading the audit journal
)

// actionPerm is the permission each journalled action needs. It is kept
// apart from actionClass (what happens while the journal is down): a
// route's action is in both, and TestEveryActionHasAPermission fails on
// one missing here. With an auth: block, an action missing here is
// refused.
var actionPerm = map[string]string{
	"tag.write":            PermWrite,
	"alarm.ack":            PermAck,
	"alarm.shelve":         PermShelve,
	"alarm.unshelve":       PermShelve,
	"sfc.step":             PermSFC,
	"sfc.transition":       PermSFC,
	"force.set":            PermForce,
	"force.clear":          PermForce,
	"force.clear-all":      PermForce,
	"program.edit":         PermEdit,
	"program.rollback":     PermEdit,
	"program.activate":     PermActivate,
	"config.stage":         PermConfig,
	"config.validate":      PermConfig,
	"config.apply":         PermConfig,
	"config.revert.cancel": PermConfig,
	"substitute.start":     PermSubstitute,
	"substitute.stop":      PermSubstitute,
	"substitute.ack":       PermSubstitute,
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
	return auth.Anonymous, true, false
}

// validCredential reports whether r carries a credential the server
// knows: NAUTILUS_TOKEN or a service token.
func (s *Server) validCredential(r *http.Request) bool {
	_, presented, valid := s.credential(r)
	return presented && valid
}

// credentialsConfigured reports whether any credential exists: a request
// may then come from any origin or Host with it.
func (s *Server) credentialsConfigured() bool { return s.authToken != "" || len(s.services) > 0 }

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
	pr, presented, valid := s.credential(r)
	if presented && !valid {
		return http.StatusUnauthorized, "invalid credential"
	}
	d := s.policy.Can(pr, perm, target)
	if d.Allowed {
		return 0, ""
	}
	if pr.Kind == auth.KindAnonymous {
		return http.StatusUnauthorized, d.Reason + ": send a token (Authorization: Bearer …)"
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
func (s *Server) preAuthorize(r *http.Request, action string) (int, string) {
	if s.policy == nil {
		return s.authorizeWrite(r)
	}
	perm, ok := actionPerm[action]
	if !ok {
		return http.StatusForbidden, fmt.Sprintf("%s needs a permission nobody has been given (the server does not map it)", action)
	}
	pr, presented, valid := s.credential(r)
	if presented && !valid {
		return http.StatusUnauthorized, "invalid credential"
	}
	if s.policy.Holds(pr, perm) {
		return 0, ""
	}
	reason := s.policy.Can(pr, perm, auth.All).Reason
	if pr.Kind == auth.KindAnonymous {
		return http.StatusUnauthorized, reason + ": send a token (Authorization: Bearer …)"
	}
	return http.StatusForbidden, reason
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
// "token", or "token:<name>". An unknown credential is anonymous; nothing
// the client names (an alarm's "by") is ever taken as the principal.
func (s *Server) principalOf(r *http.Request) string {
	pr, _, valid := s.credential(r)
	if !valid {
		return auth.Anonymous.String()
	}
	return pr.String()
}

// meResponse is GET /api/me: who the request acts as, and what it may do.
// Without an auth: block, Auth is false and only the principal is said:
// writes follow the token-or-same-origin rule, not roles.
type meResponse struct {
	Auth bool `json:"auth"`
	auth.Me
}

// handleMe is GET /api/me, for the HMI kit's role-aware controls (#708 PR
// 7): the principal, its roles, and its permissions on the whole
// controller ("*") and per area. It is a read: it never refuses, and an
// unknown credential is answered as anonymous.
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	pr, _, valid := s.credential(r)
	if !valid {
		pr = auth.Anonymous
	}
	if s.policy == nil {
		writeJSON(w, http.StatusOK, meResponse{Me: auth.Me{Principal: pr.String(), Roles: []string{}, Permissions: map[string][]string{}}})
		return
	}
	writeJSON(w, http.StatusOK, meResponse{Auth: true, Me: s.policy.Describe(pr)})
}
