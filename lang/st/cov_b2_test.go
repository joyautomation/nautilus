package st

import (
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/lang/ir"
)

// Coverage tests for docs/functions.md claims (types, user blocks,
// VAR_IN_OUT, bit access). Each names the page promise it pins.

// b2Run lowers src and runs it n scans against a fresh stub host seeded
// with seed, returning the host.
func b2Run(t *testing.T, src string, n int, seed map[string]ir.Value) *stubHost {
	t.Helper()
	h, _, _ := scanN(t, src, n, seed)
	return h
}

// b2LowerErr lowers src and returns the error text ("" when it compiles).
func b2LowerErr(t *testing.T, src string) string {
	t.Helper()
	ast, err := Parse(src)
	if err != nil {
		return err.Error()
	}
	if _, err := Lower(ast); err != nil {
		return err.Error()
	}
	return ""
}

// "Mixed numeric arguments promote to REAL; comparing or combining a STRING
// with a number is an error, not a coercion." (functions.md, Types)
func TestMixedNumericArgsPromoteStringNumberRefused(t *testing.T) {
	// A standard function's mixed INT/REAL arguments promote to REAL: MAX of
	// INT 2 and REAL 2.5 is 2.5 (an INT result would be 2 or 3), and so is
	// the three-input MAX of INT 2, 1 and 2.5.
	h := b2Run(t, `
PROGRAM P
VAR_EXTERNAL m : REAL; s : REAL; END_VAR
VAR i : INT := 2; END_VAR
m := MAX(i, 2.5);
s := MAX(i, 1, 2.5);
END_PROGRAM`, 1, map[string]ir.Value{"m": ir.RealVal(0), "s": ir.RealVal(0)})
	if got := h.globals["m"]; got.Kind != ir.TypeReal || got.F != 2.5 {
		t.Errorf("MAX(INT 2, 2.5) = %+v, want REAL 2.5", got)
	}
	if got := h.globals["s"]; got.Kind != ir.TypeReal || got.F != 2.5 {
		t.Errorf("MAX(INT 2, 1, 2.5) = %+v, want REAL 2.5", got)
	}

	// The promoted result is a REAL: both calls lower to REAL-typed values,
	// and assigning one to an INT is refused (no implicit narrowing), while
	// the all-INT MAX(i, 2) stays an INT.
	prog := lowerSource(t, `
PROGRAM P
VAR i : INT := 2; m : REAL; s : REAL; END_VAR
m := MAX(i, 2.5);
s := MAX(i, 1, 2.5);
END_PROGRAM`)
	for n, st := range prog.Body {
		as := st.(*ir.Assign)
		if k := as.Value.ExprType().Kind; k != ir.TypeReal {
			t.Errorf("statement %d: mixed MAX lowered to %v, want a REAL result", n+1, as.Value.ExprType())
		}
	}
	if got := b2LowerErr(t, "PROGRAM P\nVAR i : INT := 2; n : INT; END_VAR\nn := MAX(i, 2.5);\nEND_PROGRAM"); got == "" {
		t.Errorf("n : INT := MAX(INT, REAL) compiled; the promoted REAL result must not narrow silently")
	}
	if got := b2LowerErr(t, "PROGRAM P\nVAR i : INT := 2; n : INT; END_VAR\nn := MAX(i, 2);\nEND_PROGRAM"); got != "" {
		t.Errorf("n := MAX(INT, INT) refused: %s", got)
	}

	for _, expr := range []string{
		"b := str = 1;",
		"b := str > 1.5;",
		"b := 1 <> str;",
		"n := str + 1;",
		"n := 1 + str;",
		"n := LIMIT(0, str, 9);",
		"n := MAX(str, 1);",
	} {
		t.Run(expr, func(t *testing.T) {
			src := `
PROGRAM P
VAR str : STRING := '1'; b : BOOL; n : INT; END_VAR
` + expr + `
END_PROGRAM`
			if got := b2LowerErr(t, src); got == "" {
				t.Fatalf("%s compiled; a STRING with a number must be a compile error", expr)
			}
		})
	}
}

