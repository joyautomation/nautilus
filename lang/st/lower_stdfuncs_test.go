package st

import (
	"math"
	"testing"

	"github.com/joyautomation/nautilus/lang/ir"
)

// The standard-function library added in builtins_std.go: selection, bit
// operations, remaining numerics, strings, and string conversions — driven
// end to end through the ST compiler, exactly as FBD-transpiled code will
// call them.

func TestBuiltinSelMux(t *testing.T) {
	host := newFakeHost()
	host.vals["g"] = ir.BoolVal(true)
	host.vals["k"] = ir.IntVal(2)
	src := `
PROGRAM p
VAR_GLOBAL
    g : BOOL;
    k : INT;
    selOut : INT;
    muxOut : INT;
END_VAR
selOut := SEL(g, 10, 20);
muxOut := MUX(k, 5, 6, 7, 8);
END_PROGRAM`
	compileAndRun(t, src, host)
	if got := host.vals["selOut"].I; got != 20 {
		t.Errorf("SEL(TRUE, 10, 20) = %d, want 20", got)
	}
	if got := host.vals["muxOut"].I; got != 7 {
		t.Errorf("MUX(2, 5,6,7,8) = %d, want 7", got)
	}
}

func TestBuiltinMuxOutOfRangeFaults(t *testing.T) {
	host := newFakeHost()
	host.vals["k"] = ir.IntVal(9)
	src := `
PROGRAM p
VAR_GLOBAL
    k : INT;
    o : INT;
END_VAR
o := MUX(k, 1, 2);
END_PROGRAM`
	prog, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	irProg, err := Lower(prog)
	if err != nil {
		t.Fatal(err)
	}
	if err := ir.Run(irProg, ir.NewFrame(irProg), host); err == nil {
		t.Error("MUX with K out of range must fault the scan")
	}
}

func TestBuiltinBitOps(t *testing.T) {
	host := newFakeHost()
	host.vals["w"] = ir.IntVal(0b1011)
	src := `
PROGRAM p
VAR_GLOBAL
    w : INT;
    shl : INT;
    shr : INT;
    rol : INT;
    ror : INT;
END_VAR
shl := SHL(w, 2);
shr := SHR(w, 1);
rol := ROL(w, 4);
ror := ROR(ROL(w, 4), 4);
END_PROGRAM`
	compileAndRun(t, src, host)
	if got := host.vals["shl"].I; got != 0b101100 {
		t.Errorf("SHL = %b, want 101100", got)
	}
	if got := host.vals["shr"].I; got != 0b101 {
		t.Errorf("SHR = %b, want 101", got)
	}
	if got := host.vals["rol"].I; got != 0b10110000 {
		t.Errorf("ROL = %b, want 10110000", got)
	}
	// ROR undoes ROL over the full 64-bit width.
	if got := host.vals["ror"].I; got != 0b1011 {
		t.Errorf("ROR(ROL(w,4),4) = %b, want 1011", got)
	}
}

func TestBuiltinNumerics(t *testing.T) {
	host := newFakeHost()
	src := `
PROGRAM p
VAR_GLOBAL
    pw : REAL;
    tr : INT;
    at : REAL;
END_VAR
pw := EXPT(2.0, 10.0);
tr := TRUNC(-3.7);
at := ATAN2(1.0, 1.0);
END_PROGRAM`
	compileAndRun(t, src, host)
	if got := host.vals["pw"].F; got != 1024 {
		t.Errorf("EXPT(2,10) = %v, want 1024", got)
	}
	if got := host.vals["tr"].I; got != -3 {
		t.Errorf("TRUNC(-3.7) = %d, want -3 (toward zero)", got)
	}
	if got := host.vals["at"].F; math.Abs(got-math.Pi/4) > 1e-12 {
		t.Errorf("ATAN2(1,1) = %v, want pi/4", got)
	}
}

