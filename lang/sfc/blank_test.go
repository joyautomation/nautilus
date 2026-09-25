package sfc

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestGraphEmptyChartArrays(t *testing.T) {
	for _, src := range []string{"", "PROGRAM P\nSFC\nEND_SFC\nEND_PROGRAM\n"} {
		m, err := Graph(src)
		if err != nil {
			t.Fatal(err)
		}
		j, _ := json.Marshal(m)
		if !strings.Contains(string(j), `"steps":[]`) || !strings.Contains(string(j), `"trans":[]`) {
			t.Fatalf("%q: %s", src, j)
		}
		if m.Blank != (src == "") {
			t.Fatalf("%q: blank=%v", src, m.Blank)
		}
	}
}

func TestSeedBlankChart(t *testing.T) {
	edits, err := ApplyEdit("", EditOp{Type: "addStep", Name: "Idle", Initial: true, Pou: "Seq"})
	if err != nil {
		t.Fatal(err)
	}
	out := applyTextEdits("", edits)
	m, err := Graph(out)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if m.Name != "Seq" || len(m.Steps) != 1 || !m.Steps[0].Initial {
		t.Fatalf("seeded chart: %+v\n%s", m, out)
	}
}

// "Initialize" on a blank .sfc writes a chart naut check accepts — the
// FBD and LD skeletons already do — and "+ step" on it then adds an
// ordinary step, never a second INITIAL_STEP.
func TestSeedInitCheckClean(t *testing.T) {
	edits, err := ApplyEdit("", EditOp{Type: "init", Pou: "Seq"})
	if err != nil {
		t.Fatal(err)
	}
	out := applyTextEdits("", edits)
	mustCompile(t, out)
	m, err := Graph(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Steps) != 1 || !m.Steps[0].Initial {
		t.Fatalf("seeded chart lacks its initial step: %+v\n%s", m.Steps, out)
	}
	// The webview marks a step initial only on an empty chart; the seeded
	// Start step means the next one is plain.
	out = applyTextEdits(out, mustEdits(t, out, EditOp{Type: "addStep", Name: "Fill"}))
	if strings.Count(out, "INITIAL_STEP") != 1 {
		t.Fatalf("want exactly one INITIAL_STEP:\n%s", out)
	}
	if _, err := ApplyEdit(out, EditOp{Type: "addStep", Name: "Other", Initial: true}); err == nil {
		t.Fatal("a second INITIAL_STEP must be refused")
	}

	// Seeding through another op keeps the Start step too.
	edits, err = ApplyEdit("", EditOp{Type: "addComment", Text: "note", Pou: "Seq"})
	if err != nil {
		t.Fatal(err)
	}
	mustCompile(t, applyTextEdits("", edits))

	// "+ step" on a blank file: that step is the initial one, even if the
	// op didn't say so.
	edits, err = ApplyEdit("", EditOp{Type: "addStep", Name: "Idle", Pou: "Seq"})
	if err != nil {
		t.Fatal(err)
	}
	out = applyTextEdits("", edits)
	mustCompile(t, out)
	if strings.Count(out, "INITIAL_STEP") != 1 || !strings.Contains(out, "INITIAL_STEP Idle:") {
		t.Fatalf("blank + step should seed Idle as the only initial step:\n%s", out)
	}
}

func mustEdits(t *testing.T, src string, op EditOp) []TextEdit {
	t.Helper()
	edits, err := ApplyEdit(src, op)
	if err != nil {
		t.Fatalf("ApplyEdit(%+v): %v", op, err)
	}
	return edits
}

