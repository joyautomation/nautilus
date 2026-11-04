package ir_test

import (
	"sort"
	"strings"
	"testing"
)

// A user FUNCTION's VAR_EXTERNAL reads and writes the tag store through the
// program that calls it, so GlobalsDeep reports it as bound by that program
// (nautilus#590): called from the body, from an FB body, or from another
// FUNCTION. A FUNCTION nothing calls binds nothing.
func TestGlobalsDeepFollowsFunctionCalls(t *testing.T) {
	const lib = `FUNCTION Inner : REAL
VAR_EXTERNAL Bias : REAL; END_VAR
Inner := Bias;
END_FUNCTION

FUNCTION Scale : REAL
VAR_INPUT k : REAL; END_VAR
VAR_EXTERNAL RawLevel : REAL; PumpCmd : REAL; END_VAR
PumpCmd := RawLevel * k + Inner();
Scale := PumpCmd;
END_FUNCTION

FUNCTION FromBlock : BOOL
VAR_EXTERNAL Mode : INT; END_VAR
FromBlock := Mode > 0;
END_FUNCTION

FUNCTION Unused : BOOL
VAR_EXTERNAL Dead : BOOL; END_VAR
Unused := Dead;
END_FUNCTION

FUNCTION_BLOCK Holder
VAR_OUTPUT Q : BOOL; END_VAR
Q := FromBlock();
END_FUNCTION_BLOCK
`
	p := lowerOne(t, lib+`
PROGRAM Main
VAR_EXTERNAL Own : BOOL; END_VAR
VAR r : REAL; h : Holder; END_VAR
r := Scale(2.0);
h();
Own := h.Q;
END_PROGRAM`)
	got := p.GlobalsDeep()
	var names []string
	for n, typ := range got {
		names = append(names, n+":"+typ.String())
	}
	sort.Strings(names)
	want := "Bias:REAL,Mode:INT,Own:BOOL,PumpCmd:REAL,RawLevel:REAL"
	if strings.Join(names, ",") != want {
		t.Errorf("GlobalsDeep = %s, want %s", strings.Join(names, ","), want)
	}
}
