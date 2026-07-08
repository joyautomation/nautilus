package st

import (
	"testing"

	"github.com/joyautomation/nautilus/lang/ir"
)

// fakeHost is a minimal Host for test programs that touch globals.
type fakeHost struct {
	vals map[string]ir.Value
	now  int64
}

func newFakeHost() *fakeHost { return &fakeHost{vals: map[string]ir.Value{}} }

func (h *fakeHost) ReadGlobal(name string) (ir.Value, error) {
	v, ok := h.vals[name]
	if !ok {
		return ir.Value{}, nil
	}
	return v, nil
}

func (h *fakeHost) WriteGlobal(name string, v ir.Value) error { h.vals[name] = v; return nil }
func (h *fakeHost) NowMs() int64                              { return h.now }

func compileAndRun(t *testing.T, src string, host *fakeHost) (*ir.Program, *ir.Frame) {
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
	if err := ir.Run(irProg, frame, host); err != nil {
		t.Fatalf("run: %v", err)
	}
	return irProg, frame
}

func TestBuiltinABS(t *testing.T) {
	host := newFakeHost()
	host.vals["x"] = ir.IntVal(-5)
	host.vals["y"] = ir.IntVal(0)
	src := `
PROGRAM p
VAR_GLOBAL
    x : INT;
    y : INT;
END_VAR
y := ABS(x);
END_PROGRAM`
	compileAndRun(t, src, host)
	if got := host.vals["y"].I; got != 5 {
		t.Errorf("ABS(-5) = %d, want 5", got)
	}
}

func TestBuiltinMinMaxLimit(t *testing.T) {
	host := newFakeHost()
	host.vals["a"] = ir.IntVal(7)
	host.vals["b"] = ir.IntVal(3)
	host.vals["c"] = ir.IntVal(50)
	host.vals["minOut"] = ir.IntVal(0)
	host.vals["maxOut"] = ir.IntVal(0)
	host.vals["clamped"] = ir.IntVal(0)
	src := `
PROGRAM p
VAR_GLOBAL
    a : INT;
    b : INT;
    c : INT;
    minOut : INT;
    maxOut : INT;
    clamped : INT;
END_VAR
minOut := MIN(a, b);
maxOut := MAX(a, b);
clamped := LIMIT(0, c, 10);
END_PROGRAM`
	compileAndRun(t, src, host)
	if got := host.vals["minOut"].I; got != 3 {
		t.Errorf("MIN = %d, want 3", got)
	}
	if got := host.vals["maxOut"].I; got != 7 {
		t.Errorf("MAX = %d, want 7", got)
	}
	if got := host.vals["clamped"].I; got != 10 {
		t.Errorf("LIMIT = %d, want 10", got)
	}
}

func TestBuiltinConversions(t *testing.T) {
	host := newFakeHost()
	host.vals["i"] = ir.IntVal(7)
	host.vals["r"] = ir.RealVal(0)
	src := `
PROGRAM p
VAR_GLOBAL
    i : INT;
    r : REAL;
END_VAR
r := INT_TO_REAL(i) / 2.0;
END_PROGRAM`
	compileAndRun(t, src, host)
	if got := host.vals["r"].F; got != 3.5 {
		t.Errorf("INT_TO_REAL(7)/2.0 = %v, want 3.5", got)
	}
}

func TestFBTONReachesQAfterPT(t *testing.T) {
	host := newFakeHost()
	host.vals["start"] = ir.BoolVal(true)
	host.vals["done"] = ir.BoolVal(false)
	src := `
PROGRAM p
VAR
    t1 : TON;
END_VAR
VAR_GLOBAL
    start : BOOL;
    done : BOOL;
END_VAR
t1(IN := start, PT := T#100ms);
done := t1.Q;
END_PROGRAM`
	prog, err := Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	irProg, err := Lower(prog)
	if err != nil {
		t.Fatalf("lower: %v", err)
	}
	frame := ir.NewFrame(irProg)

	// Scan 0: start=true, NowMs=1000 — Q stays false (just armed).
	host.now = 1000
	if err := ir.Run(irProg, frame, host); err != nil {
		t.Fatalf("scan 0: %v", err)
	}
	if host.vals["done"].B {
		t.Errorf("Q true at t=0 after IN rise, want false")
	}
	// Scan 1: 50ms in — still below PT.
	host.now = 1050
	_ = ir.Run(irProg, frame, host)
	if host.vals["done"].B {
		t.Errorf("Q true at 50ms, want false (PT=100ms)")
	}
	// Scan 2: 100ms — reaches PT.
	host.now = 1100
	_ = ir.Run(irProg, frame, host)
	if !host.vals["done"].B {
		t.Errorf("Q false at 100ms, want true")
	}
	// Drop IN — Q falls immediately.
	host.vals["start"] = ir.BoolVal(false)
	host.now = 1200
	_ = ir.Run(irProg, frame, host)
	if host.vals["done"].B {
		t.Errorf("Q true after IN dropped, want false")
	}
}

