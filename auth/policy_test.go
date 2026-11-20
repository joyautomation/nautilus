package auth

import (
	"fmt"
	"strings"
	"testing"
)

// The design's example block (§2), with a custom role and a service token.
var plant = Settings{
	TokenRole: "",
	Areas: map[string][]string{
		"receiving": {"RCV_*", "PIT_*"},
		"roasting":  {"RST_*", "BURNER_*"},
	},
	Roles: map[string]RoleDef{
		"lead-operator": {Inherits: Operator, Allow: []string{Shelve}},
		"no-sfc":        {Inherits: Operator, Deny: []string{SFC}},
	},
	Users: map[string][]Grant{
		"jsmith": {{Role: Operator, Areas: []string{"receiving"}}},
		"akhan":  {{Role: Engineer}},
		"pat":    {{Role: "lead-operator", Areas: []string{"roasting"}}, {Role: Viewer}},
		"nosfc":  {{Role: "no-sfc"}},
	},
	ServiceTokens: map[string][]Grant{"historian": {{Role: Viewer}}, "pit_script": {{Role: Operator, Areas: []string{"receiving"}}}},
}

func mustCompile(t *testing.T, c Settings) *Policy {
	t.Helper()
	p, err := Compile(c)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// Every built-in role against every permission, on the whole controller.
func TestBuiltInRoles(t *testing.T) {
	want := map[string]string{
		Viewer:        "read",
		Operator:      "read write ack sfc",
		Supervisor:    "read write ack shelve substitute sfc audit",
		Engineer:      "read write ack shelve substitute force sfc edit activate deploy config audit",
		Administrator: strings.Join(Permissions, " "),
	}
	for role, perms := range want {
		p := mustCompile(t, Settings{Users: map[string][]Grant{"u": {{Role: role}}}})
		var got []string
		for _, perm := range Permissions {
			if p.Can(User("u"), perm, "ANY_TAG").Allowed {
				got = append(got, perm)
			}
		}
		if strings.Join(got, " ") != perms {
			t.Errorf("%s may %q, want %q", role, strings.Join(got, " "), perms)
		}
	}
}

func TestCan(t *testing.T) {
	p := mustCompile(t, plant)
	for _, tc := range []struct {
		pr           Principal
		perm, target string
		ok           bool
		reason       string
	}{
		// NAUTILUS_TOKEN is the administrator (Q3).
		{Token, Force, "RST_1", true, ""},
		{Token, Users, "", true, ""},
		{Token, Force, All, true, ""},
		// Anonymous may do nothing by default (Q1: sign-in required).
		{Anonymous, Read, "", false, "anonymous has no role here"},
		{Anonymous, Write, "RCV_1", false, "anonymous has no role here"},
		// A viewer service token reads, and nothing else.
		{Service("historian"), Read, "", true, ""},
		{Service("HISTORIAN"), Read, "", true, ""},
		{Service("historian"), Write, "RCV_1", false, "token:historian (viewer) may not write"},
		{Service("unknown"), Read, "", false, "token:unknown has no role here"},
		// A service token scoped to an area, like a user.
		{Service("pit_script"), Write, "PIT_LEVEL", true, ""},
		{Service("pit_script"), Write, "RST_TEMP", false, "token:pit_script may not write RST_TEMP: it is outside receiving"},
		// An operator in receiving writes there, by tag, member or index.
		{User("jsmith"), Write, "RCV_FLOW", true, ""},
		{User("jsmith"), Write, "rcv_flow.SP", true, ""},
		{User("jsmith"), Write, "PIT_3[2]", true, ""},
		{User("jsmith"), Ack, "PIT_LEVEL", true, ""},
		{User("jsmith"), Write, "RST_TEMP", false, "user:jsmith may not write RST_TEMP: it is outside receiving"},
		{User("jsmith"), Write, "", false, "user:jsmith may not write: no tag named"},
		{User("jsmith"), Write, All, false, "user:jsmith may write only in receiving, not every tag"},
		{User("jsmith"), Force, "RCV_FLOW", false, "user:jsmith (operator) may not force"},
		// Not area-scoped: the target does not matter.
		{User("jsmith"), SFC, "", true, ""},
		{User("jsmith"), Edit, "", false, "user:jsmith (operator) may not edit"},
		// Whole-controller grants hold everywhere, All included, but not
		// with no target named.
		{User("akhan"), Force, "RST_1", true, ""},
		{User("akhan"), Force, All, true, ""},
		{User("akhan"), Force, "", false, "user:akhan may not force: no tag named"},
		{User("akhan"), Users, "", false, "user:akhan (engineer) may not users"},
		// Custom roles, and grants combined.
		{User("pat"), Shelve, "BURNER_2", true, ""},
		{User("pat"), Shelve, "RCV_1", false, "outside roasting"},
		{User("pat"), Read, "", true, ""},
		{User("nosfc"), SFC, "", false, "may not sfc"},
		{User("nosfc"), Write, "X", true, ""},
		// Unknown users and permissions fail closed.
		{User("mallory"), Read, "", false, "user:mallory has no role here"},
		{Token, "launch", "", false, `no permission "launch"`},
	} {
		d := p.Can(tc.pr, tc.perm, tc.target)
		if d.Allowed != tc.ok || !strings.Contains(d.Reason, tc.reason) {
			t.Errorf("%s %s %q = %v %q, want %v %q", tc.pr, tc.perm, tc.target, d.Allowed, d.Reason, tc.ok, tc.reason)
		}
		if d.Allowed && !p.Holds(tc.pr, tc.perm) {
			t.Errorf("%s may %s %q but does not hold it", tc.pr, tc.perm, tc.target)
		}
	}
	if !p.Holds(User("jsmith"), Write) || p.Holds(User("jsmith"), Force) || p.Holds(Anonymous, Read) {
		t.Error("Holds")
	}
}

func TestAnonymousViewer(t *testing.T) {
	p := mustCompile(t, Settings{Anonymous: "Viewer"})
	if !p.Can(Anonymous, Read, "").Allowed || p.Can(Anonymous, Write, "X").Allowed {
		t.Error("anonymous: viewer reads and writes nothing")
	}
}

func TestTokenRole(t *testing.T) {
	p := mustCompile(t, Settings{TokenRole: "operator"})
	if !p.Can(Token, Write, "X").Allowed || p.Can(Token, Force, "X").Allowed {
		t.Error("token-role: operator")
	}
}

// A typo is an error at load, never a grant that matches nothing or
// everything.
func TestCompileRefuses(t *testing.T) {
	for _, tc := range []struct {
		c    Settings
		want string
	}{
		{Settings{Anonymous: "operator"}, `anonymous: use none or viewer`},
		{Settings{TokenRole: "admin"}, `token-role is not a role: the roles are viewer, operator`},
		{Settings{Areas: map[string][]string{"a": {}}}, `area "a" names no tags`},
		{Settings{Areas: map[string][]string{"a": {"[x"}}}, `area "a": tag pattern "[x"`},
		{Settings{Areas: map[string][]string{"bad name": {"X"}}}, `area "bad name"`},
		{Settings{Roles: map[string]RoleDef{"operator": {Inherits: Viewer}}}, `role "operator" is built in`},
		{Settings{Roles: map[string]RoleDef{"r": {Inherits: "nobody"}}}, `role "r" inherits something that is not a role`},
		{Settings{Roles: map[string]RoleDef{"r": {}}}, `role "r" inherits nothing`},
		{Settings{Roles: map[string]RoleDef{"r": {Inherits: Viewer, Allow: []string{"fly"}}}}, `role "r": no permission "fly"`},
		{Settings{Roles: map[string]RoleDef{"a": {Inherits: "b"}, "b": {Inherits: "a"}}}, "inherit in a cycle"},
		{Settings{Users: map[string][]Grant{"u": nil}}, `user "u" has no role`},
		{Settings{Users: map[string][]Grant{"u": {{Role: "boss"}}}}, `user "u": a grant's role is not a role`},
		{Settings{Users: map[string][]Grant{"u": {{Role: Operator, Areas: []string{"nowhere"}}}}}, `user "u": no area "nowhere"`},
		{Settings{ServiceTokens: map[string][]Grant{"h": {{Role: "reader"}}}}, `service token "h": a grant's role is not a role`},
		{Settings{ServiceTokens: map[string][]Grant{"h": {{Role: Operator, Areas: []string{"nowhere"}}}}}, `service token "h": no area "nowhere"`},
		{Settings{ServiceTokens: map[string][]Grant{"h": nil}}, `service token "h" has no role`},
		{Settings{ServiceTokens: map[string][]Grant{"h": {{Role: Operator, Areas: []string{}}}}}, `areas: [] covers nothing`},
		// F2: an explicit empty areas list covers nothing; it must not
		// load as the whole controller.
		{Settings{Users: map[string][]Grant{"u": {{Role: Operator, Areas: []string{}}}}}, `areas: [] covers nothing`},
		// F4: a token's name is its secret's variable's name.
		{Settings{ServiceTokens: map[string][]Grant{"pit-script": {{Role: Viewer}}}}, `use letters, digits and '_'`},
		{Settings{ServiceTokens: map[string][]Grant{"a.b": {{Role: Viewer}}}}, `use letters, digits and '_'`},
		// F5: an area covers tags; a member pattern could never match.
		{Settings{Areas: map[string][]string{"a": {"PUMP_1.Speed"}}}, `names a member`},
		{Settings{Users: map[string][]Grant{"A": {{Role: Viewer}}, "a": {{Role: Viewer}}}}, "declared twice"},
	} {
		_, err := Compile(tc.c)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("Compile(%+v) = %v, want %q", tc.c, err, tc.want)
		}
	}
	// F6: a refused value is never echoed: it may be a secret pasted into
	// the wrong field.
	const secret = "s3cr3t-pasted-here"
	for _, c := range []Settings{
		{Anonymous: secret},
		{TokenRole: secret},
		{Roles: map[string]RoleDef{"r": {Inherits: secret}}},
		{Users: map[string][]Grant{"u": {{Role: secret}}}},
		{ServiceTokens: map[string][]Grant{"h": {{Role: secret}}}},
	} {
		if _, err := Compile(c); err == nil || strings.Contains(err.Error(), secret) {
			t.Errorf("Compile(%+v) = %v: refused, without echoing the value", c, err)
		}
	}
}

