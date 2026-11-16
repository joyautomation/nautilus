package ld

import (
	"strings"
	"testing"
)

// A (* … *) comment inside an assignment box belongs to the assignment it
// follows: the compiler ignores it, PrintAssignments keeps it, and the
// Logix writer reads its `@logix COP` / `@logix CPS` pragma. A separator or
// bracket inside the comment is text; a comment after the last `;` joins
// the assignment before it.
func TestAssignmentComments(t *testing.T) {
	as, err := ParseAssignments("B[1] := A[0]; B[2] := A[1] (* @logix COP *); n := (m + 1) (* why; [not] a ) split *)")
	if err != nil {
		t.Fatal(err)
	}
	if len(as) != 3 {
		t.Fatalf("%d assignments, want 3: %+v", len(as), as)
	}
	if as[0].Comment != "" || as[1].Comment != "@logix COP" || as[2].Comment != "why; [not] a ) split" {
		t.Errorf("comments %q %q %q", as[0].Comment, as[1].Comment, as[2].Comment)
	}
	if !as[1].Pragma("logix COP") || as[1].Pragma("logix CPS") || as[0].Pragma("logix COP") {
		t.Error("Pragma reads the wrong words")
	}
	if as[2].Value.IEC() != "m + 1" && as[2].Value.IEC() != "(m + 1)" {
		t.Errorf("value %q", as[2].Value.IEC())
	}
	printed := PrintAssignments(as)
	again, err := ParseAssignments(printed)
	if err != nil || PrintAssignments(again) != printed {
		t.Errorf("does not round-trip: %q → %v %q", printed, err, PrintAssignments(again))
	}

	trailing, err := ParseAssignments("B := A; (* @logix CPS *)")
	if err != nil || len(trailing) != 1 || !trailing[0].Pragma("LOGIX cps") {
		t.Errorf("trailing comment: %+v %v", trailing, err)
	}
	if _, err := ParseAssignments("(* only a note *)"); err == nil {
		t.Error("a box with a comment and no assignment should be refused")
	}
	// A comment marker inside a string is the string's.
	s, err := ParseAssignments("Msg := '(* not a comment *)'")
	if err != nil || s[0].Comment != "" || !strings.Contains(s[0].Value.IEC(), "(* not a comment *)") {
		t.Errorf("string: %+v %v", s, err)
	}
}

// The pragma survives a rung parse and the graph the editor draws from.
func TestAssignmentCommentInARung(t *testing.T) {
	src := `PROGRAM Main
VAR
    Go : BOOL;
    A : ARRAY [0..3] OF DINT;
    B : ARRAY [0..3] OF DINT;
END_VAR
LD
  RUNG r (* a copy *)
    Go { B := A (* @logix CPS {braces} '} *) }
END_LD
END_PROGRAM
`
	g, err := Graph(src)
	if err != nil {
		t.Fatal(err)
	}
	var text string
	for _, e := range g.Rungs[0].Elements {
		if e.Kind == "assign" {
			text = e.Text
		}
	}
	if text != "B := A (* @logix CPS {braces} '} *)" {
		t.Errorf("assign text %q", text)
	}
}
