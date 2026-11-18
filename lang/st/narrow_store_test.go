package st

import (
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/lang/ir"
)

// #782: IEC 61131-3 converts implicitly only when widening. An assignment
// that would narrow a declared width, or store a literal the target cannot
// hold, is refused with the conversion to write instead.
func TestImplicitNarrowingIsRefused(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{"i := d;", "cannot assign DINT to INT: an implicit conversion only widens; write DINT_TO_INT(…)"},
		{"s := i;", "cannot assign INT to SINT"},
		{"i := d + 1;", "cannot assign DINT to INT"},
		{"i := 70000;", "cannot assign 70000 to INT: it holds -32768..32767"},
		{"u := -1;", "cannot assign -1 to UINT: it holds 0..65535"},
	} {
		_, err := lowerSrc(`PROGRAM P
VAR d : DINT; i : INT; s : SINT; u : UINT; END_VAR
` + tc.body + `
END_PROGRAM`)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", tc.body, err, tc.want)
		}
	}
	// Widening, a value the target holds, and the explicit conversion are
	// fine.
	if _, err := lowerSrc(`PROGRAM P
VAR d : DINT; i : INT; s : SINT; END_VAR
d := i; i := s; i := 32767; i := -32768; i := DINT_TO_INT(d); i := i + s;
END_PROGRAM`); err != nil {
		t.Errorf("widening and explicit conversions: %v", err)
	}
}

// #782: an integer store wraps to the target's width, as on a PLC:
// arithmetic itself never wraps (#326), so INT + INT stored into an INT
// keeps the low 16 bits.
func TestIntegerStoreWrapsToTheTargetWidth(t *testing.T) {
	for _, tc := range []struct {
		decl, body string
		seed       map[string]ir.Value
		want       int64
	}{
		{"x : INT; out : INT;", "out := x + x;", map[string]ir.Value{"x": ir.IntVal(30000)}, -5536},
		{"x : SINT; out : SINT;", "out := x * 2;", map[string]ir.Value{"x": ir.IntVal(100)}, -56},
		{"x : DINT; out : DINT;", "out := x + 1;", map[string]ir.Value{"x": ir.IntVal(2147483647)}, -2147483648},
		{"x : UINT; out : UINT;", "out := x - 1;", map[string]ir.Value{"x": ir.IntVal(0)}, 65535},
		{"x : INT; out : INT;", "out := x + x;", map[string]ir.Value{"x": ir.IntVal(100)}, 200},
		// Into a wider target nothing is lost: LINT holds the sum.
		{"x : INT; out : LINT;", "out := x + x;", map[string]ir.Value{"x": ir.IntVal(30000)}, 60000},
	} {
		src := "PROGRAM P\nVAR_EXTERNAL " + tc.decl + " END_VAR\n" + tc.body + "\nEND_PROGRAM"
		if got := runOne(t, src, tc.seed, "out"); got != tc.want {
			t.Errorf("%s with %v: out = %d, want %d", tc.body, tc.seed, got, tc.want)
		}
	}
}
