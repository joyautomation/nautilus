package main

import (
	"strings"
	"testing"
)

// sparkplug.writable is checked as opcua.writable is. A malformed pattern
// (a character class: the syntax has none since [n] became an array
// index) fails the check, as it would fail naut run. A pattern that
// matches no name a command can carry (a tag, or a struct member at any
// depth) is a warning; "Pump[12]", which once opened Pump1 and Pump2, is
// such a pattern now, and the warning says why.
func TestCheckSparkplugWritable(t *testing.T) {
	files := func(writable string) map[string]string {
		return map[string]string{
			"nautilus.yaml": `
tasks:
  - program: program.st
sparkplug:
  broker: tcp://localhost:1883
  group-id: Joy
  writable: ` + writable + `
tags:
  - { name: Pump1, role: setpoint, init: 0.0 }
  - { name: Pump2, role: setpoint, init: 0.0 }
  - { name: P101, role: setpoint, type: Drive }
  - { name: Trend, role: setpoint, init: [1.0, 2.0, 3.0] }
`,
			"types.st": `TYPE Inner : STRUCT Hi : REAL; END_STRUCT END_TYPE
TYPE Drive : STRUCT Speed : REAL; Limits : Inner; END_STRUCT END_TYPE`,
			"program.st": `PROGRAM Main
VAR_EXTERNAL Pump1 : REAL; Pump2 : REAL; P101 : Drive; Trend : ARRAY[0..2] OF REAL; END_VAR
END_PROGRAM`,
		}
	}

	out, code := checkIn(t, files(`["Fill[VW]alve"]`))
	if code == 0 || !strings.Contains(out, `sparkplug.writable pattern "Fill[VW]alve"`) || strings.Contains(out, "sparkplug: sparkplug") {
		t.Errorf("a character class in sparkplug.writable passed the check (%d):\n%s", code, out)
	}

	out, code = checkIn(t, files(`["Pump*", "P101.Speed", "P101.Limits.Hi", "Trend"]`))
	if code != 0 || strings.Contains(out, "sparkplug.writable") {
		t.Errorf("patterns naming tags and members warned (%d):\n%s", code, out)
	}

	out, code = checkIn(t, files(`["Pump[12]", "Pmp*", "Trend[*]", "P101"]`))
	if code != 0 {
		t.Fatalf("warnings failed the check (%d):\n%s", code, out)
	}
	for _, want := range []string{
		`warning: sparkplug.writable: "Pump[12]" matches no tag or member — a typo, or a tag that no longer exists; [n] in a pattern is an array index, not a set of characters`,
		`warning: sparkplug.writable: "Pmp*" matches no tag or member`,
		// An element is never a command's name: a scalar array is one
		// array metric, refused whole.
		`warning: sparkplug.writable: "Trend[*]" matches no tag or member`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, `"P101" matches`) {
		t.Errorf("a struct tag was called unmatched:\n%s", out)
	}
}
