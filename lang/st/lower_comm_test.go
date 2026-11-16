package st

import (
	"strings"
	"testing"
)

// READ's RD_1 and WRITE's SD_1 are ANY: they take a variable (or, for SD_1,
// a value) of any type, with no conversion. RD_1 is an in-out, so it must be
// bound, and bound to a variable.
func TestReadWriteAnyPins(t *testing.T) {
	ok := `TYPE Pt : STRUCT A : INT; END_STRUCT; END_TYPE
PROGRAM main
VAR rd : READ; wr : WRITE; p : Pt; r : REAL; arr : ARRAY[1..3] OF DINT; END_VAR
  rd(REQ := TRUE, ID := 'line2', VAR_1 := 'Remote', RD_1 := p);
  rd(REQ := TRUE, ID := 'line2', VAR_1 := 'Remote', RD_1 := arr);
  wr(REQ := TRUE, ID := 'line2', VAR_1 := 'Remote', SD_1 := r);
  wr(REQ := TRUE, ID := 'line2', VAR_1 := 'Remote', SD_1 := 5);
END_PROGRAM
`
	mustLower(t, ok)

	for _, c := range []struct{ call, want string }{
		{"rd(REQ := TRUE, ID := 'line2', VAR_1 := 'Remote');", "must be bound at every call site"},
		{"rd(REQ := TRUE, ID := 'line2', VAR_1 := 'Remote', RD_1 := 1 + 2);", ""},
	} {
		src := "PROGRAM main\nVAR rd : READ; END_VAR\n  " + c.call + "\nEND_PROGRAM\n"
		_, err := lowerSrc(src)
		if err == nil {
			t.Errorf("%s: lowered, want an error", c.call)
			continue
		}
		if c.want != "" && !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error = %v, want %q", c.call, err, c.want)
		}
	}
}
