package server

import "net/http"

// The permission names (#708, docs/design/identity-audit.md §2): what
// authorize is asked whether a request may do. A route asks for the one
// its action needs, naming the tag (or other target) it acts on when it
// has one, so that areas can scope it.
const (
	PermWrite      = "write"      // tag writes
	PermAck        = "ack"        // alarm acknowledgement
	PermShelve     = "shelve"     // alarm shelving
	PermSubstitute = "substitute" // #707's substitutes
	PermForce      = "force"      // forces, set and cleared
	PermSFC        = "sfc"        // SFC step and transition commands
	PermEdit       = "edit"       // online edits and rollbacks
	PermActivate   = "activate"   // activating a commit
	PermDeploy     = "deploy"     // a Logix or Siemens download
	PermConfig     = "config"     // live I/O configuration (#706, #711)
	PermAudit      = "audit"      // reading the audit journal
)

// authorize reports whether r may do perm to target: 0 means allowed,
// otherwise the status and message to refuse with.
//
// It is the one authorization seam a route calls, once per request:
// decode the body, call authorize with the target it names, and only then
// report a decode error (the audit seam gives a request without the write
// credential an empty body, and its refusal must be the 401, not a 400).
//
// Until users and roles (#708 PR 4) it is today's write policy: perm and
// target are not consulted, and every write needs the auth token when one
// is set, else a same-origin request. PR 4 replaces this body, not its
// callers.
func (s *Server) authorize(r *http.Request, perm, target string) (int, string) {
	return s.authorizeWrite(r)
}
