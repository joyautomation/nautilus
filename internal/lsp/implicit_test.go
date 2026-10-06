package lsp

// The language server sees the project model the compiler does: manifest
// tags in scope in every program without VAR_EXTERNAL (#177/#210), a
// shadowing local warned, and a library's error reported once, on the
// library (#199).

import (
	"encoding/json"
	"strings"
	"testing"
)

const implicitManifest = `tasks:
  - program: main.st
  - name: sim
    program: sim.st
tags:
  - { name: TempC,   role: state, init: 60.0, unit: "°C", desc: "Tank temperature" }
  - { name: Heater,  role: output, type: BOOL }
  - { name: Valve,   role: output }
`

// No VAR_EXTERNAL anywhere: TempC and Heater are tags.
const implicitMain = `PROGRAM Main
VAR
    err : REAL;
END_VAR
err := 65.0 - TempC;
Heater := err > 0.0;
END_PROGRAM
`

const implicitSim = `PROGRAM Sim
TempC := TempC + 0.1;
END_PROGRAM
`

// openDiags opens a document and returns the diagnostics pushed for it.
func (s *lspSession) openDiags(uri, text string) []Diagnostic {
	s.t.Helper()
	s.send("textDocument/didOpen", DidOpenTextDocumentParams{
		TextDocument: TextDocumentItem{URI: uri, LanguageID: "iec-st", Version: 1, Text: text},
	}, false)
	for i := 0; i < 5; i++ {
		m := s.recv()
		if m.Method != "textDocument/publishDiagnostics" {
			continue
		}
		var pub PublishDiagnosticsParams
		if err := json.Unmarshal(m.Params, &pub); err != nil {
			s.t.Fatal(err)
		}
		if pub.URI == uri {
			return pub.Diagnostics
		}
	}
	s.t.Fatal("no diagnostics for " + uri)
	return nil
}

func TestImplicitTagsCompileClean(t *testing.T) {
	files := map[string]string{"nautilus.yaml": implicitManifest, "main.st": implicitMain, "sim.st": implicitSim}
	_, paths := refProject(t, files)
	s := startSession(t)
	s.recvResponse(s.send("initialize", map[string]any{}, true))
	if d := s.openDiags(pathToURI(paths["main.st"]), implicitMain); len(d) != 0 {
		t.Fatalf("diagnostics = %+v", d)
	}
	// An untyped tag says how to fix it.
	uri := pathToURI(paths["sim.st"])
	d := s.openDiags(uri, "PROGRAM Sim\nValve := TRUE;\nEND_PROGRAM\n")
	if len(d) != 1 || !strings.Contains(d[0].Message, `"Valve" is a project tag with no type`) {
		t.Fatalf("diagnostics = %+v", d)
	}
}

func TestImplicitTagHoverCompletionDefinition(t *testing.T) {
	files := map[string]string{"nautilus.yaml": implicitManifest, "main.st": implicitMain, "sim.st": implicitSim}
	_, paths := refProject(t, files)
	s := startSession(t)
	s.recvResponse(s.send("initialize", map[string]any{}, true))
	uri := pathToURI(paths["main.st"])
	s.openDiags(uri, implicitMain)

	// hover on TempC (line 5, "err := 65.0 - TempC;")
	id := s.send("textDocument/hover", TextDocumentPositionParams{
		TextDocument: TextDocumentIdentifier{URI: uri}, Position: Position{Line: 4, Character: 15},
	}, true)
	var h Hover
	if err := json.Unmarshal(s.recvResponse(id), &h); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h.Contents.Value, "Tank temperature") || !strings.Contains(h.Contents.Value, "TempC : REAL") {
		t.Errorf("hover = %q", h.Contents.Value)
	}

	// completion in the body offers the tags with the manifest's detail.
	id = s.send("textDocument/completion", TextDocumentPositionParams{
		TextDocument: TextDocumentIdentifier{URI: uri}, Position: Position{Line: 5, Character: 0},
	}, true)
	var items []CompletionItem
	if err := json.Unmarshal(s.recvResponse(id), &items); err != nil {
		t.Fatal(err)
	}
	found := map[string]string{}
	for _, it := range items {
		found[it.Label] = it.Detail
	}
	if !strings.Contains(found["TempC"], "°C") || !strings.Contains(found["Heater"], "BOOL") {
		t.Errorf("completion details: TempC %q, Heater %q", found["TempC"], found["Heater"])
	}

	// definition jumps to the manifest's tags[].name.
	id = s.send("textDocument/definition", TextDocumentPositionParams{
		TextDocument: TextDocumentIdentifier{URI: uri}, Position: Position{Line: 5, Character: 2},
	}, true)
	var loc Location
	if err := json.Unmarshal(s.recvResponse(id), &loc); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(loc.URI, "nautilus.yaml") || loc.Range.Start.Line != 6 {
		t.Errorf("definition = %+v, want nautilus.yaml line 7", loc)
	}
}

