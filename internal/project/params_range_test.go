package project

import (
	"strings"
	"testing"
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
