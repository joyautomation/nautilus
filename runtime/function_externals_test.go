package runtime_test

import (
	"testing"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/runtime"
)

// funcIOProg reaches every tag through a user FUNCTION's VAR_EXTERNAL; the
// PROGRAM itself binds none of them.
const funcIOProg = `FUNCTION ScaleCmd : BOOL
VAR_INPUT k : REAL; END_VAR
VAR_EXTERNAL RawLevel : REAL; PumpCmd : REAL; Mode : INT; Starts : DINT; END_VAR
PumpCmd := RawLevel * k + INT_TO_REAL(Mode);
Starts := Starts + 1;
ScaleCmd := TRUE;
END_FUNCTION

PROGRAM Main
VAR ok : BOOL; END_VAR
ok := ScaleCmd(2.0);
END_PROGRAM`

// A tag bound only inside a user FUNCTION is the calling program's tag, as
// one the PROGRAM names itself would be (nautilus#590): the scan reads the
// input's current value, drives the output, seeds a state tag with no init:
// or type: at the zero of the FUNCTION's declared type, and seeds an init:
// as that type.
func TestTagsBoundOnlyInAFunction(t *testing.T) {
	drv := nio.NewMemory()
	_ = drv.WriteOutputs(nio.Values{"RawLevel": 5.0})
	rt, err := runtime.New(runtime.Options{
		Program: funcIOProg, Driver: drv,
		Tags: []runtime.TagDef{
			runtime.Input("RawLevel"),
			runtime.Output("PumpCmd"),
			{Name: "Mode", Role: runtime.RoleState}, // a GVL-style global: no init:, no type:
			runtime.State("Starts", 0.0),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"RawLevel": "REAL", "PumpCmd": "REAL", "Mode": "INT", "Starts": "DINT"} {
		if got := rt.Globals()[name]; got == nil || got.String() != want {
			t.Errorf("Globals()[%s] = %v, want %s", name, got, want)
		}
		if got := rt.TagTypes()[name]; got == nil || got.String() != want {
			t.Errorf("TagTypes()[%s] = %v, want %s", name, got, want)
		}
	}
	if v, err := rt.Tags().ReadGlobal("Starts"); err != nil || v.Kind != ir.TypeInt {
		t.Errorf("Starts seeded as %v (%v), want the DINT the FUNCTION declares", v.Kind, err)
	}

	rt.Scan()
	if e := rt.Stats().LastError; e != "" {
		t.Fatalf("scan faulted: %s", e)
	}
	out, _ := drv.ReadInputs()
	if out["PumpCmd"] != 10.0 {
		t.Errorf("PumpCmd = %v after RawLevel 5, want 10", out["PumpCmd"])
	}

	_ = drv.WriteOutputs(nio.Values{"RawLevel": 7.0})
	rt.Scan()
	out, _ = drv.ReadInputs()
	if out["PumpCmd"] != 14.0 {
		t.Errorf("PumpCmd = %v after RawLevel 7, want 14 (the input's current value)", out["PumpCmd"])
	}
	if v, _ := rt.Tags().ReadGlobal("Starts"); v.Kind != ir.TypeInt || v.I != 2 {
		t.Errorf("Starts = %+v after two scans, want DINT 2", v)
	}
}