// "A UDT can be ... a FUNCTION argument or return" and "a FUNCTION's inputs,
// locals, and return type ... resolve against the whole compile" (Types;
// A pin may be any type). The TYPE sits in a library joined ahead of the
// FUNCTION, as a project's lib/types.st would. A block VAR of a user TYPE
// keeps its value across scans like any local.
func TestUserTypeAsFunctionArgLocalReturnAndBlockVar(t *testing.T) {
	lib := `
TYPE
  Range : STRUCT LO : REAL; HI : REAL; END_STRUCT;
  Span  : STRUCT R : Range; COUNT : INT; END_STRUCT;
END_TYPE
`
	src := lib + `
FUNCTION Widen : Span
VAR_INPUT IN : Span; AMT : REAL; END_VAR
VAR tmp : Span; END_VAR
  IN.R.LO := IN.R.LO - AMT;      (* the input is the function's own copy *)
  IN.R.HI := IN.R.HI + AMT;
  tmp := IN;
  tmp.COUNT := tmp.COUNT + 1;
  Widen := tmp;
END_FUNCTION

FUNCTION_BLOCK FB_Track
VAR_INPUT  X : REAL; END_VAR
VAR_OUTPUT LO : REAL; HI : REAL; END_VAR
VAR seen : Span; first : BOOL := TRUE; END_VAR
  IF first THEN
    seen.R.LO := X; seen.R.HI := X; first := FALSE;
  END_IF;
  IF X < seen.R.LO THEN seen.R.LO := X; END_IF;
  IF X > seen.R.HI THEN seen.R.HI := X; END_IF;
  seen.COUNT := seen.COUNT + 1;
  LO := seen.R.LO; HI := seen.R.HI;
END_FUNCTION_BLOCK

PROGRAM P
VAR_EXTERNAL lo : REAL; hi : REAL; cnt : INT; srcLo : REAL;
             trLo : REAL; trHi : REAL; x : REAL; END_VAR
VAR a : Span; b : Span; tr : FB_Track; END_VAR
a.R.LO := 10.0; a.R.HI := 20.0; a.COUNT := 0;
b := Widen(IN := a, AMT := 2.5);
lo := b.R.LO; hi := b.R.HI; cnt := b.COUNT;
srcLo := a.R.LO;
tr(X := x);
trLo := tr.LO; trHi := tr.HI;
END_PROGRAM`
	prog := lowerSource(t, src)
	h := newStubHost()
	for _, k := range []string{"lo", "hi", "srcLo", "trLo", "trHi"} {
		h.globals[k] = ir.RealVal(0)
	}
	h.globals["cnt"] = ir.IntVal(0)
	frame := ir.NewFrame(prog)
	for i, x := range []float64{5, 9, 7} {
		h.globals["x"] = ir.RealVal(x)
		if err := ir.Run(prog, frame, h); err != nil {
			t.Fatalf("scan %d: %v", i+1, err)
		}
	}
	if h.globals["lo"].F != 7.5 || h.globals["hi"].F != 22.5 || h.globals["cnt"].I != 1 {
		t.Errorf("Widen returned LO=%v HI=%v COUNT=%v, want 7.5 22.5 1",
			h.globals["lo"].F, h.globals["hi"].F, h.globals["cnt"].I)
	}
	if h.globals["srcLo"].F != 10 {
		t.Errorf("the argument struct changed: a.R.LO = %v, want 10 (passed by value)", h.globals["srcLo"].F)
	}
	// The block's Span VAR carried its min/max across the three scans.
	if h.globals["trLo"].F != 5 || h.globals["trHi"].F != 9 {
		t.Errorf("FB VAR of a user TYPE: LO=%v HI=%v, want 5 and 9 (retained across scans)",
			h.globals["trLo"].F, h.globals["trHi"].F)
	}
}

