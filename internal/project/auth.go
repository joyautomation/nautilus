package project

import (
	"fmt"

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
	// ServiceTokens maps a named token to its role; its secret comes from
	// the environment (NAUTILUS_TOKEN_<NAME>), never the manifest.
	ServiceTokens map[string]string `yaml:"service-tokens"`
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

// UnmarshalYAML takes a bare role name or a {role, areas} mapping, and
// refuses any other key: a misspelt "area:" must not grant the whole
// controller.
func (g *AuthGrant) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		g.Role = n.Value
		return nil
	}
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: a grant is a role, or {role: …, areas: […]}", n.Line)
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		switch k := n.Content[i]; k.Value {
		case "role":
			if err := n.Content[i+1].Decode(&g.Role); err != nil {
				return err
			}
		case "areas":
			if err := n.Content[i+1].Decode(&g.Areas); err != nil {
				return err
			}
		default:
			return fmt.Errorf("line %d: field %s not found in a grant (role, areas)", k.Line, k.Value)
		}
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
		ServiceTokens: c.ServiceTokens,
	}
	for name, a := range c.Areas {
		s.Areas[name] = a.Tags
	}
	for name, r := range c.Roles {
		s.Roles[name] = auth.RoleDef{Inherits: r.Inherits, Allow: r.Allow, Deny: r.Deny}
	}
	for name, gs := range c.Users {
		for _, g := range gs {
			s.Users[name] = append(s.Users[name], auth.Grant{Role: g.Role, Areas: g.Areas})
		}
		if len(gs) == 0 {
			s.Users[name] = nil
		}
	}
	return s
}

// compileAuth builds the block's policy; nil when there is no block.
func compileAuth(c *AuthConfig) (*auth.Policy, error) {
	if c == nil {
		return nil, nil
	}
	// The auth package's errors already begin "auth: ".
	return auth.Compile(c.settings())
}
