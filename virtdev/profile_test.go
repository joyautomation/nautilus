package virtdev

import (
	"strconv"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/runtime"
)

func TestBuiltinProfilesLoad(t *testing.T) {
	ps, err := Builtin()
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) == 0 {
		t.Fatal("no built-in profiles")
	}
	for _, p := range ps {
		// Each built-in profile composes into a plant that compiles.
		set := &Set{Devices: []Instance{{Name: "D", Profile: p.ID}}}
		if err := set.resolve(); err != nil {
			t.Errorf("%s: %v", p.ID, err)
			continue
		}
		plant, err := NewPlant(set)
		if err != nil {
			t.Errorf("%s: plant: %v", p.ID, err)
			continue
		}
		// A profile with no simulator generates a main program with an
		// empty body; it must still compile.
		if _, err := runtime.New(plant.Options()); err != nil {
			t.Errorf("%s: plant does not compile: %v\n%s", p.ID, err, plant.Program())
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

func TestSetErrors(t *testing.T) {
	cases := map[string]string{
		"no devices":      "devices: []\n",
		"bad name":        "devices: [{ name: 1x, profile: relay-board-16 }]\n",
		"no profile":      "devices: [{ name: A }]\n",
		"unknown profile": "devices: [{ name: A, profile: nope }]\n",
		"twice":           "devices: [{ name: A, profile: relay-board-16 }, { name: a, profile: relay-board-16 }]\n",
		"same unit twice": "devices: [{ name: A, profile: relay-board-16, endpoint: 10.0.0.1 }, { name: B, profile: relay-board-16, endpoint: '10.0.0.1:502' }]\n",
		"init no point":   "devices: [{ name: A, profile: relay-board-16, init: { Nope: true } }]\n",
	}
	for name, body := range cases {
		if _, err := ParseSet([]byte(body), t.TempDir()); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	// Two boards behind one address with different unit ids are fine.
	if _, err := ParseSet([]byte("devices: [{ name: A, profile: relay-board-16, endpoint: 10.0.0.1 }, { name: B, profile: relay-board-16, endpoint: 10.0.0.1, unit-id: 2 }]\n"), t.TempDir()); err != nil {
		t.Errorf("two units behind one endpoint: %v", err)
	}
}
