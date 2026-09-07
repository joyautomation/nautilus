package ld

import (
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/lang/fbd"
	"github.com/joyautomation/nautilus/lang/ir"
)

const ladderSrc = `PROGRAM Main
VAR_EXTERNAL
  Start : BOOL; Stop : BOOL; Run : BOOL;
  Cold : BOOL; Alm : BOOL; Lamp : BOOL; Horn : BOOL;
  TempC : REAL; HiAlm : BOOL;
  Faulted : BOOL; Reset : BOOL;
  Levels : ARRAY[1..2] OF BOOL;
END_VAR
LD
  RUNG seal (* motor seal-in *)
    [ Start | Run ] /Stop ( Run )

  RUNG alarm
    Cold t1:TON(PT := T#10S) ( Alm )

  RUNG hot
    GT(TempC, 90.0) ( HiAlm )

  RUNG annunciate
    [ Alm | HiAlm ] ( Lamp ) ( Horn )

  RUNG latch
    HiAlm ( S Faulted )

  RUNG clear
    Reset ( R Faulted )

  RUNG element
    Levels[1] ( Levels[2] )
END_LD
END_PROGRAM`

func TestTranspileRungs(t *testing.T) {
	out, err := Transpile(ladderSrc)
	if err != nil {
		t.Fatalf("transpile: %v", err)
	}
	for _, want := range []string{
		"Run := AND(OR(Start, Run), NOT Stop)",
		"t1 : TON(IN := Cold, PT := T#10S)",
		"Alm := t1.Q",
		"HiAlm := GT(TempC, 90.0)",
		"w_annunciate = OR(Alm, HiAlm)",
		"Lamp := w_annunciate",
		"Horn := w_annunciate",
		"Faulted := OR(Faulted, HiAlm)",
		"Faulted := AND(Faulted, NOT Reset)",
		"Levels[2] := Levels[1]",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "FBD") || !strings.Contains(out, "END_FBD") {
		t.Errorf("output must be an FBD program:\n%s", out)
	}
}

// The emitted netlist must survive the whole FBD pipeline down to the IR.
func TestLadderCompilesEndToEnd(t *testing.T) {
	out, err := Transpile(ladderSrc)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fbd.Compile(out); err != nil {
		t.Fatalf("emitted FBD does not compile: %v\n%s", err, out)
	}
	// And it renders — every rung becomes diagram nodes.
	if _, err := fbd.Graph(out); err != nil {
		t.Fatalf("emitted FBD does not graph: %v", err)
	}
}

func TestLineMap(t *testing.T) {
	out, lineOf, err := TranspileWithLines(ladderSrc)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(out, "\n")
	if len(lineOf) != len(lines) {
		t.Fatalf("map length %d != %d lines", len(lineOf), len(lines))
	}
	// The seal rung's statement maps back to its RUNG line in the .ld.
	srcLines := strings.Split(ladderSrc, "\n")
	for i, l := range lines {
		if strings.Contains(l, "Run := AND(OR(Start, Run)") {
			if !strings.Contains(srcLines[lineOf[i]-1], "RUNG seal") {
				t.Errorf("statement maps to source line %d (%q), want the RUNG seal line", lineOf[i], srcLines[lineOf[i]-1])
			}
			return
		}
	}
	t.Fatal("seal statement not found")
}

func TestRungErrors(t *testing.T) {
	cases := []struct{ name, body, wantErr string }{
		{"coil mid-rung", "( Run ) Start ( Alm )", "right end"},
		{"no coil", "Start Stop", "at least one coil"},
		{"one-leg branch", "[ Start ] ( Run )", "two legs"},
		{"unclosed branch", "[ Start | Run ( Run )", "']'"},
		{"power pin passed", "Cold t1:TON(IN := Start) ( Alm )", "power drives IN"},
		{"elements before rung", "", "before any RUNG"},
	}
	for _, tc := range cases {
		src := "PROGRAM p\nLD\n  RUNG r\n    " + tc.body + "\nEND_LD\nEND_PROGRAM"
		if tc.name == "elements before rung" {
			src = "PROGRAM p\nLD\n    Start ( Run )\nEND_LD\nEND_PROGRAM"
		}
		_, err := Transpile(src)
		if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
			t.Errorf("%s: err = %v, want containing %q", tc.name, err, tc.wantErr)
		}
	}
}

