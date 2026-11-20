package auth

import (
	"fmt"
	"path"
	"slices"
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
	// Users are the local users' grants (credentials are never here).
	Users map[string][]Grant
	// OIDC says the block signs browsers in at an OpenID Connect provider
	// (auth.oidc, #708 PR 6). OIDCGroups grant by group id (an Entra object
	// id, a Keycloak group path: what the provider's groups claim carries,
	// matched exactly, never a display name); OIDCUsers grant by name (the
	// username claim, matched ignoring case, and pinned to one account at
	// the provider on its first sign-in). An OIDC sign-in gets both, never
	// a local user's grants.
	OIDC       bool
	OIDCGroups map[string][]Grant
	OIDCUsers  map[string][]Grant
	// ServiceTokens are named tokens for scripts and services, each with
	// its grants, like a user's. Their secrets are not here either.
	ServiceTokens map[string][]Grant
	// OPCUACerts are OPC UA client certificates mapped to grants, like a
	// service token's, by name (#708 PR 8a). An OPC UA session from a
	// trusted certificate the block does not map writes nothing; it signs
	// in with a user name to write.
	OPCUACerts map[string]CertGrant
	// Sparkplug is what a Sparkplug command (NCMD/DCMD) may do, like a
	// service token's grants (#708 PR 8b). Nil grants nothing: under an
	// auth: block every command write is refused (Node Control/Rebirth is
	// not a write). The grants hold for every command, whoever sent it:
	// the broker does not say who that was.
	Sparkplug []Grant
	// Session is how long a browser sign-in lasts, from the moment of
	// sign-in (absolute, not renewed by use): 0 is DefaultSession.
	Session time.Duration
	// Elevation is how long a browser sign-in's password counts for the
	// elevated permissions (Elevated), from the sign-in or the last
	// re-authentication (fixed, not slid by use): 0 is DefaultElevation.
	Elevation time.Duration
}

// DefaultSession is a sign-in's lifetime when auth.session is not set:
// one shift plus handover.
const DefaultSession = 12 * time.Hour

// MinSession and MaxSession bound auth.session.
const (
	MinSession = time.Minute
	MaxSession = 7 * 24 * time.Hour
)

// DefaultElevation is how long a password counts for an elevated
// permission when auth.elevation is not set.
const DefaultElevation = 15 * time.Minute

// MinElevation and MaxElevation bound auth.elevation. One at or past the
// session's lifetime is allowed: the sign-in then counts for the whole
// session.
const (
	MinElevation = time.Minute
	MaxElevation = 12 * time.Hour
)

// RoleDef is a custom role.
type RoleDef struct {
	Inherits    string
	Allow, Deny []string
}

// CertGrant maps one certificate to grants: by Thumbprint, the SHA-1 of
// its DER in hex (40 digits, as naut opcua certs prints it), or by
// Subject, its subject's distinguished name exactly as naut opcua certs
// prints it ("CN=SCADA,O=Plant"). Exactly one of the two.
type CertGrant struct {
	Thumbprint string
	Subject    string
	Grants     []Grant
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
	certs     map[string][]Grant // opcua-certificates' grants, by name
	certThumb map[string]string  // lowercase thumbprint to a certificate's name
	certSubj  map[string]string  // subject to a certificate's name
	sparkplug []Grant            // auth.sparkplug's grants, for every Sparkplug command
	oidc      bool
	oidcGroup map[string][]Grant // auth.oidc.groups, by group id exactly as given
	oidcUsers map[string][]Grant // auth.oidc.users, by lowercased name
	warnings  []string
	session   time.Duration
	elevation time.Duration
}

