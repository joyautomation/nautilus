package fbd

import (
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/lang/ir"
)

// run compiles FBD source and executes one scan against a tag host, returning
// the host so the test can assert output values.
func run(t *testing.T, src string, seed map[string]ir.Value) *host {
	t.Helper()
	prog, err := Compile(src)
	if err != nil {
		t.Fatalf("compile: %v\n--- transpiled ---\n%s", err, mustTranspile(src))
	}
	h := &host{vals: map[string]ir.Value{}}
	for k, v := range seed {
		h.vals[k] = v
	}
	frame := ir.NewFrame(prog)
	if err := ir.Run(prog, frame, h); err != nil {
		t.Fatalf("run: %v", err)
	}
	return h
}

func mustTranspile(src string) string {
	s, err := Transpile(src)
	if err != nil {
		return "TRANSPILE ERROR: " + err.Error()
	}
	return s
}

// host is a minimal ir.Host backed by a map.
type host struct{ vals map[string]ir.Value }

func (h *host) ReadGlobal(name string) (ir.Value, error) {
	if v, ok := h.vals[name]; ok {
		return v, nil
	}
	return ir.Value{}, nil // undefined reads as zero, like a fresh tag
}
func (h *host) WriteGlobal(name string, v ir.Value) error { h.vals[name] = v; return nil }
func (h *host) NowMs() int64                              { return 0 }

func TestSealInLatch(t *testing.T) {
	// Classic seal-in: Run = (Start OR Run) AND NOT Stop. The feedback is
	// through the Run variable, which the netlist reads before it writes.
	src := `PROGRAM Latch
VAR_EXTERNAL
  Start : BOOL; Stop : BOOL; Run : BOOL;
END_VAR
FBD
  seal  = OR(Start, Run)
  Run  := AND(seal, NOT Stop)
END_FBD
END_PROGRAM`

	// Start pressed → latches on.
	h := run(t, src, map[string]ir.Value{"Start": ir.BoolVal(true)})
	if !h.vals["Run"].B {
		t.Fatal("Start should latch Run on")
	}
	// Start released but Run already on → stays on (seal-in).
	h = run(t, src, map[string]ir.Value{"Start": ir.BoolVal(false), "Run": ir.BoolVal(true)})
	if !h.vals["Run"].B {
		t.Fatal("Run should seal in when Start released")
	}
	// Stop pressed → drops out.
	h = run(t, src, map[string]ir.Value{"Run": ir.BoolVal(true), "Stop": ir.BoolVal(true)})
	if h.vals["Run"].B {
		t.Fatal("Stop should drop Run")
	}
}

func TestArithmeticAndFanout(t *testing.T) {
	// A wire feeding two coils (fan-out) plus an arithmetic block.
	src := `PROGRAM Calc
VAR_EXTERNAL
  A : REAL; B : REAL; Sum : REAL; Doubled : REAL; Big : BOOL;
END_VAR
FBD
  s = ADD(A, B)
  Sum := s
  Doubled := MUL(s, 2.0)
  Big := GT(s, 100.0)
END_FBD
END_PROGRAM`
	h := run(t, src, map[string]ir.Value{"A": ir.RealVal(60), "B": ir.RealVal(50)})
	if h.vals["Sum"].F != 110 {
		t.Errorf("Sum = %v, want 110", h.vals["Sum"].F)
	}
	if h.vals["Doubled"].F != 220 {
		t.Errorf("Doubled = %v, want 220", h.vals["Doubled"].F)
	}
	if !h.vals["Big"].B {
		t.Errorf("Big = %v, want true (110 > 100)", h.vals["Big"].B)
	}
}

func TestFunctionBlockInstance(t *testing.T) {
	// A TON instance whose output pin feeds a coil; ordering must place the
	// call before the read.
	src := `PROGRAM Timed
VAR_EXTERNAL
  Run : BOOL; Elapsed : BOOL;
END_VAR
FBD
  Elapsed := t1.Q
  t1 : TON(IN := Run, PT := T#5S)
END_FBD
END_PROGRAM`
	// Just compile + one scan (timing needs NowMs; we assert it runs and the
	// call was ordered before the pin read).
	st := mustTranspile(src)
	if strings.Index(st, "t1(IN") > strings.Index(st, "Elapsed := t1.Q") {
		t.Fatalf("FB call must be ordered before its pin read:\n%s", st)
	}
	if !strings.Contains(st, "VAR\n  t1 : TON;") {
		t.Fatalf("FB instance not declared:\n%s", st)
	}
	run(t, src, map[string]ir.Value{"Run": ir.BoolVal(false)})
}