// A rung with only a coil is a rail-driven TRUE; nested branches compose.
func TestRungShapes(t *testing.T) {
	src := `PROGRAM p
VAR_EXTERNAL A : BOOL; B : BOOL; C : BOOL; D : BOOL; Out : BOOL; AlwaysOn : BOOL; END_VAR
LD
  RUNG on
    ( AlwaysOn )
  RUNG nested
    [ A B | [ C | D ] ] ( Out )
END_LD
END_PROGRAM`
	out, err := Transpile(src)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "AlwaysOn := TRUE") {
		t.Errorf("bare coil must drive TRUE:\n%s", out)
	}
	if !strings.Contains(out, "Out := OR(AND(A, B), OR(C, D))") {
		t.Errorf("nested branch composition wrong:\n%s", out)
	}
	if _, err := fbd.Compile(out); err != nil {
		t.Fatalf("does not compile: %v\n%s", err, out)
	}
}

func TestGraphModel(t *testing.T) {
	m, err := Graph(ladderSrc)
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "Main" || len(m.Rungs) != 7 {
		t.Fatalf("name %q, %d rungs", m.Name, len(m.Rungs))
	}
	seal := m.Rungs[0]
	if seal.Name != "seal" || len(seal.Elements) != 2 || len(seal.Coils) != 1 {
		t.Fatalf("seal shape: %+v", seal)
	}
	if seal.Elements[0].Kind != "branch" || len(seal.Elements[0].Legs) != 2 {
		t.Fatalf("first element must be the 2-leg branch: %+v", seal.Elements[0])
	}
	if !seal.Elements[1].Neg || seal.Elements[1].Ref != "Stop" {
		t.Fatalf("NC contact wrong: %+v", seal.Elements[1])
	}
	alarm := m.Rungs[1]
	if alarm.Elements[1].Kind != "fb" || alarm.Elements[1].PowerIn != "IN" || alarm.Elements[1].PowerOut != "Q" {
		t.Fatalf("fb element wrong: %+v", alarm.Elements[1])
	}
	latch := m.Rungs[4]
	if latch.Coils[0].Mode != "S" || latch.Coils[0].Ref != "Faulted" {
		t.Fatalf("set coil wrong: %+v", latch.Coils[0])
	}
	// Header vars carry through for bounds/badges.
	found := false
	for _, v := range m.Vars {
		if v.Name == "Levels" && strings.Contains(v.Type, "ARRAY") {
			found = true
		}
	}
	if !found {
		t.Fatalf("vars missing Levels array: %+v", m.Vars)
	}
}

func coilIdx(i int) *int { return &i }

