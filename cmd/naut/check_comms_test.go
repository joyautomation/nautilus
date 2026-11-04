package main

import (
	"strings"
	"testing"
)

// A protocol input reads as its init: or the zero of its type until the
// device first answers — when a type is knowable. One with none (no type:,
// no program declares it, the driver's binding has no IEC spelling: a Logix
// STIME) has no value until delivered, and check says so as a warning with
// the fix. The tags a program reads are typed by the program and stay quiet.
func TestCheckWarnsOnUntypedCommsInput(t *testing.T) {
	out, code := checkIn(t, map[string]string{
		"nautilus.yaml": `
tasks:
  - program: program.st
driver:
  type: eip
  host: 127.0.0.1
  manifest: eip_manifest.yaml
tags:
  - { name: Speed, role: input }
  - { name: Clock, role: input }
  - { name: Out, role: state, init: 0.0 }
`,
		"eip_manifest.yaml": `tags:
  - { name: Speed, device: Speed, type: REAL }
  - { name: Clock, device: Clock, type: STIME }
`,
		"program.st": `PROGRAM Main
VAR_EXTERNAL Speed : REAL; Out : REAL; END_VAR
Out := Speed;
END_PROGRAM`,
	})
	if code != 0 {
		t.Fatalf("a warning must not fail the check (%d):\n%s", code, out)
	}
	if !strings.Contains(out, `warning: input "Clock" comes from a communications driver but has no knowable type`) {
		t.Errorf("no warning for the untyped protocol input:\n%s", out)
	}
	if strings.Contains(out, `input "Speed"`) {
		t.Errorf("a program-typed input was reported:\n%s", out)
	}
}

// A declared tag whose driver binding differs only in case is an error: the
// driver delivers, and accepts writes for, its binding's exact spelling, and
// the tag store matches exactly, so the tag would never receive a value
// (input) or never reach the device (output), silently (#544). Exact
// spellings, companions spelled as the driver spells them, and state tags
// stay quiet.
func TestCheckRefusesACaseOnlyBindingMatch(t *testing.T) {
	out, code := checkIn(t, map[string]string{
		"nautilus.yaml": `
tasks:
  - program: program.st
driver:
  type: modbus
  manifest: modbus_manifest.yaml
tags:
  - { name: m1_kw, role: input, type: REAL }
  - { name: M1_Volts, role: input, type: REAL }
  - { name: m1_runcmd, role: output, init: false }
  - { name: M1__Online, role: input, type: BOOL }
  - { name: Total, role: state, init: 0.0 }
  - { name: m2_kw, role: state, init: 0.0 }
`,
		"modbus_manifest.yaml": `sources:
  - id: M1
    host: 127.0.0.1
    unitid: 1
tags:
  - {name: M1_KW, source: M1, table: holding, address: 0, format: float32}
  - {name: M1_Volts, source: M1, table: holding, address: 2, format: float32}
  - {name: M1_RunCmd, source: M1, table: holding, address: 10, format: bit:0, writable: true}
  - {name: M2_KW, source: M1, table: holding, address: 4, format: float32}
`,
		"program.st": `PROGRAM Main
VAR_EXTERNAL m1_kw : REAL; M1_Volts : REAL; m1_runcmd : BOOL; M1__Online : BOOL; Total : REAL; END_VAR
Total := m1_kw + M1_Volts;
m1_runcmd := M1__Online;
END_PROGRAM`,
	})
	if code == 0 {
		t.Fatalf("a case-only binding match passed the check:\n%s", out)
	}
	for _, want := range []string{
		`error: tag "m1_kw" is spelled "M1_KW" in the driver's bindings`,
		`would never receive the driver's values`,
		`error: tag "m1_runcmd" is spelled "M1_RunCmd" in the driver's bindings`,
		`would never reach the device`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	// A state tag never takes driver delivery, so one beside an undeclared
	// binding of the same name is not reported.
	for _, quiet := range []string{`tag "M1_Volts"`, `tag "M1__Online"`, `tag "m2_kw"`} {
		if strings.Contains(out, quiet) {
			t.Errorf("an exact spelling was reported (%s):\n%s", quiet, out)
		}
	}
}
