package fbd

import (
	"strings"
	"testing"
)

const notesSrc = `PROGRAM Main
VAR_EXTERNAL
  A : BOOL; B : BOOL; Y : BOOL; Z : BOOL;
END_VAR
FBD
  // note A
  Y := AND(A, B)
  // note B
  w = OR(A, B)
  // note C
  Z := w
END_FBD
(* @layout
cm:1 10,20
cm:2 30,40
*)
END_PROGRAM`

// Deleting notes A and B of A/B/C must leave C — comment ids are ordinals,
// so the selection resolves against ONE parse, never op-by-op.
func TestDeleteNodesBatchComments(t *testing.T) {
	out := apply(t, notesSrc, mustOp(t, notesSrc, EditOp{Type: "deleteNode", Nodes: []string{"cm:0", "cm:1"}}))
	if strings.Contains(out, "note A") || strings.Contains(out, "note B") || !strings.Contains(out, "note C") {
		t.Fatalf("wrong notes deleted:\n%s", out)
	}
	m, err := Graph(out)
	if err != nil {
		t.Fatal(err)
	}
	// note C was cm:2 pinned at 30,40; it is now cm:0 and keeps its pin;
	// note B's pin is gone.
	for _, n := range m.Nodes {
		if n.ID == "cm:0" {
			if n.X == nil || *n.X != 30 || *n.Y != 40 {
				t.Fatalf("note C lost its pin: %+v\n%s", n, out)
			}
			return
		}
	}
	t.Fatalf("no cm:0 after delete:\n%s", out)
}

// A selection mixing statements and notes lands as one non-overlapping
// edit set.
func TestDeleteNodesBatchMixed(t *testing.T) {
	out := apply(t, notesSrc, mustOp(t, notesSrc, EditOp{Type: "deleteNode", Nodes: []string{"cm:1", "c:Y", "b:w.w", "c:Z", "cm:2"}}))
	for _, gone := range []string{"note B", "note C", "Y :=", "w = OR", "Z :="} {
		if strings.Contains(out, gone) {
			t.Fatalf("%q survived:\n%s", gone, out)
		}
	}
	if !strings.Contains(out, "note A") {
		t.Fatalf("note A deleted:\n%s", out)
	}
	if _, err := Graph(out); err != nil {
		t.Fatalf("result no longer parses: %v\n%s", err, out)
	}
}