func TestLadderEdits(t *testing.T) {
	src := `PROGRAM p
VAR_EXTERNAL Start : BOOL; Stop : BOOL; Run : BOOL; Alt : BOOL; Alm : BOOL; END_VAR
LD
  RUNG seal (* keep the comment *)
    [ Start | Run ] /Stop ( Run )
END_LD
END_PROGRAM`
	step := func(t *testing.T, s string, op EditOp) string {
		t.Helper()
		edits, err := ApplyEdit(s, op)
		if err != nil {
			t.Fatalf("%s: %v", op.Type, err)
		}
		return applyTextEdits(s, edits)
	}

	// Retag the branch's second-leg contact: path [0 (branch), 1 (leg), 0].
	out := step(t, src, EditOp{Type: "setRef", Rung: "seal", Path: []int{0, 1, 0}, Ref: "Alt"})
	if !strings.Contains(out, "[ Start | Alt ] /Stop ( Run )") {
		t.Fatalf("setRef:\n%s", out)
	}
	if !strings.Contains(out, "(* keep the comment *)") {
		t.Fatal("the rung header (and its comment) must survive a body rewrite")
	}

	// NO <-> NC.
	out = step(t, src, EditOp{Type: "toggleNeg", Rung: "seal", Path: []int{1}})
	if !strings.Contains(out, "[ Start | Run ] Stop ( Run )") {
		t.Fatalf("toggleNeg:\n%s", out)
	}

	// Coil mode cycles through the latch forms.
	out = step(t, src, EditOp{Type: "setCoilMode", Rung: "seal", Coil: coilIdx(0), Mode: "S"})
	if !strings.Contains(out, "( S Run )") {
		t.Fatalf("setCoilMode:\n%s", out)
	}

	// A function contact swaps its function and args in one gesture.
	fnSrc := `PROGRAM p
VAR_EXTERNAL TempC : REAL; Alm : BOOL; END_VAR
LD
  RUNG cmp
    GT(TempC, 90.0) ( Alm )
END_LD
END_PROGRAM`
	out = step(t, fnSrc, EditOp{Type: "setArgs", Rung: "cmp", Path: []int{0}, Fn: "le", Args: "TempC, 10.0"})
	if !strings.Contains(out, "LE(TempC, 10.0) ( Alm )") {
		t.Fatalf("setArgs with fn:\n%s", out)
	}

	// Insert an open contact at the root series end; retag comes after.
	out = step(t, src, EditOp{Type: "insert", Rung: "seal", Kind: "contact", Index: 2})
	if !strings.Contains(out, "[ Start | Run ] /Stop _ ( Run )") {
		t.Fatalf("insert contact:\n%s", out)
	}

	// A new leg on the branch.
	out = step(t, src, EditOp{Type: "addLeg", Rung: "seal", Path: []int{0}})
	if !strings.Contains(out, "[ Start | Run | _ ]") {
		t.Fatalf("addLeg:\n%s", out)
	}

	// Delete the NC contact.
	out = step(t, src, EditOp{Type: "delete", Rung: "seal", Path: []int{1}})
	if !strings.Contains(out, "[ Start | Run ] ( Run )") {
		t.Fatalf("delete:\n%s", out)
	}

	// Deleting the only coil never blocks: it becomes the `_` placeholder
	// (the grammar needs a coil; the `_` diagnostic guides). Deleting the
	// placeholder itself is the one refusal — the rung is the target then.
	out = step(t, src, EditOp{Type: "delete", Rung: "seal", Coil: coilIdx(0)})
	if !strings.Contains(out, "[ Start | Run ] /Stop ( _ )") {
		t.Fatalf("last-coil delete must placeholder:\n%s", out)
	}
	if _, err := ApplyEdit(out, EditOp{Type: "delete", Rung: "seal", Coil: coilIdx(0)}); err == nil {
		t.Fatal("deleting the placeholder coil must point at the rung")
	}

	// Rung lifecycle.
	out = step(t, src, EditOp{Type: "addRung", Name: "alarm", After: "seal"})
	if !strings.Contains(out, "RUNG alarm") || !strings.Contains(out, "( _ )") {
		t.Fatalf("addRung:\n%s", out)
	}
	out = step(t, out, EditOp{Type: "renameRung", Rung: "alarm", Name: "annunciate"})
	if !strings.Contains(out, "RUNG annunciate") {
		t.Fatalf("renameRung:\n%s", out)
	}
	out = step(t, out, EditOp{Type: "deleteRung", Rung: "annunciate"})
	if strings.Contains(out, "annunciate") {
		t.Fatalf("deleteRung:\n%s", out)
	}

	// Every edited result still transpiles and compiles.
	final, err := Transpile(out)
	if err != nil {
		t.Fatalf("edited source no longer transpiles: %v\n%s", err, out)
	}
	if _, err := fbd.Compile(final); err != nil {
		t.Fatalf("edited source no longer compiles: %v", err)
	}

	// Garbage refs are rejected by the parser-backed gate.
	if _, err := ApplyEdit(src, EditOp{Type: "setRef", Rung: "seal", Path: []int{1}, Ref: "no such"}); err == nil {
		t.Fatal("invalid ref must be rejected")
	}
}

// applyTextEdits mirrors the extension's WorkspaceEdit application.
func applyTextEdits(src string, edits []TextEdit) string {
	lines := strings.Split(src, "\n")
	for i := len(edits) - 1; i >= 0; i-- {
		e := edits[i]
		var out []string
		out = append(out, lines[:e.Line-1]...)
		head := ""
		if e.Line-1 < len(lines) {
			head = lines[e.Line-1][:e.Col-1]
		}
		tail := ""
		if e.EndLine-1 < len(lines) {
			tail = lines[e.EndLine-1][e.EndCol-1:]
		}
		merged := head + e.NewText + tail
		out = append(out, strings.Split(merged, "\n")...)
		if e.EndLine < len(lines) {
			out = append(out, lines[e.EndLine:]...)
		}
		lines = out
	}
	return strings.Join(lines, "\n")
}

