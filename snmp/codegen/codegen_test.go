package codegen

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/snmp"
	"github.com/joyautomation/nautilus/snmp/walk"
)

func fixture(t *testing.T, name string) walk.Walk {
	t.Helper()
	raw, err := os.ReadFile("../testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	w, err := walk.ParseBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

// The rendered manifest decodes (KnownFields) back to exactly the manifest
// Generate built, for every profile — the renderer and the decoder agree
// on every key and every literal's Go type.
func TestManifestRoundTrips(t *testing.T) {
	for _, tc := range []struct{ fixture, tag string }{
		{"switch.snmpwalk", "SW1"}, {"ups.snmpwalk", "UPS1"}, {"pdu.snmpwalk", "PDU1"},
	} {
		out, err := Generate(fixture(t, tc.fixture), Options{Tag: tc.tag, Host: "192.0.2.2", Port: 1161})
		if err != nil {
			t.Fatal(err)
		}
		out.Manifest.Writes = []snmp.Write{{Name: tc.tag + "_Cmd", Tag: tc.tag, Member: "Name", OID: "1.3.6.1.2.1.1.5.0", Set: map[string]int64{"false": 2, "true": 1}}}
		body := ManifestYAML(out, "naut snmp import --tag "+tc.tag)
		back, err := snmp.ParseManifest(body)
		if err != nil {
			t.Fatalf("%s: %v\n%s", tc.fixture, err, body)
		}
		if !reflect.DeepEqual(back, out.Manifest) {
			for i := range back.Tags {
				if !reflect.DeepEqual(back.Tags[i], out.Manifest.Tags[i]) {
					t.Fatalf("%s: tag %s differs:\n%#v\n%#v", tc.fixture, back.Tags[i].Name, back.Tags[i], out.Manifest.Tags[i])
				}
			}
			t.Fatalf("%s: round trip differs:\n%#v\n%#v", tc.fixture, back.Sources, out.Manifest.Sources)
		}
		if string(ManifestYAML(out, "naut snmp import --tag "+tc.tag)) != string(body) {
			t.Fatalf("%s: a second render differs", tc.fixture)
		}
	}
}

func TestGenerateOptions(t *testing.T) {
	w := fixture(t, "ups.snmpwalk")
	for _, tc := range []struct {
		o    Options
		want string
	}{
		{Options{Tag: "SW_1", Host: "h"}, "letters and digits only"},
		{Options{Tag: "1SW", Host: "h"}, "letters and digits only"},
		{Options{Tag: "SW1"}, "--host is required"},
		{Options{Tag: "SW1", Host: "h", Version: "1"}, `--version "1"`},
		{Options{Tag: "SW1", Host: "h", Profile: "switch"}, "no ethernet ports"},
		{Options{Tag: "SW1", Host: "h", Ports: "x"}, "not an index"},
	} {
		if _, err := Generate(w, tc.o); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%+v: err = %v, want %q", tc.o, err, tc.want)
		}
	}

	out, err := Generate(w, Options{Tag: "Ups1", Host: "192.0.2.9", Version: "3", User: "nautilus", Auth: "sha256", Priv: "aes128"})
	if err != nil {
		t.Fatal(err)
	}
	s := out.Manifest.Sources[0]
	if s.AuthEnv != "SNMP_UPS1_AUTH" || s.PrivEnv != "SNMP_UPS1_PRIV" || s.CommunityEnv != "" || s.Port != 0 {
		t.Errorf("v3 source = %+v", s)
	}
	body := string(ManifestYAML(out, "x"))
	for _, want := range []string{"    version: \"3\"\n", "    auth: sha256\n", "    auth-env: SNMP_UPS1_AUTH\n", "    priv-env: SNMP_UPS1_PRIV\n"} {
		if !strings.Contains(body, want) {
			t.Errorf("v3 manifest missing %q:\n%s", want, body)
		}
	}
}

func TestTagsYAML(t *testing.T) {
	out, err := Generate(fixture(t, "pdu.snmpwalk"), Options{Tag: "PDU1", Host: "192.0.2.10"})
	if err != nil {
		t.Fatal(err)
	}
	m := out.Manifest
	m.Writes = []snmp.Write{{Name: "PDU1_Outlet03_Cmd", Tag: "PDU1_Outlet03", Member: "On", OID: "1.3.6.1.4.1.3808.1.1.3.3.3.1.1.4.3", Set: map[string]int64{"true": 1, "false": 2}}}
	body, err := TagsYAML(m, []string{"PDU1_Outlet08"})
	if err != nil {
		t.Fatal(err)
	}
	s := string(body)
	for _, want := range []string{
		`- { name: PDU1, role: input, type: PDU, desc: "a switched rack PDU" }`,
		`- { name: PDU1_Outlet03_Cmd, role: output, desc: "command → PDU1_Outlet03.On" }`,
		`- { name: PDU1__Online, role: input,`,
		`- { name: PDU1__LastPollMs, role: input, unit: "ms",`,
		"Not generated (declared by hand in the project): PDU1_Outlet08",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("tag file missing %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "name: PDU1_Outlet08,") {
		t.Error("a skipped tag was generated")
	}
	if _, err := TagsYAML(m, []string{"NOPE*"}); err == nil || !strings.Contains(err.Error(), "stale exclusion") {
		t.Errorf("stale skip = %v", err)
	}
}
