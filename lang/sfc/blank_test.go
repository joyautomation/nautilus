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
