package runtime_test

import (
	"reflect"
	"strings"
	"testing"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/runtime"
)

// nautilus#696: a typed output with no init: starts at the zero of its type,
// as a setpoint or state tag does (IEC 61131-3: every variable has an
// initial value). Before, it was absent until a whole-value write created
// it, so an element write (OutArr[1] := 5) faulted "undefined tag" and
// abandoned the rest of the scan.

const zeroSeedLib = `TYPE Motor : STRUCT
  Run : BOOL;
  Speed : REAL;
END_STRUCT; END_TYPE`

const zeroSeedProg = `PROGRAM Main
VAR_EXTERNAL
  OutArr : ARRAY [0..3] OF DINT;
  StArr : ARRAY [0..3] OF DINT;
  OutInt : DINT;
  Cmd : Motor;
  Horn : BOOL;
  Alarm : BOOL;
  Zeroed : DINT;
  Idle : DINT;
  Lamp : BOOL;
  After : DINT;
END_VAR
StArr[1] := 5;
OutInt := 3;
OutArr[1] := 5;
Cmd.Speed := 60.0;
Horn := Horn OR Alarm;
Zeroed := 0;
After := 7;
END_PROGRAM`

func newZeroSeed(t *testing.T, drv nio.Driver, coord runtime.Coordinator) *runtime.Runtime {
	t.Helper()
	rt, err := runtime.New(runtime.Options{
		Program:   zeroSeedProg,
		Libraries: []string{zeroSeedLib},
		Driver:    drv,
		Tags: []runtime.TagDef{
			runtime.Typed("OutArr", runtime.RoleOutput, "ARRAY [0..3] OF DINT"),
			runtime.Typed("StArr", runtime.RoleState, "ARRAY [0..3] OF DINT"),
			runtime.Typed("OutInt", runtime.RoleOutput, "DINT"),
			runtime.Typed("Cmd", runtime.RoleOutput, "Motor"),
			runtime.Typed("Horn", runtime.RoleOutput, "BOOL"), // a seal-in reads it first
			runtime.Typed("Alarm", runtime.RoleState, "BOOL"),
			runtime.Typed("Zeroed", runtime.RoleOutput, "DINT"), // the logic writes its zero
			runtime.Typed("Idle", runtime.RoleOutput, "DINT"),   // nothing in the logic writes it
			runtime.Typed("Lamp", runtime.RoleOutput, "BOOL", runtime.Init(false)),
			runtime.Typed("After", runtime.RoleOutput, "DINT"),
		},
		Coordinator: coord,
	})
	if err != nil {
		t.Fatal(err)
	}
	return rt
}

func ints(t *testing.T, v ir.Value) []int64 {
	t.Helper()
	out := make([]int64, len(v.Arr))
	for i, e := range v.Arr {
		out[i] = e.I
	}
	return out
}

// The issue's repro: element and member writes to typed outputs land, a
// seal-in reads its own typed coil without faulting, and every statement
// after them still runs.
func TestTypedOutputStartsAtZeroSoElementWritesLand(t *testing.T) {
	rt := newZeroSeed(t, &scriptDriver{in: nio.Values{}}, nil)

	// Before any scan the outputs exist at zero, as a state tag does.
	if v, err := rt.Tags().ReadGlobal("OutArr"); err != nil || !reflect.DeepEqual(ints(t, v), []int64{0, 0, 0, 0}) {
		t.Fatalf("OutArr before the first scan = %v, %v; want [0 0 0 0]", v, err)
	}

	rt.Scan()
	if e := rt.Stats().LastError; e != "" {
		t.Fatalf("the scan faulted: %s", e)
	}
	v, err := rt.Tags().ReadGlobal("OutArr")
	if err != nil || !reflect.DeepEqual(ints(t, v), []int64{0, 5, 0, 0}) {
		t.Errorf("OutArr = %v, %v; want [0 5 0 0]", v, err)
	}
	if got, ok := rt.Tags().ReadPath("Cmd.Speed"); !ok || got != 60.0 {
		t.Errorf("Cmd.Speed = %v (%v), want 60", got, ok)
	}
	if got, ok := rt.Tags().ReadPath("Cmd.Run"); !ok || got != false {
		t.Errorf("Cmd.Run = %v (%v), want the zero FALSE", got, ok)
	}
	if got := rt.Tags().Bool("Horn"); got {
		t.Errorf("Horn = %v, want FALSE (seal-in with no alarm)", got)
	}
	if v, err := rt.Tags().ReadGlobal("After"); err != nil || v.I != 7 {
		t.Errorf("After = %v, %v; want 7 — the statements after the element write must run", v, err)
	}
}

