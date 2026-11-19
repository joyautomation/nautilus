package devprofile

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/lang/st"
)

// Every built-in profile parses, validates and carries its file's id.
func TestBuiltinProfilesParse(t *testing.T) {
	ps, err := Builtin()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, p := range ps {
		got = append(got, p.ID)
	}
	if want := "abb-acs355-fmbt21 abb-acs550-reta01 abb-acs580-fmbt21 asi-275m fdc-mct4 ifm-al1590 pepperl-fuchs-ice3 sick-sig300 waveshare-relay-16ch"; strings.Join(got, " ") != want {
		t.Errorf("built-in profiles\n got  %s\n want %s", strings.Join(got, " "), want)
	}
	for _, p := range ps {
		if p.Dir() != "" || p.File() != "" {
			t.Errorf("%s: a built-in profile has a directory", p.ID)
		}
		if p.Simulator != nil {
			if src, err := p.SimulatorSource(); err != nil || src == "" {
				t.Errorf("%s: simulator %s: %v", p.ID, p.Simulator.Source, err)
			}
		}
	}
}

func TestRepeatExpands(t *testing.T) {
	p, err := ParseProfile([]byte(`
profile: x
protocol: modbus
docs: [{ id: d }]
points:
  - { name: "Speed{n}", type: REAL, repeat: 3, from: 0, table: holding, address: 100, doc: "d p. {n}" }
  - { name: "Bit{n}", type: BOOL, repeat: 2, table: coil, address: 8, doc: d }
`), "x.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, pt := range p.Points {
		got = append(got, pt.Name+"@"+strconv.Itoa(int(pt.Address))+":"+pt.Format+":"+pt.Doc)
	}
	want := "Speed0@100:float32:d p. 0 Speed1@102:float32:d p. 1 Speed2@104:float32:d p. 2 Bit1@8:bool:d Bit2@9:bool:d"
	if strings.Join(got, " ") != want {
		t.Errorf("got  %s\nwant %s", strings.Join(got, " "), want)
	}
}

func TestProfileErrors(t *testing.T) {
	head := "profile: x\nprotocol: modbus\ndocs: [{ id: d }]\npoints:\n"
	cases := map[string]string{
		"no provenance":         "  - { name: A, type: WORD, table: holding, address: 0 }\n",
		"doc cites nothing":     "  - { name: A, type: WORD, table: holding, address: 0, doc: nope }\n",
		"bad name":              "  - { name: 1A, type: WORD, table: holding, address: 0, doc: d }\n",
		"bad type":              "  - { name: A, type: STRING, table: holding, address: 0, doc: d }\n",
		"bool on a coil only":   "  - { name: A, type: WORD, table: coil, address: 0, doc: d }\n",
		"scaled integer":        "  - { name: A, type: INT, table: holding, address: 0, scale: 0.1, doc: d }\n",
		"writable input table":  "  - { name: A, type: WORD, table: input, address: 0, writable: true, doc: d }\n",
		"twice":                 "  - { name: A, type: WORD, table: holding, address: 0, doc: d }\n  - { name: a, type: WORD, table: holding, address: 1, doc: d }\n",
		"repeat without {n}":    "  - { name: A, type: WORD, repeat: 2, table: holding, address: 0, doc: d }\n",
		"unknown key":           "  - { name: A, type: WORD, table: holding, adress: 0, doc: d }\n",
		"repeat past the table": "  - { name: \"A{n}\", type: WORD, repeat: 3, table: holding, address: 65534, doc: d }\n",
	}
	for name, rows := range cases {
		if _, err := ParseProfile([]byte(head+rows), "x.yaml"); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if _, err := ParseProfile([]byte("profile: x\nprotocol: canbus\n"), "x.yaml"); err == nil {
		t.Error("unknown protocol: no error")
	}
}

// A node id, an Object's or a point's, and the object namespace must sit in
// the profile's namespace array: one past it can be bound by neither URI
// nor a known index.
func TestOPCUANamespaceIndices(t *testing.T) {
	head := "profile: x\nprotocol: opcua\nopcua: { namespaces: [urn:a, urn:b]%s }\npoints:\n  - { name: A, type: BOOL, node-id: \"%s\", path: \"D/A\", unverified: true }\n"
	for _, tc := range []struct{ extra, nid, want string }{
		{"", "ns=2;s=D/A", ""},
		{"", "ns=0;i=2258", ""},
		{"", "ns=3;s=D/A", `node-id "ns=3;s=D/A": namespace 3 is past the profile's namespaces (1-2)`},
		{", objects: [{ path: D, node-id: \"ns=4;s=D\" }]", "ns=1;s=D/A", "object D: node-id \"ns=4;s=D\": namespace 4 is past"},
		{", object-namespace: 3", "ns=1;s=D/A", "object-namespace 3 is not an index of namespaces (1-2)"},
	} {
		_, err := ParseProfile([]byte(fmt.Sprintf(head, tc.extra, tc.nid)), "x.yaml")
		if tc.want == "" && err != nil {
			t.Errorf("%s%s: %v", tc.nid, tc.extra, err)
		}
		if tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
			t.Errorf("%s%s: %v, want %q", tc.nid, tc.extra, err, tc.want)
		}
	}
}

// Every built-in profile's struct type compiles: a point named like an IEC
// keyword would otherwise surface as "unknown type" in a user's project.
func TestBuiltinTypesCompile(t *testing.T) {
	ps, err := Builtin()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range ps {
		src := TypesST([]Struct{{Name: p.TypeName(), Points: p.Points}}) + "PROGRAM P\nVAR d : " + p.TypeName() + "; END_VAR\nEND_PROGRAM\n"
		prog, err := st.Parse(src)
		if err != nil {
			t.Errorf("%s: %v\n%s", p.ID, err, src)
			continue
		}
		types, err := st.Types(prog)
		if err != nil || types[p.TypeName()] == nil {
			t.Errorf("%s: type %s: %v", p.ID, p.TypeName(), err)
			continue
		}
		if _, err := st.Lower(prog); err != nil {
			t.Errorf("%s: %v", p.ID, err)
		}
	}
}

// A profile id names profiles/<id>.yaml and a simulator source sits beside
// the profile: neither may carry a directory, so a vendored profile cannot
// write or read outside profiles/.
func TestProfileIDAndSimulatorAreNames(t *testing.T) {
	body := "protocol: modbus\ndocs: [{ id: d }]\npoints:\n  - { name: A, type: WORD, table: holding, address: 0, doc: d }\n"
	for _, id := range []string{"../escaped", "a/b", "/abs", "..", ".hidden", `a\b`} {
		if _, err := ParseProfile([]byte("profile: \""+id+"\"\nudt: X\n"+body), "x.yaml"); err == nil {
			t.Errorf("id %q: no error", id)
		}
	}
	for _, src := range []string{"../evil_injected.st", "sub/x.st", "/etc/x.st", "..", `c:\x.st`} {
		if _, err := ParseProfile([]byte("profile: x\n"+body+"simulator: { block: B, source: \""+src+"\" }\n"), "x.yaml"); err == nil {
			t.Errorf("simulator source %q: no error", src)
		}
	}
	if _, err := ParseProfile([]byte("profile: abb-acs355.v2\n"+body+"simulator: { block: B, source: abb-drives.st }\n"), "x.yaml"); err != nil {
		t.Errorf("a plain id and source: %v", err)
	}
}
