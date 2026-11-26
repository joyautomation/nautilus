package project

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
	"time"

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
  sparkplug: [{role: operator, areas: [receiving]}]
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
		// auth.sparkplug: every Sparkplug command, whatever its label.
		{auth.Sparkplug("scada-1"), auth.Write, "RCV_FLOW", true},
		{auth.Sparkplug("other-host"), auth.Write, "PIT_LEVEL", true},
		{auth.Sparkplug("scada-1"), auth.Write, "OTHER", false},
		{auth.Sparkplug("scada-1"), auth.Force, "RCV_FLOW", false},
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
		{"auth:\n  elevation: -1m\n", "elevation -1m0s is out of range"},
		{"auth:\n  elevation: 30s\n", "elevation 30s is out of range"},
		{"auth:\n  elevation: 12h1m\n", "elevation 12h1m0s is out of range"},
		{"auth:\n  elevation: soon\n", "bad duration"},
		{"auth:\n  session: -1h\n", "session -1h0m0s is out of range"},
		{"auth:\n  session: 30s\n", "session 30s is out of range"},
		{"auth:\n  session: 169h\n", "session 169h0m0s is out of range"},
		{"auth:\n  session: twelve\n", "bad duration"},
		{"auth:\n  roles:\n    r: {inherits: viewer, alow: [write]}\n", "field alow not found"},
		// A present auth: key with no value is not "no block": it would
		// load as an open controller, silently (#1071 review).
		// opcua-certificates (#708 PR 8a): a mapping must name one
		// certificate exactly, with real roles and areas.
		{"auth:\n  opcua-certificates:\n    scada: {thumbprint: 9f86d081884c7d659a2feaa0c55ad015a3bf4f1b, grants: boss}\n", `opcua certificate "scada": a grant's role is not a role`},
		{"auth:\n  opcua-certificates:\n    scada: {thumbprint: 9f86d081884c7d659a2feaa0c55ad015a3bf4f1b, grants: [{role: operator, areas: [nowhere]}]}\n", `no area "nowhere"`},
		{"auth:\n  opcua-certificates:\n    scada: {grants: operator}\n", "names no thumbprint or subject"},
		// auth.sparkplug (#708 PR 8b): grants as a service token's.
		{"auth:\n  sparkplug: boss\n", `sparkplug "sparkplug": a grant's role is not a role`},
		{"auth:\n  sparkplug: [{role: operator, areas: [nowhere]}]\n", `no area "nowhere"`},
		{"auth:\n  sparkplug: []\n", "sparkplug grants no role"},
		{"auth:\n  sparkplug: [{role: operator, area: [x]}]\n", "field area not found"},
		{"auth:\n  opcua-certificates:\n    scada: {thumbprint: 9f86d081884c7d659a2feaa0c55ad015a3bf4f1b, subject: CN=x, grants: operator}\n", "both a thumbprint and a subject"},
		{"auth:\n  opcua-certificates:\n    scada: {thumbprint: 9f86d081884c7d65, grants: operator}\n", "not 40 hex digits"},
		{"auth:\n  opcua-certificates:\n    scada: {thumbprint: 9f86d081884c7d659a2feaa0c55ad015a3bf4f1b}\n", `opcua certificate "scada" has no role`},
		{"auth:\n  opcua-certificates:\n    a: {thumbprint: 9f86d081884c7d659a2feaa0c55ad015a3bf4f1b, grants: operator}\n    b: {thumbprint: 9f86d081884c7d659a2feaa0c55ad015a3bf4f1b, grants: viewer}\n", "name the same thumbprint"},
		{"auth:\n  opcua-certificates:\n    scada: {thumprint: 9f86d081884c7d659a2feaa0c55ad015a3bf4f1b, grants: operator}\n", "field thumprint not found"},
		{"opcua:\n  anonymous-writes: true\nauth: {}\n", "opcua.anonymous-writes cannot be used with an auth: block"},
		{"auth:\n", "auth: is empty"},
		{"auth: ~\n", "auth: is empty"},
		{"auth: null\n", "auth: is empty"},
		{"auth:\n  # users:\n  #   jsmith: [operator]\n", "auth: is empty"},
	} {
		_, err := Load(authProject(c.block), "")
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: %v, want %q", c.block, err, c.want)
		}
	}
}

