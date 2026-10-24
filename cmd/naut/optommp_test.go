package main

// `naut optommp import` against the in-process bench chassis: the files it
// writes load through the core's strict decoders, a re-run is
// byte-identical, and a project composed from them passes `naut check`.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/modbus"
	"github.com/joyautomation/nautilus/optommp"
	"github.com/joyautomation/nautilus/optommp/optommptest"
)

func TestOptommpImportMakesACheckableProject(t *testing.T) {
	port := optommptest.NewBench().Listen(t)
	dir := t.TempDir()
	args := []string{"import", "--host", "127.0.0.1", "--port", fmt.Sprint(port), "--out", dir,
		"--manifest-host", "127.0.0.1", "--latches", "--scratch-floats", "1"}
	out, code := capture(t, func() int { return runOptommp(args) })
	if code != 0 {
		t.Fatalf("import exited %d:\n%s", code, out)
	}
	if !strings.Contains(out, "GRV-EPIC-PR1") || !strings.Contains(out, "driver: {type: optommp") {
		t.Errorf("output:\n%s", out)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "optommp_manifest.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	m, err := optommp.ParseManifest(raw)
	if err != nil {
		t.Fatalf("generated manifest does not parse: %v\n%s", err, raw)
	}
	if m.Host != "127.0.0.1" || m.Port != port || len(m.Modules) != 4 {
		t.Errorf("manifest: host %s port %d modules %d", m.Host, m.Port, len(m.Modules))
	}
	tags, err := os.ReadFile(filepath.Join(dir, "tags", "optommp.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(tags), "EPIC_M0_DI00_OnLatch") || !strings.Contains(string(tags), "EPIC_SP_F0") {
		t.Errorf("tag file lacks the optional tags:\n%s", tags)
	}

	// A re-run over an unchanged chassis changes nothing.
	if _, code := capture(t, func() int { return runOptommp(args) }); code != 0 {
		t.Fatal("re-run failed")
	}
	again, _ := os.ReadFile(filepath.Join(dir, "optommp_manifest.yaml"))
	if string(again) != string(raw) {
		t.Error("re-run is not byte-identical")
	}

	// Composed into a project, it checks offline.
	project := `name: epic
driver:
  type: optommp
  manifest: optommp_manifest.yaml
  scan-rate: 20ms
tasks:
  - program: main.st
    scan: 100ms
tag-files:
  - tags/optommp.yaml
`
	prog := `PROGRAM Main
VAR_EXTERNAL EPIC_M0_DI03 : BOOL; EPIC_M1_DO00 : BOOL; EPIC_SP_F0 : REAL; EPIC_M2_AI00 : REAL; END_VAR
EPIC_M1_DO00 := EPIC_M0_DI03;
EPIC_SP_F0 := EPIC_M2_AI00;
END_PROGRAM
`
	os.WriteFile(filepath.Join(dir, "nautilus.yaml"), []byte(project), 0o644)
	os.WriteFile(filepath.Join(dir, "main.st"), []byte(prog), 0o644)
	if out, code := capture(t, func() int { return runCheck([]string{dir}) }); code != 0 {
		t.Fatalf("naut check on the imported project exited %d:\n%s", code, out)
	}
}

func TestOptommpImportModbus(t *testing.T) {
	port := optommptest.NewBench().Listen(t)
	dir := t.TempDir()
	out, code := capture(t, func() int {
		return runOptommp([]string{"import", "--modbus", "--host", "127.0.0.1", "--port", fmt.Sprint(port), "--out", dir})
	})
	if code != 0 {
		t.Fatalf("import --modbus exited %d:\n%s", code, out)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "modbus_manifest.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := modbus.ParseManifest(raw); err != nil {
		t.Fatalf("modbus manifest does not parse: %v", err)
	}
	if !strings.Contains(string(raw), "naut optommp import --modbus --host 127.0.0.1") {
		t.Errorf("header does not record --modbus:\n%s", raw[:200])
	}
	if _, err := os.Stat(filepath.Join(dir, "optommp_manifest.yaml")); err == nil {
		t.Error("--modbus also wrote an optommp manifest")
	}
}

func TestOptommpImportCommOptions(t *testing.T) {
	port := optommptest.NewBench().Listen(t)
	dir := t.TempDir()
	out, code := capture(t, func() int {
		return runOptommp([]string{"import", "--host", "127.0.0.1", "--port", fmt.Sprint(port), "--out", dir,
			"--stream", "20ms", "--watchdog", "0s"})
	})
	if code != 0 {
		t.Fatalf("import exited %d:\n%s", code, out)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "optommp_manifest.yaml"))
	m, err := optommp.ParseManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	if m.Stream == 0 || m.Watchdog == nil || *m.Watchdog != 0 || !strings.Contains(string(raw), "--stream 20ms --watchdog 0s") {
		t.Errorf("manifest:\n%s", raw[:300])
	}
	if _, code := capture(t, func() int {
		return runOptommp([]string{"import", "--host", "127.0.0.1", "--port", fmt.Sprint(port), "--out", dir, "--watchdog", "soon"})
	}); code == 0 {
		t.Error("a bad --watchdog was accepted")
	}
}
