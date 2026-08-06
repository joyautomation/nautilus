package lsp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/acceptance"
)

// theExample is the flagship manifest project: four tasks, three IEC
// languages, and the two ST expectation expressions this file is about.
const theExample = "../../examples/heated-tank-nogo"

func exampleURI(t *testing.T, name string) (uri, text string) {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join(theExample, name))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(abs)
	if err != nil {
		t.Fatal(err)
	}
	return "file://" + abs, string(raw)
}

// The region scanner must find exactly what the loader treats as an
// expression — no more (a matcher mistaken for ST would squiggle valid
// YAML) and no less (a missed one is a silent hole).
func TestRegionsMatchTheLoader(t *testing.T) {
	_, text := exampleURI(t, "heated-tank_test.yaml")
	exprs, _ := scanSuite(text)

	suite, err := acceptance.LoadSuite(os.DirFS(theExample), "heated-tank_test.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	collect := func(exps ...*acceptance.Expect) {
		for _, e := range exps {
			if e == nil {
				continue
			}
			for _, term := range e.Terms {
				if term.Expr != "" {
					want = append(want, term.Expr)
				}
			}
		}
	}
	for _, tc := range suite.Tests {
		collect(tc.Expect, tc.Always) // the single-step shorthand
		for _, s := range tc.Steps {
			collect(s.Expect, s.Always)
		}
	}
	if len(want) == 0 {
		t.Fatal("the example suite has no ST expressions — this test proves nothing")
	}

	got := make([]string, len(exprs))
	lines := strings.Split(text, "\n")
	for i, r := range exprs {
		got[i] = r.text
		// The span must actually cover the expression in the file, or the
		// squiggle lands on the wrong characters.
		if slice := lines[r.line][r.start:r.end]; slice != r.text {
			t.Errorf("region %d spans %q but its text is %q", i, slice, r.text)
		}
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("regions = %q, loader found %q", got, want)
	}
}

// The example must be clean: the acceptance bar for this feature is that a
// suite that passes shows no squiggles.
func TestExampleSuiteHasNoDiagnostics(t *testing.T) {
	s := &Server{docs: map[string]*document{}}
	uri, text := exampleURI(t, "heated-tank_test.yaml")
	td, diags := s.analyzeTest(uri, text)
	if len(diags) != 0 {
		t.Fatalf("the example suite squiggles: %+v", diags)
	}
	if td.build == nil {
		t.Fatal("the example project did not build — nothing was actually checked")
	}
	if len(td.exprs) == 0 {
		t.Fatal("no expressions found")
	}
}

// A tag that doesn't exist is the whole point: it must squiggle, on the
// right line, under the offending name rather than across the file.
func TestUnknownTagInAnExpression(t *testing.T) {
	s := &Server{docs: map[string]*document{}}
	uri, text := exampleURI(t, "heated-tank_test.yaml")
	broken := strings.Replace(text, "ABS(TempC - TempSP)", "ABS(TempX - TempSP)", 1)
	if broken == text {
		t.Fatal("the expression this test edits is gone")
	}
	_, diags := s.analyzeTest(uri, broken)
	if len(diags) != 1 {
		t.Fatalf("got %d diagnostics, want 1: %+v", len(diags), diags)
	}
	d := diags[0]
	if !strings.Contains(d.Message, "TempX") {
		t.Errorf("message %q does not name the unknown tag", d.Message)
	}
	if strings.Contains(d.Message, "line ") {
		t.Errorf("message %q leaks the generated POU's line number", d.Message)
	}
	// The squiggle covers the expression and nothing else — not the `- `
	// bullet, not the trailing YAML comment.
	line := strings.Split(broken, "\n")[d.Range.Start.Line]
	if got := line[d.Range.Start.Character:d.Range.End.Character]; got != "ABS(TempX - TempSP) < 0.5" {
		t.Errorf("squiggle covers %q, want just the expression", got)
	}
}

