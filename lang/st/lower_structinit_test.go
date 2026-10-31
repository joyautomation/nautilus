package st

import (
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/lang/ir"
)

// A STRUCT member's declared initial value (`Temp : REAL := 72.5;`) was
// parsed and then dropped, so every value of the type started at zero.
// IEC 61131-3 has a variable of a structured type start at its members'
// initial values; the type's default is now those values, recursively.
const structInitTypes = `
TYPE
  Mode : (Idle, Run, Fault);
  Limits : STRUCT
    Lo : REAL := 1.5;
    Hi : REAL := 99.0;
  END_STRUCT;
  Motor : STRUCT
    Running : BOOL;
    Enabled : BOOL := TRUE;
    Temp    : REAL := 72.5;
    Starts  : DINT := 3;
    Tag     : STRING := 'pump';
    State   : Mode := Run;
    Lim     : Limits;
    Bands   : ARRAY[1..2] OF Limits;
  END_STRUCT;
END_TYPE
`

func wantMotorDefaults(t *testing.T, where string, v ir.Value) {
	t.Helper()
	if v.Kind != ir.TypeStruct || len(v.Fld) != 8 {
		t.Fatalf("%s: not a Motor value: %+v", where, v)
	}
	f := v.Fld
	if f[0].B {
		t.Errorf("%s: Running = TRUE, want FALSE (no initial: the zero of BOOL)", where)
	}
	if !f[1].B {
		t.Errorf("%s: Enabled = FALSE, want TRUE", where)
	}
	if f[2].Kind != ir.TypeReal || f[2].F != 72.5 {
		t.Errorf("%s: Temp = %+v, want REAL 72.5", where, f[2])
	}
	if f[3].Kind != ir.TypeInt || f[3].I != 3 {
		t.Errorf("%s: Starts = %+v, want 3", where, f[3])
	}
	if f[4].S != "pump" {
		t.Errorf("%s: Tag = %q, want pump", where, f[4].S)
	}
	if f[5].I != 1 {
		t.Errorf("%s: State = %+v, want Run (1)", where, f[5])
	}
	if lo, hi := f[6].Fld[0].F, f[6].Fld[1].F; lo != 1.5 || hi != 99 {
		t.Errorf("%s: Lim = %v..%v, want the nested type's 1.5..99", where, lo, hi)
	}
	for i, b := range f[7].Arr {
		if lo, hi := b.Fld[0].F, b.Fld[1].F; lo != 1.5 || hi != 99 {
			t.Errorf("%s: Bands[%d] = %v..%v, want 1.5..99 per element", where, i+1, lo, hi)
		}
	}
}

func TestStructMemberInitial_TypeDefault(t *testing.T) {
	prog, err := Parse(structInitTypes)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	types, err := Types(prog)
	if err != nil {
		t.Fatalf("types: %v", err)
	}
	// What a manifest tag of the type is seeded with (runtime expandTags).
	wantMotorDefaults(t, "Zero(Motor)", ir.Zero(types["Motor"]))
	// A manifest init: overrides member by member; the rest keep the
	// type's initial values.
	v, err := ir.SeedFromInit(types["Motor"], map[string]any{"Temp": 10.0, "Lim": map[string]any{"Hi": 50.0}})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if v.Fld[2].F != 10 || v.Fld[3].I != 3 || v.Fld[6].Fld[0].F != 1.5 || v.Fld[6].Fld[1].F != 50 {
		t.Fatalf("SeedFromInit: Temp=%v Starts=%v Lim=%v..%v, want 10, 3, 1.5..50",
			v.Fld[2].F, v.Fld[3].I, v.Fld[6].Fld[0].F, v.Fld[6].Fld[1].F)
	}
}

func TestStructMemberInitial_ProgramVars(t *testing.T) {
	src := structInitTypes + `
FUNCTION_BLOCK Holder
VAR m : Motor; END_VAR
VAR_OUTPUT T : REAL; END_VAR
T := m.Temp;
END_FUNCTION_BLOCK

PROGRAM Main
VAR
  m  : Motor;
  ms : ARRAY[0..1] OF Motor;
  h  : Holder;
END_VAR
VAR_TEMP tmp : Motor; END_VAR
VAR_EXTERNAL Seen, FromFB, FromArr : REAL; END_VAR
Seen := tmp.Temp;
tmp.Temp := 0.0;
h();
FromFB := h.T;
FromArr := ms[1].Temp;
END_PROGRAM
`
	prog, err := Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	irProg, err := Lower(prog)
	if err != nil {
		t.Fatalf("lower: %v", err)
	}
	frame := ir.NewFrame(irProg)
	wantMotorDefaults(t, "VAR m", frame.Slots[irProg.SlotIndex["m"]])
	for i, e := range frame.Slots[irProg.SlotIndex["ms"]].Arr {
		wantMotorDefaults(t, "ms["+string(rune('0'+i))+"]", e)
	}
	host := newFakeHost()
	for i := 0; i < 2; i++ {
		if err := ir.Run(irProg, frame, host); err != nil {
			t.Fatalf("run: %v", err)
		}
		// VAR_TEMP is put back to the type's initial values every scan.
		for _, name := range []string{"Seen", "FromFB", "FromArr"} {
			if got := host.vals[name]; got.F != 72.5 {
				t.Fatalf("scan %d: %s = %+v, want 72.5", i, name, got)
			}
		}
	}
}

func TestStructMemberInitial_BadValue(t *testing.T) {
	src := `
TYPE
  Motor : STRUCT
    Temp : REAL := 'hot';
  END_STRUCT;
END_TYPE
`
	prog, err := Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	_, err = Types(prog)
	if err == nil || !strings.Contains(err.Error(), "Motor") || !strings.Contains(err.Error(), "Temp") {
		t.Fatalf("err = %v, want one naming Motor and Temp", err)
	}
}
