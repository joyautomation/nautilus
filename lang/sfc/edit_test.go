package sfc

import (
	"strconv"
	"strings"
	"testing"
)

// applyOp runs ApplyEdit, applies the resulting edits to src (using the
// same helper ApplyEdit's own never-block gate uses), and reparses the
// result — the parity-test shape: op -> text edit -> re-parse -> assert.
func applyOp(t *testing.T, src string, op EditOp) (result string, model *Model) {
	t.Helper()
	edits, err := ApplyEdit(src, op)
	if err != nil {
		t.Fatalf("ApplyEdit(%s): %v", op.Type, err)
	}
	result = applyTextEdits(src, edits)
	if _, err := Parse(result); err != nil {
		t.Fatalf("ApplyEdit(%s) produced unparseable text: %v\n%s", op.Type, err, result)
	}
	m, err := Graph(result)
	if err != nil {
		t.Fatalf("Graph after ApplyEdit(%s): %v\n%s", op.Type, err, result)
	}
	return result, m
}

// wantOpErr asserts ApplyEdit rejects op.
func wantOpErr(t *testing.T, src string, op EditOp) {
	t.Helper()
	if _, err := ApplyEdit(src, op); err == nil {
		t.Fatalf("ApplyEdit(%s) succeeded, want an error", op.Type)
	}
}

func findStepT(t *testing.T, m *Model, id string) GStep {
	t.Helper()
	s, err := findStep(m, id)
	if err != nil {
		t.Fatal(err)
	}
	return *s
}

func findTransT(t *testing.T, m *Model, id string) GTransition {
	t.Helper()
	tr, err := findTransition(m, id)
	if err != nil {
		t.Fatal(err)
	}
	return *tr
}

// ── addStep / deleteStep / renameStep ───────────────────────────────────

func TestOpAddStep(t *testing.T) {
	_, m := applyOp(t, baseline, EditOp{Type: "addStep", Name: "C", After: "st:B"})
	if len(m.Steps) != 3 {
		t.Fatalf("len(Steps) = %d, want 3", len(m.Steps))
	}
	s := findStepT(t, m, "st:C")
	if s.Initial || len(s.Actions) != 0 {
		t.Errorf("new step = %+v, want a blank non-initial step", s)
	}
	// Never-block: a step with no transitions yet is savable, but Check
	// flags it (both unreachable — no TO targets it — and dead-end).
	prog := mustParse(t, applyResult(t, baseline, EditOp{Type: "addStep", Name: "C", After: "st:B"}))
	diags := Check(prog)
	wantDiag(t, diags, SeverityWarning, `step "C" is unreachable`)
	wantDiag(t, diags, SeverityWarning, `step "C" is a dead end`)

	wantOpErr(t, baseline, EditOp{Type: "addStep", Name: "B"}) // duplicate name
	wantOpErr(t, baseline, EditOp{Type: "addStep", Name: "not valid!"})
	wantOpErr(t, baseline, EditOp{Type: "addStep", Name: "C", Initial: true}) // already has one
}

// applyResult is applyOp without the re-Graph, for tests that just want the
// resulting source text.
func applyResult(t *testing.T, src string, op EditOp) string {
	t.Helper()
	result, _ := applyOp(t, src, op)
	return result
}

func TestOpDeleteStep(t *testing.T) {
	result, m := applyOp(t, workedExample, EditOp{Type: "deleteStep", Step: "st:Drain"})
	if len(m.Steps) != 4 {
		t.Fatalf("len(Steps) = %d, want 4", len(m.Steps))
	}
	if _, err := findStep(m, "st:Drain"); err == nil {
		t.Fatal("Drain still present after deleteStep")
	}
	// Breadcrumb philosophy: t_done's TO and t_empty's FROM still say
	// "Drain" — dangling, not cascaded away — and Check flags them.
	tDone := findTransT(t, m, "tr:t_done")
	if !equalStrings(tDone.To, []string{"Drain"}) {
		t.Errorf("t_done.To = %v, want [Drain] (left dangling)", tDone.To)
	}
	prog := mustParse(t, result)
	diags := Check(prog)
	wantDiag(t, diags, SeverityError, `TO references unknown step "Drain"`)
	wantDiag(t, diags, SeverityError, `FROM references unknown step "Drain"`)

	wantOpErr(t, workedExample, EditOp{Type: "deleteStep", Step: "st:NoSuchStep"})
}

