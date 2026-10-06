package st

import (
	"regexp"
	"strings"
	"testing"
)

// #178: a parse error must never print a token kind's number ("expected 79,
// got ..."). Walk the whole kind table: every kind has a name, no name is a
// bare number, and the fallback for an out-of-table kind is words too.
func TestTokenNamesCoverEveryKind(t *testing.T) {
	digits := regexp.MustCompile(`^[0-9]+$`)
	seen := map[string]TokenType{}
	for k := TokenType(0); k < tokenKindCount; k++ {
		name := k.String()
		if name == "" || name == "an unknown token" {
			t.Errorf("token kind %d has no name in tokenNames", int(k))
			continue
		}
		if digits.MatchString(name) {
			t.Errorf("token kind %d names itself as a number: %q", int(k), name)
		}
		if prev, dup := seen[name]; dup {
			t.Errorf("token kinds %d and %d share the name %q", int(prev), int(k), name)
		}
		seen[name] = k
	}
	if got := TokenType(9999).String(); digits.MatchString(got) || strings.Contains(got, "9999") {
		t.Errorf("out-of-table kind prints its number: %q", got)
	}
	// Every keyword the lexer reserves is named by its own spelling.
	for kw, k := range keywords {
		if k.String() != kw {
			t.Errorf("keyword %s: kind names itself %q", kw, k.String())
		}
	}
}

// The sources that leaked numbers in #178 (and a few more shapes that reach
// Parser.expect) now say what was expected in words.
func TestParseErrorsNameTokens(t *testing.T) {
	number := regexp.MustCompile(`expected [0-9]+`)
	cases := map[string]string{
		"stray comment close in VAR": "PROGRAM P\nVAR\n  x : INT; *)\nEND_VAR\nEND_PROGRAM\n",
		"missing THEN":               "PROGRAM P\nVAR x : INT; END_VAR\nIF x > 1 x := 2; END_IF;\nEND_PROGRAM\n",
		"FB without name":            "FUNCTION_BLOCK\nEND_FUNCTION_BLOCK\n",
		"array without OF":           "PROGRAM P\nVAR a : ARRAY[1..3] INT; END_VAR\nEND_PROGRAM\n",
		"index not closed":           "PROGRAM P\nVAR a : ARRAY[1..3] OF INT; END_VAR\na[1 := 2;\nEND_PROGRAM\n",
		"var name is a number":       "PROGRAM P\nVAR 3x : INT; END_VAR\nEND_PROGRAM\n",
	}
	for name, src := range cases {
		_, err := Parse(src)
		if err == nil {
			t.Errorf("%s: expected a parse error", name)
			continue
		}
		if number.MatchString(err.Error()) {
			t.Errorf("%s: error leaks a token number: %v", name, err)
		}
	}
	_, err := Parse("PROGRAM P\nVAR x : INT; END_VAR\nIF x > 1 x := 2; END_IF;\nEND_PROGRAM\n")
	if err == nil || !strings.Contains(err.Error(), "expected THEN") {
		t.Errorf("missing THEN: want \"expected THEN\", got %v", err)
	}
}
