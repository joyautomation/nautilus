package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/internal/project"
	"github.com/joyautomation/nautilus/internal/stproject"
	"github.com/joyautomation/nautilus/lang/st"
)

const checkPumpTypes = `TYPE Pump : STRUCT
  Running  : BOOL;
  SpeedSP  : REAL := 50.0;
  Start    : BOOL;
END_STRUCT; END_TYPE
`

const checkPumpWiring = `wirings:
  - type: Pump
    params: { endpoint: null }
    sources:
      drive: { driver: modbus, endpoint: "{endpoint}" }
    members:
      Running: { from: drive, table: coil, address: 0 }
      Start:   { to: drive, table: coil, address: 10 }
instances:
  - { name: P101, type: Pump, params: { endpoint: 192.0.2.1 } }
`

// A wired instance is refused for a Logix or Siemens target, as our gap,
// with the issue that closes it (design §14): the writer would write the
// TYPE, and nothing on the controller would fill it. The same project checks
// clean on nautilus.
func TestCheckRefusesWiredInstancesOnAVendorTarget(t *testing.T) {
	files := func(manifest string) map[string]string {
		return map[string]string{
			"nautilus.yaml": manifest + checkPumpWiring,
			"main.st":       "PROGRAM main\nVAR r : BOOL; END_VAR\nr := P101.Running;\nEND_PROGRAM\n",
			"types.st":      checkPumpTypes,
		}
	}
	plain := "name: d\ntasks:\n  - program: main.st\n    scan: 100ms\n"
	if out, code := checkIn(t, files(plain)); code != 0 {
		t.Fatalf("on nautilus: exit %d:\n%s", code, out)
	}
	for name, manifest := range map[string]string{
		"Siemens": goodSiemensManifest,
		"Logix":   plain + "target:\n  logix: {}\n",
	} {
		out, code := checkIn(t, files(manifest))
		want := "instance P101 is wired (source drive, Modbus); the " + name + " writer does not carry wiring yet (#1478)"
		if code != 1 || !strings.Contains(out, want) {
			t.Errorf("%s: exit %d, want %q:\n%s", name, code, want, out)
		}
	}
}

// Memory members are not retained yet: check says which, the implicit ones
// (left out of the wiring) included, so a forgotten entry is seen.
func TestCheckWarnsMemoryIsNotRetainedYet(t *testing.T) {
	out, _ := checkIn(t, map[string]string{
		"nautilus.yaml": "name: d\ntasks:\n  - program: main.st\n    scan: 100ms\n" + checkPumpWiring,
		"main.st":       "PROGRAM main\nEND_PROGRAM\n",
		"types.st":      checkPumpTypes,
	})
	if !strings.Contains(out, "instances P101 (SpeedSP): these members are memory") || !strings.Contains(out, "does not retain them yet (slice 6") {
		t.Errorf("no warning naming P101.SpeedSP:\n%s", out)
	}
}

