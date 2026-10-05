package lsp

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/lang/st"
)

// typingHead is a program the user is in the middle of typing into: a UDT,
// a TON instance and a struct-typed variable are declared, and each test
// appends the half-typed line under test before END_PROGRAM.
const typingHead = `TYPE
  Tank_Type : STRUCT
    Level : REAL;
    High : BOOL;
  END_STRUCT;
END_TYPE

PROGRAM Plant
VAR
  settle : TON; (* a VAR in a comment: END_VAR *)
  Full : BOOL;
  Tank : Tank_Type;
  Level : REAL;
END_VAR
settle(IN := Level > 90.0, PT := T#2s);
`

func typingSrc(line string) string { return typingHead + line + "\nEND_PROGRAM\n" }

func TestDeclSkeletonKeepsDeclarationsInPlace(t *testing.T) {
	src := typingSrc("Full := settle.")
	skel := declSkeleton(src)
	if len(skel) != len(src) || strings.Count(skel, "\n") != strings.Count(src, "\n") {
		t.Fatalf("skeleton changed the layout:\n%s", skel)
	}
	if strings.Contains(skel, "settle(") || strings.Contains(skel, "settle.") {
		t.Errorf("body statements survived:\n%s", skel)
	}
	for _, kept := range []string{"settle : TON;", "Tank : Tank_Type;", "END_STRUCT;", "PROGRAM Plant", "END_PROGRAM"} {
		if !strings.Contains(skel, kept) {
			t.Errorf("skeleton lost %q:\n%s", kept, skel)
		}
	}
	if _, err := st.Parse(skel); err != nil {
		t.Fatalf("skeleton does not parse: %v\n%s", err, skel)
	}
}

func TestAnalyzeParseErrorStillIndexesDeclarations(t *testing.T) {
	src := typingSrc("settle.")
	_, perr := st.Parse(src)
	if perr == nil {
		t.Fatal("the half-typed line parses; the test needs one that does not")
	}
	a := analyze(src, "", 0)
	// Diagnostics are unchanged: exactly the parse error, nothing from the
	// recovered skeleton.
	if len(a.Diags) != 1 || a.Diags[0].Message != perr.Error() {
		t.Fatalf("diags = %+v, want only %q", a.Diags, perr)
	}
	sym := a.lookup("settle", 17)
	if sym == nil || sym.Datatype != "TON" || sym.Pos.Line != 10 {
		t.Fatalf("settle = %+v, want TON declared on line 10", sym)
	}
	typ, ok := a.resolveChain("Tank", nil, 17)
	if !ok || !reflect.DeepEqual(labelsOf(a.memberCompletions(typ)), []string{"Level", "High"}) {
		t.Errorf("Tank. on a broken buffer: type %q ok=%v", typ, ok)
	}
}

func TestFBDeclarationsRecoveredWhileTypingItsBody(t *testing.T) {
	src := "FUNCTION_BLOCK Filler\nVAR_INPUT\n  Go : BOOL;\nEND_VAR\nVAR\n  t : TON;\nEND_VAR\nt.\nEND_FUNCTION_BLOCK\n\n" +
		"FUNCTION Twice : REAL\nVAR_INPUT\n  x : REAL;\nEND_VAR\nTwice := x * ;\nEND_FUNCTION\n\nPROGRAM P\nVAR\n  f : Filler;\nEND_VAR\nEND_PROGRAM\n"
	a := analyze(src, "", 0)
	if len(a.Diags) != 1 {
		t.Fatalf("diags = %+v", a.Diags)
	}
	if typ, ok := a.resolveChain("t", nil, 8); !ok || typ != "TON" {
		t.Errorf("t in the FB body = %q,%v", typ, ok)
	}
	if typ, ok := a.resolveChain("f", nil, 21); !ok || !contains(labelsOf(a.memberCompletions(typ)), "Go") {
		t.Errorf("f. = %q,%v", typ, ok)
	}
	if sym := a.lookup("x", 15); sym == nil || sym.Container != "Twice" {
		t.Errorf("x in FUNCTION body = %+v", sym)
	}
}

// completionAt asks the server for completions at (line, char) and decodes
// the answer, failing on null — a dot must always be answered with a list.
func (s *lspSession) completionAt(uri string, line, char int) []CompletionItem {
	s.t.Helper()
	id := s.send("textDocument/completion", TextDocumentPositionParams{
		TextDocument: TextDocumentIdentifier{URI: uri},
		Position:     Position{Line: line, Character: char},
	}, true)
	raw := s.recvResponse(id)
	if string(raw) == "null" {
		s.t.Fatalf("completion at %d:%d answered null", line, char)
	}
	var items []CompletionItem
	if err := json.Unmarshal(raw, &items); err != nil {
		s.t.Fatal(err)
	}
	return items
}

