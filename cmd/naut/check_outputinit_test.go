package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/internal/project"
	"github.com/joyautomation/nautilus/runtime"
)

// checkAndScan writes a project, runs naut check on it, then runs one real
// scan and returns the check output, its exit code, and the scan's fault.
func checkAndScan(t *testing.T, files map[string]string) (out string, code int, fault string) {
	t.Helper()
	dir := t.TempDir()
	for n, body := range files {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out, code = capture(t, func() int { return runCheck([]string{dir}) })
	proj, err := project.Load(os.DirFS(dir), "")
	if err != nil {
		t.Fatal(err)
	}
	rt, err := runtime.New(proj.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	rt.Scan()
	return out, code, rt.Stats().LastError
}

// An output with no init: that a program reads before writing it faults
// the scan with "undefined tag" — a seal-in reads its own coil — and naut
// check used to pass it (nautilus#560). Now it is an error on the tag's
// manifest line, and each case below is also run for one scan, so the
// check and the runtime are held to the same answer: an error exactly when
// the scan faults, nothing when it does not. A warning is the analysis
// saying it cannot tell (a loop of unknown count); those cases pin what
// the scan really does too.
func TestCheckReportsAnOutputReadBeforeItsFirstWrite(t *testing.T) {
	type tc struct {
		ext, prog string
		horn      string // the Horn tag line
		want      string // "error", "warning" or "" (nothing reported about Horn)
		faults    bool   // the first scan faults with undefined tag Horn
		cause     string // the error's stated reason, when want is "error"
	}
	const noInit = "  - { name: Horn,  role: output,   type: BOOL }\n"
	const withInit = "  - { name: Horn,  role: output,   type: BOOL, init: false }\n"
	sealLD := "PROGRAM Main\nLD\n  RUNG horn (* seals in until acknowledged *)\n    [ Alarm | Horn ] /Ack ( Horn )\nEND_LD\nEND_PROGRAM\n"
	// Siren writes Horn through its own VAR_EXTERNAL.
	const siren = "FUNCTION_BLOCK Siren\nVAR_INPUT On : BOOL; END_VAR\nVAR_EXTERNAL Horn : BOOL; END_VAR\nHorn := On;\nEND_FUNCTION_BLOCK\n\n"
	cases := map[string]tc{
		"ladder seal-in, no init": {ext: "ld", prog: sealLD, horn: noInit, want: "error", faults: true,
			cause: "Main reads it before writing it (a seal-in or set/reset coil reads its own coil)"},
		"ladder seal-in, init": {ext: "ld", prog: sealLD, horn: withInit},
		"set coil reads its tag": {ext: "ld", horn: noInit, want: "error", faults: true,
			prog:  "PROGRAM Main\nLD\n  RUNG s\n    Alarm ( S Horn )\n  RUNG r\n    Ack ( R Horn )\nEND_LD\nEND_PROGRAM\n",
			cause: "Main reads it before writing it (a seal-in or set/reset coil reads its own coil)"},
		"ST self-assignment": {ext: "st", horn: noInit, want: "error", faults: true,
			prog:  "PROGRAM Main\nHorn := Horn OR Alarm;\nEND_PROGRAM\n",
			cause: "Main reads it before writing it (the assignment that writes it reads it first)"},
		"written first, then read": {ext: "st", horn: noInit,
			prog: "PROGRAM Main\nVAR copy : BOOL; END_VAR\nHorn := Alarm AND NOT Ack;\ncopy := Horn;\nEND_PROGRAM\n"},
		"written on both branches": {ext: "st", horn: noInit,
			prog: "PROGRAM Main\nVAR copy : BOOL; END_VAR\nIF Alarm THEN Horn := TRUE; ELSE Horn := FALSE; END_IF;\ncopy := Horn;\nEND_PROGRAM\n"},
		"written on one branch only": {ext: "st", horn: noInit, want: "error", faults: true,
			prog:  "PROGRAM Main\nVAR copy : BOOL; END_VAR\nIF Alarm THEN Horn := TRUE; END_IF;\ncopy := Horn;\nEND_PROGRAM\n",
			cause: "Main reads it before writing it (it is written only on some paths before this read)"},
		"read before a later write": {ext: "st", horn: noInit, want: "error", faults: true,
			prog:  "PROGRAM Main\nVAR copy : BOOL; END_VAR\ncopy := Horn;\nHorn := Alarm;\nEND_PROGRAM\n",
			cause: "Main reads it before writing it (the read comes before the first write)"},
		"only written": {ext: "st", horn: noInit, prog: "PROGRAM Main\nHorn := Alarm;\nEND_PROGRAM\n"},

		// The shapes the first version reported but the scan never faults on.
		"an FB writes it through VAR_EXTERNAL, read after the call": {ext: "st", horn: noInit,
			prog: siren + "PROGRAM Main\nVAR s : Siren; copy : BOOL; END_VAR\ns(On := Alarm);\ncopy := Horn;\nEND_PROGRAM\n"},
		"an FB body writes it, then reads it": {ext: "st", horn: noInit,
			prog: "FUNCTION_BLOCK Siren\nVAR_EXTERNAL Horn : BOOL; Alarm : BOOL; Lamp : BOOL; END_VAR\nHorn := Alarm;\nLamp := Horn;\nEND_FUNCTION_BLOCK\n\n" +
				"PROGRAM Main\nVAR s : Siren; END_VAR\ns();\nEND_PROGRAM\n"},
		"REPEAT writes it, read after the loop": {ext: "st", horn: noInit,
			prog: "PROGRAM Main\nVAR copy : BOOL; END_VAR\nREPEAT\n  Horn := Alarm;\nUNTIL TRUE END_REPEAT;\ncopy := Horn;\nEND_PROGRAM\n"},
		"FOR 1 TO 3 writes it, read after the loop": {ext: "st", horn: noInit,
			prog: "PROGRAM Main\nVAR copy : BOOL; i : INT; END_VAR\nFOR i := 1 TO 3 DO\n  Horn := Alarm;\nEND_FOR;\ncopy := Horn;\nEND_PROGRAM\n"},
		"CASE names every enum member, no ELSE": {ext: "st", horn: noInit,
			prog: "TYPE Mode : (Idle, Run); END_TYPE\n\nPROGRAM Main\nVAR m : Mode; copy : BOOL; END_VAR\nCASE m OF\n  Idle: Horn := FALSE;\n  Run: Horn := TRUE;\nEND_CASE;\ncopy := Horn;\nEND_PROGRAM\n"},

		// The other way round: a FUNCTION's VAR_EXTERNAL read is the
		// program's read, and the first version did not follow the call.
		"a FUNCTION reads it before the program writes it": {ext: "st", horn: noInit, want: "error", faults: true,
			prog: "FUNCTION Peek : BOOL\nVAR_INPUT x : BOOL; END_VAR\nVAR_EXTERNAL Horn : BOOL; END_VAR\nPeek := Horn AND x;\nEND_FUNCTION\n\n" +
				"PROGRAM Main\nVAR copy : BOOL; END_VAR\ncopy := Peek(Alarm);\nHorn := Alarm;\nEND_PROGRAM\n",
			cause: "Main reads it before writing it (the read comes before the first write)"},
		"an FB reads it before the program writes it": {ext: "st", horn: noInit, want: "error", faults: true,
			prog: "FUNCTION_BLOCK Echo\nVAR_EXTERNAL Horn : BOOL; Lamp : BOOL; END_VAR\nLamp := Horn;\nEND_FUNCTION_BLOCK\n\n" +
				"PROGRAM Main\nVAR e : Echo; END_VAR\ne();\nHorn := Alarm;\nEND_PROGRAM\n",
			cause: "Main reads it before writing it (the read comes before the first write)"},

		// Loops whose count check cannot know: a warning. Both of these
		// pin what the scan does.
		"WHILE writes it, read after (runs here)": {ext: "st", horn: noInit, want: "warning",
			prog: "PROGRAM Main\nVAR copy : BOOL; i : INT; END_VAR\ni := 0;\nWHILE i < 1 DO\n  Horn := Alarm;\n  i := i + 1;\nEND_WHILE;\ncopy := Horn;\nEND_PROGRAM\n"},
		"FOR 1 TO N writes it, read after (N = 0 here)": {ext: "st", horn: noInit, want: "warning", faults: true,
			prog: "PROGRAM Main\nVAR copy : BOOL; i : INT; END_VAR\nFOR i := 1 TO N DO\n  Horn := Alarm;\nEND_FOR;\ncopy := Horn;\nEND_PROGRAM\n"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			out, code, fault := checkAndScan(t, map[string]string{
				"nautilus.yaml": "name: horn\ntasks:\n  - program: main." + c.ext + "\n    scan: 100ms\n" +
					"tags:\n" +
					"  - { name: Alarm, role: input,    type: BOOL, init: false }\n" +
					"  - { name: Ack,   role: setpoint, type: BOOL, init: false }\n" + c.horn +
					"  - { name: Lamp,  role: output,   type: BOOL, init: false }\n" +
					"  - { name: N,     role: setpoint, type: INT,  init: 0 }\n" +
					"driver:\n  type: memory\n",
				"main." + c.ext: c.prog,
			})
			isErr := strings.Contains(out, `error: output "Horn" has no init:`)
			isWarn := strings.Contains(out, `warning: output "Horn" has no init:`)
			if isErr != (c.want == "error") || isWarn != (c.want == "warning") {
				t.Errorf("error = %v, warning = %v; want %q (exit %d):\n%s", isErr, isWarn, c.want, code, out)
			}
			if (code != 0) != (c.want == "error") {
				t.Errorf("exit %d, want an error: %v:\n%s", code, c.want == "error", out)
			}
			if c.want == "error" {
				// On the tag's own manifest line, naming the program, why, and the fix.
				if !strings.Contains(out, "nautilus.yaml:8:") || !strings.Contains(out, c.cause) ||
					!strings.Contains(out, "(init: false)") {
					t.Errorf("the error does not point at Horn's line, say %q, or say init: false:\n%s", c.cause, out)
				}
			}
			if c.want == "warning" && !strings.Contains(out, "Main may read it before writing it (it is written first only inside a loop that may run zero times)") {
				t.Errorf("the warning does not name the loop:\n%s", out)
			}

			// The runtime agrees: one scan faults exactly when check errs (or,
			// for a warning, as this case says it does).
			if got := strings.Contains(fault, "undefined tag Horn"); got != c.faults {
				t.Errorf("the first scan's fault is %q; want faults = %v", fault, c.faults)
			}
			if c.want == "error" && !c.faults || c.want == "" && c.faults {
				t.Fatalf("case is inconsistent: want %q, faults %v", c.want, c.faults)
			}
		})
	}
}

// Tasks start concurrently, so another task's write is no guarantee: a
// task that reads an output with no init:, which a different task writes,
// can run first. Reported, naming the reader — and saying so, not that a
// seal-in reads its own coil.
func TestCheckReportsAnOutputAnotherTaskWrites(t *testing.T) {
	out, code, _ := checkAndScan(t, map[string]string{
		"nautilus.yaml": "name: two\ntasks:\n  - program: writer.st\n    scan: 100ms\n" +
			"  - name: reader\n    program: reader.st\n    scan: 100ms\ntags:\n" +
			"  - { name: Alarm, role: input, type: BOOL, init: false }\n" +
			"  - { name: Horn,  role: output, type: BOOL }\n" +
			"  - { name: Lamp,  role: output, type: BOOL, init: false }\n" +
			"driver:\n  type: memory\n",
		"writer.st": "PROGRAM Writer\nHorn := Alarm;\nEND_PROGRAM\n",
		"reader.st": "PROGRAM Reader\nLamp := Horn;\nEND_PROGRAM\n",
	})
	if code == 0 || !strings.Contains(out, `output "Horn" has no init:, and Reader reads it, and only another task writes it (tasks start together`) {
		t.Errorf("want an error naming Reader (exit %d):\n%s", code, out)
	}
	if strings.Contains(out, "seal-in") {
		t.Errorf("no seal-in here:\n%s", out)
	}
	if strings.Contains(out, `"Lamp"`) {
		t.Errorf("Lamp has an init: and is only written, but is reported:\n%s", out)
	}
}

// An output no program writes at all is not "another task's" — the error
// says nothing writes it, and the scan faults on it.
func TestCheckReportsAnOutputNothingWrites(t *testing.T) {
	out, code, fault := checkAndScan(t, map[string]string{
		"nautilus.yaml": "name: none\ntasks:\n  - program: main.st\n    scan: 100ms\ntags:\n" +
			"  - { name: Horn,  role: output, type: BOOL }\n" +
			"  - { name: Lamp,  role: output, type: BOOL, init: false }\n" +
			"driver:\n  type: memory\n",
		"main.st": "PROGRAM Main\nLamp := Horn;\nEND_PROGRAM\n",
	})
	if code == 0 || !strings.Contains(out, `output "Horn" has no init:, and Main reads it, and no program writes it`) {
		t.Errorf("want an error saying no program writes Horn (exit %d):\n%s", code, out)
	}
	if strings.Contains(out, "another task") {
		t.Errorf("no other task writes Horn:\n%s", out)
	}
	if !strings.Contains(fault, "undefined tag Horn") {
		t.Errorf("the scan's fault is %q, want undefined tag Horn", fault)
	}
}

// A tag composed from a tag file is reported at its line in that file, not
// at the manifest with no line.
func TestCheckPlacesATagFileTagAtItsLine(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "tags"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"nautilus.yaml": "name: field\ntasks:\n  - program: main.st\n    scan: 100ms\ntag-files: [tags/io.yaml]\n" +
			"tags:\n  - { name: Lamp, role: output, type: BOOL, init: false }\ndriver:\n  type: memory\n",
		"tags/io.yaml": "# generated\n- { name: Alarm, role: input, type: BOOL }\n- { name: Horn, role: output, type: BOOL }\n",
		"main.st":      "PROGRAM Main\nHorn := Horn OR Alarm;\nLamp := Horn;\nEND_PROGRAM\n",
	}
	for n, body := range files {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out, code := capture(t, func() int { return runCheck([]string{dir}) })
	want := filepath.Join(dir, "tags", "io.yaml") + `:3:11: error: output "Horn" has no init:`
	if code == 0 || !strings.Contains(out, want) {
		t.Errorf("want %q (exit %d):\n%s", want, code, out)
	}
}
