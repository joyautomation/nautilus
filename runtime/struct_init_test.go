package runtime_test

import (
	"testing"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/runtime"
)

// A manifest tag of a STRUCT type starts at the TYPE's member initial
// values (`Temp : REAL := 72.5;`); a manifest init: overrides them member
// by member. Before the fix the tag was seeded with zeros and the type's
// initial values were dropped, so P101/Temp was born 0.
const motorInitLib = `
TYPE
  Motor : STRUCT
    Running : BOOL;
    Speed   : REAL;
    Starts  : DINT;
    Temp    : REAL := 72.5;
  END_STRUCT;
END_TYPE
`

const motorInitProg = `
PROGRAM Main
VAR_EXTERNAL P101 : Motor; P102 : Motor; END_VAR
END_PROGRAM
`

func TestStructTagTakesTypeInitialValues(t *testing.T) {
	rt, err := runtime.New(runtime.Options{
		Program:   motorInitProg,
		Libraries: []string{motorInitLib},
		Driver:    nio.NewMemory(),
		Tags: []runtime.TagDef{
			runtime.Typed("P101", runtime.RoleState, "Motor"),
			func() runtime.TagDef {
				d := runtime.Typed("P102", runtime.RoleState, "Motor")
				d.Init = map[string]any{"Temp": 60.0, "Starts": 4}
				return d
			}(),
		},
	})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	// Before any scan: the seed itself, which is what the NBIRTH carries.
	p101, err := rt.Tags().ReadGlobal("P101")
	if err != nil {
		t.Fatalf("read P101: %v", err)
	}
	if got := p101.Fld[3]; got.F != 72.5 {
		t.Fatalf("P101.Temp = %+v, want 72.5 from the TYPE", got)
	}
	p102, _ := rt.Tags().ReadGlobal("P102")
	if p102.Fld[3].F != 60 || p102.Fld[2].I != 4 {
		t.Fatalf("P102 Temp=%v Starts=%v, want the manifest's 60 and 4", p102.Fld[3].F, p102.Fld[2].I)
	}
	rt.Scan()
	if v, _ := rt.Tags().ReadGlobal("P101"); v.Fld[3].F != 72.5 {
		t.Fatalf("P101.Temp after a scan = %v, want 72.5", v.Fld[3].F)
	}
}
