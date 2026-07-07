
package st

import (
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/lang/ir"
)

type stubHost struct {
	globals map[string]ir.Value
	now     int64
}

func newStubHost() *stubHost { return &stubHost{globals: map[string]ir.Value{}} }

func (h *stubHost) ReadGlobal(name string) (ir.Value, error)  { return h.globals[name], nil }
func (h *stubHost) WriteGlobal(name string, v ir.Value) error { h.globals[name] = v; return nil }
func (h *stubHost) NowMs() int64                              { return h.now }

// lowerSource parses, lowers, and returns both the program and any error.
func lowerSource(t *testing.T, src string) *ir.Program {
	t.Helper()
	ast, err := Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	prog, err := Lower(ast)
	if err != nil {
		t.Fatalf("lower: %v", err)
	}
	return prog
}

func lowerExpectErr(t *testing.T, src, want string) {
	t.Helper()
	ast, err := Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	_, err = Lower(ast)
	if err == nil {
		t.Fatalf("expected error containing %q, got nil", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error %q does not contain %q", err.Error(), want)
	}
}

func TestLowerScalarAssign(t *testing.T) {
	src := `
PROGRAM p
VAR
    x : INT;
    y : INT := 5;
END_VAR
x := y + 3;
END_PROGRAM`
	prog := lowerSource(t, src)
	frame := ir.NewFrame(prog)
	if err := ir.Run(prog, frame, newStubHost()); err != nil {
		t.Fatal(err)
	}
	xSlot := prog.SlotIndex["x"]
	if got := frame.Slots[xSlot].I; got != 8 {
		t.Errorf("x = %d, want 8", got)
	}
}

func TestLowerIfElsif(t *testing.T) {
	src := `
PROGRAM p
VAR
    x : INT := 2;
    result : INT;
END_VAR
IF x = 1 THEN
    result := 10;
ELSIF x = 2 THEN
    result := 20;
ELSIF x = 3 THEN
    result := 30;
ELSE
    result := 99;
END_IF;
END_PROGRAM`
	prog := lowerSource(t, src)
	frame := ir.NewFrame(prog)
	if err := ir.Run(prog, frame, newStubHost()); err != nil {
		t.Fatal(err)
	}
	if got := frame.Slots[prog.SlotIndex["result"]].I; got != 20 {
		t.Errorf("result = %d, want 20", got)
	}
}

func TestLowerForLoopSum(t *testing.T) {
	src := `
PROGRAM p
VAR
    i : INT;
    sum : INT;
END_VAR
FOR i := 1 TO 10 DO
    sum := sum + i;
END_FOR;
END_PROGRAM`
	prog := lowerSource(t, src)
	frame := ir.NewFrame(prog)
	if err := ir.Run(prog, frame, newStubHost()); err != nil {
		t.Fatal(err)
	}
	if got := frame.Slots[prog.SlotIndex["sum"]].I; got != 55 {
		t.Errorf("sum = %d, want 55", got)
	}
}

func TestLowerWhileWithExit(t *testing.T) {
	src := `
PROGRAM p
VAR
    n : INT := 0;
END_VAR
WHILE TRUE DO
    n := n + 1;
    IF n >= 5 THEN
        EXIT;
    END_IF;
END_WHILE;
END_PROGRAM`
	prog := lowerSource(t, src)
	frame := ir.NewFrame(prog)
	if err := ir.Run(prog, frame, newStubHost()); err != nil {
		t.Fatal(err)
	}
	if got := frame.Slots[prog.SlotIndex["n"]].I; got != 5 {
		t.Errorf("n = %d, want 5", got)
	}
}

func TestLowerContinueSkipsEven(t *testing.T) {
	src := `
PROGRAM p
VAR
    i : INT;
    odd : INT;
END_VAR
FOR i := 1 TO 10 DO
    IF (i MOD 2) = 0 THEN
        CONTINUE;
    END_IF;
    odd := odd + i;
END_FOR;
END_PROGRAM`
	prog := lowerSource(t, src)
	frame := ir.NewFrame(prog)
	if err := ir.Run(prog, frame, newStubHost()); err != nil {
		t.Fatal(err)
	}
	// 1+3+5+7+9 = 25
	if got := frame.Slots[prog.SlotIndex["odd"]].I; got != 25 {
		t.Errorf("odd = %d, want 25", got)
	}
}

func TestLowerRepeatUntil(t *testing.T) {
	src := `
PROGRAM p
VAR
    n : INT := 0;
END_VAR
REPEAT
    n := n + 1;
UNTIL n >= 4 END_REPEAT;
END_PROGRAM`
	prog := lowerSource(t, src)
	frame := ir.NewFrame(prog)
	if err := ir.Run(prog, frame, newStubHost()); err != nil {
		t.Fatal(err)
	}
	if got := frame.Slots[prog.SlotIndex["n"]].I; got != 4 {
		t.Errorf("n = %d, want 4", got)
	}
}

func TestLowerCaseMultipleValues(t *testing.T) {
	src := `
PROGRAM p
VAR
    x : INT := 3;
    result : INT;
END_VAR
CASE x OF
    1: result := 10;
    2, 3: result := 20;
ELSE
    result := 99;
END_CASE;
END_PROGRAM`
	prog := lowerSource(t, src)
	frame := ir.NewFrame(prog)
	if err := ir.Run(prog, frame, newStubHost()); err != nil {
		t.Fatal(err)
	}
	if got := frame.Slots[prog.SlotIndex["result"]].I; got != 20 {
		t.Errorf("result = %d, want 20", got)
	}
}

func TestLowerArrayOneBasedIndexing(t *testing.T) {
	src := `
PROGRAM p
VAR
    a : ARRAY[1..5] OF INT;
    sum : INT;
END_VAR
a[1] := 10;
a[2] := 20;
a[5] := 50;
sum := a[1] + a[2] + a[5];
END_PROGRAM`
	prog := lowerSource(t, src)
	frame := ir.NewFrame(prog)
	if err := ir.Run(prog, frame, newStubHost()); err != nil {
		t.Fatal(err)
	}
	if got := frame.Slots[prog.SlotIndex["sum"]].I; got != 80 {
		t.Errorf("sum = %d, want 80", got)
	}
	arr := frame.Slots[prog.SlotIndex["a"]].Arr
	if arr[0].I != 10 || arr[1].I != 20 || arr[4].I != 50 {
		t.Errorf("array = %+v, want [10 20 0 0 50]", arr)
	}
}

func TestLowerArrayArbitraryLowerBound(t *testing.T) {
	src := `
PROGRAM p
VAR
    a : ARRAY[10..12] OF INT;
    v : INT;
END_VAR
a[10] := 100;
a[12] := 300;
v := a[10] + a[12];
END_PROGRAM`
	prog := lowerSource(t, src)
	frame := ir.NewFrame(prog)
	if err := ir.Run(prog, frame, newStubHost()); err != nil {
		t.Fatal(err)
	}
	if got := frame.Slots[prog.SlotIndex["v"]].I; got != 400 {
		t.Errorf("v = %d, want 400", got)
	}
}

func TestLowerMultiDimArray(t *testing.T) {
	src := `
PROGRAM p
VAR
    m : ARRAY[1..2, 1..3] OF INT;
    v : INT;
END_VAR
m[1][1] := 11;
m[2][3] := 23;
v := m[1][1] + m[2][3];
END_PROGRAM`
	prog := lowerSource(t, src)
	frame := ir.NewFrame(prog)
	if err := ir.Run(prog, frame, newStubHost()); err != nil {
		t.Fatal(err)
	}
	if got := frame.Slots[prog.SlotIndex["v"]].I; got != 34 {
		t.Errorf("v = %d, want 34", got)
	}
}

func TestLowerUDTFieldAccess(t *testing.T) {
	src := `
TYPE
    Point : STRUCT
        x : INT;
        y : INT;
    END_STRUCT;
END_TYPE

PROGRAM p
VAR
    pt : Point;
    sum : INT;
END_VAR
pt.x := 3;
pt.y := 4;
sum := pt.x + pt.y;
END_PROGRAM`
	prog := lowerSource(t, src)
	frame := ir.NewFrame(prog)
	if err := ir.Run(prog, frame, newStubHost()); err != nil {
		t.Fatal(err)
	}
	if got := frame.Slots[prog.SlotIndex["sum"]].I; got != 7 {
		t.Errorf("sum = %d, want 7", got)
	}
	pt := frame.Slots[prog.SlotIndex["pt"]].Fld
	if pt[0].I != 3 || pt[1].I != 4 {
		t.Errorf("pt = %+v, want x=3 y=4", pt)
	}
}

func TestLowerNestedAggregateAssign(t *testing.T) {
	src := `
TYPE
    Row : STRUCT
        vals : ARRAY[1..3] OF INT;
    END_STRUCT;
END_TYPE

PROGRAM p
VAR
    table : ARRAY[1..2] OF Row;
    v : INT;
END_VAR
table[1].vals[2] := 42;
v := table[1].vals[2];
END_PROGRAM`
	prog := lowerSource(t, src)
	frame := ir.NewFrame(prog)
	if err := ir.Run(prog, frame, newStubHost()); err != nil {
		t.Fatal(err)
	}
	if got := frame.Slots[prog.SlotIndex["v"]].I; got != 42 {
		t.Errorf("v = %d, want 42", got)
	}
}

func TestLowerGlobalVar(t *testing.T) {
	src := `
PROGRAM p
VAR_GLOBAL
    enabled : BOOL;
    fault : BOOL;
    motor_run : BOOL;
END_VAR
motor_run := enabled AND NOT fault;
END_PROGRAM`
	prog := lowerSource(t, src)
	frame := ir.NewFrame(prog)
	host := newStubHost()
	host.globals["enabled"] = ir.BoolVal(true)
	host.globals["fault"] = ir.BoolVal(false)
	if err := ir.Run(prog, frame, host); err != nil {
		t.Fatal(err)
	}
	if !host.globals["motor_run"].B {
		t.Errorf("motor_run = %v, want true", host.globals["motor_run"].B)
	}
}

func TestLowerRealArithmetic(t *testing.T) {
	src := `
PROGRAM p
VAR
    x : REAL;
END_VAR
x := 1.5 + 2.25;
END_PROGRAM`
	prog := lowerSource(t, src)
	frame := ir.NewFrame(prog)
	if err := ir.Run(prog, frame, newStubHost()); err != nil {
		t.Fatal(err)
	}
	if got := frame.Slots[prog.SlotIndex["x"]].F; got != 3.75 {
		t.Errorf("x = %v, want 3.75", got)
	}
}

func TestLowerIntToRealPromotion(t *testing.T) {
	src := `
PROGRAM p
VAR
    i : INT := 3;
    r : REAL;
END_VAR
r := i + 0.5;
END_PROGRAM`
	prog := lowerSource(t, src)
	frame := ir.NewFrame(prog)
	if err := ir.Run(prog, frame, newStubHost()); err != nil {
		t.Fatal(err)
	}
	if got := frame.Slots[prog.SlotIndex["r"]].F; got != 3.5 {
		t.Errorf("r = %v, want 3.5", got)
	}
}

func TestLowerTimeArithmetic(t *testing.T) {
	src := `
PROGRAM p
VAR
    a : TIME := T#2s;
    b : TIME := T#500ms;
    total : TIME;
END_VAR
total := a + b;
END_PROGRAM`
	prog := lowerSource(t, src)
	frame := ir.NewFrame(prog)
	if err := ir.Run(prog, frame, newStubHost()); err != nil {
		t.Fatal(err)
	}
	if got := frame.Slots[prog.SlotIndex["total"]].I; got != 2500 {
		t.Errorf("total = %d ms, want 2500", got)
	}
}

func TestLowerRetainedAccumulator(t *testing.T) {
	src := `
PROGRAM p
VAR RETAIN
    counter : INT := 0;
END_VAR
counter := counter + 1;
END_PROGRAM`
	prog := lowerSource(t, src)
	frame := ir.NewFrame(prog)
	host := newStubHost()
	for i := 0; i < 5; i++ {
		if err := ir.Run(prog, frame, host); err != nil {
			t.Fatal(err)
		}
	}
	if got := frame.Slots[prog.SlotIndex["counter"]].I; got != 5 {
		t.Errorf("counter = %d, want 5", got)
	}
	if !prog.Slots[prog.SlotIndex["counter"]].Retained {
		t.Errorf("counter slot missing Retained flag")
	}
}

func TestLowerBasedLiteral(t *testing.T) {
	src := `
PROGRAM p
VAR
    x : INT;
END_VAR
x := 16#FF;
END_PROGRAM`
	prog := lowerSource(t, src)
	frame := ir.NewFrame(prog)
	if err := ir.Run(prog, frame, newStubHost()); err != nil {
		t.Fatal(err)
	}
	if got := frame.Slots[prog.SlotIndex["x"]].I; got != 255 {
		t.Errorf("x = %d, want 255", got)
	}
}

// ─── Type error detection ────────────────────────────────────────────────────

func TestLowerUndeclaredIdent(t *testing.T) {
	src := `
PROGRAM p
x := 1;
END_PROGRAM`
	lowerExpectErr(t, src, "undeclared identifier")
}

func TestLowerAssignRealToBool(t *testing.T) {
	src := `
PROGRAM p
VAR
    b : BOOL;
END_VAR
b := 1.5;
END_PROGRAM`
	lowerExpectErr(t, src, "cannot assign")
}

func TestLowerTimePlusInt(t *testing.T) {
	src := `
PROGRAM p
VAR
    t : TIME := T#1s;
    n : INT := 1;
END_VAR
t := t + n;
END_PROGRAM`
	lowerExpectErr(t, src, "TIME")
}

func TestLowerNotOnInt(t *testing.T) {
	src := `
PROGRAM p
VAR
    i : INT := 1;
    b : BOOL;
END_VAR
b := NOT i;
END_PROGRAM`
	lowerExpectErr(t, src, "NOT on non-BOOL")
}

func TestLowerIndexNonArray(t *testing.T) {
	src := `
PROGRAM p
VAR
    i : INT := 1;
    v : INT;
END_VAR
v := i[1];
END_PROGRAM`
	lowerExpectErr(t, src, "non-array")
}

func TestLowerMemberOnScalar(t *testing.T) {
	src := `
PROGRAM p
VAR
    i : INT := 1;
    v : INT;
END_VAR
v := i.x;
END_PROGRAM`
	lowerExpectErr(t, src, "non-struct")
}

func TestLowerIfNonBool(t *testing.T) {
	src := `
PROGRAM p
VAR
    i : INT := 1;
END_VAR
IF i THEN
    i := 2;
END_IF;
END_PROGRAM`
	lowerExpectErr(t, src, "IF condition")
}

func TestLowerDuplicateVar(t *testing.T) {
	src := `
PROGRAM p
VAR
    x : INT;
    x : REAL;
END_VAR
END_PROGRAM`
	lowerExpectErr(t, src, "duplicate")
}

func TestLowerUnknownUDT(t *testing.T) {
	src := `
PROGRAM p
VAR
    x : UnknownType;
END_VAR
END_PROGRAM`
	lowerExpectErr(t, src, "unknown type")
}

func TestLowerImplicitGlobals(t *testing.T) {
	src := `
PROGRAM p
drivestatus_ready := TRUE;
IF drivelogicrslt_start THEN
    outputfreq := commandedfreq;
END_IF;
END_PROGRAM`
	ast, err := Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	prog, err := LowerWithOpts(ast, LowerOpts{
		ImplicitGlobals: map[string]*ir.Type{
			"drivestatus_ready":    ir.BoolT,
			"drivelogicrslt_start": ir.BoolT,
			"commandedfreq":        ir.IntT,
			"outputfreq":           ir.IntT,
		},
	})
	if err != nil {
		t.Fatalf("lower: %v", err)
	}
	host := newStubHost()
	frame := ir.NewFrame(prog)
	host.globals["drivelogicrslt_start"] = ir.BoolVal(true)
	host.globals["commandedfreq"] = ir.IntVal(60)
	if err := ir.Run(prog, frame, host); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !host.globals["drivestatus_ready"].B {
		t.Errorf("drivestatus_ready: want TRUE, got FALSE")
	}
	if host.globals["outputfreq"].I != 60 {
		t.Errorf("outputfreq: want 60, got %d", host.globals["outputfreq"].I)
	}
}

func TestLowerExplicitGlobalShadowsImplicit(t *testing.T) {
	// An explicit VAR_GLOBAL declaration wins over the implicit table,
	// so existing programs that already declare their globals keep working.
	src := `
PROGRAM p
VAR_GLOBAL
    x : INT;
END_VAR
x := 7;
END_PROGRAM`
	ast, err := Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, err := LowerWithOpts(ast, LowerOpts{
		ImplicitGlobals: map[string]*ir.Type{"x": ir.BoolT}, // wrong type — must be ignored
	}); err != nil {
		t.Fatalf("lower: %v", err)
	}
}
