package st

import (
	"strings"
	"testing"
)

// What used to parse without a word (nautilus#609): a PROGRAM with no
// END_PROGRAM, a (* comment that never closes, and an expression followed
// by another operand. Each is now an error saying what is wrong.
func TestParseRejectsWhatItUsedToSkip(t *testing.T) {
	const head = "PROGRAM P\nVAR a : INT; b : INT; s : INT; END_VAR\n"
	for _, tc := range []struct{ name, src, want string }{
		{"no END_PROGRAM", head + "s := 1;\n", "line 1: PROGRAM P has no END_PROGRAM"},
		{"unclosed comment at the end", head + "s := 1;\nEND_PROGRAM\n(* open\n", "line 5:1: unterminated block comment opened"},
		{"unclosed comment swallows END_PROGRAM", head + "s := 1; (* open\nEND_PROGRAM\n", "line 3:9: unterminated block comment opened"},
		{"two names in a row", head + "s := a b;\nEND_PROGRAM\n", `line 3:8: expected an operator or ';' after the expression, got "b"`},
		{"a name then a number", head + "s := a 5;\nEND_PROGRAM\n", `line 3:8: expected an operator or ';' after the expression, got "5"`},
		{"a name alone", head + "s := a;\nb;\nEND_PROGRAM\n", `line 4:1: "b" alone is not a statement`},
		{"an FB instance not called", "PROGRAM P\nVAR t1 : TON; END_VAR\n  t1;\nEND_PROGRAM\n", `line 3:3: "t1" alone is not a statement: assign it (t1 := …), or call it as t1()`},
		{"a stray number", head + "s := a;\n5;\nEND_PROGRAM\n", `line 4:1: unexpected "5"`},
		{"a stray keyword", head + "s := a;\nEND_VAR\nEND_PROGRAM\n", `line 4:1: unexpected "END_VAR"`},
	} {
		_, err := Parse(tc.src)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err %v, want it to contain %q", tc.name, err, tc.want)
		}
	}
}

// ParseErrorPos reads the column a message carries, and the line alone
// when it carries none.
func TestParseErrorPosReadsTheColumn(t *testing.T) {
	for src, want := range map[string]Pos{
		"PROGRAM P\nVAR a : INT; s : INT; END_VAR\ns := a a;\nEND_PROGRAM\n": {Line: 3, Col: 8},
		"PROGRAM P\nVAR s : STRING; END_VAR\ns := 'abc;\nEND_PROGRAM\n":      {Line: 3, Col: 6},
		"PROGRAM P\nVAR s : INT; END_VAR\ns := 1;\n":                         {Line: 1, Col: 1},
	} {
		_, err := Parse(src)
		if got, ok := ParseErrorPos(err); !ok || got != want {
			t.Errorf("%q: ParseErrorPos(%v) = %+v, want %+v", src, err, got, want)
		}
	}
}

// What still parses: ';' is optional before a new line, a call is a
// statement, and a library file has no END_PROGRAM to miss.
func TestParseStillAccepts(t *testing.T) {
	for _, src := range []string{
		"PROGRAM P\nVAR a : INT; s : INT; END_VAR\ns := a\na := s + 1\nEND_PROGRAM\n",
		"PROGRAM P\nVAR a : INT; s : INT; t : TON; END_VAR\nt(IN := a > 0, PT := T#1s);\nIF a > 0 THEN s := 1 END_IF;\nEND_PROGRAM\n",
		"PROGRAM P\nVAR s : INT; END_VAR\ns := (1 + 2) * 3; (* a (* nested *) comment *)\nEND_PROGRAM\n",
		"FUNCTION F : INT\nVAR_INPUT x : INT; END_VAR\nF := x;\nEND_FUNCTION\n",
		"PROGRAM P\nVAR s : INT; END_VAR\nREGION init\ns := 1 END_REGION\nIF s > 0 THEN s := 2 END_IF\nEND_PROGRAM\n",
	} {
		if _, err := Parse(src); err != nil {
			t.Errorf("%q: %v", src, err)
		}
	}
}
