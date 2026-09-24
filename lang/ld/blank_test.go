package ld

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestGraphEmptyRungsIsArray(t *testing.T) {
	for _, src := range []string{"", "PROGRAM P\nLD\nEND_LD\nEND_PROGRAM\n"} {
		m, err := Graph(src)
		if err != nil {
			t.Fatal(err)
		}
		j, _ := json.Marshal(m)
		if !strings.Contains(string(j), `"rungs":[]`) {
			t.Fatalf("%q: %s", src, j)
		}
		if m.Blank != (src == "") {
			t.Fatalf("%q: blank=%v", src, m.Blank)
		}
	}
}

func TestSeedBlankLadder(t *testing.T) {
	edits, err := ApplyEdit("", EditOp{Type: "addRung", Name: "rung1", Pou: "Interlock"})
	if err != nil {
		t.Fatal(err)
	}
	out := applyTextEdits("", edits)
	if !strings.HasPrefix(out, "PROGRAM Interlock\n") || !strings.Contains(out, "RUNG rung1") {
		t.Fatalf("seeded:\n%s", out)
	}
	m, err := Graph(out)
	if err != nil || len(m.Rungs) != 1 {
		t.Fatalf("seeded ladder: %v %+v\n%s", err, m, out)
	}
}