// "Assigning a struct or an array copies it." (Types) The struct half is
// TestStructAssignmentIsACopy; this is the array half, for a local array and
// an array inside a struct.
func TestArrayAssignmentIsACopy(t *testing.T) {
	h := b2Run(t, `
TYPE Box : STRUCT V : ARRAY[0..2] OF INT; END_STRUCT; END_TYPE
PROGRAM P
VAR_EXTERNAL a1 : INT; b1 : INT; c1 : INT; d1 : INT; END_VAR
VAR a : ARRAY[0..2] OF INT; b : ARRAY[0..2] OF INT; c : Box; d : Box; END_VAR
a[1] := 5;
b := a;
b[1] := 99;
a1 := a[1]; b1 := b[1];
c.V[1] := 7;
d := c;
d.V[1] := 42;
c1 := c.V[1]; d1 := d.V[1];
END_PROGRAM`, 1, map[string]ir.Value{"a1": ir.IntVal(0), "b1": ir.IntVal(0), "c1": ir.IntVal(0), "d1": ir.IntVal(0)})
	if h.globals["a1"].I != 5 || h.globals["b1"].I != 99 {
		t.Errorf("array assign: a[1]=%d b[1]=%d, want 5 and 99 (b := a copies)", h.globals["a1"].I, h.globals["b1"].I)
	}
	if h.globals["c1"].I != 7 || h.globals["d1"].I != 42 {
		t.Errorf("struct with array: c.V[1]=%d d.V[1]=%d, want 7 and 42", h.globals["c1"].I, h.globals["d1"].I)
	}
}

// "VAR CONSTANT reads fold at compile time; assigning one (or a field,
// element or bit of one, or using it as a FOR variable) is a compile error."
func TestConstantFoldsAndPartsCannotBeWritten(t *testing.T) {
	// Folding: the read of K lowers to a literal, not a slot read.
	prog := lowerSource(t, `
PROGRAM P
VAR CONSTANT K : INT := 3 * 4; END_VAR
VAR x : INT; END_VAR
x := K;
END_PROGRAM`)
	if len(prog.Body) != 1 {
		t.Fatalf("body has %d statements, want 1", len(prog.Body))
	}
	as, ok := prog.Body[0].(*ir.Assign)
	if !ok {
		t.Fatalf("statement is %T, want *ir.Assign", prog.Body[0])
	}
	lit, ok := as.Value.(*ir.Lit)
	if !ok {
		t.Fatalf("x := K lowered its value to %T, want a folded *ir.Lit", as.Value)
	}
	if lit.V.I != 12 {
		t.Errorf("K folded to %d, want 12", lit.V.I)
	}

	decl := `
TYPE Pair : STRUCT A : INT; B : INT; END_STRUCT; END_TYPE
PROGRAM P
VAR CONSTANT
  W : WORD := 16#00F0;
  T : ARRAY[0..2] OF INT;
  S : Pair;
END_VAR
VAR i : INT; END_VAR
`
	for _, c := range []struct{ name, stmt string }{
		{"whole", "W := 1;"},
		{"field", "S.A := 5;"},
		{"element", "T[1] := 9;"},
		{"bit", "W.3 := TRUE;"},
		{"IEC bit", "W.%X3 := TRUE;"},
		{"partial", "W.%B0 := 16#FF;"},
		{"FOR variable", "FOR W := 1 TO 2 DO i := i + 1; END_FOR;"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := b2LowerErr(t, decl+c.stmt+"\nEND_PROGRAM")
			if !strings.Contains(got, "is a constant") {
				t.Fatalf("%s: err = %q, want the \"is a constant\" compile error", c.stmt, got)
			}
		})
	}
}