func TestOpRenameStep(t *testing.T) {
	result, m := applyOp(t, workedExample, EditOp{Type: "renameStep", Step: "st:Fill", NewName: "Filling"})
	if _, err := findStep(m, "st:Fill"); err == nil {
		t.Fatal("old name st:Fill still resolves after rename")
	}
	s := findStepT(t, m, "st:Filling")
	if s.Name != "Filling" || len(s.Actions) != 2 {
		t.Errorf("renamed step = %+v", s)
	}
	// Every transition whose FROM/TO named Fill now names Filling.
	if to := findTransT(t, m, "tr:t_start").To; !equalStrings(to, []string{"Filling"}) {
		t.Errorf("t_start.To = %v, want [Filling]", to)
	}
	if from := findTransT(t, m, "tr:t_abort").From; !equalStrings(from, []string{"Filling"}) {
		t.Errorf("t_abort.From = %v, want [Filling]", from)
	}
	if from := findTransT(t, m, "tr:t_full").From; !equalStrings(from, []string{"Filling"}) {
		t.Errorf("t_full.From = %v, want [Filling]", from)
	}
	// Transitions that never mentioned Fill are untouched, line-for-line.
	origLines := strings.Split(workedExample, "\n")
	newLines := strings.Split(result, "\n")
	tDoneOld := findTransT(t, mustGraph(t, workedExample), "tr:t_done")
	tDoneNew := findTransT(t, m, "tr:t_done")
	if tDoneOld.Line != tDoneNew.Line {
		t.Fatalf("t_done moved from line %d to %d — rename should only rewrite touched lines", tDoneOld.Line, tDoneNew.Line)
	}
	for l := tDoneOld.Line; l <= tDoneOld.EndLine; l++ {
		if origLines[l-1] != newLines[l-1] {
			t.Errorf("line %d changed by an unrelated rename:\n- %q\n+ %q", l, origLines[l-1], newLines[l-1])
		}
	}
	// The design doc is silent on rewriting Step.X/.T references inside ST
	// condition/action-body spans for a rename — this implementation's
	// chosen interpretation is: leave them, and let Check flag the
	// resulting unknown-step reference as a breadcrumb. None of the
	// workedExample's spans reference Fill.X/Fill.T, so this is a smoke
	// test that such text is untouched where it DOES appear (Mix.X in the
	// Stir action body must survive an unrelated Mix rename unchanged).
	result2, m2 := applyOp(t, workedExample, EditOp{Type: "renameStep", Step: "st:Mix", NewName: "Blend"})
	stir := findGAction(t, m2, "ac:Stir")
	if !strings.Contains(stir.Body, "Mix.X") {
		t.Errorf("Stir body lost its Mix.X reference after an unrelated (Step-only) rename: %q", stir.Body)
	}
	prog := mustParse(t, result2)
	// Blend.X is fine (Blend is the new step name); nothing should flag it.
	wantNoDiag(t, Check(prog), "Blend.X")

	wantOpErr(t, workedExample, EditOp{Type: "renameStep", Step: "st:Fill", NewName: "Heat"}) // taken
	wantOpErr(t, workedExample, EditOp{Type: "renameStep", Step: "st:Fill", NewName: "1bad"})
	wantOpErr(t, workedExample, EditOp{Type: "renameStep", Step: "st:NoSuch", NewName: "X"})
}

func findGAction(t *testing.T, m *Model, id string) GAction {
	t.Helper()
	a, err := findAction(m, id)
	if err != nil {
		t.Fatal(err)
	}
	return *a
}

// ── addTransition / deleteTransition / setCondition ─────────────────────

func TestOpAddTransition(t *testing.T) {
	result, m := applyOp(t, workedExample, EditOp{
		Type: "addTransition", Name: "t_extra", From: []string{"Drain"}, To: []string{"Idle"}, Cond: "Level <= 0.0",
	})
	if len(m.Trans) != 6 {
		t.Fatalf("len(Trans) = %d, want 6", len(m.Trans))
	}
	tr := findTransT(t, m, "tr:t_extra")
	if tr.Cond != "Level <= 0.0" || !equalStrings(tr.From, []string{"Drain"}) || !equalStrings(tr.To, []string{"Idle"}) {
		t.Errorf("new transition = %+v", tr)
	}
	// The five original transitions are untouched, byte for byte, at their
	// original line numbers (an append-only insert moves nothing above it).
	origLines := strings.Split(workedExample, "\n")
	newLines := strings.Split(result, "\n")
	for _, id := range []string{"tr:t_start", "tr:t_abort", "tr:t_full", "tr:t_done", "tr:t_empty"} {
		want := findTransT(t, mustGraph(t, workedExample), id)
		got := findTransT(t, m, id)
		if want.Line != got.Line {
			t.Fatalf("%s moved from line %d to %d", id, want.Line, got.Line)
		}
		for l := want.Line; l <= want.EndLine; l++ {
			if origLines[l-1] != newLines[l-1] {
				t.Errorf("%s line %d changed: %q vs %q", id, l, origLines[l-1], newLines[l-1])
			}
		}
	}
	// Never-block: an empty condition is syntactically legal (Check flags
	// it, ApplyEdit does not refuse).
	result2, _ := applyOp(t, workedExample, EditOp{Type: "addTransition", From: []string{"Drain"}, To: []string{"Idle"}})
	wantDiag(t, Check(mustParse(t, result2)), SeverityError, "empty condition")

	wantOpErr(t, workedExample, EditOp{Type: "addTransition", From: []string{"Drain"}, To: []string{"Idle"}, Cond: "a; b"}) // ';' in cond
	wantOpErr(t, workedExample, EditOp{Type: "addTransition", From: nil, To: []string{"Idle"}, Cond: "TRUE"})
	wantOpErr(t, workedExample, EditOp{Type: "addTransition", Name: "t_start", From: []string{"Drain"}, To: []string{"Idle"}, Cond: "TRUE"})
}

