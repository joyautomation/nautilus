package sfc

import (
	"strings"
	"testing"
)

// The SFC editor parity batch (issues #76, #180, #181, #186): the edit ops
// behind the chart's keyboard/name/reorder/vars gestures.


// #186: "+ action" is drawn under the last row, so an association posted
// without an index lands LAST — typed in order, read back in order.
func TestOpAddAssocAppendsWithoutIndex(t *testing.T) {
	src := baseline
	for _, tg := range []string{"First", "Second", "Third"} {
		src, _ = applyOp(t, src, EditOp{Type: "addAssoc", Step: "st:B", Qualifier: "N", Target: tg})
	}
	b := findStepT(t, mustGraph(t, src), "st:B")
	var got []string
	for _, a := range b.Actions {
		got = append(got, a.Target)
	}
	if strings.Join(got, ",") != "Lamp,First,Second,Third" {
		t.Fatalf("B's associations = %v, want them in the order they were added", got)
	}
	// An explicit index 0 still inserts first.
	_, m := applyOp(t, src, EditOp{Type: "addAssoc", Step: "st:B", Qualifier: "N", Target: "Zero", Index: ip(0)})
	if a := findStepT(t, m, "st:B").Actions[0]; a.Target != "Zero" {
		t.Fatalf("index 0: first association = %+v", a)
	}
	// setAssoc/deleteAssoc without an index still address row 0.
	_, m = applyOp(t, src, EditOp{Type: "deleteAssoc", Step: "st:B"})
	if a := findStepT(t, m, "st:B").Actions[0]; a.Target != "First" {
		t.Fatalf("deleteAssoc with no index: first association = %+v", a)
	}
}

// #76: a chained "+ step" can name its transition; renameTransition names,
// renames and un-names one, its layout pin following it.
func TestOpTransitionNames(t *testing.T) {
	out, m := applyOp(t, baseline, EditOp{Type: "addStep", Name: "C", From: []string{"B"}, Cond: "X", TransName: "T_BC"})
	tr := findTransT(t, m, "tr:T_BC")
	if tr.From[0] != "B" || tr.To[0] != "C" {
		t.Fatalf("T_BC = %+v", tr)
	}
	if !strings.Contains(out, "TRANSITION T_BC FROM B TO C := X;") {
		t.Fatalf("chained transition not named:\n%s", out)
	}

	// Name an unnamed one (the first, FROM A TO B).
	g := mustGraph(t, baseline)
	first := g.Trans[0].ID
	pinned, _ := applyOp(t, baseline, EditOp{Type: "setLayout", Node: first, X: ip(10), Y: ip(20)})
	out, m = applyOp(t, pinned, EditOp{Type: "renameTransition", Transition: first, NewName: "T_go"})
	if !strings.Contains(out, "TRANSITION T_go FROM A TO B := X;") {
		t.Fatalf("not named:\n%s", out)
	}
	if p, ok := m.Layout["tr:T_go"]; !ok || p.X != 10 {
		t.Fatalf("the pin did not follow the rename: %+v", m.Layout)
	}
	// Rename, then clear.
	out, _ = applyOp(t, out, EditOp{Type: "renameTransition", Transition: "tr:T_go", NewName: "T_start"})
	if !strings.Contains(out, "TRANSITION T_start FROM A TO B") {
		t.Fatalf("not renamed:\n%s", out)
	}
	out, m = applyOp(t, out, EditOp{Type: "renameTransition", Transition: "tr:T_start", NewName: ""})
	if !strings.Contains(out, "TRANSITION FROM A TO B := X;") || m.Trans[0].Name != "" {
		t.Fatalf("not cleared:\n%s", out)
	}

	named, _ := applyOp(t, baseline, EditOp{Type: "renameTransition", Transition: first, NewName: "T1"})
	second := mustGraph(t, named).Trans[1].ID
	wantOpErr(t, named, EditOp{Type: "renameTransition", Transition: second, NewName: "t1"}) // taken (case-insensitive)
	wantOpErr(t, named, EditOp{Type: "renameTransition", Transition: second, NewName: "not valid"})
	wantOpErr(t, baseline, EditOp{Type: "addStep", Name: "C", From: []string{"B"}, TransName: "bad name"})
}