// The deploy and write paths refuse a wired instance too, not only check:
// each hands the manifest to its writer through one shared input, and that
// input refuses it, so check and deploy agree.
func TestWriterInputsRefuseWiredInstances(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"nautilus.yaml": "name: d\ntasks:\n  - program: main.st\n    scan: 100ms\n" + checkPumpWiring,
		"main.st":       "PROGRAM main\nEND_PROGRAM\n",
		"types.st":      checkPumpTypes,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := siemensWriterInput(dir, ""); err == nil || !strings.Contains(err.Error(), "the Siemens writer does not carry wiring yet (#1478)") {
		t.Errorf("the Siemens writer's input: %v", err)
	}
	m, err := project.ReadManifest(os.DirFS(dir), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := logixTagTypes(m); err == nil || !strings.Contains(err.Error(), "the Logix writer does not carry wiring yet (#1478)") {
		t.Errorf("the Logix writer's tags: %v", err)
	}
}

const checkWriteManifest = "name: d\ntasks:\n  - program: %s\n    scan: 100ms\n"

func checkWriteFiles(program, body string, extra map[string]string) map[string]string {
	files := map[string]string{
		"nautilus.yaml": strings.Replace(checkWriteManifest, "%s", program, 1) + checkPumpWiring,
		program:         body,
		"types.st":      checkPumpTypes,
	}
	for k, v := range extra {
		files[k] = v
	}
	return files
}

// A program that assigns a from: member is refused by check, on the line,
// naming the member, its kind and the fix; commands and memory pass. ST,
// FBD and LD all reach the same check, each reporting on its own source's
// line (#1458 slice 7).
func TestCheckRefusesAssigningAReadOnlyMember(t *testing.T) {
	want := "P101.Running is read from the device (from:), so the program cannot assign it; remove the assignment, or wire the member as memory:"
	for _, c := range []struct{ name, file, body, at string }{
		{"ST", "main.st", "PROGRAM main\nVAR r : BOOL; END_VAR\nr := TRUE;\nP101.Running := r;\nEND_PROGRAM\n", "main.st:4:"},
		{"FBD", "main.fbd", "PROGRAM main\nVAR\n  r : BOOL;\nEND_VAR\nFBD\n  NETWORK 'x'\n  P101.Running := AND(r, TRUE)\nEND_FBD\nEND_PROGRAM\n", "main.fbd:7:"},
		{"LD", "main.ld", "PROGRAM main\nVAR\n  r : BOOL;\nEND_VAR\nLD\n  RUNG one\n    r ( P101.Running )\nEND_LD\nEND_PROGRAM\n", "main.ld:6:"},
	} {
		files := checkWriteFiles(c.file, c.body, nil)
		out, code := checkIn(t, files)
		if code != 1 || !strings.Contains(out, c.at) || !strings.Contains(out, "error: "+want) {
			t.Errorf("%s: exit %d, want %s ... error: %s:\n%s", c.name, code, c.at, want, out)
		}
	}
	// A command and a memory member are the program's to set.
	body := "PROGRAM main\nP101.Start := TRUE;\nP101.SpeedSP := 40.0;\nEND_PROGRAM\n"
	if out, code := checkIn(t, checkWriteFiles("main.st", body, nil)); code != 0 {
		t.Errorf("commands and memory: exit %d:\n%s", code, out)
	}
}

// A library block that assigns a from: member is refused where the instance
// is passed to it, on the program's line, with the block's own file and
// line; the same block on a plain local struct passes.
func TestCheckRefusesPassingAnInstanceToABlockThatAssignsIt(t *testing.T) {
	lib := "FUNCTION_BLOCK Reset\n  VAR_IN_OUT P : Pump; END_VAR\n  VAR_INPUT X : BOOL; END_VAR\n  IF X THEN\n    P.Running := FALSE;\n  END_IF;\nEND_FUNCTION_BLOCK\n"
	main := "PROGRAM main\nVAR r : Reset; Spare : Pump; END_VAR\nr(P := Spare, X := TRUE);\nr(P := P101, X := TRUE);\nEND_PROGRAM\n"
	out, code := checkIn(t, checkWriteFiles("main.st", main, map[string]string{"lib/reset.st": lib}))
	for _, want := range []string{
		"main.st:4:",
		"calling Reset with P101 would assign P101.Running, which is read from the device (from:): Reset assigns P.Running (",
		"lib/reset.st:5)",
		"or pass a local variable of that type",
	} {
		if code != 1 || !strings.Contains(out, want) {
			t.Errorf("exit %d, missing %q:\n%s", code, want, out)
		}
	}
	if strings.Contains(out, "main.st:3:") {
		t.Errorf("the plain local struct was refused:\n%s", out)
	}
}

// The PumpCtl block of the struct-wiring guide (one-struct-model §2.4) takes
// an instance, assigns only commands and memory, and so checks clean; called
// with a plain local struct it checks clean too. What it reads of from:
// members is never a write.
func TestCheckAcceptsABlockThatTakesAnInstance(t *testing.T) {
	lib := "FUNCTION_BLOCK PumpCtl\n  VAR_IN_OUT P : Pump; END_VAR\n  VAR_INPUT Demand : BOOL; END_VAR\n" +
		"  P.Start := Demand AND NOT P.Running;\n  IF P.Running THEN\n    P.SpeedSP := P.SpeedSP + 0.1;\n  END_IF;\nEND_FUNCTION_BLOCK\n"
	main := "PROGRAM main\nVAR Ctl1, Ctl2 : PumpCtl; Spare : Pump; END_VAR\nCtl1(P := P101, Demand := TRUE);\nCtl2(P := Spare, Demand := TRUE);\nEND_PROGRAM\n"
	if out, code := checkIn(t, checkWriteFiles("main.st", main, map[string]string{"lib/pumpctl.st": lib})); code != 0 {
		t.Errorf("exit %d:\n%s", code, out)
	}
}

// A block declared in the program's own file is named by its line there.
func TestCheckNamesTheBlocksLineInTheSameFile(t *testing.T) {
	main := "FUNCTION_BLOCK Reset\n  VAR_IN_OUT P : Pump; END_VAR\n  P.Running := FALSE;\nEND_FUNCTION_BLOCK\n" +
		"PROGRAM main\nVAR r : Reset; END_VAR\nr(P := P101);\nEND_PROGRAM\n"
	out, code := checkIn(t, checkWriteFiles("main.st", main, nil))
	if code != 1 || !strings.Contains(out, "main.st:7:") || !strings.Contains(out, "Reset assigns P.Running (line 3)") {
		t.Errorf("exit %d:\n%s", code, out)
	}
}

// A library block that reaches an instance through VAR_EXTERNAL assigns it
// itself, so the refusal is on the library's own line. It is said once
// whichever files are named: the library's own check, or only the program
// that composes it (`naut check main.st`), which used to pass silently.
func TestCheckRefusesALibraryBlockAssigningAnExternalInstance(t *testing.T) {
	lib := "FUNCTION_BLOCK Reset\n  VAR_EXTERNAL P101 : Pump; END_VAR\n  P101.Start := TRUE;\n  P101.Running := FALSE;\nEND_FUNCTION_BLOCK\n"
	main := "PROGRAM main\nVAR r : Reset; END_VAR\nr();\nEND_PROGRAM\n"
	files := checkWriteFiles("main.st", main, map[string]string{"lib/reset.st": lib})
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
	want := "reset.st:4:3: error: P101.Running is read from the device (from:), so the program cannot assign it"
	for name, args := range map[string][]string{
		"the project":   {dir},
		"main.st alone": {filepath.Join(dir, "main.st")},
		"both files":    {filepath.Join(dir, "main.st"), filepath.Join(dir, "lib", "reset.st")},
	} {
		out, code := capture(t, func() int { return runCheck(args) })
		if code != 1 || strings.Count(out, want) != 1 || strings.Count(out, "error:") != 1 {
			t.Errorf("%s: exit %d, want %q once and no other error:\n%s", name, code, want, out)
		}
	}
}

// A refused write in the prelude lands on its library's line; one on a
// line no library claims is still said (on the program), never dropped.
func TestLibWriteErrPlacesOrFallsBack(t *testing.T) {
	w := st.InstanceWrite{Pos: st.Pos{Line: 3, Col: 3}, Instance: "P101", Member: "Running", Kind: "from"}
	same := func(n int) int { return n }
	res := checkResult{preludeLines: 5, segs: []stproject.Segment{{Path: "/p/lib/reset.st", StartLine: 1, Lines: 5}}}
	le, ok := libWriteErr(w, res, same)
	if !ok || le.path != "/p/lib/reset.st" || le.line != 3 || le.col != 3 || !strings.HasPrefix(le.msg, "error: P101.Running is read from the device") {
		t.Errorf("in a library: %+v %v", le, ok)
	}
	res.segs = nil
	le, ok = libWriteErr(w, res, same)
	if ok || le.msg == "" || !strings.HasPrefix(le.msg, "error: in project library files: P101.Running is read from the device") {
		t.Errorf("no library claims the line: %+v %v", le, ok)
	}
}

// An expr: member checks clean and runs (slice 8b took the load fence
// away), and a sibling that hides a project tag of its name is a warning
// naming both.
func TestCheckWarnsAnExprSiblingShadowsATag(t *testing.T) {
	wiring := strings.Replace(checkPumpWiring, "      Start:   { to: drive, table: coil, address: 10 }\n",
		"      Start:   { to: drive, table: coil, address: 10 }\n      Healthy: { expr: \"Running AND SpeedSP > 1.0\" }\n", 1)
	types := strings.Replace(checkPumpTypes, "  Start    : BOOL;\n", "  Start    : BOOL;\n  Healthy  : BOOL;\n", 1)
	out, code := checkIn(t, map[string]string{
		"nautilus.yaml": "name: d\ntasks:\n  - program: main.st\n    scan: 100ms\ntags:\n  - { name: Running, role: state, type: BOOL }\n" + wiring,
		"main.st":       "PROGRAM main\nVAR h : BOOL; END_VAR\nh := P101.Healthy;\nEND_PROGRAM\n",
		"types.st":      types,
	})
	if code != 0 || strings.Contains(out, "slice 8b") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if !strings.Contains(out, `warning: nautilus.yaml:`) || !strings.Contains(out, `instance P101: member "Healthy": expr: Running reads the member P101.Running, not the project tag Running`) {
		t.Errorf("no shadow warning:\n%s", out)
	}
}

// naut check prints what it has to say about an instance's OPC UA session
// (#1458 slice 9a-1): a session with no template is security None, and a
// node read by namespace index alone could be misread after a renumbering.
func TestCheckWarnsAboutAnOPCUASession(t *testing.T) {
	out, code := checkIn(t, map[string]string{
		"nautilus.yaml": "name: d\ntasks:\n  - program: main.st\n    scan: 100ms\n" + `wirings:
  - type: Pump
    sources:
      drive: { driver: opcua-client, endpoint: "opc.tcp://192.0.2.1:4840" }
    members:
      Running: { from: drive, node: "ns=2;s=P101/Running" }
      Start:   { to: drive, node: "ns=2;s=P101/Start", namespace: "urn:x:drives" }
instances:
  - { name: P101, type: Pump }
`,
		"main.st":  "PROGRAM main\nEND_PROGRAM\n",
		"types.st": checkPumpTypes,
	})
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	for _, want := range []string{
		"nautilus.yaml: warning: instance P101: source drive connects with security None, anonymous",
		"nautilus.yaml: warning: instance P101: member Running is read by namespace index 2 alone",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("naut check does not say %q:\n%s", want, out)
		}
	}
}

