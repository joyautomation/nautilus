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
	a := analyze(goodSrc, "", 0)
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
	a := analyze(src, "", 0)
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
	a := analyze(src, "", 0)
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
	a := analyze(src, "", 0)
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
	// Keyword, standard FB, common type, and — the regression this guards —
	// elementary types that the old hardcoded list omitted but the compiler
	// accepts (LINT, WSTRING, TOD, ...).
	want := map[string]bool{
		"IF": false, "TON": false, "REAL": false,
		"LINT": false, "WSTRING": false, "TOD": false, "LTIME": false,
	}
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

	// A name that is both a keyword token and an elementary type (REAL) must
	// appear exactly once, as the type.
	var realCount, realKind int
	for _, it := range items {
		if it.Label == "REAL" {
			realCount++
			realKind = it.Kind
		}
	}
	if realCount != 1 {
		t.Errorf("REAL appears %d times, want 1", realCount)
	}
	if realKind != CompletionKindStruct {
		t.Errorf("REAL kind = %d, want type (%d)", realKind, CompletionKindStruct)
	}
}

func TestAnalyzeWithPreludeResolvesCrossFileTypes(t *testing.T) {
	prelude := "TYPE\n  Header_Type : STRUCT\n    Valid : BOOL;\n  END_STRUCT;\nEND_TYPE\n"
	src := "PROGRAM Main\nVAR_EXTERNAL\n  H : Header_Type;\n  Ok : BOOL;\nEND_VAR\nOk := H.Valid;\nEND_PROGRAM\n"

	// Without the prelude the type is unknown; with it the program is clean.
	if a := analyze(src, "", 0); len(a.Diags) == 0 {
		t.Fatalf("expected unknown-type diagnostic without prelude")
	}
	if a := analyze(src, prelude, strings.Count(prelude, "\n")); len(a.Diags) != 0 {
		t.Fatalf("unexpected diagnostics with prelude: %+v", a.Diags)
	}
}

func TestAnalyzeWithPreludeRemapsPositions(t *testing.T) {
	prelude := "TYPE\n  Header_Type : STRUCT\n    Valid : BOOL;\n  END_STRUCT;\nEND_TYPE\n"
	// Line 6 references an undeclared identifier.
	src := "PROGRAM Main\nVAR_EXTERNAL\n  H : Header_Type;\n  Ok : BOOL;\nEND_VAR\nOk := Nope;\nEND_PROGRAM\n"
	a := analyze(src, prelude, strings.Count(prelude, "\n"))
	if len(a.Diags) != 1 {
		t.Fatalf("diags = %+v, want 1", a.Diags)
	}
	if got := a.Diags[0].Range.Start.Line; got != 5 { // 0-based line of "Ok := Nope;"
		t.Fatalf("diagnostic on 0-based line %d, want 5 (position not remapped?)", got)
	}
}

func TestTypeExpansionForHover(t *testing.T) {
	prelude := "TYPE\n  Header_Type : STRUCT\n    Displacement : REAL;\n    Valid : BOOL;\n  END_STRUCT;\nEND_TYPE\n"
	src := "PROGRAM Main\nVAR_EXTERNAL\n  H : Header_Type;\n  Arr : ARRAY [0..3] OF Header_Type;\nEND_VAR\nEND_PROGRAM\n"
	a := analyze(src, prelude, strings.Count(prelude, "\n"))

	def, ok := a.typeExpansion("Header_Type")
	if !ok {
		t.Fatalf("prelude type not indexed; have %v", a.types)
	}
	for _, want := range []string{"Header_Type : STRUCT", "Displacement : REAL;", "Valid : BOOL;", "END_STRUCT"} {
		if !strings.Contains(def, want) {
			t.Errorf("expansion missing %q:\n%s", want, def)
		}
	}
	// Array declarations expand their element type; case-insensitive.
	if _, ok := a.typeExpansion("ARRAY [0..3] OF header_type"); !ok {
		t.Errorf("array-of-UDT datatype did not expand")
	}
	if _, ok := a.typeExpansion("REAL"); ok {
		t.Errorf("elementary type should not expand")
	}
}

func TestMemberContext(t *testing.T) {
	cases := []struct {
		line string
		col  int
		base string
		path []string
		ok   bool
	}{
		{"X := PIT_001.VAL", 17, "PIT_001", nil, true},
		{"X := PIT_001.", 13, "PIT_001", nil, true},
		{"X := Plt[3].Header.", 19, "Plt", []string{"Header"}, true},
		{"X := A.B.C.", 11, "A", []string{"B", "C"}, true},
		{"X := PIT_001", 12, "", nil, false},
		{"X := 3.14", 9, "", nil, false},
	}
	for _, tc := range cases {
		base, path, ok := memberContext(tc.line, tc.col)
		if ok != tc.ok || base != tc.base {
			t.Errorf("memberContext(%q,%d) = %q,%v,%v; want %q,%v,%v", tc.line, tc.col, base, path, ok, tc.base, tc.path, tc.ok)
			continue
		}
		if len(path) != len(tc.path) {
			t.Errorf("memberContext(%q,%d) path = %v, want %v", tc.line, tc.col, path, tc.path)
		}
	}
}

func TestMemberCompletionsThroughChain(t *testing.T) {
	prelude := "TYPE\n  Header_Type : STRUCT\n    Displacement : REAL;\n    Valid : BOOL;\n  END_STRUCT;\n  Plt_Type : STRUCT\n    Header : Header_Type;\n    Count : DINT;\n  END_STRUCT;\nEND_TYPE\n"
	src := "PROGRAM Main\nVAR_EXTERNAL\n  P : Plt_Type;\n  Arr : ARRAY [0..3] OF Plt_Type;\nEND_VAR\nVAR\n  T1 : TON;\nEND_VAR\nEND_PROGRAM\n"
	a := analyze(src, prelude, strings.Count(prelude, "\n"))

	// P. → Plt_Type members
	typ, ok := a.resolveChain("P", nil, 6)
	if !ok {
		t.Fatalf("resolveChain(P) failed")
	}
	labels := labelsOf(a.memberCompletions(typ))
	if !contains(labels, "Header") || !contains(labels, "Count") {
		t.Errorf("P. completions = %v", labels)
	}

	// P.Header. → nested Header_Type members
	typ, ok = a.resolveChain("P", []string{"Header"}, 6)
	if !ok || !contains(labelsOf(a.memberCompletions(typ)), "Displacement") {
		t.Errorf("P.Header. completions wrong (type %q)", typ)
	}

	// Arr (array of UDT) resolves through the element type; case-insensitive member.
	typ, ok = a.resolveChain("Arr", []string{"header"}, 6)
	if !ok || !contains(labelsOf(a.memberCompletions(typ)), "Valid") {
		t.Errorf("Arr[i].header. completions wrong (type %q)", typ)
	}

	// Builtin FB instance: T1. → TON slots.
	typ, ok = a.resolveChain("T1", nil, 6)
	if !ok {
		t.Fatalf("resolveChain(T1) failed")
	}
	labels = labelsOf(a.memberCompletions(typ))
	if !contains(labels, "Q") || !contains(labels, "ET") {
		t.Errorf("T1. completions = %v", labels)
	}
}

func labelsOf(items []CompletionItem) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.Label
	}
	return out
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
