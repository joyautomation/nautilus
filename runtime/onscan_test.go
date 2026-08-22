package runtime_test

import (
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/runtime"
)

// OnScan is the post-scan observer hook: an alarm engine (or anything else
// that needs to react to the tag store every cycle without a private
// ticker) registers a callback that fires at the end of every main-task
// Scan(), after the program ran and outputs were written, with scanMu
// still held.

// onScanProg drives one output tag (Heater, derived from an input, TempC)
// so a test can tell "the value the observer saw" apart from "whatever was
// there before this scan ran."
const onScanProg = `PROGRAM Main
VAR_EXTERNAL
    TempC : REAL;
    Heater : REAL;
END_VAR
Heater := TempC * 2.0;
END_PROGRAM`

func onScanRuntime(t *testing.T, drv nio.Driver, clock runtime.Clock) *runtime.Runtime {
	t.Helper()
	rt, err := runtime.New(runtime.Options{
		Program: onScanProg, Driver: drv,
		Inputs: []string{"TempC"}, Outputs: []string{"Heater"},
		Clock: clock,
	})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return rt
}

// The observer sees the value the PROGRAM just computed this scan, not a
// stale one — proof it fires after Run, not before it or on some other
// cadence.
func TestOnScanFiresWithPostProgramValues(t *testing.T) {
	drv := nio.NewMemory()
	_ = drv.WriteOutputs(nio.Values{"TempC": 10.0})
	rt := onScanRuntime(t, drv, nil)

	var seen []float64
	cancel := rt.OnScan(func(t *runtime.Tags) {
		seen = append(seen, t.Real("Heater"))
	})
	defer cancel()

	rt.Scan()
	_ = drv.WriteOutputs(nio.Values{"TempC": 25.0})
	rt.Scan()

	if len(seen) != 2 {
		t.Fatalf("observer fired %d times, want 2 (one per Scan)", len(seen))
	}
	if seen[0] != 20.0 {
		t.Errorf("first scan: observer saw Heater=%v, want 20 (10*2, this scan's value)", seen[0])
	}
	if seen[1] != 50.0 {
		t.Errorf("second scan: observer saw Heater=%v, want 50 (25*2, this scan's value)", seen[1])
	}
}

// Multiple observers run in registration order, every scan.
func TestOnScanMultipleObserversOrder(t *testing.T) {
	drv := nio.NewMemory()
	rt := onScanRuntime(t, drv, nil)

	var order []string
	rt.OnScan(func(*runtime.Tags) { order = append(order, "a") })
	rt.OnScan(func(*runtime.Tags) { order = append(order, "b") })
	rt.OnScan(func(*runtime.Tags) { order = append(order, "c") })

	rt.Scan()
	want := []string{"a", "b", "c"}
	if len(order) != len(want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Errorf("order[%d] = %q, want %q (full: %v)", i, order[i], want[i], order)
		}
	}

	order = nil
	rt.Scan()
	if len(order) != 3 {
		t.Fatalf("second scan fired %d observers, want 3 (registration is not one-shot)", len(order))
	}
}

// cancel stops future firings, and calling it more than once is a no-op —
// not a panic, not a double-removal of a different observer registered at
// the same index after the first removal.
func TestOnScanCancelStopsFiringAndIsIdempotent(t *testing.T) {
	drv := nio.NewMemory()
	rt := onScanRuntime(t, drv, nil)

	count := 0
	cancel := rt.OnScan(func(*runtime.Tags) { count++ })
	other := 0
	rt.OnScan(func(*runtime.Tags) { other++ })

	rt.Scan()
	if count != 1 || other != 1 {
		t.Fatalf("before cancel: count=%d other=%d, want 1,1", count, other)
	}

	cancel()
	cancel() // idempotent: must not panic or remove "other"

	rt.Scan()
	rt.Scan()
	if count != 1 {
		t.Errorf("count after cancel = %d, want 1 (no further firings)", count)
	}
	if other != 3 {
		t.Errorf("other = %d, want 3 (cancelling one observer must not touch another)", other)
	}
}

// A panicking observer is recovered — the scan completes, the driver still
// gets its outputs, and observers after the panicking one still run.
func TestOnScanPanicIsRecoveredAndScanContinues(t *testing.T) {
	drv := nio.NewMemory()
	_ = drv.WriteOutputs(nio.Values{"TempC": 5.0})
	rt := onScanRuntime(t, drv, nil)

	after := 0
	rt.OnScan(func(*runtime.Tags) { panic("boom") })
	rt.OnScan(func(*runtime.Tags) { after++ })

	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("Scan() propagated the observer's panic: %v", r)
			}
		}()
		rt.Scan()
	}()

	if after != 1 {
		t.Errorf("observer after the panicking one ran %d times, want 1", after)
	}
	if rt.Stats().Count != 1 {
		t.Errorf("scan count = %d, want 1 (the scan itself must complete)", rt.Stats().Count)
	}
	// The scan loop, and this observer's own registration, must still work
	// on the NEXT scan — a recovered panic must not have corrupted state.
	rt.Scan()
	if after != 2 {
		t.Errorf("after a recovered panic, the next scan's observer ran %d times, want 2", after)
	}
}

// fakeClock is a minimal runtime.Clock a test moves by hand — the same
// seam the acceptance package's VirtualClock uses (a stopped clock the
// harness advances explicitly), reproduced locally so this test does not
// need to import the acceptance package.
type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

// OnScan fires the same way whether Scan() is driven by Run's real ticker
// or called directly against a stopped virtual clock — exactly how the
// acceptance harness's Scheduler drives it (scheduler.go calls rt.Scan()
// directly, never Run). No wall-clock sleep here proves it: time never
// advances and the observer still fires once per direct Scan() call, with
// NowMs following the injected clock.
func TestOnScanUnderVirtualClock(t *testing.T) {
	clk := &fakeClock{now: time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)}
	drv := nio.NewMemory()
	rt := onScanRuntime(t, drv, clk)

	var nowMs []int64
	rt.OnScan(func(t *runtime.Tags) { nowMs = append(nowMs, t.NowMs()) })

	rt.Scan()
	clk.now = clk.now.Add(10 * time.Second) // virtual time jump, no sleep
	rt.Scan()
	clk.now = clk.now.Add(10 * time.Second)
	rt.Scan()

	if len(nowMs) != 3 {
		t.Fatalf("observer fired %d times under the virtual clock, want 3", len(nowMs))
	}
	if nowMs[1]-nowMs[0] != 10000 || nowMs[2]-nowMs[1] != 10000 {
		t.Errorf("NowMs seen by the observer = %v, want steps of exactly 10000ms (the injected clock, not the wall)", nowMs)
	}
}

// An observer may read the tag store, including a member path, without
// deadlocking — Scan holds scanMu while firing observers, never t.mu, so
// Tags.ReadPath (which takes t.mu.RLock) is safe to call from inside one.
func TestOnScanObserverCanReadPathWithoutDeadlock(t *testing.T) {
	drv := nio.NewMemory()
	_ = drv.WriteOutputs(nio.Values{"TempC": 3.0})
	rt := onScanRuntime(t, drv, nil)

	done := make(chan any, 1)
	rt.OnScan(func(t *runtime.Tags) {
		v, ok := t.ReadPath("Heater")
		if !ok {
			done <- nil
			return
		}
		done <- v
	})

	go rt.Scan()
	select {
	case v := <-done:
		if v != 6.0 {
			t.Errorf("ReadPath(\"Heater\") from inside OnScan = %v, want 6.0", v)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("OnScan observer calling ReadPath deadlocked")
	}
}