func TestFBRTrigDetectsRisingEdge(t *testing.T) {
	host := newFakeHost()
	host.vals["clk"] = ir.BoolVal(false)
	host.vals["edge"] = ir.BoolVal(false)
	src := `
PROGRAM p
VAR
    r : R_TRIG;
END_VAR
VAR_GLOBAL
    clk : BOOL;
    edge : BOOL;
END_VAR
r(CLK := clk);
edge := r.Q;
END_PROGRAM`
	_, frame := compileAndRun(t, src, host)
	prog, _ := Parse(src)
	irProg, _ := Lower(prog)
	_ = irProg
	if host.vals["edge"].B {
		t.Errorf("edge true with CLK low, want false")
	}
	host.vals["clk"] = ir.BoolVal(true)
	_ = ir.Run(irProg, frame, host)
	if !host.vals["edge"].B {
		t.Errorf("edge false on rising edge, want true")
	}
	_ = ir.Run(irProg, frame, host)
	if host.vals["edge"].B {
		t.Errorf("edge stayed true on second steady-high scan, want false")
	}
}

func TestFBCTUCountsAndResets(t *testing.T) {
	host := newFakeHost()
	host.vals["pulse"] = ir.BoolVal(false)
	host.vals["reset"] = ir.BoolVal(false)
	host.vals["count"] = ir.IntVal(0)
	host.vals["full"] = ir.BoolVal(false)
	src := `
PROGRAM p
VAR
    c : CTU;
END_VAR
VAR_GLOBAL
    pulse : BOOL;
    reset : BOOL;
    count : INT;
    full : BOOL;
END_VAR
c(CU := pulse, R := reset, PV := 3);
count := c.CV;
full := c.Q;
END_PROGRAM`
	prog, err := Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	irProg, err := Lower(prog)
	if err != nil {
		t.Fatalf("lower: %v", err)
	}
	frame := ir.NewFrame(irProg)

	// 3 rising edges to reach PV.
	for i := 0; i < 3; i++ {
		host.vals["pulse"] = ir.BoolVal(true)
		_ = ir.Run(irProg, frame, host)
		host.vals["pulse"] = ir.BoolVal(false)
		_ = ir.Run(irProg, frame, host)
	}
	if host.vals["count"].I != 3 {
		t.Errorf("count = %d, want 3", host.vals["count"].I)
	}
	if !host.vals["full"].B {
		t.Errorf("Q false at CV=PV, want true")
	}
	// Reset.
	host.vals["reset"] = ir.BoolVal(true)
	_ = ir.Run(irProg, frame, host)
	if host.vals["count"].I != 0 {
		t.Errorf("count after R = %d, want 0", host.vals["count"].I)
	}
}

func TestUnknownFunctionErrors(t *testing.T) {
	src := `
PROGRAM p
VAR_GLOBAL
    x : INT;
END_VAR
x := WIBBLE(1);
END_PROGRAM`
	prog, err := Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, err := Lower(prog); err == nil {
		t.Fatalf("expected error for unknown function")
	}
}

func TestFBCallWithoutInstanceErrors(t *testing.T) {
	src := `
PROGRAM p
VAR_GLOBAL
    x : INT;
END_VAR
TON(IN := TRUE, PT := T#1s);
END_PROGRAM`
	prog, err := Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, err := Lower(prog); err == nil {
		t.Fatalf("expected error: TON used as bare call without instance")
	}
}
