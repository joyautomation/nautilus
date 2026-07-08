package st

import (
	"testing"

	"github.com/joyautomation/nautilus/lang/ir"
)

// TestUserFB_DeclareAndCall verifies a user-defined FUNCTION_BLOCK can
// be declared in the same source as a PROGRAM that instantiates it.
// The FB squares its input; the program writes the squared output back
// to a global so the test can assert on it.
func TestUserFB_DeclareAndCall(t *testing.T) {
	src := `
FUNCTION_BLOCK Squarer
VAR_INPUT
  X : INT;
END_VAR
VAR_OUTPUT
  Y : INT;
END_VAR
  Y := X * X;
END_FUNCTION_BLOCK

PROGRAM main
VAR
  s : Squarer;
END_VAR
VAR_GLOBAL
  result : INT;
END_VAR
  s(X := 7);
  result := s.Y;
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
	if len(irProg.UserFBs) != 1 || irProg.UserFBs[0].Name != "Squarer" {
		t.Fatalf("expected one UserFB named Squarer, got %+v", irProg.UserFBs)
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

// TestUserFB_RetainsState verifies that internal slots persist across
// scans — the canonical reason FBs exist as a stateful abstraction.
func TestUserFB_RetainsState(t *testing.T) {
	src := `
FUNCTION_BLOCK Accum
VAR_INPUT
  Step : INT;
END_VAR
VAR_OUTPUT
  Total : INT;
END_VAR
VAR
  acc : INT;
END_VAR
  acc := acc + Step;
  Total := acc;
END_FUNCTION_BLOCK

PROGRAM main
VAR
  a : Accum;
END_VAR
VAR_GLOBAL
  total : INT;
END_VAR
  a(Step := 3);
  total := a.Total;
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
	for i, want := range []int64{3, 6, 9, 12} {
		if err := ir.Run(irProg, frame, host); err != nil {
			t.Fatalf("scan %d: %v", i, err)
		}
		if got := host.vals["total"]; got.I != want {
			t.Fatalf("scan %d: expected total=%d, got %d", i, want, got.I)
		}
	}
}

// TestUserFB_CallsBuiltinFB exercises a user FB whose body contains a
// built-in FB instance (TON), proving the nested-Step plumbing forwards
// Host through stepCtx so timers can read NowMs.
func TestUserFB_CallsBuiltinFB(t *testing.T) {
	src := `
FUNCTION_BLOCK Delay100
VAR_INPUT
  IN : BOOL;
END_VAR
VAR_OUTPUT
  Q : BOOL;
END_VAR
VAR
  t : TON;
END_VAR
  t(IN := IN, PT := T#100ms);
  Q := t.Q;
END_FUNCTION_BLOCK

PROGRAM main
VAR
  d : Delay100;
END_VAR
VAR_GLOBAL
  trigger : BOOL;
  out : BOOL;
END_VAR
  d(IN := trigger);
  out := d.Q;
END_PROGRAM
`
	host := newFakeHost()
	host.vals["trigger"] = ir.BoolVal(true)
	host.now = 0
	prog, err := Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	irProg, err := Lower(prog)
	if err != nil {
		t.Fatalf("lower: %v", err)
	}
	frame := ir.NewFrame(irProg)

	// First scan: timer starts.
	if err := ir.Run(irProg, frame, host); err != nil {
		t.Fatalf("scan 1: %v", err)
	}
	if host.vals["out"].B {
		t.Fatalf("scan 1 (now=0): expected out=FALSE")
	}
	host.now = 50
	if err := ir.Run(irProg, frame, host); err != nil {
		t.Fatalf("scan 2: %v", err)
	}
	if host.vals["out"].B {
		t.Fatalf("scan 2 (now=50): expected out=FALSE before PT")
	}
	host.now = 150
	if err := ir.Run(irProg, frame, host); err != nil {
		t.Fatalf("scan 3: %v", err)
	}
	if !host.vals["out"].B {
		t.Fatalf("scan 3 (now=150): expected out=TRUE after PT elapsed")
	}
}

// TestUserFB_OutputBinding verifies IEC 61131-3 `=>` syntax copies an FB
// output into a caller lvalue in the same call expression, instead of
// requiring a follow-up `x := fb.Out` read.
func TestUserFB_OutputBinding(t *testing.T) {
	src := `
FUNCTION_BLOCK Doubler
VAR_INPUT
  X : INT;
END_VAR
VAR_OUTPUT
  Y : INT;
END_VAR
  Y := X * 2;
END_FUNCTION_BLOCK

PROGRAM main
VAR
  d : Doubler;
END_VAR
VAR_GLOBAL
  result : INT;
END_VAR
  d(X := 21, Y => result);
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
	if got := host.vals["result"]; got.Kind != ir.TypeInt || got.I != 42 {
		t.Fatalf("expected result=42, got %+v", got)
	}
}

// TestUserFB_OutputBindingRejectsInput verifies `=>` cannot bind an INPUT
// or VAR field — only declared outputs are valid targets.
func TestUserFB_OutputBindingRejectsInput(t *testing.T) {
	src := `
FUNCTION_BLOCK Doubler
VAR_INPUT
  X : INT;
END_VAR
VAR_OUTPUT
  Y : INT;
END_VAR
  Y := X * 2;
END_FUNCTION_BLOCK

PROGRAM main
VAR
  d : Doubler;
END_VAR
VAR_GLOBAL
  result : INT;
END_VAR
  d(X => result);
END_PROGRAM
`
	prog, err := Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, err := Lower(prog); err == nil {
		t.Fatalf("expected lowering to reject => on input field")
	}
}

// TestUserFB_PeerReference checks that two user FBs in the same source
// can reference one another via instance declarations regardless of
// declaration order.
func TestUserFB_PeerReference(t *testing.T) {
	src := `
FUNCTION_BLOCK Outer
VAR_INPUT
  X : INT;
END_VAR
VAR_OUTPUT
  Y : INT;
END_VAR
VAR
  inner : Inner;
END_VAR
  inner(In := X);
  Y := inner.Out * 2;
END_FUNCTION_BLOCK

FUNCTION_BLOCK Inner
VAR_INPUT
  In : INT;
END_VAR
VAR_OUTPUT
  Out : INT;
END_VAR
  Out := In + 1;
END_FUNCTION_BLOCK

PROGRAM main
VAR
  o : Outer;
END_VAR
VAR_GLOBAL
  result : INT;
END_VAR
  o(X := 5);
  result := o.Y;
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
	if got := host.vals["result"].I; got != 12 { // (5 + 1) * 2
		t.Fatalf("expected result=12, got %d", got)
	}
}
