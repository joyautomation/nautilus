package ld

import (
	"strings"
	"testing"
)

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

// A header declaration of the instance (`m101 : MotorStarter;` in VAR, the
// lift station's permissives.ld) is reused by an insert of the SAME type —
// the call goes in, nothing is redeclared, and the program still compiles.
// A different type is refused, naming both. Renaming such an instance
// renames the header declaration with it.
func TestInsertReusesHeaderDeclaredInstance(t *testing.T) {
	src := `PROGRAM p
VAR
  a : BOOL;
  y : BOOL;
  t5 : TON;
END_VAR
LD
  RUNG r1
    a ( y )
END_LD
END_PROGRAM`
	edits, err := ApplyEdit(src, EditOp{Type: "insert", Rung: "r1", Kind: "fb", Inst: "t5", FbType: "TON", Args: "PT := T#1S", Index: 999})
	if err != nil {
		t.Fatalf("same-type header instance refused: %v", err)
	}
	out := applyTextEdits(src, edits)
	if !strings.Contains(out, "a t5:TON(PT := T#1S) ( y )") {
		t.Fatalf("insert:\n%s", out)
	}
	if strings.Count(strings.ToLower(out), "t5 : ton") != 1 {
		t.Fatalf("the header declaration must not be duplicated:\n%s", out)
	}
	compileST(t, toST(t, out))

	// Case-insensitive type match, too.
	if _, err := ApplyEdit(src, EditOp{Type: "insert", Rung: "r1", Kind: "fb", Inst: "T5", FbType: "ton", Index: 999}); err != nil {
		t.Fatalf("case-insensitive match refused: %v", err)
	}

	// A type mismatch names both types.
	_, err = ApplyEdit(src, EditOp{Type: "insert", Rung: "r1", Kind: "fb", Inst: "t5", FbType: "CTU", Index: 999})
	if err == nil || !strings.Contains(err.Error(), "TON") || !strings.Contains(err.Error(), "CTU") {
		t.Fatalf("type mismatch: %v", err)
	}
	// Once a rung calls it, a second in-rung call is a duplicate again.
	if _, err := ApplyEdit(out, EditOp{Type: "insert", Rung: "r1", Kind: "fb", Inst: "t5", FbType: "TON", Index: 999}); err == nil {
		t.Fatal("a second in-rung t5 must be refused")
	}

	// renameInst on a header-declared instance: call, declaration and
	// every reference follow.
	withRead := strings.Replace(out, "END_LD", "  RUNG r2\n    t5.Q ( a )\nEND_LD", 1)
	edits, err = ApplyEdit(withRead, EditOp{Type: "renameInst", Rung: "r1", Path: []int{1}, Name: "dly"})
	if err != nil {
		t.Fatalf("renameInst: %v", err)
	}
	renamed := applyTextEdits(withRead, edits)
	for _, want := range []string{"dly : TON;", "dly:TON(PT := T#1S)", "dly.Q ( a )"} {
		if !strings.Contains(renamed, want) {
			t.Errorf("renameInst missing %q:\n%s", want, renamed)
		}
	}
	if strings.Contains(strings.ToLower(renamed), "t5") {
		t.Errorf("t5 survives the rename:\n%s", renamed)
	}
	compileST(t, toST(t, renamed))
}