// change replaces the document's text and consumes the diagnostics push it
// triggers (the pipe is unbuffered: an unread push would stall the server).
func (s *lspSession) change(uri, text string) {
	s.t.Helper()
	s.send("textDocument/didChange", DidChangeTextDocumentParams{
		TextDocument:   TextDocumentIdentifier{URI: uri},
		ContentChanges: []TextDocumentContentChange{{Text: text}},
	}, false)
	if m := s.recv(); m.Method != "textDocument/publishDiagnostics" {
		s.t.Fatalf("expected a diagnostics push, got %q", m.Method)
	}
}

// open is change for the first version of a document.
func (s *lspSession) open(uri, text string) {
	s.t.Helper()
	s.send("textDocument/didOpen", DidOpenTextDocumentParams{
		TextDocument: TextDocumentItem{URI: uri, LanguageID: "iec-st", Version: 1, Text: text},
	}, false)
	if m := s.recv(); m.Method != "textDocument/publishDiagnostics" {
		s.t.Fatalf("expected a diagnostics push, got %q", m.Method)
	}
}

// TestMemberCompletionWhileTyping is #139: the dot is typed into a buffer
// that cannot parse, and the server must still list the members — the same
// list Ctrl+Space gives once the line parses.
func TestMemberCompletionWhileTyping(t *testing.T) {
	const uri = "file:///plant.st"
	s := startSession(t)
	id := s.send("initialize", map[string]any{}, true)
	var init InitializeResult
	if err := json.Unmarshal(s.recvResponse(id), &init); err != nil {
		t.Fatal(err)
	}
	if cp := init.Capabilities.CompletionProvider; cp == nil || !reflect.DeepEqual(cp.TriggerCharacters, []string{"."}) {
		t.Fatalf("completion trigger characters = %+v, want [.]", cp)
	}
	s.open(uri, typingSrc("Full := settle.Q;"))
	const body = 15 // 0-based line of the half-typed statement

	// Baseline: Ctrl+Space right after the dot on a line that parses.
	want := s.completionAt(uri, body, len("Full := settle."))
	if !reflect.DeepEqual(labelsOf(want), []string{"IN", "PT", "Q", "ET"}) {
		t.Fatalf("baseline TON pins = %v", labelsOf(want))
	}
	for _, it := range want {
		if it.Kind != CompletionKindField || it.Detail == "" {
			t.Errorf("pin %s: kind %d detail %q", it.Label, it.Kind, it.Detail)
		}
	}

	for _, tc := range []struct {
		line string
		want []string // nil: the same list as the baseline
	}{
		{line: "settle."},
		{line: "Full := settle."},
		{line: "  IF settle."},
		{line: "Level := Tank.", want: []string{"Level", "High"}},
		{line: "Full := nosuch.", want: []string{}},
	} {
		s.change(uri, typingSrc(tc.line))
		got := s.completionAt(uri, body, len(tc.line))
		if tc.want == nil {
			if !reflect.DeepEqual(got, want) {
				t.Errorf("%q: got %+v, want %+v", tc.line, got, want)
			}
			continue
		}
		if !reflect.DeepEqual(labelsOf(got), tc.want) {
			t.Errorf("%q: got %v, want %v", tc.line, labelsOf(got), tc.want)
		}
	}

	// Hover on a declared variable elsewhere in a buffer that does not parse.
	s.change(uri, typingSrc("Full := settle."))
	id = s.send("textDocument/hover", TextDocumentPositionParams{
		TextDocument: TextDocumentIdentifier{URI: uri},
		Position:     Position{Line: 14, Character: 1}, // settle( ... ) call
	}, true)
	var hov Hover
	if err := json.Unmarshal(s.recvResponse(id), &hov); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(hov.Contents.Value, "settle : TON") {
		t.Errorf("hover on a broken buffer = %q", hov.Contents.Value)
	}

	// Not even the declarations parse (a VAR line mid-edit): the previous
	// version's declarations answer.
	broken := strings.Replace(typingSrc("settle."), "Level : REAL;", "Level : ", 1)
	s.change(uri, broken)
	if got := s.completionAt(uri, body, len("settle.")); !reflect.DeepEqual(got, want) {
		t.Errorf("with a broken VAR block: got %v", labelsOf(got))
	}
}

// A document opened broken all the way down has nothing to carry over: the
// dot is answered with an empty list, not an error.
func TestMemberCompletionNothingRecoverable(t *testing.T) {
	const uri = "file:///broken.st"
	s := startSession(t)
	s.recvResponse(s.send("initialize", map[string]any{}, true))
	s.open(uri, "PROGRAM P\nVAR\n  settle : \nEND_VAR\nsettle.\nEND_PROGRAM\n")
	if got := s.completionAt(uri, 4, len("settle.")); len(got) != 0 {
		t.Errorf("got %v, want none", labelsOf(got))
	}
}
