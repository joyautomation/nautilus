package st

import (
	"regexp"
	"strings"
	"testing"
)

const bindDecls = `
FUNCTION_BLOCK H
VAR_INPUT x : REAL; END_VAR
VAR_OUTPUT o : REAL; lo : LREAL; e : INT; si : SINT; b : BOOL; tm : TIME; s : STRING; END_VAR
o := x;
END_FUNCTION_BLOCK
PROGRAM P
VAR h : H; c : PID; tn : TON; cu : CTU; cd : CTUD; lt : LOCAL_TIME; i : INT; d : DINT; li : LINT; r : REAL; l : LREAL; bb : BOOL; t : TIME; w : WORD; END_VAR
`

var linePrefix = regexp.MustCompile(`^line \d+: `)

// #1378: out => v stores the output into v, so it is refused exactly where
// v := fb.out is, in the same words: a REAL output bound to an INT used to
// pass check and leave the INT holding 2.5 (or 40000) at run time.
func TestOutputBindingFollowsTheAssignmentRule(t *testing.T) {
	for _, tc := range []struct{ out, target string }{
		{"h.o", "i"}, {"h.o", "d"}, {"h.o", "li"}, {"h.lo", "i"}, {"h.lo", "d"},
		{"c.CV", "i"}, {"c.CV", "d"}, // the standard PID's REAL output
		{"h.o", "bb"}, {"h.o", "t"}, {"h.o", "w"},
		{"h.b", "i"}, {"h.tm", "d"}, {"h.s", "i"}, {"tn.ET", "d"},
		{"h.lo", "r"}, // LREAL into REAL: already refused, still the same words
	} {
		inst, pin, _ := strings.Cut(tc.out, ".")
		_, aerr := lowerSrc(bindDecls + tc.target + " := " + tc.out + ";\nEND_PROGRAM")
		_, berr := lowerSrc(bindDecls + inst + "(" + pin + " => " + tc.target + ");\nEND_PROGRAM")
		if aerr == nil {
			t.Fatalf("%s := %s: the assignment is not refused; the test's premise is wrong", tc.target, tc.out)
		}
		if berr == nil {
			t.Errorf("%s(%s => %s): not refused, while %s := %s is (%v)", inst, pin, tc.target, tc.target, tc.out, aerr)
			continue
		}
		fb := map[string]string{"h": "H", "c": "PID", "tn": "TON"}[inst]
		want := "FB " + fb + " output " + pin + " => " + tc.target + ": " + linePrefix.ReplaceAllString(aerr.Error(), "")
		if got := linePrefix.ReplaceAllString(berr.Error(), ""); got != want {
			t.Errorf("%s(%s => %s):\n got %q\nwant %q", inst, pin, tc.target, got, want)
		}
	}
	// What an assignment allows, a binding allows: widenings, an integer
	// into a REAL, a REAL into an LREAL, and every standard block's
	// outputs into variables of their own type.
	if _, err := lowerSrc(bindDecls + `
h(o => l, lo => l, e => d, si => i, e => r, e => li);
c(CV => r, SAT_HI => bb); c(CV => l, ERR => r);
tn(IN := bb, PT := T#1s, Q => bb, ET => t);
cu(CU := bb, PV := 3, CV => i); cu(CU := bb, PV := 3, CV => d); cu(CU := bb, PV := 3, CV => li);
cd(CU := bb, CD := bb, PV := 3, QU => bb, CV => d);
lt(YEAR => i, SECOND => d);
END_PROGRAM`); err != nil {
		t.Errorf("widening bindings: %v", err)
	}
}