func TestLadderMove(t *testing.T) {
	src := `PROGRAM p
VAR_EXTERNAL A : BOOL; B : BOOL; C : BOOL; Out : BOOL; Out2 : BOOL; END_VAR
LD
  RUNG one
    A B C ( Out )
  RUNG two
    ( Out2 )
END_LD
END_PROGRAM`
	step := func(t *testing.T, s string, op EditOp) string {
		t.Helper()
		edits, err := ApplyEdit(s, op)
		if err != nil {
			t.Fatalf("move: %v", err)
		}
		return applyTextEdits(s, edits)
	}

	// Move C before A (same series, target index before source).
	out := step(t, src, EditOp{Type: "move", Rung: "one", Path: []int{2}, ToPath: []int{}, ToIndex: 0})
	if !strings.Contains(out, "C A B ( Out )") {
		t.Fatalf("reorder before:\n%s", out)
	}
	// Move A after C (same series, target index past source — fixup).
	out = step(t, src, EditOp{Type: "move", Rung: "one", Path: []int{0}, ToPath: []int{}, ToIndex: 3})
	if !strings.Contains(out, "B C A ( Out )") {
		t.Fatalf("reorder after:\n%s", out)
	}
	// Move B to another rung.
	out = step(t, src, EditOp{Type: "move", Rung: "one", Path: []int{1}, ToRung: "two", ToPath: []int{}, ToIndex: 0})
	if !strings.Contains(out, "A C ( Out )") || !strings.Contains(out, "B ( Out2 )") {
		t.Fatalf("cross-rung move:\n%s", out)
	}
	// A contact can't land in the coil zone; a coil can't land mid-series.
	if _, err := ApplyEdit(src, EditOp{Type: "move", Rung: "one", Path: []int{0}, ToCoil: true, ToIndex: 0}); err == nil {
		t.Fatal("contact into coil zone must be refused")
	}
	if _, err := ApplyEdit(src, EditOp{Type: "move", Rung: "one", Coil: coilIdx(0), ToPath: []int{}, ToIndex: 0}); err == nil {
		t.Fatal("coil into the series must be refused")
	}
}

func TestLadderComments(t *testing.T) {
	src := `PROGRAM p
VAR_EXTERNAL A : BOOL; Out : BOOL; Out2 : BOOL; END_VAR
LD
  // Interlock notes:
  // horn latches until acknowledged.
  RUNG one (* the seal-in *)
    A ( Out )

  // trailing note
  RUNG two
    ( Out2 )
END_LD
END_PROGRAM`

	m, err := Graph(src)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Comments) != 2 {
		t.Fatalf("want 2 comment runs, got %+v", m.Comments)
	}
	if m.Comments[0].Text != "Interlock notes:\nhorn latches until acknowledged." {
		t.Fatalf("run join: %q", m.Comments[0].Text)
	}
	if m.Comments[0].Line != 4 || m.Comments[0].EndLine != 5 {
		t.Fatalf("run lines: %+v", m.Comments[0])
	}
	if m.Rungs[0].Comment != "the seal-in" {
		t.Fatalf("header comment: %q", m.Rungs[0].Comment)
	}

	step := func(t *testing.T, s string, op EditOp) string {
		t.Helper()
		edits, err := ApplyEdit(s, op)
		if err != nil {
			t.Fatalf("%s: %v", op.Type, err)
		}
		return applyTextEdits(s, edits)
	}

	// Rewrite a run (multiline in, multiline out).
	out := step(t, src, EditOp{Type: "setComment", Comment: coilIdx(0), Text: "first\nsecond"})
	if !strings.Contains(out, "  // first\n  // second\n  RUNG one") {
		t.Fatalf("setComment:\n%s", out)
	}

	// Empty text deletes the run, FBD-style.
	out = step(t, src, EditOp{Type: "setComment", Comment: coilIdx(1), Text: ""})
	if strings.Contains(out, "trailing note") {
		t.Fatalf("setComment delete:\n%s", out)
	}

	// Add after a rung and at the end.
	out = step(t, src, EditOp{Type: "addComment", After: "one", Text: "between"})
	if !strings.Contains(out, "( Out )\n\n  // between\n") {
		t.Fatalf("addComment after:\n%s", out)
	}
	out = step(t, out, EditOp{Type: "addComment", Text: "at the end"})
	if !strings.Contains(out, "  // at the end\nEND_LD") {
		t.Fatalf("addComment end:\n%s", out)
	}

	// Header comment set / clear survives, and the model reflects it.
	out = step(t, src, EditOp{Type: "setRungComment", Rung: "two", Text: "annunciator"})
	m2, err := Graph(out)
	if err != nil {
		t.Fatal(err)
	}
	if m2.Rungs[1].Comment != "annunciator" {
		t.Fatalf("setRungComment: %q\n%s", m2.Rungs[1].Comment, out)
	}
	out = step(t, out, EditOp{Type: "setRungComment", Rung: "one", Text: ""})
	if strings.Contains(out, "seal-in") {
		t.Fatalf("setRungComment clear:\n%s", out)
	}

	// Everything still transpiles.
	if _, err := Transpile(out); err != nil {
		t.Fatalf("edited source no longer transpiles: %v\n%s", err, out)
	}
}