func TestTranspileLineMap(t *testing.T) {
	// Each transpiled line must map back to the .fbd line it came from:
	// header verbatim, FB decls and statements to their netlist lines.
	src := `PROGRAM Timed
VAR_EXTERNAL
  Run : BOOL; Elapsed : BOOL;
END_VAR
FBD
  Elapsed := t1.Q
  t1 : TON(IN := Run, PT := T#5S)
END_FBD
END_PROGRAM`
	stSrc, lineMap, err := TranspileWithLines(src)
	if err != nil {
		t.Fatalf("TranspileWithLines: %v", err)
	}
	if got, want := stSrc, mustTranspile(src); got != want {
		t.Fatalf("TranspileWithLines output differs from Transpile:\n%s\n---\n%s", got, want)
	}
	stLines := strings.Split(stSrc, "\n")
	if len(lineMap) != len(stLines) {
		t.Fatalf("lineMap has %d entries for %d lines", len(lineMap), len(stLines))
	}
	find := func(prefix string) int {
		for i, l := range stLines {
			if strings.HasPrefix(strings.TrimSpace(l), prefix) {
				return i
			}
		}
		t.Fatalf("no transpiled line starts with %q:\n%s", prefix, stSrc)
		return -1
	}
	// Header maps 1:1; the t1 decl and call map to .fbd line 7; the coil to 6.
	for i, want := range map[int]int{
		0:                     1, // PROGRAM Timed
		2:                     3, // Run : BOOL; ...
		find("t1 : TON;"):     7,
		find("t1(IN"):         7,
		find("Elapsed := t1"): 6,
		find("END_PROGRAM"):   9,
	} {
		if lineMap[i] != want {
			t.Errorf("lineMap[%d] (%q) = %d, want %d", i, stLines[i], lineMap[i], want)
		}
	}
}

func TestCombinationalLoopRejected(t *testing.T) {
	src := `PROGRAM Loop
VAR_EXTERNAL X : BOOL; END_VAR
FBD
  a = AND(b, X)
  b = OR(a, X)
  X := a
END_FBD
END_PROGRAM`
	if _, err := Compile(src); err == nil || !strings.Contains(err.Error(), "combinational loop") {
		t.Fatalf("expected combinational-loop error, got %v", err)
	}
}

func TestSharedTypes(t *testing.T) {
	// An FBD POU using a UDT — proves it lowers through the same type system.
	src := `TYPE
  Motor_Type : STRUCT
    Speed : REAL;
    Run : BOOL;
  END_STRUCT;
END_TYPE
PROGRAM UsesUDT
VAR_EXTERNAL
  M : Motor_Type; FastOut : BOOL;
END_VAR
FBD
  FastOut := GT(M.Speed, 50.0)
END_FBD
END_PROGRAM`
	h := run(t, src, map[string]ir.Value{
		"M": {Kind: ir.TypeStruct, Fld: []ir.Value{ir.RealVal(75), ir.BoolVal(true)}},
	})
	if !h.vals["FastOut"].B {
		t.Errorf("FastOut = %v, want true", h.vals["FastOut"].B)
	}
}

// `pin => target` output bindings (IEC formal-call outputs) pass through
// the netlist verbatim — the one way to capture ET/CV into a variable at
// the call site (coils are the BOOL path).
func TestOutputBinding(t *testing.T) {
	src := `PROGRAM p
VAR_EXTERNAL Run : BOOL; Done : BOOL; Elapsed : TIME; END_VAR
FBD
  t1 : TON(IN := Run, PT := T#5S, ET => Elapsed)
  Done := t1.Q
END_FBD
END_PROGRAM`
	out, err := Transpile(src)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "ET => Elapsed") {
		t.Fatalf("binding must emit verbatim:\n%s", out)
	}
	if _, err := Compile(src); err != nil {
		t.Fatalf("does not compile: %v", err)
	}
	m, err := Graph(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range m.Nodes {
		if n.Kind == "fb" && n.Label == "t1" {
			has := false
			for _, o := range n.Outputs {
				if o == "ET" {
					has = true
				}
			}
			if !has {
				t.Fatalf("ET must render as an output pin: %+v", n)
			}
		}
	}
	// A non-variable target is rejected at parse.
	bad := `PROGRAM p
VAR_EXTERNAL Run : BOOL; END_VAR
FBD
  t1 : TON(IN := Run, ET => 5)
END_FBD
END_PROGRAM`
	if _, err := Transpile(bad); err == nil {
		t.Fatal("literal binding target must be rejected")
	}
}

