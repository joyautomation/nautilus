package st

import (
	"testing"

	"github.com/joyautomation/nautilus/lang/ir"
)

// TestUserFunc_DeclareAndCall verifies that a user-defined FUNCTION can
// be declared in the same source as a PROGRAM that calls it by bare name.
func TestUserFunc_DeclareAndCall(t *testing.T) {
	src := `
FUNCTION Square : INT
VAR_INPUT
  X : INT;
END_VAR
  Square := X * X;
END_FUNCTION

PROGRAM main
VAR_GLOBAL
  result : INT;
END_VAR
  result := Square(7);
END_PROGRAM
`
	host := newFakeHost()
	prog, err := Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	irProg, err := Lower(prog)
	if err != nil {
		t.Fatalf("lower: %v", err)
	}
	if len(irProg.UserFuncs) != 1 || irProg.UserFuncs[0].Name != "Square" {
		t.Fatalf("expected one UserFunc named Square, got %+v", irProg.UserFuncs)
	}
	frame := ir.NewFrame(irProg)
	if err := ir.Run(irProg, frame, host); err != nil {
		t.Fatalf("run: %v", err)
	}
	got := host.vals["result"]
	if got.Kind != ir.TypeInt || got.I != 49 {
		t.Fatalf("expected result=49, got %+v", got)
	}
}

// TestUserFunc_NamedArgs exercises the named-argument call form on a
// user-defined function with multiple inputs.
func TestUserFunc_NamedArgs(t *testing.T) {
	src := `
FUNCTION Scale : REAL
VAR_INPUT
  In : REAL;
  Factor : REAL;
END_VAR
  Scale := In * Factor;
END_FUNCTION

PROGRAM main
VAR_GLOBAL
  out : REAL;
END_VAR
  out := Scale(In := 3.0, Factor := 2.5);
END_PROGRAM
`
	host := newFakeHost()
	prog, err := Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	irProg, err := Lower(prog)
	if err != nil {
		t.Fatalf("lower: %v", err)
	}
	frame := ir.NewFrame(irProg)
	if err := ir.Run(irProg, frame, host); err != nil {
		t.Fatalf("run: %v", err)
	}
	got := host.vals["out"]
	if got.Kind != ir.TypeReal || got.F != 7.5 {
		t.Fatalf("expected out=7.5, got %+v", got)
	}
}

// TestUserFunc_NoState verifies functions are stateless — each call
// gets a fresh frame so locals do not leak between calls.
func TestUserFunc_NoState(t *testing.T) {
	src := `
FUNCTION Bump : INT
VAR_INPUT
  X : INT;
END_VAR
VAR
  acc : INT;
END_VAR
  acc := acc + X;
  Bump := acc;
END_FUNCTION

PROGRAM main
VAR_GLOBAL
  a : INT;
  b : INT;
END_VAR
  a := Bump(3);
  b := Bump(3);
END_PROGRAM
`
	host := newFakeHost()
	prog, err := Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	irProg, err := Lower(prog)
	if err != nil {
		t.Fatalf("lower: %v", err)
	}
	frame := ir.NewFrame(irProg)
	if err := ir.Run(irProg, frame, host); err != nil {
		t.Fatalf("run: %v", err)
	}
	if host.vals["a"].I != 3 || host.vals["b"].I != 3 {
		t.Fatalf("expected a=b=3 (stateless), got a=%d b=%d", host.vals["a"].I, host.vals["b"].I)
	}
}

// TestUserFunc_TopKeyword captures the source's top-level POU keyword so
// the API layer can derive the navigator category without re-parsing.
func TestUserFunc_TopKeyword(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{"program", "PROGRAM main\nVAR_GLOBAL x : INT; END_VAR\n  x := 1;\nEND_PROGRAM\n", "PROGRAM"},
		{"function", "FUNCTION F : INT\nVAR_INPUT a : INT; END_VAR\n  F := a;\nEND_FUNCTION\n", "FUNCTION"},
		{"function_block", "FUNCTION_BLOCK FB\nVAR_INPUT a : INT; END_VAR\nVAR_OUTPUT b : INT; END_VAR\n  b := a;\nEND_FUNCTION_BLOCK\n", "FUNCTION_BLOCK"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			prog, err := Parse(c.src)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if prog.TopKeyword != c.want {
				t.Fatalf("expected TopKeyword=%q, got %q", c.want, prog.TopKeyword)
			}
		})
	}
}