func TestLadderPaste(t *testing.T) {
	src := `PROGRAM p
VAR_EXTERNAL A : BOOL; B : BOOL; C : BOOL; Out : BOOL; Out2 : BOOL; END_VAR
LD
  RUNG one
    [ A | /B ] t1:TON(PT := T#1S) ( Out )
  RUNG two
    C ( Out2 )
END_LD
END_PROGRAM`

	step := func(t *testing.T, s string, op EditOp) string {
		t.Helper()
		edits, err := ApplyEdit(s, op)
		if err != nil {
			t.Fatalf("%s: %v", op.Type, err)
		}
		return applyTextEdits(s, edits)
	}

	// Paste a whole branch subtree into another rung — legs survive.
	branch := &Element{Kind: "branch", Legs: [][]Element{
		{{Kind: "contact", Ref: "A"}},
		{{Kind: "contact", Ref: "B", Neg: true}},
	}}
	out := step(t, src, EditOp{Type: "insert", Rung: "two", Element: branch, Index: 0})
	if !strings.Contains(out, "[ A | /B ] C ( Out2 )") {
		t.Fatalf("paste branch:\n%s", out)
	}

	// Paste an fb whose instance already exists — it gets a fresh name.
	fb := &Element{Kind: "fb", Inst: "t1", Type: "TON", Args: "PT := T#1S"}
	out = step(t, src, EditOp{Type: "insert", Rung: "two", Element: fb, Index: 1})
	if !strings.Contains(out, "C t2:TON(PT := T#1S) ( Out2 )") {
		t.Fatalf("paste fb uniquify:\n%s", out)
	}

	// Paste a coil into the coil zone.
	coil := &Element{Kind: "coil", Ref: "Out", Mode: "S"}
	out = step(t, src, EditOp{Type: "insert", Rung: "two", Kind: "coil", Element: coil, Index: 999})
	if !strings.Contains(out, "( Out2 ) ( S Out )") {
		t.Fatalf("paste coil:\n%s", out)
	}

	// Everything still transpiles.
	if _, err := Transpile(out); err != nil {
		t.Fatalf("pasted source no longer transpiles: %v\n%s", err, out)
	}

	// Garbage payloads are rejected with a readable error.
	if _, err := ApplyEdit(src, EditOp{Type: "insert", Rung: "two", Element: &Element{Kind: "nope"}}); err == nil {
		t.Fatal("unknown pasted kind must be rejected")
	}
}

