package acceptance_test

// The acceptance bar for virtual time, against examples/heated-tank-nogo:
// four tasks at four rates, three IEC languages, and the plant itself
// simulated in ST. These are the assertions that were impossible before —
// a ten-second on-delay and a PI loop's settling time — and they now run
// in microseconds, identically on every machine.
//
// The fixture is the manifest. Nothing here restates a scan rate, a seed,
// or a tag role, so no drift between what is tested and what deploys.

import (
	"os"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/acceptance"
	"github.com/joyautomation/nautilus/internal/project"
	"github.com/joyautomation/nautilus/runtime"
)

const nogo = "../examples/heated-tank-nogo"

// load builds the example exactly as `nautilus run` would, then puts it on
// a virtual clock.
func load(t *testing.T) (*runtime.Runtime, *acceptance.Scheduler) {
	t.Helper()
	proj, err := project.Load(os.DirFS(nogo), "")
	if err != nil {
		t.Fatalf("load %s: %v", nogo, err)
	}
	rt, sch, err := acceptance.NewRuntime(proj.Runtime)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return rt, sch
}

// noFaults is the assertion you never have to write: a scan that errors
// fails the test that ran it.
func noFaults(t *testing.T, sch *acceptance.Scheduler) {
	t.Helper()
	if n, last := sch.LogicErrors(); n > 0 {
		t.Fatalf("%d logic error(s) during the run, last: %s", n, last)
	}
}

// TAL-101 delays the low-temperature alarm by a full ten seconds. This is
// the assertion §1 of the design brief called impossible: at wall-clock
// speed, a hundred scans is a few hundred microseconds of process time.
func TestLowTempAlarmWaitsItsFullDelay(t *testing.T) {
	rt, sch := load(t)
	// Freeze the plant — this test owns the temperature.
	if err := sch.Suspend("sim"); err != nil {
		t.Fatal(err)
	}
	rt.Tags().SetReal("TempC", 45.0) // below the 62 °C cold threshold

	sch.Advance(9500 * time.Millisecond)
	if rt.Tags().Bool("TempLowAlm") {
		t.Fatalf("alarm tripped early, at t=%v (PT is 10s)", sch.Elapsed())
	}

	sch.Advance(time.Second)
	if !rt.Tags().Bool("TempLowAlm") {
		t.Fatalf("alarm never tripped; still clear at t=%v", sch.Elapsed())
	}

	// TON has no off-delay: warm up and it drops out on the next scan.
	rt.Tags().SetReal("TempC", 70.0)
	if err := sch.Scans(1); err != nil {
		t.Fatal(err)
	}
	if rt.Tags().Bool("TempLowAlm") {
		t.Fatal("alarm should clear on the first scan above the threshold")
	}
	noFaults(t, sch)
}

// TIC-101 settles a setpoint step. Closed loop: sim.st integrates the
// plant at 100 ms while the PI runs at 100 ms, both on virtual dt, so the
// whole trajectory is deterministic. The measured response is ~32 s.
func TestPISettlesSetpointStep(t *testing.T) {
	rt, sch := load(t)
	settled := func() bool {
		e := rt.Tags().Real("TempC") - rt.Tags().Real("TempSP")
		return e > -0.5 && e < 0.5
	}

	// Reach steady state at the 65 °C setpoint the manifest seeds. The cold
	// start is far slower than the step below — Ki is 0.15, so the integral
	// has to climb from zero all the way to the ~75 % the plant needs to
	// hold 65 °C. That asymmetry is a property of the loop, not of the
	// harness, and it is exactly the kind of thing nobody measures until a
	// test makes it cheap to look at.
	if ok, _ := sch.AdvanceUntil(300*time.Second, 0, settled); !ok {
		t.Fatalf("never reached the initial setpoint: TempC = %.3f, want 65.0 ± 0.5", rt.Tags().Real("TempC"))
	}
	t.Logf("cold start settled at t=%v", sch.Elapsed())

	// Now the step. Require it to settle AND stay settled: a loop that
	// merely passes through its target on the way to an overshoot has not
	// settled, and a bare "assert after 45 s" cannot tell the difference.
	stepAt := sch.Elapsed()
	rt.Tags().SetReal("TempSP", 72.0)
	ok, at := sch.AdvanceUntil(45*time.Second, 5*time.Second, settled)
	if !ok {
		t.Fatalf("never settled within 45s of the step: TempC = %.3f, want 72.0 ± 0.5",
			rt.Tags().Real("TempC"))
	}
	t.Logf("step settled %v after it, then held 5s", at-stepAt)

	// Anti-windup: the integral clamp means the heater saturates on the way
	// up and backs off cleanly, rather than winding up and overshooting.
	if h := rt.Tags().Real("Heater"); h < 0 || h > 100 {
		t.Fatalf("Heater outside its 0–100 clamp: %.3f", h)
	}
	if peak := rt.Tags().Real("TempC"); peak > 72.5 {
		t.Fatalf("overshot: TempC = %.3f after settling, want no more than 72.5", peak)
	}
	noFaults(t, sch)
}