// A file may hold several netlist blocks, one per POU — how a
// FUNCTION_BLOCK carries an FBD (or, through the LD hop, a ladder) body
// alongside the file's PROGRAM. Each block becomes its own POU's
// statements; everything between them passes through as ST verbatim.
func TestTranspileMultipleBlocks(t *testing.T) {
	src := `FUNCTION_BLOCK Scale
VAR_INPUT  IN : REAL; END_VAR
VAR_OUTPUT OUT : REAL; END_VAR
FBD
  OUT := MUL(IN, 2.0)
END_FBD
END_FUNCTION_BLOCK

PROGRAM Main
VAR_EXTERNAL Raw : REAL; Eng : REAL; END_VAR
FBD
  s1 : Scale(IN := Raw)
  Eng := s1.OUT
END_FBD
END_PROGRAM
`
	out, lineMap, err := TranspileWithLines(src)
	if err != nil {
		t.Fatalf("transpile: %v", err)
	}
	for _, want := range []string{
		"FUNCTION_BLOCK Scale",
		"OUT := (IN * 2.0);",
		"END_FUNCTION_BLOCK",
		"PROGRAM Main",
		"  s1 : Scale;",
		"s1(IN := Raw);",
		"Eng := s1.OUT;",
		"END_PROGRAM",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if n := strings.Count(out, "FBD"); n != 0 {
		t.Errorf("no FBD keyword should survive, found %d:\n%s", n, out)
	}
	if got, want := len(lineMap), len(strings.Split(out, "\n")); got != want {
		t.Fatalf("lineMap has %d entries for %d lines", got, want)
	}
	// The second POU's statement maps back to ITS netlist line, not the
	// first block's — the map composes across POUs.
	srcLines := strings.Split(src, "\n")
	for i, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "Eng := s1.OUT;") {
			if !strings.Contains(srcLines[lineMap[i]-1], "Eng := s1.OUT") {
				t.Fatalf("maps to source line %d (%q)", lineMap[i], srcLines[lineMap[i]-1])
			}
			return
		}
	}
	t.Fatal("statement not found")
}

func TestSplitBlocksRejectsMalformed(t *testing.T) {
	cases := []struct{ name, src, want string }{
		{"unclosed", "PROGRAM p\nFBD\n  a := b\nEND_PROGRAM\n", "no END_FBD"},
		{"orphan end", "PROGRAM p\nEND_FBD\nEND_PROGRAM\n", "END_FBD with no FBD"},
		{"nested", "PROGRAM p\nFBD\nFBD\nEND_FBD\nEND_PROGRAM\n", "inside an FBD block"},
		{"none", "PROGRAM p\nEND_PROGRAM\n", "must contain an FBD"},
	}
	for _, tc := range cases {
		_, err := Transpile(tc.src)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want containing %q", tc.name, err, tc.want)
		}
	}
}

// An instance the POU's header already declares is CALLED by the netlist,
// not re-declared — writing both reads naturally and must not collide.
func TestInstanceDeclaredInHeaderIsNotRedeclared(t *testing.T) {
	src := `PROGRAM p
VAR_EXTERNAL Run : BOOL; Done : BOOL; END_VAR
VAR t1 : TON; END_VAR
FBD
  t1 : TON(IN := Run, PT := T#5S)
  Done := t1.Q
END_FBD
END_PROGRAM
`
	out, err := Transpile(src)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(out, "t1 : TON"); n != 1 {
		t.Fatalf("t1 declared %d times, want 1:\n%s", n, out)
	}
	if _, err := Compile(src); err != nil {
		t.Fatalf("compile: %v\n%s", err, out)
	}
}
