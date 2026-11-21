package project

import (
	"strings"
	"testing"
	"testing/fstest"
)

// min/max are declared on a setpoint, the manifest refuses nonsense about
// them with the fix named, and the runtime gets the range (#718).
func TestTagRangeDeclaration(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	cases := []struct {
		name string
		tag  TagConfig
		want string // substring of the error; "" = accepted
	}{
		{"ok", TagConfig{Name: "SP", Role: "setpoint", Init: 65, Min: f(10), Max: f(90)}, ""},
		{"open top", TagConfig{Name: "SP", Role: "setpoint", Init: 65.0, Min: f(0)}, ""},
		{"init at the edge", TagConfig{Name: "SP", Role: "setpoint", Init: 90, Max: f(90)}, ""},
		{"init below", TagConfig{Name: "SP", Role: "setpoint", Init: 5, Min: f(10)}, "init 5 is outside its range [10, ..]"},
		{"init above", TagConfig{Name: "SP", Role: "setpoint", Init: 95.5, Max: f(90)}, "outside its range"},
		{"min above max", TagConfig{Name: "SP", Role: "setpoint", Init: 5, Min: f(9), Max: f(3)}, "min 9 is above max 3"},
		{"text init", TagConfig{Name: "H", Role: "setpoint", Init: "T#5s", Max: f(9)}, "init \"T#5s\" is text"},
		{"on an output", TagConfig{Name: "Y", Role: "output", Init: false, Max: f(1)}, "bound a setpoint"},
		{"on state", TagConfig{Name: "S", Role: "state", Init: 1, Min: f(0)}, "bound a setpoint"},
	}
	for _, c := range cases {
		defs, err := tagDefs([]TagConfig{c.tag})
		if c.want == "" {
			if err != nil {
				t.Errorf("%s: %v", c.name, err)
			} else if m := defs[0].Meta; m.Min == nil && m.Max == nil {
				t.Errorf("%s: the range did not reach the runtime", c.name)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.want)
		}
	}
}

// parameters.writeBack takes off|patch|commit; anything else is refused
// with the choices named (#718 slice 2).
func TestParametersWriteBackMode(t *testing.T) {
	for _, c := range []struct{ yaml, want, mode string }{
		{"", "", "off"},
		{"parameters:\n  writeBack: patch\n", "", "patch"},
		{"parameters:\n  writeBack: commit\n", "", "commit"},
		{"parameters:\n  writeBack: off\n", "", "off"},
		{"parameters: {}\n", "", "off"},
		{"parameters:\n  writeBack: auto\n", "not one of off, patch, commit", ""},
		{"parameters:\n  writeback: commit\n", "writeback", ""},
	} {
		fsys := fstest.MapFS{"nautilus.yaml": {Data: []byte("name: x\ntasks:\n  - program: main.st\n" + c.yaml)}}
		m, err := ReadManifest(fsys, "")
		if c.want != "" {
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("%q: err = %v, want %q", c.yaml, err, c.want)
			}
			continue
		}
		if err != nil || m.WriteBackMode() != c.mode {
			t.Errorf("%q: mode %v, err %v, want %s", c.yaml, m, err, c.mode)
		}
	}
}
