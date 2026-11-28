package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/internal/project"
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
