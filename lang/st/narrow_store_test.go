package st

import (
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/lang/ir"
)

const narrowDecls = `
FUNCTION F_Half : INT
VAR_INPUT x : INT; END_VAR
F_Half := x / 2;
END_FUNCTION

FUNCTION_BLOCK FB_Pass
VAR_INPUT x : INT; END_VAR
VAR_OUTPUT y : DINT; END_VAR
y := x;
END_FUNCTION_BLOCK
`

// #782: IEC 61131-3 converts implicitly only as its Figure 11 allows
// (within the signed, unsigned or bit-string family and wider, or an
// unsigned type into a wider signed one). Anything else, and a constant
// the target cannot hold, is refused with the conversion to write, at
// every place a program stores a value.
func TestImplicitConversionsFollowTheIECTable(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{"i := d;", "cannot assign DINT to INT implicitly; write DINT_TO_INT(…)"},
		{"s := i;", "cannot assign INT to SINT implicitly; write INT_TO_SINT(…)"},
		{"i := d + 1;", "cannot assign DINT to INT implicitly"},
		{"ud := i;", "cannot assign INT to UDINT implicitly; write INT_TO_UDINT(…)"},
		{"ui := i;", "cannot assign INT to UINT implicitly"},
		{"i := w;", "cannot assign WORD to INT implicitly; write WORD_TO_INT(…)"},
		{"w := i;", "cannot assign INT to WORD implicitly; write INT_TO_WORD(…)"},
		{"d := w;", "cannot assign WORD to DINT implicitly; write WORD_TO_DINT(…)"},
		{"i := MAX(i, d);", "cannot assign DINT to INT implicitly"},
		{"i := LIMIT(0, d, 100);", "cannot assign DINT to INT implicitly"},
		// Constants: a literal, a folded expression, a named constant.
		{"i := 70000;", "cannot assign 70000 to INT: it holds -32768..32767"},
		{"ui := -1;", "cannot assign -1 to UINT: it holds 0..65535"},
		{"i := 30000 + 30000;", "cannot assign 60000 to INT"},
		{"i := K;", "cannot assign DINT to INT implicitly"},
		{"us := KI;", "cannot assign INT to USINT implicitly"},
		// The other store sites.
		{"i := F_Half(d);", "FUNCTION F_Half arg 1: cannot assign DINT to INT implicitly"},
		{"i := F_Half(x := 70000);", "cannot assign 70000 to INT"},
		{"fb(x := d);", "FB FB_Pass input x: cannot assign DINT to INT implicitly"},
		{"fb(x := 1, y => i);", "FB FB_Pass output y => i: cannot assign DINT to INT implicitly"},
		{"FOR s := 0 TO 200 DO END_FOR;", "FOR s end: cannot assign 200 to SINT"},
	} {
		_, err := lowerSrc(narrowDecls + `PROGRAM P
VAR CONSTANT K : DINT := 70000; KI : INT := -1; END_VAR
VAR d : DINT; i : INT; s : SINT; us : USINT; ui : UINT; ud : UDINT; w : WORD; fb : FB_Pass; END_VAR
` + tc.body + `
END_PROGRAM`)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", tc.body, err, tc.want)
		}
	}
	if _, err := lowerSrc(`PROGRAM P
VAR v : INT := 70000; END_VAR
END_PROGRAM`); err == nil || !strings.Contains(err.Error(), "initial value 70000 does not fit INT (-32768..32767)") {
		t.Errorf("declaration init: %v", err)
	}
	// What the table allows, and the explicit conversions, are fine.
	if _, err := lowerSrc(narrowDecls + `PROGRAM P
VAR d : DINT; i : INT; s : SINT; us : USINT; ui : UINT; w : WORD; b : BYTE; l : LINT; fb : FB_Pass; END_VAR
d := i; i := s; i := us; d := ui; w := b; l := d; i := 32767; i := -32768;
i := DINT_TO_INT(d); w := INT_TO_WORD(i); i := i + s; i := F_Half(s);
fb(x := s, y => d); fb(x := i, y => l);
FOR s := 0 TO 100 DO END_FOR;
END_PROGRAM`); err != nil {
		t.Errorf("the table's widenings and explicit conversions: %v", err)
	}
}

// #782: an integer store wraps to the target's width, as on a PLC:
// arithmetic itself never wraps (#326), so INT + INT stored into an INT
// keeps the low 16 bits, at an assignment and at an input.
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
		{"x : INT; out : LINT;", "out := x + x;", map[string]ir.Value{"x": ir.IntVal(30000)}, 60000},
		// An input stores too: F_Half's INT x gets 60000 as -5536.
		{"x : INT; out : INT;", "out := F_Half(x + x);", map[string]ir.Value{"x": ir.IntVal(30000)}, -2768},
		// A shift keeps its operand's width, a computed one included.
		{"w : WORD; out : WORD;", "out := ROL(w AND 16#FFFF, 1);", map[string]ir.Value{"w": ir.IntVal(0x8000)}, 1},
		{"x : BYTE; out : WORD;", "out := ROL(BYTE#1, 9);", nil, 2},
	} {
		src := narrowDecls + "PROGRAM P\nVAR_EXTERNAL " + tc.decl + " END_VAR\n" + tc.body + "\nEND_PROGRAM"
		if got := runOne(t, src, tc.seed, "out"); got != tc.want {
			t.Errorf("%s with %v: out = %d, want %d", tc.body, tc.seed, got, tc.want)
		}
	}
}
