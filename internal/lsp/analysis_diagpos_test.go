package lsp

import (
	"strings"
	"testing"
)

// TestAnalyzeDiagnosticCoversOffendingName covers issue #141: the squiggle
// for an undeclared identifier (or an unknown FB member) covers exactly that
// name, so the hover lands where the user points, not on the statement's
// first token.
func TestAnalyzeDiagnosticCoversOffendingName(t *testing.T) {
	const header = "PROGRAM P\nVAR\n  x : INT;\n  Heater : BOOL;\n  settle : TON;\nEND_VAR\n"
	cases := []struct {
		name string
		line string // the offending statement, placed on 0-based line 6
		word string // the text the range must cover exactly
	}{
		{"IF condition", "IF Heatr THEN x := 1; END_IF;", "Heatr"},
		{"compound IF condition", "IF Heater AND x > 0 OR Heatr THEN x := 1; END_IF;", "Heatr"},
		{"call argument", "x := ABS(Levle);", "Levle"},
		{"FB call argument", "settle(IN := Heatr, PT := T#1s);", "Heatr"},
		{"right of assignment", "x := x + 1 + Levle;", "Levle"},
		{"left of assignment", "    Levle := x;", "Levle"},
		{"unknown FB member", "Heater := Heater OR settle.Qx;", "Qx"},
		{"unknown function", "x := 1 + FROB(x);", "FROB"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := header + tc.line + "\nEND_PROGRAM\n"
			a := analyze(src, "", 0)
			if len(a.Diags) != 1 {
				t.Fatalf("diags = %+v, want 1", a.Diags)
			}
			r := a.Diags[0].Range
			if r.Start.Line != 6 || r.End.Line != 6 {
				t.Fatalf("range %+v not on 0-based line 6", r)
			}
			if got := tc.line[r.Start.Character:r.End.Character]; got != tc.word {
				t.Errorf("range covers %q, want %q (msg %q)", got, tc.word, a.Diags[0].Message)
			}
			if strings.HasPrefix(a.Diags[0].Message, "line ") {
				t.Errorf("message keeps the line prefix: %q", a.Diags[0].Message)
			}
		})
	}
}

// With a prelude in front (sibling library files), the span is shifted back
// into the user's file along with the start.
func TestAnalyzeDiagnosticSpanWithPrelude(t *testing.T) {
	prelude := "TYPE\n  Header_Type : STRUCT\n    Valid : BOOL;\n  END_STRUCT;\nEND_TYPE\n"
	src := "PROGRAM Main\nVAR_EXTERNAL\n  H : Header_Type;\n  Ok : BOOL;\nEND_VAR\nOk := H.Valid AND Nope;\nEND_PROGRAM\n"
	a := analyze(src, prelude, strings.Count(prelude, "\n"))
	if len(a.Diags) != 1 {
		t.Fatalf("diags = %+v, want 1", a.Diags)
	}
	r := a.Diags[0].Range
	want := Range{Start: Position{Line: 5, Character: 18}, End: Position{Line: 5, Character: 22}}
	if r != want {
		t.Errorf("range = %+v, want %+v (\"Nope\")", r, want)
	}
	// An unknown struct field lands on the field.
	src = strings.Replace(src, "Nope", "H.Vlaid", 1)
	a = analyze(src, prelude, strings.Count(prelude, "\n"))
	if len(a.Diags) != 1 {
		t.Fatalf("diags = %+v, want 1", a.Diags)
	}
	r = a.Diags[0].Range
	line := "Ok := H.Valid AND H.Vlaid;"
	if got := line[r.Start.Character:r.End.Character]; r.Start.Line != 5 || got != "Vlaid" {
		t.Errorf("range %+v covers %q, want \"Vlaid\" on line 5", r, got)
	}
}
