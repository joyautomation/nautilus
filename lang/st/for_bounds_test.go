package st

import (
	"strings"
	"testing"
)

// #893: a FOR's start, end and step are integers, like its variable
// (IEC 61131-3). A REAL end used to pass check and then run the loop zero
// times (the VM read its integer field, 0); now it is refused, naming the
// conversion, as #782's store rules do.
func TestForRefusesNonIntegerBounds(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{"FOR i := 1 TO N DO c := c + 1; END_FOR;", "FOR i end: N is REAL, and a FOR's bounds and step are integers like its variable; write REAL_TO_DINT(N)"},
		{"FOR i := 1 TO 3.0 DO END_FOR;", "FOR i end: the value is REAL"},
		{"FOR i := 1 TO 10 BY L DO END_FOR;", "FOR i step: L is LREAL, and a FOR's bounds and step are integers like its variable; write LREAL_TO_DINT(L)"},
		{"FOR i := T TO 3 DO END_FOR;", "FOR i start: T is TIME"},
		{"FOR i := 0 TO N * 2.0 DO END_FOR;", "FOR i end: the value is REAL, and a FOR's bounds and step are integers like its variable; write REAL_TO_DINT(…)"},
		{"FOR s := 1 TO N DO END_FOR;", "write REAL_TO_SINT(N)"},
		{"FOR i := 1 TO Txt DO END_FOR;", "FOR i end: Txt is STRING, and a FOR's bounds and step are integers like its variable; write STRING_TO_DINT(Txt)"},
		// A struct has no conversion to name.
		{"FOR i := 1 TO P DO END_FOR;", "FOR i end: P is Pair, and a FOR's bounds and step are integers like its variable\n"},
	} {
		_, err := lowerSrc(`TYPE Pair : STRUCT a : DINT; b : DINT; END_STRUCT; END_TYPE
PROGRAM P
VAR i : DINT; s : SINT; c : DINT; N : REAL := 3.0; L : LREAL := 1.0; T : TIME; Txt : STRING; P : Pair; END_VAR
` + tc.body + `
END_PROGRAM`)
		if err == nil || !strings.Contains(err.Error()+"\n", tc.want) {
			t.Errorf("%s: err = %v, want %q", tc.body, err, tc.want)
		}
	}
	// Integer bounds of any width, a converted REAL, and a named integer
	// constant are fine.
	if _, err := lowerSrc(`PROGRAM P
VAR CONSTANT K : INT := 4; END_VAR
VAR i : DINT; m : INT; u : USINT; R : REAL; END_VAR
FOR i := 1 TO m DO END_FOR;
FOR i := u TO K BY 2 DO END_FOR;
FOR i := 1 TO REAL_TO_DINT(R) DO END_FOR;
END_PROGRAM`); err != nil {
		t.Errorf("integer bounds: %v", err)
	}
}