func TestBuiltinStrings(t *testing.T) {
	host := newFakeHost()
	host.vals["s"] = ir.StringVal("ASTONISHMENT")
	src := `
PROGRAM p
VAR_GLOBAL
    s : STRING;
    n : INT;
    l : STRING;
    r : STRING;
    m : STRING;
    c : STRING;
    ins : STRING;
    del : STRING;
    rep : STRING;
    f : INT;
    nf : INT;
END_VAR
n := LEN(s);
l := LEFT(s, 4);
r := RIGHT(s, 4);
m := MID(s, 5, 3);
c := CONCAT('AB', 'CD', 'EF');
ins := INSERT('ABCD', 'XY', 2);
del := DELETE('ABXYCD', 2, 3);
rep := REPLACE('ABCDE', 'X', 2, 2);
f := FIND('ABCBC', 'BC');
nf := FIND('ABC', 'Q');
END_PROGRAM`
	compileAndRun(t, src, host)
	want := map[string]string{
		"l": "ASTO", "r": "MENT", "m": "TONIS", "c": "ABCDEF",
		"ins": "ABXYCD", "del": "ABCD", "rep": "AXDE",
	}
	for name, w := range want {
		if got := host.vals[name].S; got != w {
			t.Errorf("%s = %q, want %q", name, got, w)
		}
	}
	if got := host.vals["n"].I; got != 12 {
		t.Errorf("LEN = %d, want 12", got)
	}
	if got := host.vals["f"].I; got != 2 {
		t.Errorf("FIND = %d, want 2 (1-based)", got)
	}
	if got := host.vals["nf"].I; got != 0 {
		t.Errorf("FIND miss = %d, want 0", got)
	}
	// Out-of-range positions clamp instead of faulting.
	host2 := newFakeHost()
	src2 := `
PROGRAM p
VAR_GLOBAL
    a : STRING;
    b : STRING;
END_VAR
a := LEFT('AB', 99);
b := MID('AB', 5, 99);
END_PROGRAM`
	compileAndRun(t, src2, host2)
	if got := host2.vals["a"].S; got != "AB" {
		t.Errorf("LEFT clamp = %q, want AB", got)
	}
	if got := host2.vals["b"].S; got != "" {
		t.Errorf("MID clamp = %q, want empty", got)
	}
}

func TestBuiltinStringConversions(t *testing.T) {
	host := newFakeHost()
	src := `
PROGRAM p
VAR_GLOBAL
    si : STRING;
    sr : STRING;
    sb : STRING;
    pi : INT;
    pr : REAL;
    pb : BOOL;
    br : REAL;
END_VAR
si := INT_TO_STRING(42);
sr := REAL_TO_STRING(2.5);
sb := BOOL_TO_STRING(TRUE);
pi := STRING_TO_INT(' 42 ');
pr := STRING_TO_REAL('2.5');
pb := STRING_TO_BOOL('true');
br := BOOL_TO_REAL(TRUE);
END_PROGRAM`
	compileAndRun(t, src, host)
	if got := host.vals["si"].S; got != "42" {
		t.Errorf("INT_TO_STRING = %q", got)
	}
	if got := host.vals["sr"].S; got != "2.5" {
		t.Errorf("REAL_TO_STRING = %q", got)
	}
	if got := host.vals["sb"].S; got != "TRUE" {
		t.Errorf("BOOL_TO_STRING = %q", got)
	}
	if got := host.vals["pi"].I; got != 42 {
		t.Errorf("STRING_TO_INT = %d", got)
	}
	if got := host.vals["pr"].F; got != 2.5 {
		t.Errorf("STRING_TO_REAL = %v", got)
	}
	if got := host.vals["pb"].B; got != true {
		t.Errorf("STRING_TO_BOOL = %v", got)
	}
	if got := host.vals["br"].F; got != 1.0 {
		t.Errorf("BOOL_TO_REAL = %v", got)
	}
	// A garbage parse faults the scan — bad data must not become zero.
	prog, _ := Parse(`
PROGRAM p
VAR_GLOBAL
    o : INT;
END_VAR
o := STRING_TO_INT('nope');
END_PROGRAM`)
	irProg, err := Lower(prog)
	if err != nil {
		t.Fatal(err)
	}
	if err := ir.Run(irProg, ir.NewFrame(irProg), newFakeHost()); err == nil {
		t.Error("STRING_TO_INT('nope') must fault")
	}
}

// REAL_TO_INT and REAL_TO_TIME round to nearest, ties to even (IEC 60559).
func TestBuiltinRealRoundingTiesToEven(t *testing.T) {
	cases := []struct {
		in   float64
		want int64
	}{
		{2.5, 2}, {3.5, 4}, {-2.5, -2}, {0.5, 0}, {1.4999, 1}, {1.5, 2}, {-0.5, 0}, {-3.5, -4},
	}
	src := `
PROGRAM p
VAR_GLOBAL
    x : REAL;
    asInt : INT;
    asTime : TIME;
END_VAR
asInt := REAL_TO_INT(x);
asTime := REAL_TO_TIME(x);
END_PROGRAM`
	for _, c := range cases {
		host := newFakeHost()
		host.vals["x"] = ir.RealVal(c.in)
		compileAndRun(t, src, host)
		if got := host.vals["asInt"].I; got != c.want {
			t.Errorf("REAL_TO_INT(%v) = %d, want %d", c.in, got, c.want)
		}
		if got := host.vals["asTime"].I; got != c.want {
			t.Errorf("REAL_TO_TIME(%v) = %d ms, want %d", c.in, got, c.want)
		}
	}
}

func TestBuiltinTruncStaysTruncation(t *testing.T) {
	host := newFakeHost()
	host.vals["x"] = ir.RealVal(2.9)
	compileAndRun(t, `
PROGRAM p
VAR_GLOBAL
    x : REAL;
    t : INT;
END_VAR
t := TRUNC(x);
END_PROGRAM`, host)
	if got := host.vals["t"].I; got != 2 {
		t.Errorf("TRUNC(2.9) = %d, want 2", got)
	}
}
