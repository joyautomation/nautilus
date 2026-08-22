package lsp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// applyEdits replays a document's edits onto its text — edits are applied
// back-to-front so earlier ranges stay valid.
func applyEdits(text string, edits []TextEdit) string {
	lines := strings.Split(text, "\n")
	for i := len(edits) - 1; i >= 0; i-- {
		e := edits[i]
		l := lines[e.Range.Start.Line]
		lines[e.Range.Start.Line] = l[:e.Range.Start.Character] + e.NewText + l[e.Range.End.Character:]
	}
	return strings.Join(lines, "\n")
}

func TestIdentOccurrencesSkipsCommentsAndStrings(t *testing.T) {
	src := "x := x + 1; (* x in a comment *) // x again\ns := 'x$'x'; X := 2;"
	got := identOccurrences(src, "x")
	// x, x on line 0; X on line 1 — never the ones in comments or the string.
	if len(got) != 3 || got[0].Start.Character != 0 || got[1].Start.Character != 5 || got[2].Start.Line != 1 {
		t.Fatalf("occurrences = %+v", got)
	}
}

func TestIdentOccurrencesSkipsNestedComments(t *testing.T) {
	src := "x := 1; (* outer (* x nested *) still outer *) x := 2;"
	got := identOccurrences(src, "x")
	// First x, then the x after the nested comment — never the one inside.
	// First x is at line 0, character 0; second x is at line 0, character 47
	if len(got) != 2 || got[0].Start.Character != 0 || got[1].Start.Character != 47 {
		t.Fatalf("occurrences = %+v", got)
	}
}

// A library file with two function blocks, each with its own local `err` —
// the real shape a nautilus project uses (FBs live in library .st files).
// Renaming Alpha's err must leave Beta's err alone: the scoping guarantee.
const renameSrc = `FUNCTION_BLOCK Alpha
VAR
    err : REAL;
END_VAR
err := 1.0;   (* Alpha's err *)
END_FUNCTION_BLOCK

FUNCTION_BLOCK Beta
VAR
    err : REAL;   (* a DIFFERENT err *)
END_VAR
err := 2.0;
END_FUNCTION_BLOCK
`

func TestRenameLocalIsScoped(t *testing.T) {
	s := startSession(t)
	id := s.send("initialize", map[string]any{}, true)
	var init InitializeResult
	if err := json.Unmarshal(s.recvResponse(id), &init); err != nil {
		t.Fatal(err)
	}
	if init.Capabilities.RenameProvider == nil || !init.Capabilities.RenameProvider.PrepareProvider {
		t.Fatalf("renameProvider not advertised: %+v", init.Capabilities)
	}
	s.send("textDocument/didOpen", DidOpenTextDocumentParams{
		TextDocument: TextDocumentItem{URI: "file:///blocks.st", LanguageID: "iec-st", Version: 1, Text: renameSrc},
	}, false)
	s.recv() // diagnostics

	// prepareRename on Alpha's err use (line 4, "err := 1.0").
	id = s.send("textDocument/prepareRename", TextDocumentPositionParams{
		TextDocument: TextDocumentIdentifier{URI: "file:///blocks.st"},
		Position:     Position{Line: 4, Character: 1},
	}, true)
	var prep PrepareRenameResult
	if err := json.Unmarshal(s.recvResponse(id), &prep); err != nil {
		t.Fatal(err)
	}
	if prep.Placeholder != "err" || prep.Range.Start.Line != 4 {
		t.Fatalf("prepareRename = %+v", prep)
	}

	id = s.send("textDocument/rename", RenameParams{
		TextDocument: TextDocumentIdentifier{URI: "file:///blocks.st"},
		Position:     Position{Line: 4, Character: 1},
		NewName:      "error",
	}, true)
	var we WorkspaceEdit
	if err := json.Unmarshal(s.recvResponse(id), &we); err != nil {
		t.Fatal(err)
	}
	edits := we.Changes["file:///blocks.st"]
	// Alpha's declaration and its one use — NOT Beta's err.
	if len(edits) != 2 {
		t.Fatalf("expected 2 edits, got %+v", edits)
	}
	got := applyEdits(renameSrc, edits)
	if !strings.Contains(got, "    error : REAL;\nEND_VAR\nerror := 1.0;") {
		t.Fatalf("Alpha's err not renamed:\n%s", got)
	}
	if !strings.Contains(got, "    err : REAL;   (* a DIFFERENT err *)") || !strings.Contains(got, "err := 2.0;") {
		t.Fatalf("Beta's err must be untouched:\n%s", got)
	}
}

func TestRenameRefusesKeywordsAndCollisions(t *testing.T) {
	s := startSession(t)
	id := s.send("initialize", map[string]any{}, true)
	s.recvResponse(id)
	s.send("textDocument/didOpen", DidOpenTextDocumentParams{
		TextDocument: TextDocumentItem{URI: "file:///p.st", LanguageID: "iec-st", Version: 1, Text: renameSrc},
	}, false)
	s.recv()

	for _, bad := range []string{"VAR", "TempC", "9lives", "bad name"} {
		id = s.send("textDocument/rename", RenameParams{
			TextDocument: TextDocumentIdentifier{URI: "file:///p.st"},
			Position:     Position{Line: 8, Character: 1},
			NewName:      bad,
		}, true)
		m := s.recvErrorOrResult(id)
		if len(m.Error) == 0 {
			t.Fatalf("rename to %q should be refused, got %s", bad, m.Result)
		}
	}
}