// naut test computes an instance's combined companion too (#1458 slice
// 9a-2): the runtime writes <Instance>__Online from its connections'
// companions after each delivery, so a suite that gives the companions
// sees the AND, with no driver of its own.
func TestNautTestComputesTheCombinedCompanion(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"nautilus.yaml": "name: d\ntasks:\n  - program: main.st\n    scan: 100ms\n" + `wirings:
  - type: Pump
    sources:
      x: { driver: modbus, endpoint: 192.0.2.1 }
      y: { driver: modbus, endpoint: 192.0.2.2 }
    members:
      Running: { from: x, table: coil, address: 0 }
      Start:   { to: y, table: coil, address: 10 }
instances:
  - { name: P101, type: Pump }
`,
		"main.st":  "PROGRAM main\nVAR_EXTERNAL P101__Online : BOOL; END_VAR\nVAR up : BOOL; END_VAR\nup := P101__Online;\nEND_PROGRAM\n",
		"types.st": checkPumpTypes,
		"main_test.yaml": `tests:
  - name: both connections answer
    given: { P101_x__Online: true, P101_y__Online: true }
    scans: 2
    expect: { P101__Online: true }
  - name: one connection down
    given: { P101_x__Online: true, P101_y__Online: false }
    scans: 2
    expect: { P101__Online: false }
`,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var code int
	out := captureStdout(t, func() int { code = runTest([]string{dir}); return code })
	if code != 0 {
		t.Fatalf("naut test exit %d:\n%s", code, out)
	}
}