// opcua-certificates maps an OPC UA client certificate, by thumbprint
// (any case) or subject, to a name whose grants answer as a service
// token's would; a certificate it does not name maps to nothing.
func TestLoadAuthOPCUACertificates(t *testing.T) {
	p, err := Load(authProject(`auth:
  areas:
    receiving: {tags: ["RCV_*"]}
  opcua-certificates:
    scada: {thumbprint: 9F86D081884C7D659A2FEAA0C55AD015A3BF4F1B, grants: [{role: operator, areas: [receiving]}]}
    historian: {subject: "CN=Historian,O=Plant", grants: viewer}
`), "")
	if err != nil {
		t.Fatal(err)
	}
	if n, ok := p.Auth.OPCUACertFor("9f86d081884c7d659a2feaa0c55ad015a3bf4f1b", "CN=whatever"); !ok || n != "scada" {
		t.Errorf("by thumbprint: %q %v", n, ok)
	}
	if n, ok := p.Auth.OPCUACertFor("0000000000000000000000000000000000000000", "CN=Historian,O=Plant"); !ok || n != "historian" {
		t.Errorf("by subject: %q %v", n, ok)
	}
	// A certificate matching both a thumbprint mapping and a subject
	// mapping is the thumbprint's.
	if n, ok := p.Auth.OPCUACertFor("9f86d081884c7d659a2feaa0c55ad015a3bf4f1b", "CN=Historian,O=Plant"); !ok || n != "scada" {
		t.Errorf("thumbprint and subject both mapped: %q %v, want scada", n, ok)
	}
	for _, c := range [][2]string{{"", ""}, {"9f86d081884c7d65", ""}, {"", "CN=historian,O=Plant"}, {"", "CN=Historian"}} {
		if n, ok := p.Auth.OPCUACertFor(c[0], c[1]); ok {
			t.Errorf("%q %q maps to %q", c[0], c[1], n)
		}
	}
	for _, c := range []struct {
		pr     auth.Principal
		target string
		ok     bool
	}{
		{auth.OPCUACert("scada"), "RCV_FLOW", true},
		{auth.OPCUACert("scada"), "OTHER", false},
		{auth.OPCUACert("historian"), "RCV_FLOW", false},
		{auth.OPCUACert("nobody"), "RCV_FLOW", false},
	} {
		if got := p.Auth.Can(c.pr, auth.Write, c.target).Allowed; got != c.ok {
			t.Errorf("%s write %s = %v, want %v", c.pr, c.target, got, c.ok)
		}
	}
	if got := auth.OPCUACert("scada").String(); got != "opcua-cert:scada" {
		t.Errorf("the journal's name: %q", got)
	}
	// opcua.anonymous-writes without an auth: block still loads.
	if _, err := Load(authProject("opcua:\n  anonymous-writes: true\n"), ""); err != nil {
		t.Errorf("anonymous-writes without auth: %v", err)
	}
}

// An empty block (auth: {}) is a policy, not an open controller: nobody
// may do anything, anonymous least of all. No auth: key at all stays open.
func TestLoadAuthEmptyBlockClosesEverything(t *testing.T) {
	p, err := Load(authProject("auth: {}\n"), "")
	if err != nil || p.Auth == nil {
		t.Fatalf("auth: {}: %v, policy %v", err, p)
	}
	if p.Auth.Holds(auth.Anonymous, auth.Read) || p.Auth.Holds(auth.User("jsmith"), auth.Read) {
		t.Error("auth: {} lets someone read")
	}
	if p, err := Load(authProject(""), ""); err != nil || p.Auth != nil {
		t.Errorf("no auth: key: %v, policy %v", err, p.Auth)
	}
}

// auth.session sets a sign-in's lifetime; left out (or 0) it is 12h.
func TestLoadAuthSession(t *testing.T) {
	for block, want := range map[string]time.Duration{
		"auth:\n  session: 8h\n":                    8 * time.Hour,
		"auth:\n  session: 0s\n":                    12 * time.Hour,
		"auth:\n  users:\n    jsmith: [operator]\n": 12 * time.Hour,
	} {
		p, err := Load(authProject(block), "")
		if err != nil {
			t.Fatalf("%q: %v", block, err)
		}
		if got := p.Auth.Session(); got != want {
			t.Errorf("%q: session %s, want %s", block, got, want)
		}
	}
}

