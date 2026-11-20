package project

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/joyautomation/nautilus/auth"
	"gopkg.in/yaml.v3"
)

// AuthConfig is the manifest's auth: block (#708,
// docs/design/identity-audit.md §2): who may do what. Roles, areas and the
// user-to-role mapping live here, reviewed in git; credentials never do.
// Load compiles it into Project.Auth, so a typo (an unknown role, area or
// permission, a bad tag pattern) fails `naut check`.
type AuthConfig struct {
	// Anonymous is what a request with no credential may do: none (the
	// default: sign-in required) or viewer.
	Anonymous string `yaml:"anonymous"`
	// TokenRole is NAUTILUS_TOKEN's role (default administrator).
	TokenRole string `yaml:"token-role"`
	// Areas are named sets of tag patterns that scope a grant's write,
	// ack, shelve, substitute and force.
	Areas map[string]AuthArea `yaml:"areas"`
	// Roles are custom roles: a built-in or custom role to inherit, plus
	// permissions to allow or deny.
	Roles map[string]AuthRole `yaml:"roles"`
	// Users maps a user to their grants: a role, or {role, areas}.
	Users map[string][]AuthGrant `yaml:"users"`
	// ServiceTokens maps a named token to its role, or to grants as a
	// user's ({role, areas}); its secret comes from the environment
	// (NAUTILUS_TOKEN_<NAME>), never the manifest.
	ServiceTokens map[string]AuthGrants `yaml:"service-tokens"`
	// OPCUACerts maps an OPC UA client certificate (a trusted one, by its
	// thumbprint or its subject) to grants, as a service token's: such a
	// session writes as opcua-cert:<name> within those grants. Under an
	// auth: block a trusted certificate it does not map writes nothing;
	// its client signs in with a user name to write (#708 PR 8a).
	OPCUACerts map[string]AuthCert `yaml:"opcua-certificates"`
	// StateDir is where the controller keeps its users file (users.json:
	// password hashes and personal access tokens). Default: systemd's
	// $STATE_DIRECTORY. Never inside the project, so no hash reaches git.
	StateDir string `yaml:"state-dir"`
	// Session is how long a browser sign-in lasts, from the sign-in
	// (absolute): 1m to 168h, default 12h. Sessions live in the leader's
	// memory, so a restart or a failover also ends them.
	Session Duration `yaml:"session"`
	// Elevation is how long a browser sign-in's password counts for the
	// elevated permissions (force, edit, activate, deploy, config, users,
	// override), from the sign-in or the last re-authentication
	// (POST /api/elevate): 1m to 12h, default 15m. A token always counts.
	Elevation Duration `yaml:"elevation"`
}

// AuthArea is an area: the tags it covers, as patterns ("RCV_*").
type AuthArea struct {
	Tags []string `yaml:"tags"`
}

// AuthRole is a custom role.
type AuthRole struct {
	Inherits string   `yaml:"inherits"`
	Allow    []string `yaml:"allow"`
	Deny     []string `yaml:"deny"`
}

// AuthGrant is one of a user's roles, over the whole controller or the
// areas named. In YAML it is a role's name, or {role, areas}.
type AuthGrant struct {
	Role  string   `yaml:"role"`
	Areas []string `yaml:"areas"`
}

// AuthCert is one OPC UA client certificate's mapping: exactly one of a
// thumbprint (the 40-digit SHA-1 naut opcua certs prints) or a subject (as
// it prints it), and its grants, a role or a list as a user's.
type AuthCert struct {
	Thumbprint string     `yaml:"thumbprint"`
	Subject    string     `yaml:"subject"`
	Grants     AuthGrants `yaml:"grants"`
}

// AuthGrants are a service token's grants: a role's name, or a list of
// grants as a user's.
type AuthGrants []AuthGrant

// UnmarshalYAML takes a role's name or a list of grants.
func (gs *AuthGrants) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		*gs = AuthGrants{{Role: n.Value}}
		return nil
	}
	var list []AuthGrant
	if err := n.Decode(&list); err != nil {
		return err
	}
	*gs = list
	return nil
}

