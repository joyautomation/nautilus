package main

import (
	"strings"
	"testing"
)

// opcua.writable is checked against the tags it is meant to open: a
// malformed glob is an error; a pattern that matches nothing, one that
// matches only inputs, one that opens only arrays of scalars, and an
// output a program writes are warnings. A pattern that opens a setpoint, or
// a UDT member, is quiet.
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
  - { name: Counts, role: setpoint, type: "ARRAY[1..3] OF DINT" }
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

	out, code = checkIn(t, files(`["LevelSp*", "Lvl*", "Lev?l", "Pump", "Trend", "Counts"]`))
	if code != 0 {
		t.Fatalf("warnings must not fail the check (%d):\n%s", code, out)
	}
	if strings.Contains(out, "not writable until array writes land") {
		t.Errorf("an array of scalars is called not writable, though its elements are:\n%s", out)
	}
	for _, want := range []string{
		`warning: opcua.writable: "Lvl*" matches no tag or member`,
		`warning: opcua.writable: "Lev?l" matches only inputs (Level)`,
		`warning: opcua.writable opens output "Pump", which a program writes`,
		// An array of scalars is refused whole, but its elements are
		// writable one at a time by node id, from the declared lower bound.
		`warning: opcua.writable: "Trend" opens arrays (Trend): a client writes their elements one at a time, by node id (ns=1;s=Trend[0]); a whole-array or IndexRange write is refused until array writes land`,
		`warning: opcua.writable: "Counts" opens arrays (Counts): a client writes their elements one at a time, by node id (ns=1;s=Counts[1])`,
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

	// Element patterns: [n] is that index, [*] any. Naming an element of
	// an array of scalars, every element, or every element's member of an
	// array of UDTs matches something; an index past the bounds does not.
	// A character class is not a pattern any more: refused.
	out, code = checkIn(t, files(`["Counts[2]", "Trend[*]", "Drives[*].Speed", "Counts[9]"]`))
	if code != 0 {
		t.Fatalf("element patterns failed the check (%d):\n%s", code, out)
	}
	for _, quiet := range []string{`"Counts[2]"`, `"Trend[*]"`, `"Drives[*].Speed"`} {
		if strings.Contains(out, quiet) {
			t.Errorf("%s warned:\n%s", quiet, out)
		}
	}
	if !strings.Contains(out, `warning: opcua.writable: "Counts[9]" matches no tag or member`) {
		t.Errorf("an index past the bounds was not reported:\n%s", out)
	}
	out, code = checkIn(t, files(`["Counts[1-3]"]`))
	if code == 0 || !strings.Contains(out, `opcua.writable pattern "Counts[1-3]"`) {
		t.Errorf("a character class passed (%d):\n%s", code, out)
	}
}

// opcua.anonymous-writes is a warning, never an error: with nothing under
// opcua.writable it opens nothing; with a list it is in effect, and the
// warning says any client that can reach the endpoint may write, more
// strongly (naming the address) when it listens beyond loopback.
func TestCheckWarnsOnAnonymousWrites(t *testing.T) {
	files := func(opcua string) map[string]string {
		return map[string]string{
			"nautilus.yaml": `
tasks:
  - program: program.st
opcua:
` + opcua + `
tags:
  - { name: LevelSP, role: setpoint, init: 5.0 }
`,
			"program.st": `PROGRAM Main
VAR_EXTERNAL LevelSP : REAL; END_VAR
END_PROGRAM`,
		}
	}
	out, code := checkIn(t, files("  anonymous-writes: true\n"))
	if code != 0 {
		t.Fatalf("a warning failed the check (%d):\n%s", code, out)
	}
	if !strings.Contains(out, "warning: opcua.anonymous-writes is on but opcua.writable lists nothing") {
		t.Errorf("no warning for anonymous-writes without writable:\n%s", out)
	}
	out, code = checkIn(t, files("  anonymous-writes: true\n  writable: [LevelSP]\n"))
	if code != 0 || !strings.Contains(out, "warning: opcua.anonymous-writes is on: any client that can reach opc.tcp://localhost:4840 (this machine only") ||
		!strings.Contains(out, "(LevelSP) without a certificate") || strings.Contains(out, "beyond loopback") {
		t.Errorf("anonymous-writes on loopback with a writable list (%d):\n%s", code, out)
	}
	out, code = checkIn(t, files("  endpoint: opc.tcp://0.0.0.0:4840\n  anonymous-writes: true\n  writable: [LevelSP]\n"))
	if code != 0 || !strings.Contains(out, "warning: opcua.anonymous-writes is on and opc.tcp://0.0.0.0:4840 listens beyond loopback: ANY client") {
		t.Errorf("anonymous-writes on every interface (%d):\n%s", code, out)
	}
	out, code = checkIn(t, files("  writable: [LevelSP]\n"))
	if code != 0 || strings.Contains(out, "anonymous-writes") {
		t.Errorf("a writable list without the switch mentioned anonymous-writes (%d):\n%s", code, out)
	}
}

// opcua.anonymous-writes is a warning, never an error: with nothing under
// opcua.writable it opens nothing; with a list it is in effect, and the
// warning says any client that can reach the endpoint may write, more
// strongly (naming the address) when it listens beyond loopback.
func TestCheckWarnsOnAnonymousWrites(t *testing.T) {
	files := func(opcua string) map[string]string {
		return map[string]string{
			"nautilus.yaml": `
tasks:
  - program: program.st
opcua:
` + opcua + `
tags:
  - { name: LevelSP, role: setpoint, init: 5.0 }
`,
			"program.st": `PROGRAM Main
VAR_EXTERNAL LevelSP : REAL; END_VAR
END_PROGRAM`,
		}
	}
	out, code := checkIn(t, files("  anonymous-writes: true\n"))
	if code != 0 {
		t.Fatalf("a warning failed the check (%d):\n%s", code, out)
	}
	if !strings.Contains(out, "warning: opcua.anonymous-writes is on but opcua.writable lists nothing") {
		t.Errorf("no warning for anonymous-writes without writable:\n%s", out)
	}
	out, code = checkIn(t, files("  anonymous-writes: true\n  writable: [LevelSP]\n"))
	if code != 0 || !strings.Contains(out, "warning: opcua.anonymous-writes is on: any client that can reach opc.tcp://localhost:4840 (this machine only") ||
		!strings.Contains(out, "(LevelSP) without a certificate") || strings.Contains(out, "beyond loopback") {
		t.Errorf("anonymous-writes on loopback with a writable list (%d):\n%s", code, out)
	}
	out, code = checkIn(t, files("  endpoint: opc.tcp://0.0.0.0:4840\n  anonymous-writes: true\n  writable: [LevelSP]\n"))
	if code != 0 || !strings.Contains(out, "warning: opcua.anonymous-writes is on and opc.tcp://0.0.0.0:4840 listens beyond loopback: ANY client") {
		t.Errorf("anonymous-writes on every interface (%d):\n%s", code, out)
	}
	out, code = checkIn(t, files("  writable: [LevelSP]\n"))
	if code != 0 || strings.Contains(out, "anonymous-writes") {
		t.Errorf("a writable list without the switch mentioned anonymous-writes (%d):\n%s", code, out)
	}
}
