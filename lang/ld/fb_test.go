package ld

import (
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/lang/fbd"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/lang/st"
)

// FUNCTION_BLOCKs written in ladder — the IEC answer to a JSR. The whole
// point is that nothing downstream needs to know: the block transpiles to
// an ordinary ST FUNCTION_BLOCK, so ST, FBD and ladder all call it the
// same way, and the ST compiler type-checks every pin.

// host is the tag store the VM writes through, for the tests that RUN a
// block rather than just compile it.
type host struct {
	vals map[string]ir.Value
	now  int64
}

// The clock starts at a real epoch, never 0: a timer's "not started yet"
// sentinel IS zero, so a zero clock would keep restarting it.
func newHost() *host { return &host{vals: map[string]ir.Value{}, now: 1_700_000_000_000} }

func (h *host) ReadGlobal(name string) (ir.Value, error)  { return h.vals[name], nil }
func (h *host) WriteGlobal(name string, v ir.Value) error { h.vals[name] = v; return nil }
func (h *host) NowMs() int64                              { return h.now }

// toST runs the full ladder pipeline: LD → FBD netlist → ST.
func toST(t *testing.T, src string, libs ...string) string {
	t.Helper()
	fbdSrc, err := Transpile(src, libs...)
	if err != nil {
		t.Fatalf("ld transpile: %v", err)
	}
	stSrc, err := fbd.Transpile(fbdSrc)
	if err != nil {
		t.Fatalf("fbd transpile: %v\n%s", err, fbdSrc)
	}
	return stSrc
}

// compileST parses and lowers composed ST, failing the test on either.
func compileST(t *testing.T, src string) *ir.Program {
	t.Helper()
	ast, err := st.Parse(src)
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, src)
	}
	prog, err := st.Lower(ast)
	if err != nil {
		t.Fatalf("lower: %v\n%s", err, src)
	}
	return prog
}

// pumpSeq is the motivating block: a seal-in with a level permissive and a
// run-time timer, written as rungs, with pins instead of tags.
const pumpSeq = `FUNCTION_BLOCK PumpSeq
VAR_INPUT  Start : BOOL; Stop : BOOL; Level : REAL; END_VAR
VAR_OUTPUT Run : BOOL; END_VAR
VAR        t1 : TON; END_VAR
LD
  RUNG seal  [ Start | Run ] /Stop GT(Level, 10.0) ( Run )
  RUNG dly   Run t1:TON(PT := T#5S)
END_LD
END_FUNCTION_BLOCK`