// "Word.3 is bit 3 of an integer as a BOOL, readable anywhere a BOOL is (IF,
// ..., a compare argument) and assignable ..., numbered from 0 at the least
// significant end; ... bits of array elements and structure members."
func TestBitAccessInIfCompareArrayAndStruct(t *testing.T) {
	src := `
TYPE Motor : STRUCT Status : WORD; Cmd : WORD; END_STRUCT; END_TYPE
PROGRAM P
VAR_EXTERNAL
  Status : WORD; Words : ARRAY[0..3] OF WORD; P101 : Motor;
  inIf : BOOL; cmp : BOOL; cmpFn : BOOL; arrBit : BOOL; arrBitX : BOOL;
  stBit : BOOL; stBitX : BOOL;
END_VAR
inIf := FALSE;
IF Status.0 THEN inIf := TRUE; END_IF;
cmp := Status.3 = TRUE;
cmpFn := Status.%X3 = Status.0;
arrBit := Words[2].15;
arrBitX := Words[2].%X0;
stBit := P101.Status.12;
stBitX := P101.Status.%X1;
Words[1].4 := TRUE;
Words[1].%X0 := FALSE;
P101.Cmd.12 := TRUE;
P101.Cmd.%X0 := FALSE;
END_PROGRAM`
	prog := lowerSource(t, src)
	h := newStubHost()
	words := ir.Zero(prog.Globals["Words"])
	words.Arr[2] = ir.IntVal(0x8000) // bit 15 only
	words.Arr[1] = ir.IntVal(0x0101) // bits 0 and 8
	motor := ir.Zero(prog.Types["Motor"])
	motor.Fld[0] = ir.IntVal(0x1000) // Status bit 12 only
	motor.Fld[1] = ir.IntVal(0x0003) // Cmd bits 0 and 1
	h.globals["Words"] = words
	h.globals["P101"] = motor
	h.globals["Status"] = ir.IntVal(0x0009) // bits 0 and 3

	frame := ir.NewFrame(prog)
	if err := ir.Run(prog, frame, h); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"inIf": true, "cmp": true, "cmpFn": true,
		"arrBit": true, "arrBitX": false, "stBit": true, "stBitX": false,
	}
	for k, w := range want {
		if got := h.globals[k].B; got != w {
			t.Errorf("%s = %v, want %v", k, got, w)
		}
	}
	if got := h.globals["Words"].Arr[1].I; got != 0x0110 {
		t.Errorf("Words[1] = 16#%X after .4 := TRUE and .%%X0 := FALSE, want 16#110 (other bits kept)", got)
	}
	if got := h.globals["P101"].Fld[1].I; got != 0x1002 {
		t.Errorf("P101.Cmd = 16#%X, want 16#1002 (bit 12 set, bit 0 cleared, bit 1 kept)", got)
	}

	// The same reads with every bit flipped: Status bit 0 and 3 clear.
	h.globals["Status"] = ir.IntVal(0xFFF6)
	if err := ir.Run(prog, frame, h); err != nil {
		t.Fatal(err)
	}
	if h.globals["inIf"].B || h.globals["cmp"].B {
		t.Errorf("Status bits 0/3 clear: inIf=%v cmp=%v, want both FALSE", h.globals["inIf"].B, h.globals["cmp"].B)
	}
	if !h.globals["cmpFn"].B {
		t.Errorf("Status.%%X3 = Status.0 with both FALSE = FALSE, want TRUE")
	}
}

// Partial access "reads as an unsigned integer of that width and writes back
// only its own bits": %B, %W, %D with the part's top bit set read unsigned,
// and %L reads and writes the whole 64 bits.
func TestPartialAccessUnsignedAndL(t *testing.T) {
	src := `
PROGRAM P
VAR_EXTERNAL
  d : DINT; l : LINT; out : LINT;
  b3 : BYTE; w1 : WORD; d0 : DWORD; d1 : DWORD; l0 : LWORD; (* a part is a bit string (#782) *)
END_VAR
b3 := d.%B3;
w1 := d.%W1;
d0 := d.%D0;
d1 := l.%D1;
l0 := l.%L0;
out := 0;
out.%L0 := LINT_TO_LWORD(l);
out.%D1 := d.%D0;
END_PROGRAM`
	seed := map[string]ir.Value{
		"d": ir.IntVal(-2147481080),          // 16#8000_0A08: top bit of every part set
		"l": ir.IntVal(-8070450532247928577), // 16#9000_0000_0000_00FF
	}
	for _, k := range []string{"out", "b3", "w1", "d0", "d1", "l0"} {
		seed[k] = ir.IntVal(0)
	}
	h := b2Run(t, src, 1, seed)
	for k, w := range map[string]int64{
		"b3": 0x80, "w1": 0x8000, "d0": 0x80000A08, "d1": 0x90000000,
		"l0": -8070450532247928577,
		// %L0 wrote all of l, then %D1 replaced only the high 32 bits.
		"out": b2Bits(uint64(0x80000A08)<<32 | 0xFF),
	} {
		if got := h.globals[k].I; got != w {
			t.Errorf("%s = %d (16#%X), want %d (16#%X)", k, got, uint64(got), w, uint64(w))
		}
	}
}

func b2Bits(u uint64) int64 { return int64(u) }

