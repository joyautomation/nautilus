package main

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func violationIDs(pm *ProjectMetrics) []string {
	seen := map[string]bool{}
	var ids []string
	for _, v := range pm.Violations() {
		id := ViolationID(v)
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

func measure(t *testing.T, dir string) *ProjectMetrics {
	t.Helper()
	pm, err := MeasureProject(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range pm.Files {
		if f.ParseError != "" {
			t.Fatalf("%s: %s", f.Path, f.ParseError)
		}
	}
	return pm
}

// Every reference solution is the shape AGENTS.md asks for: the scorer
// must find nothing wrong with it.
func TestReferencesHaveNoStructureViolations(t *testing.T) {
	refs, _ := filepath.Glob("tasks/*/reference")
	if len(refs) < 6 {
		t.Fatalf("found %d reference solutions", len(refs))
	}
	for _, ref := range refs {
		pm := measure(t, ref)
		if v := pm.Violations(); len(v) > 0 {
			t.Errorf("%s: %v", ref, v)
		}
	}
}

func TestLDKnot(t *testing.T) {
	pm := measure(t, "testdata/bad/ld-knot")
	l := pm.Files[0].LD
	want := LDMetrics{
		Rungs:            6,
		MaxSeries:        8, // r5
		MaxBranchDepth:   2, // r1: a branch inside a leg
		MaxCoils:         1,
		Uncommented:      6,
		GenericNames:     6,
		RepeatedContacts: 2, // StopPB on both inner legs, /Overload on both outer legs
		MultiAssign:      1, // r6
	}
	got := *l
	got.MaxElements, got.MeanElements = 0, 0
	if got != want {
		t.Errorf("metrics\n got %+v\nwant %+v", got, want)
	}
	if l.MaxElements != 11 { // r1: 9 contacts + /LSHH101 + the coil
		t.Errorf("MaxElements = %d, want 11", l.MaxElements)
	}
	if !reflect.DeepEqual(pm.MultiWriter, []string{"HORN"}) {
		t.Errorf("MultiWriter = %v, want [HORN] (S, plain and R coils)", pm.MultiWriter)
	}
	wantIDs := []string{
		"ld.branch-depth>1", "ld.contact-on-every-leg", "ld.generic-rung-name",
		"ld.multi-statement-move", "ld.multiple-coils", "ld.series>6", "ld.uncommented-rung",
	}
	if got := violationIDs(pm); !reflect.DeepEqual(got, wantIDs) {
		t.Errorf("violations\n got %v\nwant %v", got, wantIDs)
	}
}

func TestFBDNested(t *testing.T) {
	pm := measure(t, "testdata/bad/fbd-nested")
	b := pm.Files[0].FBD
	if b.MaxNesting != 3 {
		t.Errorf("MaxNesting = %d, want 3 (OR inside AND inside AND)", b.MaxNesting)
	}
	if !b.Implicit || b.Networks != 0 {
		t.Errorf("Implicit = %v, Networks = %d; want logic outside any NETWORK", b.Implicit, b.Networks)
	}
	if b.GenericWires != 2 { // wire w1, instance t1
		t.Errorf("GenericWires = %d, want 2", b.GenericWires)
	}
	if !b.Layout {
		t.Error("the @layout block was not seen")
	}
	wantIDs := []string{"fbd.generic-name", "fbd.layout-block", "fbd.logic-outside-network", "fbd.nesting>2"}
	if got := violationIDs(pm); !reflect.DeepEqual(got, wantIDs) {
		t.Errorf("violations\n got %v\nwant %v", got, wantIDs)
	}
}

func TestFBDReferenceShape(t *testing.T) {
	pm := measure(t, "tasks/analog-pid/reference")
	b := pm.Files[0].FBD
	if b.Networks != 3 || b.Untitled != 0 || b.Implicit {
		t.Errorf("networks %d, untitled %d, implicit %v; want 3 titled networks", b.Networks, b.Untitled, b.Implicit)
	}
	if b.MaxNesting != 1 {
		t.Errorf("MaxNesting = %d, want 1 (one block per line)", b.MaxNesting)
	}
	// conveyor's seal-in nests OR/NOT inside one AND: two deep is allowed.
	pm = measure(t, "tasks/conveyor-fbd/reference")
	if n := pm.Files[0].FBD.MaxNesting; n != 2 {
		t.Errorf("conveyor MaxNesting = %d, want 2", n)
	}
}

func TestSFCCaseMachine(t *testing.T) {
	pm := measure(t, "testdata/bad/sfc-case-machine")
	s := pm.Files[0].SFC
	want := SFCMetrics{
		Steps: 3, InitialSteps: 1, Transitions: 3, Associations: 3, Actions: 3,
		GenericSteps: 3, UnnamedTransitions: 2, StateVarTransitions: 3, ActionsWithIF: 3, ActionsSteeringChart: 3,
	}
	if *s != want {
		t.Errorf("metrics\n got %+v\nwant %+v", *s, want)
	}
	wantIDs := []string{"sfc.action-steers-chart", "sfc.generic-step-name", "sfc.state-variable-transition", "sfc.unnamed-transition"}
	if got := violationIDs(pm); !reflect.DeepEqual(got, wantIDs) {
		t.Errorf("violations\n got %v\nwant %v", got, wantIDs)
	}
}

func TestSFCReferenceShape(t *testing.T) {
	pm := measure(t, "tasks/batch-sfc/reference")
	s := pm.Files[0].SFC
	if s.StateVarTransitions != 0 || s.GenericSteps != 0 || s.UnnamedTransitions != 0 || s.ActionsSteeringChart != 0 {
		t.Errorf("reference chart flagged: %+v", *s)
	}
	if s.Steps != 6 || s.Transitions != 9 || s.Actions != 1 {
		t.Errorf("steps %d, transitions %d, actions %d; want 6, 9, 1", s.Steps, s.Transitions, s.Actions)
	}
}

func TestMultiWriters(t *testing.T) {
	cases := []struct {
		name string
		uses []coilUse
		bad  bool
	}{
		{"one plain coil", []coilUse{{rung: "a"}}, false},
		{"plain coil on two rungs", []coilUse{{rung: "a"}, {rung: "b"}}, true},
		{"same tag in two files", []coilUse{{file: "x.ld", rung: "a"}, {file: "y.ld", rung: "a"}}, true},
		{"set/reset pair", []coilUse{{rung: "a", mode: "S"}, {rung: "b", mode: "R"}}, false},
		{"several sets and a reset", []coilUse{{rung: "a", mode: "S"}, {rung: "b", mode: "S"}, {rung: "c", mode: "R"}}, false},
		{"plain mixed with set", []coilUse{{rung: "a"}, {rung: "b", mode: "S"}}, true},
		{"edge coil and plain coil", []coilUse{{rung: "a"}, {rung: "b", mode: "P"}}, false},
	}
	for _, c := range cases {
		got := multiWriters(map[string][]coilUse{"X": c.uses})
		if (len(got) > 0) != c.bad {
			t.Errorf("%s: flagged = %v, want %v", c.name, len(got) > 0, c.bad)
		}
	}
}

func TestGenericNames(t *testing.T) {
	for _, n := range []string{"r1", "R12", "rung3", "w1", "wire_2", "S0", "Step4", "t1", "tmp1", "x2", "net3", "State2"} {
		if !genericName.MatchString(n) {
			t.Errorf("%q should be generic", n)
		}
	}
	for _, n := range []string{"fillcmd", "hhTmr", "Fill", "t_full", "Step", "heatOn", "pump2run", "M101"} {
		if genericName.MatchString(n) {
			t.Errorf("%q should not be generic", n)
		}
	}
}

func TestStateVarCondition(t *testing.T) {
	for _, c := range []string{"State = 2", "(Step = 10)", "seq.State = 3"} {
		if !stateVarCond.MatchString(c) {
			t.Errorf("%q should be a state-variable transition", c)
		}
	}
	for _, c := range []string{"LT101_Level >= FullSP", "Mix.T >= T#30S", "StartPB", "Count = Limit", "State = 2 AND Ready"} {
		if stateVarCond.MatchString(c) {
			t.Errorf("%q should not be a state-variable transition", c)
		}
	}
}

// A file that does not parse is reported, not fatal: the agent's broken
// output is still scored.
func TestParseErrorIsAViolation(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "x.ld"), []byte("PROGRAM X\nLD\n  RUNG a\n    [ A | B ( C )\nEND_LD\nEND_PROGRAM\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pm, err := MeasureProject(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := violationIDs(pm); !reflect.DeepEqual(got, []string{"ld.parse-error"}) {
		t.Errorf("violations = %v, want [ld.parse-error]", got)
	}
}

// AGENTS.md allows a short ACTION body for what an association cannot do,
// such as a counter, and forbids only an IF that decides where the chart
// goes next. A conditional counter is fine; an IF setting a flag a
// transition reads is not.
func TestSFCActionIFRule(t *testing.T) {
	chart := func(body string) string {
		return "PROGRAM B\nSFC\n  INITIAL_STEP Idle:\n  END_STEP\n  STEP Drain:\n    P0 Count;\n  END_STEP\n" +
			"  TRANSITION t_go FROM Idle TO Drain := StartPB AND NOT Busy;\n  END_TRANSITION\n" +
			"  TRANSITION t_done FROM Drain TO Idle := LT101_Level <= EmptySP;\n  END_TRANSITION\n" +
			"  ACTION Count:\n    " + body + "\n  END_ACTION\nEND_SFC\nEND_PROGRAM\n"
	}
	counter, err := measureSFC(chart("IF NOT Aborted THEN BatchCount := BatchCount + 1; END_IF;"))
	if err != nil {
		t.Fatal(err)
	}
	if counter.ActionsWithIF != 1 || counter.ActionsSteeringChart != 0 {
		t.Errorf("conditional counter: %+v, want one IF action steering nothing", *counter)
	}
	steer, err := measureSFC(chart("IF LT101_Level > 50.0 THEN Busy := TRUE; END_IF;"))
	if err != nil {
		t.Fatal(err)
	}
	if steer.ActionsSteeringChart != 1 {
		t.Errorf("IF setting a transition's input: %+v, want it flagged", *steer)
	}
}