// F3: Can decides the target once. Only All is every tag; a padded,
// member-only, index-only or patterned target is refused, even under an
// area whose pattern would match it.
func TestCanNormalisesTheTarget(t *testing.T) {
	p := mustCompile(t, Settings{
		Areas: map[string][]string{"every": {"*"}},
		Users: map[string][]Grant{"u": {{Role: Engineer, Areas: []string{"every"}}}},
	})
	for _, target := range []string{" *", "* ", "[0]", ".x", " ", "PUMP_*", "PUMP_?", "[ab]", All} {
		if d := p.Can(User("u"), Force, target); d.Allowed {
			t.Errorf("force %q under a '*' area was allowed", target)
		}
	}
	for _, target := range []string{"PUMP_1", " PUMP_1 ", "pump_1.Speed", "PUMP_1[2]"} {
		if d := p.Can(User("u"), Force, target); !d.Allowed {
			t.Errorf("force %q: %s", target, d.Reason)
		}
	}
}

func TestDescribe(t *testing.T) {
	p := mustCompile(t, plant)
	got := fmt.Sprint(p.Describe(User("pat")))
	want := "{user:pat [lead-operator viewer] map[*:[read sfc] roasting:[write ack shelve]]}"
	if got != want {
		t.Errorf("Describe(pat) = %s, want %s", got, want)
	}
	if got := fmt.Sprint(p.Describe(Anonymous)); got != "{anonymous [] map[]}" {
		t.Errorf("Describe(anonymous) = %s", got)
	}
	if me := p.Describe(Token); len(me.Permissions[All]) != len(Permissions) {
		t.Errorf("Describe(token) = %v", me)
	}
}

