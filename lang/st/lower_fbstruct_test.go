package st

import (
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/lang/ir"
)

// A user TYPE is a legal FUNCTION_BLOCK pin type. The block below takes a
// nested struct in, scales it, and hands a nested struct back out; the
// PROGRAM binds a VAR_EXTERNAL struct tag on both ends, which is the shape
// a real site program has (one pin instead of a dozen scalars).
func TestFBStructPins(t *testing.T) {
	src := `
TYPE
  Range : STRUCT
    LO : REAL;
    HI : REAL;
  END_STRUCT;
  AnalogInput : STRUCT
    RAW   : INT;
    SCALE : Range;
    VALUE : REAL;
    FAULT : BOOL;
  END_STRUCT;
END_TYPE

FUNCTION_BLOCK FB_Scale
VAR_INPUT
  IN : AnalogInput;
END_VAR
VAR_OUTPUT
  OUT : AnalogInput;
END_VAR
VAR
  span : REAL;
END_VAR
  span := IN.SCALE.HI - IN.SCALE.LO;
  OUT := IN;
  OUT.VALUE := IN.SCALE.LO + span * INT_TO_REAL(IN.RAW) / 32767.0;
  OUT.FAULT := IN.RAW < 0;
END_FUNCTION_BLOCK

PROGRAM main
VAR
  s : FB_Scale;
END_VAR
VAR_EXTERNAL
  FIT001 : AnalogInput;
END_VAR
VAR
  whole : AnalogInput;
END_VAR
VAR_GLOBAL
  scaled : REAL;
  faulted : BOOL;
  wholeValue : REAL;
END_VAR
  s(IN := FIT001);
  scaled := s.OUT.VALUE;
  faulted := s.OUT.FAULT;
  whole := s.OUT;          (* a struct output reads whole, too *)
  wholeValue := whole.VALUE;
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
	// Seed the tag: RAW at half scale over 0..100.
	ai := ir.Zero(irProg.Types["AnalogInput"])
	ai.Fld[0] = ir.IntVal(16383)                                        // RAW
	ai.Fld[1].Fld[0], ai.Fld[1].Fld[1] = ir.RealVal(0), ir.RealVal(100) // SCALE.LO/.HI
	host.vals["FIT001"] = ai

	frame := ir.NewFrame(irProg)
	if err := ir.Run(irProg, frame, host); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := host.vals["scaled"].F; got < 49.9 || got > 50.1 {
		t.Fatalf("scaled = %v, want ~50", got)
	}
	if host.vals["faulted"].B {
		t.Fatalf("faulted should be FALSE for a positive raw count")
	}
	if got := host.vals["wholeValue"].F; got != host.vals["scaled"].F {
		t.Fatalf("whole-struct read of the OUT pin = %v, want %v", got, host.vals["scaled"].F)
	}

	// The FB's declared pin types survived signature resolution.
	def := irProg.UserFBs[0]
	if def.Inputs[0].Type.Kind != ir.TypeStruct || def.Inputs[0].Type.Struct.Name != "AnalogInput" {
		t.Fatalf("IN pin type = %v, want AnalogInput struct", def.Inputs[0].Type)
	}
	if def.Outputs[0].Type.Struct.Name != "AnalogInput" {
		t.Fatalf("OUT pin type = %v, want AnalogInput struct", def.Outputs[0].Type)
	}
}

// A TYPE declared in a library file is in scope for an FB pin: libraries
// compose by joining ahead of the program, so this is the shape a project
// with lib/types.st and lib/blocks.st actually compiles as.
func TestFBStructPinFromLibraryFile(t *testing.T) {
	lib := `
TYPE
  Motor : STRUCT
    Running : BOOL;
    Speed   : REAL;
  END_STRUCT;
END_TYPE
`
	blocks := `
FUNCTION_BLOCK FB_MotorView
VAR_INPUT  M : Motor; END_VAR
VAR_OUTPUT Fast : BOOL; END_VAR
  Fast := M.Running AND M.Speed > 50.0;
END_FUNCTION_BLOCK
`
	program := `
PROGRAM main
VAR v : FB_MotorView; END_VAR
VAR_EXTERNAL P101 : Motor; END_VAR
VAR_GLOBAL fast : BOOL; END_VAR
  v(M := P101);
  fast := v.Fast;
END_PROGRAM
`
	host := newFakeHost()
	irProg := mustLower(t, lib+blocks+program)
	m := ir.Zero(irProg.Types["Motor"])
	m.Fld[0], m.Fld[1] = ir.BoolVal(true), ir.RealVal(80)
	host.vals["P101"] = m

	frame := ir.NewFrame(irProg)
	if err := ir.Run(irProg, frame, host); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !host.vals["fast"].B {
		t.Fatalf("fast should be TRUE at 80 rpm")
	}
}

// An FB pin naming a type nobody declared is still a clear compile error —
// the point of threading the TYPE table through is resolution, not silence.
func TestFBUnknownPinTypeStillErrors(t *testing.T) {
	src := `
FUNCTION_BLOCK FB_Bad
VAR_INPUT  IN : NoSuchType; END_VAR
VAR_OUTPUT Q : BOOL; END_VAR
  Q := TRUE;
END_FUNCTION_BLOCK

PROGRAM main
VAR b : FB_Bad; END_VAR
  b();
END_PROGRAM
`
	_, err := lowerSrc(src)
	if err == nil {
		t.Fatal("expected an error for an undeclared pin type")
	}
	if !strings.Contains(err.Error(), `unknown type "NoSuchType"`) {
		t.Fatalf("error = %v, want it to name the unknown type", err)
	}
}

// ─── VAR_IN_OUT ────────────────────────────────────────────────────────────

// A scalar VAR_IN_OUT: the block reads the caller's variable and writes it,
// and the write is visible to the caller after the call.
func TestVarInOutScalar(t *testing.T) {
	src := `
FUNCTION_BLOCK FB_Bump
VAR_IN_OUT
  N : INT;
END_VAR
VAR_INPUT
  Amt : INT;
END_VAR
  N := N + Amt;
END_FUNCTION_BLOCK

PROGRAM main
VAR
  b : FB_Bump;
  count : INT;
END_VAR
VAR_GLOBAL
  seen : INT;
END_VAR
  b(N := count, Amt := 5);
  seen := count;
END_PROGRAM
`
	host := newFakeHost()
	irProg := mustLower(t, src)
	frame := ir.NewFrame(irProg)
	for scan, want := range []int64{5, 10, 15} {
		if err := ir.Run(irProg, frame, host); err != nil {
			t.Fatalf("scan %d: %v", scan, err)
		}
		if got := host.vals["seen"].I; got != want {
			t.Fatalf("scan %d: count = %d, want %d", scan, got, want)
		}
	}
}

// A struct VAR_IN_OUT bound to a plain local: the block mutates fields and
// the caller sees them. Nested structs work the same way.
func TestVarInOutStructLocal(t *testing.T) {
	src := `
TYPE
  Limits : STRUCT LO : REAL; HI : REAL; END_STRUCT;
  Analog : STRUCT VALUE : REAL; ALARM : BOOL; LIM : Limits; END_STRUCT;
END_TYPE

FUNCTION_BLOCK FB_Alarm
VAR_IN_OUT
  AI : Analog;
END_VAR
  AI.ALARM := AI.VALUE > AI.LIM.HI OR AI.VALUE < AI.LIM.LO;
  AI.LIM.LO := AI.LIM.LO + 1.0;
END_FUNCTION_BLOCK

PROGRAM main
VAR
  a : FB_Alarm;
  tank : Analog;
END_VAR
VAR_GLOBAL
  alarm : BOOL;
  lo : REAL;
END_VAR
  tank.VALUE := 95.0;
  tank.LIM.HI := 90.0;
  a(AI := tank);
  alarm := tank.ALARM;
  lo := tank.LIM.LO;
END_PROGRAM
`
	host := newFakeHost()
	irProg := mustLower(t, src)
	frame := ir.NewFrame(irProg)
	if err := ir.Run(irProg, frame, host); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !host.vals["alarm"].B {
		t.Fatal("caller should see ALARM written through the VAR_IN_OUT")
	}
	if got := host.vals["lo"].F; got != 1 {
		t.Fatalf("nested field write-back: LO = %v, want 1", got)
	}
	// Second scan proves the write-back landed in the caller's variable,
	// not just in the block's retained pin.
	if err := ir.Run(irProg, frame, host); err != nil {
		t.Fatalf("run 2: %v", err)
	}
	if got := host.vals["lo"].F; got != 2 {
		t.Fatalf("scan 2: LO = %v, want 2", got)
	}
}

// A VAR_IN_OUT bound to a VAR_EXTERNAL struct tag round-trips through the
// tag store: read on copy-in, whole-struct write on copy-back.
func TestVarInOutExternalStructTag(t *testing.T) {
	src := `
TYPE
  Pump : STRUCT Running : BOOL; Starts : INT; END_STRUCT;
END_TYPE

FUNCTION_BLOCK FB_Starter
VAR_IN_OUT
  P : Pump;
END_VAR
VAR_INPUT
  Cmd : BOOL;
END_VAR
  IF Cmd AND NOT P.Running THEN
    P.Starts := P.Starts + 1;
  END_IF;
  P.Running := Cmd;
END_FUNCTION_BLOCK

PROGRAM main
VAR s : FB_Starter; END_VAR
VAR_EXTERNAL P101 : Pump; END_VAR
VAR_GLOBAL Run : BOOL; END_VAR
  s(P := P101, Cmd := Run);
END_PROGRAM
`
	host := newFakeHost()
	irProg := mustLower(t, src)
	host.vals["P101"] = ir.Zero(irProg.Types["Pump"])
	frame := ir.NewFrame(irProg)

	host.vals["Run"] = ir.BoolVal(true)
	for i := 0; i < 3; i++ {
		if err := ir.Run(irProg, frame, host); err != nil {
			t.Fatalf("scan %d: %v", i, err)
		}
	}
	p := host.vals["P101"]
	if !p.Fld[0].B {
		t.Fatal("tag store should show the pump Running")
	}
	if p.Fld[1].I != 1 {
		t.Fatalf("Starts = %d, want 1 (the block sees its own previous write-back)", p.Fld[1].I)
	}

	host.vals["Run"] = ir.BoolVal(false)
	if err := ir.Run(irProg, frame, host); err != nil {
		t.Fatalf("stop scan: %v", err)
	}
	host.vals["Run"] = ir.BoolVal(true)
	if err := ir.Run(irProg, frame, host); err != nil {
		t.Fatalf("restart scan: %v", err)
	}
	if got := host.vals["P101"].Fld[1].I; got != 2 {
		t.Fatalf("Starts = %d after a restart, want 2", got)
	}
}

// A VAR_IN_OUT bound to a field of a VAR_EXTERNAL struct writes back
// through the field path — the tag store holds the aggregate, so the VM
// does the read-modify-write the program used to have to spell out.
func TestVarInOutExternalStructField(t *testing.T) {
	src := `
TYPE
  Pump : STRUCT Running : BOOL; Starts : INT; END_STRUCT;
END_TYPE

FUNCTION_BLOCK FB_Count
VAR_IN_OUT N : INT; END_VAR
  N := N + 1;
END_FUNCTION_BLOCK

PROGRAM main
VAR c : FB_Count; END_VAR
VAR_EXTERNAL P101 : Pump; END_VAR
  c(N := P101.Starts);
END_PROGRAM
`
	host := newFakeHost()
	irProg := mustLower(t, src)
	host.vals["P101"] = ir.Zero(irProg.Types["Pump"])
	frame := ir.NewFrame(irProg)
	for i := 0; i < 4; i++ {
		if err := ir.Run(irProg, frame, host); err != nil {
			t.Fatalf("scan %d: %v", i, err)
		}
	}
	if got := host.vals["P101"].Fld[1].I; got != 4 {
		t.Fatalf("P101.Starts = %d, want 4", got)
	}
}

// Assigning a FIELD of a VAR_EXTERNAL struct directly used to fault every
// scan ("no store path for a field of a global"); it now read-modify-writes
// the tag.
func TestExternalStructFieldAssignment(t *testing.T) {
	src := `
TYPE Motor : STRUCT Running : BOOL; Speed : REAL; END_STRUCT; END_TYPE

PROGRAM main
VAR_EXTERNAL P101 : Motor; END_VAR
VAR_GLOBAL Cmd : BOOL; END_VAR
  P101.Running := Cmd;
  P101.Speed := 61.5;
END_PROGRAM
`
	host := newFakeHost()
	irProg := mustLower(t, src)
	host.vals["P101"] = ir.Zero(irProg.Types["Motor"])
	host.vals["Cmd"] = ir.BoolVal(true)
	frame := ir.NewFrame(irProg)
	if err := ir.Run(irProg, frame, host); err != nil {
		t.Fatalf("run: %v", err)
	}
	p := host.vals["P101"]
	if !p.Fld[0].B || p.Fld[1].F != 61.5 {
		t.Fatalf("P101 = %+v, want Running=TRUE Speed=61.5", p)
	}
}

// A VAR_IN_OUT has to be given somewhere to write back to.
func TestVarInOutRejectsNonLValue(t *testing.T) {
	cases := []struct{ name, arg, want string }{
		{"expression", "count + 1", "needs a variable to write back to"},
		{"literal", "7", "needs a variable to write back to"},
		{"function call", "ABS(count)", "needs a variable to write back to"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := `
FUNCTION_BLOCK FB_Bump
VAR_IN_OUT N : INT; END_VAR
  N := N + 1;
END_FUNCTION_BLOCK

PROGRAM main
VAR b : FB_Bump; count : INT; END_VAR
  b(N := ` + tc.arg + `);
END_PROGRAM
`
			_, err := lowerSrc(src)
			if err == nil {
				t.Fatalf("expected a compile error binding %q to a VAR_IN_OUT", tc.arg)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

// Every VAR_IN_OUT must be bound: there is no default for a reference.
func TestVarInOutMustBeBound(t *testing.T) {
	src := `
FUNCTION_BLOCK FB_Bump
VAR_IN_OUT N : INT; END_VAR
VAR_INPUT Amt : INT; END_VAR
  N := N + Amt;
END_FUNCTION_BLOCK

PROGRAM main
VAR b : FB_Bump; count : INT; END_VAR
  b(Amt := 2);
END_PROGRAM
`
	_, err := lowerSrc(src)
	if err == nil || !strings.Contains(err.Error(), "must be bound at every call site") {
		t.Fatalf("error = %v, want an unbound-VAR_IN_OUT diagnostic", err)
	}
}

// A VAR_IN_OUT is by reference, so its argument's type has to match exactly
// — an INT variable cannot stand in for a REAL reference.
func TestVarInOutTypeMustMatch(t *testing.T) {
	src := `
FUNCTION_BLOCK FB_Bump
VAR_IN_OUT X : REAL; END_VAR
  X := X + 1.0;
END_FUNCTION_BLOCK

PROGRAM main
VAR b : FB_Bump; n : INT; END_VAR
  b(X := n);
END_PROGRAM
`
	_, err := lowerSrc(src)
	if err == nil || !strings.Contains(err.Error(), "needs a REAL variable") {
		t.Fatalf("error = %v, want a type-mismatch diagnostic", err)
	}
}

// A struct passed to a VAR_INPUT is a COPY: the block may scribble on its
// pin without the caller's variable changing. (VAR_IN_OUT is the pin that
// writes back — that is the whole distinction.)
func TestFBStructInputIsCopied(t *testing.T) {
	src := `
TYPE Box : STRUCT N : INT; END_STRUCT; END_TYPE

FUNCTION_BLOCK FB_Scribble
VAR_INPUT B : Box; END_VAR
VAR_OUTPUT N : INT; END_VAR
  B.N := 99;
  N := B.N;
END_FUNCTION_BLOCK

PROGRAM main
VAR s : FB_Scribble; mine : Box; END_VAR
VAR_GLOBAL kept : INT; END_VAR
  mine.N := 1;
  s(B := mine);
  kept := mine.N;
END_PROGRAM
`
	host := newFakeHost()
	irProg := mustLower(t, src)
	frame := ir.NewFrame(irProg)
	if err := ir.Run(irProg, frame, host); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := host.vals["kept"].I; got != 1 {
		t.Fatalf("caller's struct = %d, want 1 — a VAR_INPUT struct must be a copy", got)
	}
}

// Assigning a struct copies it: two variables must not share field storage.
func TestStructAssignmentIsACopy(t *testing.T) {
	src := `
TYPE Box : STRUCT N : INT; END_STRUCT; END_TYPE

PROGRAM main
VAR a : Box; b : Box; END_VAR
VAR_GLOBAL an : INT; bn : INT; END_VAR
  a.N := 1;
  b := a;
  b.N := 2;
  an := a.N;
  bn := b.N;
END_PROGRAM
`
	host := newFakeHost()
	irProg := mustLower(t, src)
	frame := ir.NewFrame(irProg)
	if err := ir.Run(irProg, frame, host); err != nil {
		t.Fatalf("run: %v", err)
	}
	if host.vals["an"].I != 1 || host.vals["bn"].I != 2 {
		t.Fatalf("a.N=%d b.N=%d, want 1 and 2", host.vals["an"].I, host.vals["bn"].I)
	}
}

// ─── helpers ───────────────────────────────────────────────────────────────

func lowerSrc(src string) (*ir.Program, error) {
	prog, err := Parse(src)
	if err != nil {
		return nil, err
	}
	return Lower(prog)
}

func mustLower(t *testing.T, src string) *ir.Program {
	t.Helper()
	irProg, err := lowerSrc(src)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return irProg
}

// An FB instance addresses its pins by slot rather than by struct field, so
// the assignment path has to know the difference — writing a pin directly
// used to fault the scan with "field index out of bounds".
func TestAssignToFBPin(t *testing.T) {
	src := `
FUNCTION_BLOCK FB_Hold
VAR_INPUT  Preset : INT; END_VAR
VAR_OUTPUT Q : INT; END_VAR
  Q := Preset;
END_FUNCTION_BLOCK

PROGRAM main
VAR h : FB_Hold; END_VAR
VAR_GLOBAL out : INT; END_VAR
  h.Preset := 12;
  h();
  out := h.Q;
END_PROGRAM
`
	host := newFakeHost()
	irProg := mustLower(t, src)
	frame := ir.NewFrame(irProg)
	if err := ir.Run(irProg, frame, host); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := host.vals["out"].I; got != 12 {
		t.Fatalf("out = %d, want 12", got)
	}
}

// A VAR_IN_OUT pin may not itself be a function-block type: an instance is
// retained state, not a value, and nothing in the language copies one.
func TestVarInOutRejectsFBPinType(t *testing.T) {
	src := `
FUNCTION_BLOCK FB_Bad
VAR_IN_OUT T : TON; END_VAR
  T(IN := TRUE, PT := T#1S);
END_FUNCTION_BLOCK

PROGRAM main
VAR b : FB_Bad; t1 : TON; END_VAR
  b(T := t1);
END_PROGRAM
`
	_, err := lowerSrc(src)
	if err == nil || !strings.Contains(err.Error(), "cannot be passed by reference") {
		t.Fatalf("error = %v, want an FB-typed-VAR_IN_OUT diagnostic", err)
	}
}
