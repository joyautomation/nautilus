package fbd

import (
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/lang/ir"
)

// #197: a project FUNCTION is called from FBD however its name is cased, and
// the block keeps the user's spelling — the netlist no longer upper-cases a
// call name. A standard function keeps its standard (upper-case) name.
func TestCaseInsensitiveCallsKeepSpelling(t *testing.T) {
	src := `FUNCTION SCALEANALOG : REAL
VAR_INPUT Raw : REAL; Hi : REAL; END_VAR
  ScaleAnalog := raw * HI;
END_FUNCTION

PROGRAM main
VAR_EXTERNAL
  FT101_Raw : REAL; ft : REAL; lim : REAL;
END_VAR
FBD
  ft := ScaleAnalog(ft101_raw, 2.0)
  lim := limit(0.0, FT101_RAW, 1.0)
END_FBD
END_PROGRAM`
	out := mustTranspile(src)
	if !strings.Contains(out, "ScaleAnalog(") || !strings.Contains(out, "LIMIT(") {
		t.Fatalf("call spelling: want the user's ScaleAnalog and the standard LIMIT:\n%s", out)
	}
	h := run(t, src, map[string]ir.Value{"FT101_Raw": ir.RealVal(3)})
	if got := h.vals["ft"]; got.F != 6 {
		t.Fatalf("ft = %+v, want 6", got)
	}
	if got := h.vals["lim"]; got.F != 1 {
		t.Fatalf("lim = %+v, want 1", got)
	}
	m, err := Graph(src)
	if err != nil {
		t.Fatalf("graph: %v", err)
	}
	var labels []string
	for _, n := range m.Nodes {
		if n.Kind == "block" {
			labels = append(labels, n.Label)
		}
	}
	if strings.Join(labels, ",") != "ScaleAnalog,LIMIT" {
		t.Fatalf("block labels = %v, want [ScaleAnalog LIMIT]", labels)
	}
}

// Wires and FB instances are netlist names: a reference in another casing is
// the same wire / instance (one graph node, the call ordered before the read).
func TestCaseInsensitiveWiresAndInstances(t *testing.T) {
	src := `PROGRAM Timed
VAR_EXTERNAL
  Run : BOOL; Elapsed : BOOL; Both : BOOL;
END_VAR
FBD
  Elapsed := T1.q
  t1 : TON(IN := Run, PT := T#5S)
  w = AND(Run, t1.Q)
  Both := W
END_FBD
END_PROGRAM`
	out := mustTranspile(src)
	if strings.Index(out, "t1(IN") > strings.Index(out, "Elapsed := t1.q") {
		t.Fatalf("the call must be ordered before its pin read, whatever the casing:\n%s", out)
	}
	run(t, src, map[string]ir.Value{"Run": ir.BoolVal(true)})
	m, err := Graph(src)
	if err != nil {
		t.Fatalf("graph: %v", err)
	}
	fbs := 0
	for _, n := range m.Nodes {
		if n.Kind == "fb" {
			fbs++
		}
	}
	if fbs != 1 {
		t.Fatalf("want one TON node for t1/T1, got %d", fbs)
	}

	if _, err := Transpile(`PROGRAM p
VAR_EXTERNAL a : BOOL; END_VAR
FBD
  w = NOT(a)
  W = NOT(a)
END_FBD
END_PROGRAM`); err == nil || !strings.Contains(err.Error(), "defined twice") {
		t.Fatalf("w and W are one wire: want a duplicate error, got %v", err)
	}
}
