package acceptance_test

import (
	"testing"
	"testing/fstest"

	"github.com/joyautomation/nautilus/acceptance"
	"github.com/joyautomation/nautilus/internal/project"
)

// A test's given: on an instance's from: member stands for a reading
// (#1458 slice 8b): an expr: member reading it is computed, and the program
// reads the result, with no device in the test.
func TestGivenOnAFromMemberFeedsAnExpression(t *testing.T) {
	fsys := fstest.MapFS{
		"nautilus.yaml": &fstest.MapFile{Data: []byte(`name: g
tasks: [{ program: main.st }]
tags:
  - { name: Seen, role: state, type: BOOL }
wirings:
  - type: Pump
    params: { endpoint: null }
    sources:
      drive: { driver: modbus, endpoint: "{endpoint}", unit-id: 1 }
    members:
      Running: { from: drive, table: coil, address: 0 }
      Fault:   { from: drive, table: coil, address: 1 }
      Healthy: { expr: "Running AND NOT Fault" }
instances:
  - { name: P101, type: Pump, params: { endpoint: 192.0.2.1 } }
`)},
		"types.st": &fstest.MapFile{Data: []byte("TYPE Pump : STRUCT\n  Running : BOOL;\n  Fault : BOOL;\n  Healthy : BOOL;\nEND_STRUCT; END_TYPE\n")},
		"main.st":  &fstest.MapFile{Data: []byte("PROGRAM Main\nSeen := P101.Healthy;\nEND_PROGRAM\n")},
		"pump_test.yaml": &fstest.MapFile{Data: []byte(`tests:
  - name: healthy while running without a fault
    steps:
      - given: { P101.Running: true }
        scans: 1
        expect: { P101.Healthy: true, Seen: true }
      - given: { P101.Fault: true }
        scans: 1
        expect: { P101.Healthy: false, Seen: false }
`)},
	}
	proj, err := project.Load(fsys, "")
	if err != nil {
		t.Fatal(err)
	}
	results, err := acceptance.RunDir(fsys, proj.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || !results[0].Passed {
		for _, r := range results {
			t.Errorf("FAIL %s\n%s", r.Name, acceptance.FormatFailure(r))
		}
	}
}
