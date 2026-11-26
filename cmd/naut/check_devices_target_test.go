package main

import (
	"os"
	"strings"
	"testing"
)

// A project with devices: rows is refused for a Logix or Siemens target,
// with the gap named: neither writer carries the device adapter, so the
// device's struct and its derived members would never be filled there.
// The same project checks clean on nautilus.
func TestCheckRefusesDevicesOnAVendorTarget(t *testing.T) {
	prof, err := os.ReadFile("../../devprofile/profiles/abb-acs355-fmbt21.yaml")
	if err != nil {
		t.Fatal(err)
	}
	sim, err := os.ReadFile("../../devprofile/profiles/abb-drives.st")
	if err != nil {
		t.Fatal(err)
	}
	devices := "devices:\n  - { name: CONV, profile: abb-acs355-fmbt21, endpoint: 10.0.0.9, points: [Tripped] }\n"
	files := func(manifest string) map[string]string {
		return map[string]string{
			"nautilus.yaml":                   manifest + devices,
			"main.st":                         "PROGRAM main\nVAR t : BOOL; END_VAR\nt := CONV.Tripped;\nEND_PROGRAM\n",
			"profiles/abb-acs355-fmbt21.yaml": string(prof),
			"profiles/abb-drives.st":          string(sim),
		}
	}
	plain := "name: d\ntasks:\n  - program: main.st\n    scan: 100ms\n"
	if out, code := checkIn(t, files(plain)); code != 0 {
		t.Fatalf("on nautilus: exit %d:\n%s", code, out)
	}
	for name, manifest := range map[string]string{
		"siemens": goodSiemensManifest,
		"logix":   plain + "target:\n  logix: {}\n",
	} {
		out, code := checkIn(t, files(manifest))
		if code != 1 || !strings.Contains(out, "devices: rows run on the nautilus runtime") || !strings.Contains(out, "the "+name+" writer does not carry them") {
			t.Errorf("%s: exit %d, want the refusal:\n%s", name, code, out)
		}
	}
}

// Devices without an alarms: section are not watched: check says so, and
// `alarms: {}` is enough to turn the devices' own alarms on.
func TestCheckWarnsOfDevicesWithoutAlarms(t *testing.T) {
	prof, _ := os.ReadFile("../../devprofile/profiles/abb-acs355-fmbt21.yaml")
	sim, _ := os.ReadFile("../../devprofile/profiles/abb-drives.st")
	files := func(alarms string) map[string]string {
		return map[string]string{
			"nautilus.yaml":                   "name: d\ntasks:\n  - program: main.st\n    scan: 100ms\ndevices:\n  - { name: CONV, profile: abb-acs355-fmbt21, endpoint: 10.0.0.9 }\n" + alarms,
			"main.st":                         "PROGRAM main\nVAR t : BOOL; END_VAR\nt := CONV.Tripped;\nEND_PROGRAM\n",
			"profiles/abb-acs355-fmbt21.yaml": string(prof),
			"profiles/abb-drives.st":          string(sim),
		}
	}
	if out, _ := checkIn(t, files("")); !strings.Contains(out, "no alarms: section") {
		t.Errorf("no warning for devices without alarms:\n%s", out)
	}
	if out, code := checkIn(t, files("alarms: {}\n")); code != 0 || strings.Contains(out, "no alarms: section") {
		t.Errorf("alarms: {} still warns (exit %d):\n%s", code, out)
	}
}
