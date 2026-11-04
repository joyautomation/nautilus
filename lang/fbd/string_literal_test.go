package fbd

import (
	"errors"
	"strings"
	"testing"
)

// A string literal is text, never operators: "a&b" is a WSTRING, not
// a & b (nautilus#591). Both quote kinds read as in ST, $ escapes and all,
// and the value reaches the tag.
func TestStringLiteralsAreNotOperators(t *testing.T) {
	for _, tc := range []struct{ lit, want string }{
		{`"a&b"`, "a&b"},
		{`"a+b"`, "a+b"},
		{`'a&b'`, "a&b"},
		{`'a+b'`, "a+b"},
		{`"x AND y"`, "x AND y"},
		{`"it's"`, "it's"},
		{`'it$'s'`, "it's"},
		{`"say $"hi$""`, `say "hi"`},
		{`'50$$ & up'`, "50$ & up"},
		{`"$00C4"`, "Ä"},
		{`CONCAT("a&b", 'c+d')`, "a&bc+d"},
	} {
		src := "PROGRAM P\nVAR_EXTERNAL s : STRING; END_VAR\nFBD\n  s := " + tc.lit + "\nEND_FBD\nEND_PROGRAM\n"
		if _, err := Transpile(src); err != nil {
			t.Errorf("%s: %v", tc.lit, err)
			continue
		}
		if got := run(t, src, nil).vals["s"].S; got != tc.want {
			t.Errorf("%s: s = %q, want %q", tc.lit, got, tc.want)
		}
	}
	// A & after a string is still an operator.
	_, err := Transpile("PROGRAM P\nVAR s : STRING; END_VAR\nFBD\n  s := 'a' & 'b'\nEND_FBD\nEND_PROGRAM\n")
	var pe *ParseError
	if !errors.As(err, &pe) || !strings.Contains(pe.Msg, "not a & b") || pe.Line != 4 || pe.Col != 12 {
		t.Errorf("'a' & 'b': got %v, want the infix-AND error at 4:12", err)
	}
}

// A string that does not close on its line, or holds an unknown $ escape,
// says so at the string, in the .fbd file's own line numbers.
func TestBadStringLiteralSaysSo(t *testing.T) {
	for _, tc := range []struct {
		lit, want string
		col       int
	}{
		{`'abc`, "unterminated string: close it with '", 8},
		{`"a&b`, `unterminated string: close it with "`, 8},
		{`'it$'s`, "unterminated string", 8},
		{`'a$Qb'`, "unknown string escape", 10},
	} {
		src := "PROGRAM P\nVAR s : STRING; END_VAR\nFBD\n  s := " + tc.lit + "\nEND_FBD\nEND_PROGRAM\n"
		_, err := Transpile(src)
		var pe *ParseError
		if !errors.As(err, &pe) {
			t.Errorf("%s: want a ParseError, got %v", tc.lit, err)
			continue
		}
		if !strings.Contains(pe.Msg, tc.want) || pe.Line != 4 || pe.Col != tc.col {
			t.Errorf("%s: %d:%d %q, want 4:%d %q", tc.lit, pe.Line, pe.Col, pe.Msg, tc.col, tc.want)
		}
	}
}

// A network title is a string literal too: $ escapes read, and a title
// written back by an edit keeps its $ as $$.
func TestNetworkTitleEscapes(t *testing.T) {
	if got := networkLine("  ", "50$ budget"); got != "  NETWORK '50$$ budget'\n" {
		t.Errorf("networkLine = %q", got)
	}
	src := "PROGRAM P\nVAR x : BOOL; y : BOOL; END_VAR\nFBD\n  NETWORK '50$$ budget'\n  y := x\nEND_FBD\nEND_PROGRAM\n"
	m, err := Graph(src)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Networks) != 1 || m.Networks[0].Title != "50$ budget" {
		t.Errorf("networks = %+v, want one titled %q", m.Networks, "50$ budget")
	}
}

// Retyping a constant to a string the lexer cannot read is refused: the
// edit would write a file that does not parse. A good string is taken.
func TestEditSetLiteralRefusesABadString(t *testing.T) {
	for _, bad := range []string{`'abc`, `'a$Qb'`, `"open`} {
		if _, err := ApplyEdit(editSrc, EditOp{Type: "setLiteral", Node: "k:0", Value: bad}); err == nil {
			t.Errorf("setLiteral %s was accepted", bad)
		}
	}
	for _, good := range []string{`'it$'s'`, `"a&b"`} {
		if _, err := ApplyEdit(editSrc, EditOp{Type: "setLiteral", Node: "k:0", Value: good}); err != nil {
			t.Errorf("setLiteral %s: %v", good, err)
		}
	}
}
