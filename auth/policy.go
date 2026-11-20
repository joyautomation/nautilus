package auth

import (
	"fmt"
	"path"
	"sort"
	"strings"
	"time"
)

// Settings is a manifest's auth: block, already parsed (internal/project
// reads the YAML). Names are matched ignoring case.
type Settings struct {
	// Anonymous is what a request with no credential may do: "none" (the
	// default: sign-in required) or "viewer" (a wallboard).
	Anonymous string
	// TokenRole is NAUTILUS_TOKEN's role; "" is administrator, as before
	// the auth: block existed.
	TokenRole string
	// Areas are named sets of tag patterns ("RCV_*"), matched against a
	// target's tag (its name before any member or index).
	Areas map[string][]string
	// Roles are custom roles, each inheriting a built-in or another
	// custom role and adding or removing permissions.
	Roles map[string]RoleDef
	// Users are the local and OIDC users' grants (credentials are never
	// here).
	Users map[string][]Grant
	// ServiceTokens are named tokens for scripts and services, each with
	// its grants, like a user's. Their secrets are not here either.
	ServiceTokens map[string][]Grant
	// Session is how long a browser sign-in lasts, from the moment of
	// sign-in (absolute, not renewed by use): 0 is DefaultSession.
	Session time.Duration
}

// DefaultSession is a sign-in's lifetime when auth.session is not set:
// one shift plus handover.
const DefaultSession = 12 * time.Hour

// MinSession and MaxSession bound auth.session.
const (
	MinSession = time.Minute
	MaxSession = 7 * 24 * time.Hour
)

// RoleDef is a custom role.
type RoleDef struct {
	Inherits    string
	Allow, Deny []string
}

// Grant is a role, over the whole controller or over named areas.
type Grant struct {
	Role  string
	Areas []string
}

// Policy answers what a principal may do. Build one with Compile.
type Policy struct {
	roles     map[string]map[string]bool
	areas     map[string][]string // lowercased patterns
	anonymous []Grant
	token     []Grant
	services  map[string][]Grant
	users     map[string][]Grant
	session   time.Duration
}

// Compile checks c and builds its Policy. Every name it refers to must
// exist, and every pattern must be valid: a typo is an error at load, not
// a grant that silently matches nothing or everything.
func Compile(c Settings) (*Policy, error) {
	p := &Policy{roles: map[string]map[string]bool{}, areas: map[string][]string{},
		services: map[string][]Grant{}, users: map[string][]Grant{}}
	for name, set := range builtinRoles {
		p.roles[name] = set
	}
	p.session = c.Session
	if p.session == 0 {
		p.session = DefaultSession
	}
	if p.session < MinSession || p.session > MaxSession {
		return nil, fmt.Errorf("auth: session %s is out of range: give 1m to 168h (default 12h)", c.Session)
	}

	for name, pats := range c.Areas {
		key := lower(name)
		if err := validName("area", key); err != nil {
			return nil, err
		}
		if _, dup := p.areas[key]; dup {
			return nil, fmt.Errorf("auth: area %q is declared twice", name)
		}
		if len(pats) == 0 {
			return nil, fmt.Errorf("auth: area %q names no tags", name)
		}
		for _, pat := range pats {
			pat = lower(pat)
			if pat == "" {
				return nil, fmt.Errorf("auth: area %q has an empty tag pattern", name)
			}
			if _, err := path.Match(pat, ""); err != nil {
				return nil, fmt.Errorf("auth: area %q: tag pattern %q: %v", name, pat, err)
			}
			// A target is matched by its tag, never a member: a pattern
			// with a '.' could never match, and must not load as one that
			// silently grants nothing.
			if strings.ContainsAny(pat, ".") {
				return nil, fmt.Errorf("auth: area %q: tag pattern %q names a member; an area covers whole tags", name, pat)
			}
			p.areas[key] = append(p.areas[key], pat)
		}
	}

	if err := p.compileRoles(c.Roles); err != nil {
		return nil, err
	}

	switch lower(c.Anonymous) {
	case "", "none":
	case Viewer:
		p.anonymous = []Grant{{Role: Viewer}}
	default:
		// Never more than viewing: a role that writes would need the
		// cross-site defences the credential gives (#811, #815).
		return nil, fmt.Errorf("auth: anonymous: use none or viewer")
	}

	tokenRole := lower(c.TokenRole)
	if tokenRole == "" {
		tokenRole = Administrator
	}
	if p.roles[tokenRole] == nil {
		return nil, fmt.Errorf("auth: token-role is not a role: %s", p.roleList())
	}
	p.token = []Grant{{Role: tokenRole}}

	if err := p.compileGrants("service token", validTokenName, c.ServiceTokens, p.services); err != nil {
		return nil, err
	}
	userName := func(n string) error { return validName("user", n) }
	if err := p.compileGrants("user", userName, c.Users, p.users); err != nil {
		return nil, err
	}
	return p, nil
}

