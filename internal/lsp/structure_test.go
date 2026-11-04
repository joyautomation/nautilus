package lsp

import (
	"path/filepath"
	"testing"
)

// The structure warnings naut check prints (#561) reach the editor on the
// same lines, as warnings whose code is the rule ID, under the governing
// manifest's check: settings — and not on a buffer that does not compile.
func TestStructureWarningsInTheEditor(t *testing.T) {
	const prog = `PROGRAM Main
LD
  RUNG bare
    Go ( Out )
END_LD
END_PROGRAM
`
	root := writeLogixProject(t, logixManifest, map[string]string{"Main.ld": prog})
	_, diags := startSession(t).openAndDiagnose(filepath.Join(root, "Main.ld"), "nautilus-ld")
	if len(diags) != 1 {
		t.Fatalf("want one structure warning, got %+v", diags)
	}
	d := diags[0]
	if d.Code != "LD005" || d.Source != structureSource || d.Severity != SeverityWarning || d.Range.Start.Line != 2 {
		t.Errorf("got %+v, want LD005, a warning from %q, on line 3", d, structureSource)
	}

	root = writeLogixProject(t, logixManifest+"check:\n  disable: [LD005]\n", map[string]string{"Main.ld": prog})
	if _, diags := startSession(t).openAndDiagnose(filepath.Join(root, "Main.ld"), "nautilus-ld"); len(diags) != 0 {
		t.Errorf("LD005 disabled in the manifest, got %+v", diags)
	}

	broken := "PROGRAM Main\nLD\n  RUNG bare\n    Go ( Nope )\nEND_LD\nEND_PROGRAM\n"
	root = writeLogixProject(t, logixManifest, map[string]string{"Main.ld": broken})
	_, diags = startSession(t).openAndDiagnose(filepath.Join(root, "Main.ld"), "nautilus-ld")
	if !hasErrors(diags) {
		t.Fatalf("the broken buffer has no error: %+v", diags)
	}
	for _, d := range diags {
		if d.Source == structureSource {
			t.Errorf("a structure warning on a buffer that does not compile: %+v", d)
		}
	}
}