func TestOpDeleteTransition(t *testing.T) {
	// Deleting t_full removes Fill's only remaining competitor for
	// t_abort's source, so t_abort's Kind recomputes from alt to normal.
	_, m := applyOp(t, workedExample, EditOp{Type: "deleteTransition", Transition: "tr:t_full"})
	if len(m.Trans) != 4 {
		t.Fatalf("len(Trans) = %d, want 4", len(m.Trans))
	}
	if _, err := findTransition(m, "tr:t_full"); err == nil {
		t.Fatal("t_full still present")
	}
	if kind := findTransT(t, m, "tr:t_abort").Kind; kind != "normal" {
		t.Errorf("t_abort.Kind after removing its only alt sibling = %q, want normal", kind)
	}
	wantOpErr(t, workedExample, EditOp{Type: "deleteTransition", Transition: "tr:nope"})
}

func TestOpSetCondition(t *testing.T) {
	_, m := applyOp(t, workedExample, EditOp{Type: "setCondition", Transition: "tr:t_start", Cond: "Start"})
	if cond := findTransT(t, m, "tr:t_start").Cond; cond != "Start" {
		t.Errorf("t_start.Cond = %q, want Start", cond)
	}
	wantOpErr(t, workedExample, EditOp{Type: "setCondition", Transition: "tr:t_start", Cond: "a; b"})
}

// ── setTransitionEnds ─────────────────────────────────────────────────

func TestOpSetTransitionEnds(t *testing.T) {
	// Retarget FROM only — TO carries over unchanged.
	_, m := applyOp(t, workedExample, EditOp{Type: "setTransitionEnds", Transition: "tr:t_start", From: []string{"Drain"}})
	ts := findTransT(t, m, "tr:t_start")
	if !equalStrings(ts.From, []string{"Drain"}) || !equalStrings(ts.To, []string{"Fill"}) {
		t.Errorf("t_start = %+v, want From=[Drain] To=[Fill] (TO untouched)", ts)
	}

	// Retarget TO only — FROM carries over unchanged.
	_, m2 := applyOp(t, workedExample, EditOp{Type: "setTransitionEnds", Transition: "tr:t_start", To: []string{"Drain"}})
	ts2 := findTransT(t, m2, "tr:t_start")
	if !equalStrings(ts2.From, []string{"Idle"}) || !equalStrings(ts2.To, []string{"Drain"}) {
		t.Errorf("t_start = %+v, want From=[Idle] To=[Drain]", ts2)
	}

	// Retarget both at once.
	_, m3 := applyOp(t, workedExample, EditOp{Type: "setTransitionEnds", Transition: "tr:t_start", From: []string{"Heat"}, To: []string{"Mix"}})
	ts3 := findTransT(t, m3, "tr:t_start")
	if !equalStrings(ts3.From, []string{"Heat"}) || !equalStrings(ts3.To, []string{"Mix"}) {
		t.Errorf("t_start = %+v, want From=[Heat] To=[Mix]", ts3)
	}

	// Other transitions/steps are untouched, line-for-line.
	origLines := strings.Split(workedExample, "\n")
	result3, _ := applyOp(t, workedExample, EditOp{Type: "setTransitionEnds", Transition: "tr:t_start", From: []string{"Heat"}})
	newLines := strings.Split(result3, "\n")
	for l := 1; l < findTransT(t, mustGraph(t, workedExample), "tr:t_start").Line; l++ {
		if origLines[l-1] != newLines[l-1] {
			t.Errorf("line %d before the touched transition changed: %q vs %q", l, origLines[l-1], newLines[l-1])
		}
	}

	// Never-block: retargeting onto a name that doesn't (yet) exist as a
	// step is allowed — the same posture as addTransition/deleteStep's
	// dangling refs — Check flags it, ApplyEdit does not refuse it.
	result4, _ := applyOp(t, workedExample, EditOp{Type: "setTransitionEnds", Transition: "tr:t_start", From: []string{"Ghost"}})
	wantDiag(t, Check(mustParse(t, result4)), SeverityError, `FROM references unknown step "Ghost"`)

	// Fixing a dangling reference left by deleteStep: delete Drain (leaves
	// t_done's TO and t_empty's FROM dangling), then retarget both back onto
	// a real step — the result checks clean again for that transition.
	afterDelete := applyResult(t, workedExample, EditOp{Type: "deleteStep", Step: "st:Drain"})
	fixed := applyResult(t, afterDelete, EditOp{Type: "setTransitionEnds", Transition: "tr:t_done", To: []string{"Idle"}})
	fixed = applyResult(t, fixed, EditOp{Type: "setTransitionEnds", Transition: "tr:t_empty", From: []string{"Idle"}})
	diags := Check(mustParse(t, fixed))
	wantNoDiag(t, diags, `unknown step "Drain"`)

	wantOpErr(t, workedExample, EditOp{Type: "setTransitionEnds", Transition: "tr:nope", From: []string{"Idle"}})
	wantOpErr(t, workedExample, EditOp{Type: "setTransitionEnds", Transition: "tr:t_start", From: []string{"not valid!"}})
}

