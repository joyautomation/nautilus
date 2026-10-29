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