func TestLadderVars(t *testing.T) {
	src := `PROGRAM p
VAR_EXTERNAL
    A : BOOL;
    Out : BOOL;
END_VAR
LD
  RUNG one
    A ( Out )
END_LD
END_PROGRAM`

	step := func(t *testing.T, s string, op EditOp) string {
		t.Helper()
		edits, err := ApplyEdit(s, op)
		if err != nil {
			t.Fatalf("%s: %v", op.Type, err)
		}
		return applyTextEdits(s, edits)
	}

	// Declare into the existing external section.
	out := step(t, src, EditOp{Type: "declareVar", Name: "Spd", VarType: "REAL"})
	if !strings.Contains(out, "    Spd : REAL;\nEND_VAR") {
		t.Fatalf("declareVar ext:\n%s", out)
	}

	// A retained local creates its missing VAR section above LD.
	out = step(t, out, EditOp{Type: "declareVar", Name: "Cnt", VarType: "INT", Section: "VAR"})
	if !strings.Contains(out, "VAR\n    Cnt : INT;\nEND_VAR\nLD") {
		t.Fatalf("declareVar local:\n%s", out)
	}
	m, err := Graph(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Vars) != 4 {
		t.Fatalf("want 4 vars after declares, got %+v", m.Vars)
	}

	// Duplicates (any section) refuse.
	if _, err := ApplyEdit(out, EditOp{Type: "declareVar", Name: "spd", VarType: "REAL"}); err == nil {
		t.Fatal("duplicate declare must be refused")
	}

	// Delete removes the declaration line; the dangling rung reference is
	// allowed (it becomes a diagnostic, same as FBD).
	out = step(t, out, EditOp{Type: "deleteVar", Name: "A"})
	if strings.Contains(out, "A : BOOL") {
		t.Fatalf("deleteVar:\n%s", out)
	}
	if _, err := Graph(out); err != nil {
		t.Fatalf("header still parses after delete: %v", err)
	}

	// Compact one-line headers refuse whole-line deletes.
	oneLine := `PROGRAM p
VAR_EXTERNAL A : BOOL; Out : BOOL; END_VAR
LD
  RUNG one
    A ( Out )
END_LD
END_PROGRAM`
	if _, err := ApplyEdit(oneLine, EditOp{Type: "deleteVar", Name: "A"}); err == nil {
		t.Fatal("compact-header delete must be refused")
	}
}

// `pin => target` output bindings flow through fb args verbatim — the way
// ladder captures ET/CV into variables (coils are the BOOL path).
func TestLadderOutputBinding(t *testing.T) {
	src := `PROGRAM p
VAR_EXTERNAL Run : BOOL; Done : BOOL; Elapsed : TIME; Warm : BOOL; END_VAR
LD
  RUNG timer
    Run t2:TON(PT := T#5S, ET => Elapsed) ( Done )
  RUNG warm
    GE(Elapsed, T#2S) ( Warm )
END_LD
END_PROGRAM`
	out, err := Transpile(src)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "ET => Elapsed") {
		t.Fatalf("binding must pass through:\n%s", out)
	}
	if _, err := fbd.Compile(out); err != nil {
		t.Fatalf("does not compile: %v", err)
	}
}

func TestLadderWrapBranch(t *testing.T) {
	src := `PROGRAM p
VAR_EXTERNAL A : BOOL; B : BOOL; Out : BOOL; END_VAR
LD
  RUNG r
    A B ( Out )
END_LD
END_PROGRAM`
	edits, err := ApplyEdit(src, EditOp{Type: "wrapBranch", Rung: "r", Path: []int{1}})
	if err != nil {
		t.Fatal(err)
	}
	out := applyTextEdits(src, edits)
	if !strings.Contains(out, "A [ B | _ ] ( Out )") {
		t.Fatalf("wrapBranch:\n%s", out)
	}
	// Wrapping a branch nests it as leg one.
	edits, err = ApplyEdit(out, EditOp{Type: "wrapBranch", Rung: "r", Path: []int{1}})
	if err != nil {
		t.Fatal(err)
	}
	out = applyTextEdits(out, edits)
	if !strings.Contains(out, "A [ [ B | _ ] | _ ] ( Out )") {
		t.Fatalf("wrapBranch nest:\n%s", out)
	}
	if _, err := ApplyEdit(src, EditOp{Type: "wrapBranch", Rung: "r", Coil: coilIdx(0)}); err == nil {
		t.Fatal("wrapping a coil must be refused")
	}
}

// ── a rung's only output may be a function block ────────────────────────────