// The zero is the store's starting value, not a command: the driver is not
// handed a typed output with no init: until something writes it — the
// logic, its own zero included, or an operator. An output with an init: is
// pushed on the first scan as it always was.
func TestTypedOutputZeroIsNotPushedUntilWritten(t *testing.T) {
	drv := &scriptDriver{in: nio.Values{}}
	rt := newZeroSeed(t, drv, nil)
	rt.Scan()

	drv.mu.Lock()
	out := drv.lastOut
	drv.mu.Unlock()
	if _, ok := out["Idle"]; ok {
		t.Errorf("the first push carried Idle = %v; nothing wrote it, so the driver must not see it", out["Idle"])
	}
	for _, name := range []string{"OutArr", "OutInt", "Cmd", "Horn", "Zeroed", "After"} {
		if _, ok := out[name]; !ok {
			t.Errorf("the first push is missing %s, which the logic wrote", name)
		}
	}
	if got, ok := out["Lamp"]; !ok || got != false {
		t.Errorf("the first push carried Lamp = %v (%v); an init: output is pushed on scan one", got, ok)
	}
	if got := out["Zeroed"]; got != int64(0) {
		t.Errorf("Zeroed pushed as %#v, want int64 0 — the logic wrote the zero it already held", got)
	}

	// An operator write releases it, and the next push carries it.
	rt.Tags().Set("Idle", int64(4))
	rt.Scan()
	drv.mu.Lock()
	out = drv.lastOut
	drv.mu.Unlock()
	if got := out["Idle"]; got != int64(4) {
		t.Errorf("after an operator write the push carried Idle = %#v, want 4", got)
	}
}

// A replica that takes over pushes the full output set on its first scan
// as leader — and still leaves out a typed output nothing has written, so
// a takeover does not drive zero over what the device holds.
func TestTypedOutputZeroIsNotPushedOnTakeover(t *testing.T) {
	drv := &scriptDriver{in: nio.Values{}}
	lead := &leaderFlag{lead: false}
	rt := newZeroSeed(t, drv, lead)
	rt.Scan() // standby: no scan, no push
	if got := drv.writeCount(); got != 0 {
		t.Fatalf("a standby pushed outputs %d times", got)
	}
	lead.lead = true
	rt.Scan()
	drv.mu.Lock()
	out := drv.lastOut
	drv.mu.Unlock()
	if drv.writeCount() != 1 {
		t.Fatalf("the takeover scan pushed %d times, want 1", drv.writeCount())
	}
	if _, ok := out["Idle"]; ok {
		t.Errorf("the takeover push carried Idle = %v; nothing wrote it", out["Idle"])
	}
	if _, ok := out["OutArr"]; !ok {
		t.Error("the takeover push is missing OutArr, which the logic wrote")
	}
}

// Typed INPUTS stay unseeded: a read before the driver delivers still
// faults, which is the whole reason RoleInput exists.
func TestTypedInputStillFaultsUntilDelivered(t *testing.T) {
	rt, err := runtime.New(runtime.Options{
		Program: `PROGRAM Main
VAR_EXTERNAL Level : DINT; Out : DINT; END_VAR
Out := Level;
END_PROGRAM`,
		Driver: &scriptDriver{in: nio.Values{}},
		Tags: []runtime.TagDef{
			runtime.Typed("Level", runtime.RoleInput, "DINT"),
			runtime.Typed("Out", runtime.RoleOutput, "DINT"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	rt.Scan()
	if e := rt.Stats().LastError; !strings.Contains(e, "undefined tag Level") {
		t.Fatalf("LastError = %q, want an undefined tag Level fault", e)
	}
}
