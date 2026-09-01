package fbd

import (
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/lang/st"
)

// ── stripComments itself ────────────────────────────────────────────────────

func TestStripCommentsNested(t *testing.T) {
	src := "(* outer (* inner *) still outer *) FBD"
	got := stripComments(src)
	if strings.Contains(got, "outer") || strings.Contains(got, "inner") {
		t.Fatalf("a nested comment's body must be fully blanked: %q", got)
	}
	if !strings.Contains(got, "FBD") {
		t.Fatalf("code after a nested comment must survive untouched: %q", got)
	}
}

func TestStripCommentsStringLiteral(t *testing.T) {
	// A "(*" inside a string literal, with no matching "*)" in the same
	// string, must not be read as opening a real comment — if it were, the
	// scan would keep consuming everything (real code included) looking for
	// SOME "*)" to close it, landing on the real trailing comment's closer
	// instead and wiping out the real code in between.
	src := "s : STRING := 'contains (* but no closing quote here';\n" +
		"END_FBD (* trailing *)"
	got := stripComments(src)
	if !strings.Contains(got, "'contains (* but no closing quote here'") {
		t.Fatalf("string literal content must survive untouched: %q", got)
	}
	if !strings.Contains(got, "END_FBD") {
		t.Fatalf("a stray (* inside a string literal must not swallow the real code that follows: %q", got)
	}
	if strings.Contains(got, "trailing") {
		t.Fatalf("the real trailing comment must still be blanked: %q", got)
	}
}

func TestStripCommentsPreservesShape(t *testing.T) {
	src := "FBD (* x\ny *) a := b\n// note\nEND_FBD\n"
	got := stripComments(src)
	if len(got) != len(src) {
		t.Fatalf("stripComments must preserve length: got %d want %d", len(got), len(src))
	}
	if strings.Count(got, "\n") != strings.Count(src, "\n") {
		t.Fatalf("stripComments must preserve line count:\n got: %q\nwant: %q", got, src)
	}
}

// ── the actual bug: comment text that LOOKS like structure ─────────────────

// docCommentSrc documents the multi-POU FBD grammar in a block comment full
// of example code — every keyword splitBlocks/splitFBD/pouName scan for
// (FBD, END_FBD, FUNCTION_BLOCK, PROGRAM) appears alone on a line inside
// it. None of it is real: the only real POUs are Scale and Main, after the
// comment closes.
const docCommentSrc = `(* FBD netlist grammar, by example — this text is
documentation inside a block comment, not real IEC source:

FUNCTION_BLOCK Example
VAR_INPUT IN : REAL; END_VAR
VAR_OUTPUT OUT : REAL; END_VAR
FBD
  OUT := MUL(IN, 2.0)
END_FBD
END_FUNCTION_BLOCK

PROGRAM Demo
FBD
  x := y
END_FBD
END_PROGRAM
*)

FUNCTION_BLOCK Scale
VAR_INPUT  IN : REAL; END_VAR
VAR_OUTPUT OUT : REAL; END_VAR
FBD
  OUT := MUL(IN, 2.0)
END_FBD
END_FUNCTION_BLOCK

PROGRAM Main
VAR_EXTERNAL Raw : REAL; Eng : REAL; END_VAR
FBD
  s1 : Scale(IN := Raw)
  Eng := s1.OUT
END_FBD
END_PROGRAM
`

// TestDocCommentDoesNotTripSplitBlocks reproduces the ladder-package bug's
// FBD-side analog: a doc comment showing example FBD text must not be read
// as real POUs or block boundaries by splitBlocks, splitFBD, or pouName.
func TestDocCommentDoesNotTripSplitBlocks(t *testing.T) {
	blocks, err := splitBlocks(docCommentSrc)
	if err != nil {
		t.Fatalf("splitBlocks: %v", err)
	}
	if len(blocks) != 2 {
		t.Fatalf("want exactly 2 real FBD blocks (Scale's and Main's), got %d", len(blocks))
	}

	out, err := Transpile(docCommentSrc)
	if err != nil {
		t.Fatalf("transpile: %v", err)
	}
	for _, want := range []string{
		"FUNCTION_BLOCK Scale",
		"OUT := (IN * 2.0);",
		"PROGRAM Main",
		"s1(IN := Raw);",
		"Eng := s1.OUT;",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if _, err := st.Parse(out); err != nil {
		t.Fatalf("the transpiled output must parse as valid ST despite the decoy text: %v\n%s", err, out)
	}
}

// TestRealMalformedBlockStillCaughtNearDocComment guards the fix's other
// edge: a GENUINELY unclosed FBD block must still be reported, even with a
// doc comment full of decoy FBD/END_FBD text sitting right above it.
func TestRealMalformedBlockStillCaughtNearDocComment(t *testing.T) {
	src := `(* Example:
FBD
  a := b
END_FBD
*)
PROGRAM p
FBD
  a := b
END_PROGRAM
`
	_, err := Transpile(src)
	if err == nil || !strings.Contains(err.Error(), "no END_FBD") {
		t.Fatalf("a real unclosed FBD block must still be reported: err = %v", err)
	}
}

// TestDocCommentHeaderDoesNotFakeDeclaration is declaredNames' analog: a
// VAR-shaped example sitting in a header comment must not be read as a real
// declaration — otherwise the transpiler wrongly believes an FB instance
// the netlist needs is already declared and skips emitting it, leaving the
// output referencing an undeclared identifier.
func TestDocCommentHeaderDoesNotFakeDeclaration(t *testing.T) {
	src := `PROGRAM Timed
VAR_EXTERNAL Run : BOOL; Elapsed : BOOL; END_VAR
(* Example: VAR t1 : TON; END_VAR — shown here as documentation, not real. *)
FBD
  Elapsed := t1.Q
  t1 : TON(IN := Run, PT := T#5S)
END_FBD
END_PROGRAM`
	stSrc := mustTranspile(src)
	if !strings.Contains(stSrc, "VAR\n  t1 : TON;") {
		t.Fatalf("a VAR-shaped decoy inside a comment must not be read as a real declaration — t1 still needs auto-declaring:\n%s", stSrc)
	}
	run(t, src, map[string]ir.Value{"Run": ir.BoolVal(false)})
}
