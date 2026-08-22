package ld

import (
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/lang/st"
)

// ── stripComments itself ────────────────────────────────────────────────────

func TestStripCommentsNested(t *testing.T) {
	src := "(* outer (* inner *) still outer *) RUNG r ( y )"
	got := stripComments(src)
	if strings.Contains(got, "outer") || strings.Contains(got, "inner") {
		t.Fatalf("a nested comment's body must be fully blanked: %q", got)
	}
	if !strings.Contains(got, "RUNG r ( y )") {
		t.Fatalf("code after a nested comment must survive untouched: %q", got)
	}
}

func TestStripCommentsStringLiteral(t *testing.T) {
	// A "(*" inside a string literal, with no matching "*)" in the same
	// string, must not be read as opening a real comment — if it were, the
	// scan would keep consuming everything (real code included) looking for
	// SOME "*)" to close it, and land on the real trailing comment's closer
	// instead, wiping out the RUNG in between.
	src := "Tag : STRING := 'contains (* but no closing quote here';\n" +
		"RUNG realRung ( Out ) (* trailing *)"
	got := stripComments(src)
	if !strings.Contains(got, "'contains (* but no closing quote here'") {
		t.Fatalf("string literal content must survive untouched: %q", got)
	}
	if !strings.Contains(got, "RUNG realRung ( Out )") {
		t.Fatalf("a stray (* inside a string literal must not swallow the real code that follows: %q", got)
	}
	if strings.Contains(got, "trailing") {
		t.Fatalf("the real trailing comment must still be blanked: %q", got)
	}
}

func TestStripCommentsPreservesShape(t *testing.T) {
	src := "RUNG a (* x\ny *) ( Out )\n// note\nEND_LD\n"
	got := stripComments(src)
	if len(got) != len(src) {
		t.Fatalf("stripComments must preserve length: got %d want %d", len(got), len(src))
	}
	if strings.Count(got, "\n") != strings.Count(src, "\n") {
		t.Fatalf("stripComments must preserve line count:\n got: %q\nwant: %q", got, src)
	}
}

// ── the actual bug: comment text that LOOKS like structure ─────────────────

// docCommentLib is a project library whose header documents the ladder
// subroutine grammar with a block comment full of example code — every
// keyword this package scans for (FUNCTION_BLOCK, END_FUNCTION_BLOCK,
// PROGRAM, LD, END_LD, RUNG) appears at the START of a line inside that
// comment. None of it is real: the only real declaration is PumpSeq, after
// the comment closes.
const docCommentLib = `(* Ladder subroutine grammar, by example — this text is
documentation inside a block comment, not real IEC source:

FUNCTION_BLOCK here, instantiated once by the caller, looks like:
  VAR_INPUT Start : BOOL; END_VAR
  VAR_OUTPUT Run : BOOL; END_VAR
  LD
    RUNG seal [ Start | Run ] ( Run )
  END_LD
END_FUNCTION_BLOCK

A stand-alone PROGRAM reads the same way, just without pins:
PROGRAM Example
LD
  RUNG r Start ( Run )
END_LD
END_PROGRAM
*)

FUNCTION_BLOCK PumpSeq
VAR_INPUT  Start : BOOL; Level : REAL; END_VAR
VAR_OUTPUT Run : BOOL; END_VAR
VAR        t1 : TON; END_VAR
LD
  RUNG seal [ Start | Run ] t1:TON(PT := T#10S) ( Run )
END_LD
END_FUNCTION_BLOCK`

// TestLibraryDocCommentDoesNotTripStructuralScan is the reported bug,
// reproduced directly: a library whose header comment shows example ladder
// text must not be read as real declarations by any of the structural
// scans (checkDuplicateFBs, scanFBSigs, HasBlock, pouName, Graph).
func TestLibraryDocCommentDoesNotTripStructuralScan(t *testing.T) {
	if err := checkDuplicateFBs(docCommentLib); err != nil {
		t.Fatalf("a FUNCTION_BLOCK named only inside a comment must not count as a real (or duplicate) declaration: %v", err)
	}
	if !HasBlock(docCommentLib) {
		t.Fatal("HasBlock must still find the one REAL LD block")
	}
	if pouName(docCommentLib) != "" {
		t.Fatalf("pouName = %q, want \"\" — the PROGRAM named in the comment isn't real", pouName(docCommentLib))
	}

	sigs := scanFBSigs(docCommentLib)
	if len(sigs) != 1 {
		t.Fatalf("want exactly 1 real FUNCTION_BLOCK signature, got %d: %+v", len(sigs), sigs)
	}
	if sigs[0].name != "PumpSeq" {
		t.Fatalf("sig name = %q, want PumpSeq", sigs[0].name)
	}
	if len(sigs[0].inputs) != 2 || len(sigs[0].outputs) != 1 {
		t.Fatalf("PumpSeq pins wrong: %+v", sigs[0])
	}

	m, err := Graph(docCommentLib)
	if err != nil {
		t.Fatalf("graph: %v", err)
	}
	if len(m.Blocks) != 1 || m.Blocks[0].Name != "PumpSeq" {
		t.Fatalf("want exactly 1 real block (PumpSeq), got %+v", m.Blocks)
	}
	if len(m.Rungs) != 1 || m.Rungs[0].Name != "seal" || m.Rungs[0].POU != "PumpSeq" {
		t.Fatalf("want exactly 1 real rung (seal, inside PumpSeq), got %+v", m.Rungs)
	}

	// And the library compiles as expected: a single transpile hop with no
	// false "already declared" error, producing PumpSeq's real body — the
	// decoy text stays exactly where it was, as an ordinary comment (a
	// transpile never strips comments; the ST lexer does that later), it's
	// just no longer mistaken for a second declaration.
	stSrc := toST(t, docCommentLib)
	if !strings.Contains(stSrc, "FUNCTION_BLOCK PumpSeq") {
		t.Fatalf("transpiled output missing the real FUNCTION_BLOCK:\n%s", stSrc)
	}
	if strings.Count(stSrc, "END_FUNCTION_BLOCK") != 2 { // decoy comment's + the real one
		t.Fatalf("want exactly 2 END_FUNCTION_BLOCK occurrences (one decoy in the comment, one real):\n%s", stSrc)
	}
	if _, err := st.Parse(stSrc); err != nil {
		t.Fatalf("the transpiled output must parse as valid ST despite the decoy text: %v\n%s", err, stSrc)
	}
}