// "A call may leave any VAR_INPUT out: it keeps its value, the declared
// initial value until something writes it ... EN, unbound, is TRUE."
func TestUnboundInputKeepsTheWrittenValue(t *testing.T) {
	src := `
FUNCTION_BLOCK Offset
VAR_INPUT X : INT; Bias : INT := 7; END_VAR
VAR_OUTPUT Y : INT; END_VAR
Y := X + Bias;
END_FUNCTION_BLOCK

PROGRAM Main
VAR_EXTERNAL phase : INT; out : INT; ok : BOOL; END_VAR
VAR o : Offset; END_VAR
IF phase = 0 THEN
  o(X := 1, ENO => ok);           (* Bias unbound: its initial 7; EN unbound: runs *)
ELSIF phase = 1 THEN
  o(X := 1, Bias := 100);         (* something writes Bias *)
ELSE
  o(X := 2);                      (* omitted again: keeps 100, not 7 *)
END_IF;
out := o.Y;
END_PROGRAM`
	prog := lowerSource(t, src)
	h := newStubHost()
	h.globals["out"] = ir.IntVal(0)
	h.globals["ok"] = ir.BoolVal(false)
	frame := ir.NewFrame(prog)
	for phase, want := range []int64{8, 101, 102, 102} {
		p := phase
		if p > 2 {
			p = 2
		}
		h.globals["phase"] = ir.IntVal(int64(p))
		if err := ir.Run(prog, frame, h); err != nil {
			t.Fatal(err)
		}
		if got := h.globals["out"].I; got != want {
			t.Fatalf("scan %d: Y = %d, want %d", phase+1, got, want)
		}
		if phase == 0 && !h.globals["ok"].B {
			t.Fatalf("ENO with EN unbound = FALSE, want TRUE (unbound EN is TRUE)")
		}
	}
}

// "The argument must be assignable: a variable, a struct field, an array
// element, or a VAR_EXTERNAL tag." Every accepted form compiles, and the
// block's write lands in the bound storage (a literal and a variable array
// index alike).
func TestVarInOutAcceptsEveryAssignableForm(t *testing.T) {
	src := `
TYPE Cnt : STRUCT N : INT; END_STRUCT; END_TYPE
FUNCTION_BLOCK FB_Bump
VAR_IN_OUT N : INT; END_VAR
  N := N + 1;
END_FUNCTION_BLOCK

PROGRAM main
VAR_EXTERNAL tag : INT; arr : ARRAY[0..3] OF INT; rec : Cnt; i : INT;
             loc : INT; fld : INT; END_VAR
VAR b : FB_Bump; v : INT := 10; s : Cnt; END_VAR
b(N := v);
b(N := s.N);
b(N := arr[1]);
b(N := arr[i]);
b(N := tag);
b(N := rec.N);
loc := v; fld := s.N;
END_PROGRAM`
	prog := lowerSource(t, src)
	h := newStubHost()
	h.globals["tag"] = ir.IntVal(40)
	h.globals["arr"] = ir.Zero(prog.Globals["arr"])
	h.globals["rec"] = ir.Zero(prog.Types["Cnt"])
	h.globals["i"] = ir.IntVal(3)
	h.globals["loc"] = ir.IntVal(0)
	h.globals["fld"] = ir.IntVal(0)
	frame := ir.NewFrame(prog)
	if err := ir.Run(prog, frame, h); err != nil {
		t.Fatal(err)
	}
	if got := h.globals["loc"].I; got != 11 {
		t.Errorf("variable: %d, want 11", got)
	}
	if got := h.globals["fld"].I; got != 1 {
		t.Errorf("struct field: %d, want 1", got)
	}
	if a := h.globals["arr"].Arr; a[1].I != 1 || a[3].I != 1 || a[0].I != 0 || a[2].I != 0 {
		t.Errorf("array elements: %v, want [0 1 0 1]", []int64{a[0].I, a[1].I, a[2].I, a[3].I})
	}
	if got := h.globals["tag"].I; got != 41 {
		t.Errorf("VAR_EXTERNAL tag: %d, want 41", got)
	}
	if got := h.globals["rec"].Fld[0].I; got != 1 {
		t.Errorf("VAR_EXTERNAL struct field: %d, want 1", got)
	}
}

