package ld

import (
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/lang/fbd"
	"github.com/joyautomation/nautilus/lang/ir"
)

// #782: a ladder assignment is a store like any other: an implicit
// conversion the IEC table does not allow is refused with the conversion
// to write, through the FBD a rung lowers to, and what is left wraps.
func TestLadderStoreFollowsTheIntegerRules(t *testing.T) {
	out, err := Transpile(`PROGRAM p
VAR_EXTERNAL go : BOOL; d : DINT; i : INT; END_VAR
LD
  RUNG r0
    go { i := d }
END_LD
END_PROGRAM`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fbd.Compile(out); err == nil || !strings.Contains(err.Error(), "cannot assign DINT to INT implicitly; write DINT_TO_INT(…)") {
		t.Errorf("{ i := d }: err = %v", err)
	}
	scan, h := runLD(t, `PROGRAM p
VAR_EXTERNAL go : BOOL; x : INT; i : INT; END_VAR
LD
  RUNG r0
    go { i := x + x }
END_LD
END_PROGRAM`)
	scan(map[string]ir.Value{"go": ir.BoolVal(true), "x": ir.IntVal(30000)})
	if h.vals["i"].I != -5536 {
		t.Errorf("{ i := x + x } with x = 30000: i = %d, want -5536", h.vals["i"].I)
	}
}
