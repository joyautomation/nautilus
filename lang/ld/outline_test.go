package ld

import (
	"fmt"
	"strings"
	"testing"
)

func TestRungsWithoutParsingElements(t *testing.T) {
	src := `FUNCTION_BLOCK F
VAR_INPUT
    A : BOOL;
END_VAR
LD
  RUNG one (* first *)
    A ( B
  // a note for the next rung
  RUNG
    A ( C )
END_LD
END_FUNCTION_BLOCK
`
	var got []string
	for _, r := range Rungs(src) {
		got = append(got, fmt.Sprintf("%s %q %d:%d-%d name@%d pou=%s", r.Name, r.Comment, r.Line, r.Col, r.EndLine, r.NameCol, r.POU))
	}
	// The first rung's elements do not parse (Graph fails); the rungs
	// are still there, and the note does not stretch the first one.
	want := `one "first" 6:3-7 name@8 pou=F
rung9 "" 9:3-10 name@0 pou=F`
	if g := strings.Join(got, "\n"); g != want {
		t.Errorf("got:\n%s\nwant:\n%s", g, want)
	}
	if _, err := Graph(src); err == nil {
		t.Error("the fixture parses; it should not")
	}
}
