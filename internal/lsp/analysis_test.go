package lsp

import (
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/internal/stproject"
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

func TestAnalyzeFBDMapsDiagnosticsToSource(t *testing.T) {
	// "bogus" is undeclared; the diagnostic must land on the .fbd line that
	// reads it (line 6, 0-based 5), not on the transpiled ST line.
	src := `PROGRAM Latch
VAR_EXTERNAL
  Start : BOOL; Run : BOOL;
END_VAR
FBD
  Run := AND(Start, bogus)
END_FBD
END_PROGRAM
`
	a := analyzeFBD(src, "", 0)
	if len(a.Diags) != 1 {
		t.Fatalf("expected 1 diagnostic, got %v", a.Diags)
	}
	d := a.Diags[0]
	if !strings.Contains(d.Message, "undeclared identifier") {
		t.Errorf("message = %q", d.Message)
	}
	if d.Range.Start.Line != 5 {
		t.Errorf("diagnostic on 0-based line %d, want 5 (the netlist statement)", d.Range.Start.Line)
	}
}

func TestAnalyzeFBDParseError(t *testing.T) {
	src := "PROGRAM P\nFBD\n  x := \nEND_FBD\nEND_PROGRAM\n" // empty RHS
	a := analyzeFBD(src, "", 0)
	if len(a.Diags) != 1 {
		t.Fatalf("expected 1 diagnostic, got %v", a.Diags)
	}
	d := a.Diags[0]
	if d.Source != "nautilus-fbd" || d.Severity != SeverityError {
		t.Errorf("source/severity = %q/%d", d.Source, d.Severity)
	}
	if d.Range.Start.Line != 2 { // 0-based: the "x :=" line
		t.Errorf("diagnostic on 0-based line %d, want 2", d.Range.Start.Line)
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

// ─── SFC ────────────────────────────────────────────────────────────────────

// sfcWorkedExample is the design doc's §1.2 complete worked example, verbatim.
const sfcWorkedExample = `PROGRAM TankBatch
VAR_EXTERNAL
  Start     : BOOL;
  Abort     : BOOL;
  Level     : REAL;
  TempC     : REAL;
  FillSP    : REAL;
  EmptySP   : REAL;
  HeatSP    : REAL;
  FillValve : BOOL;
  DrainValve: BOOL;
  Heater    : BOOL;
  Mixer     : BOOL;
  RunLamp   : BOOL;
  AbortLamp : BOOL;
  BatchCount: INT;
END_VAR
VAR
  mixT : TON;   (* referenced by the Mix action body *)
END_VAR
SFC

  INITIAL_STEP Idle:
    N  RunLamp;              (* boolean action: RunLamp := Idle.X ... see §2.5 *)
    R  AbortLamp;            (* clear the abort lamp on return to Idle *)
  END_STEP

  STEP Fill:
    N  RunLamp;
    N  FillValve;            (* valve open while Fill active *)
  END_STEP

  STEP Heat:
    N  RunLamp;
    N  Heater;
  END_STEP

  STEP Mix:
    N  RunLamp;
    N  Stir;                 (* ACTION block, below *)
  END_STEP

  STEP Drain:
    N   RunLamp;
    N   DrainValve;
    P1  CountBatch;          (* pulse: run its body once on activation *)
  END_STEP

  (* --- transitions --- *)

  TRANSITION t_start FROM Idle TO Fill := Start AND NOT Abort;
  END_TRANSITION

  (* alternative divergence out of Fill: abort has priority (declared first) *)
  TRANSITION t_abort FROM Fill TO Idle := Abort;
  END_TRANSITION
  TRANSITION t_full  FROM Fill TO (Heat, Mix) := Level >= FillSP;   (* simultaneous divergence *)
  END_TRANSITION

  (* simultaneous convergence: fires only when BOTH Heat and Mix are active *)
  TRANSITION t_done FROM (Heat, Mix) TO Drain := (TempC >= HeatSP) AND mixT.Q;
  END_TRANSITION

  TRANSITION t_empty FROM Drain TO Idle := Level <= EmptySP;
  END_TRANSITION

  (* --- action blocks (ST bodies) --- *)

  ACTION Stir:
    Mixer := Mix.X;                  (* track the step: drops to FALSE on the final scan *)
    mixT(IN := Mix.X, PT := T#30S);  (* mixing timer; falling edge resets it (§2.5.1) *)
  END_ACTION

  ACTION CountBatch:
    BatchCount := BatchCount + 1;     (* runs exactly once, on Drain activation *)
  END_ACTION

  (* S/R stored-action demo lives on the lamp: an operator-visible latch *)
  ACTION HoldAbort:
  END_ACTION

END_SFC
END_PROGRAM
`

func TestAnalyzeSFCWorkedExampleClean(t *testing.T) {
	a := analyzeSFC(sfcWorkedExample, "", 0)
	if len(a.Diags) != 0 {
		t.Fatalf("expected no diagnostics for the §1.2 worked example, got %+v", a.Diags)
	}
}

func TestAnalyzeSFCConditionSemanticError(t *testing.T) {
	// "Bogus" is undeclared; it's referenced from t1's condition, on line 12
	// (1-based) / 0-based 11 — the TRANSITION keyword line, since a
	// (here single-line) condition maps to the transition's source line.
	src := `PROGRAM P
VAR_EXTERNAL
  Start : BOOL;
  Run   : BOOL;
END_VAR
SFC
  INITIAL_STEP Idle:
  END_STEP
  STEP Run_Step:
    N Run;
  END_STEP
  TRANSITION t1 FROM Idle TO Run_Step := Bogus;
  END_TRANSITION
  TRANSITION t2 FROM Run_Step TO Idle := NOT Start;
  END_TRANSITION
END_SFC
END_PROGRAM
`
	a := analyzeSFC(src, "", 0)
	var found bool
	for _, d := range a.Diags {
		if strings.Contains(d.Message, "undeclared identifier") {
			found = true
			if d.Range.Start.Line != 11 {
				t.Errorf("diagnostic on 0-based line %d, want 11 (the TRANSITION line)", d.Range.Start.Line)
			}
		}
	}
	if !found {
		t.Fatalf("expected an undeclared-identifier diagnostic, got %+v", a.Diags)
	}
}

func TestAnalyzeSFCActionBodySemanticError(t *testing.T) {
	// "Bogus" is undeclared, referenced from the ACTION DoIt body; that body
	// statement is on 0-based line 18 (1-based 19) — action bodies map
	// per-line, unlike conditions, so the diagnostic lands on the exact
	// offending line rather than the ACTION keyword's line.
	src := `PROGRAM P
VAR_EXTERNAL
  Start : BOOL;
END_VAR
VAR
  Cnt : INT;
END_VAR
SFC
  INITIAL_STEP Idle:
  END_STEP
  STEP Run_Step:
    N DoIt;
  END_STEP
  TRANSITION t1 FROM Idle TO Run_Step := Start;
  END_TRANSITION
  TRANSITION t2 FROM Run_Step TO Idle := NOT Start;
  END_TRANSITION
  ACTION DoIt:
    Cnt := Bogus;
  END_ACTION
END_SFC
END_PROGRAM
`
	a := analyzeSFC(src, "", 0)
	var found bool
	for _, d := range a.Diags {
		if strings.Contains(d.Message, "undeclared identifier") {
			found = true
			if d.Range.Start.Line != 18 {
				t.Errorf("diagnostic on 0-based line %d, want 18 (the action body statement)", d.Range.Start.Line)
			}
		}
	}
	if !found {
		t.Fatalf("expected an undeclared-identifier diagnostic, got %+v", a.Diags)
	}
}

func TestAnalyzeSFCStructuralErrorStillReported(t *testing.T) {
	// "Ghost" is not a declared step: a structural (§5.1) diagnostic from
	// lang/sfc.Check must still surface even though the chart also proceeds
	// through the transpile+ST hop.
	src := `PROGRAM P
VAR_EXTERNAL
  Start : BOOL;
END_VAR
SFC
  INITIAL_STEP Idle:
  END_STEP
  TRANSITION t1 FROM Ghost TO Idle := Start;
  END_TRANSITION
END_SFC
END_PROGRAM
`
	a := analyzeSFC(src, "", 0)
	var found bool
	for _, d := range a.Diags {
		if d.Source == "nautilus-sfc" && strings.Contains(d.Message, "unknown step") {
			found = true
			if d.Severity != SeverityError {
				t.Errorf("severity = %d, want error", d.Severity)
			}
		}
	}
	if !found {
		t.Fatalf("expected a structural 'unknown step' diagnostic, got %+v", a.Diags)
	}
}

func TestAnalyzeSFCHoverVariableInCondition(t *testing.T) {
	a := analyzeSFC(sfcWorkedExample, "", 0)
	// Level is VAR_EXTERNAL REAL, referenced only from t_full's condition
	// ("Level >= FillSP") and t_empty's — never declared anywhere else in the
	// generated ST body, so a successful lookup demonstrates hover works
	// inside a transition condition, not just the header.
	sym := a.lookup("Level", 1)
	if sym == nil || sym.Datatype != "REAL" {
		t.Fatalf("lookup(Level) = %+v, want REAL", sym)
	}
	sym = a.lookup("level", 1) // IEC case-insensitivity
	if sym == nil || sym.Name != "Level" {
		t.Fatalf("case-insensitive lookup(level) = %+v", sym)
	}
}

func TestAnalyzeSFCStepPseudoSymbol(t *testing.T) {
	a := analyzeSFC(sfcWorkedExample, "", 0)
	sym := a.lookup("Fill", 1)
	if sym == nil || sym.Datatype != "STEP" || sym.BlockKind != "SFC step" {
		t.Fatalf("lookup(Fill) = %+v, want STEP/\"SFC step\"", sym)
	}
	// Fill's step position is in .sfc-source coordinates straight off the
	// AST — no line-map remap — so go-to-definition lands on the real
	// "STEP Fill:" line (1-based 28 in sfcWorkedExample).
	if sym.Pos.Line != 28 {
		t.Errorf("Fill step Pos.Line = %d, want 28 (the STEP Fill: line)", sym.Pos.Line)
	}

	// The Step.X / Step.T idiom: "Fill." resolves through the synthetic
	// "STEP" type to X (BOOL) and T (TIME) member completions.
	typ, ok := a.resolveChain("Fill", nil, 1)
	if !ok || typ != "STEP" {
		t.Fatalf("resolveChain(Fill) = %q,%v, want STEP,true", typ, ok)
	}
	labels := labelsOf(a.memberCompletions(typ))
	if !contains(labels, "X") || !contains(labels, "T") {
		t.Errorf("Fill. completions = %v, want X and T", labels)
	}
}

// A `.ld` file that DEFINES FUNCTION_BLOCKs analyses cleanly, and a
// diagnostic inside a block's rung lands on that rung — the LD → FBD → ST
// line map composes through the extra POU exactly as it does for one.
func TestAnalyzeLDFunctionBlocks(t *testing.T) {
	clean := `FUNCTION_BLOCK PumpSeq
VAR_INPUT  Start : BOOL; Stop : BOOL; END_VAR
VAR_OUTPUT Run : BOOL; END_VAR
LD
  RUNG seal  [ Start | Run ] /Stop ( Run )
END_LD
END_FUNCTION_BLOCK

PROGRAM Main
VAR_EXTERNAL Cmd : BOOL; Halt : BOOL; Motor : BOOL; END_VAR
VAR p1 : PumpSeq; END_VAR
LD
  RUNG call  Cmd p1:PumpSeq(Stop := Halt, Run => Motor)
END_LD
END_PROGRAM
`
	if a := analyzeLD(clean, "", 0); len(a.Diags) != 0 {
		t.Fatalf("expected no diagnostics, got %v", a.Diags)
	}

	// An undeclared reference inside the BLOCK's rung (0-based line 4).
	bad := strings.Replace(clean, "/Stop ( Run )", "/bogus ( Run )", 1)
	a := analyzeLD(bad, "", 0)
	if len(a.Diags) != 1 {
		t.Fatalf("expected 1 diagnostic, got %v", a.Diags)
	}
	if !strings.Contains(a.Diags[0].Message, "undeclared identifier") {
		t.Errorf("message = %q", a.Diags[0].Message)
	}
	if got := a.Diags[0].Range.Start.Line; got != 4 {
		t.Errorf("diagnostic on 0-based line %d, want 4 (the block's rung)", got)
	}
}

// A library `.ld` — FUNCTION_BLOCKs, no PROGRAM — analyses on its own, and
// a program that calls one resolves it through the prelude.
func TestAnalyzeLDLibraryAndCaller(t *testing.T) {
	lib := `FUNCTION_BLOCK PumpSeq
VAR_INPUT  Start : BOOL; Stop : BOOL; END_VAR
VAR_OUTPUT Run : BOOL; END_VAR
LD
  RUNG seal  [ Start | Run ] /Stop ( Run )
END_LD
END_FUNCTION_BLOCK
`
	if a := analyzeLD(lib, "", 0); len(a.Diags) != 0 {
		t.Fatalf("a PROGRAM-less ladder library should analyse clean: %v", a.Diags)
	}
	// The prelude a project composition would hand the caller.
	prelude, err := stproject.LibraryST("blocks.ld", lib)
	if err != nil {
		t.Fatal(err)
	}
	caller := `PROGRAM Main
VAR_EXTERNAL Cmd : BOOL; Halt : BOOL; Motor : BOOL; END_VAR
VAR p1 : PumpSeq; END_VAR
LD
  RUNG call  Cmd p1:PumpSeq(Stop := Halt, Run => Motor)
END_LD
END_PROGRAM
`
	a := analyzeLD(caller, prelude, strings.Count(prelude, "\n"))
	if len(a.Diags) != 0 {
		t.Fatalf("calling a library block should analyse clean: %v", a.Diags)
	}
}
