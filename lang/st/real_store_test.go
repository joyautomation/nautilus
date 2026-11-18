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
r := i; r := ui; lr := d; lr := ud; lr := r; r := DINT_TO_REAL(d); r := 1; lr := d / 2.0; r := i * 2.0;
END_PROGRAM`); err != nil {
		t.Errorf("what the table allows: %v", err)
	}
}

const realDecls = `
FUNCTION F_R : REAL
VAR_INPUT x : REAL; END_VAR
F_R := x;
END_FUNCTION

FUNCTION_BLOCK FB_R
VAR_INPUT x : REAL; END_VAR
VAR_OUTPUT y : LREAL; END_VAR
y := x;
END_FUNCTION_BLOCK
`

// #782: the REAL half of the table holds at every place a program stores
// a value, not only at an assignment, and a real result keeps its width
// through arithmetic and functions (an LREAL in, or a DINT, gives an
// LREAL), so none of these slips into a REAL.
func TestRealRuleAtEveryStoreAndThroughExpressions(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		// Store sites.
		{"fr(x := lr);", "FB FB_R input x: cannot assign LREAL to REAL implicitly"},
		{"fr(x := d);", "FB FB_R input x: cannot assign DINT to REAL implicitly"},
		{"fr(x := 1.0, y => r);", "FB FB_R output y => r: cannot assign LREAL to REAL implicitly"},
		{"r := F_R(lr);", "FUNCTION F_R arg 1: cannot assign LREAL to REAL implicitly"},
		{"r := F_R(x := d);", "FUNCTION F_R arg x: cannot assign DINT to REAL implicitly"},
		// Expressions keep the width.
		{"r := lr * 2.0;", "cannot assign LREAL to REAL implicitly"},
		{"r := lr + r;", "cannot assign LREAL to REAL implicitly"},
		{"r := MAX(lr, r);", "cannot assign LREAL to REAL implicitly"},
		{"r := SEL(TRUE, r, lr);", "cannot assign LREAL to REAL implicitly"},
		{"r := SQRT(lr);", "cannot assign LREAL to REAL implicitly"},
		{"r := d * 1.0;", "cannot assign LREAL to REAL implicitly"},
		{"lr := l * 1.0;", "LINT does not convert implicitly to REAL or LREAL; write LINT_TO_LREAL(…)"},
	} {
		_, err := lowerSrc(realDecls + `PROGRAM P
VAR d : DINT; i : INT; l : LINT; r : REAL; lr : LREAL; fr : FB_R; END_VAR
` + tc.body + `
END_PROGRAM`)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", tc.body, err, tc.want)
		}
	}
	for _, init := range []struct{ decl, want string }{
		{"o : REAL := KLR;", "initial value: cannot assign LREAL to REAL implicitly"},
		{"o : REAL := K;", "initial value: cannot assign DINT to REAL implicitly"},
	} {
		_, err := lowerSrc(`PROGRAM P
VAR CONSTANT KLR : LREAL := 1.5; K : DINT := 7; END_VAR
VAR ` + init.decl + ` END_VAR
END_PROGRAM`)
		if err == nil || !strings.Contains(err.Error(), init.want) {
			t.Errorf("%s: err = %v, want %q", init.decl, err, init.want)
		}
	}
	if _, err := lowerSrc(realDecls + `PROGRAM P
VAR d : DINT; i : INT; r : REAL; lr : LREAL; fr : FB_R; pic : PID; END_VAR
lr := lr * 2.0; lr := d * 1.0; r := r * 2.0; r := i * 2.0; lr := SQRT(lr); r := SQRT(r);
fr(x := r, y => lr); fr(x := i); r := F_R(r); lr := MAX(lr, r);
pic(PV := lr, SP := lr, KP := lr); (* a standard block's pins are generic *)
END_PROGRAM`); err != nil {
		t.Errorf("what the table allows: %v", err)
	}
}
