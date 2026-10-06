package runtime_test

import (
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/runtime"
)

// #197: tag names are IEC identifiers, so case-insensitive. A program that
// cases a manifest tag differently binds the SAME tag (no second tag
// appears), two POUs casing one tag differently share one value within a
// scan, the store shows the declared spelling, and reads/writes by any
// casing land on it.
func TestTagNamesAreCaseInsensitive(t *testing.T) {
	mainProg := `PROGRAM Main
VAR_EXTERNAL level : REAL; DOUBLED : REAL; motor : Motor; END_VAR
VAR f : Halver; END_VAR
DOUBLED := LEVEL * 2.0;
f();
MOTOR.speed := Level;
END_PROGRAM`
	lib := `TYPE Motor : STRUCT Speed : REAL; END_STRUCT END_TYPE
FUNCTION_BLOCK Halver
VAR_EXTERNAL LEVEL : REAL; halved : REAL; END_VAR
Halved := level / 2.0;
END_FUNCTION_BLOCK`
	other := `PROGRAM Other
VAR_EXTERNAL Doubled : REAL; TRIPLED : REAL; END_VAR
Tripled := doubled * 1.5;
END_PROGRAM`
	drv := nio.NewMemory()
	_ = drv.WriteOutputs(nio.Values{"Level": 4.0})
	rt, err := runtime.New(runtime.Options{
		Program:   mainProg,
		Libraries: []string{lib},
		Driver:    drv,
		Tags: []runtime.TagDef{
			runtime.Input("Level"),
			runtime.State("Doubled", 0.0),
			runtime.State("Halved", 0.0),
			runtime.State("Tripled", 0.0),
			runtime.Typed("Motor", runtime.RoleState, "MOTOR"),
		},
		Tasks: []runtime.Task{{Name: "other", Program: other, Scan: time.Second}},
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	rt.Scan()
	if err := rt.ScanTask("other"); err != nil {
		t.Fatal(err)
	}
	snap := rt.Tags().Snapshot()
	for _, want := range []string{"Level", "Doubled", "Halved", "Tripled", "Motor"} {
		if _, ok := snap[want]; !ok {
			t.Errorf("store lacks the declared spelling %q: %v", want, keys(snap))
		}
	}
	if len(snap) != 5 {
		t.Errorf("a casing variant became a second tag: %v", keys(snap))
	}
	for name, want := range map[string]float64{"Doubled": 8, "halved": 2, "TRIPLED": 12} {
		if got := rt.Tags().Real(name); got != want {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}
	if v, ok := rt.Tags().ReadPath("motor.SPEED"); !ok || v != 4.0 {
		t.Errorf("ReadPath(motor.SPEED) = %v, %v; want 4", v, ok)
	}

	// Operator writes by another casing land on the declared tag.
	if err := rt.Tags().SetPath("MOTOR.speed", 7.5); err != nil {
		t.Fatal(err)
	}
	rt.Tags().Set("doubled", 1.0)
	if got := rt.Tags().Real("Doubled"); got != 1 {
		t.Errorf("Set(doubled) did not reach Doubled: %v", got)
	}
	if v, _ := rt.Tags().ReadPath("Motor.Speed"); v != 7.5 {
		t.Errorf("SetPath(MOTOR.speed) did not reach Motor.Speed: %v", v)
	}
	if name, ok := rt.Tags().Canonical("tripled"); !ok || name != "Tripled" {
		t.Errorf("Canonical(tripled) = %q, %v", name, ok)
	}

	// Forces name the declared address, and unforce takes any casing.
	if err := rt.Tags().Force("motor.speed", 3.0); err != nil {
		t.Fatal(err)
	}
	if fs := rt.Tags().Forces(); len(fs) != 1 || fs[0].Name != "Motor.Speed" {
		t.Errorf("force entry = %+v, want Motor.Speed", fs)
	}
	if f := rt.Tags().ForcedOverlap("MOTOR"); f != "Motor.Speed" {
		t.Errorf("ForcedOverlap(MOTOR) = %q", f)
	}
	if !rt.Tags().Unforce("MOTOR.SPEED") {
		t.Error("Unforce by another casing found nothing")
	}
	if len(snap) != 5 || len(rt.Tags().Snapshot()) != 5 {
		t.Errorf("writes by another casing created tags: %v", keys(rt.Tags().Snapshot()))
	}
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
