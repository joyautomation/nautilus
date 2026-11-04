package ir_test

import (
	"sort"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/lang/ir"
)

func globalsText(m map[string]*ir.Type) string {
	var names []string
	for n, typ := range m {
		names = append(names, n+":"+typ.String())
	}
	sort.Strings(names)
	return strings.Join(names, ",")
}

// An FB instance a FUNCTION declares runs as part of the call, so what the
// block binds through its VAR_EXTERNAL is the calling program's too
// (nautilus#590): bound, read and written.
func TestGlobalsDeepFollowsAnFBInsideAFunction(t *testing.T) {
	p := lowerOne(t, `FUNCTION_BLOCK Holder
VAR_EXTERNAL Mode : INT; Count : DINT; END_VAR
VAR_OUTPUT Q : BOOL; END_VAR
Q := Mode > 0;
Count := Count + 1;
END_FUNCTION_BLOCK

FUNCTION Wrap : INT
VAR h : Holder; END_VAR
h();
IF h.Q THEN Wrap := 1; ELSE Wrap := 0; END_IF;
END_FUNCTION

PROGRAM Main
VAR r : INT; END_VAR
r := Wrap();
END_PROGRAM`)
	if got, want := globalsText(p.GlobalsDeep()), "Count:DINT,Mode:INT"; got != want {
		t.Errorf("GlobalsDeep = %s, want %s", got, want)
	}
	u := p.GlobalUses()
	if !u.Read["Mode"] || !u.Read["Count"] || !u.Written["Count"] {
		t.Errorf("GlobalUses = %+v, want Mode read and Count read and written", u)
	}
}

// One tag declared by the PROGRAM and by a FUNCTION it calls keeps the
// program's declaration; a different shape is a BindingConflict, a
// different width of the same kind is not.
func TestGlobalsDeepKeepsTheProgramsDeclaration(t *testing.T) {
	p := lowerOne(t, `FUNCTION Peek : INT
VAR_EXTERNAL Mode : INT; Count : INT; END_VAR
Peek := Mode + Count;
END_FUNCTION

PROGRAM Main
VAR_EXTERNAL Mode : REAL; Count : DINT; END_VAR
VAR r : INT; END_VAR
Mode := 1.5;
Count := 2;
r := Peek();
END_PROGRAM`)
	if got, want := globalsText(p.GlobalsDeep()), "Count:DINT,Mode:REAL"; got != want {
		t.Errorf("GlobalsDeep = %s, want %s (the program's own declarations)", got, want)
	}
	c := p.BindingConflicts()
	if len(c) != 1 || c[0].Name != "Mode" || c[0].First.String() != "REAL" || c[0].Second.String() != "INT" ||
		c[0].FirstPOU != "Main" || c[0].SecondPOU != "Peek" {
		t.Errorf("BindingConflicts = %+v, want Mode REAL in Main vs INT in Peek", c)
	}
}