// A block body does not see tags implicitly — not in completion, and an
// FB naming one undeclared is an error.
func TestImplicitTagsNotInFunctionBlocks(t *testing.T) {
	src := "FUNCTION_BLOCK Probe\nVAR_OUTPUT\n    Q : REAL;\nEND_VAR\nQ := TempC;\nEND_FUNCTION_BLOCK\n"
	files := map[string]string{"nautilus.yaml": implicitManifest, "main.st": implicitMain, "sim.st": implicitSim, "probe.st": src}
	_, paths := refProject(t, files)
	s := startSession(t)
	s.recvResponse(s.send("initialize", map[string]any{}, true))
	uri := pathToURI(paths["probe.st"])
	d := s.openDiags(uri, src)
	if len(d) != 1 || !strings.Contains(d[0].Message, `undeclared identifier "TempC"`) {
		t.Fatalf("diagnostics = %+v", d)
	}
	id := s.send("textDocument/completion", TextDocumentPositionParams{
		TextDocument: TextDocumentIdentifier{URI: uri}, Position: Position{Line: 4, Character: 0},
	}, true)
	var items []CompletionItem
	if err := json.Unmarshal(s.recvResponse(id), &items); err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		if it.Label == "TempC" {
			t.Errorf("a block body was offered the tag TempC")
		}
	}
}

// References and rename reach a tag's implicit uses in every program.
func TestImplicitTagReferencesAndRename(t *testing.T) {
	files := map[string]string{"nautilus.yaml": implicitManifest, "main.st": implicitMain, "sim.st": implicitSim}
	_, paths := refProject(t, files)
	texts := refTexts(files, paths)
	s := startSession(t)
	s.recvResponse(s.send("initialize", map[string]any{}, true))
	uri := pathToURI(paths["main.st"])
	s.openDiags(uri, implicitMain)
	refSameLines(t, refLines(t, s.refsAt(uri, 4, 15, true), "TempC", texts),
		"main.st:5", "sim.st:2", "sim.st:2", "nautilus.yaml:6")

	id := s.send("textDocument/rename", RenameParams{
		TextDocument: TextDocumentIdentifier{URI: uri}, Position: Position{Line: 4, Character: 15}, NewName: "TankTemp",
	}, true)
	var we WorkspaceEdit
	if err := json.Unmarshal(s.recvResponse(id), &we); err != nil {
		t.Fatal(err)
	}
	if len(we.Changes[uri]) != 1 || len(we.Changes[pathToURI(paths["sim.st"])]) != 2 ||
		len(we.Changes[pathToURI(paths["nautilus.yaml"])]) != 1 {
		t.Errorf("rename edits = %+v", we.Changes)
	}
}

// A local of a tag's name shadows it: a warning on the declaration.
func TestShadowWarning(t *testing.T) {
	src := "PROGRAM Main\nVAR\n    TempC : REAL;\nEND_VAR\nTempC := 1.0;\nEND_PROGRAM\n"
	files := map[string]string{"nautilus.yaml": implicitManifest, "main.st": src, "sim.st": implicitSim}
	_, paths := refProject(t, files)
	s := startSession(t)
	s.recvResponse(s.send("initialize", map[string]any{}, true))
	d := s.openDiags(pathToURI(paths["main.st"]), src)
	if len(d) != 1 || d[0].Severity != SeverityWarning || d[0].Range.Start.Line != 2 ||
		!strings.Contains(d[0].Message, "local TempC shadows the project tag TempC") {
		t.Fatalf("diagnostics = %+v", d)
	}
}

// #199: an error in a library is published on the library's own line,
// not on the program that composes it.
func TestLibraryErrorPublishedOnLibrary(t *testing.T) {
	lib := "FUNCTION_BLOCK Bad\nVAR_OUTPUT\n    Q : REAL;\nEND_VAR\nQ := Nope;\nEND_FUNCTION_BLOCK\n"
	files := map[string]string{"nautilus.yaml": implicitManifest, "main.st": implicitMain, "sim.st": implicitSim, "lib.st": lib}
	_, paths := refProject(t, files)
	s := startSession(t)
	s.recvResponse(s.send("initialize", map[string]any{}, true))
	mainURI, libURI := pathToURI(paths["main.st"]), pathToURI(paths["lib.st"])
	s.send("textDocument/didOpen", DidOpenTextDocumentParams{
		TextDocument: TextDocumentItem{URI: mainURI, LanguageID: "iec-st", Version: 1, Text: implicitMain},
	}, false)
	got := map[string][]Diagnostic{}
	for i := 0; i < 2; i++ {
		var pub PublishDiagnosticsParams
		if err := json.Unmarshal(s.recv().Params, &pub); err != nil {
			t.Fatal(err)
		}
		got[pub.URI] = pub.Diagnostics
	}
	if len(got[mainURI]) != 0 {
		t.Errorf("the program got %+v; a library's error belongs to the library", got[mainURI])
	}
	d := got[libURI]
	if len(d) != 1 || d[0].Range.Start.Line != 4 || !strings.Contains(d[0].Message, `undeclared identifier "Nope"`) {
		t.Errorf("library diagnostics = %+v, want line 5", d)
	}
}
