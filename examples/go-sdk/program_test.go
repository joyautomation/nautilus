package main

import (
	"testing"
	"time"

	"github.com/joyautomation/nautilus/acceptance"
	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/runtime"
)

// The Go-SDK tier's acceptance test: no `*_test.yaml`, just Go, driven in
// virtual time by the acceptance package's Scheduler — the same harness
// `naut test` runs against a manifest project's YAML suites. A ten-minute
// thermal settling time elapses in milliseconds here, identically on every
// run and every machine.

// tags is the same declaration main.go passes to runtime.New — repeated
// here (rather than shared) because a test and its production wiring
// drifting apart silently is worse than one small duplication that a
// compile error catches instantly if it goes stale.
var tags = []runtime.TagDef{
	runtime.Input("LevelPct"),
	runtime.Input("TempC"),
	runtime.Setpoint("TempSP", 65.0),
	runtime.Setpoint("Kp", 12.0),
	runtime.Setpoint("Ki", 0.15),
	runtime.Setpoint("PumpStartLevel", 40.0),
	runtime.Setpoint("PumpStopLevel", 75.0),
	runtime.Output("PumpRun", runtime.Init(false)),
	runtime.Output("Heater"),
}

// newRT builds a resource on a fresh virtual clock. drv is the driver to
// scan against: a *Plant for a closed-loop test, or an *nio.Memory stub
// (like acceptance.RunSuite always substitutes) to drive the field inputs
// by hand and test the logic in isolation from the physics.
func newRT(t *testing.T, drv nio.Driver) (*runtime.Runtime, *acceptance.Scheduler) {
	t.Helper()
	clk := acceptance.NewClock()
	if p, ok := drv.(*Plant); ok {
		// Sync the plant's own Euler integration to the same virtual
		// clock the scheduler advances — otherwise its dt would come
		// from the real wall-clock microseconds a test loop actually
		// takes, and the tank would barely move no matter how far
		// virtual time is advanced.
		p.WithClock(clk)
	}
	rt, err := runtime.New(runtime.Options{
		Program: program,
		Driver:  drv,
		Scan:    100 * time.Millisecond,
		DtTag:   "ScanDtS",
		Clock:   clk,
		Tags:    tags,
	})
	if err != nil {
		t.Fatal(err)
	}
	return rt, acceptance.NewScheduler(rt, clk)
}

// TestPumpSealIn drives LevelPct directly through a stub driver: a
// combinational question ("is the latch in the right state") needs scans,
// not virtual duration.
func TestPumpSealIn(t *testing.T) {
	drv := nio.NewMemory()
	rt, sch := newRT(t, drv)

	for _, step := range []struct {
		level float64
		want  bool
		why   string
	}{
		{35.0, true, "starts below PumpStartLevel"},
		{60.0, true, "seals in between the bands"},
		{80.0, false, "drops out above PumpStopLevel"},
		{60.0, false, "stays out between the bands (hysteresis)"},
	} {
		if err := drv.WriteOutputs(nio.Values{"LevelPct": step.level, "TempC": 60.0}); err != nil {
			t.Fatal(err)
		}
		if err := sch.Scans(1); err != nil {
			t.Fatal(err)
		}
		if got := rt.Tags().Bool("PumpRun"); got != step.want {
			t.Fatalf("LevelPct=%.0f: PumpRun = %v, want %v (%s)", step.level, got, step.want, step.why)
		}
	}
	noFaults(t, sch)
}

// TestPISettles runs the closed loop against the real Plant: cold start to
// the seeded 65°C setpoint, then a step to 72°C, both required to settle
// AND stay settled — a loop that merely passes through its target on the
// way to an overshoot has not settled. The level is frozen mid-band (the
// pump's own seal-in is proven separately, by TestPumpSealIn) so the
// cold-inflow mixing a running pump would inject doesn't fight the
// temperature loop this test is about.
func TestPISettles(t *testing.T) {
	rt, sch := newRT(t, NewPlant().Freeze(55.0))

	settled := func(sp float64) func() bool {
		return func() bool {
			e := rt.Tags().Real("TempC") - sp
			return e > -0.5 && e < 0.5
		}
	}

	if ok, _ := sch.AdvanceUntil(time.Hour, 5*time.Second, settled(65.0)); !ok {
		t.Fatalf("never reached the initial setpoint: TempC = %.3f, want 65.0 ± 0.5", rt.Tags().Real("TempC"))
	}
	t.Logf("cold start settled at t=%v", sch.Elapsed())

	stepAt := sch.Elapsed()
	rt.Tags().SetReal("TempSP", 72.0)
	if ok, at := sch.AdvanceUntil(time.Hour, 5*time.Second, settled(72.0)); !ok {
		t.Fatalf("never settled within an hour of the step: TempC = %.3f, want 72.0 ± 0.5", rt.Tags().Real("TempC"))
	} else {
		t.Logf("step settled %v after it", at-stepAt)
	}

	// Anti-windup: the integral clamp means the heater saturates on the
	// way up and backs off cleanly, rather than winding up and overshooting.
	if h := rt.Tags().Real("Heater"); h < 0 || h > 100 {
		t.Fatalf("Heater outside its 0-100 clamp: %.3f", h)
	}
	noFaults(t, sch)
}

// noFaults is the assertion you never have to write by hand: a scan that
// errors fails the test that ran it.
func noFaults(t *testing.T, sch *acceptance.Scheduler) {
	t.Helper()
	if n, last := sch.LogicErrors(); n > 0 {
		t.Fatalf("%d logic error(s) during the run, last: %s", n, last)
	}
}
