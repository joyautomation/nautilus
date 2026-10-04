package main

import (
	"strings"
	"testing"
)

// `naut check` prints the column of the offending name, not of the
// statement's first token (issue #141). Diagram sources keep column 1: their
// position is the rung/netlist line the generated ST maps back to.
func TestCheckReportsIdentifierColumn(t *testing.T) {
	out, code := checkIn(t, map[string]string{
		"plant.st": "PROGRAM plant\nVAR\n  Heater : BOOL;\n  Pump : BOOL;\nEND_VAR\n" +
			"IF Heatr THEN\n  Pump := TRUE;\nEND_IF;\nEND_PROGRAM\n",
		"latch.fbd": "PROGRAM Latch\nVAR_EXTERNAL\n  Start : BOOL; Run : BOOL;\nEND_VAR\n" +
			"FBD\n  Run := AND(Start, bogus)\nEND_FBD\nEND_PROGRAM\n",
	})
	if code != 1 {
		t.Fatalf("exit = %d, want 1\n%s", code, out)
	}
	for _, want := range []string{
		`plant.st:6:4: undeclared identifier "Heatr" (declare in VAR_* or VAR_GLOBAL block)`,
		`latch.fbd:6:1: `,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("check output missing %q:\n%s", want, out)
		}
	}
}