// "Two calls in one scan bound to different variables each see and update
// their own." One instance called twice in a scan, and two instances.
func TestVarInOutTwoCallsSameScanEachOwnVariable(t *testing.T) {
	src := `
FUNCTION_BLOCK FB_Acc
VAR_IN_OUT N : INT; END_VAR
VAR_OUTPUT Seen : INT; END_VAR
  Seen := N;
  N := N + 100;
END_FUNCTION_BLOCK

PROGRAM main
VAR_EXTERNAL a : INT; c : INT; sa : INT; sc : INT;
             x : INT; y : INT; sx : INT; sy : INT; END_VAR
VAR b : FB_Acc; p : FB_Acc; q : FB_Acc; END_VAR
b(N := a); sa := b.Seen;
b(N := c); sc := b.Seen;
p(N := x); q(N := y);
sx := p.Seen; sy := q.Seen;
END_PROGRAM`
	prog := lowerSource(t, src)
	h := newStubHost()
	for k, v := range map[string]int64{"a": 1, "c": 2, "x": 3, "y": 4, "sa": 0, "sc": 0, "sx": 0, "sy": 0} {
		h.globals[k] = ir.IntVal(v)
	}
	frame := ir.NewFrame(prog)
	for scan := 0; scan < 2; scan++ {
		if err := ir.Run(prog, frame, h); err != nil {
			t.Fatal(err)
		}
		base := int64(scan) * 100
		want := map[string]int64{
			"sa": 1 + base, "sc": 2 + base, "a": 101 + base, "c": 102 + base,
			"sx": 3 + base, "sy": 4 + base, "x": 103 + base, "y": 104 + base,
		}
		for k, w := range want {
			if got := h.globals[k].I; got != w {
				t.Errorf("scan %d: %s = %d, want %d", scan+1, k, got, w)
			}
		}
	}
}

// "FUNCTIONs have no VAR_IN_OUT (or VAR_OUTPUT): an IEC function is a single
// return value, and the compiler says so."
func TestFunctionRefusesInOutAndOutput(t *testing.T) {
	for _, kind := range []string{"VAR_IN_OUT", "VAR_OUTPUT"} {
		t.Run(kind, func(t *testing.T) {
			src := `
FUNCTION F : INT
VAR_INPUT a : INT; END_VAR
` + kind + ` b : INT; END_VAR
  F := a;
END_FUNCTION

PROGRAM main
VAR x : INT; END_VAR
  x := F(1);
END_PROGRAM`
			got := b2LowerErr(t, src)
			if !strings.Contains(got, kind+" not allowed") || !strings.Contains(got, "single return value") {
				t.Fatalf("err = %q, want %s refused because a FUNCTION has a single return value", got, kind)
			}
		})
	}
}

// "Bound with :=, like an input — the => form binds outputs" (the
// VAR_IN_OUT rules table): => on a VAR_IN_OUT is a compile error that says
// to bind it with :=.
func TestVarInOutRefusesOutputBinding(t *testing.T) {
	fb := `
FUNCTION_BLOCK FB_Bump
VAR_IN_OUT N : INT; END_VAR
  N := N + 1;
END_FUNCTION_BLOCK

PROGRAM main
VAR b : FB_Bump; count : INT; other : INT; END_VAR
`
	// => alone leaves the IN_OUT unbound: refused, pointing at N := <variable>.
	got := b2LowerErr(t, fb+"  b(N => count);\nEND_PROGRAM")
	if !strings.Contains(got, "VAR_IN_OUT") || !strings.Contains(got, "N := <variable>") {
		t.Fatalf("b(N => count): err = %q, want a VAR_IN_OUT error pointing at N := <variable>", got)
	}
	// => beside a := binding is refused as a VAR_IN_OUT, not taken as an output.
	got = b2LowerErr(t, fb+"  b(N := count, N => other);\nEND_PROGRAM")
	if !strings.Contains(got, "is a VAR_IN_OUT") || !strings.Contains(got, `"N" := <variable>`) {
		t.Fatalf("b(N := count, N => other): err = %q, want the => refusal pointing at N := <variable>", got)
	}
}
