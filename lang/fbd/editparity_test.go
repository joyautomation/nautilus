package fbd

import (
	"strings"
	"testing"
)

const paritySrc = `PROGRAM Main
VAR_EXTERNAL
  Start : BOOL;
  Run : BOOL;
  Hot : BOOL;
END_VAR
FBD
  // pump seal-in
  // keeps running until stop
  seal = OR(Start, Run)
  Run := seal
  hot = GT(TempC, 62.0)
  Hot := hot
END_FBD
END_PROGRAM`

func TestCommentsInModel(t *testing.T) {
	m, err := Graph(paritySrc)
	if err != nil {
		t.Fatal(err)
	}
	var cms []*Node
	for _, n := range m.Nodes {
		if n.Kind == "comment" {
			cms = append(cms, n)
		}
	}
	if len(cms) != 1 {
		t.Fatalf("want 1 comment node, got %d", len(cms))
	}
	if cms[0].ID != "cm:0" || cms[0].Label != "pump seal-in\nkeeps running until stop" || cms[0].Line != 8 {
		t.Errorf("comment node wrong: %+v", cms[0])
	}
}

func TestEditSetComment(t *testing.T) {
	out := apply(t, paritySrc, mustOp(t, paritySrc, EditOp{Type: "setComment", Node: "cm:0", Text: "P-101 latch"}))
	if !strings.Contains(out, "// P-101 latch\n") || strings.Contains(out, "pump seal-in") {
		t.Errorf("comment not replaced:\n%s", out)
	}
	// Empty text deletes the run.
	out = apply(t, paritySrc, mustOp(t, paritySrc, EditOp{Type: "setComment", Node: "cm:0", Text: "  "}))
	if strings.Contains(out, "pump seal-in") {
		t.Errorf("comment not deleted:\n%s", out)
	}
	// deleteNode works on comments too.
	out = apply(t, paritySrc, mustOp(t, paritySrc, EditOp{Type: "deleteNode", Node: "cm:0"}))
	if strings.Contains(out, "keeps running") {
		t.Errorf("comment not deleted via deleteNode:\n%s", out)
	}
}

// A comment run touching END_FBD — exactly where the palette inserts new
// notes — must flush with its own last line, not end-of-file: the regression
// had delete/rewrite of a trailing note take END_FBD and END_PROGRAM with it.
func TestEditTrailingComment(t *testing.T) {
	src := `PROGRAM Main
VAR_EXTERNAL
  Start : BOOL;
  Run : BOOL;
END_VAR
FBD
  seal = OR(Start, Run)
  Run := seal
  // trailing note
END_FBD
END_PROGRAM`

	// cm:0 must span only its own line.
	out := apply(t, src, mustOp(t, src, EditOp{Type: "deleteNode", Node: "cm:0"}))
	if strings.Contains(out, "trailing note") {
		t.Errorf("comment not deleted:\n%s", out)
	}
	for _, keep := range []string{"Run := seal", "END_FBD", "END_PROGRAM"} {
		if !strings.Contains(out, keep) {
			t.Fatalf("deleting a trailing comment must not remove %q:\n%s", keep, out)
		}
	}
	if _, err := Graph(out); err != nil {
		t.Fatalf("source no longer parses after delete: %v\n%s", err, out)
	}

	// Rewriting shares the run span — it must not swallow END_FBD either.
	out = apply(t, src, mustOp(t, src, EditOp{Type: "setComment", Node: "cm:0", Text: "kept note"}))
	if !strings.Contains(out, "// kept note\n") || strings.Contains(out, "trailing note") {
		t.Errorf("comment not replaced:\n%s", out)
	}
	if !strings.Contains(out, "END_FBD") || !strings.Contains(out, "END_PROGRAM") {
		t.Fatalf("rewriting a trailing comment must keep END_FBD/END_PROGRAM:\n%s", out)
	}
	if _, err := Graph(out); err != nil {
		t.Fatalf("source no longer parses after rewrite: %v\n%s", err, out)
	}
}

func TestEditInsertComment(t *testing.T) {
	// insertStatement accepts pure comment text (lexer-invisible, so the
	// fragment parses as an empty netlist).
	out := apply(t, paritySrc, mustOp(t, paritySrc, EditOp{Type: "insertStatement", Text: "// alarm section"}))
	if !strings.Contains(out, "// alarm section\n") {
		t.Errorf("comment not inserted:\n%s", out)
	}
	m, err := Graph(out)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, n := range m.Nodes {
		if n.Kind == "comment" && n.Label == "alarm section" {
			found = true
		}
	}
	if !found {
		t.Error("inserted comment missing from model")
	}
}

func TestEditDeleteVar(t *testing.T) {
	out := apply(t, paritySrc, mustOp(t, paritySrc, EditOp{Type: "deleteVar", NewName: "Hot"}))
	if strings.Contains(out, "Hot : BOOL") {
		t.Errorf("declaration not deleted:\n%s", out)
	}
	// The netlist still writes Hot — deliberately allowed (diagnostics flag it).
	if !strings.Contains(out, "Hot := hot") {
		t.Errorf("netlist must be untouched:\n%s", out)
	}
	if _, err := ApplyEdit(paritySrc, EditOp{Type: "deleteVar", NewName: "Nope"}); err == nil {
		t.Error("unknown declaration must error")
	}
}