// ── addAssoc / setAssoc / deleteAssoc ───────────────────────────────────

func TestOpAssoc(t *testing.T) {
	result, m := applyOp(t, workedExample, EditOp{Type: "addAssoc", Step: "st:Heat", Qualifier: "s", Target: "AlarmLamp", Index: 2})
	heat := findStepT(t, m, "st:Heat")
	if len(heat.Actions) != 3 || heat.Actions[2].Qualifier != "S" || heat.Actions[2].Target != "AlarmLamp" {
		t.Fatalf("Heat.Actions = %+v", heat.Actions)
	}
	// Other steps' text is untouched.
	origLines := strings.Split(workedExample, "\n")
	newLines := strings.Split(result, "\n")
	idle := findTransT(t, m, "tr:t_start") // sentinel: line numbers below Heat are unaffected only if Heat's rewrite has the same or fewer lines as before... Heat gained a line, so lines AFTER Heat shift; check lines BEFORE Heat instead.
	_ = idle
	for l := 1; l < findStepT(t, mustGraph(t, workedExample), "st:Heat").Line; l++ {
		if origLines[l-1] != newLines[l-1] {
			t.Errorf("line %d before the touched step changed: %q vs %q", l, origLines[l-1], newLines[l-1])
		}
	}

	result2, m2 := applyOp(t, result, EditOp{Type: "setAssoc", Step: "st:Heat", Index: 2, Qualifier: "R", Target: "AlarmLamp"})
	heat2 := findStepT(t, m2, "st:Heat")
	if heat2.Actions[2].Qualifier != "R" {
		t.Fatalf("after setAssoc: %+v", heat2.Actions)
	}

	_, m3 := applyOp(t, result2, EditOp{Type: "deleteAssoc", Step: "st:Heat", Index: 2})
	heat3 := findStepT(t, m3, "st:Heat")
	if len(heat3.Actions) != 2 {
		t.Fatalf("after deleteAssoc: %+v", heat3.Actions)
	}

	wantOpErr(t, workedExample, EditOp{Type: "addAssoc", Step: "st:Heat", Qualifier: "not valid", Target: "X"})
	wantOpErr(t, workedExample, EditOp{Type: "deleteAssoc", Step: "st:Heat", Index: 99})
}

// ── setActionBody (create-or-update) ────────────────────────────────────

func TestOpSetActionBody(t *testing.T) {
	_, m := applyOp(t, workedExample, EditOp{Type: "setActionBody", Action: "ac:Stir", Body: "Mixer := FALSE;"})
	stir := findGAction(t, m, "ac:Stir")
	if strings.TrimSpace(stir.Body) != "Mixer := FALSE;" {
		t.Errorf("Stir.Body = %q", stir.Body)
	}

	// Creating a brand-new action (the assoc referencing it can be added
	// separately — never-block: an association naming an action that
	// doesn't exist YET is itself allowed too, see TestOpAssocForwardRef).
	result2, m2 := applyOp(t, workedExample, EditOp{Type: "setActionBody", Action: "Brand", Body: "X := TRUE;"})
	if len(m2.Actions) != 4 {
		t.Fatalf("len(Actions) = %d, want 4", len(m2.Actions))
	}
	brand := findGAction(t, m2, "ac:Brand")
	if strings.TrimSpace(brand.Body) != "X := TRUE;" {
		t.Errorf("Brand.Body = %q", brand.Body)
	}
	if _, err := Parse(result2); err != nil {
		t.Fatalf("result did not parse: %v", err)
	}
}

// TestOpAssocForwardRef: a step may reference an action/variable that
// doesn't exist yet — never-block, Check flags it, ApplyEdit does not.
func TestOpAssocForwardRef(t *testing.T) {
	result, _ := applyOp(t, baseline, EditOp{Type: "addAssoc", Step: "st:A", Qualifier: "N", Target: "NotYetDeclared"})
	wantDiag(t, Check(mustParse(t, result)), SeverityError, "references neither an ACTION block nor a declared variable")
}

// ── insertAlternativeBranch / insertSimultaneousBranch ──────────────────

