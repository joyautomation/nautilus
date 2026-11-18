package devprofile

import (
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

// Every built-in profile's struct type compiles: a point named like an IEC
// keyword would otherwise surface as "unknown type" in a user's project.
func TestBuiltinTypesCompile(t *testing.T) {
	ps, err := Builtin()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range ps {
		src := TypesST([]*Profile{p}) + "PROGRAM P\nVAR d : " + p.TypeName() + "; END_VAR\nEND_PROGRAM\n"
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
