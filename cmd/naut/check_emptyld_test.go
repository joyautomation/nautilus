package main

import (
	"strings"
	"testing"
)

// A 0-byte .ld is a ladder file with nothing in it yet: `naut check` names
// the file and says so in ladder terms — not the FBD hop's "FBD ... END_FBD"
// message, which names a body the author never wrote.
func TestCheckEmptyLadderFileSpeaksLadder(t *testing.T) {
	out, code := checkIn(t, map[string]string{
		"permissives.ld": "",
		"notes.ld":       "PROGRAM p\nEND_PROGRAM\n",
	})
	if code != 1 {
		t.Fatalf("exit = %d, want 1\n%s", code, out)
	}
	for _, want := range []string{
		"permissives.ld: ld: empty file — a ladder source must contain an LD ... END_LD body",
		"notes.ld: ld: source must contain an LD ... END_LD body",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("check output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "FBD") {
		t.Errorf("a ladder file's message must not name the FBD hop:\n%s", out)
	}
}
