package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/eip/logix"
	"github.com/joyautomation/nautilus/internal/project"
	"github.com/joyautomation/nautilus/runtime"
)

func l5xFixture(name string) string {
	return filepath.Join("..", "..", "lang", "l5x", "testdata", name)
}

// emulateL5X starts the emulator in-process on a free port, the way
// `naut logix emulate --l5x path --listen 127.0.0.1:0` would.
func emulateL5X(t *testing.T, path string, values map[string]any, ramp bool) *logixEmulator {
	t.Helper()
	surf, err := loadLogixSurface(path, "", "")
	if err != nil {
		t.Fatalf("load %s: %v", path, err)
	}
	em, err := startLogixEmulate(surf, "127.0.0.1:0", values, ramp, io.Discard, io.Discard)
	if err != nil {
		t.Fatalf("emulate %s: %v", path, err)
	}
	t.Cleanup(em.Stop)
	return em
}

func hostPort(t *testing.T, em *logixEmulator) (string, string) {
	t.Helper()
	i := strings.LastIndexByte(em.Addr, ':')
	return em.Addr[:i], em.Addr[i+1:]
}

// captureRun runs fn with os.Stdout redirected, returning what it
// printed and its exit code.
func captureRun(t *testing.T, fn func() int) (string, int) {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	code := fn()
	w.Close()
	os.Stdout = old
	out := <-done
	r.Close()
	return out, code
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// The acceptance path: an L5X in, `naut eip browse` and `naut eip import`
// against the emulator out — no PLC anywhere.
func TestLogixEmulateBrowseAndImport(t *testing.T) {
	em := emulateL5X(t, l5xFixture("DemoProgram.L5X"), nil, false)
	host, port := hostPort(t, em)

	out, code := captureRun(t, func() int {
		return runEIP([]string{"browse", "--host", host, "--port", port})
	})
	if code != 0 {
		t.Fatalf("browse failed (%d):\n%s", code, out)
	}
	for _, tag := range []string{"StartPB", "StopPB", "RunCmd", "HiLevelAlm", "LevelPct", "HiLevelSP"} {
		if !strings.Contains(out, "Program:MainProgram."+tag) {
			t.Errorf("browse is missing Program:MainProgram.%s:\n%s", tag, out)
		}
	}
	if !strings.Contains(out, "REAL") || !strings.Contains(out, "BOOL") {
		t.Errorf("browse lost the types:\n%s", out)
	}

	dir := t.TempDir()
	if _, code := captureRun(t, func() int {
		return runEIP([]string{"import", "--host", host, "--port", port, "--format", "yaml", "--out", dir})
	}); code != 0 {
		t.Fatalf("import failed (%d)", code)
	}
	for _, f := range []string{"eip_types.st", "eip_manifest.yaml", filepath.Join("tags", "eip.yaml")} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("%s not written: %v", f, err)
		}
	}
	manifest, _ := os.ReadFile(filepath.Join(dir, "eip_manifest.yaml"))
	if !strings.Contains(string(manifest), "device: Program:MainProgram.LevelPct") {
		t.Errorf("manifest does not bind the program tag:\n%s", manifest)
	}
}

// TestLogixEmulateEIPImportRenamesKeywordMember reproduces the eip codegen
// bug this fixture exists for: variety.L5X's Analog_Input UDT has a member
// named "retain", an IEC keyword. `naut logix import` (the L5X path) already
// renames it; `naut eip import` (browsed from the live emulator, same as a
// real controller) used to emit it unrenamed and fail its own compile-check
// ("generated ST is invalid"). It must now import cleanly, escaping the
// member the same way, while the manifest still addresses the controller by
// its real member name.
func TestLogixEmulateEIPImportRenamesKeywordMember(t *testing.T) {
	em := emulateL5X(t, l5xFixture("variety.L5X"), nil, false)
	host, port := hostPort(t, em)

	dir := t.TempDir()
	out, code := captureRun(t, func() int {
		return runEIP([]string{"import", "--host", host, "--port", port, "--format", "yaml", "--out", dir})
	})
	if code != 0 {
		t.Fatalf("import failed (%d):\n%s", code, out)
	}

	types, err := os.ReadFile(filepath.Join(dir, "eip_types.st"))
	if err != nil {
		t.Fatalf("eip_types.st not written: %v", err)
	}
	if !strings.Contains(string(types), "retain_ : BOOL;") {
		t.Errorf("keyword member not renamed in eip_types.st:\n%s", types)
	}

	manifest, err := os.ReadFile(filepath.Join(dir, "eip_manifest.yaml"))
	if err != nil {
		t.Fatalf("eip_manifest.yaml not written: %v", err)
	}
	if !strings.Contains(string(manifest), "name: retain_") {
		t.Errorf("manifest does not use the escaped identifier:\n%s", manifest)
	}
	if !strings.Contains(string(manifest), "device: retain") {
		t.Errorf("manifest lost the controller's real member name:\n%s", manifest)
	}
}