func TestOpInsertAlternativeBranch(t *testing.T) {
	_, m := applyOp(t, workedExample, EditOp{
		Type: "insertAlternativeBranch", After: "tr:t_full", To: []string{"Drain"}, Cond: "Level > 999.0", Name: "t_overflow",
	})
	if len(m.Trans) != 6 {
		t.Fatalf("len(Trans) = %d, want 6", len(m.Trans))
	}
	nt := findTransT(t, m, "tr:t_overflow")
	if !equalStrings(nt.From, []string{"Fill"}) || nt.Kind != "alt" {
		t.Errorf("t_overflow = %+v, want From=[Fill] Kind=alt (inherited source from After)", nt)
	}
	// +2, not +1: a blank separator line precedes the inserted block (the
	// same "\n"-prefixed insertion style lang/ld's addRung uses).
	if nt.Line != findTransT(t, m, "tr:t_full").EndLine+2 {
		t.Errorf("t_overflow should be inserted immediately after t_full (priority = insertion order): got line %d, t_full ends at %d", nt.Line, findTransT(t, m, "tr:t_full").EndLine)
	}
	// t_abort/t_full stay alt/simDiverge; a 3-way alt group sharing "Fill"
	// forms — not flagged ambiguous here since all three pairwise share
	// the same single source (a clique, per checkAltGroups).
	if kind := findTransT(t, m, "tr:t_abort").Kind; kind != "alt" {
		t.Errorf("t_abort.Kind = %q, want alt", kind)
	}

	wantOpErr(t, workedExample, EditOp{Type: "insertAlternativeBranch", To: []string{"Drain"}, Cond: "TRUE"}) // no From or After
	wantOpErr(t, workedExample, EditOp{Type: "insertAlternativeBranch", After: "tr:t_full", To: nil, Cond: "TRUE"})
}

func TestOpInsertSimultaneousBranch(t *testing.T) {
	result, m := applyOp(t, workedExample, EditOp{Type: "insertSimultaneousBranch", Transition: "tr:t_start", NewStep: "Prime"})
	if len(m.Steps) != 6 {
		t.Fatalf("len(Steps) = %d, want 6", len(m.Steps))
	}
	prime := findStepT(t, m, "st:Prime")
	if prime.Initial || len(prime.Actions) != 0 {
		t.Errorf("Prime = %+v, want a blank non-initial step", prime)
	}
	ts := findTransT(t, m, "tr:t_start")
	if !equalStrings(ts.To, []string{"Fill", "Prime"}) || ts.Kind != "simDiverge" {
		t.Errorf("t_start = %+v, want To=[Fill Prime] Kind=simDiverge", ts)
	}
	if _, err := Parse(result); err != nil {
		t.Fatalf("result did not parse: %v", err)
	}
	// Never-block: Prime has no outgoing transition yet — savable, Check warns.
	wantDiag(t, Check(mustParse(t, result)), SeverityWarning, `step "Prime" is a dead end`)

	wantOpErr(t, workedExample, EditOp{Type: "insertSimultaneousBranch", Transition: "tr:t_start", NewStep: "Fill"}) // exists
	wantOpErr(t, workedExample, EditOp{Type: "insertSimultaneousBranch", Transition: "tr:t_full", NewStep: "Heat"})  // already a target
}

// A third leg on an existing divergence must join the convergence too —
// otherwise it is a dead end the join never waits for. And the edit stays
// minimal: t_full/t_done keep their comments, the step lands with its
// siblings.
func TestOpInsertSimultaneousBranchWidensJoin(t *testing.T) {
	result, m := applyOp(t, workedExample, EditOp{Type: "insertSimultaneousBranch", Transition: "tr:t_full", NewStep: "Sample"})
	if tf := findTransT(t, m, "tr:t_full"); !equalStrings(tf.To, []string{"Heat", "Mix", "Sample"}) {
		t.Errorf("t_full.To = %v, want [Heat Mix Sample]", tf.To)
	}
	if td := findTransT(t, m, "tr:t_done"); !equalStrings(td.From, []string{"Heat", "Mix", "Sample"}) {
		t.Errorf("t_done.From = %v, want [Heat Mix Sample] (the join must wait for the new leg)", td.From)
	}
	if sample, mix := findStepT(t, m, "st:Sample"), findStepT(t, m, "st:Mix"); sample.Line != mix.EndLine+2 {
		t.Errorf("Sample at line %d, want right after Mix (ends %d)", sample.Line, mix.EndLine)
	}
	if !strings.Contains(result, "TO (Heat, Mix, Sample) := Level >= FillSP;   (* simultaneous divergence *)") {
		t.Errorf("t_full's trailing comment was not preserved:\n%s", result)
	}
	for _, d := range Check(mustParse(t, result)) {
		if strings.Contains(d.Message, "dead end") {
			t.Errorf("unexpected diagnostic: %s", d.Message)
		}
	}
}

// ── layout ────────────────────────────────────────────────────────────

func TestOpLayout(t *testing.T) {
	x, y := 10, 20
	result, m := applyOp(t, workedExample, EditOp{Type: "setLayout", Node: "st:Idle", X: &x, Y: &y})
	if p, ok := m.Layout["st:Idle"]; !ok || p != (Point{X: 10, Y: 20}) {
		t.Fatalf("Layout[st:Idle] = %v, ok=%v", p, ok)
	}

	x2, y2 := 200, 5
	result2, m2 := applyOp(t, result, EditOp{Type: "setLayout", Entries: []LayoutOpEntry{
		{Node: "st:Fill", X: x2, Y: y2},
	}})
	if len(m2.Layout) != 2 {
		t.Fatalf("Layout = %v, want 2 entries after a batched pin", m2.Layout)
	}

	_, m3 := applyOp(t, result2, EditOp{Type: "clearLayout", Node: "st:Idle"})
	if _, ok := m3.Layout["st:Idle"]; ok {
		t.Fatal("st:Idle still pinned after clearLayout")
	}
	if _, ok := m3.Layout["st:Fill"]; !ok {
		t.Fatal("st:Fill should survive a single-node clearLayout")
	}

	_, m4 := applyOp(t, result2, EditOp{Type: "clearLayout"})
	if m4.Layout != nil {
		t.Fatalf("Layout = %v, want nil after clearing the whole block", m4.Layout)
	}

	wantOpErr(t, workedExample, EditOp{Type: "setLayout", Node: "st:NoSuch", X: &x, Y: &y})
}

