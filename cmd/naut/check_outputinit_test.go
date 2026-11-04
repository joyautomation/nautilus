package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/internal/project"
	"github.com/joyautomation/nautilus/runtime"
)

// An output with no init: that a program reads before writing it faults
// the scan with "undefined tag" — a seal-in reads its own coil — and naut
// check used to pass it (nautilus#560). Now it is an error on the tag's
// manifest line, and each case below is also run for one scan, so the
// check and the runtime are held to the same answer.
func TestCheckReportsAnOutputReadBeforeItsFirstWrite(t *testing.T) {
	type tc struct {
		ext, prog string
		horn      string // the Horn tag line
		faults    bool
	}
	const noInit = "  - { name: Horn,  role: output,   type: BOOL }\n"
	const withInit = "  - { name: Horn,  role: output,   type: BOOL, init: false }\n"
	sealLD := "PROGRAM Main\nLD\n  RUNG horn (* seals in until acknowledged *)\n    [ Alarm | Horn ] /Ack ( Horn )\nEND_LD\nEND_PROGRAM\n"
	cases := map[string]tc{
		"ladder seal-in, no init": {ext: "ld", prog: sealLD, horn: noInit, faults: true},
		"ladder seal-in, init":    {ext: "ld", prog: sealLD, horn: withInit},
		"set coil reads its tag":  {ext: "ld", prog: "PROGRAM Main\nLD\n  RUNG s\n    Alarm ( S Horn )\n  RUNG r\n    Ack ( R Horn )\nEND_LD\nEND_PROGRAM\n", horn: noInit, faults: true},
		"written first, then read": {ext: "st", horn: noInit,
			prog: "PROGRAM Main\nVAR copy : BOOL; END_VAR\nHorn := Alarm AND NOT Ack;\ncopy := Horn;\nEND_PROGRAM\n"},
		"written on both branches": {ext: "st", horn: noInit,
			prog: "PROGRAM Main\nVAR copy : BOOL; END_VAR\nIF Alarm THEN Horn := TRUE; ELSE Horn := FALSE; END_IF;\ncopy := Horn;\nEND_PROGRAM\n"},
		"written on one branch only": {ext: "st", horn: noInit, faults: true,
			prog: "PROGRAM Main\nVAR copy : BOOL; END_VAR\nIF Alarm THEN Horn := TRUE; END_IF;\ncopy := Horn;\nEND_PROGRAM\n"},
		"only written": {ext: "st", horn: noInit, prog: "PROGRAM Main\nHorn := Alarm;\nEND_PROGRAM\n"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			files := map[string]string{
				"nautilus.yaml": "name: horn\ntasks:\n  - program: main." + c.ext + "\n    scan: 100ms\n" +
					"tags:\n" +
					"  - { name: Alarm, role: input,    type: BOOL, init: false }\n" +
					"  - { name: Ack,   role: setpoint, type: BOOL, init: false }\n" + c.horn +
					"driver:\n  type: memory\n",
				"main." + c.ext: c.prog,
			}
			for n, body := range files {
				if err := os.WriteFile(filepath.Join(dir, n), []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			out, code := capture(t, func() int { return runCheck([]string{dir}) })
			reported := strings.Contains(out, `output "Horn" has no init:`)
			if reported != c.faults {
				t.Errorf("reported = %v, want %v (exit %d):\n%s", reported, c.faults, code, out)
			}
			if c.faults {
				if code == 0 {
					t.Errorf("naut check passed a project whose scan faults:\n%s", out)
				}
				// On the tag's own manifest line, naming the program and the fix.
				if !strings.Contains(out, "nautilus.yaml:8:") || !strings.Contains(out, "Main reads it before writing it (a seal-in reads its own coil)") ||
					!strings.Contains(out, "init: false") {
					t.Errorf("the error does not point at Horn's line, name Main, or say init: false:\n%s", out)
				}
			} else if code != 0 {
				t.Errorf("naut check failed (exit %d):\n%s", code, out)
			}

			// The runtime agrees: one scan faults exactly when check says so.
			proj, err := project.Load(os.DirFS(dir), "")
			if err != nil {
				t.Fatal(err)
			}
			rt, err := runtime.New(proj.Runtime)
			if err != nil {
				t.Fatal(err)
			}
			rt.Scan()
			fault := rt.Stats().LastError
			if got := strings.Contains(fault, "undefined tag Horn"); got != c.faults {
				t.Errorf("the first scan's fault is %q; check said faults = %v", fault, c.faults)
			}
		})
	}
}

// Tasks start concurrently, so another task's write is no guarantee: a
// task that reads an output with no init:, which a different task writes,
// can run first. Reported, naming the reader.
func TestCheckReportsAnOutputAnotherTaskWrites(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"nautilus.yaml": "name: two\ntasks:\n  - program: writer.st\n    scan: 100ms\n" +
			"  - name: reader\n    program: reader.st\n    scan: 100ms\ntags:\n" +
			"  - { name: Alarm, role: input, type: BOOL, init: false }\n" +
			"  - { name: Horn,  role: output, type: BOOL }\n" +
			"  - { name: Lamp,  role: output, type: BOOL, init: false }\n" +
			"driver:\n  type: memory\n",
		"writer.st": "PROGRAM Writer\nHorn := Alarm;\nEND_PROGRAM\n",
		"reader.st": "PROGRAM Reader\nLamp := Horn;\nEND_PROGRAM\n",
	}
	for n, body := range files {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out, code := capture(t, func() int { return runCheck([]string{dir}) })
	if code == 0 || !strings.Contains(out, `output "Horn" has no init:, and Reader reads it before writing it (another task writes it`) {
		t.Errorf("want an error naming Reader (exit %d):\n%s", code, out)
	}
	if strings.Contains(out, `"Lamp"`) {
		t.Errorf("Lamp has an init: and is only written, but is reported:\n%s", out)
	}
}
