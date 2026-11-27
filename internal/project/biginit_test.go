package project

import (
	"testing"
	"testing/fstest"

	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/runtime"
)

// #1432: an integer init above 2^53 seeds exactly. The loader used to turn
// every YAML integer into a float64, so `init: 9007199254740993` on a LINT
// started at ...992 (the nearest float64).
func TestIntegerInitAbove2To53SeedsExactly(t *testing.T) {
	const big = 9007199254740993 // 2^53 + 1: no float64 holds it
	fsys := fstest.MapFS{
		"nautilus.yaml": &fstest.MapFile{Data: []byte("tasks:\n  - program: program.st\ntags:\n" +
			"  - { name: Typed, role: setpoint, type: LINT, init: 9007199254740993 }\n" +
			"  - { name: Declared, role: state, init: -9007199254740993 }\n" +
			"  - { name: Spare, role: state, init: 7 }\n")},
		"program.st": &fstest.MapFile{Data: []byte("PROGRAM Main\nVAR_EXTERNAL Typed, Declared : LINT; END_VAR\nTyped := Typed;\nEND_PROGRAM\n")},
	}
	proj, err := Load(fsys, "")
	if err != nil {
		t.Fatal(err)
	}
	rt, err := runtime.New(proj.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]int64{"Typed": big, "Declared": -big} {
		v, err := rt.Tags().ReadGlobal(name)
		if err != nil {
			t.Fatal(err)
		}
		if v.Kind != ir.TypeInt || v.I != want {
			t.Errorf("%s seeded %+v, want %d", name, v, want)
		}
	}
	// A number on a tag nothing types is still a REAL, as before.
	if v, _ := rt.Tags().ReadGlobal("Spare"); v.Kind != ir.TypeReal || v.F != 7 {
		t.Errorf("Spare seeded %+v, want REAL 7", v)
	}
}
