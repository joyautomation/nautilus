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
	"sort"
	"strings"
)

// The permissions (design §2).
const (
	Read   = "read"
	Write  = "write"  // tag writes
	Ack    = "ack"    // alarm acknowledgement
	Shelve = "shelve" // alarm shelving
	// OutOfService takes a piece of equipment's alarms out of service
	// for up to a week (#715): heavier than a shelf, so it needs a fresh
	// password. Returning to service needs only Ack: showing alarms
	// again must be easy.
	OutOfService = "out-of-service"
	Substitute   = "substitute" // #707's substitutes
	Note         = "note"       // operator notes and the shift log (#720)
	Force        = "force"      // forces, set and cleared
	SFC          = "sfc"        // SFC step and transition commands
	Edit         = "edit"       // online edits and rollbacks
	Activate     = "activate"   // activating a commit
	Deploy       = "deploy"     // a Logix or Siemens download
	Config       = "config"     // live I/O configuration (#706, #711)
	Audit        = "audit"      // reading the audit journal
	Users        = "users"      // managing users and tokens
	Override     = "override"   // #787's engineer override
	// Checkout records the results of an I/O checkout and runs its steps (#719);
	// Signoff seals a checkout run. Forcing an output in a checkout needs Force too.
	Checkout = "checkout"
	Signoff  = "signoff"
	// Camera views the plant's cameras (#724). Video of a plant floor shows
	// people, so it is its own permission, whole-controller (not area-scoped:
	// that is a bigger question than cameras) and held by every built-in role
	// that can read, so a project without auth: and the existing role setups
	// keep working; a custom role's deny: takes it away.
	Camera = "camera"
	// PTZ moves a camera (#724): a write to the world, whole-controller like
	// Camera and elevated like the other writes that change something physical.
	// One elevation covers a session of moves (auth.elevation, 15 minutes by default).
	PTZ = "ptz"
	// Redundancy is the redundant pair's operator commands: clearing a
	// duplicate mark, commissioning and re-commissioning (#710, #1029
	// 3c-3). Safety-critical: who may tell the pair which process is the
	// member.
	Redundancy = "redundancy"
)

// Permissions lists every permission, in the order /api/me reports them.
var Permissions = []string{Read, Write, Ack, Shelve, OutOfService, Note, Substitute, Force, SFC, Edit, Activate, Deploy, Config, Audit, Users, Override, Redundancy, Checkout, Signoff, Camera, PTZ}

// areaScoped are the permissions an area limits to its tags. The rest
// apply to the whole controller, whatever areas a grant names.
var areaScoped = map[string]bool{Write: true, Ack: true, Shelve: true, OutOfService: true, Note: true, Substitute: true, Force: true, Checkout: true}

// AreaScoped reports whether perm is limited by areas.
func AreaScoped(perm string) bool { return areaScoped[perm] }

// ElevatedPermissions are the permissions a browser sign-in holds only
// while its password is fresh (auth.elevation, #708 PR 5c): what changes
// the program, the I/O, a forced value, who may do what, or which process
// a redundant pair counts as its member. A token always
// counts as fresh. In /api/me's order.
var ElevatedPermissions = []string{OutOfService, Force, Edit, Activate, Deploy, Config, Users, Override, Redundancy, Signoff, PTZ}

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
	{Viewer, []string{Read, Camera}},
	{Operator, []string{Write, Ack, SFC, Note, PTZ}},
	{Supervisor, []string{Shelve, OutOfService, Substitute, Audit, Signoff}},
	{Engineer, []string{Force, Edit, Activate, Deploy, Config, Checkout}},
	{Administrator, []string{Users, Override, Redundancy}},
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
	KindSparkplug = "sparkplug"  // a Sparkplug command (NCMD/DCMD), named by its host: a label, not a proof (#708 PR 8b)
	KindOIDC      = "oidc"       // a browser signed in at the auth: block's OpenID Connect provider (#708 PR 6)
)