// #181: moveTransition reorders alternative branches — priority is
// declaration order, so moving the abort first gives it priority.
func TestOpMoveTransition(t *testing.T) {
	// "+ alt branch" lands last: t_full first, the abort second.
	src := strings.Replace(workedExample,
		"  TRANSITION t_abort FROM Fill TO Idle := Abort;\n  END_TRANSITION\n  TRANSITION t_full  FROM Fill TO (Heat, Mix) := Level >= FillSP;   (* simultaneous divergence *)\n  END_TRANSITION\n",
		"  TRANSITION t_full  FROM Fill TO (Heat, Mix) := Level >= FillSP;   (* simultaneous divergence *)\n  END_TRANSITION\n  TRANSITION t_abort FROM Fill TO Idle := Abort;\n  END_TRANSITION\n", 1)
	if src == workedExample {
		t.Fatal("fixture did not swap")
	}
	order := func(m *Model) string {
		var s []string
		for _, tr := range m.Trans {
			s = append(s, tr.ID)
		}
		return strings.Join(s, ",")
	}
	out, m := applyOp(t, src, EditOp{Type: "moveTransition", Transition: "tr:t_abort", Delta: -1})
	if got := order(m); got != "tr:t_start,tr:t_abort,tr:t_full,tr:t_done,tr:t_empty" {
		t.Fatalf("order = %s\n%s", got, out)
	}
	// The trailing comment travels with its transition; nothing else moved.
	if !strings.Contains(out, "TRANSITION t_full  FROM Fill TO (Heat, Mix) := Level >= FillSP;   (* simultaneous divergence *)") {
		t.Fatalf("t_full's line changed:\n%s", out)
	}
	if out != workedExample {
		t.Fatalf("moving the abort up should restore the worked example byte for byte:\n%s", out)
	}
	noErrors(t, out)

	// Already first: a no-op, not an error.
	if edits, err := ApplyEdit(out, EditOp{Type: "moveTransition", Transition: "tr:t_abort", Delta: -1}); err != nil || len(edits) != 0 {
		t.Fatalf("moving the first branch earlier: %v %v", edits, err)
	}
	// And back down.
	_, m = applyOp(t, out, EditOp{Type: "moveTransition", Transition: "tr:t_abort", Delta: 1})
	if got := order(m); got != "tr:t_start,tr:t_full,tr:t_abort,tr:t_done,tr:t_empty" {
		t.Fatalf("order after +1 = %s", got)
	}

	// Unnamed transitions of different lengths swap cleanly; a group of one
	// has nothing to trade places with.
	multi := strings.Replace(baseline, "TRANSITION FROM B TO A := X;\nEND_TRANSITION\n",
		"TRANSITION FROM B TO A := X;\nEND_TRANSITION\nTRANSITION FROM B TO B\n  := NOT X;\nEND_TRANSITION\n", 1)
	g := mustGraph(t, multi)
	out, m = applyOp(t, multi, EditOp{Type: "moveTransition", Transition: g.Trans[2].ID, Delta: -1})
	if m.Trans[1].Cond != "NOT X" || m.Trans[2].Cond != "X" || m.Trans[0].To[0] != "B" {
		t.Fatalf("unnamed swap:\n%s", out)
	}
	wantOpErr(t, baseline, EditOp{Type: "moveTransition", Transition: mustGraph(t, baseline).Trans[0].ID, Delta: 1})
	wantOpErr(t, workedExample, EditOp{Type: "moveTransition", Transition: "tr:t_abort", Delta: 2})
}

// #180: the vars panel declares constants and initial values.
func TestOpDeclareVarConstantAndInit(t *testing.T) {
	out, m := applyOp(t, baseline, EditOp{Type: "declareVar", Name: "tMaxFill", VarType: "TIME := T#60S", Section: "VAR CONSTANT"})
	if !strings.Contains(out, "VAR CONSTANT\n    tMaxFill : TIME := T#60S;\nEND_VAR\nSFC") {
		t.Fatalf("no CONSTANT section:\n%s", out)
	}
	v := m.Vars[len(m.Vars)-1]
	if v.Section != "VAR CONSTANT" || v.Init != "T#60S" || v.Type != "TIME" {
		t.Fatalf("tMaxFill = %+v", v)
	}
	noErrors(t, out)
	// A second constant joins that section, with its value in Init.
	out, m = applyOp(t, out, EditOp{Type: "declareVar", Name: "ST_FILL", VarType: "INT", Init: "1", Section: "VAR CONSTANT"})
	if !strings.Contains(out, "    tMaxFill : TIME := T#60S;\n    ST_FILL : INT := 1;\nEND_VAR") {
		t.Fatalf("second constant:\n%s", out)
	}
	// A plain VAR with an initial value goes to VAR, not the constants.
	out, m = applyOp(t, out, EditOp{Type: "declareVar", Name: "Count", VarType: "INT", Init: "5", Section: "VAR"})
	if !strings.Contains(out, "  Lamp : BOOL;\n    Count : INT := 5;\nEND_VAR") {
		t.Fatalf("VAR init:\n%s", out)
	}
	if got := varNames(m); got != "VAR:X:BOOL,VAR:Lamp:BOOL,VAR:Count:INT,VAR CONSTANT:tMaxFill:TIME,VAR CONSTANT:ST_FILL:INT" {
		t.Fatalf("vars = %s", got)
	}
	noErrors(t, out)

	wantOpErr(t, baseline, EditOp{Type: "declareVar", Name: "k", VarType: "INT", Section: "VAR CONSTANT"}) // a constant needs a value
	wantOpErr(t, baseline, EditOp{Type: "declareVar", Name: "k", VarType: "INT", Init: "1; X := 2", Section: "VAR"})
}

// #179: an empty .sfc is an empty chart, and "initialize" makes it a
// runnable one.
func TestBlankChartInitializes(t *testing.T) {
	m := mustGraph(t, "")
	if !m.Blank {
		t.Fatal("a 0-byte .sfc is not Blank")
	}
	out, _ := applyOp(t, "\n", EditOp{Type: "init", Pou: "washer"})
	noErrors(t, out)
}
