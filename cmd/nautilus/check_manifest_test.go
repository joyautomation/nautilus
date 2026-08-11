package main

// Compiling every file proves each one is well-formed and says nothing about
// whether the tag set and the logic agree. That gap is what made a generated
// manifest risky to trust: a generator can emit a tag list that compiles
// perfectly and still faults every scan.
//
// The asymmetry these tests pin down is the whole design:
//
//	read an undeclared tag   → error   (faults the scan, every scan)
//	write an undeclared tag  → warning (runs; no unit or description)
//	declared input, unbound  → silent  (driver-fed telemetry is legitimate)
//	declared state, unbound  → warning (nothing writes it, nothing reads it)

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// checkIn runs runCheck against a temp project and returns its stdout.
func checkIn(t *testing.T, files map[string]string) (string, int) {
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
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	code := runCheck([]string{dir})
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
	return sb.String(), code
}

const checkManifestYAML = `
tasks:
  - program: program.st
tags:
  - { name: Sensor, role: input }
  - { name: Actuator, role: output }
`

func programBinding(extraDecls, extraBody string) string {
	return `PROGRAM Main
VAR_EXTERNAL
    Sensor   : REAL;
    Actuator : REAL;
` + extraDecls + `END_VAR
Actuator := Sensor;
` + extraBody + `END_PROGRAM`
}

func TestCheckManifestAgreesWithPrograms(t *testing.T) {
	out, code := checkIn(t, map[string]string{
		"nautilus.yaml": checkManifestYAML,
		"program.st":    programBinding("", ""),
	})
	if code != 0 {
		t.Errorf("a project whose tags and logic agree failed the check (%d):\n%s", code, out)
	}
	if strings.Contains(out, ": warning:") || strings.Contains(out, ": error:") {
		t.Errorf("clean project produced diagnostics:\n%s", out)
	}
}

// Acceptance bar 5.3: check FAILS a project whose program reads a tag the
// manifest never declares, and says which. Verified against a real run: such
// a program logs a logic error on every scan.
func TestCheckFailsOnUndeclaredRead(t *testing.T) {
	out, code := checkIn(t, map[string]string{
		"nautilus.yaml": checkManifestYAML,
		"program.st":    programBinding("    Ghost : REAL;\n", "Actuator := Ghost;\n"),
	})
	if code == 0 {
		t.Fatalf("reading an undeclared tag passed the check:\n%s", out)
	}
	if !strings.Contains(out, ": error:") || !strings.Contains(out, "Ghost") {
		t.Errorf("output does not report Ghost as an error:\n%s", out)
	}
}

// Writing one is survivable, so it must not fail the build — the controller
// runs, the tag just reaches an HMI undocumented.
func TestCheckWarnsOnUndeclaredWrite(t *testing.T) {
	out, code := checkIn(t, map[string]string{
		"nautilus.yaml": checkManifestYAML,
		"program.st":    programBinding("    Scratch : REAL;\n", "Scratch := Sensor;\n"),
	})
	if code != 0 {
		t.Errorf("writing an undeclared tag failed the check; it runs fine (%d):\n%s", code, out)
	}
	if !strings.Contains(out, ": warning:") || !strings.Contains(out, "Scratch") {
		t.Errorf("output does not warn about Scratch:\n%s", out)
	}
	if strings.Contains(out, ": error:") {
		t.Errorf("a write-only undeclared tag was reported as an error:\n%s", out)
	}
}

// Assigning one field of a struct tag reads the aggregate first, so a
// compound target counts as a read — the case a naive walker gets wrong.
func TestCheckTreatsFieldAssignmentAsRead(t *testing.T) {
	out, code := checkIn(t, map[string]string{
		"nautilus.yaml": checkManifestYAML,
		"types.st":      "TYPE Motor : STRUCT Speed : REAL; END_STRUCT; END_TYPE\n",
		"program.st":    programBinding("    P101 : Motor;\n", "P101.Speed := Sensor;\n"),
	})
	if code == 0 {
		t.Fatalf("assigning a field of an undeclared struct tag passed the check:\n%s", out)
	}
	if !strings.Contains(out, "P101") {
		t.Errorf("output does not mention P101:\n%s", out)
	}
}