// A misspelled tag in KEY position — `expect: { PudmpRun: false }` — is the
// same mistake as one in an expression, and `nautilus test` already refuses
// it. It must squiggle, name the tag, and suggest the near miss.
func TestUnknownTagInKeyPosition(t *testing.T) {
	s := &Server{docs: map[string]*document{}}
	uri, text := exampleURI(t, "heated-tank_test.yaml")
	for _, tc := range []struct{ from, to, want string }{
		{"expect: { PumpRun: false }", "expect: { PudmpRun: false }", "PudmpRun"},
		{"given: { LevelPct: 35.0 }", "given: { LevelPtc: 35.0 }", "LevelPtc"},
		{"expect: { TempLowAlm: true }", "expect: { templowalm: true }", "templowalm"},
	} {
		broken := strings.Replace(text, tc.from, tc.to, 1)
		if broken == text {
			t.Fatalf("%q is no longer in the example suite", tc.from)
		}
		_, diags := s.analyzeTest(uri, broken)
		if len(diags) != 1 {
			t.Errorf("%s: got %d diagnostics, want 1: %+v", tc.want, len(diags), diags)
			continue
		}
		d := diags[0]
		if !strings.Contains(d.Message, tc.want) || !strings.Contains(d.Message, "did you mean") {
			t.Errorf("%s: message = %q", tc.want, d.Message)
		}
		line := strings.Split(broken, "\n")[d.Range.Start.Line]
		if got := line[d.Range.Start.Character:d.Range.End.Character]; got != tc.want {
			t.Errorf("%s: squiggle covers %q, want just the key", tc.want, got)
		}
	}
}

// The far more damaging failure is the opposite one: squiggling a name that
// is real. Every key the example suite uses must pass, matcher operators
// and `task.local` included.
func TestKeyCheckHasNoFalsePositives(t *testing.T) {
	s := &Server{docs: map[string]*document{}}
	uri, text := exampleURI(t, "heated-tank_test.yaml")
	// A matcher's own keys (near, tol, between, gt) are operators, and a
	// dotted name is a program local nothing can enumerate before a run.
	extra := strings.Replace(text,
		"expect: { TempLowAlm: true }",
		"expect: { TempLowAlm: true, main.integral: { gt: 0.0 } }", 1)
	if extra == text {
		t.Fatal("the expectation this test edits is gone")
	}
	td, diags := s.analyzeTest(uri, extra)
	if len(diags) != 0 {
		t.Fatalf("real names squiggled: %+v", diags)
	}
	// And the keys were actually found — a scanner that saw nothing would
	// pass this test for the wrong reason.
	var found int
	for _, k := range td.keys {
		if k.text == "TempLowAlm" || k.text == "Heater" || k.text == "LevelPct" {
			found++
		}
	}
	if found == 0 {
		t.Fatal("no tag keys were scanned at all")
	}
	for _, k := range td.keys {
		if k.text == "near" || k.text == "between" || k.text == "gt" {
			t.Errorf("matcher operator %q was scanned as a tag name", k.text)
		}
	}
}

// A syntax error inside an expression is reported too — and against the
// expression, not the YAML.
func TestSyntaxErrorInAnExpression(t *testing.T) {
	s := &Server{docs: map[string]*document{}}
	uri, text := exampleURI(t, "heated-tank_test.yaml")
	broken := strings.Replace(text, "ABS(TempC - TempSP) < 0.5", "ABS(TempC - < 0.5", 1)
	if broken == text {
		t.Fatal("the expression this test edits is gone")
	}
	_, diags := s.analyzeTest(uri, broken)
	if len(diags) != 1 {
		t.Fatalf("got %d diagnostics, want 1: %+v", len(diags), diags)
	}
	if d := diags[0]; d.Range.Start.Line != 62 {
		t.Errorf("diagnostic on line %d (0-based), want the expression's line 62", d.Range.Start.Line)
	}
}

