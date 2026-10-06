package st

import (
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/lang/ir"
)

// EN/ENO execution control (lower_eneno.go, #206).

func runFrame(t *testing.T, src string) (*ir.Program, *ir.Frame) {
	t.Helper()
	prog, err := Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	irProg, err := Lower(prog)
	if err != nil {
		t.Fatalf("lower: %v", err)
	}
	return irProg, ir.NewFrame(irProg)
}

func scan(t *testing.T, p *ir.Program, f *ir.Frame, h *fakeHost) {
	t.Helper()
	if err := ir.Run(p, f, h); err != nil {
		t.Fatal(err)
	}
}

func TestENOnFunctionAssign(t *testing.T) {
	src := `
PROGRAM p
VAR_GLOBAL
    en : BOOL; x : REAL; out : REAL; ok : BOOL;
END_VAR
out := LIMIT(EN := en, MN := 0.0, IN := x, MX := 10.0, ENO => ok);
END_PROGRAM`
	h := newFakeHost()
	h.vals["en"], h.vals["x"], h.vals["out"], h.vals["ok"] = ir.BoolVal(true), ir.RealVal(42), ir.RealVal(-1), ir.BoolVal(false)
	p, f := runFrame(t, src)
	scan(t, p, f, h)
	if h.vals["out"].F != 10 || !h.vals["ok"].B {
		t.Fatalf("EN TRUE: out=%v ok=%v, want 10 TRUE", h.vals["out"].F, h.vals["ok"].B)
	}
	// EN FALSE: the result is not assigned — out keeps 10 although x moved.
	h.vals["en"], h.vals["x"] = ir.BoolVal(false), ir.RealVal(3)
	scan(t, p, f, h)
	if h.vals["out"].F != 10 || h.vals["ok"].B {
		t.Fatalf("EN FALSE: out=%v ok=%v, want 10 (held) FALSE", h.vals["out"].F, h.vals["ok"].B)
	}
	h.vals["en"] = ir.BoolVal(true)
	scan(t, p, f, h)
	if h.vals["out"].F != 3 || !h.vals["ok"].B {
		t.Fatalf("EN TRUE again: out=%v ok=%v, want 3 TRUE", h.vals["out"].F, h.vals["ok"].B)
	}
}

func TestENOOnlyAndPositional(t *testing.T) {
	// ENO alone (EN unbound = TRUE); a positional standard call is unchanged.
	src := `
PROGRAM p
VAR_GLOBAL
    a : INT; b : INT; ok : BOOL;
END_VAR
a := MAX(IN1 := 3, IN2 := 9, ENO => ok);
b := MAX(3, 9);
END_PROGRAM`
	h := newFakeHost()
	p, f := runFrame(t, src)
	scan(t, p, f, h)
	if h.vals["a"].I != 9 || h.vals["b"].I != 9 || !h.vals["ok"].B {
		t.Fatalf("a=%v b=%v ok=%v", h.vals["a"].I, h.vals["b"].I, h.vals["ok"].B)
	}
}

func TestENOnFBCall(t *testing.T) {
	// EN FALSE: TON does not run — ET freezes, Q and the => binding hold.
	src := `
PROGRAM p
VAR_GLOBAL
    en : BOOL; go : BOOL; q : BOOL; et : TIME; ok : BOOL;
END_VAR
VAR
    t1 : TON;
END_VAR
t1(EN := en, IN := go, PT := T#100MS, Q => q, ET => et, ENO => ok);
END_PROGRAM`
	h := newFakeHost()
	h.vals["en"], h.vals["go"] = ir.BoolVal(true), ir.BoolVal(true)
	p, f := runFrame(t, src)
	h.now = 1000
	scan(t, p, f, h) // the timer starts
	h.now = 1050
	scan(t, p, f, h)
	if h.vals["et"].I != 50 || !h.vals["ok"].B {
		t.Fatalf("running: et=%v ok=%v", h.vals["et"].I, h.vals["ok"].B)
	}
	h.vals["en"] = ir.BoolVal(false)
	h.now = 1080
	scan(t, p, f, h)
	if h.vals["et"].I != 50 || h.vals["ok"].B {
		t.Fatalf("EN FALSE: et=%v (want held 50) ok=%v", h.vals["et"].I, h.vals["ok"].B)
	}
	h.vals["en"] = ir.BoolVal(true)
	h.now = 1200
	scan(t, p, f, h)
	if !h.vals["q"].B || !h.vals["ok"].B {
		t.Fatalf("EN TRUE again: q=%v ok=%v", h.vals["q"].B, h.vals["ok"].B)
	}
}

func TestENOnUserFunctionStatementAndOwnEN(t *testing.T) {
	// A user FUNCTION_BLOCK that declares its own EN keeps it as a pin.
	src := `
FUNCTION_BLOCK Gate
VAR_INPUT EN : BOOL; END_VAR
VAR_OUTPUT ENO : BOOL; Runs : INT; END_VAR
IF EN THEN Runs := Runs + 1; END_IF;
ENO := EN;
END_FUNCTION_BLOCK
PROGRAM p
VAR_GLOBAL en : BOOL; runs : INT; ok : BOOL; END_VAR
VAR g : Gate; END_VAR
g(EN := en, Runs => runs, ENO => ok);
END_PROGRAM`
	h := newFakeHost()
	p, f := runFrame(t, src)
	scan(t, p, f, h) // EN FALSE: the block still runs (its own EN), counts nothing
	h.vals["en"] = ir.BoolVal(true)
	scan(t, p, f, h)
	scan(t, p, f, h)
	if h.vals["runs"].I != 2 || !h.vals["ok"].B {
		t.Fatalf("own EN: runs=%v ok=%v", h.vals["runs"].I, h.vals["ok"].B)
	}
}

func TestENErrors(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{"x := ABS(LIMIT(EN := en, MN := 0.0, IN := x, MX := 1.0));", "statement of its own"},
		{"x := LIMIT(EN := x, MN := 0.0, IN := x, MX := 1.0);", "EN must be BOOL"},
		{"x := LIMIT(MN := 0.0, IN := x, MX := 1.0, ENO => x);", "ENO is BOOL"},
		{"x := LIMIT(MN := 0.0, 1.0, MX := 1.0);", "formal or positional"},
		{"x := LIMIT(MN := 0.0, IN := x, MAX := 1.0);", `no input "MAX"`},
		{"x := LIMIT(MN := 0.0, IN := x);", "missing input MX"},
		{"x := ABS(x, Q => en);", `no output "Q"`},
	} {
		src := "PROGRAM p\nVAR_GLOBAL x : REAL; en : BOOL; END_VAR\n" + tc.body + "\nEND_PROGRAM"
		prog, err := Parse(src)
		if err != nil {
			t.Fatalf("%s: parse: %v", tc.body, err)
		}
		_, err = Lower(prog)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", tc.body, err, tc.want)
		}
	}
}
