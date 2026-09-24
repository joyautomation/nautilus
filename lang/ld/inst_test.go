package ld

import "testing"

// Two `t1:TON` declarations don't compile ("duplicate declaration"), so a
// palette insert with a taken instance name is refused; a paste still
// uniquifies.
func TestInsertDuplicateInstanceRefused(t *testing.T) {
	src := `PROGRAM p
VAR
  a : BOOL;
  y : BOOL;
  c1 : BOOL;
END_VAR
LD
  RUNG r1
    a t1:TON(PT := T#1S) ( y )
END_LD
END_PROGRAM`
	for _, inst := range []string{"t1", "T1", "c1"} {
		if _, err := ApplyEdit(src, EditOp{Type: "insert", Rung: "r1", Kind: "fb", Inst: inst, FbType: "TON", Args: "PT := T#1S", Index: 999}); err == nil {
			t.Fatalf("duplicate instance %q must be refused", inst)
		}
	}
	if _, err := ApplyEdit(src, EditOp{Type: "insert", Rung: "r1", Kind: "fb", Inst: "t2", FbType: "TON", Args: "PT := T#1S", Index: 999}); err != nil {
		t.Fatalf("fresh instance refused: %v", err)
	}
}