// A rung whose only output is a TON/CTU instance is legal — the block's own
// output (.Q, .ET, …) is read elsewhere, the way an AB status bit was.
func TestFBOnlyRung(t *testing.T) {
	src := `PROGRAM p
VAR_EXTERNAL Cold : BOOL; Hot : BOOL; Elapsed : TIME; END_VAR
LD
  RUNG timer
    Cold Hot t1:TON(PT := T#5S)
  RUNG bare
    c1:CTU(PV := 5)
END_LD
END_PROGRAM`
	out, err := Transpile(src)
	if err != nil {
		t.Fatalf("a rung whose sole output is a function block must compile: %v", err)
	}
	if !strings.Contains(out, "t1 : TON(IN := AND(Cold, Hot), PT := T#5S)") {
		t.Errorf("timer rung wrong:\n%s", out)
	}
	if !strings.Contains(out, "c1 : CTU(CU := TRUE, PV := 5)") {
		t.Errorf("bare (rail-driven) FB-only rung wrong:\n%s", out)
	}
	if _, err := fbd.Compile(out); err != nil {
		t.Fatalf("does not compile: %v\n%s", err, out)
	}

	// Nested in a branch leg, still counts as the rung's output.
	nested := `PROGRAM p
VAR_EXTERNAL A : BOOL; B : BOOL; END_VAR
LD
  RUNG r
    [ A | B c1:CTU(PV := 3) ]
END_LD
END_PROGRAM`
	if _, err := Transpile(nested); err != nil {
		t.Fatalf("FB nested in a branch leg must still satisfy the rung: %v", err)
	}

	// A rung with neither a coil nor a function block anywhere is still an
	// error — TestRungErrors' "no coil" case covers the exact message.
}

// ── edge contacts (+Name rising, -Name falling; ( P x ) / ( N x ) coils) ────