// A .ld file may hold a FUNCTION_BLOCK, a PROGRAM, or both. The block's
// rungs become the block's body; the program's become the program's.
func TestLadderFunctionBlockLowers(t *testing.T) {
	src := pumpSeq + `

PROGRAM Main
VAR_EXTERNAL Cmd : BOOL; Halt : BOOL; Lvl : REAL; PumpRun : BOOL; END_VAR
VAR p1 : PumpSeq; END_VAR
LD
  RUNG call  Cmd p1:PumpSeq(Stop := Halt, Level := Lvl, Run => PumpRun)
END_LD
END_PROGRAM`

	got := toST(t, src)
	for _, want := range []string{
		"FUNCTION_BLOCK PumpSeq",
		"Run := ((Start OR Run) AND NOT (Stop) AND (Level > 10.0));",
		"t1(IN := Run, PT := T#5S);",
		"END_FUNCTION_BLOCK",
		// The rung's power lands on the block's FIRST unbound BOOL input,
		// and the output capture passes through verbatim.
		"p1(Start := Cmd, Stop := Halt, Level := Lvl, Run => PumpRun);",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	// The declared `t1 : TON` is NOT re-declared by the rung that calls it.
	if n := strings.Count(got, "t1 : TON"); n != 1 {
		t.Errorf("t1 declared %d times, want 1:\n%s", n, got)
	}
	compileST(t, got)
}

// The block RUNS: the seal-in latches, the level permissive drops it, and
// the retained TON inside the block keeps its own state per instance.
func TestLadderFunctionBlockRuns(t *testing.T) {
	src := pumpSeq + `

PROGRAM Main
VAR_EXTERNAL Cmd : BOOL; Halt : BOOL; Lvl : REAL; PumpRun : BOOL; Warm : BOOL; END_VAR
VAR p1 : PumpSeq; END_VAR
LD
  RUNG call  Cmd p1:PumpSeq(Stop := Halt, Level := Lvl, Run => PumpRun)
  RUNG warm  p1.t1.Q ( Warm )
END_LD
END_PROGRAM`

	prog := compileST(t, toST(t, src))
	h := newHost()
	frame := ir.NewFrame(prog)
	scan := func() {
		t.Helper()
		if err := ir.Run(prog, frame, h); err != nil {
			t.Fatalf("run: %v", err)
		}
	}

	h.vals["Lvl"] = ir.RealVal(50)
	h.vals["Cmd"] = ir.BoolVal(true)
	scan()
	if !h.vals["PumpRun"].B {
		t.Fatal("PumpRun should latch on the start command")
	}
	// The seal-in holds with the command released.
	h.vals["Cmd"] = ir.BoolVal(false)
	scan()
	if !h.vals["PumpRun"].B {
		t.Fatal("the seal-in should hold PumpRun")
	}
	// The level permissive is inside the block: drop it and the coil drops.
	h.vals["Lvl"] = ir.RealVal(5)
	scan()
	if h.vals["PumpRun"].B {
		t.Fatal("the level permissive should drop PumpRun")
	}
	// The block's own TON is retained per instance: five seconds of Run.
	h.vals["Lvl"] = ir.RealVal(50)
	h.vals["Cmd"] = ir.BoolVal(true)
	scan()
	if h.vals["Warm"].B {
		t.Fatal("the block's TON should not be done on the first scan")
	}
	h.now += 6000
	scan()
	if !h.vals["Warm"].B {
		t.Fatal("the block's TON should elapse — its state is retained per instance")
	}
}

// A ladder-written block is an ordinary FUNCTION_BLOCK, so an ST program
// calls it exactly like any other. This is the composition a project gets:
// blocks.ld transpiled into the prelude, main.st joined after it.
func TestLadderFunctionBlockFromST(t *testing.T) {
	lib := toST(t, pumpSeq)
	prog := compileST(t, lib+"\n"+`
PROGRAM Main
VAR_EXTERNAL Cmd : BOOL; Lvl : REAL; PumpRun : BOOL; END_VAR
VAR p1 : PumpSeq; END_VAR
  p1(Start := Cmd, Stop := FALSE, Level := Lvl);
  PumpRun := p1.Run;
END_PROGRAM`)

	h := newHost()
	h.vals["Lvl"] = ir.RealVal(50)
	h.vals["Cmd"] = ir.BoolVal(true)
	if err := ir.Run(prog, ir.NewFrame(prog), h); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !h.vals["PumpRun"].B {
		t.Fatal("an ST program calling the ladder block should see Run")
	}
}

// A VAR_IN_OUT struct pin through a ladder block: the block reads and
// writes the caller's UDT tag by reference, so one pin replaces a dozen.
func TestLadderFunctionBlockInOutStructPin(t *testing.T) {
	src := `TYPE
  Motor : STRUCT
    Cmd     : BOOL;
    Running : BOOL;
    Starts  : INT;
  END_STRUCT;
END_TYPE

FUNCTION_BLOCK FB_Starter
VAR_IN_OUT M : Motor; END_VAR
VAR_INPUT  Permit : BOOL; END_VAR
LD
  RUNG run    Permit M.Cmd ( M.Running )
  RUNG count  +M.Cmd cnt:CTU(PV := 32767, CV => M.Starts)
END_LD
END_FUNCTION_BLOCK

PROGRAM Main
VAR_EXTERNAL P101 : Motor; Permissive : BOOL; END_VAR
VAR s : FB_Starter; END_VAR
LD
  RUNG start  s:FB_Starter(M := P101, Permit := Permissive)
END_LD
END_PROGRAM`

	got := toST(t, src)
	// A VAR_IN_OUT is bound with `:=` like an input, and the rung's power
	// finds the block's first BOOL input — Permit is bound by name here,
	// so nothing is left for power, which the unconditioned rung allows.
	if !strings.Contains(got, "s(M := P101, Permit := Permissive);") {
		t.Fatalf("in-out binding wrong:\n%s", got)
	}
	prog := compileST(t, got)

	h := newHost()
	motor := ir.Zero(prog.Types["Motor"])
	motor.Fld[0] = ir.BoolVal(true) // Cmd
	h.vals["P101"] = motor
	h.vals["Permissive"] = ir.BoolVal(true)
	frame := ir.NewFrame(prog)
	if err := ir.Run(prog, frame, h); err != nil {
		t.Fatalf("run: %v", err)
	}
	// The block wrote back through the reference pin.
	if !h.vals["P101"].Fld[1].B {
		t.Fatalf("M.Running should be written back to the caller's tag: %+v", h.vals["P101"])
	}
	if got := h.vals["P101"].Fld[2].I; got != 1 {
		t.Fatalf("M.Starts = %d, want 1 (the edge counted through the reference pin)", got)
	}
}

// Blocks nest: a ladder block instantiates another ladder block, from a
// SEPARATE library file, resolved through the library sources.
func TestLadderFunctionBlockNested(t *testing.T) {
	inner := `FUNCTION_BLOCK Debounce
VAR_INPUT  IN : BOOL; END_VAR
VAR_OUTPUT OUT : BOOL; END_VAR
VAR        t : TON; END_VAR
LD
  RUNG hold  IN t:TON(PT := T#200MS) ( OUT )
END_LD
END_FUNCTION_BLOCK`

	outer := `FUNCTION_BLOCK Guarded
VAR_INPUT  Raw : BOOL; END_VAR
VAR_OUTPUT Clean : BOOL; END_VAR
LD
  RUNG deb  Raw d1:Debounce() ( Clean )
END_LD
END_FUNCTION_BLOCK`

	// The outer file resolves Debounce's power pins from the inner file.
	outerST := toST(t, outer, inner)
	if !strings.Contains(outerST, "d1(IN := Raw);") {
		t.Fatalf("nested call should place power on Debounce's IN pin:\n%s", outerST)
	}
	if !strings.Contains(outerST, "Clean := d1.OUT;") {
		t.Fatalf("power should continue from Debounce's OUT pin:\n%s", outerST)
	}

	prog := compileST(t, toST(t, inner)+"\n"+outerST+"\n"+`
PROGRAM Main
VAR_EXTERNAL Sig : BOOL; Clean : BOOL; END_VAR
VAR g : Guarded; END_VAR
  g(Raw := Sig);
  Clean := g.Clean;
END_PROGRAM`)
	h := newHost()
	h.vals["Sig"] = ir.BoolVal(true)
	frame := ir.NewFrame(prog)
	if err := ir.Run(prog, frame, h); err != nil {
		t.Fatalf("run: %v", err)
	}
	if h.vals["Clean"].B {
		t.Fatal("the nested debounce should still be timing on scan one")
	}
	h.now += 500
	if err := ir.Run(prog, frame, h); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !h.vals["Clean"].B {
		t.Fatal("the nested debounce should have elapsed")
	}
}

// Power-pin resolution on a user block, and the two shapes that have no
// answer: every BOOL input bound, and no BOOL output at all.
func TestLadderFunctionBlockPowerPins(t *testing.T) {
	lib := `FUNCTION_BLOCK Latch
VAR_INPUT  Set : BOOL; Clear : BOOL; END_VAR
VAR_OUTPUT Q : BOOL; END_VAR
Q := (Q OR Set) AND NOT Clear;
END_FUNCTION_BLOCK

FUNCTION_BLOCK Totalizer
VAR_INPUT  Enable : BOOL; Rate : REAL; END_VAR
VAR_OUTPUT Total : REAL; END_VAR
IF Enable THEN Total := Total + Rate; END_IF;
END_FUNCTION_BLOCK`

	// Power drives the first unbound BOOL input and continues from the
	// first BOOL output.
	got := toST(t, `PROGRAM p
VAR_EXTERNAL A : BOOL; C : BOOL; Out : BOOL; END_VAR
VAR l1 : Latch; END_VAR
LD
  RUNG r  A l1:Latch(Clear := C) ( Out )
END_LD
END_PROGRAM`, lib)
	if !strings.Contains(got, "l1(Set := A, Clear := C);") || !strings.Contains(got, "Out := l1.Q;") {
		t.Fatalf("power pins on a user block wrong:\n%s", got)
	}

	// No BOOL output: power passes THROUGH, so the contact ahead of the
	// block still conditions the coil behind it.
	got = toST(t, `PROGRAM p
VAR_EXTERNAL A : BOOL; R : REAL; Out : BOOL; END_VAR
VAR z1 : Totalizer; END_VAR
LD
  RUNG r  A z1:Totalizer(Rate := R) ( Out )
END_LD
END_PROGRAM`, lib)
	if !strings.Contains(got, "z1(Enable := A, Rate := R);") || !strings.Contains(got, "Out := A;") {
		t.Fatalf("a block with no BOOL output should pass power through:\n%s", got)
	}

	// Every BOOL input bound by name leaves power nowhere to land — fine
	// on an unconditioned rung, an error on a conditioned one.
	conditioned := `PROGRAM p
VAR_EXTERNAL A : BOOL; S : BOOL; C : BOOL; END_VAR
VAR l1 : Latch; END_VAR
LD
  RUNG r  A l1:Latch(Set := S, Clear := C)
END_LD
END_PROGRAM`
	if _, err := Transpile(conditioned, lib); err == nil || !strings.Contains(err.Error(), "no free BOOL input") {
		t.Fatalf("err = %v, want a 'no free BOOL input' refusal", err)
	}
	unconditioned := strings.Replace(conditioned, "RUNG r  A l1:", "RUNG r  l1:", 1)
	if _, err := Transpile(unconditioned, lib); err != nil {
		t.Fatalf("an unconditioned all-pins-bound call is legal: %v", err)
	}
}

func TestLadderFunctionBlockErrors(t *testing.T) {
	dup := `FUNCTION_BLOCK Twice
VAR_INPUT IN : BOOL; END_VAR
VAR_OUTPUT Q : BOOL; END_VAR
LD
  RUNG a  IN ( Q )
END_LD
END_FUNCTION_BLOCK

FUNCTION_BLOCK Twice
VAR_INPUT IN : BOOL; END_VAR
VAR_OUTPUT Q : BOOL; END_VAR
LD
  RUNG b  IN ( Q )
END_LD
END_FUNCTION_BLOCK`
	_, err := Transpile(dup)
	if err == nil || !strings.Contains(err.Error(), "already declared") {
		t.Fatalf("duplicate FUNCTION_BLOCK: err = %v", err)
	}
	if err != nil && !strings.Contains(err.Error(), "line 9") {
		t.Errorf("the error should name the SECOND declaration's line: %v", err)
	}

	// A VAR_IN_OUT left unbound at the call site is a compile error, and it
	// names the pin — the ST compiler's rule, reached through ladder.
	missing := `FUNCTION_BLOCK FB_Ref
VAR_IN_OUT N : INT; END_VAR
VAR_INPUT  Go : BOOL; END_VAR
LD
  RUNG bump  Go bumper:CTU(PV := 100, CV => N)
END_LD
END_FUNCTION_BLOCK

PROGRAM p
VAR_EXTERNAL Cmd : BOOL; END_VAR
VAR r : FB_Ref; END_VAR
LD
  RUNG r1  Cmd r:FB_Ref()
END_LD
END_PROGRAM`
	stSrc := toST(t, missing)
	ast, perr := st.Parse(stSrc)
	if perr != nil {
		t.Fatalf("parse: %v", perr)
	}
	if _, lerr := st.Lower(ast); lerr == nil || !strings.Contains(lerr.Error(), "VAR_IN_OUT") {
		t.Fatalf("unbound VAR_IN_OUT: err = %v", lerr)
	}
}

// The render model groups a file's POUs: rungs stay in one source-ordered
// list (an editor that knows nothing about blocks still draws them all)
// and each names the block it belongs to.
func TestLadderFunctionBlockGraph(t *testing.T) {
	src := pumpSeq + `

PROGRAM Main
VAR_EXTERNAL Cmd : BOOL; Lvl : REAL; PumpRun : BOOL; END_VAR
VAR p1 : PumpSeq; END_VAR
LD
  RUNG call  Cmd p1:PumpSeq(Level := Lvl, Run => PumpRun)
END_LD
END_PROGRAM`

	m, err := Graph(src)
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "Main" {
		t.Errorf("model name = %q, want Main", m.Name)
	}
	if len(m.Rungs) != 3 {
		t.Fatalf("want 3 rungs (2 in the block, 1 in the program), got %d", len(m.Rungs))
	}
	if m.Rungs[0].POU != "PumpSeq" || m.Rungs[1].POU != "PumpSeq" {
		t.Errorf("the block's rungs should name their POU: %+v", m.Rungs[:2])
	}
	if m.Rungs[2].POU != "" {
		t.Errorf("the program's rung should have no POU: %+v", m.Rungs[2])
	}
	if len(m.Blocks) != 1 || m.Blocks[0].Name != "PumpSeq" {
		t.Fatalf("blocks = %+v", m.Blocks)
	}
	if len(m.Blocks[0].Pins) != 4 {
		t.Errorf("PumpSeq pins = %+v, want Start/Stop/Level/Run", m.Blocks[0].Pins)
	}
	// The call element shows the pins power really uses, not the IN/Q
	// default a block with no resolvable signature would get.
	call := m.Rungs[2].Elements[1]
	if call.Kind != "fb" || call.PowerIn != "Start" || call.PowerOut != "Run" {
		t.Errorf("fb element power pins = %+v", call)
	}
	// Declarations carry their POU too, compact one-line sections included.
	var start *VarDecl
	for i := range m.Vars {
		if m.Vars[i].Name == "Start" {
			start = &m.Vars[i]
		}
	}
	if start == nil || start.POU != "PumpSeq" || start.Section != "VAR_INPUT" {
		t.Errorf("Start declaration = %+v", start)
	}
}

// A rung may carry its elements on the RUNG header line — the compact form
// a short subroutine reads best in — and an edit keeps it there.
func TestInlineRungBody(t *testing.T) {
	src := `PROGRAM p
VAR_EXTERNAL A : BOOL; B : BOOL; Out : BOOL; END_VAR
LD
  RUNG one (* a one-liner *)  A /B ( Out )
END_LD
END_PROGRAM`
	got := toST(t, src)
	if !strings.Contains(got, "Out := (A AND NOT (B));") {
		t.Fatalf("inline rung body:\n%s", got)
	}
	m, err := Graph(src)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Rungs) != 1 || !m.Rungs[0].Inline || m.Rungs[0].Comment != "a one-liner" {
		t.Fatalf("rung = %+v", m.Rungs)
	}
	edits, err := ApplyEdit(src, EditOp{Type: "setRef", Rung: "one", Path: []int{0}, Ref: "C"})
	if err != nil {
		t.Fatal(err)
	}
	out := applyTextEdits(src, edits)
	if !strings.Contains(out, "RUNG one (* a one-liner *)  C /B ( Out )") {
		t.Fatalf("an inline rung should stay inline:\n%s", out)
	}
	if strings.Count(out, "\n") != strings.Count(src, "\n") {
		t.Fatalf("the edit added or removed lines:\n%s", out)
	}

	// The rung comment rewrites the HEADER, never landing after the
	// elements (where the next parse would read it as one).
	edits, err = ApplyEdit(out, EditOp{Type: "setRungComment", Rung: "one", Text: "retagged"})
	if err != nil {
		t.Fatal(err)
	}
	out = applyTextEdits(out, edits)
	if !strings.Contains(out, "RUNG one  (* retagged *)  C /B ( Out )") {
		t.Fatalf("setRungComment on an inline rung:\n%s", out)
	}
	m, err = Graph(out)
	if err != nil {
		t.Fatalf("the result must still parse: %v\n%s", err, out)
	}
	if m.Rungs[0].Comment != "retagged" || len(m.Rungs[0].Elements) != 2 {
		t.Fatalf("rung = %+v", m.Rungs[0])
	}
}
