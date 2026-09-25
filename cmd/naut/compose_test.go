package main

// `naut compose` is what the VS Code extension downloads for an online
// edit, so it must be byte-identical to what the runtime composes itself —
// .ld/.fbd libraries transpiled into the prelude, lib/ included — and must
// refuse what `naut check` refuses.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/internal/project"
	"github.com/joyautomation/nautilus/internal/stproject"
	"github.com/joyautomation/nautilus/runtime"
)

func compose(t *testing.T, stdin string, args ...string) (string, string, int) {
	t.Helper()
	var out, errb bytes.Buffer
	code := composeTo(args, strings.NewReader(stdin), &out, &errb)
	return out.String(), errb.String(), code
}

// writeTree materializes a name → body map under a fresh directory.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestComposeMatchesTheRuntime(t *testing.T) {
	out, stderr, code := compose(t, "", filepath.Join(libDirFixture, "main.st"))
	if code != 0 {
		t.Fatalf("naut compose = %d: %s", code, stderr)
	}
	// The ladder library arrives transpiled, not as rungs.
	if !strings.Contains(out, "FUNCTION_BLOCK PumpSeq") {
		t.Errorf("prelude is missing the ladder library's block:\n%s", out)
	}
	if strings.Contains(out, "RUNG") || strings.Contains(out, "END_LD") {
		t.Errorf("the ladder library should be transpiled to ST, got rungs:\n%s", out)
	}
	if strings.Contains(out, "hmi/") || strings.Count(out, "FUNCTION_BLOCK HiAlarm") != 1 {
		t.Errorf("hmi/ is not a library directory:\n%s", out)
	}

	// Exactly the runtime's composition.
	p, err := project.Load(os.DirFS(libDirFixture), "")
	if err != nil {
		t.Fatal(err)
	}
	want := stproject.Join(p.Runtime.Libraries, p.Runtime.Program)
	if out != want {
		t.Errorf("naut compose differs from the runtime's composition\n--- compose\n%s\n--- runtime\n%s", out, want)
	}

	// A project with one program composes from the directory as well.
	if dirOut, _, code := compose(t, "", libDirFixture); code != 0 || dirOut != out {
		t.Errorf("compose <dir> = %d, want the one program's composition", code)
	}
}

func TestComposeJSON(t *testing.T) {
	out, stderr, code := compose(t, "", "--json", filepath.Join(libDirFixture, "main.st"))
	if code != 0 {
		t.Fatalf("naut compose --json = %d: %s", code, stderr)
	}
	var res composeResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(res.Libraries, " "), "lib/blocks.st lib/physics/tank.st lib/rungs.ld"; got != want {
		t.Errorf("libraries = %s, want %s (ST first, then graphical, path order)", got, want)
	}
	if !strings.Contains(res.Prelude, "FUNCTION_BLOCK PumpSeq") {
		t.Errorf("prelude is missing the transpiled ladder block")
	}
	if res.File != "main.st" || res.POU != "Main" || res.Language != "st" {
		t.Errorf("program = %s/%s/%s, want main.st/Main/st", res.File, res.POU, res.Language)
	}
	if res.Source != res.Prelude+res.Program {
		t.Errorf("source must be prelude + program")
	}
	if len(res.Programs) != 1 || res.Programs[0].File != "main.st" {
		t.Errorf("programs = %+v", res.Programs)
	}

	// A library file names no program but still describes the project.
	out, _, code = compose(t, "", "--json", filepath.Join(libDirFixture, "lib", "rungs.ld"))
	res = composeResult{}
	if err := json.Unmarshal([]byte(out), &res); code != 0 || err != nil {
		t.Fatalf("compose --json <library> = %d, %v", code, err)
	}
	if res.File != "" || len(res.Programs) != 1 {
		t.Errorf("a library file resolves to its project, with no program named: %+v", res)
	}
	if _, stderr, code := compose(t, "", filepath.Join(libDirFixture, "lib", "rungs.ld")); code == 0 || !strings.Contains(stderr, "declares no PROGRAM") {
		t.Errorf("text compose of a library should say it isn't a program: %d %s", code, stderr)
	}
}

