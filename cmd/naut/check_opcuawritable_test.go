package main

import (
	"strings"
	"testing"
)

// opcua.writable is checked against the tags it is meant to open: a
// malformed glob is an error; a pattern that matches nothing, one that
// matches only inputs, and an output a program writes are warnings. A
// pattern that opens a setpoint, or a UDT member, is quiet.
func TestCheckWarnsOnOPCUAWritable(t *testing.T) {
	files := func(writable string) map[string]string {
		return map[string]string{
			"nautilus.yaml": `
tasks:
  - program: program.st
opcua:
  writable: ` + writable + `
tags:
  - { name: Level, role: input, type: REAL }
  - { name: LevelSP, role: setpoint, init: 5.0 }
  - { name: Pump, role: output, init: false }
  - { name: P101, role: setpoint, type: Drive }
  - { name: Drives, role: setpoint, type: "ARRAY[0..1] OF Drive" }
  - { name: Trend, role: setpoint, init: [1.0, 2.0, 3.0] }
`,
			"types.st": `TYPE Drive : STRUCT Speed : REAL; Amps : REAL; END_STRUCT END_TYPE`,
			"program.st": `PROGRAM Main
VAR_EXTERNAL Level : REAL; LevelSP : REAL; Pump : BOOL; P101 : Drive; Drives : ARRAY[0..1] OF Drive; Trend : ARRAY[0..2] OF REAL; END_VAR
Pump := Level > LevelSP AND P101.Speed > 0.0;
END_PROGRAM`,
		}
	}

	// A pattern on an array of UDTs opens its elements' members.
	out, code := checkIn(t, files(`["LevelSP", "P101.Speed", "Drives"]`))
	if code != 0 || strings.Contains(out, "opcua.writable") {
		t.Errorf("a list that opens a setpoint, a member and an array of UDTs warned (%d):\n%s", code, out)
	}

	out, code = checkIn(t, files(`["LevelSp*", "Lvl*", "Lev?l", "Pump", "Trend"]`))
	if code != 0 {
		t.Fatalf("warnings must not fail the check (%d):\n%s", code, out)
	}
	for _, want := range []string{
		`warning: opcua.writable: "Lvl*" matches no tag or member`,
		`warning: opcua.writable: "Lev?l" matches only inputs (Level)`,
		`warning: opcua.writable opens output "Pump", which a program writes`,
		`warning: opcua.writable: "Trend" opens only arrays or Objects with no scalar member (Trend)`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	// Globs match names in their declared casing, as sparkplug.writable's
	// do (path.Match is exact), so a glob in another casing opens nothing,
	// and check says so.
	if !strings.Contains(out, `"LevelSp*" matches no tag or member`) {
		t.Errorf("a case-mismatched glob was not reported:\n%s", out)
	}

	out, code = checkIn(t, files(`["["]`))
	if code == 0 || !strings.Contains(out, `opcua.writable pattern "["`) {
		t.Errorf("a malformed glob passed (%d):\n%s", code, out)
	}
}
