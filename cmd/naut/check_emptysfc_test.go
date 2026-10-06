package main

import (
	"strings"
	"testing"
)

// #179: a new, still-empty .sfc (VS Code's New File, before the diagram's
// "initialize") is not a parse error: naut check says what to do with it
// as one warning and stays clean. A task naming it still fails.
func TestCheckEmptySFCIsOneWarning(t *testing.T) {
	out, code := checkIn(t, map[string]string{
		"washer.sfc": "",
		"main.st":    "PROGRAM main\nVAR x : BOOL; END_VAR\nx := TRUE;\nEND_PROGRAM\n",
	})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\n%s", code, out)
	}
	if n := strings.Count(out, "washer.sfc"); n != 1 {
		t.Errorf("want exactly one line about washer.sfc, got %d:\n%s", n, out)
	}
	for _, want := range []string{
		`washer.sfc:1:1: warning: empty chart — no PROGRAM yet; open it as a diagram and click "initialize"`,
		"0 with errors, 1 warning(s)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("check output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "END_SFC") {
		t.Errorf("an empty chart's message must not be the parser's:\n%s", out)
	}
}

func TestCheckEmptySFCNamedByATaskFails(t *testing.T) {
	out, code := checkIn(t, map[string]string{
		"washer.sfc":    "\n",
		"nautilus.yaml": "name: w\ntasks:\n  - program: washer.sfc\n    scan: 100ms\n",
	})
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (a task runs it)\n%s", code, out)
	}
}
