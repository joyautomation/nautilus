package lsp

import (
	"strings"
	"testing"
)

// Note: the parser only recognizes FUNCTION_BLOCK declarations before
// END_PROGRAM, so the canonical layout is FBs first, program last.
const goodSrc = `FUNCTION_BLOCK PIController
VAR_INPUT
  sp : REAL;
  pv : REAL;
END_VAR
VAR_OUTPUT
  out : REAL;
END_VAR
VAR
  acc : REAL;
END_VAR
acc := acc + (sp - pv);
out := acc;
END_FUNCTION_BLOCK

PROGRAM HeatedTank
VAR_EXTERNAL
  LevelPct : REAL;
  PumpRun : BOOL;
END_VAR
VAR
  err : REAL;
  pi : PIController;
END_VAR
IF LevelPct < 40.0 THEN
  PumpRun := TRUE;
END_IF;
err := 1.0;
END_PROGRAM
`

func TestAnalyzeGoodProgram(t *testing.T) {
	a := analyze(goodSrc)
	if len(a.Diags) != 0 {
		t.Fatalf("expected no diagnostics, got %v", a.Diags)
	}
	// Program-scope var, referenced from the program body
	sym := a.lookup("LevelPct", 25)
	if sym == nil || sym.Pos.Line != 18 || sym.BlockKind != "VAR_EXTERNAL" {
		t.Fatalf("LevelPct lookup = %+v", sym)
	}
	// FB-local var resolved from inside the FB body
	sym = a.lookup("acc", 12)
	if sym == nil || sym.Container != "PIController" {
		t.Fatalf("acc lookup from FB body = %+v", sym)
	}
	// The FB type itself
	sym = a.lookup("PIController", 23)
	if sym == nil || sym.BlockKind != "FUNCTION_BLOCK" {
		t.Fatalf("PIController lookup = %+v", sym)
	}
	// IEC case-insensitivity
	sym = a.lookup("levelpct", 25)
	if sym == nil || sym.Name != "LevelPct" {
		t.Fatalf("case-insensitive lookup = %+v", sym)
	}
}

func TestAnalyzeUndeclaredIdentifier(t *testing.T) {
	src := "PROGRAM P\nVAR\n  x : REAL;\nEND_VAR\nx := y + 1.0;\nEND_PROGRAM\n"
	a := analyze(src)
	if len(a.Diags) != 1 {
		t.Fatalf("expected 1 diagnostic, got %v", a.Diags)
	}
	d := a.Diags[0]
	if d.Range.Start.Line != 4 { // 0-based: the `x := y` line
		t.Errorf("diagnostic on line %d, want 4", d.Range.Start.Line)
	}
	if !strings.Contains(d.Message, "undeclared identifier") {
		t.Errorf("message = %q", d.Message)
	}
	if d.Severity != SeverityError {
		t.Errorf("severity = %d", d.Severity)
	}
}

func TestAnalyzeParseError(t *testing.T) {
	src := "PROGRAM P\nVAR\n  x : REAL;\nEND_VAR\nx := ;\nEND_PROGRAM\n" // empty RHS
	a := analyze(src)
	if len(a.Diags) != 1 {
		t.Fatalf("expected 1 diagnostic, got %v", a.Diags)
	}
	if a.Diags[0].Severity != SeverityError {
		t.Errorf("severity = %d", a.Diags[0].Severity)
	}
}

func TestScopePreference(t *testing.T) {
	// `n` declared both at program scope and inside the FB: a reference in
	// the FB body must resolve to the FB's local.
	src := `FUNCTION_BLOCK FB1
VAR
  n : BOOL;
END_VAR
n := TRUE;
END_FUNCTION_BLOCK

PROGRAM P
VAR
  n : REAL;
END_VAR
n := 1.0;
END_PROGRAM
`
	a := analyze(src)
	inFB := a.lookup("n", 5)
	if inFB == nil || inFB.Container != "FB1" || inFB.Datatype != "BOOL" {
		t.Fatalf("FB-body lookup = %+v", inFB)
	}
	inProg := a.lookup("n", 12)
	if inProg == nil || inProg.Container != "" || inProg.Datatype != "REAL" {
		t.Fatalf("program-body lookup = %+v", inProg)
	}
}

func TestWordAt(t *testing.T) {
	text := "  PumpRun := TRUE;\n"
	for _, c := range []int{2, 5, 9} {
		word, r := wordAt(text, Position{Line: 0, Character: c})
		if word != "PumpRun" {
			t.Errorf("wordAt char %d = %q, want PumpRun", c, word)
		}
		if r.Start.Character != 2 || r.End.Character != 9 {
			t.Errorf("range = %+v", r)
		}
	}
	if w, _ := wordAt(text, Position{Line: 0, Character: 11}); w != "" {
		t.Errorf("wordAt on ':=' = %q, want empty", w)
	}
}

func TestStaticCompletionsIncludeBuiltins(t *testing.T) {
	items := staticCompletions()
	want := map[string]bool{"IF": false, "TON": false, "REAL": false}
	for _, it := range items {
		if _, ok := want[it.Label]; ok {
			want[it.Label] = true
		}
	}
	for label, seen := range want {
		if !seen {
			t.Errorf("static completions missing %s", label)
		}
	}
}
