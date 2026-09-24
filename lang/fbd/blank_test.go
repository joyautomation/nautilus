package fbd

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/lang/internal/seed"
)

func applySeed(src string, edits []TextEdit) string { return seed.Apply(src, toSeed(edits)) }

func TestGraphEmptyArrays(t *testing.T) {
	for _, src := range []string{"", "  \n", "PROGRAM P\nFBD\nEND_FBD\nEND_PROGRAM\n"} {
		m, err := Graph(src)
		if err != nil {
			t.Fatalf("%q: %v", src, err)
		}
		j, _ := json.Marshal(m)
		if strings.Contains(string(j), "null") {
			t.Fatalf("%q: null array in %s", src, j)
		}
		if m.Blank != (strings.TrimSpace(src) == "") {
			t.Fatalf("%q: blank=%v", src, m.Blank)
		}
	}
}

func TestSeedBlankFile(t *testing.T) {
	edits := mustOp(t, "", EditOp{Type: "insertStatement", Text: "Y := AND(A, B)", Pou: "Heater"})
	out := applySeed("", edits)
	if !strings.HasPrefix(out, "PROGRAM Heater\n") || !strings.Contains(out, "Y := AND(A, B)") {
		t.Fatalf("seeded source:\n%s", out)
	}
	if _, err := Graph(out); err != nil {
		t.Fatal(err)
	}
	out = applySeed("\n", mustOp(t, "\n", EditOp{Type: "init", Pou: "1bad"}))
	if out != skeleton("Main") {
		t.Fatalf("init with an invalid name should fall back to Main:\n%s", out)
	}
}
