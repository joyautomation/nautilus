package lsp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
  TempC: { unit: degC }
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
		`- { name: TankTempC,   role: state,`,    // flow-style name
		"tag-meta:\n  TankTempC: { unit: degC }", // tag-meta key
		`name: "HiTempSP"`,                       // untouched neighbour keeps its quotes
		"dt-tag: ScanDtS",                        // a different tag's dt-tag untouched
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

// #195: rename edits only identifiers that resolve to the symbol. The Q of
// t1.Q is TON's output, not the local Q, and IN/PT before := in t1's call
// are its formals.
func TestRenameSkipsMembersAndFormals(t *testing.T) {
	src := "PROGRAM P\nVAR\n Q : BOOL;\n t1 : TON;\n d : TIME;\nEND_VAR\nt1(IN := TRUE, PT := T#2s);\nQ := t1.Q;\nEND_PROGRAM\n"
	an := analyze(src, "", 0)
	got := applyEdits(src, renameInDocument(src, &an, an.lookup("Q", 3), "Out"))
	if !strings.Contains(got, " Out : BOOL;") || !strings.Contains(got, "Out := t1.Q;") {
		t.Fatalf("rename Q → Out:\n%s", got)
	}
}

// #195: the T of T#2s is a literal prefix, never the local T.
func TestRenameSkipsLiteralPrefixes(t *testing.T) {
	src := "PROGRAM P\nVAR\n T : BOOL;\n d : TIME;\nEND_VAR\nd := T#2s;\nT := NOT T;\nEND_PROGRAM\n"
	an := analyze(src, "", 0)
	got := applyEdits(src, renameInDocument(src, &an, an.lookup("T", 3), "Flag"))
	for _, want := range []string{" Flag : BOOL;", "d := T#2s;", "Flag := NOT Flag;"} {
		if !strings.Contains(got, want) {
			t.Fatalf("rename T → Flag: missing %q in\n%s", want, got)
		}
	}
}

// #195: a local is renamed only inside the POU that declares it. Beta's own
// n is a different symbol, and the program's undeclared n is nobody's —
// lookup() would fall back to Alpha's for it.
func TestRenameStaysInItsPOU(t *testing.T) {
	src := "FUNCTION_BLOCK Alpha\nVAR\n n : INT;\nEND_VAR\nn := n + 1;\nEND_FUNCTION_BLOCK\n\n" +
		"FUNCTION_BLOCK Beta\nVAR\n n : INT;\nEND_VAR\nn := 2;\nEND_FUNCTION_BLOCK\n\n" +
		"PROGRAM P\nVAR\n x : INT;\nEND_VAR\nx := n;\nEND_PROGRAM\n"
	an := analyze(src, "", 0)
	got := applyEdits(src, renameInDocument(src, &an, an.lookup("n", 3), "count"))
	want := "FUNCTION_BLOCK Alpha\nVAR\n count : INT;\nEND_VAR\ncount := count + 1;\nEND_FUNCTION_BLOCK\n\n" +
		"FUNCTION_BLOCK Beta\nVAR\n n : INT;\nEND_VAR\nn := 2;\nEND_FUNCTION_BLOCK\n\n" +
		"PROGRAM P\nVAR\n x : INT;\nEND_VAR\nx := n;\nEND_PROGRAM\n"
	if got != want {
		t.Fatalf("rename Alpha's n:\n%s", got)
	}
}

// Renaming a FUNCTION_BLOCK's variable also renames it where an instance of
// that FB uses it — p1.Run and p1(Run := …) — but not another FB's
// same-named member or a same-named local, and asking at any of those
// sites gives the same edit.
const renameFBParamSrc = `FUNCTION_BLOCK Pump
VAR_INPUT
    Run : BOOL;
END_VAR
VAR_OUTPUT
    On : BOOL;
END_VAR
On := Run;
END_FUNCTION_BLOCK

FUNCTION_BLOCK Fan
VAR_INPUT
    Run : BOOL;
END_VAR
END_FUNCTION_BLOCK

PROGRAM P
VAR
    p1 : Pump;
    f1 : Fan;
    Run : BOOL;
END_VAR
p1(Run := Run);
f1(Run := p1.Run);
END_PROGRAM
`

