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
	// copy is a self-consistent loop, not a tap into the original.
	if !strings.Contains(out, "seal_copy = OR(Start, Run_copy)") {
		t.Errorf("wire copy missing/unrenamed:\n%s", out)
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