func TestPrincipalString(t *testing.T) {
	for pr, want := range map[Principal]string{Anonymous: "anonymous", {}: "anonymous", Token: "token", Service("h"): "token:h", User("j"): "user:j"} {
		if pr.String() != want {
			t.Errorf("%#v = %q, want %q", pr, pr.String(), want)
		}
	}
}

// An area names tags: a member or index is matched by its tag, so an area
// of exact names covers their members; and All needs a grant over the
// whole controller, even when an area's pattern would match "*".
func TestAreaEdges(t *testing.T) {
	p := mustCompile(t, Settings{
		Areas: map[string][]string{"pump": {"PUMP_1"}, "every": {"*"}, "one": {"?"}},
		Users: map[string][]Grant{
			"u": {{Role: Operator, Areas: []string{"pump"}}},
			"w": {{Role: Engineer, Areas: []string{"every", "one"}}},
		},
	})
	for _, c := range []struct {
		pr           Principal
		perm, target string
		ok           bool
	}{
		{User("u"), Write, "PUMP_1", true},
		{User("u"), Write, "pump_1.Speed", true},
		{User("u"), Write, "PUMP_1[3]", true},
		{User("u"), Write, "PUMP_10", false},
		{User("w"), Force, "ANY", true},
		{User("w"), Force, All, false},
	} {
		if got := p.Can(c.pr, c.perm, c.target).Allowed; got != c.ok {
			t.Errorf("%s %s %q = %v, want %v", c.pr, c.perm, c.target, got, c.ok)
		}
	}
}

// The elevated permissions are exactly those that change the program, the
// I/O, a forced value or who may do what (#708 PR 5c): an operator's
// writes, acks and SFC commands, and a supervisor's shelving and
// substitutes, never ask for the password again.
func TestElevatedPermissions(t *testing.T) {
	want := map[string]bool{Force: true, Edit: true, Activate: true, Deploy: true, Config: true, Users: true, Override: true}
	for _, p := range Permissions {
		if Elevated(p) != want[p] {
			t.Errorf("Elevated(%q) = %v, want %v", p, Elevated(p), want[p])
		}
	}
	for _, p := range ElevatedPermissions {
		if !IsPermission(p) {
			t.Errorf("elevated %q is not a permission", p)
		}
	}
	if Elevated("") || Elevated("FORCE") {
		t.Error("only a permission's own name is elevated")
	}
}