// compileGrants checks and stores each principal's grants: a valid name,
// declared once, at least one grant, each naming a role and only areas
// that exist. Errors name the principal, never the refused value (a
// secret pasted into the wrong field).
func (p *Policy) compileGrants(what string, valid func(string) error, in, out map[string][]Grant) error {
	for name, grants := range in {
		key := lower(name)
		if err := valid(key); err != nil {
			return err
		}
		if _, dup := out[key]; dup {
			return fmt.Errorf("auth: %s %q is declared twice", what, name)
		}
		if len(grants) == 0 {
			return fmt.Errorf("auth: %s %q has no role", what, name)
		}
		var gs []Grant
		for _, g := range grants {
			role := lower(g.Role)
			if p.roles[role] == nil {
				return fmt.Errorf("auth: %s %q: a grant's role is not a role: %s", what, name, p.roleList())
			}
			if g.Areas != nil && len(g.Areas) == 0 {
				// No areas is the whole controller; an empty list must not
				// be, or deleting someone's last area would widen them.
				return fmt.Errorf("auth: %s %q: a grant with areas: [] covers nothing; leave areas out for the whole controller", what, name)
			}
			ng := Grant{Role: role}
			for _, a := range g.Areas {
				if p.areas[lower(a)] == nil {
					return fmt.Errorf("auth: %s %q: no area %q", what, name, a)
				}
				ng.Areas = append(ng.Areas, lower(a))
			}
			gs = append(gs, ng)
		}
		out[key] = gs
	}
	return nil
}

// compileRoles resolves the custom roles: each inherits a built-in or
// another custom role (no cycles, no redefining a built-in) and adds or
// removes known permissions.
func (p *Policy) compileRoles(defs map[string]RoleDef) error {
	byName := map[string]RoleDef{}
	for name, d := range defs {
		key := lower(name)
		if err := validName("role", key); err != nil {
			return err
		}
		if builtinRoles[key] != nil {
			return fmt.Errorf("auth: role %q is built in: name your own role differently", name)
		}
		if _, dup := byName[key]; dup {
			return fmt.Errorf("auth: role %q is declared twice", name)
		}
		for _, perm := range append(append([]string{}, d.Allow...), d.Deny...) {
			if !IsPermission(lower(perm)) {
				return fmt.Errorf("auth: role %q: no permission %q (permissions: %s)", name, perm, strings.Join(Permissions, ", "))
			}
		}
		byName[key] = d
	}
	var resolve func(name string, seen []string) (map[string]bool, error)
	resolve = func(name string, seen []string) (map[string]bool, error) {
		if set := p.roles[name]; set != nil {
			return set, nil
		}
		d, ok := byName[name]
		if !ok {
			return nil, fmt.Errorf("auth: role %q inherits something that is not a role: %s", seen[len(seen)-1], p.roleList())
		}
		for _, s := range seen {
			if s == name {
				return nil, fmt.Errorf("auth: roles inherit in a cycle: %s", strings.Join(append(seen, name), " → "))
			}
		}
		parent := lower(d.Inherits)
		if parent == "" {
			return nil, fmt.Errorf("auth: role %q inherits nothing: name a role to start from (viewer, operator, …)", name)
		}
		base, err := resolve(parent, append(seen, name))
		if err != nil {
			return nil, err
		}
		set := make(map[string]bool, len(base))
		for perm := range base {
			set[perm] = true
		}
		for _, perm := range d.Allow {
			set[lower(perm)] = true
		}
		for _, perm := range d.Deny {
			delete(set, lower(perm))
		}
		p.roles[name] = set
		return set, nil
	}
	names := make([]string, 0, len(byName))
	for n := range byName {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if _, err := resolve(n, nil); err != nil {
			return err
		}
	}
	return nil
}

// grants are a principal's grants; nil when it has none.
func (p *Policy) grants(pr Principal) []Grant {
	switch pr.Kind {
	case KindAnonymous, "":
		return p.anonymous
	case KindToken:
		return p.token
	case KindService:
		return p.services[lower(pr.Name)]
	case KindUser:
		return p.users[lower(pr.Name)]
	}
	return nil
}

// HasServiceToken reports whether name is a declared service token.
func (p *Policy) HasServiceToken(name string) bool { return p.services[lower(name)] != nil }