func TestEdgeContactSyntax(t *testing.T) {
	src := `PROGRAM p
VAR_EXTERNAL A : BOOL; B : BOOL; Rise : BOOL; Fall : BOOL; Both : BOOL; PulseOut : BOOL; DropOut : BOOL; END_VAR
LD
  RUNG rise
    +A ( Rise )
  RUNG fall
    -A ( Fall )
  RUNG dup
    [ +B | +B ] ( Both )
  RUNG pcoil
    A ( P PulseOut )
  RUNG ncoil
    A ( N DropOut )
END_LD
END_PROGRAM`
	out, err := Transpile(src)
	if err != nil {
		t.Fatalf("transpile: %v", err)
	}
	for _, want := range []string{
		"rt_rise_A : R_TRIG(CLK := A)",
		"Rise := rt_rise_A.Q",
		"ft_fall_A : F_TRIG(CLK := A)",
		"Fall := ft_fall_A.Q",
		"rt_pcoil_PulseOut : R_TRIG(CLK := A)",
		"PulseOut := rt_pcoil_PulseOut.Q",
		"ft_ncoil_DropOut : F_TRIG(CLK := A)",
		"DropOut := ft_ncoil_DropOut.Q",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	// Two edge contacts on the same tag in one rung are independent
	// instances, not one instance referenced twice.
	if n := strings.Count(out, ": R_TRIG(CLK := B)"); n != 2 {
		t.Fatalf("want 2 independent R_TRIG instances for B, got %d:\n%s", n, out)
	}
	if !strings.Contains(out, "rt_dup_B :") || !strings.Contains(out, "rt_dup_B_2 :") {
		t.Fatalf("second occurrence must get a distinguishing, stable name:\n%s", out)
	}
	if _, err := fbd.Compile(out); err != nil {
		t.Fatalf("does not compile: %v\n%s", err, out)
	}

	// A recompile of unchanged text yields the same instance names — the
	// naming is stable, so a warm swap's retained R_TRIG/F_TRIG state
	// (their _prev slot) survives.
	out2, err := Transpile(src)
	if err != nil {
		t.Fatal(err)
	}
	if out != out2 {
		t.Fatalf("edge instance naming is not stable across recompiles:\n--- first ---\n%s\n--- second ---\n%s", out, out2)
	}
}

// ldHost is a minimal ir.Host that lets a test drive several scans and
// watch retained state (here, an edge trigger's one-shot pulse) evolve.
type ldHost struct{ vals map[string]ir.Value }

func (h *ldHost) ReadGlobal(name string) (ir.Value, error) {
	if v, ok := h.vals[name]; ok {
		return v, nil
	}
	return ir.Value{}, nil
}
func (h *ldHost) WriteGlobal(name string, v ir.Value) error { h.vals[name] = v; return nil }
func (h *ldHost) NowMs() int64                              { return 0 }

// A rising-edge contact fires for exactly one scan on 0->1, a falling-edge
// contact for exactly one scan on 1->0, and holding the input steady fires
// neither again.
func TestEdgeContactOneShot(t *testing.T) {
	src := `PROGRAM p
VAR_EXTERNAL A : BOOL; Rise : BOOL; Fall : BOOL; END_VAR
LD
  RUNG rise
    +A ( Rise )
  RUNG fall
    -A ( Fall )
END_LD
END_PROGRAM`
	out, err := Transpile(src)
	if err != nil {
		t.Fatal(err)
	}
	prog, err := fbd.Compile(out)
	if err != nil {
		t.Fatalf("compile: %v\n%s", err, out)
	}
	frame := ir.NewFrame(prog)
	h := &ldHost{vals: map[string]ir.Value{"A": ir.BoolVal(false)}}

	scan := func(a bool) (rise, fall bool) {
		h.vals["A"] = ir.BoolVal(a)
		if err := ir.Run(prog, frame, h); err != nil {
			t.Fatalf("run: %v", err)
		}
		return h.vals["Rise"].B, h.vals["Fall"].B
	}

	if r, f := scan(false); r || f {
		t.Fatalf("scan1 (A=false, no transition): rise=%v fall=%v, want false,false", r, f)
	}
	if r, f := scan(true); !r || f {
		t.Fatalf("scan2 (A rises): rise=%v fall=%v, want true,false", r, f)
	}
	if r, f := scan(true); r || f {
		t.Fatalf("scan3 (A held true): rise=%v fall=%v, want false,false (one-shot)", r, f)
	}
	if r, f := scan(false); r || !f {
		t.Fatalf("scan4 (A falls): rise=%v fall=%v, want false,true", r, f)
	}
	if r, f := scan(false); r || f {
		t.Fatalf("scan5 (A held false): rise=%v fall=%v, want false,false (one-shot)", r, f)
	}
}

// The edge-trigger instances must show up as R_TRIG/F_TRIG in the ST the
// FBD stage lowers to — not some hand-rolled equivalent.
func TestEdgeContactLowersToST(t *testing.T) {
	src := `PROGRAM p
VAR_EXTERNAL A : BOOL; Rise : BOOL; END_VAR
LD
  RUNG rise
    +A ( Rise )
END_LD
END_PROGRAM`
	fbdSrc, err := Transpile(src)
	if err != nil {
		t.Fatal(err)
	}
	st, err := fbd.Transpile(fbdSrc)
	if err != nil {
		t.Fatalf("fbd -> st: %v\n%s", err, fbdSrc)
	}
	if !strings.Contains(st, "rt_rise_A : R_TRIG;") {
		t.Errorf("ST must declare the implicit R_TRIG instance:\n%s", st)
	}
	if !strings.Contains(st, "R_TRIG") {
		t.Errorf("ST must show the R_TRIG call:\n%s", st)
	}
}

// ── negated function contacts ────────────────────────────────────────────────

// `/` negates any BOOL-yielding contact term, not just a plain reference:
// a function call (/GT(a, b)) and an accessor chain (/t1.Q) both negate.
func TestNegatedFunctionContact(t *testing.T) {
	src := `PROGRAM p
VAR_EXTERNAL A : REAL; B : REAL; Ok : BOOL; NotDone : BOOL; END_VAR
LD
  RUNG cmp
    /GT(A, B) ( Ok )
  RUNG fb
    t1:TON(PT := T#1S) /t1.Q ( NotDone )
END_LD
END_PROGRAM`
	out, err := Transpile(src)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Ok := NOT GT(A, B)") {
		t.Errorf("negated function contact wrong:\n%s", out)
	}
	if !strings.Contains(out, "NotDone := AND(t1.Q, NOT t1.Q)") {
		t.Errorf("negated accessor-chain contact wrong:\n%s", out)
	}
	if _, err := fbd.Compile(out); err != nil {
		t.Fatalf("does not compile: %v\n%s", err, out)
	}
}