func TestEditDuplicate(t *testing.T) {
	out := apply(t, paritySrc, mustOp(t, paritySrc, EditOp{
		Type: "duplicate", Nodes: []string{"b:w.seal", "c:Run"},
	}))
	// The latch's feedback read of its own coil follows the rename — the
	// copy is a self-consistent loop, not a tap into the original — while
	// the out-of-selection tag (Start) severs to an open `_` pin: a paste
	// must never quietly read the original's tags.
	if !strings.Contains(out, "seal_copy = OR(_, Run_copy)") {
		t.Errorf("wire copy missing/unrenamed/unsevered:\n%s", out)
	}
	// The copied coil renames its target AND follows the copied wire.
	if !strings.Contains(out, "Run_copy := seal_copy") {
		t.Errorf("coil copy must follow renamed wire:\n%s", out)
	}
	// Original statements intact.
	if !strings.Contains(out, "seal = OR(Start, Run)") || !strings.Contains(out, "Run := seal") {
		t.Errorf("originals disturbed:\n%s", out)
	}
	// The result still parses and models.
	if _, err := Graph(out); err != nil {
		t.Fatalf("duplicated source no longer graphs: %v", err)
	}
}

// Copy severs wiring but keeps configuration: variable/wire/pin references
// outside the selection open as `_` pins; literals (thresholds, time
// presets) ride along.
func TestEditDuplicateSevers(t *testing.T) {
	src := `PROGRAM Main
VAR_EXTERNAL
  TempC : REAL; Started : BOOL;
END_VAR
FBD
  cold = LT(TempC, 62)
  t1 : TON(IN := cold, PT := T#10S)
  Started := t1.Q
END_FBD
END_PROGRAM`

	// A lone block: tag severs, literal threshold stays.
	out := apply(t, src, mustOp(t, src, EditOp{Type: "duplicate", Nodes: []string{"b:w.cold"}}))
	if !strings.Contains(out, "cold_copy = LT(_, 62)") {
		t.Errorf("single block copy must sever the tag and keep the literal:\n%s", out)
	}
	if _, err := Graph(out); err != nil {
		t.Fatalf("copy no longer graphs: %v\n%s", err, out)
	}

	// A lone FB instance: wire input severs, time literal stays.
	out = apply(t, src, mustOp(t, src, EditOp{Type: "duplicate", Nodes: []string{"f:t1"}}))
	if !strings.Contains(out, "t1_copy : TON(IN := _, PT := T#10S)") {
		t.Errorf("FB copy must sever the wire and keep the preset:\n%s", out)
	}

	// A lone coil reading an FB pin: the inst.pin read severs whole.
	out = apply(t, src, mustOp(t, src, EditOp{Type: "duplicate", Nodes: []string{"c:Started"}}))
	if !strings.Contains(out, "Started_copy := _") {
		t.Errorf("coil copy must sever the pin read:\n%s", out)
	}

	// Copied together, the FB keeps feeding the copied coil.
	out = apply(t, src, mustOp(t, src, EditOp{Type: "duplicate", Nodes: []string{"f:t1", "c:Started"}}))
	if !strings.Contains(out, "Started_copy := t1_copy.Q") {
		t.Errorf("intra-selection pin read must follow the rename:\n%s", out)
	}
	if _, err := Graph(out); err != nil {
		t.Fatalf("copy no longer graphs: %v\n%s", err, out)
	}
}

func TestGhostLifecycle(t *testing.T) {
	// Place a ghost input and a ghost output via setLayout.
	x1, y1 := 10, 20
	out := apply(t, paritySrc, mustOp(t, paritySrc, EditOp{Type: "setLayout", Entries: []LayoutOpEntry{
		{Node: "g:in.TempSP", X: x1, Y: y1},
		{Node: "g:out.Alarm", X: 30, Y: 40},
	}}))
	m, err := Graph(out)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]*Node{}
	for _, n := range m.Nodes {
		byID[n.ID] = n
	}
	gin, gout := byID["g:in.TempSP"], byID["g:out.Alarm"]
	if gin == nil || !gin.Ghost || gin.Kind != "input" || gin.X == nil || *gin.X != x1 {
		t.Fatalf("ghost input wrong: %+v", gin)
	}
	if gout == nil || !gout.Ghost || gout.Kind != "coil" {
		t.Fatalf("ghost output wrong: %+v", gout)
	}

	// Wiring FROM the ghost input rewires the arg and converts the entry.
	out2 := apply(t, out, mustOp(t, out, EditOp{
		Type: "rewire", To: "b:w.hot", ToPin: "IN1", From: "v:TempC", Source: "g:in.TempSP",
	}))
	if !strings.Contains(out2, "GT(TempSP, 62.0)") {
		t.Errorf("ghost input not wired:\n%s", out2)
	}
	if strings.Contains(out2, "g:in.TempSP") || !strings.Contains(out2, "v:TempSP") {
		t.Errorf("ghost entry must convert to the real chip id:\n%s", out2)
	}

	// Wiring INTO the ghost output writes its coil statement.
	out3 := apply(t, out, mustOp(t, out, EditOp{
		Type: "rewire", To: "g:out.Alarm", Source: "b:w.hot",
	}))
	if !strings.Contains(out3, "Alarm := hot") {
		t.Errorf("ghost output not realized:\n%s", out3)
	}
	if strings.Contains(out3, "g:out.Alarm") || !strings.Contains(out3, "c:Alarm") {
		t.Errorf("ghost entry must convert to the coil id:\n%s", out3)
	}

	// Deleting a ghost drops the entry.
	out4 := apply(t, out, mustOp(t, out, EditOp{Type: "deleteNode", Node: "g:in.TempSP"}))
	if strings.Contains(out4, "g:in.TempSP") {
		t.Errorf("ghost entry not dropped:\n%s", out4)
	}
}