// auth.elevation sets how long a password counts for an elevated
// permission; left out (or 0) it is 15m. One past the session is allowed:
// the sign-in then counts for the whole session.
func TestLoadAuthElevation(t *testing.T) {
	for block, want := range map[string]time.Duration{
		"auth:\n  elevation: 5m\n":                  5 * time.Minute,
		"auth:\n  elevation: 0s\n":                  15 * time.Minute,
		"auth:\n  users:\n    jsmith: [operator]\n": 15 * time.Minute,
		"auth:\n  elevation: 1m\n":                  time.Minute,
		"auth:\n  elevation: 12h\n  session: 1h\n":  12 * time.Hour,
	} {
		p, err := Load(authProject(block), "")
		if err != nil {
			t.Fatalf("%q: %v", block, err)
		}
		if got := p.Auth.Elevation(); got != want {
			t.Errorf("%q: elevation %s, want %s", block, got, want)
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
		{"oidc", AuthOIDC{}, func(m json.RawMessage) json.RawMessage { return m }},
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

// auth.oidc loads into a policy that grants by group id and by name, and
// a block that could never sign anyone in is refused with the fix.
func TestLoadAuthOIDC(t *testing.T) {
	block := "auth:\n  oidc:\n    issuer: https://login.example/realm\n    client-id: naut\n" +
		"    groups:\n      \"8f1c-ops\": operator\n      \"Plant Engineers\": engineer\n" +
		"    users:\n      JSmith@corp.example: [{role: viewer}]\n"
	p, err := Load(authProject(block), "")
	if err != nil {
		t.Fatal(err)
	}
	if p.AuthOIDC == nil || p.AuthOIDC.Issuer != "https://login.example/realm" || p.AuthOIDC.ClientID != "naut" {
		t.Fatalf("AuthOIDC %+v", p.AuthOIDC)
	}
	pol := p.Auth
	if !pol.OIDC() || !pol.Can(auth.OIDC("x", []string{"8f1c-ops"}), auth.Write, "RCV_FLOW").Allowed {
		t.Error("group 8f1c-ops does not write")
	}
	if pol.Can(auth.OIDC("x", []string{"8F1C-OPS"}), auth.Write, "RCV_FLOW").Allowed {
		t.Error("a group id matched in another case")
	}
	if !pol.Holds(auth.OIDC("jsmith@corp.example", nil), auth.Read) || pol.Holds(auth.User("jsmith@corp.example"), auth.Read) {
		t.Error("auth.oidc.users granted the wrong principal")
	}
	if w := pol.Warnings(); len(w) != 1 || !strings.Contains(w[0], "Plant Engineers") || !strings.Contains(w[0], "display name") {
		t.Errorf("warnings %v", w)
	}

	for _, c := range []struct{ block, want string }{
		{"auth:\n  oidc:\n    client-id: naut\n", "oidc.issuer is empty"},
		{"auth:\n  oidc:\n    issuer: http://login.example\n    client-id: naut\n", "give an https:// URL"},
		{"auth:\n  oidc:\n    issuer: https://login.example\n", "oidc.client-id is empty"},
		{"auth:\n  oidc:\n    issuer: https://login.example\n    client-id: n\n    redirect-url: https://plc.example/callback\n", "oidc.redirect-url"},
		{"auth:\n  oidc:\n    issuer: https://login.example\n    client-id: n\n    users:\n      \"j smith\": viewer\n", "OIDC name"},
		{"auth:\n  oidc:\n    issuer: https://login.example\n    client-id: n\n    groups:\n      g: boss\n", "not a role"},
		{"auth:\n  oidc:\n    issuer: https://login.example\n    client-id: n\n    secret: x\n", "field secret not found"},
	} {
		if _, err := Load(authProject(c.block), ""); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: err = %v, want %q", c.block, err, c.want)
		}
	}
	// No grant yet is a warning (naut auth init writes such a block), not
	// an error.
	p, err = Load(authProject("auth:\n  oidc:\n    issuer: https://login.example\n    client-id: n\n"), "")
	if err != nil || len(p.Auth.Warnings()) != 1 || !strings.Contains(p.Auth.Warnings()[0], "grants nobody") {
		t.Errorf("no grants: %v %v", err, p.Auth.Warnings())
	}
}

// auth.oidc runs on a redundant pair (#710): each controller loads the
// block and signs in at the provider itself; a name's pin comes to the
// other controller with the sign-in, through the operator log.
func TestLoadAuthOIDCOnAPair(t *testing.T) {
	fs := authProject("auth:\n  oidc:\n    issuer: https://login.example\n    client-id: n\n" + pairOK)
	p, err := Load(fs, "")
	if err != nil || p.AuthOIDC == nil || p.AuthOIDC.Issuer != "https://login.example" {
		t.Fatalf("err = %v", err)
	}
}