// ── comments ──────────────────────────────────────────────────────────

const commentChart = `PROGRAM P
VAR
  X : BOOL;
  Lamp : BOOL;
END_VAR
SFC
// original note
INITIAL_STEP A:
  N Lamp;
END_STEP
STEP B:
  N Lamp;
END_STEP
TRANSITION FROM A TO B := X;
END_TRANSITION
TRANSITION FROM B TO A := X;
END_TRANSITION
END_SFC
END_PROGRAM
`

func TestOpSetComment(t *testing.T) {
	zero := 0
	_, m := applyOp(t, commentChart, EditOp{Type: "setComment", Comment: &zero, Text: "updated note"})
	if len(m.Comments) != 1 || m.Comments[0].Text != "updated note" {
		t.Fatalf("Comments = %v", m.Comments)
	}
	_, m2 := applyOp(t, commentChart, EditOp{Type: "setComment", Comment: &zero, Text: ""})
	if len(m2.Comments) != 0 {
		t.Fatalf("Comments = %v, want none after an empty-text delete", m2.Comments)
	}
	bad := 5
	wantOpErr(t, commentChart, EditOp{Type: "setComment", Comment: &bad, Text: "x"})
}

func TestOpAddComment(t *testing.T) {
	result, m := applyOp(t, commentChart, EditOp{Type: "addComment", Text: "a fresh note"})
	if len(m.Comments) != 2 {
		t.Fatalf("len(Comments) = %d, want 2", len(m.Comments))
	}
	if m.Comments[0].Text != "original note" || m.Comments[1].Text != "a fresh note" {
		t.Fatalf("Comments = %+v", m.Comments)
	}
	if !strings.Contains(result, "// a fresh note") {
		t.Fatalf("result missing the new note:\n%s", result)
	}
	wantOpErr(t, commentChart, EditOp{Type: "addComment", Text: "   "}) // nothing to insert
	wantOpErr(t, commentChart, EditOp{Type: "addComment"})
}

func TestOpDeleteComment(t *testing.T) {
	zero := 0
	_, m := applyOp(t, commentChart, EditOp{Type: "deleteComment", Comment: &zero})
	if len(m.Comments) != 0 {
		t.Fatalf("Comments = %v, want none", m.Comments)
	}
	bad := 5
	wantOpErr(t, commentChart, EditOp{Type: "deleteComment", Comment: &bad})
	wantOpErr(t, commentChart, EditOp{Type: "deleteComment"}) // nil index
}

// ── add+delete round trips (guards the blank-line regression: an add* op
// prefixes its insertion with a blank separator line, matching the file's
// own hand-written style — see workedExample's blank line before every
// STEP/TRANSITION — so the matching delete must consume that same blank
// back, or repeated add/delete cycles accumulate stray blanks) ──────────

func roundTrip(t *testing.T, src string, add, del EditOp) string {
	t.Helper()
	addEdits, err := ApplyEdit(src, add)
	if err != nil {
		t.Fatalf("ApplyEdit(%s): %v", add.Type, err)
	}
	added := applyTextEdits(src, addEdits)
	delEdits, err := ApplyEdit(added, del)
	if err != nil {
		t.Fatalf("ApplyEdit(%s): %v", del.Type, err)
	}
	return applyTextEdits(added, delEdits)
}

func TestRoundTripAddDeleteStep(t *testing.T) {
	got := roundTrip(t, workedExample,
		EditOp{Type: "addStep", Name: "Extra"},
		EditOp{Type: "deleteStep", Step: "st:Extra"})
	if got != workedExample {
		t.Fatalf("add+delete step not byte-identical:\n--- want ---\n%q\n--- got ---\n%q", workedExample, got)
	}
}

func TestRoundTripAddDeleteTransition(t *testing.T) {
	got := roundTrip(t, workedExample,
		EditOp{Type: "addTransition", Name: "t_extra", From: []string{"Drain"}, To: []string{"Idle"}, Cond: "TRUE"},
		EditOp{Type: "deleteTransition", Transition: "tr:t_extra"})
	if got != workedExample {
		t.Fatalf("add+delete transition not byte-identical:\n--- want ---\n%q\n--- got ---\n%q", workedExample, got)
	}
}

func TestRoundTripAddDeleteComment(t *testing.T) {
	addEdits, err := ApplyEdit(workedExample, EditOp{Type: "addComment", Text: "scratch note"})
	if err != nil {
		t.Fatal(err)
	}
	added := applyTextEdits(workedExample, addEdits)
	m, err := Graph(added)
	if err != nil {
		t.Fatal(err)
	}
	idx := len(m.Comments) - 1
	delEdits, err := ApplyEdit(added, EditOp{Type: "deleteComment", Comment: &idx})
	if err != nil {
		t.Fatal(err)
	}
	got := applyTextEdits(added, delEdits)
	if got != workedExample {
		t.Fatalf("add+delete comment not byte-identical:\n--- want ---\n%q\n--- got ---\n%q", workedExample, got)
	}
}

