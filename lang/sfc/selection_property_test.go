package sfc

import (
	"math/rand"
	"os"
	"strings"
	"testing"
)

// The chart editor's copy/cut/paste/delete ops for every selection shape —
// each step alone, select-all, random subsets, and a selection that names
// a step twice — mirroring the webview's clipSteps/deleteStepsOp: the
// selected steps plus the transitions wholly between them. Results always
// parse; a copy never adds a check error; select-all cut + paste gives
// back a chart that compiles (its entry step becomes initial again).
func TestSelectionOpsProperty(t *testing.T) {
	b, err := os.ReadFile("../../examples/tank-batch-sfc/program.sfc")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	if _, err := Compile(src); err != nil {
		t.Fatalf("example doesn't compile: %v", err)
	}
	m, err := Graph(src)
	if err != nil {
		t.Fatal(err)
	}
	var all []string
	for _, s := range m.Steps {
		all = append(all, s.ID)
	}
	sels := [][]string{all, append(append([]string(nil), all...), all[0])}
	for _, id := range all {
		sels = append(sels, []string{id})
	}
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 60; i++ {
		var sub []string
		for _, id := range all {
			if rng.Intn(2) == 0 {
				sub = append(sub, id)
			}
		}
		if len(sub) > 0 {
			sels = append(sels, sub)
		}
	}

	for _, sel := range sels {
		in := map[string]bool{}
		var paste EditOp
		paste.Type = "pasteSteps"
		for _, id := range sel {
			for _, s := range m.Steps {
				if s.ID == id {
					in[strings.ToLower(s.Name)] = true
					paste.Steps = append(paste.Steps, PasteStep{Name: s.Name, Actions: s.Actions})
				}
			}
		}
		del := EditOp{Type: "deleteSelection", Nodes: append([]string(nil), sel...)}
		for _, tr := range m.Trans {
			inner := len(tr.From) > 0 && len(tr.To) > 0
			for _, n := range append(append([]string(nil), tr.From...), tr.To...) {
				inner = inner && in[strings.ToLower(n)]
			}
			if inner {
				paste.Trans = append(paste.Trans, PasteTrans{Name: tr.Name, From: tr.From, To: tr.To, Cond: tr.Cond})
				del.Nodes = append(del.Nodes, tr.ID)
			}
		}

		// Copy + paste in place: the copies are warnings (unreachable)
		// at worst, never errors.
		out := applyResult(t, src, paste)
		mustCheckClean(t, "paste "+strings.Join(sel, ","), out)

		// Cut: the batched delete, then paste into what's left.
		cut := applyResult(t, src, del)
		if _, err := Graph(cut); err != nil {
			t.Fatalf("deleteSelection %v no longer parses: %v\n%s", sel, err, cut)
		}
		back := applyResult(t, cut, paste)
		if len(in) == len(all) {
			cm, _ := Graph(cut)
			if len(cm.Steps) != 0 || len(cm.Trans) != 0 {
				t.Fatalf("select-all cut left elements behind:\n%s", cut)
			}
			mustCheckClean(t, "select-all cut+paste", back)
			if _, err := Compile(back); err != nil {
				t.Fatalf("select-all cut+paste doesn't compile: %v\n%s", err, back)
			}
		} else if _, err := Graph(back); err != nil {
			t.Fatalf("cut+paste %v no longer parses: %v\n%s", sel, err, back)
		}
	}

	// A paste into a blank file seeds the chart around it: the copied
	// fragment's entry step is the initial one.
	edits, err := ApplyEdit("", EditOp{Type: "pasteSteps", Pou: "Seq",
		Steps: []PasteStep{{Name: "A"}, {Name: "B"}},
		Trans: []PasteTrans{{From: []string{"B"}, To: []string{"A"}, Cond: "TRUE"}}})
	if err != nil {
		t.Fatal(err)
	}
	out := applyTextEdits("", edits)
	if !strings.Contains(out, "INITIAL_STEP B:") || strings.Count(out, "INITIAL_STEP") != 1 {
		t.Fatalf("blank paste should make its entry step B initial:\n%s", out)
	}
}

func mustCheckClean(t *testing.T, what, src string) {
	t.Helper()
	prog, err := Parse(src)
	if err != nil {
		t.Fatalf("%s no longer parses: %v\n%s", what, err, src)
	}
	for _, d := range Check(prog) {
		if d.Severity == SeverityError {
			t.Fatalf("%s: check error %s\n%s", what, d, src)
		}
	}
}