// An unbound INPUT is driver-fed telemetry — the bulk of any imported tag
// list. Warning on those would train people to ignore this warning before it
// ever caught anything (examples/client60 has eight).
func TestCheckStaysQuietOnUnboundInputs(t *testing.T) {
	out, code := checkIn(t, map[string]string{
		"nautilus.yaml": checkManifestYAML +
			"  - { name: Telemetry, role: input }\n",
		"program.st": programBinding("", ""),
	})
	if code != 0 {
		t.Errorf("an unbound input failed the check (%d):\n%s", code, out)
	}
	if strings.Contains(out, "Telemetry") {
		t.Errorf("an unbound input was reported; it is legitimate telemetry:\n%s", out)
	}
}

// A state or setpoint exists to be read by logic. Unbound, it is dead — or a
// stale entry in a generated tag file, which is the case worth catching.
func TestCheckWarnsOnUnboundState(t *testing.T) {
	out, code := checkIn(t, map[string]string{
		"nautilus.yaml": checkManifestYAML +
			"  - { name: Orphan, role: state, init: 0.0 }\n",
		"program.st": programBinding("", ""),
	})
	if code != 0 {
		t.Errorf("an unbound state failed the check; it is a warning (%d):\n%s", code, out)
	}
	if !strings.Contains(out, ": warning:") || !strings.Contains(out, "Orphan") {
		t.Errorf("output does not warn about Orphan:\n%s", out)
	}
}

// A task's dt-tag is written by the runtime every scan, so a program may read
// it with no tags: entry. It is declared — just not under tags: — and
// reporting it would be a false alarm on the one tag the manifest is most
// certain about.
func TestCheckAcceptsDtTag(t *testing.T) {
	out, code := checkIn(t, map[string]string{
		"nautilus.yaml": `
tasks:
  - program: program.st
    dt-tag: ScanDt
tags:
  - { name: Sensor, role: input }
  - { name: Actuator, role: output }
`,
		"program.st": programBinding("    ScanDt : REAL;\n", "Actuator := Sensor * ScanDt;\n"),
	})
	if code != 0 || strings.Contains(out, "ScanDt") {
		t.Errorf("the dt-tag was reported as undeclared (%d):\n%s", code, out)
	}
}

// Composition and verification landed together on purpose: a tag file is
// where a generated tag set lives, so the cross-check has to see through it.
func TestCheckSeesTagFiles(t *testing.T) {
	out, code := checkIn(t, map[string]string{
		"nautilus.yaml": `
tasks:
  - program: program.st
tag-files: [tags/gen.yaml]
`,
		"tags/gen.yaml": "- { name: Sensor, role: input }\n- { name: Actuator, role: output }\n",
		"program.st":    programBinding("", ""),
	})
	if code != 0 {
		t.Errorf("tags declared in a tag file were not seen by check (%d):\n%s", code, out)
	}
}

// A directory that is not a manifest project still compiles its files; the
// cross-check simply has nothing to say.
func TestCheckSkipsNonManifestProjects(t *testing.T) {
	out, code := checkIn(t, map[string]string{
		"program.st": programBinding("", ""),
	})
	if code != 0 {
		t.Errorf("a bare source tree failed the check (%d):\n%s", code, out)
	}
}

// tag-meta layers documentation onto tags declared elsewhere. A key matching
// nothing documents nothing — the typo case in a hand-written block — while
// a dotted key is a field path and legitimate.
func TestCheckWarnsOnTagMetaNamingNothing(t *testing.T) {
	out, code := checkIn(t, map[string]string{
		"nautilus.yaml": checkManifestYAML + `tag-meta:
  Sensor: { unit: "°C" }
  Sensr: { unit: "°C" }
  Sensor.Raw: { unit: counts }
`,
		"program.st": programBinding("", ""),
	})
	if code != 0 {
		t.Errorf("tag-meta warnings should not fail the check (%d):\n%s", code, out)
	}
	if !strings.Contains(out, "Sensr") {
		t.Errorf("a tag-meta key naming no tag was not reported:\n%s", out)
	}
	if strings.Contains(out, "Sensor.Raw") {
		t.Errorf("a dotted tag-meta key is a field path and must not be reported:\n%s", out)
	}
}
