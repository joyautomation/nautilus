package sfc

import (
	"strings"
	"testing"
)

func ip(v int) *int { return &v }

func TestFreshName(t *testing.T) {
	taken := map[string]bool{"fill": true, "fill2": true, "step3": true, "step4": true}
	is := func(n string) bool { return taken[strings.ToLower(n)] }
	for in, want := range map[string]string{"Fill": "Fill3", "Step3": "Step5", "Heat": "Heat", "Fill2": "Fill3"} {
		if got := freshName(in, is); got != want {
			t.Errorf("freshName(%s) = %s, want %s", in, got, want)
		}
	}
}

func TestOpPasteSteps(t *testing.T) {
	// Copy Heat + Mix with the transition between... there is none, so copy
	// Fill + Heat and the t_full-like transition between them.
	op := EditOp{
		Type: "pasteSteps",
		Steps: []PasteStep{
			{Name: "Fill", Actions: []GAssoc{{Qualifier: "N", Target: "FillValve"}}, X: ip(300), Y: ip(40)},
			{Name: "Heat", Actions: []GAssoc{{Qualifier: "L", Target: "Heater", Time: "T#5S"}}, X: ip(300), Y: ip(160)},
		},
		Trans: []PasteTrans{
			{Name: "t_full", From: []string{"Fill"}, To: []string{"Heat"}, Cond: "Level >= FillSP"},
			{From: []string{"Fill"}, To: []string{"Elsewhere"}, Cond: "TRUE"}, // an end outside: dropped
		},
	}
	out, m := applyOp(t, workedExample, op)
	fill2 := findStepT(t, m, "st:Fill2")
	if fill2.Initial || len(fill2.Actions) != 1 || fill2.Actions[0].Target != "FillValve" {
		t.Fatalf("Fill2 = %+v\n%s", fill2, out)
	}
	heat2 := findStepT(t, m, "st:Heat2")
	if heat2.Actions[0].Qualifier != "L" || heat2.Actions[0].Time != "T#5S" {
		t.Fatalf("Heat2 = %+v", heat2)
	}
	tr := findTransT(t, m, "tr:t_full2")
	if tr.From[0] != "Fill2" || tr.To[0] != "Heat2" || tr.Cond != "Level >= FillSP" {
		t.Fatalf("pasted transition = %+v", tr)
	}
	if strings.Contains(out, "Elsewhere") {
		t.Fatalf("a transition leaving the paste must not come along:\n%s", out)
	}
	if p, ok := m.Layout["st:Heat2"]; !ok || p.X != 300 || p.Y != 160 {
		t.Fatalf("layout pins = %+v", m.Layout)
	}
	// Originals untouched.
	if findStepT(t, m, "st:Fill").Actions[0].Target != "RunLamp" {
		t.Fatal("original Fill disturbed")
	}

	// After a cut the names are free again: the paste keeps them.
	cut, _ := applyOp(t, baseline, EditOp{Type: "deleteSelection", Nodes: []string{"st:B"}})
	_, m = applyOp(t, cut, EditOp{Type: "pasteSteps", Steps: []PasteStep{{Name: "B"}}})
	findStepT(t, m, "st:B")

	// An INITIAL step pastes as a plain one (a chart has exactly one).
	_, m = applyOp(t, baseline, EditOp{Type: "pasteSteps", Steps: []PasteStep{{Name: "A"}}})
	if s := findStepT(t, m, "st:A2"); s.Initial {
		t.Fatal("pasted copy of the initial step must not be initial")
	}

	wantOpErr(t, baseline, EditOp{Type: "pasteSteps"})
	wantOpErr(t, baseline, EditOp{Type: "pasteSteps", Steps: []PasteStep{{Name: "bad name"}}})
	wantOpErr(t, baseline, EditOp{Type: "pasteSteps", Steps: []PasteStep{{Name: "C", Actions: []GAssoc{{Qualifier: "N", Target: "x;y"}}}}})
}

func TestOpDeleteSelection(t *testing.T) {
	// Pin B first, so the delete must drop its pin too.
	pinned, _ := applyOp(t, baseline, EditOp{Type: "setLayout", Node: "st:B", X: ip(10), Y: ip(20)})
	unnamed := "tr:" // the B→A transition is unnamed: its id is its line
	var backID string
	_, m := applyOp(t, pinned, EditOp{Type: "setLayout", Node: "st:A", X: ip(0), Y: ip(0)})
	for _, tr := range m.Trans {
		if tr.From[0] == "B" && tr.To[0] == "A" {
			backID = tr.ID
		}
	}
	if !strings.HasPrefix(backID, unnamed) {
		t.Fatalf("no B→A transition: %+v", m.Trans)
	}
	edits, err := ApplyEdit(pinned, EditOp{Type: "deleteSelection", Nodes: []string{"st:B", backID}})
	if err != nil {
		t.Fatal(err)
	}
	if len(edits) != 1 {
		t.Fatalf("want one collapsed edit, got %d", len(edits))
	}
	out := applyTextEdits(pinned, edits)
	m2, err := Graph(out)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(m2.Steps) != 1 || len(m2.Trans) != 1 {
		t.Fatalf("after delete: steps %d trans %d\n%s", len(m2.Steps), len(m2.Trans), out)
	}
	if _, ok := m2.Layout["st:B"]; ok {
		t.Fatalf("B's pin must go with it:\n%s", out)
	}
	wantOpErr(t, baseline, EditOp{Type: "deleteSelection", Nodes: []string{"st:Nope"}})
	wantOpErr(t, baseline, EditOp{Type: "deleteSelection", Nodes: []string{"ac:X"}})
}

func TestCollapseEdit(t *testing.T) {
	for _, c := range [][2]string{
		{"a\nb\nc\n", "a\nX\nb\nc\n"},
		{"a\nb\nc\n", "a\nc\n"},
		{"a\nb", "a\nb\nc"},
		{"a\nb\n", "a\nb\n"},
		{"a", "b"},
	} {
		got := applyTextEdits(c[0], collapseEdit(c[0], c[1]))
		if got != c[1] {
			t.Errorf("collapse %q → %q gave %q", c[0], c[1], got)
		}
	}
}