// The richest fixture: nested UDTs, BIT overlays, an array member, a STRING,
// a predefined TIMER, an elementary array, a program tag — all served with
// the export's initial values, and what cannot be served said out loud.
func TestLogixEmulateVarietyValues(t *testing.T) {
	surf, err := loadLogixSurface(l5xFixture("variety.L5X"), "", "")
	if err != nil {
		t.Fatal(err)
	}
	skipped := strings.Join(surf.skipped, "\n")
	for _, want := range []string{"Analog_Input.Axis", "LocalStart: alias", "Handle: type MESSAGE"} {
		if !strings.Contains(skipped, want) {
			t.Errorf("skipped list is missing %q:\n%s", want, skipped)
		}
	}
	em := emulateL5X(t, l5xFixture("variety.L5X"), map[string]any{"Trend": []any{1.5, 2.5}}, false)
	host, port := hostPort(t, em)
	p, _ := strconv.Atoi(port)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ctrl, err := logix.Dial(ctx, host, logix.WithPort(p))
	if err != nil {
		t.Fatal(err)
	}
	defer ctrl.Close()
	br, err := ctrl.Browse(ctx)
	if err != nil {
		t.Fatal(err)
	}
	reg := logix.NewRegistry(br.Templates)
	read := func(tag string, count uint16) logix.Value {
		t.Helper()
		raw, err := ctrl.ReadTag(ctx, tag, count)
		if err != nil {
			t.Fatalf("read %s: %v", tag, err)
		}
		v, err := reg.Decode(raw)
		if err != nil {
			t.Fatalf("decode %s: %v", tag, err)
		}
		return v
	}
	field := func(v logix.Value, name string) logix.Value {
		t.Helper()
		for _, f := range v.Fields {
			if f.Name == name {
				return f.Value
			}
		}
		t.Fatalf("%s has no member %s: %+v", v.Type, name, v)
		return logix.Value{}
	}

	if v := read("RunHours", 1); v.Scalar != int64(1274) && v.Scalar != int32(1274) {
		t.Errorf("RunHours = %#v, want the export's 1274", v.Scalar)
	}
	lvl := read("P101_Level", 1)
	if hi := field(field(lvl, "Alarms"), "Hi"); hi.Scalar != float32(85) && hi.Scalar != float64(85) {
		t.Errorf("P101_Level.Alarms.Hi = %#v, want 85", hi.Scalar)
	}
	if lbl := field(lvl, "Label"); lbl.Scalar != "Wet Well" {
		t.Errorf("P101_Level.Label = %#v, want \"Wet Well\"", lbl.Scalar)
	}
	if h := field(lvl, "History"); len(h.Elems) != 10 {
		t.Errorf("P101_Level.History has %d elements, want 10", len(h.Elems))
	}
	tmr := read("DwellTmr", 1)
	if pre := field(tmr, "PRE"); pre.Scalar != int64(30000) && pre.Scalar != int32(30000) {
		t.Errorf("DwellTmr.PRE = %#v, want 30000", pre.Scalar)
	}
	trend := read("Trend", 4)
	if len(trend.Elems) != 4 || trend.Elems[1].Scalar != float32(2.5) && trend.Elems[1].Scalar != float64(2.5) {
		t.Errorf("Trend = %+v, want 4 elements with [1] = 2.5 from --values", trend)
	}
	if v := read("Program:MainProgram.Scratch", 1); v.Type != "DINT" {
		t.Errorf("Program:MainProgram.Scratch = %+v", v)
	}
}