// A compact header — several declarations on one line, or a whole section
// on one line — lists every variable (it used to list only the first) and
// deletes just the one declaration.
func TestCompactHeaderVars(t *testing.T) {
	src := "PROGRAM P\nVAR A : BOOL; B : BOOL; END_VAR\nVAR_EXTERNAL\n  C : INT; D : INT;\nEND_VAR\nFBD\n  B := AND(A, TRUE);\nEND_FBD\nEND_PROGRAM\n"
	m, err := Graph(src)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, v := range m.Vars {
		got = append(got, v.Section+":"+v.Name)
	}
	if strings.Join(got, ",") != "VAR:A,VAR:B,VAR_EXTERNAL:C,VAR_EXTERNAL:D" {
		t.Fatalf("vars = %v", got)
	}
	out := apply(t, src, mustOp(t, src, EditOp{Type: "deleteVar", NewName: "A"}))
	if !strings.Contains(out, "VAR B : BOOL; END_VAR") {
		t.Fatalf("compact delete:\n%s", out)
	}
	out = apply(t, out, mustOp(t, out, EditOp{Type: "deleteVar", NewName: "D"}))
	if !strings.Contains(out, "  C : INT;\n") {
		t.Fatalf("second-on-line delete:\n%s", out)
	}
	if _, err := ApplyEdit(src, EditOp{Type: "declareVar", NewName: "b", Value: "BOOL"}); err == nil {
		t.Error("a compact-line duplicate must be refused")
	}
}

// Cut then paste: the copies come from the snapshot taken at cut time, keep
// their names (free again once the originals are gone) and, with KeepRefs,
// their wiring — a move, not a severed copy.
func TestEditPasteFromSnapshot(t *testing.T) {
	nodes := []string{"b:w.seal", "c:Run"}
	cut := apply(t, paritySrc, mustOp(t, paritySrc, EditOp{Type: "deleteNode", Nodes: nodes}))
	if strings.Contains(cut, "seal = OR") {
		t.Fatalf("cut left the statement:\n%s", cut)
	}
	out := apply(t, cut, mustOp(t, cut, EditOp{Type: "duplicate", Nodes: nodes, Text: paritySrc, KeepRefs: true}))
	if !strings.Contains(out, "seal = OR(Start, Run)") || !strings.Contains(out, "Run := seal") {
		t.Fatalf("cut+paste must restore the statements, names and wiring intact:\n%s", out)
	}
	if strings.Index(out, "Run := seal") > strings.Index(out, "END_FBD") {
		t.Fatalf("paste must land inside the FBD block:\n%s", out)
	}
	if _, err := Graph(out); err != nil {
		t.Fatalf("pasted source no longer graphs: %v\n%s", err, out)
	}

	// A plain copy pasted into a file that still has the originals: fresh
	// names, out-of-selection refs severed — the duplicate rules.
	out = apply(t, paritySrc, mustOp(t, paritySrc, EditOp{Type: "duplicate", Nodes: nodes, Text: paritySrc}))
	if !strings.Contains(out, "seal_copy = OR(_, Run_copy)") || !strings.Contains(out, "Run_copy := seal_copy") {
		t.Fatalf("snapshot copy into the same file:\n%s", out)
	}

	// Into another file: names that don't collide there are kept.
	other := "PROGRAM Other\nVAR_EXTERNAL\n  X : BOOL;\nEND_VAR\nFBD\n  X := TRUE\nEND_FBD\nEND_PROGRAM\n"
	out = apply(t, other, mustOp(t, other, EditOp{Type: "duplicate", Nodes: []string{"b:w.hot"}, Text: paritySrc}))
	if !strings.Contains(out, "hot = GT(_, 62.0)") {
		t.Fatalf("cross-file paste:\n%s", out)
	}
	if _, err := ApplyEdit(other, EditOp{Type: "duplicate", Nodes: []string{"b:w.nope"}, Text: paritySrc}); err == nil {
		t.Error("ids that resolve to nothing in the snapshot must error")
	}
}