// Principal is who a request acts as.
type Principal struct {
	Kind string
	Name string // the service token's or the user's name
	// groups are an OIDC principal's group ids, from its ID token, sorted
	// and joined by NUL (a string keeps a Principal comparable). Only
	// OIDC sets them, and only auth.oidc.groups reads them.
	groups string
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

// Sparkplug is the principal of a Sparkplug command, named by the host it
// is taken to come from: sparkplug.primary-host when set, else the
// broker's host. The name is a LABEL for the journal, not a credential:
// anyone who can publish to the broker can send a command, so every
// Sparkplug command has the grants auth.sparkplug gives, whatever the name.
func Sparkplug(name string) Principal { return Principal{Kind: KindSparkplug, Name: name} }

// OIDC is the principal of a browser signed in at the OIDC provider:
// name is its username claim (ValidOIDCName, lowercased), groups the
// group ids its ID token carried. Its grants are auth.oidc.users' for the
// name plus auth.oidc.groups' for each id, never auth.users': an OIDC
// name and a local user of the same name are two people.
func OIDC(name string, groups []string) Principal {
	gs := slices.Clone(groups)
	sort.Strings(gs)
	gs = slices.Compact(gs)
	return Principal{Kind: KindOIDC, Name: name, groups: strings.Join(gs, "\x00")}
}

// Groups are an OIDC principal's group ids, sorted; nil for any other.
func (p Principal) Groups() []string {
	if p.groups == "" {
		return nil
	}
	return strings.Split(p.groups, "\x00")
}

// String is how the journal names the principal: "anonymous", "token",
// "token:<name>", "user:<name>", "oidc:<name>", "opcua-cert:<name>" or
// "sparkplug:<name>".
func (p Principal) String() string {
	switch p.Kind {
	case KindService:
		return "token:" + p.Name
	case KindUser:
		return "user:" + p.Name
	case KindOIDC:
		return "oidc:" + p.Name
	case KindOPCUACert:
		return "opcua-cert:" + p.Name
	case KindSparkplug:
		return "sparkplug:" + p.Name
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

// MaxOIDCName is the longest OIDC name (a username claim) taken.
const MaxOIDCName = 128

// ValidOIDCName checks an OIDC name, the username claim's value once
// lowercased: 1 to MaxOIDCName ASCII letters, digits and '-', '_', '.',
// '@', '+' and an apostrophe, starting with a letter or digit. That covers user
// principal names and e-mail addresses, and keeps out what a journal
// reader could be fooled by: spaces, control characters, look-alike
// letters from other scripts, and ':'.
func ValidOIDCName(name string) error {
	if name == "" {
		return fmt.Errorf("an empty OIDC name")
	}
	if len(name) > MaxOIDCName {
		return fmt.Errorf("an OIDC name longer than %d characters", MaxOIDCName)
	}
	for i, r := range name {
		ok := r >= 'a' && r <= 'z' || r >= '0' && r <= '9' ||
			i > 0 && strings.ContainsRune("-_.@+'", r)
		if !ok {
			return fmt.Errorf("OIDC name %q: it may hold only lowercase letters, digits and - _ . @ + ' (and start with a letter or digit)", name)
		}
	}
	return nil
}

// OIDCName is the OIDC name a username claim's value gives: trimmed, and
// lowered as ASCII only, after any non-ASCII byte is refused. Lowering
// first would fold look-alikes into ASCII names (strings.ToLower maps
// U+0130 'İ' to 'i' and the Kelvin sign to 'k'), and let one account take
// another's name.
func OIDCName(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	b := []byte(raw)
	for i, c := range b {
		if c > 0x7e {
			return "", fmt.Errorf("OIDC name %q: it may hold only ASCII letters, digits and - _ . @ + '", raw)
		}
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	name := string(b)
	return name, ValidOIDCName(name)
}

func lower(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
