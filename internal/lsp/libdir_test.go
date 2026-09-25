package lsp

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// copyLibDir copies internal/project/testdata/libdir (shared code in lib/,
// one level nested) into a temp dir and returns it.
func copyLibDir(t *testing.T) string {
	t.Helper()
	src := "../project/testdata/libdir"
	dst := t.TempDir()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out := filepath.Join(dst, rel)
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		return os.WriteFile(out, raw, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}

// openAndDiagnose opens a file from disk and returns its diagnostics.
func (s *lspSession) openAndDiagnose(path, lang string) (string, []Diagnostic) {
	s.t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		s.t.Fatal(err)
	}
	uri := pathToURI(path)
	s.send("textDocument/didOpen", DidOpenTextDocumentParams{
		TextDocument: TextDocumentItem{URI: uri, LanguageID: lang, Version: 1, Text: string(raw)},
	}, false)
	for {
		m := s.recv()
		if m.Method != "textDocument/publishDiagnostics" {
			continue
		}
		var pub PublishDiagnosticsParams
		if err := json.Unmarshal(m.Params, &pub); err != nil {
			s.t.Fatal(err)
		}
		if pub.URI == uri {
			return uri, pub.Diagnostics
		}
	}
}

// A root program resolves types and blocks from lib/ (at any depth), and a
// lib/ file resolves what the root and the rest of lib/ declare — the
// composition `naut check` and the runtime use.
func TestLibDirResolvesInTheLanguageServer(t *testing.T) {
	dir := copyLibDir(t)
	// A root library and a lib/ block that leans on both it and lib/physics.
	os.WriteFile(filepath.Join(dir, "units.st"), []byte("TYPE Setpoints :\nSTRUCT\n    Hi : REAL;\nEND_STRUCT;\nEND_TYPE\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "lib", "watch.st"), []byte(
		"FUNCTION_BLOCK Watch\nVAR_INPUT sp : Setpoints; END_VAR\nVAR t : TankModel; s : TankState; END_VAR\nEND_FUNCTION_BLOCK\n"), 0o644)

	s := startSession(t)
	s.recvResponse(s.send("initialize", map[string]any{}, true))
	s.send("initialized", map[string]any{}, false)

	uri, diags := s.openAndDiagnose(filepath.Join(dir, "main.st"), "iec-st")
	if len(diags) != 0 {
		t.Fatalf("main.st diagnostics = %+v", diags)
	}
	// Hover on TankState (declared in lib/physics/tank.st), on main.st line 13.
	id := s.send("textDocument/hover", TextDocumentPositionParams{
		TextDocument: TextDocumentIdentifier{URI: uri},
		Position:     Position{Line: 12, Character: 14},
	}, true)
	var h Hover
	if err := json.Unmarshal(s.recvResponse(id), &h); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h.Contents.Value, "TankState") || !strings.Contains(h.Contents.Value, "Level") {
		t.Errorf("hover on TankState = %q, want its TYPE from lib/physics/tank.st", h.Contents.Value)
	}

	if _, diags := s.openAndDiagnose(filepath.Join(dir, "lib", "watch.st"), "iec-st"); len(diags) != 0 {
		t.Fatalf("lib/watch.st diagnostics = %+v", diags)
	}
	if _, diags := s.openAndDiagnose(filepath.Join(dir, "lib", "rungs.ld"), "iec-ld"); len(diags) != 0 {
		t.Fatalf("lib/rungs.ld diagnostics = %+v", diags)
	}
}