// UnmarshalYAML takes a bare role name or a {role, areas} mapping. It
// walks the node itself, so it refuses here what the decoder would: any
// other key (a misspelt "area:" must not grant the whole controller) and
// a key given twice (the last would win: {role: viewer, role:
// administrator}). And an areas: that is empty or null covers nothing;
// leaving areas out is the whole controller, so deleting someone's last
// area must not widen them.
func (g *AuthGrant) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		g.Role = n.Value
		return nil
	}
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: a grant is a role, or {role: …, areas: […]}", n.Line)
	}
	seen := map[string]bool{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if seen[k.Value] {
			return fmt.Errorf("line %d: %s is given twice in a grant", k.Line, k.Value)
		}
		seen[k.Value] = true
		switch k.Value {
		case "role":
			if err := v.Decode(&g.Role); err != nil {
				return err
			}
		case "areas":
			if v.Tag == "!!null" || v.Kind == yaml.SequenceNode && len(v.Content) == 0 {
				return fmt.Errorf("line %d: areas is empty, which covers nothing; leave areas out for the whole controller", k.Line)
			}
			if err := v.Decode(&g.Areas); err != nil {
				return err
			}
		default:
			return fmt.Errorf("line %d: field %s not found in a grant (role, areas)", k.Line, k.Value)
		}
	}
	if !seen["role"] {
		return fmt.Errorf("line %d: a grant names no role", n.Line)
	}
	return nil
}

// settings is the block as the auth package takes it.
func (c *AuthConfig) settings() auth.Settings {
	s := auth.Settings{
		Anonymous:     c.Anonymous,
		TokenRole:     c.TokenRole,
		Areas:         map[string][]string{},
		Roles:         map[string]auth.RoleDef{},
		Users:         map[string][]auth.Grant{},
		ServiceTokens: map[string][]auth.Grant{},
		Session:       time.Duration(c.Session),
		Elevation:     time.Duration(c.Elevation),
	}
	for name, a := range c.Areas {
		s.Areas[name] = a.Tags
	}
	for name, r := range c.Roles {
		s.Roles[name] = auth.RoleDef{Inherits: r.Inherits, Allow: r.Allow, Deny: r.Deny}
	}
	grants := func(gs []AuthGrant) []auth.Grant {
		var out []auth.Grant
		for _, g := range gs {
			out = append(out, auth.Grant{Role: g.Role, Areas: g.Areas})
		}
		return out
	}
	for name, gs := range c.Users {
		s.Users[name] = grants(gs)
	}
	for name, gs := range c.ServiceTokens {
		s.ServiceTokens[name] = grants(gs)
	}
	if len(c.OPCUACerts) > 0 {
		s.OPCUACerts = map[string]auth.CertGrant{}
		for name, cg := range c.OPCUACerts {
			s.OPCUACerts[name] = auth.CertGrant{Thumbprint: cg.Thumbprint, Subject: cg.Subject, Grants: grants(cg.Grants)}
		}
	}
	return s
}

// errAuthNull refuses an auth: key with no value. YAML reads "auth:" with
// nothing after it, "auth: ~" and a block whose every line is commented
// out as null, the same as no key at all: a controller with every read and
// write open. Someone who comments out users: while editing must not open
// the plant without a word, so it is refused; "auth: {}" is a block that
// grants nobody anything.
var errAuthNull = fmt.Errorf("auth: is empty: remove the key for an open controller, or give it a policy (auth: {} lets nobody do anything)")

// authKeyNull reports a manifest whose top-level auth: key is present with
// a null value.
func authKeyNull(raw []byte) bool {
	var doc yaml.Node
	if yaml.Unmarshal(raw, &doc) != nil || len(doc.Content) == 0 {
		return false
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return false
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "auth" {
			v := root.Content[i+1]
			for v.Kind == yaml.AliasNode && v.Alias != nil {
				v = v.Alias
			}
			return v.Kind == yaml.ScalarNode && v.Tag == "!!null"
		}
	}
	return false
}

// compileAuth builds the block's policy; nil when there is no block.
func compileAuth(c *AuthConfig) (*auth.Policy, error) {
	if c == nil {
		return nil, nil
	}
	// naut users and the controller resolve a relative path against
	// different working directories, and would read different files.
	if c.StateDir != "" && !filepath.IsAbs(c.StateDir) {
		return nil, fmt.Errorf("auth: state-dir %q is relative: give an absolute path outside the project", c.StateDir)
	}
	// The auth package's errors already begin "auth: ".
	return auth.Compile(c.settings())
}
