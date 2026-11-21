package runtime

import (
	"errors"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/retain"
)

func fp(f float64) *float64 { return &f }

// paramRT is a controller with one ranged parameter, SP (init 65, range
// 10..90), a program that can drive it past the range, and a store.
func paramRT(t *testing.T, store retain.Store, init float64) *Runtime {
	t.Helper()
	rt, err := New(Options{
		Program: `PROGRAM P
VAR_EXTERNAL SP : REAL; Push : BOOL; END_VAR
IF Push THEN SP := 500.0; END_IF;
END_PROGRAM`,
		Tags: []TagDef{
			Setpoint("SP", init, Unit("%"), Range(fp(10), fp(90))),
			Setpoint("Free", 1.0),
			State("Push", false),
		},
		Retain: store,
	})
	if err != nil {
		t.Fatal(err)
	}
	return rt
}

func param(t *testing.T, rt *Runtime, name string) Parameter {
	t.Helper()
	for _, p := range rt.Parameters() {
		if p.Name == name {
			return p
		}
	}
	t.Fatalf("no parameter %s in %v", name, rt.Parameters())
	return Parameter{}
}

// A range binds external writes — the one front door every HMI, API,
// Sparkplug and OPC UA write goes through — inclusively, and the refusal
// says what the range is and is an ir.ErrOutOfRange so each protocol maps it.
func TestExternalWriteRefusedOutsideRange(t *testing.T) {
	rt := paramRT(t, nil, 65)
	rt.Scan()
	for _, c := range []struct {
		v  any
		ok bool
	}{{10.0, true}, {90.0, true}, {50, true}, {int64(51), true}, {9.999, false}, {90.001, false}, {-1.0, false}, {1000, false}} {
		err := rt.Tags().Write([]WriteOp{{Path: "SP", Value: c.v}})[0]
		if c.ok != (err == nil) {
			t.Errorf("write %v: err = %v, want ok=%v", c.v, err, c.ok)
		}
		if err != nil {
			if !errors.Is(err, ir.ErrOutOfRange) || !strings.Contains(err.Error(), "[10, 90]") {
				t.Errorf("write %v: %q: want ErrOutOfRange naming [10, 90]", c.v, err)
			}
		}
	}
	// Refused means unchanged; an unranged setpoint takes anything.
	if err := rt.Tags().Write([]WriteOp{{Path: "SP", Value: 12.5}})[0]; err != nil {
		t.Fatal(err)
	}
	_ = rt.Tags().Write([]WriteOp{{Path: "SP", Value: 99.0}})
	if got := rt.Tags().Real("SP"); got != 12.5 {
		t.Errorf("SP = %v after a refused write, want 12.5", got)
	}
	if err := rt.Tags().Write([]WriteOp{{Path: "Free", Value: 1e9}})[0]; err != nil {
		t.Errorf("an unranged setpoint refused: %v", err)
	}
	// A case-insensitive spelling is the same tag.
	if err := rt.Tags().Write([]WriteOp{{Path: "sp", Value: 99.0}})[0]; err == nil {
		t.Error("the range was bypassed by another spelling of the name")
	}
}

// The range never touches the program: its own write outside the range
// lands (clamping or refusing it would change control behaviour), and is
// shown as outOfRange in the parameter listing instead.
func TestProgramWriteIsNeverRefusedByRange(t *testing.T) {
	rt := paramRT(t, nil, 65)
	rt.Scan()
	if p := param(t, rt, "SP"); p.OutOfRange {
		t.Fatal("65 flagged out of range")
	}
	if err := rt.Tags().Write([]WriteOp{{Path: "Push", Value: true}})[0]; err != nil {
		t.Fatal(err)
	}
	rt.Scan()
	if got := rt.Tags().Real("SP"); got != 500 {
		t.Fatalf("SP = %v, the program's own write of 500 must land", got)
	}
	if p := param(t, rt, "SP"); !p.OutOfRange || p.Min == nil || *p.Min != 10 || *p.Max != 90 {
		t.Errorf("listing = %+v, want outOfRange with the range", p)
	}
}

