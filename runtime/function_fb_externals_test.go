package runtime_test

import (
	"strings"
	"testing"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/runtime"
)

// A tag bound only by an FB instance that a FUNCTION declares is the
// calling program's (nautilus#590): seeded from its declared type, so the
// scan reads it instead of faulting "undefined tag".
func TestTagBoundByAnFBInsideAFunction(t *testing.T) {
	rt, err := runtime.New(runtime.Options{
		Program: `FUNCTION_BLOCK Holder
VAR_EXTERNAL Mode : INT; END_VAR
VAR_OUTPUT Q : BOOL; END_VAR
Q := Mode > 0;
END_FUNCTION_BLOCK

FUNCTION Wrap : BOOL
VAR h : Holder; END_VAR
h();
Wrap := h.Q;
END_FUNCTION

PROGRAM Main
VAR_EXTERNAL Lamp : BOOL; END_VAR
Lamp := Wrap();
END_PROGRAM`,
		Driver: nio.NewMemory(),
		Tags: []runtime.TagDef{
			{Name: "Mode", Role: runtime.RoleState}, // no init:, no type:
			runtime.Output("Lamp", runtime.Init(false)),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := rt.Globals()["Mode"]; got == nil || got.Kind != ir.TypeInt {
		t.Errorf("Globals()[Mode] = %v, want INT", got)
	}
	rt.Scan()
	if e := rt.Stats().LastError; e != "" {
		t.Fatalf("scan faulted: %s", e)
	}
	if err := rt.Tags().WriteGlobal("Mode", ir.IntVal(3)); err != nil {
		t.Fatal(err)
	}
	rt.Scan()
	if v, _ := rt.Tags().ReadGlobal("Lamp"); !v.B {
		t.Errorf("Lamp = %+v with Mode 3, want TRUE", v)
	}
}

// The PROGRAM and a FUNCTION it calls declaring one tag with different
// types is a startup error naming both: the store holds one value, and one
// of them would read it wrong.
func TestFunctionDeclaringATagDifferentlyIsAnError(t *testing.T) {
	_, err := runtime.New(runtime.Options{
		Program: `FUNCTION Peek : INT
VAR_EXTERNAL Mode : INT; END_VAR
Peek := Mode;
END_FUNCTION

PROGRAM Main
VAR_EXTERNAL Mode : REAL; END_VAR
VAR r : INT; END_VAR
Mode := 1.5;
r := Peek();
END_PROGRAM`,
		Driver: nio.NewMemory(),
		Tags:   []runtime.TagDef{runtime.State("Mode", 0.0)},
	})
	if err == nil || !strings.Contains(err.Error(), "tag Mode is REAL in Main but INT in Peek") {
		t.Errorf("err = %v, want the REAL/INT conflict between Main and Peek", err)
	}
}