// recvErrorOrResult is recvResponse without the fatal-on-error: rename
// refusals are error responses by design.
func (s *lspSession) recvErrorOrResult(id int) *message {
	s.t.Helper()
	for i := 0; i < 10; i++ {
		m := s.recv()
		if m.ID == nil {
			continue
		}
		var got int
		if err := json.Unmarshal(*m.ID, &got); err != nil || got != id {
			continue
		}
		return m
	}
	s.t.Fatal("response never arrived")
	return nil
}

const renameManifest = `name: tank
server:
  addr: "localhost:8080"
tasks:
  - program: program.st
    scan: 100ms
    dt-tag: ScanDtS
  - name: sim
    program: sim.st
    scan: 100ms
tags:
  - { name: TempC,   role: state,    init: 60.0, unit: "°C", desc: "Tank temperature" }
  - { name: ScanDtS, role: state,    init: 0.0 }
  - name: "HiTempSP"
    role: setpoint
    init: 68.0
tag-meta:
  TempC: { hmi: gauge }
`

const renameProgram = `PROGRAM Main
VAR_EXTERNAL
    TempC   : REAL;
    ScanDtS : REAL;
END_VAR
VAR
    err : REAL;
END_VAR
err := 65.0 - TempC;
END_PROGRAM
`

const renameSim = `PROGRAM Sim
VAR_EXTERNAL
    TempC : REAL;
END_VAR
VAR
    heat : REAL;
END_VAR
TempC := TempC + heat * 0.1;
END_PROGRAM
`

// A program in the same project that declares ITS OWN local called TempC
// (not bound to the tag) — the rename must leave it alone.
const renameOther = `PROGRAM Other
VAR
    TempC : REAL;
END_VAR
TempC := 1.0;
END_PROGRAM
`

func TestRenameTagAcrossProject(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	write("nautilus.yaml", renameManifest)
	progPath := write("program.st", renameProgram)
	simPath := write("sim.st", renameSim)
	otherPath := write("other.st", renameOther)

	s := startSession(t)
	id := s.send("initialize", map[string]any{}, true)
	s.recvResponse(id)
	progURI := pathToURI(progPath)
	s.send("textDocument/didOpen", DidOpenTextDocumentParams{
		TextDocument: TextDocumentItem{URI: progURI, LanguageID: "iec-st", Version: 1, Text: renameProgram},
	}, false)
	s.recv()

	// Rename TempC (use site, line 8) → TankTempC.
	id = s.send("textDocument/rename", RenameParams{
		TextDocument: TextDocumentIdentifier{URI: progURI},
		Position:     Position{Line: 8, Character: 15},
		NewName:      "TankTempC",
	}, true)
	var we WorkspaceEdit
	if err := json.Unmarshal(s.recvResponse(id), &we); err != nil {
		t.Fatal(err)
	}

	if _, touched := we.Changes[pathToURI(otherPath)]; touched {
		t.Fatalf("other.st's local TempC must not be renamed: %+v", we.Changes)
	}
	prog := applyEdits(renameProgram, we.Changes[progURI])
	if !strings.Contains(prog, "    TankTempC   : REAL;") || !strings.Contains(prog, "err := 65.0 - TankTempC;") {
		t.Fatalf("program.st:\n%s", prog)
	}
	sim := applyEdits(renameSim, we.Changes[pathToURI(simPath)])
	if !strings.Contains(sim, "    TankTempC : REAL;") || !strings.Contains(sim, "TankTempC := TankTempC + heat * 0.1;") {
		t.Fatalf("sim.st:\n%s", sim)
	}
	manifest := applyEdits(renameManifest, we.Changes[pathToURI(filepath.Join(dir, "nautilus.yaml"))])
	for _, want := range []string{
		`- { name: TankTempC,   role: state,`, // flow-style name
		"tag-meta:\n  TankTempC: { hmi: gauge }", // tag-meta key
		`name: "HiTempSP"`,                         // untouched neighbour keeps its quotes
		"dt-tag: ScanDtS",                          // a different tag's dt-tag untouched
	} {
		if !strings.Contains(manifest, want) {
			t.Fatalf("manifest missing %q:\n%s", want, manifest)
		}
	}
	if strings.Contains(manifest, "{ name: TempC,") || strings.Contains(manifest, "\n  TempC:") {
		t.Fatalf("old name survives in manifest:\n%s", manifest)
	}

	// Renaming onto an existing tag name is refused.
	id = s.send("textDocument/rename", RenameParams{
		TextDocument: TextDocumentIdentifier{URI: progURI},
		Position:     Position{Line: 8, Character: 15},
		NewName:      "ScanDtS",
	}, true)
	if m := s.recvErrorOrResult(id); len(m.Error) == 0 {
		t.Fatalf("rename onto an existing tag should be refused, got %s", m.Result)
	}
}

func TestRenameQuotedManifestScalarKeepsQuotes(t *testing.T) {
	dir := t.TempDir()
	m := "tags:\n  - name: \"HiTempSP\"\n    role: setpoint\n"
	mpath := filepath.Join(dir, "nautilus.yaml")
	if err := os.WriteFile(mpath, []byte(m), 0o644); err != nil {
		t.Fatal(err)
	}
	edits, err := renameTagInManifest(mpath, "HiTempSP", "HiTempTrip")
	if err != nil {
		t.Fatal(err)
	}
	got := applyEdits(m, edits[mpath])
	if !strings.Contains(got, `name: "HiTempTrip"`) {
		t.Fatalf("quoted scalar edit:\n%s", got)
	}
}
