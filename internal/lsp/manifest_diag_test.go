package lsp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// publishedUntilBarrier opens (or re-sends) a document and returns every
// diagnostics publication the server makes for it, by URI, the last one
// per URI winning. A hover request after the open is the barrier: the
// server handles messages in order, so everything published for the open precedes
// the hover's response.
func (s *lspSession) publishedUntilBarrier(uri, text string, version int) map[string][]Diagnostic {
	s.t.Helper()
	if version == 1 {
		s.send("textDocument/didOpen", DidOpenTextDocumentParams{
			TextDocument: TextDocumentItem{URI: uri, LanguageID: "iec-st", Version: 1, Text: text},
		}, false)
	} else {
		s.send("textDocument/didChange", map[string]any{
			"textDocument":   map[string]any{"uri": uri, "version": version},
			"contentChanges": []map[string]any{{"text": text}},
		}, false)
	}
	// Sent from a goroutine: the pipes are unbuffered, and the server is
	// writing the open's publications while this is written.
	id := s.nextID + 1
	go s.send("textDocument/hover", TextDocumentPositionParams{
		TextDocument: TextDocumentIdentifier{URI: uri}, Position: Position{Line: 0, Character: 0},
	}, true)
	out := map[string][]Diagnostic{}
	for {
		m := s.recv()
		if m.ID != nil {
			var got int
			if json.Unmarshal(*m.ID, &got) == nil && got == id {
				return out
			}
			continue
		}
		if m.Method != "textDocument/publishDiagnostics" {
			continue
		}
		var pub PublishDiagnosticsParams
		if err := json.Unmarshal(m.Params, &pub); err != nil {
			s.t.Fatal(err)
		}
		out[pub.URI] = pub.Diagnostics
	}
}

// A manifest tag that does not read (#518) is reported on the manifest, on
// that tag's name, and the programs are not blamed for it: the tags that do
// read stay in scope, and a use of the bad one is not "undeclared". Before,
// the server dropped every tag and each program said its first tag was an
// undeclared identifier, with nothing on the manifest.
func TestManifestTagErrorIsReportedOnTheManifest(t *testing.T) {
	dir := t.TempDir()
	good := "name: lift-station\ntasks:\n  - program: main.st\n    scan: 100ms\ntags:\n" +
		"  - { name: Level, role: input, type: REAL, init: 42.0 }\n" +
		"  - { name: PumpRun, role: output, type: BOOL, init: false }\n"
	mpath := filepath.Join(dir, "nautilus.yaml")
	if err := os.WriteFile(mpath, []byte(good+"  - { name: Horn, role: command, type: BOOL }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	prog := filepath.Join(dir, "main.st")
	src := "PROGRAM Main\nPumpRun := Level > 50.0;\nEND_PROGRAM\n"
	if err := os.WriteFile(prog, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	uri, muri := pathToURI(prog), pathToURI(mpath)

	s := startSession(t)
	s.recvResponse(s.send("initialize", map[string]any{}, true))
	s.send("initialized", map[string]any{}, false)

	got := s.publishedUntilBarrier(uri, src, 1)
	if d := got[uri]; len(d) != 0 {
		t.Errorf("main.st blamed for the manifest's tag error: %+v", d)
	}
	md := got[muri]
	if len(md) != 1 || !strings.Contains(md[0].Message, "tag Horn") || !strings.Contains(md[0].Message, "role must be") {
		t.Fatalf("nautilus.yaml diagnostics = %+v, want one naming tag Horn's role", md)
	}
	if md[0].Range.Start.Line != 7 {
		t.Errorf("tag Horn's error is on line %d, want 7 (its tags[].name)", md[0].Range.Start.Line)
	}

	// A program using the bad tag is not told it is undeclared either.
	src2 := "PROGRAM Main\nPumpRun := Horn AND Level > 50.0;\nEND_PROGRAM\n"
	if d := s.publishedUntilBarrier(uri, src2, 2)[uri]; len(d) != 0 {
		t.Errorf("main.st's use of the unreadable tag Horn is reported: %+v", d)
	}
	// A real error in the program still is.
	src3 := "PROGRAM Main\nPumpRun := Bogus;\nEND_PROGRAM\n"
	if d := s.publishedUntilBarrier(uri, src3, 3)[uri]; len(d) != 1 || !strings.Contains(d[0].Message, "Bogus") {
		t.Errorf("main.st's undeclared Bogus = %+v, want it reported", d)
	}

	// Fixed: the next analysis clears the manifest's diagnostic.
	if err := os.WriteFile(mpath, []byte(good+"  - { name: Horn, role: output, type: BOOL }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(mpath, later, later); err != nil {
		t.Fatal(err)
	}
	got = s.publishedUntilBarrier(uri, src2, 4)
	if d := got[uri]; len(d) != 0 {
		t.Errorf("main.st after the fix: %+v", d)
	}
	if md, ok := got[muri]; !ok || len(md) != 0 {
		t.Errorf("nautilus.yaml after the fix = %+v (published %v), want cleared", md, ok)
	}
}

// A manifest that does not read at all (an unknown field) is reported on
// the line YAML names, and with no tag known, a program's tags are not
// called undeclared.
func TestUnreadableManifestIsReportedOnTheManifest(t *testing.T) {
	dir := t.TempDir()
	mpath := filepath.Join(dir, "nautilus.yaml")
	if err := os.WriteFile(mpath, []byte("name: lift-station\ntasks:\n  - program: main.st\n    scan: 100ms\ntags:\n"+
		"  - { name: Level, rol: input, type: REAL }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	prog := filepath.Join(dir, "main.st")
	src := "PROGRAM Main\nLevel := Level + 1.0;\nEND_PROGRAM\n"
	if err := os.WriteFile(prog, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	uri, muri := pathToURI(prog), pathToURI(mpath)
	s := startSession(t)
	s.recvResponse(s.send("initialize", map[string]any{}, true))
	s.send("initialized", map[string]any{}, false)
	got := s.publishedUntilBarrier(uri, src, 1)
	if d := got[uri]; len(d) != 0 {
		t.Errorf("main.st blamed for the unreadable manifest: %+v", d)
	}
	md := got[muri]
	if len(md) != 1 || !strings.Contains(md[0].Message, "rol") || md[0].Range.Start.Line != 5 {
		t.Fatalf("nautilus.yaml diagnostics = %+v, want one on line 6 naming field rol", md)
	}
}
