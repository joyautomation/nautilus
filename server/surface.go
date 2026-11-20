package server

import (
	"context"
	"errors"
	"strings"

	"github.com/joyautomation/nautilus/audit"
	"github.com/joyautomation/nautilus/auth"
	"github.com/joyautomation/nautilus/auth/users"
)

// Surface is what a protocol surface outside HTTP (the OPC UA server, #708
// PR 8) is handed so that it signs users in, decides what they may do and
// journals what they did through this server, without importing it
// (docs/design/identity-audit.md §2: other surfaces get a principal and a
// record function). Everything goes through the same pieces HTTP uses:
// the users file, the sign-in throttle and its hashing slots, the auth:
// block's policy, and the journal's recorder (refusal coalescing, entries
// kept while the journal cannot be written, the system alarm).
//
// A nil *Surface is valid and does nothing: it signs nobody in, holds
// nothing, allows nothing and records nothing.
type Surface struct {
	s      *Server
	source string
}

// Surface returns the hooks for the surface source names, the journal's
// "source" for what it records ("opcua").
func (s *Server) Surface(source string) *Surface {
	return &Surface{s: s, source: source}
}

// ErrSignInRefused is every refused sign-in through a Surface: a wrong
// name or password, a user the auth: block grants nothing, the throttle,
// or no slot to check the password in. The journal entry says which; the
// client is told only this.
var ErrSignInRefused = errors.New("sign-in refused")

// SignsIn reports whether users can sign in: the auth: block grants users
// roles and the controller has a users file.
func (u *Surface) SignsIn() bool {
	return u != nil && u.s.sessions != nil && u.s.users != nil && u.s.policy != nil
}

// SignIn checks name's password as POST /api/signin does, and journals the
// attempt as auth.signin from the surface: from is the client's address
// (the throttle's key), detail what the entry adds (a certificate, say).
// It returns the user's name as the users file spells it (lowercase) and
// the credential Holds rechecks, or ErrSignInRefused.
func (u *Surface) SignIn(ctx context.Context, from, name, password string, detail map[string]any) (user, cred string, err error) {
	if !u.SignsIn() {
		return "", "", ErrSignInRefused
	}
	name = strings.ToLower(strings.TrimSpace(name))
	e := audit.Entry{User: "anonymous", Source: u.source, Action: "auth.signin", From: from, Result: audit.ResultRefused}
	if users.ValidName(name) == nil {
		e.Target = "user:" + name
	}
	d := map[string]any{}
	for k, v := range detail {
		d[k] = v
	}
	cred, refusal := u.s.checkPassword(ctx, from, name, password)
	if refusal.reason != "" {
		d["reason"] = refusal.reason
		e.Detail = d
		u.s.audit.record(e)
		return "", "", ErrSignInRefused
	}
	e.User, e.Target, e.Result = "user:"+name, "", audit.ResultOK
	if len(d) > 0 {
		e.Detail = d
	}
	u.s.audit.record(e)
	return name, cred, nil
}

// Holds reports whether user, signed in with cred, still may act: still
// in the users file, enabled, with the password cred was proved with, and
// granted something by the auth: block. A surface asks it at each action,
// so removing or disabling a user, or changing their password, ends what
// their earlier sign-in allowed there, as it ends a browser's session.
func (u *Surface) Holds(user, cred string) bool {
	if !u.SignsIn() || cred == "" {
		return false
	}
	rec, known := u.s.users.Current().Users[user]
	return known && !rec.Disabled && credPrint(rec.Password) == cred && u.s.policy.HasUser(user)
}

// May reports whether p (a user, or an OPC UA certificate OPCUACert
// named) may use perm (an auth permission: "write") on target (a tag, a
// member path, or an index into it). False without an auth: block.
func (u *Surface) May(p auth.Principal, perm, target string) bool {
	if u == nil || u.s.policy == nil {
		return false
	}
	return u.s.policy.Can(p, perm, target).Allowed
}

// OPCUACert is the name the auth: block maps an OPC UA client certificate
// to (auth.opcua-certificates), by its thumbprint (hex SHA-1) or its
// subject: false when it maps neither, or there is no block.
func (u *Surface) OPCUACert(thumbprint, subject string) (string, bool) {
	if u == nil || u.s.policy == nil {
		return "", false
	}
	return u.s.policy.OPCUACertFor(thumbprint, subject)
}

// Record journals e from the surface: its Source is set, its target and
// values are capped as the HTTP seam caps them, and it goes through the
// recorder, so a refusal flood is coalesced and an entry the journal could
// not take is kept and written later.
func (u *Surface) Record(e audit.Entry) {
	if u == nil {
		return
	}
	e.Source = u.source
	e.Target = capText(e.Target, targetMax)
	limit := valueMax
	if e.Result != audit.ResultOK {
		limit = refusedValueMax
	}
	e.Before, e.After = audit.Cap(e.Before, limit), audit.Cap(e.After, limit)
	u.s.audit.record(e)
}