// The driver round trip: import from the emulator into a manifest project,
// load it the way `naut run` does, and watch an input arrive and an output
// land back in the emulator.
func TestLogixEmulateDriverRoundTrip(t *testing.T) {
	em := emulateL5X(t, filepath.Join("testdata", "logix", "upstream.L5X"), nil, false)
	host, port := hostPort(t, em)

	dir := t.TempDir()
	if _, code := captureRun(t, func() int {
		return runEIP([]string{"import", "--host", host, "--port", port,
			"--format", "yaml", "--out", dir, "--writable", "Batch_Ack"})
	}); code != 0 {
		t.Fatalf("import failed (%d)", code)
	}
	files := map[string]string{
		"nautilus.yaml": `
tasks:
  - program: program.st
driver:
  type: eip
  host: ` + em.Addr + `
  manifest: eip_manifest.yaml
  scan-rate: 50ms
tag-files: [tags/eip.yaml]
`,
		"program.st": `PROGRAM Main
VAR_EXTERNAL
  Line_Rate : REAL;
  Handshake : BatchHandshake;
  Batch_Ack : DINT;
  MainProgram_Step : DINT;
END_VAR
IF Handshake.Ready THEN
  Batch_Ack := Handshake.BatchID + MainProgram_Step;
END_IF;
END_PROGRAM
`,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	proj, err := project.Load(os.DirFS(dir), "")
	if err != nil {
		t.Fatalf("load project: %v", err)
	}
	rt, err := runtime.New(proj.Runtime)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	drv := proj.Runtime.Driver
	starter, ok := drv.(interface {
		Start(context.Context)
		Stop()
	})
	if !ok {
		t.Fatalf("driver %T has no Start/Stop", drv)
	}
	starter.Start(context.Background())
	t.Cleanup(starter.Stop)
	waitUntil(t, "first poll", func() bool {
		v, err := drv.ReadInputs()
		return err == nil && v["Handshake"] != nil && v["Line_Rate"] != nil
	})

	rt.Scan()
	all := rt.Tags().All()
	if all["Line_Rate"] != 12.5 {
		t.Errorf("Line_Rate = %#v, want the export's 12.5", all["Line_Rate"])
	}
	// BatchID 4101 + Step 3, written back on change.
	waitUntil(t, "Batch_Ack on the emulator", func() bool {
		_, v, _ := em.Store.Resolve("Batch_Ack")
		return v == 4104.0
	})

	// A value the emulator changes arrives on the next poll.
	em.Store.UpdateValue("Handshake.BatchID", 5000.0)
	waitUntil(t, "BatchID repoll", func() bool {
		rt.Scan()
		_, v, _ := em.Store.Resolve("Batch_Ack")
		return v == 5003.0
	})
}

func TestLogixEmulateUsage(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte(`{"NoSuchTag": 1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		args []string
		code int
	}{
		{nil, 2},
		{[]string{"--l5x", "a.L5X", "--surface", "b.json"}, 2},
		{[]string{"--l5x", l5xFixture("DemoProgram.L5X"), "--listen", "nonsense"}, 2},
		{[]string{"--l5x", filepath.Join(dir, "missing.L5X")}, 1},
		{[]string{"--surface", filepath.Join(dir, "missing.json")}, 1},
		{[]string{"--l5x", l5xFixture("DemoProgram.L5X"), "--listen", "127.0.0.1:0", "--values", bad}, 1},
	}
	for _, c := range cases {
		if got := runLogixEmulate(c.args); got != c.code {
			t.Errorf("emulate %v = %d, want %d", c.args, got, c.code)
		}
	}
}

// --ramp moves numeric leaves, and stops moving one a client wrote.
func TestLogixEmulateRamp(t *testing.T) {
	em := emulateL5X(t, filepath.Join("testdata", "logix", "upstream.L5X"), nil, true)
	waitUntil(t, "Line_Rate to drift", func() bool {
		_, v, _ := em.Store.Resolve("Line_Rate")
		return v != 12.5
	})
	// Simulate a client write: the ramp must leave it alone from here on.
	em.Store.UpdateValue("Batch_Ack", 77.0)
	time.Sleep(1200 * time.Millisecond)
	if _, v, _ := em.Store.Resolve("Batch_Ack"); v != 77.0 {
		t.Errorf("ramp overwrote a client write: Batch_Ack = %v", v)
	}
	if _, v, _ := em.Store.Resolve("Line_Running"); v != true {
		t.Errorf("ramp touched a BOOL: %v", v)
	}
}
