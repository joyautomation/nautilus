package main

// The lang/l5x tests prove the reader reads. These prove the two files it
// writes are a nautilus project: `nautilus logix import` then `nautilus
// check`, with a program that actually binds the imported tags and their
// imported types. That is the same posture as lang/stgen — generate, then
// let the compiler have the last word — carried out to the CLI.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/lang/ld"
)

const varietyL5X = "../../lang/l5x/testdata/variety.L5X"

func TestLogixImportProducesACheckableProject(t *testing.T) {
	dir := t.TempDir()
	if code := runLogixImport([]string{"--out", dir, "--scope", "*", varietyL5X}); code != 0 {
		t.Fatalf("import exited %d", code)
	}
	types, err := os.ReadFile(filepath.Join(dir, "logix_types.st"))
	if err != nil {
		t.Fatal(err)
	}
	tags, err := os.ReadFile(filepath.Join(dir, "tags", "logix.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	// A program that binds the controller's tags at the controller's own
	// types — the UDT nested three deep, and a predefined TIMER.
	out, code := checkIn(t, map[string]string{
		"nautilus.yaml": `
tasks:
  - program: program.st
tag-files:
  - tags/logix.yaml
tags:
  - { name: HiAlarm, role: state, init: false, desc: "Level above the high setpoint" }
`,
		"tags/logix.yaml": string(tags),
		"logix_types.st":  string(types),
		"program.st": `PROGRAM Main
VAR_EXTERNAL
    P101_Level : Analog_Input;
    DwellTmr   : TIMER;
    StartPB    : BOOL;
    HiAlarm    : BOOL;
END_VAR
HiAlarm := StartPB AND (P101_Level.EU >= P101_Level.Alarms.Hi) AND DwellTmr.DN;
END_PROGRAM`,
	})
	if code != 0 {
		t.Fatalf("an imported project did not check out (%d):\n%s", code, out)
	}
	if strings.Contains(out, ": error:") {
		t.Errorf("imported project produced errors:\n%s", out)
	}
}

// The descriptions are the reason to read an L5X at all: Logix keeps tag
// documentation in the offline project, so `nautilus eip import` from a
// live controller has to leave desc: empty (eip/codegen/tags.go).
func TestLogixImportRecoversDescriptions(t *testing.T) {
	dir := t.TempDir()
	if code := runLogixImport([]string{"--out", dir, varietyL5X}); code != 0 {
		t.Fatalf("import exited %d", code)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "tags", "logix.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `desc: "HS-101 start pushbutton"`) {
		t.Errorf("tag file lost the controller's documentation:\n%s", raw)
	}
	// Controller scope by default, so a program's tags stay out.
	if strings.Contains(string(raw), "MainProgram_Scratch") {
		t.Errorf("default scope reached into a program:\n%s", raw)
	}
}

// graph emits exactly what `nautilus ld graph` emits for a .ld file, so
// the ladder preview and the revision diff consume a Logix routine without
// knowing it is one.
func TestLogixGraphEmitsTheLadderModel(t *testing.T) {
	out := captureStdout(t, func() int {
		return runLogixGraph([]string{varietyL5X, "MainRoutine"})
	})
	var m ld.Model
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("not a ladder model: %v\n%s", err, out)
	}
	if len(m.Rungs) != 6 {
		t.Fatalf("rungs = %d, want 6", len(m.Rungs))
	}
	first := m.Rungs[0]
	if first.Line != 147 || first.Coils[0].Ref != "RunCmd" {
		t.Errorf("first rung = %+v", first)
	}
}

func TestLogixGraphReportsErrorsAsJSON(t *testing.T) {
	out := captureStdout(t, func() int {
		return runLogixGraph([]string{varietyL5X, "NoSuchRoutine"})
	})
	var msg struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(out), &msg); err != nil || msg.Error == "" {
		t.Fatalf("want {\"error\": ...} like `nautilus ld graph`, got: %s", out)
	}
}

// Drift detection, in one command: two exports of the same project are the
// same project even though every export stamps a new ExportDate.
func TestLogixNormalizeChecksDrift(t *testing.T) {
	const a = "../../lang/l5x/testdata/demoline.L5X"
	const b = "../../lang/l5x/testdata/demoline.v80.L5X"
	if code := runLogixNormalize([]string{"--check", a, a}); code != 0 {
		t.Errorf("an export should match itself, exited %d", code)
	}
	if code := runLogixNormalize([]string{"--check", b, a}); code != 1 {
		t.Errorf("a changed setpoint should be reported as drift, exited %d", code)
	}
}

func TestLogixNormalizeWritesAPinnedExport(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "pinned.L5X")
	if code := runLogixNormalize([]string{"-o", dst, varietyL5X}); code != 0 {
		t.Fatalf("normalize exited %d", code)
	}
	raw, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "Thu Sep 17 08:14:03 2026") {
		t.Errorf("ExportDate survived normalization:\n%s", firstLine(string(raw)))
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// captureStdout runs fn with stdout redirected and returns what it wrote.
func captureStdout(t *testing.T, fn func() int) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	fn()
	w.Close()
	os.Stdout = old
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	r.Close()
	return sb.String()
}
