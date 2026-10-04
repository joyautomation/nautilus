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
	var stamps []time.Time
	runLoop(ctx, period, func(time.Time) {
		mu.Lock()
		scans++
		stamps = append(stamps, time.Now())
		n := scans
		mu.Unlock()
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
	// 150 slots minus the ~5 lost to the stall; loose bounds for CI.
	if scans < 120 || scans > 151 {
		t.Errorf("scans = %d, want ≈ 145", scans)
	}
	if missed < 3 || missed > 8 {
		t.Errorf("missed = %d, want ≈ 4–5 (one 5-period stall)", missed)
	}
	// No burst: after the stall, consecutive scans are still ~a period apart.
	burst := 0
	for i := 1; i < len(stamps); i++ {
		if stamps[i].Sub(stamps[i-1]) < period/4 {
			burst++
		}
	}
	if burst > 0 {
		t.Errorf("%d back-to-back catch-up scans after the stall", burst)
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
