// identity.go is who a session is, beyond its certificate (#708 PR 8a,
// docs/design/identity-audit.md §5 item 8, opcua-server-writes.md slice 4):
// UserName identity tokens checked against the controller's local users,
// and every write journalled (#709).
//
// The server does not keep users itself. `naut run` hands it the HTTP
// server's hooks (SetIdentity, server.Surface): the same users file, the
// same sign-in throttle and hashing slots, the same auth: block and the
// same journal. A UserName token is offered on the secure endpoints only,
// and accepted only on a SignAndEncrypt channel from a trusted
// certificate: the certificate says which application, the user says who.
// A user session writes a tag when opcua.writable opens it AND the user's
// roles allow write on the tag's area. The user is checked again at every
// request, so a user removed, disabled or with a new password stops
// writing at once, as a browser's session ends.
//
// A session without a user writes only when the auth: block maps its
// certificate to roles (auth.opcua-certificates, by thumbprint or
// subject): then it writes as opcua-cert:<name>, within those roles' areas,
// like a service token. A trusted certificate the block does not map is
// read-only (the maintainer's decision, 2026-11-20). Without an Identity
// (no auth: block) a trusted certificate writes what opcua.writable opens,
// as before (opcua-server-writes.md §7 decision 2).
//
// Reads, browsing and subscriptions are not gated by the auth: block yet:
// every session reads, as before (opcua-server-writes.md §7 decision 4).
package server

import (
	"context"
	"net"
	"strings"

	"github.com/gopcua/opcua/ua"
	"github.com/joyautomation/nautilus/audit"
	"github.com/joyautomation/nautilus/auth"
	"github.com/joyautomation/nautilus/opcua/internal/uasc"
)

// Identity is what the server asks about users, and where it journals.
// *server.Surface (the HTTP server's, for source "opcua") is one.
type Identity interface {
	// SignsIn reports whether users can sign in at all.
	SignsIn() bool
	// SignIn checks name's password (throttled, journalled as
	// auth.signin) and returns the user's name and a credential to
	// recheck with Holds.
	SignIn(ctx context.Context, from, name, password string, detail map[string]any) (user, cred string, err error)
	// Holds reports whether user, signed in with cred, may still act.
	Holds(user, cred string) bool
	// May reports whether p (a user, or a mapped certificate) may use
	// perm on target (a tag address).
	May(p auth.Principal, perm, target string) bool
	// OPCUACert is the name the auth: block maps a client certificate to,
	// by its full thumbprint or its subject; false when it maps neither.
	OPCUACert(thumbprint, subject string) (string, bool)
	// Record journals an entry.
	Record(audit.Entry)
}

// SetIdentity gives the server its users and its journal. Call it before
// Start: UserName tokens are offered from the next Start when id signs
// users in and the server has a PKI. Nil (the default) offers anonymous
// only and journals nothing, as before: the log lines stay either way.
func (s *Server) SetIdentity(id Identity) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.w.id = id
}

// userPrefix is a user session's identity on the session: "user:<name>".
const userPrefix = "user:"

// checkUser is the UserName check the session service calls, off its
// message loop, once the token's password is decrypted. The channel must
// be a writer's (SignAndEncrypt from a certificate in the trust list,
// within its validity): a user signed in elsewhere would be a user whose
// requests nothing binds to an application the operator trusted. Every
// refusal is Bad_UserAccessDenied, whatever the journal says it was, so a
// client learns nothing about the name.
func (w *writeAccess) checkUser(ctx context.Context, sc *uasc.SecureChannel, name string, password []byte) (string, string, ua.StatusCode) {
	id := w.id
	if id == nil || !id.SignsIn() {
		return "", "", ua.StatusBadIdentityTokenRejected
	}
	cl := w.channel(sc)
	detail := map[string]any{"remote": cl.remote}
	if cl.thumbprint != "" {
		detail["thumbprint"], detail["client"] = cl.thumbprint, cl.uri
	}
	from := hostOnly(cl.remote)
	if !cl.writer || cl.anonymous {
		why := "not a SignAndEncrypt channel from a trusted client certificate"
		if cl.why != "" {
			why = cl.why
		}
		detail["reason"] = why
		e := audit.Entry{User: "anonymous", Action: "auth.signin", From: from, Result: audit.ResultRefused, Detail: detail}
		if n := strings.ToLower(strings.TrimSpace(name)); n != "" {
			e.Target = userPrefix + clip(n)
		}
		id.Record(e)
		return "", "", ua.StatusBadIdentityTokenRejected
	}
	user, cred, err := id.SignIn(ctx, from, name, string(password), detail)
	if err != nil {
		return "", "", ua.StatusBadUserAccessDenied
	}
	return userPrefix + user, cred, ua.StatusOK
}

// hostOnly is an address without its port: the throttle's and the
// journal's "from" for a client with no certificate to name.
func hostOnly(addr string) string {
	if h, _, err := net.SplitHostPort(addr); err == nil {
		return h
	}
	return addr
}
