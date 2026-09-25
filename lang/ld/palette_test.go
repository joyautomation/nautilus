package ld

import (
	"strings"
	"testing"
)

// Edit parity for the ladder palette's FB picker and instance naming:
// inserting any block type (standard or a project library's), naming the
// instance, capturing outputs with `=>`, renaming an instance everywhere it
// is used, and declaring what a retag introduced. Every result must parse;
// semantic holes (`_`) are left for the diagnostics, as elsewhere.

// A library block shaped like the lift station's MotorStarter: a non-BOOL
// input first, so power lands on the first free BOOL input.
const starterLib = `FUNCTION_BLOCK Starter
VAR_INPUT
    Mode   : INT;
    Req    : BOOL;
    Permit : BOOL;
END_VAR
VAR_IN_OUT
    Hours : REAL;
END_VAR
VAR_OUTPUT
    Run   : BOOL;
    Fault : BOOL;
END_VAR
Run := Req AND Permit AND Mode = 2;
Fault := FALSE;
END_FUNCTION_BLOCK
`

const palSrc = `PROGRAM p
VAR_EXTERNAL
    Cmd : BOOL;
    Out : BOOL;
    Alm : BOOL;
END_VAR
LD
  RUNG run
    Cmd ( Out )
END_LD
END_PROGRAM`

func applyStep(t *testing.T, src string, op EditOp, libs ...string) string {
	t.Helper()
	edits, err := ApplyEdit(src, op, libs...)
	if err != nil {
		t.Fatalf("%s: %v", op.Type, err)
	}
	out := applyTextEdits(src, edits)
	if _, err := Graph(out, libs...); err != nil {
		t.Fatalf("%s: result does not parse: %v\n%s", op.Type, err, out)
	}
	return out
}

func findType(ts []FBType, name string) (FBType, bool) {
	for _, t := range ts {
		if t.Name == name {
			return t, true
		}
	}
	return FBType{}, false
}

func TestCatalogListsStandardThenLibraryBlocks(t *testing.T) {
	m, err := Graph(palSrc, starterLib)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.FBTypes) == 0 || m.FBTypes[0].Name != "TON" {
		t.Fatalf("catalog must lead with the standard blocks: %+v", m.FBTypes)
	}
	for _, std := range []string{"TON", "TOF", "TP", "CTU", "CTD", "CTUD", "R_TRIG", "F_TRIG", "SR", "RS"} {
		ty, ok := findType(m.FBTypes, std)
		if !ok || ty.User || ty.PowerIn == "" {
			t.Errorf("standard %s missing or without power pins: %+v", std, ty)
		}
	}
	ton, _ := findType(m.FBTypes, "TON")
	if ton.Args != "PT := T#1S" || ton.Prefix != "t" {
		t.Errorf("TON keeps its old palette defaults: %+v", ton)
	}
	ctud, _ := findType(m.FBTypes, "CTUD")
	if ctud.PowerIn != "CU" || ctud.PowerOut != "QU" {
		t.Errorf("CTUD takes power on CU, gives it on QU: %+v", ctud)
	}
	st, ok := findType(m.FBTypes, "Starter")
	if !ok || !st.User {
		t.Fatalf("the library block must be listed: %+v", m.FBTypes)
	}
	// Power on the first free BOOL input; non-BOOL inputs and in-outs get a
	// `_` to retag; other BOOL inputs stay unbound.
	if st.PowerIn != "Req" || st.PowerOut != "Run" {
		t.Errorf("Starter power pins = %s/%s, want Req/Run", st.PowerIn, st.PowerOut)
	}
	if st.Args != "Mode := _, Hours := _" {
		t.Errorf("Starter default args = %q", st.Args)
	}
	if st.Prefix != "s" {
		t.Errorf("Starter prefix = %q, want s", st.Prefix)
	}
	if len(st.Pins) != 6 || st.Pins[3].Dir != "inout" {
		t.Errorf("Starter pins = %+v", st.Pins)
	}
}

func TestInsertLibraryBlockWithNamedInstance(t *testing.T) {
	out := applyStep(t, palSrc, EditOp{
		Type: "insert", Rung: "run", Kind: "fb", Index: 1, Path: []int{},
		Inst: "m_pump", FbType: "Starter", Args: "Mode := _, Hours := _",
	}, starterLib)
	if !strings.Contains(out, "Cmd m_pump:Starter(Mode := _, Hours := _) ( Out )") {
		t.Fatalf("insert:\n%s", out)
	}
	m, _ := Graph(out, starterLib)
	el := m.Rungs[0].Elements[1]
	if el.PowerIn != "Req" || el.PowerOut != "Run" {
		t.Errorf("inserted block's power pins = %s/%s, want Req/Run", el.PowerIn, el.PowerOut)
	}
	// The name is the author's; a taken one is refused, not aliased.
	if _, err := ApplyEdit(out, EditOp{Type: "insert", Rung: "run", Kind: "fb", Inst: "M_PUMP", FbType: "TON"}, starterLib); err == nil {
		t.Error("a second m_pump must be refused")
	}
}

