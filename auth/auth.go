// Package auth decides what a principal may do (#708,
// docs/design/identity-audit.md §2): principals, permissions, roles,
// areas, and a Policy compiled from a manifest's auth: block.
//
// It knows no HTTP and no manifest format: the server asks a Policy
// whether a principal may use a permission on a target, and turns the
// answer into a status; internal/project turns YAML into Settings. It
// imports no other nautilus package.
//
// Every question fails closed: an unknown permission, a principal with no
// role, a target outside every area a grant covers, and an empty target
// for an area-scoped permission are all refused.
package auth

import (
	"fmt"
	"slices"
	"strings"
)

// The permissions (design §2).
const (
	Read       = "read"
	Write      = "write"      // tag writes
	Ack        = "ack"        // alarm acknowledgement
	Shelve     = "shelve"     // alarm shelving
	Substitute = "substitute" // #707's substitutes
	Force      = "force"      // forces, set and cleared
	SFC        = "sfc"        // SFC step and transition commands
	Edit       = "edit"       // online edits and rollbacks
	Activate   = "activate"   // activating a commit
	Deploy     = "deploy"     // a Logix or Siemens download
	Config     = "config"     // live I/O configuration (#706, #711)
	Audit      = "audit"      // reading the audit journal
	Users      = "users"      // managing users and tokens
	Override   = "override"   // #787's engineer override
)

// Permissions lists every permission, in the order /api/me reports them.
var Permissions = []string{Read, Write, Ack, Shelve, Substitute, Force, SFC, Edit, Activate, Deploy, Config, Audit, Users, Override}

// areaScoped are the permissions an area limits to its tags. The rest
// apply to the whole controller, whatever areas a grant names.
var areaScoped = map[string]bool{Write: true, Ack: true, Shelve: true, Substitute: true, Force: true}

// AreaScoped reports whether perm is limited by areas.
func AreaScoped(perm string) bool { return areaScoped[perm] }

// ElevatedPermissions are the permissions a browser sign-in holds only
// while its password is fresh (auth.elevation, #708 PR 5c): what changes
// the program, the I/O, a forced value or who may do what. A token always
// counts as fresh. In /api/me's order.
var ElevatedPermissions = []string{Force, Edit, Activate, Deploy, Config, Users, Override}

// Elevated reports whether perm is one of the ElevatedPermissions.
func Elevated(perm string) bool { return slices.Contains(ElevatedPermissions, perm) }

// IsPermission reports whether name is a permission.
func IsPermission(name string) bool { return slices.Contains(Permissions, name) }

// All is the target that means every tag: clearing all forces, say. Only
// a grant that covers the whole controller (one with no areas) holds an
// area-scoped permission on it.
const All = "*"

// The built-in roles, each adding to the one before (design §2).
const (
	Viewer        = "viewer"
	Operator      = "operator"
	Supervisor    = "supervisor"
	Engineer      = "engineer"
	Administrator = "administrator"
)

var builtins = []struct {
	name string
	adds []string
}{
	{Viewer, []string{Read}},
	{Operator, []string{Write, Ack, SFC}},
	{Supervisor, []string{Shelve, Substitute, Audit}},
	{Engineer, []string{Force, Edit, Activate, Deploy, Config}},
	{Administrator, []string{Users, Override}},
}

// builtinRoles is each built-in role's permission set.
var builtinRoles = func() map[string]map[string]bool {
	out := map[string]map[string]bool{}
	acc := map[string]bool{}
	for _, b := range builtins {
		for _, p := range b.adds {
			acc[p] = true
		}
		set := make(map[string]bool, len(acc))
		for p := range acc {
			set[p] = true
		}
		out[b.name] = set
	}
	return out
}()

// Kinds of principal.
const (
	KindAnonymous = "anonymous"  // no credential
	KindToken     = "token"      // NAUTILUS_TOKEN
	KindService   = "service"    // a named service token
	KindUser      = "user"       // a signed-in user or a personal access token (#708 PR 5)
	KindOPCUACert = "opcua-cert" // an OPC UA client certificate the block maps to roles (#708 PR 8a)
)

// Principal is who a request acts as.
type Principal struct {
	Kind string
	Name string // the service token's or the user's name
}

// Anonymous is the principal of a request with no credential.
var Anonymous = Principal{Kind: KindAnonymous}

// Token is NAUTILUS_TOKEN's principal.
var Token = Principal{Kind: KindToken}

// Service is a named service token's principal.
func Service(name string) Principal { return Principal{Kind: KindService, Name: name} }

// User is a user's principal.
func User(name string) Principal { return Principal{Kind: KindUser, Name: name} }

// OPCUACert is the principal of an OPC UA client certificate the block
// names in opcua-certificates.
func OPCUACert(name string) Principal { return Principal{Kind: KindOPCUACert, Name: name} }

// String is how the journal names the principal: "anonymous", "token",
// "token:<name>", "user:<name>" or "opcua-cert:<name>".
func (p Principal) String() string {
	switch p.Kind {
	case KindService:
		return "token:" + p.Name
	case KindUser:
		return "user:" + p.Name
	case KindOPCUACert:
		return "opcua-cert:" + p.Name
	case "":
		return KindAnonymous
	}
	return p.Kind
}

// validName is a role, area, user or token name: letters, digits, '-',
// '_' and '.', starting with a letter or digit.
func validName(what, name string) error {
	if name == "" {
		return fmt.Errorf("auth: an empty %s name", what)
	}
	for i, r := range name {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
			i > 0 && (r == '-' || r == '_' || r == '.')
		if !ok {
			return fmt.Errorf("auth: %s %q: use letters, digits, '-', '_' and '.'", what, name)
		}
	}
	return nil
}

// validTokenName is a service token's name: letters, digits and '_'
// only, since it names the variable its secret comes from
// (NAUTILUS_TOKEN_<NAME>), and a-b, a_b and a.b must not be three tokens
// with one secret.
func validTokenName(name string) error {
	if name == "" {
		return fmt.Errorf("auth: an empty service token name")
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_') {
			return fmt.Errorf("auth: service token %q: use letters, digits and '_' (it names NAUTILUS_TOKEN_<NAME>)", name)
		}
	}
	return nil
}

func lower(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