// A graphical program stays in its own language after the prelude — the
// controller transpiles it and reports the original, which is what diff and
// pull compare against.
func TestComposeGraphicalProgramUsesLadderLibrary(t *testing.T) {
	files := libDirFiles(t)
	delete(files, "main.st")
	files["main.ld"] = "PROGRAM Main\nVAR_EXTERNAL Start : BOOL; Stop : BOOL; Run : BOOL; END_VAR\n" +
		"VAR seq : PumpSeq; END_VAR\nLD\n  RUNG call\n    seq:PumpSeq(Start := Start, Stop := Stop, Run => Run)\nEND_LD\nEND_PROGRAM\n"
	files["nautilus.yaml"] = strings.Replace(files["nautilus.yaml"], "program: main.st", "program: main.ld", 1)
	dir := writeTree(t, files)
	out, stderr, code := compose(t, "", "--json", filepath.Join(dir, "main.ld"))
	if code != 0 {
		t.Fatalf("compose = %d: %s", code, stderr)
	}
	var res composeResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err)
	}
	if res.Language != "ld" || res.Program != files["main.ld"] {
		t.Errorf("program should be the .ld file as written, got %s:\n%s", res.Language, res.Program)
	}
	if !strings.Contains(res.Prelude, "FUNCTION_BLOCK PumpSeq") {
		t.Errorf("prelude is missing the ladder library the program instantiates")
	}
	// What a download sends is what the controller compiles — and accepts.
	if _, err := runtime.Compile(res.Source); err != nil {
		t.Errorf("the controller would refuse the composed source: %v", err)
	}
	// Without the ladder library (the old .st-only prelude) it would not.
	var stOnly strings.Builder
	for _, f := range []string{"lib/blocks.st", "lib/physics/tank.st"} {
		stOnly.WriteString(files[f])
	}
	if _, err := runtime.Compile(stOnly.String() + res.Program); err == nil {
		t.Errorf("an .st-only prelude should not compile a program using a ladder block")
	}
}

func TestComposeRefusesAProgramInLib(t *testing.T) {
	files := libDirFiles(t)
	files["lib/extra.st"] = "PROGRAM Extra\nVAR x : BOOL; END_VAR\nx := TRUE;\nEND_PROGRAM\n"
	dir := writeTree(t, files)
	_, stderr, code := compose(t, "", filepath.Join(dir, "main.st"))
	if code != 1 {
		t.Fatalf("compose = %d, want 1", code)
	}
	if !strings.Contains(stderr, "lib/extra.st declares a PROGRAM, but lib/ holds libraries only") {
		t.Errorf("stderr should name the file and the rule: %s", stderr)
	}
}

func TestComposeRefusesABrokenLadderLibrary(t *testing.T) {
	files := libDirFiles(t)
	files["lib/bad.ld"] = "FUNCTION_BLOCK Bad\nVAR_OUTPUT Q : BOOL; END_VAR\nLD\n  RUNG r [ ( Q )\nEND_LD\nEND_FUNCTION_BLOCK\n"
	dir := writeTree(t, files)
	_, stderr, code := compose(t, "", "--json", filepath.Join(dir, "main.st"))
	if code != 1 || !strings.Contains(stderr, "lib/bad.ld") {
		t.Errorf("compose = %d, want 1 naming lib/bad.ld: %s", code, stderr)
	}
}

// Unsaved editor buffers (project-relative paths) win over the disk.
func TestComposeOverrides(t *testing.T) {
	edited := strings.Replace(libDirFiles(t)["lib/rungs.ld"], "PumpSeq", "PumpSeq2", 1)
	stdin, _ := json.Marshal(map[string]string{"lib/rungs.ld": edited})
	out, stderr, code := compose(t, string(stdin), "--overrides", "-", "--json", libDirFixture)
	if code != 0 {
		t.Fatalf("compose = %d: %s", code, stderr)
	}
	var res composeResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Prelude, "FUNCTION_BLOCK PumpSeq2") {
		t.Errorf("the override should win over the file on disk")
	}
}

// Flags after the path work too.
func TestComposeFlagsAfterPath(t *testing.T) {
	out, _, code := compose(t, "", filepath.Join(libDirFixture, "main.st"), "--json")
	if code != 0 || !strings.HasPrefix(out, "{") {
		t.Errorf("compose <file> --json = %d:\n%s", code, out)
	}
}

// An editor keys its buffers by absolute path; those outside the project
// are ignored.
func TestComposeAbsoluteOverrides(t *testing.T) {
	abs, err := filepath.Abs(filepath.Join(libDirFixture, "lib", "rungs.ld"))
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(libDirFiles(t)["lib/rungs.ld"], "PumpSeq", "PumpSeq3", 1)
	elsewhere := filepath.Join(t.TempDir(), "lib", "rungs.ld")
	stdin, _ := json.Marshal(map[string]string{abs: edited, elsewhere: "garbage"})
	out, stderr, code := compose(t, string(stdin), "--overrides", "-", filepath.Join(libDirFixture, "main.st"))
	if code != 0 {
		t.Fatalf("compose = %d: %s", code, stderr)
	}
	if !strings.Contains(out, "FUNCTION_BLOCK PumpSeq3") || strings.Contains(out, "garbage") {
		t.Errorf("absolute override should apply, and only inside the project:\n%s", out)
	}
}
