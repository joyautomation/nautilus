package runtime_test

import (
	"testing"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/runtime"
)

// A FUNCTION_BLOCK whose only pin is `VAR_IN_OUT M : Motor` — the shape a
// site program wants when a UDT already names every value the block needs.
// Bound to a UDT tag it round-trips through the tag store: the block's
// writes are in the store when the scan ends, and it reads them back on the
// next scan.
const structPinLib = `
TYPE
  Motor : STRUCT
    Cmd     : BOOL;
    Running : BOOL;
    Starts  : INT;
    Speed   : REAL;
  END_STRUCT;
END_TYPE

FUNCTION_BLOCK FB_Starter
VAR_IN_OUT
    M : Motor;
END_VAR
VAR
    edge : R_TRIG;
END_VAR
edge(CLK := M.Cmd);
IF edge.Q THEN
    M.Starts := M.Starts + 1;
END_IF;
M.Running := M.Cmd;
IF M.Running THEN M.Speed := 60.0; ELSE M.Speed := 0.0; END_IF;
END_FUNCTION_BLOCK
`

const structPinProg = `
PROGRAM Main
VAR_EXTERNAL
    P101    : Motor;
    StartCmd : BOOL;
END_VAR
VAR s : FB_Starter; END_VAR
(* Writing a FIELD of a VAR_EXTERNAL struct: the tag store holds the whole
   aggregate, so the VM read-modify-writes it. *)
P101.Cmd := StartCmd;
s(M := P101);
END_PROGRAM
`

func TestVarInOutStructTagRoundTrip(t *testing.T) {
	rt, err := runtime.New(runtime.Options{
		Program:   structPinProg,
		Libraries: []string{structPinLib},
		Driver:    nio.NewMemory(),
		Tags: []runtime.TagDef{
			runtime.Typed("P101", runtime.RoleState, "Motor"),
			runtime.State("StartCmd", false),
		},
	})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	rt.Scan()
	if v, _ := rt.Tags().ReadGlobal("P101"); v.Fld[1].B {
		t.Fatal("motor should be stopped before any command")
	}

	rt.Tags().WriteGlobal("StartCmd", ir.BoolVal(true))
	rt.Scan()
	rt.Scan()
	v, err := rt.Tags().ReadGlobal("P101")
	if err != nil {
		t.Fatalf("read P101: %v", err)
	}
	if !v.Fld[1].B {
		t.Fatal("P101.Running should be TRUE in the tag store after the call")
	}
	if v.Fld[2].I != 1 {
		t.Fatalf("P101.Starts = %d, want 1 — the block must see its own write-back", v.Fld[2].I)
	}
	if v.Fld[3].F != 60 {
		t.Fatalf("P101.Speed = %v, want 60", v.Fld[3].F)
	}

	// Stop, then start again: a second start only counts if the first
	// write-back actually landed in the store.
	rt.Tags().WriteGlobal("StartCmd", ir.BoolVal(false))
	rt.Scan()
	rt.Tags().WriteGlobal("StartCmd", ir.BoolVal(true))
	rt.Scan()
	v, _ = rt.Tags().ReadGlobal("P101")
	if v.Fld[2].I != 2 {
		t.Fatalf("P101.Starts = %d after a restart, want 2", v.Fld[2].I)
	}
	if v.Fld[0].B != true {
		t.Fatalf("P101.Cmd = %v, want TRUE — the field write must survive the block's write-back", v.Fld[0].B)
	}
}
