package fbd

import (
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/lang/st"
)

// #208: the @layout block lives at one fixed place — right after END_FBD —
// so statements added later never land after it, and a block an older
// version wrote mid-body moves there on the next layout write.
func TestLayoutBlockFixedPlace(t *testing.T) {
	x, y := 40, 60
	out := apply(t, editSrc, mustOp(t, editSrc, EditOp{Type: "setLayout", Node: "c:Run", X: &x, Y: &y}))
	endFBD := strings.Index(out, "END_FBD")
	lay := strings.Index(out, "(* @layout")
	if lay < endFBD || lay > strings.Index(out, "END_PROGRAM") {
		t.Fatalf("layout block must sit between END_FBD and END_PROGRAM:\n%s", out)
	}
	// A statement added after the pin goes into the body, above END_FBD.
	out = apply(t, out, mustOp(t, out, EditOp{Type: "insertStatement", Text: "Lamp := Run"}))
	if !(strings.Index(out, "Lamp := Run") < strings.Index(out, "END_FBD")) {
		t.Fatalf("statement must land inside the body:\n%s", out)
	}

	legacy := `PROGRAM Main
VAR_EXTERNAL A : BOOL; B : BOOL; C : BOOL; END_VAR
FBD
  B := A
  (* @layout
    c:B 10,20
  *)
  C := NOT A
END_FBD
END_PROGRAM
`
	x2, y2 := 99, 98
	out = apply(t, legacy, mustOp(t, legacy, EditOp{Type: "setLayout", Node: "c:C", X: &x2, Y: &y2}))
	want := `PROGRAM Main
VAR_EXTERNAL A : BOOL; B : BOOL; C : BOOL; END_VAR
FBD
  B := A
  C := NOT A
END_FBD
  (* @layout
    c:B 10,20
    c:C 99,98
  *)
END_PROGRAM
`
	if out != want {
		t.Fatalf("legacy mid-body block must migrate after END_FBD:\n%s", out)
	}
	m := mustGraph(t, out)
	if n := m.node(t, "c:B"); n.X == nil || *n.X != 10 {
		t.Fatalf("migrated pins must still apply: %s", mustJSON(n))
	}
	if _, err := Compile(out); err != nil {
		t.Fatal(err)
	}
}

const dosingLib = `FUNCTION_BLOCK Dosing
VAR_INPUT
    Start      : BOOL;
    FlowLpm    : REAL;  (* measured flow *)
    NoFlowTime : TIME := T#5S;
    Gain, Bias : REAL := 1.0;
END_VAR
VAR_OUTPUT
    ValveOpen : BOOL;
END_VAR
VAR_IN_OUT
    Recipe : INT;
END_VAR
ValveOpen := Start AND FlowLpm > 0.0;
END_FUNCTION_BLOCK

FUNCTION ScaleAnalog : REAL
VAR_INPUT
    Raw   : INT;
    EngLo : REAL;
    EngHi : REAL;
END_VAR
ScaleAnalog := INT_TO_REAL(Raw) / 27648.0 * (EngHi - EngLo) + EngLo;
END_FUNCTION
`

// #205: the FB picker leaves an input with a declared initial value
// unbound — it keeps that value, as an unconnected FB input does — and
// writes `_` only on inputs with no default and on every VAR_IN_OUT.
func TestPickerLeavesDefaultedInputsUnbound(t *testing.T) {
	m, err := GraphWithLibs(levelSrc, []string{dosingLib})
	if err != nil {
		t.Fatal(err)
	}
	d := catalogType(t, m.FBTypes, "Dosing")
	if d.Args != "Start := _, FlowLpm := _, Recipe := _" {
		t.Fatalf("Dosing open args = %q", d.Args)
	}
	for _, p := range d.Pins {
		if p.Name == "NoFlowTime" && p.Init != "T#5S" || p.Name == "Bias" && p.Init != "1.0" {
			t.Fatalf("pin init not carried: %+v", p)
		}
	}
	// The unbound pin still draws (from the signature), unwired.
	out := insertFB(t, levelSrc, "doseA", "Dosing", dosingLib)
	mm, err := GraphWithLibs(out, []string{dosingLib})
	if err != nil {
		t.Fatal(err)
	}
	if n := fbNode(t, mm, "f:doseA"); !strings.Contains(strings.Join(n.Inputs, ","), "NoFlowTime") {
		t.Fatalf("NoFlowTime pin must still draw: %v", n.Inputs)
	}
	// Wired, the call compiles with NoFlowTime/Gain/Bias left out.
	src := strings.Replace(levelSrc, "  hi = GE(LIT101_Level, LevelSP)",
		"  doseA : Dosing(Start := LeadReq, FlowLpm := SpeedRef, Recipe := cnt)", 1)
	src = strings.Replace(src, "END_VAR\nFBD", "END_VAR\nVAR cnt : INT; END_VAR\nFBD", 1)
	if _, err := compileWithLibs(src, dosingLib); err != nil {
		t.Fatalf("a call leaving defaulted inputs unbound must compile: %v", err)
	}
}

// #204: the model carries the project's FUNCTIONs by their declared names
// (with inputs and return type) for the palette's function field.
func TestModelListsUserFunctions(t *testing.T) {
	m, err := GraphWithLibs(levelSrc, []string{dosingLib})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Funcs) != 1 || m.Funcs[0].Name != "ScaleAnalog" || m.Funcs[0].Result != "REAL" {
		t.Fatalf("funcs = %+v", m.Funcs)
	}
	if m.Funcs[0].Detail != "(Raw, EngLo, EngHi) → REAL" {
		t.Fatalf("detail = %q", m.Funcs[0].Detail)
	}
	for _, ty := range m.FBTypes {
		if ty.Name == "ScaleAnalog" {
			t.Fatal("a FUNCTION is not a function block")
		}
	}
	blank, _ := GraphWithLibs("", []string{dosingLib})
	if len(blank.Funcs) != 1 {
		t.Fatalf("a blank file's model must list them too: %+v", blank.Funcs)
	}
}

// compileWithLibs compiles .fbd source with library ST prepended — the
// shape `naut check` composes (the libraries' prelude, then the program).
func compileWithLibs(src string, libs ...string) (*ir.Program, error) {
	stSrc, err := Transpile(src)
	if err != nil {
		return nil, err
	}
	prog, err := st.Parse(strings.Join(libs, "\n") + "\n" + stSrc)
	if err != nil {
		return nil, err
	}
	return st.Lower(prog)
}
