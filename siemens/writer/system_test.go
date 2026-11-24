package writer

import "testing"

// The nautilus system functions (#710 (b)) read a nautilus pair's state.
// A CPU this writes for runs none: they are constants, FALSE and 0.
func TestSystemFunctionsAreConstants(t *testing.T) {
	scl, _ := write(t, "PROGRAM P\nVAR a, b, c, d : BOOL; n : DINT; END_VAR\n"+
		"a := TAKEOVER_SCAN();\nb := HOT_TAKEOVER();\nc := PEER_READY();\nd := TAKEOVER_HOLD();\nn := TAKEOVERS();\nEND_PROGRAM\n")
	wantLines(t, scl, "#a := FALSE;", "#b := FALSE;", "#c := FALSE;", "#d := FALSE;", "#n := 0;")
}