// ServiceTokens lists the declared service tokens' names.
func (p *Policy) ServiceTokens() []string {
	out := make([]string, 0, len(p.services))
	for n := range p.services {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Session is how long a browser sign-in lasts (auth.session), from the
// sign-in.
func (p *Policy) Session() time.Duration { return p.session }

// HasUser reports whether the block grants the user name anything: a box
// user it does not name may not sign in.
func (p *Policy) HasUser(name string) bool { return len(p.users[lower(name)]) > 0 }

// HasUsers reports whether the block grants any user a role: without
// one, no local user could do anything, and a controller needs no users
// file.
func (p *Policy) HasUsers() bool { return len(p.users) > 0 }

// Decision is Can's answer: whether the action is allowed, and if not,
// a sentence saying why.
type Decision struct {
	Allowed bool
	Reason  string
}

// Can reports whether pr may use perm on target. For an area-scoped
// permission (write, ack, shelve, substitute, force) target is the tag
// acted on (a member path or index is matched by its tag), or All; an
// empty target is never allowed, so a request whose target could not be
// read is refused. Other permissions ignore target.
func (p *Policy) Can(pr Principal, perm, target string) Decision {
	perm = lower(perm)
	if !IsPermission(perm) {
		return Decision{Reason: fmt.Sprintf("%s: no permission %q", pr, perm)}
	}
	gs := p.grants(pr)
	if len(gs) == 0 {
		return Decision{Reason: fmt.Sprintf("%s has no role here", pr)}
	}
	// The target as areas see it, decided once: All exactly, or a tag
	// (the name before any member or index, ignoring case), never empty
	// and never a pattern.
	raw := target
	if target != All && areaScoped[perm] {
		tag := tagOf(target)
		switch {
		case tag == "":
			target = ""
		case strings.ContainsAny(tag, "*?[]\\"):
			return Decision{Reason: fmt.Sprintf("%s may not %s %q: a target names a tag, not a pattern", pr, perm, target)}
		default:
			target = tag
		}
	}
	held := false
	var outside []string
	for _, g := range gs {
		if !p.roles[g.Role][perm] {
			continue
		}
		held = true
		if !areaScoped[perm] || len(g.Areas) == 0 && target != "" {
			return Decision{Allowed: true}
		}
		if target == "" || target == All {
			outside = append(outside, g.Areas...)
			continue
		}
		for _, a := range g.Areas {
			if p.inArea(a, target) {
				return Decision{Allowed: true}
			}
		}
		outside = append(outside, g.Areas...)
	}
	switch {
	case !held:
		return Decision{Reason: fmt.Sprintf("%s (%s) may not %s", pr, p.roleNames(gs), perm)}
	case target == "":
		return Decision{Reason: fmt.Sprintf("%s may not %s: no tag named", pr, perm)}
	case target == All:
		return Decision{Reason: fmt.Sprintf("%s may %s only in %s, not every tag", pr, perm, strings.Join(dedupe(outside), ", "))}
	}
	return Decision{Reason: fmt.Sprintf("%s may not %s %s: it is outside %s", pr, perm, raw, strings.Join(dedupe(outside), ", "))}
}

// Holds reports whether pr holds perm anywhere: on the whole controller
// or in some area. A request's credential is checked against it before
// its body is read; Can, with the target, decides.
func (p *Policy) Holds(pr Principal, perm string) bool {
	for _, g := range p.grants(pr) {
		if p.roles[g.Role][lower(perm)] {
			return true
		}
	}
	return false
}

// tagOf is the tag a target names: lowercased, without spaces, member or
// index ("Pump_1.Speed" → "pump_1").
func tagOf(target string) string {
	tag := lower(target)
	if i := strings.IndexAny(tag, ".["); i >= 0 {
		tag = tag[:i]
	}
	return strings.TrimSpace(tag)
}

// roleList names the roles, for an error that must not echo the value it
// refuses (a secret pasted into the wrong field).
func (p *Policy) roleList() string {
	var names []string
	for _, b := range builtins {
		names = append(names, b.name)
	}
	var custom []string
	for n := range p.roles {
		if builtinRoles[n] == nil {
			custom = append(custom, n)
		}
	}
	sort.Strings(custom)
	return "the roles are " + strings.Join(append(names, custom...), ", ")
}

// inArea reports whether tag (from tagOf) matches one of area's patterns.
func (p *Policy) inArea(area, tag string) bool {
	for _, pat := range p.areas[area] {
		if ok, _ := path.Match(pat, tag); ok {
			return true
		}
	}
	return false
}

func (p *Policy) roleNames(gs []Grant) string {
	var out []string
	for _, g := range gs {
		out = append(out, g.Role)
	}
	return strings.Join(dedupe(out), ", ")
}

// Me is what a principal may do, for GET /api/me: its roles, and its
// permissions on the whole controller ("*") and per area.
type Me struct {
	Principal   string              `json:"principal"`
	Roles       []string            `json:"roles"`
	Permissions map[string][]string `json:"permissions"`
}

// Describe is pr's Me.
func (p *Policy) Describe(pr Principal) Me {
	me := Me{Principal: pr.String(), Roles: []string{}, Permissions: map[string][]string{}}
	held := map[string]map[string]bool{}
	add := func(scope, perm string) {
		if held[scope] == nil {
			held[scope] = map[string]bool{}
		}
		held[scope][perm] = true
	}
	gs := p.grants(pr)
	for _, g := range gs {
		for perm := range p.roles[g.Role] {
			if !areaScoped[perm] || len(g.Areas) == 0 {
				add(All, perm)
				continue
			}
			for _, a := range g.Areas {
				add(a, perm)
			}
		}
	}
	if len(gs) > 0 {
		me.Roles = strings.Split(p.roleNames(gs), ", ")
	}
	for scope, set := range held {
		for _, perm := range Permissions {
			if set[perm] && (scope == All || !held[All][perm]) {
				me.Permissions[scope] = append(me.Permissions[scope], perm)
			}
		}
	}
	return me
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
