package fbd

import (
	"errors"
	"strings"
	"testing"
)

// FBD has blocks, not infix operators. An operator where an expression has
// ended says so and names the block that does its job (nautilus#562),
// instead of the `expected ')'` or `expected a wire, coil, or block` the
// next grammar rule used to report — on the operator's own column.
func TestInfixOperatorSaysWhichBlockToUse(t *testing.T) {
	for _, tc := range []struct {
		expr, want string
		col        int
	}{
		{"a + b", "write ADD(a, b), not a + b", 10},
		{"a - b", "write SUB(a, b), not a - b", 10},
		{"a * b", "write MUL(a, b), not a * b", 10},
		{"a / b", "write DIV(a, b), not a / b", 10},
		{"a ** 2", "write EXPT(a, b), not a ** b", 10},
		{"a MOD 2", "write MOD(a, b), not a MOD b", 10},
		{"(a + b)", "write ADD(a, b), not a + b", 11},
		{"ADD(a + 1, b)", "write ADD(a, b), not a + b", 14},
		{"ADD(a, b) * 2", "write MUL(a, b), not a * b", 18},
		{"x = y", "write EQ(a, b), not a = b", 10},
		{"x <> y", "write NE(a, b), not a <> b", 10},
		{"a < b", "write LT(a, b), not a < b", 10},
		{"a <= b", "write LE(a, b), not a <= b", 10},
		{"a > b", "write GT(a, b), not a > b", 10},
		{"a >= b", "write GE(a, b), not a >= b", 10},
		{"x AND y", "write AND(a, b), not a AND b", 10},
		{"x OR y", "write OR(a, b), not a OR b", 10},
		{"x XOR y", "write XOR(a, b), not a XOR b", 10},
		{"NOT x AND y", "write AND(a, b), not a AND b", 14},
		{"-a", "negate with SUB(0, a)", 8},
		{"ADD(a, -5)", "a typed literal (INT#-5, REAL#-1.5)", 15},
	} {
		src := "PROGRAM P\nVAR a : INT; b : INT; x : BOOL; y : BOOL; z : INT; END_VAR\nFBD\n  z := " + tc.expr + "\nEND_FBD\nEND_PROGRAM\n"
		_, err := Transpile(src)
		var pe *ParseError
		if !errors.As(err, &pe) {
			t.Errorf("%s: want a ParseError, got %v", tc.expr, err)
			continue
		}
		if !strings.Contains(pe.Msg, "FBD has no operators") || !strings.Contains(pe.Msg, tc.want) {
			t.Errorf("%s: message %q, want it to say FBD has no operators and %q", tc.expr, pe.Msg, tc.want)
		}
		if pe.Line != 4 || pe.Col != tc.col {
			t.Errorf("%s: at %d:%d, want 4:%d (the operator)", tc.expr, pe.Line, pe.Col, tc.col)
		}
	}

	// The block forms parse; NOT stays pin negation.
	for _, ok := range []string{"ADD(a, b)", "MOD(a, 2)", "SUB(0, a)", "ADD(a, INT#-5)"} {
		if _, err := Transpile("PROGRAM P\nVAR a : INT; b : INT; z : INT; END_VAR\nFBD\n  z := " + ok + "\nEND_FBD\nEND_PROGRAM\n"); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	if _, err := Transpile("PROGRAM P\nVAR x : BOOL; y : BOOL; END_VAR\nFBD\n  y := AND(NOT x, y)\nEND_FBD\nEND_PROGRAM\n"); err != nil {
		t.Errorf("NOT as pin negation: %v", err)
	}
}
