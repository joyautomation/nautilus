package hw

import (
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/lang/ir"
)

func TestExprEval(t *testing.T) {
	env := map[string]ir.Value{
		"AdminUp":   ir.BoolVal(true),
		"OperUp":    ir.BoolVal(false),
		"InBps":     ir.RealVal(250e6),
		"SpeedMbps": ir.RealVal(1000),
		"Zero":      ir.RealVal(0),
		"Health":    ir.IntVal(2),
		"Name":      ir.StringVal("eth0"),
		"idle":      ir.RealVal(3.5),
		"cpus":      ir.RealVal(4),
	}
	lookup := func(n string) (ir.Value, bool) { v, ok := env[n]; return v, ok }
	cases := []struct {
		src  string
		want ir.Value
	}{
		{"AdminUp && !OperUp", ir.BoolVal(true)},
		{"AdminUp && OperUp", ir.BoolVal(false)},
		{"OperUp || AdminUp", ir.BoolVal(true)},
		{"100 * InBps / (SpeedMbps * 1e6)", ir.RealVal(25)},
		{"InBps / Zero", ir.RealVal(0)}, // division by zero is 0, never NaN
		{"Health == 2", ir.BoolVal(true)},
		{"Health >= 1 && Health != 3", ir.BoolVal(true)},
		{"Name == Name", ir.BoolVal(true)},
		{"100 - 100 * idle / cpus", ir.RealVal(12.5)},
		{"-idle + 4", ir.RealVal(0.5)},
		{"!(AdminUp && OperUp)", ir.BoolVal(true)},
		{"true", ir.BoolVal(true)},
		{"2 + 3 * 4", ir.RealVal(14)},
		{"(2 + 3) * 4", ir.RealVal(20)},
	}
	for _, c := range cases {
		e, err := ParseExpr(c.src)
		if err != nil {
			t.Fatalf("%s: %v", c.src, err)
		}
		got, err := e.Eval(lookup)
		if err != nil {
			t.Fatalf("%s: %v", c.src, err)
		}
		if got.Kind != c.want.Kind || got.B != c.want.B || got.F != c.want.F {
			t.Errorf("%s = %+v, want %+v", c.src, got, c.want)
		}
	}
}

func TestExprVars(t *testing.T) {
	e, err := ParseExpr("100 * InBps / (SpeedMbps * 1e6) + InBps")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(e.Vars(), ","); got != "InBps,SpeedMbps" {
		t.Errorf("Vars = %q", got)
	}
}

func TestExprErrors(t *testing.T) {
	bad := []string{"", "AdminUp &&", "(1 + 2", "1 +* 2", "1 2", "a ? b", "1e", "&& a"}
	for _, src := range bad {
		if _, err := ParseExpr(src); err == nil {
			t.Errorf("%q parsed; want an error", src)
		}
	}
	env := map[string]ir.Value{"S": ir.StringVal("x"), "B": ir.BoolVal(true), "N": ir.RealVal(1)}
	lookup := func(n string) (ir.Value, bool) { v, ok := env[n]; return v, ok }
	for _, src := range []string{"S + 1", "!N", "B * 2", "Missing == 1", "S == 1", "B < N", "N && B"} {
		e, err := ParseExpr(src)
		if err != nil {
			t.Fatalf("%s: %v", src, err)
		}
		if _, err := e.Eval(lookup); err == nil {
			t.Errorf("%q evaluated; want a type error", src)
		}
	}
}

func TestExprShortCircuit(t *testing.T) {
	// The right operand of && is not evaluated when the left is false, so a
	// derived BOOL can guard a sibling whose lookup would fail.
	e, _ := ParseExpr("false && Missing")
	v, err := e.Eval(func(string) (ir.Value, bool) { return ir.Value{}, false })
	if err != nil || v.B {
		t.Fatalf("got %+v, %v", v, err)
	}
}