// TestLibraryDocCommentResolvesAsLibrary confirms the same library, used the
// way project libraries actually are — joined ahead of a caller file via
// Transpile's libs argument — resolves PumpSeq's power pins correctly and
// does not report the FUNCTION_BLOCK it only mentions in prose as already
// declared.
func TestLibraryDocCommentResolvesAsLibrary(t *testing.T) {
	caller := `PROGRAM p
VAR_EXTERNAL Go : BOOL; Running : BOOL; END_VAR
VAR p1 : PumpSeq; END_VAR
LD
  RUNG r  Go p1:PumpSeq(Level := 1.0) ( Running )
END_LD
END_PROGRAM`

	out := toST(t, caller, docCommentLib)
	if !strings.Contains(out, "p1(Start := Go, Level := 1.0);") {
		t.Fatalf("power pins on the library block resolved wrong:\n%s", out)
	}
	if !strings.Contains(out, "Running := p1.Run;") {
		t.Fatalf("power should continue from PumpSeq's Run output:\n%s", out)
	}
}

// TestRealDuplicateStillCaughtNearDocComment guards the fix's other edge: a
// GENUINE duplicate FUNCTION_BLOCK must still be reported, even with a doc
// comment mentioning the same name sitting right next to it.
func TestRealDuplicateStillCaughtNearDocComment(t *testing.T) {
	src := `(* PumpSeq is a pump-sequencing block. *)
FUNCTION_BLOCK PumpSeq
VAR_INPUT IN : BOOL; END_VAR
VAR_OUTPUT Q : BOOL; END_VAR
LD
  RUNG a  IN ( Q )
END_LD
END_FUNCTION_BLOCK

FUNCTION_BLOCK PumpSeq
VAR_INPUT IN : BOOL; END_VAR
VAR_OUTPUT Q : BOOL; END_VAR
LD
  RUNG b  IN ( Q )
END_LD
END_FUNCTION_BLOCK`
	_, err := Transpile(src)
	if err == nil || !strings.Contains(err.Error(), "already declared") {
		t.Fatalf("a real duplicate FUNCTION_BLOCK must still be reported: err = %v", err)
	}
}

// TestRungCommentSurvivesDecoyDocComment: a rung's own (* ... *) header
// comment must round-trip through Graph and an edit op even when a leading
// doc comment full of decoy "RUNG"/"LD"/"END_LD" text sits above it in the
// same file.
func TestRungCommentSurvivesDecoyDocComment(t *testing.T) {
	src := `(* Reference grammar:
RUNG decoy (* not real *) fake elements here
LD
END_LD
*)
PROGRAM p
VAR_EXTERNAL Start : BOOL; Run : BOOL; END_VAR
LD
  RUNG seal (* keep the comment *)
    Start ( Run )
END_LD
END_PROGRAM`

	m, err := Graph(src)
	if err != nil {
		t.Fatalf("graph: %v", err)
	}
	if len(m.Rungs) != 1 {
		t.Fatalf("want exactly 1 real rung — the decoy text must not parse as one, got %+v", m.Rungs)
	}
	if m.Rungs[0].Name != "seal" || m.Rungs[0].Comment != "keep the comment" {
		t.Fatalf("real rung wrong: %+v", m.Rungs[0])
	}

	edits, err := ApplyEdit(src, EditOp{Type: "setRef", Rung: "seal", Path: []int{0}, Ref: "Go"})
	if err != nil {
		t.Fatalf("setRef: %v", err)
	}
	out := applyTextEdits(src, edits)
	if !strings.Contains(out, "(* keep the comment *)") {
		t.Fatalf("the real rung header comment must survive an edit:\n%s", out)
	}
	if !strings.Contains(out, "RUNG decoy (* not real *) fake elements here") {
		t.Fatalf("the decoy text inside the leading doc comment must be left untouched:\n%s", out)
	}
}
