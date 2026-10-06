package lsp

import (
	"encoding/json"
	"strings"
	"testing"
)

// #197: identifiers are case-insensitive. One symbol, written three ways.
const caseSrc = `FUNCTION ScaleAnalog : REAL
VAR_INPUT
    Raw : REAL;
END_VAR
SCALEANALOG := raw * 2.0;
END_FUNCTION

PROGRAM P
VAR
    Level : REAL;
    Out : REAL;
END_VAR
LEVEL := 1.0;
level := Level + 1.0;
Out := scaleanalog(LEVEL);
END_PROGRAM
`

func TestCaseInsensitiveIdentifiersInTheEditor(t *testing.T) {
	s := startSession(t)
	s.recvResponse(s.send("initialize", map[string]any{}, true))
	const uri = "file:///case.st"
	s.send("textDocument/didOpen", DidOpenTextDocumentParams{
		TextDocument: TextDocumentItem{URI: uri, LanguageID: "iec-st", Version: 1, Text: caseSrc},
	}, false)
	m := s.recv()
	var diag PublishDiagnosticsParams
	if err := json.Unmarshal(m.Params, &diag); err != nil {
		t.Fatal(err)
	}
	if len(diag.Diagnostics) != 0 {
		t.Fatalf("other casings of a declared name must compile clean, got %+v", diag.Diagnostics)
	}
	texts := map[string]string{uri: caseSrc}

	// references from `level` (line 14) find every spelling and the declaration.
	refSameLines(t, refLines(t, s.refsAt(uri, 13, 1, true), "level", texts),
		"case.st:10", "case.st:13", "case.st:14", "case.st:14", "case.st:15")

	// hover on LEVEL shows the declaration's spelling.
	id := s.send("textDocument/hover", TextDocumentPositionParams{
		TextDocument: TextDocumentIdentifier{URI: uri},
		Position:     Position{Line: 12, Character: 2},
	}, true)
	var hov Hover
	if err := json.Unmarshal(s.recvResponse(id), &hov); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(hov.Contents.Value, "Level : REAL") {
		t.Fatalf("hover = %q, want the declared spelling Level", hov.Contents.Value)
	}

	// definition from a lower-case use lands on the declaration.
	id = s.send("textDocument/definition", TextDocumentPositionParams{
		TextDocument: TextDocumentIdentifier{URI: uri},
		Position:     Position{Line: 14, Character: 9},
	}, true)
	var loc Location
	if err := json.Unmarshal(s.recvResponse(id), &loc); err != nil {
		t.Fatal(err)
	}
	if loc.Range.Start.Line != 0 {
		t.Fatalf("definition of scaleanalog = %+v, want the FUNCTION on line 1", loc)
	}

	// signature help on scaleanalog( is ScaleAnalog's.
	if h := s.sigAt(uri, 14, 19); h == nil || len(h.Signatures) == 0 || !strings.Contains(h.Signatures[0].Label, "Raw") {
		t.Fatalf("signature help for scaleanalog( = %+v", h)
	}

	// rename from LEVEL rewrites every spelling to the new name.
	id = s.send("textDocument/rename", RenameParams{
		TextDocument: TextDocumentIdentifier{URI: uri},
		Position:     Position{Line: 12, Character: 1},
		NewName:      "Tank",
	}, true)
	var we WorkspaceEdit
	if err := json.Unmarshal(s.recvResponse(id), &we); err != nil {
		t.Fatal(err)
	}
	got := applyEdits(caseSrc, we.Changes[uri])
	for _, want := range []string{"    Tank : REAL;", "Tank := 1.0;", "Tank := Tank + 1.0;", "Out := scaleanalog(Tank);"} {
		if !strings.Contains(got, want) {
			t.Fatalf("rename missed a spelling — want %q in:\n%s", want, got)
		}
	}
	if strings.Contains(strings.ToLower(got), "level") {
		t.Fatalf("rename left a spelling of Level behind:\n%s", got)
	}
}

// A clash that only case distinguished is now a duplicate, reported on the
// second declaration and naming the first.
func TestCaseOnlyDuplicateIsDiagnosed(t *testing.T) {
	s := startSession(t)
	s.recvResponse(s.send("initialize", map[string]any{}, true))
	src := "PROGRAM P\nVAR\n  Running : BOOL;\n  running : BOOL;\nEND_VAR\nEND_PROGRAM\n"
	s.send("textDocument/didOpen", DidOpenTextDocumentParams{
		TextDocument: TextDocumentItem{URI: "file:///dup.st", LanguageID: "iec-st", Version: 1, Text: src},
	}, false)
	var diag PublishDiagnosticsParams
	if err := json.Unmarshal(s.recv().Params, &diag); err != nil {
		t.Fatal(err)
	}
	if len(diag.Diagnostics) != 1 || diag.Diagnostics[0].Range.Start.Line != 3 ||
		!strings.Contains(diag.Diagnostics[0].Message, `"Running" is already declared`) {
		t.Fatalf("diagnostics = %+v", diag.Diagnostics)
	}
}