// Compile checks c and builds its Policy. Every name it refers to must
// exist, and every pattern must be valid: a typo is an error at load, not
// a grant that silently matches nothing or everything.
func Compile(c Settings) (*Policy, error) {
	p := &Policy{roles: map[string]map[string]bool{}, areas: map[string][]string{},
		services: map[string][]Grant{}, users: map[string][]Grant{},
		certs: map[string][]Grant{}, certThumb: map[string]string{}, certSubj: map[string]string{},
		oidcGroup: map[string][]Grant{}, oidcUsers: map[string][]Grant{}}
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
	p.elevation = c.Elevation
	if p.elevation == 0 {
		p.elevation = DefaultElevation
	}
	if p.elevation < MinElevation || p.elevation > MaxElevation {
		return nil, fmt.Errorf("auth: elevation %s is out of range: give 1m to 12h (default 15m)", c.Elevation)
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
	if err := p.compileCerts(c.OPCUACerts); err != nil {
		return nil, err
	}
	if c.Sparkplug != nil {
		if len(c.Sparkplug) == 0 {
			return nil, fmt.Errorf("auth: sparkplug grants no role: give one (sparkplug: operator), or remove the key to refuse every Sparkplug command write")
		}
		// Checked as a service token's, under one name: the block's key.
		sp := map[string][]Grant{}
		if err := p.compileGrants("sparkplug", func(string) error { return nil },
			map[string][]Grant{"sparkplug": c.Sparkplug}, sp); err != nil {
			return nil, err
		}
		p.sparkplug = sp["sparkplug"]
	}
	if err := p.compileOIDC(c); err != nil {
		return nil, err
	}
	return p, nil
}

// compileOIDC checks auth.oidc's grants: names as ValidOIDCName says,
// group ids as given (one with a space is warned about: it looks like a
// display name, which a groups claim never carries and which anyone who
// may name a group could copy).
func (p *Policy) compileOIDC(c Settings) error {
	if !c.OIDC {
		if len(c.OIDCGroups) > 0 || len(c.OIDCUsers) > 0 {
			return fmt.Errorf("auth: OIDC grants without an OIDC provider")
		}
		return nil
	}
	p.oidc = true
	if len(c.OIDCGroups) == 0 && len(c.OIDCUsers) == 0 {
		p.warnings = append(p.warnings, "auth.oidc grants nobody anything yet, so every OIDC sign-in is refused: map a group id under auth.oidc.groups, or a name under auth.oidc.users")
	}
	oidcName := func(n string) error {
		if err := ValidOIDCName(n); err != nil {
			return fmt.Errorf("auth: oidc.users: %v", err)
		}
		return nil
	}
	if err := p.compileGrants("oidc user", oidcName, c.OIDCUsers, p.oidcUsers); err != nil {
		return err
	}
	ids := make([]string, 0, len(c.OIDCGroups))
	for id := range c.OIDCGroups {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if strings.TrimSpace(id) == "" || strings.ContainsFunc(id, func(r rune) bool { return r < ' ' || r == 0x7f }) {
			return fmt.Errorf("auth: oidc.groups: a group id that is empty or holds a control character")
		}
		// Checked as grants under a placeholder name, then kept under the
		// id exactly: compileGrants lowercases its keys.
		tmp := map[string][]Grant{}
		if err := p.compileGrants("oidc group", func(string) error { return nil },
			map[string][]Grant{"g": c.OIDCGroups[id]}, tmp); err != nil {
			return fmt.Errorf("%s (group %q)", err, id)
		}
		p.oidcGroup[id] = tmp["g"]
		if strings.ContainsRune(id, ' ') {
			p.warnings = append(p.warnings, fmt.Sprintf("auth.oidc.groups %q holds a space: it looks like a group's display name, but a group is matched by its id (an Entra object id, a Keycloak group path), as the ID token's groups claim carries it", id))
		}
	}
	return nil
}

// Warnings are what Compile found odd but not wrong, for naut check and
// naut run to print.
func (p *Policy) Warnings() []string { return slices.Clone(p.warnings) }

// OIDC reports whether the block signs browsers in at an OpenID Connect
// provider (auth.oidc).
func (p *Policy) OIDC() bool { return p.oidc }

// HasGrants reports whether the block grants pr anything at all: an OIDC
// sign-in that none of auth.oidc.users and auth.oidc.groups grants a role
// is refused.
func (p *Policy) HasGrants(pr Principal) bool { return len(p.grants(pr)) > 0 }

// compileCerts checks opcua-certificates: each a valid name, matched by
// exactly one of a full thumbprint (40 hex digits) or a subject, no two
// matching the same certificate, with grants checked as a service token's.
func (p *Policy) compileCerts(in map[string]CertGrant) error {
	grants := map[string][]Grant{}
	for name, c := range in {
		certName := func(n string) error { return validName("opcua certificate", n) }
		if err := certName(lower(name)); err != nil {
			return err
		}
		thumb, subj := lower(c.Thumbprint), strings.TrimSpace(c.Subject)
		switch {
		case thumb == "" && subj == "":
			return fmt.Errorf("auth: opcua certificate %q names no thumbprint or subject", name)
		case thumb != "" && subj != "":
			return fmt.Errorf("auth: opcua certificate %q names both a thumbprint and a subject: give one", name)
		case thumb != "":
			if len(thumb) != 40 || strings.Trim(thumb, "0123456789abcdef") != "" {
				return fmt.Errorf("auth: opcua certificate %q: thumbprint is not 40 hex digits (the SHA-1 naut opcua certs prints)", name)
			}
			if other, dup := p.certThumb[thumb]; dup {
				return fmt.Errorf("auth: opcua certificates %q and %q name the same thumbprint", other, name)
			}
			p.certThumb[thumb] = lower(name)
		default:
			if other, dup := p.certSubj[subj]; dup {
				return fmt.Errorf("auth: opcua certificates %q and %q name the same subject", other, name)
			}
			p.certSubj[subj] = lower(name)
		}
		grants[name] = c.Grants
	}
	return p.compileGrants("opcua certificate", func(string) error { return nil }, grants, p.certs)
}

// OPCUACertFor is the name the block maps a certificate to, by its
// thumbprint (hex SHA-1, any case) first, then its subject: false when
// the block names neither.
func (p *Policy) OPCUACertFor(thumbprint, subject string) (string, bool) {
	if n, ok := p.certThumb[lower(thumbprint)]; ok && thumbprint != "" {
		return n, true
	}
	if n, ok := p.certSubj[strings.TrimSpace(subject)]; ok && subject != "" {
		return n, true
	}
	return "", false
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
	case KindOPCUACert:
		return p.certs[lower(pr.Name)]
	case KindSparkplug:
		return p.sparkplug
	case KindOIDC:
		if !p.oidc {
			return nil
		}
		gs := slices.Clone(p.oidcUsers[lower(pr.Name)])
		for _, id := range pr.Groups() {
			gs = append(gs, p.oidcGroup[id]...)
		}
		return gs
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

// Elevation is how long a browser sign-in's password counts for an
// elevated permission (auth.elevation), from the sign-in or the last
// re-authentication.
func (p *Policy) Elevation() time.Duration { return p.elevation }

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
	// Areas are the tag patterns of each area Permissions names, so a page
	// can tell ahead of time whether a tag is in one (matched as Can
	// matches: path.Match on the lowercased tag, member and index cut).
	// Only the caller's own areas; omitted when it holds none.
	Areas map[string][]string `json:"areas,omitempty"`
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
	for scope := range me.Permissions {
		if scope == All {
			continue
		}
		if me.Areas == nil {
			me.Areas = map[string][]string{}
		}
		me.Areas[scope] = append([]string(nil), p.areas[scope]...)
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