func TestRenameFBVariableFollowsInstances(t *testing.T) {
	s := startSession(t)
	s.recvResponse(s.send("initialize", map[string]any{}, true))
	const uri = "file:///fb.st"
	s.refOpen(uri, renameFBParamSrc)
	want := strings.NewReplacer(
		"    Run : BOOL;\nEND_VAR\nVAR_OUTPUT", "    Enable : BOOL;\nEND_VAR\nVAR_OUTPUT",
		"On := Run;", "On := Enable;",
		"p1(Run := Run);", "p1(Enable := Run);",
		"f1(Run := p1.Run);", "f1(Run := p1.Enable);",
	).Replace(renameFBParamSrc)
	for _, at := range []Position{
		{Line: 2, Character: 5},   // the declaration
		{Line: 22, Character: 4},  // the formal in p1's call
		{Line: 23, Character: 14}, // the member p1.Run
	} {
		id := s.send("textDocument/rename", RenameParams{
			TextDocument: TextDocumentIdentifier{URI: uri}, Position: at, NewName: "Enable",
		}, true)
		var we WorkspaceEdit
		if err := json.Unmarshal(s.recvResponse(id), &we); err != nil {
			t.Fatal(err)
		}
		if got := applyEdits(renameFBParamSrc, we.Changes[uri]); got != want {
			t.Fatalf("rename from %+v:\n%s", at, got)
		}
	}
	// Find All References on the declaration reports the same four sites.
	refSameLines(t, refLines(t, s.refsAt(uri, 2, 5, true), "Run", map[string]string{uri: renameFBParamSrc}),
		"fb.st:3", "fb.st:8", "fb.st:23", "fb.st:24")
}

// Rename edits exactly the sites Find All References reports, diagrams
// included: a tag from an ST program and from inside an FBD.
func TestRenameEditsWhatReferencesFind(t *testing.T) {
	files := map[string]string{
		"nautilus.yaml": diagManifest,
		"main.st":       diagMain,
		"level.fbd":     diagFBD,
		"perms.ld":      diagLD,
	}
	_, paths := refProject(t, files)
	s := startSession(t)
	s.recvResponse(s.send("initialize", map[string]any{}, true))
	for _, at := range []struct {
		file      string
		line, col int
	}{{"main.st", 4, 0}, {"level.fbd", 10, 3}} {
		uri := pathToURI(paths[at.file])
		s.refOpen(uri, files[at.file])
		want := map[string][]Range{}
		for _, l := range s.refsAt(uri, at.line, at.col, true) {
			want[l.URI] = append(want[l.URI], l.Range)
		}
		id := s.send("textDocument/rename", RenameParams{
			TextDocument: TextDocumentIdentifier{URI: uri},
			Position:     Position{Line: at.line, Character: at.col},
			NewName:      "Renamed",
		}, true)
		var we WorkspaceEdit
		if err := json.Unmarshal(s.recvResponse(id), &we); err != nil {
			t.Fatal(err)
		}
		got := map[string][]Range{}
		for u, edits := range we.Changes {
			for _, e := range edits {
				got[u] = append(got[u], e.Range)
			}
			sort.Slice(got[u], func(i, j int) bool { return posLess(got[u][i].Start, got[u][j].Start) })
		}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("%s: rename edits\n %v\nreferences\n %v", at.file, got, want)
		}
	}
}

// A builtin FB's member and a literal prefix have no declaration in the
// file to rename: prepareRename refuses rather than offering a same-named
// local.
func TestPrepareRenameRefusesWhatIsNotTheSymbol(t *testing.T) {
	src := "PROGRAM P\nVAR\n Q : BOOL;\n T : BOOL;\n IN : BOOL;\n t1 : TON;\nEND_VAR\nt1(IN := T, PT := T#2s);\nQ := t1.Q;\nEND_PROGRAM\n"
	s := startSession(t)
	s.recvResponse(s.send("initialize", map[string]any{}, true))
	const uri = "file:///p.st"
	s.refOpen(uri, src)
	for _, at := range []Position{
		{Line: 8, Character: 8},  // the Q of t1.Q
		{Line: 7, Character: 18}, // the T of T#2s
		{Line: 7, Character: 3},  // TON's IN
	} {
		id := s.send("textDocument/prepareRename", TextDocumentPositionParams{
			TextDocument: TextDocumentIdentifier{URI: uri}, Position: at,
		}, true)
		if m := s.recvErrorOrResult(id); len(m.Error) == 0 {
			t.Fatalf("prepareRename at %+v should be refused, got %s", at, m.Result)
		}
	}
}
