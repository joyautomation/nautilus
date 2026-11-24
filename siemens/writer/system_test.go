package writer

import "testing"

// The nautilus system functions (#710 (b)) read a nautilus pair's state.
// A CPU this writes for runs none: they are constants, FALSE and 0, with
// one warning.
func TestSystemFunctionsAreConstants(t *testing.T) {
	scl, out := write(t, "PROGRAM P\nVAR a, b, c, d : BOOL; n : DINT; END_VAR\n"+
		"a := TAKEOVER_SCAN();\nb := HOT_TAKEOVER();\nc := PEER_READY();\nd := TAKEOVER_HOLD();\nn := TAKEOVERS();\nEND_PROGRAM\n")
	wantLines(t, scl, "#a := FALSE;", "#b := FALSE;", "#c := FALSE;", "#d := FALSE;", "#n := 0;")
	// Logic gated on them is dead code on the CPU: one warning per
	// project, not one per use (naut-lead).
	n := 0
	for _, w := range out.Warnings {
		if w.Rule == PairStateRule {
			n++
			if w.Message != PairStateWarning || w.Line != 3 {
				t.Errorf("the warning: %+v", w)
			}
		}
	}
	if n != 1 {
		t.Errorf("%d pair-state warnings in %v, want one", n, out.Warnings)
	}
}
