package st

import (
	"strings"
	"testing"
)

// identLits returns the literals of every TokenIdent in toks, in order —
// a convenient way to assert "what code survived the comment" without
// caring about exact punctuation tokens.
func identLits(toks []Token) []string {
	var out []string
	for _, tok := range toks {
		if tok.Type == TokenIdent {
			out = append(out, tok.Literal)
		}
	}
	return out
}

func TestBlockComment_NestedTwoLevels(t *testing.T) {
	toks := Lex("(* outer (* inner *) still outer *) CODE")
	// The entire nested comment must be consumed as one unit: only CODE
	// (plus EOF) should come out the other side.
	if got := identLits(toks); len(got) != 1 || got[0] != "CODE" {
		t.Fatalf("identifiers = %v, want [CODE]", got)
	}
	if toks[len(toks)-1].Type != TokenEOF {
		t.Fatalf("last token = %v, want TokenEOF", toks[len(toks)-1].Type)
	}
}

func TestBlockComment_NestedWithCodeAfter(t *testing.T) {
	// Regression test for the reported bug: a nested comment used to close
	// at the FIRST "*)" (the inner one), spilling " still comment *)" out
	// as code and producing a confusing downstream parse error several
	// lines later. It must now close only at the outer "*)".
	src := `PROGRAM p
VAR
	x : INT; (* outer comment (* inner *) still comment *)
	y : INT;
END_VAR
END_PROGRAM`
	prog, err := Parse(src)
	if err != nil {
		t.Fatalf("Parse: unexpected error: %v", err)
	}
	if prog.Name != "p" {
		t.Fatalf("prog.Name = %q, want %q", prog.Name, "p")
	}
}

func TestBlockComment_NestedThreeLevels(t *testing.T) {
	toks := Lex("(* a (* b (* c *) b *) a *) TAIL")
	if got := identLits(toks); len(got) != 1 || got[0] != "TAIL" {
		t.Fatalf("identifiers = %v, want [TAIL]", got)
	}
}

func TestBlockComment_Unterminated_ErrorPosition(t *testing.T) {
	// The outer "(*" opens at line 2, col 1. It's never closed, so the
	// error must point at the OUTER open, not the inner one (line 2, col 8)
	// or the point where scanning gave up (EOF).
	src := "PROGRAM p\n(* outer (* inner *) still open"
	_, errs := LexErrors(src)
	if len(errs) != 1 {
		t.Fatalf("errs = %v, want exactly 1 error", errs)
	}
	want := "unterminated block comment opened at line 2:1"
	if got := errs[0].Error(); got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
	if errs[0].Pos.Line != 2 || errs[0].Pos.Col != 1 {
		t.Fatalf("Pos = %+v, want line 2 col 1", errs[0].Pos)
	}
}

func TestBlockComment_Unterminated_SimpleCase(t *testing.T) {
	_, errs := LexErrors("(* never closed")
	if len(errs) != 1 {
		t.Fatalf("errs = %v, want exactly 1 error", errs)
	}
	want := "unterminated block comment opened at line 1:1"
	if got := errs[0].Error(); got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
}

func TestBlockComment_Terminated_NoError(t *testing.T) {
	_, errs := LexErrors("(* fine *) x := 1;")
	if len(errs) != 0 {
		t.Fatalf("errs = %v, want none", errs)
	}
}

func TestLex_IgnoresLexErrors_BackwardCompatible(t *testing.T) {
	// Lex's signature never changed; an unterminated comment must not
	// panic or otherwise change Lex's behavior for existing callers.
	toks := Lex("(* never closed")
	if len(toks) != 1 || toks[0].Type != TokenEOF {
		t.Fatalf("tokens = %v, want just [EOF]", toks)
	}
}

func TestStringLiteral_ContainingCommentMarkers_Unaffected(t *testing.T) {
	// A string holding "(*" / "*)" text must lex as an ordinary string —
	// never mistaken for a comment delimiter, and never disturbed by the
	// comment-skipping logic (which never runs while a string is being
	// scanned).
	toks := Lex(`s := '(* not a comment *)'; t := '*)'; u := '(*';`)

	var strs []string
	for _, tok := range toks {
		if tok.Type == TokenString {
			strs = append(strs, tok.Literal)
		}
	}
	want := []string{"(* not a comment *)", "*)", "(*"}
	if len(strs) != len(want) {
		t.Fatalf("string literals = %v, want %v", strs, want)
	}
	for i := range want {
		if strs[i] != want[i] {
			t.Fatalf("string literal[%d] = %q, want %q", i, strs[i], want[i])
		}
	}

	// And the assignments around them must still lex as real assignments,
	// not get swallowed as comment text.
	var assigns int
	for _, tok := range toks {
		if tok.Type == TokenAssign {
			assigns++
		}
	}
	if assigns != 3 {
		t.Fatalf("assign count = %d, want 3", assigns)
	}
}

func TestStringLiteral_WithStrayClosingMarker_InsideRealComment(t *testing.T) {
	// A comment body is scanned as raw bytes (not string-aware), so a
	// quote inside a comment is just more comment text — this documents
	// that a stray "*)" written as if it were "inside a string" but
	// actually inside a comment still closes the comment, and that a
	// genuinely separate string after the comment is unaffected.
	toks := Lex(`(* he said 'hi' *) s := '*)';`)
	var strs []string
	for _, tok := range toks {
		if tok.Type == TokenString {
			strs = append(strs, tok.Literal)
		}
	}
	if len(strs) != 1 || strs[0] != "*)" {
		t.Fatalf("string literals = %v, want [*)]", strs)
	}
}

func TestLineComment_StillWorks(t *testing.T) {
	toks := Lex("x := 1; // trailing comment (* not nested *)\ny := 2;")
	got := identLits(toks)
	want := []string{"x", "y"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("identifiers = %v, want %v", got, want)
	}
}

func TestBlockComment_LineCountingAcrossNesting(t *testing.T) {
	// Line/col tracking must stay correct across a multi-line nested
	// comment so tokens after it (and any later error) land on the right
	// line.
	src := "(* line1\n(* line2\n*)\nline4 *)\nCODE"
	toks := Lex(src)
	var codeTok Token
	found := false
	for _, tok := range toks {
		if tok.Type == TokenIdent && tok.Literal == "CODE" {
			codeTok = tok
			found = true
		}
	}
	if !found {
		t.Fatalf("CODE identifier not found in %v", toks)
	}
	if codeTok.Line != 5 {
		t.Fatalf("CODE line = %d, want 5", codeTok.Line)
	}
}

func TestLexErrors_MultipleTopLevelComments_OnlyUnterminatedOneReported(t *testing.T) {
	src := "(* fine *) x := 1; (* also fine (* nested *) *) y := 2;"
	_, errs := LexErrors(src)
	if len(errs) != 0 {
		t.Fatalf("errs = %v, want none", errs)
	}
}

// Sanity check that the standard parser test suite (which exercises Lex
// indirectly through Parse) isn't affected by these changes: a plain,
// non-nested block comment must behave exactly as before.
func TestBlockComment_PlainNonNested_StillWorks(t *testing.T) {
	src := "PROGRAM p\nVAR\n\tx : INT; (* just a comment *)\nEND_VAR\nEND_PROGRAM"
	if _, err := Parse(src); err != nil {
		t.Fatalf("Parse: unexpected error: %v", err)
	}
	if !strings.Contains(src, "(*") {
		t.Fatal("sanity: test source should contain a block comment")
	}
}
