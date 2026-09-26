package st

import (
	"math"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/lang/ir"
)

// A FUNCTION_BLOCK that declares a VAR CONSTANT block and reads one of
// those constants must still write back its VAR_IN_OUT and `=>`-captured
// VAR_OUTPUT parameters to the caller.

func runScans(t *testing.T, src string, host *fakeHost, scans int) {
	t.Helper()
	prog, err := Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	irProg, err := Lower(prog)
	if err != nil {
		t.Fatalf("lower: %v", err)
	}
	frame := ir.NewFrame(irProg)
	for i := 0; i < scans; i++ {
		if err := ir.Run(irProg, frame, host); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
}

func TestUserFB_VarConstant_InOutWriteBack(t *testing.T) {
	src := `
FUNCTION_BLOCK Integrator
VAR_INPUT  Rate : REAL; Dt : REAL; END_VAR
VAR_IN_OUT Acc  : REAL; END_VAR
VAR CONSTANT Gain : REAL := 2.0; END_VAR
Acc := Acc + Rate * Gain * Dt;
END_FUNCTION_BLOCK

PROGRAM Main
VAR_EXTERNAL Total : REAL; END_VAR
VAR i : Integrator; END_VAR
i(Rate := 1.0, Dt := 0.1, Acc := Total);
END_PROGRAM
`
	host := newFakeHost()
	host.vals["Total"] = ir.RealVal(1.0)
	runScans(t, src, host, 3)
	if got := host.vals["Total"]; got.Kind != ir.TypeReal || math.Abs(got.F-1.6) > 1e-9 {
		t.Fatalf("Total = %+v, want 1.6", got)
	}
}

func TestUserFB_VarConstant_OutputCapture(t *testing.T) {
	src := `
FUNCTION_BLOCK Scale
VAR_INPUT  X : REAL; END_VAR
VAR_OUTPUT Y : REAL; END_VAR
VAR CONSTANT Gain : REAL := 3.0; END_VAR
Y := X * Gain;
END_FUNCTION_BLOCK

PROGRAM Main
VAR_EXTERNAL Out : REAL; END_VAR
VAR s : Scale; END_VAR
s(X := 2.0, Y => Out);
END_PROGRAM
`
	host := newFakeHost()
	runScans(t, src, host, 1)
	if got := host.vals["Out"]; got.Kind != ir.TypeReal || got.F != 6.0 {
		t.Fatalf("Out = %+v, want 6.0", got)
	}
}

// The root cause was broader than CONSTANT: every declared initial value on
// a FUNCTION_BLOCK variable was dropped at instance allocation. A plain VAR
// and a VAR_OUTPUT start at their declaration too.
func TestUserFB_InitialValues(t *testing.T) {
	src := `
FUNCTION_BLOCK Counter
VAR_OUTPUT Count : INT := 10; END_VAR
VAR Step : INT := 5; END_VAR
Count := Count + Step;
END_FUNCTION_BLOCK

PROGRAM Main
VAR_EXTERNAL N : INT; END_VAR
VAR c : Counter; END_VAR
c(Count => N);
END_PROGRAM
`
	host := newFakeHost()
	runScans(t, src, host, 2)
	if got := host.vals["N"]; got.Kind != ir.TypeInt || got.I != 20 {
		t.Fatalf("N = %+v, want 20", got)
	}
}

// A FUNCTION's variables start at their declared value on every call.
func TestUserFunc_InitialValues(t *testing.T) {
	src := `
FUNCTION Scale : REAL
VAR_INPUT X : REAL; END_VAR
VAR CONSTANT Gain : REAL := 3.0; END_VAR
VAR acc : REAL := 1.0; END_VAR
acc := acc + X * Gain;
Scale := acc;
END_FUNCTION

PROGRAM Main
VAR_EXTERNAL Out : REAL; END_VAR
Out := Scale(X := 2.0);
END_PROGRAM
`
	host := newFakeHost()
	runScans(t, src, host, 2) // re-initialised per call: 7, not 13
	if got := host.vals["Out"]; got.Kind != ir.TypeReal || got.F != 7.0 {
		t.Fatalf("Out = %+v, want 7.0", got)
	}
}

// An online edit that changes a constant's value must land, program-level
// and inside an FB instance alike; ordinary state still carries.
func TestMigrateTakesNewConstantValue(t *testing.T) {
	compile := func(gain string) *ir.Program {
		src := `
FUNCTION_BLOCK Acc
VAR_OUTPUT Y : REAL; END_VAR
VAR CONSTANT K : REAL := ` + gain + `; END_VAR
Y := Y + K;
END_FUNCTION_BLOCK

PROGRAM Main
VAR_EXTERNAL Out : REAL; Kp : REAL; END_VAR
VAR CONSTANT P : REAL := ` + gain + `; END_VAR
VAR a : Acc; END_VAR
a();
Out := a.Y;
Kp := P;
END_PROGRAM
`
		prog, err := Parse(src)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		irProg, err := Lower(prog)
		if err != nil {
			t.Fatalf("lower: %v", err)
		}
		return irProg
	}
	host := newFakeHost()
	old := compile("1.0")
	frame := ir.NewFrame(old)
	for i := 0; i < 2; i++ {
		if err := ir.Run(old, frame, host); err != nil {
			t.Fatal(err)
		}
	}
	next := compile("10.0")
	nf, resets := ir.MigrateFrame(next, old, frame)
	if len(resets) != 0 {
		t.Fatalf("unexpected resets %v", resets)
	}
	if err := ir.Run(next, nf, host); err != nil {
		t.Fatal(err)
	}
	if got := host.vals["Out"].F; got != 12.0 { // 1 + 1 carried, + 10
		t.Errorf("Out = %v, want 12", got)
	}
	if got := host.vals["Kp"].F; got != 10.0 {
		t.Errorf("Kp = %v, want 10", got)
	}
}

// An initialiser on a VAR_EXTERNAL/VAR_GLOBAL has nowhere to land (the tag's
// value lives in the store), so it is a compile error, not silently dropped.
func TestExternalInitialValueRejected(t *testing.T) {
	for _, src := range []string{
		"PROGRAM Main\nVAR_EXTERNAL X : REAL := 5.0; END_VAR\nX := X;\nEND_PROGRAM\n",
		"FUNCTION_BLOCK F\nVAR_EXTERNAL X : REAL := 5.0; END_VAR\nX := X;\nEND_FUNCTION_BLOCK\nPROGRAM Main\nVAR f : F; END_VAR\nf();\nEND_PROGRAM\n",
	} {
		prog, err := Parse(src)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if _, err := Lower(prog); err == nil || !strings.Contains(err.Error(), "init:") {
			t.Errorf("want an initial-value error, got %v\n%s", err, src)
		}
	}
}