// `Heater` has no `init:` in the manifest, so its type cannot be inferred
// from the manifest alone — the FBD program declares it. An expression
// naming it must still check, or the flagship example squiggles for a tag
// that is perfectly real.
func TestExternalsCoverTagsWithNoSeed(t *testing.T) {
	entry := projectBuildFor(filepath.Join(theExample, "nautilus.yaml"))
	if entry == nil {
		t.Fatal("the example project did not build")
	}
	if ty := entry.build.ext["Heater"]; ty != "REAL" {
		t.Errorf("Heater : %q, want REAL from the program's declaration", ty)
	}
	// A dt-tag exists only because a task names it; nothing seeds it.
	if ty := entry.build.ext["ScanDtS"]; ty == "" {
		t.Error("ScanDtS is missing — a dt-tag is a tag a test may assert on")
	}
}

// Everything outside an expression is YAML. Hovering a key must not
// produce ST documentation for it.
func TestHoverOnlyInsideExpressions(t *testing.T) {
	_, text := exampleURI(t, "heated-tank_test.yaml")
	td := &testDoc{}
	td.exprs, td.keys = scanSuite(text)
	// Line 63 (1-based) holds `- ABS(TempC - TempSP) < 0.5`.
	inside := Position{Line: 62, Character: strings.Index(strings.Split(text, "\n")[62], "TempC")}
	if td.exprAt(inside) == nil {
		t.Fatal("TempC in the expression is not inside a region")
	}
	// Line 58 (1-based) holds `expect: { TempC: { near: 65.0 } }` — a
	// matcher, not ST.
	outside := Position{Line: 57, Character: strings.Index(strings.Split(text, "\n")[57], "TempC")}
	if r := td.exprAt(outside); r != nil {
		t.Errorf("a tag matcher was treated as an expression: %q", r.text)
	}
}

func TestNamesTags(t *testing.T) {
	const src = `tests:
  - name: a test
    given: { LevelPct: 35.0 }
    expect:
      PumpRun: true
      Horn: false
    scans: 1
    steps:
      - given: { TempC: 45.0 }
        expect:
          TempLowAlm: false
`
	for _, tc := range []struct {
		line, char int
		want       bool
		why        string
	}{
		{2, 14, true, "inside the given flow mapping"},
		{2, 4, false, "on `given` itself, before the brace"},
		{4, 6, true, "a key in the expect block"},
		{5, 6, true, "a second key in the expect block"},
		{6, 4, false, "the scans key, a sibling of expect"},
		{1, 4, false, "the test name"},
		{8, 18, true, "inside a step's `- given:` flow mapping"},
		{10, 10, true, "a key under a step's expect block"},
	} {
		if got := namesTags(src, Position{Line: tc.line, Character: tc.char}); got != tc.want {
			t.Errorf("%d:%d (%s) = %v, want %v", tc.line, tc.char, tc.why, got, tc.want)
		}
	}
}

// A suite outside any project must not error or invent checks — writing one
// before `nautilus new` has run is an ordinary thing to do.
func TestSuiteWithoutAProject(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "loose_test.yaml")
	const src = "tests:\n  - name: x\n    scans: 1\n    expect:\n      - Whatever > 1\n"
	if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &Server{docs: map[string]*document{}}
	td, diags := s.analyzeTest("file://"+p, src)
	if len(diags) != 0 {
		t.Fatalf("got diagnostics with no project to check against: %+v", diags)
	}
	if len(td.exprs) != 1 {
		t.Fatalf("got %d regions, want 1", len(td.exprs))
	}
}

