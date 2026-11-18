package st

import (
	"strings"
	"testing"
)

// #782 (the REAL half, held for James's call): IEC 61131-3's table takes
// an integer into a REAL only while REAL's mantissa holds every value
// (SINT, INT, USINT, UINT), into an LREAL up to the 32-bit types, and an
// LREAL never into a REAL implicitly.
func TestRealStoresFollowTheIECTable(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{"r := d;", "cannot assign DINT to REAL implicitly; write DINT_TO_REAL(…)"},
		{"r := ud;", "cannot assign UDINT to REAL implicitly"},
		{"r := l;", "cannot assign LINT to REAL implicitly"},
		{"lr := l;", "cannot assign LINT to LREAL implicitly"},
		{"r := lr;", "cannot assign LREAL to REAL implicitly; write LREAL_TO_REAL(…)"},
	} {
		_, err := lowerSrc(`PROGRAM P
VAR d : DINT; ud : UDINT; l : LINT; i : INT; ui : UINT; r : REAL; lr : LREAL; END_VAR
` + tc.body + `
END_PROGRAM`)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", tc.body, err, tc.want)
		}
	}
	if _, err := lowerSrc(`PROGRAM P
VAR d : DINT; ud : UDINT; i : INT; ui : UINT; r : REAL; lr : LREAL; END_VAR
r := i; r := ui; lr := d; lr := ud; lr := r; r := DINT_TO_REAL(d); r := 1; r := d / 2.0;
END_PROGRAM`); err != nil {
		t.Errorf("what the table allows: %v", err)
	}
}