// TestDeleteMiddleStepNoDoubleBlank exercises the case round-tripping a
// single add/delete pair can't: deleting an element from the MIDDLE of a
// blank-separated list. Each add prefixes ITS OWN leading blank, so once
// two are added back to back and the first is removed, its neighbors —
// Fill's END_STEP above and M2's STEP header below — must end up separated
// by exactly one blank line, not the two that would result from naively
// keeping both original separators.
func TestDeleteMiddleStepNoDoubleBlank(t *testing.T) {
	e1, err := ApplyEdit(workedExample, EditOp{Type: "addStep", Name: "M1", After: "st:Fill"})
	if err != nil {
		t.Fatal(err)
	}
	src1 := applyTextEdits(workedExample, e1)
	e2, err := ApplyEdit(src1, EditOp{Type: "addStep", Name: "M2", After: "st:M1"})
	if err != nil {
		t.Fatal(err)
	}
	src2 := applyTextEdits(src1, e2)
	del, err := ApplyEdit(src2, EditOp{Type: "deleteStep", Step: "st:M1"})
	if err != nil {
		t.Fatal(err)
	}
	src3 := applyTextEdits(src2, del)
	if strings.Contains(src3, "\n\n\n") {
		t.Fatalf("deleting a middle step left a double-blank gap:\n%s", src3)
	}
	if _, err := Parse(src3); err != nil {
		t.Fatalf("result doesn't parse: %v\n%s", err, src3)
	}
}

// ── unknown op ───────────────────────────────────────────────────────

func TestApplyEditUnknownOp(t *testing.T) {
	wantOpErr(t, baseline, EditOp{Type: "frobnicate"})
}

// ── "new step" transitions and chained steps (the diagram's "+ transition →
// other… (new step)" and "+ step" with a step selected): the step and the
// transition arrive in ONE op — one edit, one undo — and the result checks
// clean of unknown-step errors. ─────────────────────────────────────────

// noErrors fails on any error-severity Check diagnostic.
func noErrors(t *testing.T, src string) {
	t.Helper()
	for _, d := range Check(mustParse(t, src)) {
		if d.Severity == SeverityError {
			t.Errorf("Check error: %v\n%s", d, src)
		}
	}
}

func TestOpAddTransitionNewStep(t *testing.T) {
	result, m := applyOp(t, baseline, EditOp{Type: "addTransition", From: []string{"B"}, To: []string{"C"}, NewStep: "C", Cond: "X"})
	c := findStepT(t, m, "st:C")
	if c.Initial || len(c.Actions) != 0 {
		t.Errorf("new step = %+v, want a plain empty STEP", c)
	}
	// Right after its source step in the file, before the transitions.
	if b := findStepT(t, m, "st:B"); c.Line != b.EndLine+2 {
		t.Errorf("STEP C at line %d, want right after B (ends %d)", c.Line, b.EndLine)
	}
	var tr *GTransition
	for i := range m.Trans {
		if equalStrings(m.Trans[i].From, []string{"B"}) && equalStrings(m.Trans[i].To, []string{"C"}) {
			tr = &m.Trans[i]
		}
	}
	if tr == nil || tr.Cond != "X" {
		t.Fatalf("no TRANSITION FROM B TO C := X:\n%s", result)
	}
	noErrors(t, result)
	if !strings.Contains(result, "  STEP C:\n  END_STEP\n") {
		t.Errorf("STEP C not printed canonically:\n%s", result)
	}

	// A NewStep that already exists is just the transition (no second STEP).
	_, m2 := applyOp(t, baseline, EditOp{Type: "addTransition", From: []string{"B"}, To: []string{"A"}, NewStep: "A", Cond: "X"})
	if len(m2.Steps) != 2 || len(m2.Trans) != 3 {
		t.Errorf("existing NewStep: steps=%d trans=%d, want 2 and 3", len(m2.Steps), len(m2.Trans))
	}
	// NewStep must be a TO member.
	wantOpErr(t, baseline, EditOp{Type: "addTransition", From: []string{"B"}, To: []string{"A"}, NewStep: "C", Cond: "X"})
	wantOpErr(t, baseline, EditOp{Type: "addTransition", From: []string{"B"}, To: []string{"1C"}, NewStep: "1C", Cond: "X"})

	// Round trip: one op in, deleteSelection of both out → byte-identical.
	got := roundTrip(t, workedExample,
		EditOp{Type: "addTransition", From: []string{"Drain"}, To: []string{"Rinse"}, NewStep: "Rinse", Cond: "TRUE", Name: "t_rinse"},
		EditOp{Type: "deleteSelection", Nodes: []string{"st:Rinse", "tr:t_rinse"}})
	if got != workedExample {
		t.Fatalf("new-step transition + delete not byte-identical:\n%q", got)
	}
}

