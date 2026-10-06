package lsp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const logixManifest = `name: tp
tasks:
  - program: Main.ld
    scan: 10ms
tags:
  - { name: Go, role: input, init: false }
  - { name: Out, role: output, init: false }
`

const logixTargetSection = `target:
  logix:
    controller: TP
`

// A TP instance: the type is outside the v1 subset (line 7) and so is the
// block its rung runs (line 10, the rung).
const logixTP = `PROGRAM Main
VAR_EXTERNAL
    Go  : BOOL;
    Out : BOOL;
END_VAR
VAR
    pulse : TP;
END_VAR
LD
  RUNG one
    Go pulse:TP(PT := T#500MS) ( Out )
END_LD
END_PROGRAM
`

// writeLogixProject lays out a project with the given manifest and files
// and returns its root.
func writeLogixProject(t *testing.T, manifest string, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	files["nautilus.yaml"] = manifest
	for name, src := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func logixOnly(diags []Diagnostic) []Diagnostic {
	var out []Diagnostic
	for _, d := range diags {
		if d.Source == logixSource {
			out = append(out, d)
		}
	}
	return out
}

// The squiggles `naut check` reports for a Logix project, on the lines it
// reports them, with the rule in the Problems panel.
func TestLogixTargetRulesInTheEditor(t *testing.T) {
	root := writeLogixProject(t, logixManifest+logixTargetSection, map[string]string{"Main.ld": logixTP})
	_, diags := startSession(t).openAndDiagnose(filepath.Join(root, "Main.ld"), "nautilus-ld")
	if len(diags) != 2 {
		t.Fatalf("got %d diagnostics, want 2: %+v", len(diags), diags)
	}
	for i, want := range []struct {
		line int // 0-based
		rule string
	}{{6, "logix/type"}, {9, "logix/fb"}} {
		d := diags[i]
		if d.Range.Start.Line != want.line || d.Code != want.rule {
			t.Errorf("diagnostic %d = line %d %s, want line %d %s", i, d.Range.Start.Line, d.Code, want.line, want.rule)
		}
		if d.Source != logixSource || d.Severity != SeverityError {
			t.Errorf("diagnostic %d: source %q severity %d", i, d.Source, d.Severity)
		}
		if d.Range.End.Character <= d.Range.Start.Character {
			t.Errorf("diagnostic %d squiggles nothing: %+v", i, d.Range)
		}
	}
}

// Without a target the same program is a fine nautilus program.
func TestLogixRulesOnlyWithTheTarget(t *testing.T) {
	root := writeLogixProject(t, logixManifest, map[string]string{"Main.ld": logixTP})
	_, diags := startSession(t).openAndDiagnose(filepath.Join(root, "Main.ld"), "nautilus-ld")
	if len(diags) != 0 {
		t.Fatalf("no target, got %+v", diags)
	}
}

// A library is checked where a program uses it, not on its own: a type
// library, and a block library in a language the writer does not take.
func TestLogixRulesSkipLibraries(t *testing.T) {
	root := writeLogixProject(t, logixManifest+logixTargetSection, map[string]string{
		"Main.ld":   logixTP,
		"types.st":  "TYPE Pump :\nSTRUCT\n    Run : BOOL;\n    Speed : TIME;\nEND_STRUCT;\nEND_TYPE\n",
		"lib/b.fbd": "FUNCTION_BLOCK Edge\nVAR_INPUT x : BOOL; END_VAR\nVAR_OUTPUT q : BOOL; END_VAR\nVAR t : R_TRIG; END_VAR\nFBD\n  t(CLK := x)\n  q := t.Q\nEND_FBD\nEND_FUNCTION_BLOCK\n",
	})
	s := startSession(t)
	for _, f := range []string{"types.st", "lib/b.fbd"} {
		lang := "nautilus-st"
		if strings.HasSuffix(f, ".fbd") {
			lang = "nautilus-fbd"
		}
		if _, diags := s.openAndDiagnose(filepath.Join(root, filepath.FromSlash(f)), lang); len(diags) != 0 {
			t.Errorf("%s: got %+v", f, diags)
		}
	}
}

// An FBD program is one diagnostic on its PROGRAM line.
func TestLogixRulesRefuseAnFBDProgram(t *testing.T) {
	root := writeLogixProject(t, logixManifest+logixTargetSection, map[string]string{
		"Main.fbd": "PROGRAM Main\nVAR_EXTERNAL\n    Go  : BOOL;\n    Out : BOOL;\nEND_VAR\nFBD\n  Out := Go\nEND_FBD\nEND_PROGRAM\n",
	})
	_, diags := startSession(t).openAndDiagnose(filepath.Join(root, "Main.fbd"), "nautilus-fbd")
	if len(diags) != 1 || diags[0].Range.Start.Line != 0 || !strings.Contains(diags[0].Message, "only ladder") {
		t.Fatalf("got %+v", diags)
	}
}

// Target rules on a file that does not compile are noise: only the compile
// error shows.
func TestLogixRulesWaitForACompile(t *testing.T) {
	broken := strings.Replace(logixTP, "( Out )", "( Missing )", 1)
	root := writeLogixProject(t, logixManifest+logixTargetSection, map[string]string{"Main.ld": broken})
	_, diags := startSession(t).openAndDiagnose(filepath.Join(root, "Main.ld"), "nautilus-ld")
	if len(diags) == 0 {
		t.Fatal("a broken file reported nothing")
	}
	if got := logixOnly(diags); len(got) != 0 {
		t.Fatalf("target rules ran on a broken file: %+v", got)
	}
}

// Adding `target: logix` to the manifest turns the rules on without a
// restart: the manifest cache is keyed on modtime.
func TestLogixTargetCacheInvalidates(t *testing.T) {
	root := writeLogixProject(t, logixManifest, map[string]string{"Main.ld": logixTP})
	prog := filepath.Join(root, "Main.ld")
	if logixTarget(prog) {
		t.Fatal("no target section, but logixTarget is true")
	}
	manifest := filepath.Join(root, "nautilus.yaml")
	if err := os.WriteFile(manifest, []byte(logixManifest+logixTargetSection), 0o644); err != nil {
		t.Fatal(err)
	}
	ahead := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(manifest, ahead, ahead); err != nil {
		t.Fatal(err)
	}
	if !logixTarget(prog) {
		t.Fatal("after adding target: logix, logixTarget is false — cache did not invalidate")
	}
}
