package runtime

import (
	"context"
	"sync"
	"testing"
	"time"
)

// runLoop keeps an absolute schedule: over 300 ms at 2 ms it must land
// close to 150 scans (a drifting ticker loses several percent), and when
// one scan stalls for several periods the lost slots are reported as
// missed instead of being fired in a burst.
func TestRunLoopHoldsScheduleAndCountsMissed(t *testing.T) {
	const period = 2 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	var mu sync.Mutex
	var scans, missed uint64
	runLoop(ctx, period, func(due time.Time) {
		mu.Lock()
		scans++
		n := scans
		mu.Unlock()
		if time.Now().Before(due) {
			t.Errorf("scan %d started before its slot", n)
		}
		if n == 50 {
			time.Sleep(5 * period) // one stall of five periods
		}
	}, func(k uint64) {
		mu.Lock()
		missed += k
		mu.Unlock()
	})
	mu.Lock()
	defer mu.Unlock()
	// 150 slots minus the ~5 lost to the stall; loose bounds for a shared
	// CI runner, which can stall the loop on its own.
	if scans < 120 {
		t.Errorf("scans = %d, want ≈ 145", scans)
	}
	if missed < 3 {
		t.Errorf("missed = %d, want ≥ 4 (one 5-period stall)", missed)
	}
	// No burst: every scan accounts for one slot, so scans + missed can
	// never exceed the slots that elapsed. A catch-up scan within ONE
	// period of a late wake is by design (the slot was due); firing extra
	// scans to make up skipped slots is not.
	if scans+missed > 152 {
		t.Errorf("scans %d + missed %d = %d exceeds the ~150 slots elapsed", scans, missed, scans+missed)
	}
}

func TestRunLoopStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runLoop(ctx, time.Hour, func(time.Time) { t.Error("scan ran") }, func(uint64) {})
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("runLoop did not return on cancel")
	}
}

// A cancel must end a long sleep promptly, not at the next slot: the
// Linux sleeper chunks its uninterruptible sleeps for exactly this.
func TestSleepUntilReturnsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	t0 := time.Now()
	if sleepUntil(ctx, time.Now().Add(time.Hour)) {
		t.Fatal("sleepUntil reported the deadline, not the cancel")
	}
	if d := time.Since(t0); d > 500*time.Millisecond {
		t.Errorf("returned after %v, want well under the chunk bound", d)
	}
}

// And it must land on the deadline, not before it.
func TestSleepUntilLandsOnDeadline(t *testing.T) {
	deadline := time.Now().Add(15 * time.Millisecond)
	if !sleepUntil(context.Background(), deadline) {
		t.Fatal("cancelled?")
	}
	if late := time.Since(deadline); late < 0 || late > 10*time.Millisecond {
		t.Errorf("woke %v from the deadline", late)
	}
}
