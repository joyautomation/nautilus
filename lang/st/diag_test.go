package st

import (
	"errors"
	"testing"
)

func TestParseErrorPos(t *testing.T) {
	// A parser error that embeds a line number resolves to that line.
	_, err := Parse("PROGRAM P\nVAR\n  x : REAL;\nEND_VAR\nx := ;\nEND_PROGRAM\n")
	if err == nil {
		t.Fatal("expected a parse error")
	}
	pos, ok := ParseErrorPos(err)
	if !ok {
		t.Fatalf("no position extracted from %q", err)
	}
	if pos.Line != 5 {
		t.Errorf("line = %d, want 5 (err: %v)", pos.Line, err)
	}

	// No position when the message carries none.
	if _, ok := ParseErrorPos(errors.New("something went wrong")); ok {
		t.Error("extracted a position from a positionless error")
	}
	if _, ok := ParseErrorPos(nil); ok {
		t.Error("extracted a position from nil")
	}
}

func TestKeywordAndTypeNames(t *testing.T) {
	// The accessors expose the full compiler tables (guards the LSP against
	// drift). Sanity-check a few representative entries and the counts.
	kw := KeywordNames()
	if len(kw) != len(keywords) {
		t.Errorf("KeywordNames len = %d, want %d", len(kw), len(keywords))
	}
	types := ScalarTypeNames()
	if len(types) != len(scalarTypeNames) {
		t.Errorf("ScalarTypeNames len = %d, want %d", len(types), len(scalarTypeNames))
	}
	has := func(s []string, want string) bool {
		for _, x := range s {
			if x == want {
				return true
			}
		}
		return false
	}
	for _, want := range []string{"LINT", "WSTRING", "TOD", "LTIME"} {
		if !has(types, want) {
			t.Errorf("ScalarTypeNames missing %s", want)
		}
	}
	if !has(kw, "FUNCTION_BLOCK") {
		t.Error("KeywordNames missing FUNCTION_BLOCK")
	}
}