// The annunciator in ladder, on its own 200 ms task: a 5 s on-delay, the
// horn, and the ack that releases itself once both alarms clear. Proves
// virtual time reaches a task that is neither main nor the one under
// direct test.
func TestHighTempInterlockAndHornAck(t *testing.T) {
	rt, sch := load(t)
	if err := sch.Suspend("sim"); err != nil {
		t.Fatal(err)
	}
	rt.Tags().SetReal("TempC", 95.0) // above the 90 °C trip

	sch.Advance(4 * time.Second)
	if rt.Tags().Bool("HiTempAlm") {
		t.Fatalf("high-temp alarm tripped early, at t=%v (PT is 5s)", sch.Elapsed())
	}

	sch.Advance(1500 * time.Millisecond)
	if !rt.Tags().Bool("HiTempAlm") {
		t.Fatalf("high-temp alarm never tripped by t=%v", sch.Elapsed())
	}
	if !rt.Tags().Bool("Horn") {
		t.Fatal("horn should sound while an alarm stands unacked")
	}

	rt.Tags().SetBool("HornAck", true)
	sch.Advance(400 * time.Millisecond) // two ticks of the 200 ms task
	if rt.Tags().Bool("Horn") {
		t.Fatal("horn should silence on ack")
	}
	if !rt.Tags().Bool("HiTempAlm") {
		t.Fatal("acking the horn must not clear the alarm itself")
	}

	// Both alarms clear → the ack releases itself.
	rt.Tags().SetReal("TempC", 60.0)
	sch.Advance(time.Second)
	if rt.Tags().Bool("HiTempAlm") {
		t.Fatal("high-temp alarm should clear below the trip")
	}
	if rt.Tags().Bool("HornAck") {
		t.Fatal("ack should release once both alarms are clear")
	}
	noFaults(t, sch)
}

// Combinational logic needs scans, not durations: the pump seal-in is a
// one-scan question at every step.
func TestPumpSealInHysteresis(t *testing.T) {
	rt, sch := load(t)
	if err := sch.Suspend("sim"); err != nil {
		t.Fatal(err)
	}

	for _, step := range []struct {
		level float64
		want  bool
		why   string
	}{
		{35.0, true, "starts below PumpStartLevel"},
		{60.0, true, "seals in between the bands"},
		{80.0, false, "drops out above PumpStopLevel"},
		{60.0, false, "stays out between the bands"},
	} {
		rt.Tags().SetReal("LevelPct", step.level)
		if err := sch.Scans(1); err != nil {
			t.Fatal(err)
		}
		if got := rt.Tags().Bool("PumpRun"); got != step.want {
			t.Fatalf("LevelPct=%.0f: PumpRun = %v, want %v (%s)", step.level, got, step.want, step.why)
		}
	}
	noFaults(t, sch)
}

// Determinism is the property the whole harness rests on: same resource,
// same inputs, same trajectory — every run, every machine.
func TestRunsAreReproducible(t *testing.T) {
	trajectory := func() []float64 {
		rt, sch := load(t)
		var out []float64
		for range 60 {
			sch.Advance(time.Second)
			out = append(out, rt.Tags().Real("TempC"))
		}
		return out
	}
	a, b := trajectory(), trajectory()
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("run diverged at t=%ds: %v vs %v (bit-identical is the point)", i+1, a[i], b[i])
		}
	}
}

// dt must be exactly the configured period, not merely close to it — that
// exactness is what makes the trajectories above reproducible.
func TestVirtualDtIsExact(t *testing.T) {
	rt, sch := load(t)
	sch.Advance(5 * time.Second)
	if got := rt.Tags().Real("ScanDtS"); got != 0.1 {
		t.Errorf("main dt = %v, want exactly 0.1", got)
	}
	if got := rt.Tags().Real("RepDtS"); got != 1.0 {
		t.Errorf("reports dt = %v, want exactly 1.0", got)
	}
}
