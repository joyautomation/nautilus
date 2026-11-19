package project

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/joyautomation/nautilus/auth"
)

func authProject(block string) fstest.MapFS {
	return fstest.MapFS{
		"nautilus.yaml": &fstest.MapFile{Data: []byte("name: plant\ntasks:\n  - program: main.st\n    scan: 100ms\n" +
			"tags:\n  - { name: RCV_FLOW, role: setpoint, type: REAL, init: 1 }\n" + block)},
		"main.st": &fstest.MapFile{Data: []byte("PROGRAM Main\nEND_PROGRAM\n")},
	}
}

// The design's auth: block loads into a policy that answers as written.
func TestLoadAuthBlock(t *testing.T) {
	p, err := Load(authProject(`auth:
  anonymous: viewer
  token-role: engineer
  areas:
    receiving: {tags: ["RCV_*", "PIT_*"]}
  roles:
    lead-operator: {inherits: operator, allow: [shelve]}
  users:
    jsmith: [{role: operator, areas: [receiving]}]
    akhan: [engineer]
    pat: [lead-operator, {role: viewer}]
  service-tokens:
    historian: viewer
    pit_script: [{role: operator, areas: [receiving]}]
`), "")
	if err != nil {
		t.Fatal(err)
	}
	if p.Auth == nil {
		t.Fatal("no policy")
	}
	for _, c := range []struct {
		pr           auth.Principal
		perm, target string
		ok           bool
	}{
		{auth.User("jsmith"), auth.Write, "RCV_FLOW", true},
		{auth.User("jsmith"), auth.Write, "OTHER", false},
		{auth.User("akhan"), auth.Force, "OTHER", true},
		{auth.User("pat"), auth.Shelve, "OTHER", true},
		{auth.Token, auth.Force, "X", true},
		{auth.Token, auth.Users, "", false},
		{auth.Anonymous, auth.Read, "", true},
		{auth.Anonymous, auth.Write, "RCV_FLOW", false},
		{auth.Service("historian"), auth.Write, "RCV_FLOW", false},
		{auth.Service("pit_script"), auth.Write, "RCV_FLOW", true},
		{auth.Service("pit_script"), auth.Write, "OTHER", false},
	} {
		if got := p.Auth.Can(c.pr, c.perm, c.target).Allowed; got != c.ok {
			t.Errorf("%s %s %s = %v, want %v", c.pr, c.perm, c.target, got, c.ok)
		}
	}
	if p, err := Load(authProject(""), ""); err != nil || p.Auth != nil {
		t.Errorf("no auth: block: %v %v", p.Auth, err)
	}
}

// Mistakes fail the load (and so naut check), naming the manifest.
func TestLoadAuthRefuses(t *testing.T) {
	for _, c := range []struct{ block, want string }{
		{"auth:\n  users:\n    jsmith: [{role: operator, area: [receiving]}]\n", "field area not found in a grant"},
		{"auth:\n  users:\n    jsmith: [boss]\n", `nautilus.yaml: auth: user "jsmith": a grant's role is not a role`},
		// F1: a key given twice, which the decoder would refuse and the
		// last of which would otherwise win.
		{"auth:\n  users:\n    jsmith: [{role: viewer, role: administrator}]\n", "role is given twice in a grant"},
		{"auth:\n  areas:\n    receiving: {tags: [RCV_*]}\n  users:\n    jsmith: [{role: operator, areas: [receiving], areas: []}]\n", "areas is given twice in a grant"},
		// F2: an empty or null areas covers nothing; it must not load as
		// the whole controller.
		{"auth:\n  users:\n    jsmith: [{role: operator, areas: []}]\n", "areas is empty"},
		{"auth:\n  users:\n    jsmith: [{role: operator, areas: }]\n", "areas is empty"},
		{"auth:\n  users:\n    jsmith: [{role: operator, areas: null}]\n", "areas is empty"},
		{"auth:\n  state-dir: state/nautilus\n", "state-dir \"state/nautilus\" is relative"},
		{"auth:\n  users:\n    jsmith: [{areas: [x]}]\n", "a grant names no role"},
		// F4, F5 through the manifest.
		{"auth:\n  service-tokens:\n    pit-script: viewer\n", "use letters, digits and '_'"},
		{"auth:\n  areas:\n    p: {tags: [PUMP_1.Speed]}\n", "names a member"},
		{"auth:\n  service-tokens:\n    s: [{role: operator, areas: []}]\n", "areas is empty"},
		{"auth:\n  service-tokens:\n    s: [{role: viewer, role: administrator}]\n", "role is given twice"},
		{"auth:\n  areas:\n    r: {tags: []}\n", `area "r" names no tags`},
		{"auth:\n  anonymous: operator\n", `use none or viewer`},
		{"auth:\n  session: 12h\n", "field session not found"},
		{"auth:\n  roles:\n    r: {inherits: viewer, alow: [write]}\n", "field alow not found"},
	} {
		_, err := Load(authProject(c.block), "")
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: %v, want %q", c.block, err, c.want)
		}
	}
}

// The schema the editor validates against declares exactly the keys the
// loader takes, at every level of the block.
func TestSchemaAuthKeys(t *testing.T) {
	s := loadSchema(t)
	var top schemaNode
	if err := json.Unmarshal(s.Properties["auth"], &top); err != nil {
		t.Fatal(err)
	}
	if got, want := goKeys(AuthConfig{}), schemaKeys(top.Properties); !reflect.DeepEqual(got, want) {
		t.Errorf("auth: the loader accepts %v, the schema declares %v", got, want)
	}
	for _, c := range []struct {
		key string
		v   any
		sub func(json.RawMessage) json.RawMessage
	}{
		{"areas", AuthArea{}, additional},
		{"roles", AuthRole{}, additional},
		{"users", AuthGrant{}, func(m json.RawMessage) json.RawMessage {
			var n struct {
				Items struct {
					OneOf []json.RawMessage `json:"oneOf"`
				} `json:"items"`
			}
			json.Unmarshal(additional(m), &n)
			return n.Items.OneOf[1]
		}},
	} {
		var node schemaNode
		if err := json.Unmarshal(c.sub(top.Properties[c.key]), &node); err != nil {
			t.Fatalf("%s: %v", c.key, err)
		}
		if got, want := goKeys(c.v), schemaKeys(node.Properties); !reflect.DeepEqual(got, want) {
			t.Errorf("auth.%s: the loader accepts %v, the schema declares %v", c.key, got, want)
		}
	}
}

func additional(m json.RawMessage) json.RawMessage {
	var n struct {
		Additional json.RawMessage `json:"additionalProperties"`
	}
	json.Unmarshal(m, &n)
	return n.Additional
}