func TestParameterState(t *testing.T) {
	for _, c := range []struct {
		cur, init, base any
		want            string
	}{
		{65.0, 65.0, 65.0, ParamInStep},
		{70.0, 65.0, 65.0, ParamPlantAhead},
		{65.0, 70.0, 65.0, ParamRepoAhead}, // a redeploy moved init; retained value shadows it
		{80.0, 70.0, 65.0, ParamConflict},
		{70.0, 70.0, 65.0, ParamInStep}, // both moved to the same value
		{int64(65), 65.0, 65.0, ParamInStep},
		{true, false, false, ParamPlantAhead},
	} {
		if got := paramState(c.cur, c.init, c.base); got != c.want {
			t.Errorf("state(cur=%v init=%v base=%v) = %s, want %s", c.cur, c.init, c.base, got, c.want)
		}
	}
}

// Drift end to end through the store: an operator edit is plant-ahead and
// survives a restart as such; a redeploy whose init moved while the plant
// held the old value is repo-ahead (retained still wins); one whose init
// moved while the plant had also moved is a conflict; a redeploy that
// commits the plant's value is in step, and the base follows.
func TestParameterDriftAcrossRestartAndRedeploy(t *testing.T) {
	store := &fakeStore{}
	rt := paramRT(t, store, 65)
	rt.Scan()
	rt.saveRetained(nil)
	if p := param(t, rt, "SP"); p.State != ParamInStep {
		t.Fatalf("fresh: %+v", p)
	}
	// An operator edit.
	if err := rt.Tags().Write([]WriteOp{{Path: "SP", Value: 70.0}})[0]; err != nil {
		t.Fatal(err)
	}
	rt.saveRetained(nil)
	if b := store.state.ParamBases["SP"]; b != 65.0 {
		t.Fatalf("base = %v, want the init 65 it was written over", b)
	}
	if p := param(t, rt, "SP"); p.State != ParamPlantAhead {
		t.Fatalf("after edit: %+v", p)
	}
	// Restart, same manifest: retained wins, still plant-ahead.
	rt = paramRT(t, store, 65)
	rt.LoadRetained()
	rt.Scan()
	if p := param(t, rt, "SP"); p.Value != 70.0 || p.State != ParamPlantAhead {
		t.Fatalf("restart: %+v", p)
	}
	// Redeploy, init moved to 40: the plant (70) is neither the old init nor
	// the new one, so both moved.
	rt = paramRT(t, store, 40)
	rt.LoadRetained()
	rt.Scan()
	if p := param(t, rt, "SP"); p.Value != 70.0 || p.State != ParamConflict {
		t.Fatalf("redeploy with a moved init: %+v, want retained 70 kept and a conflict", p)
	}
	// Redeploy that commits the plant's value: in step, base follows.
	rt = paramRT(t, store, 70)
	rt.LoadRetained()
	rt.Scan()
	rt.saveRetained(nil)
	if p := param(t, rt, "SP"); p.State != ParamInStep {
		t.Fatalf("committed: %+v", p)
	}
	if b := store.state.ParamBases["SP"]; b != 70.0 {
		t.Fatalf("base = %v after the repo caught up, want 70", b)
	}
	// Repo-ahead: the plant sits on the value the old init gave and a
	// redeploy moves the init.
	rt = paramRT(t, store, 55)
	rt.LoadRetained()
	rt.Scan()
	if p := param(t, rt, "SP"); p.Value != 70.0 || p.State != ParamRepoAhead {
		t.Fatalf("repo moved only: %+v", p)
	}
}

// A store from before parameters has no bases: the plant is then taken to
// have started at the manifest's init.
func TestParameterWithNoRecordedBase(t *testing.T) {
	store := &fakeStore{state: retain.State{Tags: map[string]any{"SP": 70.0}}}
	rt := paramRT(t, store, 65)
	rt.LoadRetained()
	rt.Scan()
	if p := param(t, rt, "SP"); p.State != ParamPlantAhead || p.Base != 65.0 {
		t.Fatalf("%+v", p)
	}
}

// Only scalar setpoints with an init are parameters.
func TestParametersAreScalarSetpoints(t *testing.T) {
	rt := paramRT(t, nil, 65)
	rt.Scan()
	names := map[string]bool{}
	for _, p := range rt.Parameters() {
		names[p.Name] = true
	}
	if !names["SP"] || !names["Free"] || names["Push"] {
		t.Fatalf("parameters = %v, want SP and Free but not the state tag Push", names)
	}
}