func TestOpAddTransitionNewStepSameInsertionPoint(t *testing.T) {
	// No transitions yet: the step and the transition both want the line
	// after the last step — they must land as one edit, step first.
	src := "PROGRAM P\nVAR\n  X : BOOL;\nEND_VAR\nSFC\n  INITIAL_STEP A:\n  END_STEP\nEND_SFC\nEND_PROGRAM\n"
	edits, err := ApplyEdit(src, EditOp{Type: "addTransition", From: []string{"A"}, To: []string{"B"}, NewStep: "B", Cond: "X"})
	if err != nil {
		t.Fatal(err)
	}
	if len(edits) != 1 {
		t.Fatalf("got %d edits, want 1 merged insert: %+v", len(edits), edits)
	}
	result, m := applyOp(t, src, EditOp{Type: "addTransition", From: []string{"A"}, To: []string{"B"}, NewStep: "B", Cond: "X"})
	if findStepT(t, m, "st:B").Line > m.Trans[0].Line {
		t.Errorf("STEP B after its transition:\n%s", result)
	}
	noErrors(t, result)
}

func TestOpInsertAlternativeBranchNewStep(t *testing.T) {
	// Branch out of a STEP (From, no After) to a new step.
	result, m := applyOp(t, workedExample, EditOp{Type: "insertAlternativeBranch", From: []string{"Fill"}, To: []string{"Overflow"}, NewStep: "Overflow", Cond: "Level > 99.0"})
	findStepT(t, m, "st:Overflow")
	var alt *GTransition
	for i := range m.Trans {
		if equalStrings(m.Trans[i].To, []string{"Overflow"}) {
			alt = &m.Trans[i]
		}
	}
	if alt == nil || !equalStrings(alt.From, []string{"Fill"}) || alt.Kind != "alt" {
		t.Fatalf("alt branch = %+v\n%s", alt, result)
	}
	noErrors(t, result)
	// From a transition (After) to a new step: the source is inherited.
	_, m2 := applyOp(t, workedExample, EditOp{Type: "insertAlternativeBranch", After: "tr:t_full", To: []string{"Overflow"}, NewStep: "Overflow", Cond: "TRUE"})
	if s := findStepT(t, m2, "st:Overflow"); s.Line != findStepT(t, m2, "st:Fill").EndLine+2 {
		t.Errorf("Overflow should land right after Fill (the inherited source)")
	}
}

func TestOpAddStepChained(t *testing.T) {
	result, m := applyOp(t, baseline, EditOp{Type: "addStep", Name: "C", From: []string{"A"}, Cond: "TRUE"})
	c := findStepT(t, m, "st:C")
	if a := findStepT(t, m, "st:A"); c.Line != a.EndLine+2 {
		t.Errorf("chained step at line %d, want right after A (ends %d)", c.Line, a.EndLine)
	}
	var tr *GTransition
	for i := range m.Trans {
		if equalStrings(m.Trans[i].From, []string{"A"}) && equalStrings(m.Trans[i].To, []string{"C"}) {
			tr = &m.Trans[i]
		}
	}
	if tr == nil || tr.Cond != "TRUE" {
		t.Fatalf("no TRANSITION FROM A TO C := TRUE:\n%s", result)
	}
	// Lowest priority of A's group: after A's existing transition.
	if tr.Line <= m.Trans[0].EndLine || !equalStrings(m.Trans[0].To, []string{"B"}) {
		t.Errorf("chained transition should follow A's existing one:\n%s", result)
	}
	noErrors(t, result)

	// Without From: today's free step, one edit, no transition.
	_, m2 := applyOp(t, baseline, EditOp{Type: "addStep", Name: "C", After: "st:A"})
	if len(m2.Trans) != 2 {
		t.Errorf("free step added %d transitions", len(m2.Trans)-2)
	}
	wantOpErr(t, baseline, EditOp{Type: "addStep", Name: "C", From: []string{"A"}, Cond: "a; b"})
	wantOpErr(t, baseline, EditOp{Type: "addStep", Name: "B", From: []string{"A"}, Cond: "TRUE"}) // exists

	got := roundTrip(t, workedExample,
		EditOp{Type: "addStep", Name: "Rinse", From: []string{"Drain"}, Cond: "TRUE"},
		EditOp{Type: "deleteSelection", Nodes: []string{"st:Rinse", "tr:" + itoa(transLineTo(t, workedExample, EditOp{Type: "addStep", Name: "Rinse", From: []string{"Drain"}, Cond: "TRUE"}, "Rinse"))}})
	if got != workedExample {
		t.Fatalf("chained step + delete not byte-identical:\n%q", got)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

// transLineTo applies op and returns the line of the transition reaching `to`.
func transLineTo(t *testing.T, src string, op EditOp, to string) int {
	t.Helper()
	_, m := applyOp(t, src, op)
	for _, tr := range m.Trans {
		if equalStrings(tr.To, []string{to}) {
			return tr.Line
		}
	}
	t.Fatalf("no transition to %s", to)
	return 0
}