// The whole feature, driven the way the editor drives it: open the example
// suite over real JSON-RPC and ask for the three things layer 3 promises.
func TestSuiteSession(t *testing.T) {
	s := startSession(t)
	uri, text := exampleURI(t, "heated-tank_test.yaml")
	lines := strings.Split(text, "\n")

	id := s.send("initialize", map[string]any{}, true)
	s.recvResponse(id)
	s.send("textDocument/didOpen", DidOpenTextDocumentParams{
		TextDocument: TextDocumentItem{URI: uri, LanguageID: "yaml", Version: 1, Text: text},
	}, false)
	var pub PublishDiagnosticsParams
	if err := json.Unmarshal(s.recv().Params, &pub); err != nil {
		t.Fatal(err)
	}
	if len(pub.Diagnostics) != 0 {
		t.Fatalf("clean suite published %+v", pub.Diagnostics)
	}

	// Hover on TempC inside `ABS(TempC - TempSP) < 0.5` — the manifest's
	// meaning, not just its type.
	at := func(line int, needle string) Position {
		return Position{Line: line, Character: strings.Index(lines[line], needle) + 1}
	}
	id = s.send("textDocument/hover", TextDocumentPositionParams{
		TextDocument: TextDocumentIdentifier{URI: uri},
		Position:     at(62, "TempC"),
	}, true)
	var hov Hover
	if err := json.Unmarshal(s.recvResponse(id), &hov); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"TempC : REAL", "Tank temperature", "°C"} {
		if !strings.Contains(hov.Contents.Value, want) {
			t.Errorf("hover %q is missing %q", hov.Contents.Value, want)
		}
	}

	// Completion inside the expression: the project's tags and ST's
	// builtins, but not statement keywords.
	id = s.send("textDocument/completion", TextDocumentPositionParams{
		TextDocument: TextDocumentIdentifier{URI: uri},
		Position:     at(62, "TempC"),
	}, true)
	var items []CompletionItem
	if err := json.Unmarshal(s.recvResponse(id), &items); err != nil {
		t.Fatal(err)
	}
	labels := map[string]CompletionItem{}
	for _, it := range items {
		labels[it.Label] = it
	}
	for _, want := range []string{"TempSP", "Heater", "ABS", "TRUE"} {
		if _, ok := labels[want]; !ok {
			t.Errorf("expression completion is missing %q (got %d items)", want, len(items))
		}
	}
	if _, leaked := labels["END_PROGRAM"]; leaked {
		t.Error("expression completion offers statement keywords")
	}
	// Heater has no init in the manifest; its type comes from the program.
	if d := labels["Heater"].Detail; !strings.HasPrefix(d, "REAL") {
		t.Errorf("Heater detail = %q, want it to lead with REAL", d)
	}

	// Completion in key position: tags, and nothing from ST.
	id = s.send("textDocument/completion", TextDocumentPositionParams{
		TextDocument: TextDocumentIdentifier{URI: uri},
		Position:     at(16, "LevelPct"), // `given: { LevelPct: 35.0 }`
	}, true)
	items = nil
	if err := json.Unmarshal(s.recvResponse(id), &items); err != nil {
		t.Fatal(err)
	}
	if len(items) == 0 {
		t.Fatal("no tag completions in a given: mapping")
	}
	for _, it := range items {
		if it.Kind != CompletionKindVariable {
			t.Fatalf("key position offered a non-tag: %+v", it)
		}
	}

	// Plain YAML: the schema owns it, we say nothing.
	id = s.send("textDocument/completion", TextDocumentPositionParams{
		TextDocument: TextDocumentIdentifier{URI: uri},
		Position:     at(13, "name"),
	}, true)
	items = nil
	if err := json.Unmarshal(s.recvResponse(id), &items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("completion outside an expression returned %d items", len(items))
	}

	s.send("exit", nil, false)
	if err := <-s.done; err != nil {
		t.Fatalf("Serve returned %v", err)
	}
}

// Mid-edit YAML is normal. It must not throw, and it must not squiggle —
// the YAML extension owns syntax here.
func TestBrokenYAMLIsSilent(t *testing.T) {
	s := &Server{docs: map[string]*document{}}
	uri, _ := exampleURI(t, "heated-tank_test.yaml")
	td, diags := s.analyzeTest(uri, "tests:\n  - name: [unclosed\n")
	if len(diags) != 0 || len(td.exprs) != 0 {
		t.Fatalf("broken YAML produced %d diagnostics and %d regions", len(diags), len(td.exprs))
	}
}