func TestInsertBlockCapturingOutputs(t *testing.T) {
	out := applyStep(t, palSrc, EditOp{
		Type: "insert", Rung: "run", Kind: "fb", Index: 1,
		Inst: "s1", FbType: "Starter", Args: "Mode := 2, Hours := RunHrs, Fault => Alm",
	}, starterLib)
	if !strings.Contains(out, "s1:Starter(Mode := 2, Hours := RunHrs, Fault => Alm)") {
		t.Fatalf("insert with =>:\n%s", out)
	}
	st, err := Transpile(out, starterLib)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(st, "Fault => Alm") || !strings.Contains(st, "Req := Cmd") {
		t.Fatalf("power must land on Req and the capture pass through:\n%s", st)
	}
	// Capturing the power-out pin by name leaves power on it too.
	out = applyStep(t, palSrc, EditOp{
		Type: "insert", Rung: "run", Kind: "fb", Index: 1,
		Inst: "t9", FbType: "TON", Args: "PT := T#2S, ET => Elapsed",
	}, starterLib)
	if !strings.Contains(out, "Cmd t9:TON(PT := T#2S, ET => Elapsed) ( Out )") {
		t.Fatalf("standard block with =>:\n%s", out)
	}
}

func TestRenameInstanceEverywhere(t *testing.T) {
	src := `FUNCTION_BLOCK Inner
VAR_INPUT Go : BOOL; END_VAR
VAR_OUTPUT Done : BOOL; END_VAR
VAR t1 : TON; END_VAR
LD
  RUNG inner
    Go t1:TON(PT := T#1S) ( Done )
END_LD
END_FUNCTION_BLOCK

PROGRAM p
VAR_EXTERNAL Cmd : BOOL; Out : BOOL; Warm : BOOL; Elapsed : TIME; END_VAR
VAR
    t1 : TON; (* t1 is the warm-up timer *)
END_VAR
LD
  // t1 times the warm-up
  RUNG t1
    Cmd t1:TON(PT := T#5S, ET => Elapsed) ( Out )
  RUNG warm
    t1.Q GE(t1.ET, T#2S) ( Warm )
  RUNG pins
    Cmd x1:Inner(Go := t1.Q) ( Warm )
END_LD
END_PROGRAM`
	edits, err := ApplyEdit(src, EditOp{Type: "renameInst", Rung: "t1", Path: []int{1}, Name: "t_run"})
	if err != nil {
		t.Fatal(err)
	}
	out := applyTextEdits(src, edits)
	for _, want := range []string{
		"    t_run : TON; (* t1 is the warm-up timer *)", // header decl; comment untouched
		"// t1 times the warm-up",                        // a note is prose
		"RUNG t1\n",                                      // a rung's own name is not the instance
		"Cmd t_run:TON(PT := T#5S, ET => Elapsed) ( Out )",
		"t_run.Q GE(t_run.ET, T#2S) ( Warm )",
		"x1:Inner(Go := t_run.Q)",
		"Go t1:TON(PT := T#1S) ( Done )", // the block's own t1 is another scope
		"VAR t1 : TON; END_VAR",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rename: missing %q in\n%s", want, out)
		}
	}
	if _, err := Graph(out); err != nil {
		t.Fatalf("renamed source must parse: %v", err)
	}

	// Refusals: taken, invalid, and not a block.
	for _, op := range []EditOp{
		{Type: "renameInst", Rung: "t1", Path: []int{1}, Name: "Cmd"},
		{Type: "renameInst", Rung: "t1", Path: []int{1}, Name: "x1"},
		{Type: "renameInst", Rung: "t1", Path: []int{1}, Name: "2bad"},
		{Type: "renameInst", Rung: "t1", Path: []int{0}, Name: "ok"},
	} {
		if _, err := ApplyEdit(src, op); err == nil {
			t.Errorf("rename to %q at %v must be refused", op.Name, op.Path)
		}
	}
	// Same name: nothing to do.
	if e, err := ApplyEdit(src, EditOp{Type: "renameInst", Rung: "t1", Path: []int{1}, Name: "t1"}); err != nil || len(e) != 0 {
		t.Errorf("rename to itself = %v, %v", e, err)
	}
}

func TestRenameInlineInstanceWithoutHeader(t *testing.T) {
	out := applyStep(t, ladderSrc, EditOp{Type: "renameInst", Rung: "alarm", Path: []int{1}, Name: "coldTimer"})
	if !strings.Contains(out, "Cold coldTimer:TON(PT := T#10S) ( Alm )") {
		t.Fatalf("inline rename:\n%s", out)
	}
}

// Declare-on-retag writes the PROGRAM's header even when the file defines
// a FUNCTION_BLOCK ahead of it.
func TestDeclareVarTargetsTheProgram(t *testing.T) {
	src := `FUNCTION_BLOCK Inner
VAR_INPUT Go : BOOL; END_VAR
VAR_OUTPUT Done : BOOL; END_VAR
LD
  RUNG inner
    Go ( Done )
END_LD
END_FUNCTION_BLOCK

PROGRAM p
VAR_EXTERNAL
    Cmd : BOOL;
END_VAR
LD
  RUNG r
    Cmd LevelLow ( Go )
END_LD
END_PROGRAM`
	out := applyStep(t, src, EditOp{Type: "declareVar", Name: "LevelLow", VarType: "BOOL", Section: "VAR_EXTERNAL"})
	if !strings.Contains(out, "    Cmd : BOOL;\n    LevelLow : BOOL;\nEND_VAR") {
		t.Fatalf("declareVar must land in the PROGRAM's VAR_EXTERNAL:\n%s", out)
	}
	// Go is the block's input, not the program's: declarable in the program.
	out = applyStep(t, out, EditOp{Type: "declareVar", Name: "Go", VarType: "BOOL", Section: "VAR"})
	if !strings.Contains(out, "VAR\n    Go : BOOL;\nEND_VAR\nLD\n  RUNG r") {
		t.Fatalf("a new VAR section goes above the PROGRAM's LD:\n%s", out)
	}
}
