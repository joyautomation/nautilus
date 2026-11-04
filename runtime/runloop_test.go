package runtime

import (
	"context"
	"testing"
	"time"
)

// virtualLoopClock is a loopClock in virtual time: a sleep lands `late`
// past its deadline (the wake-up latency every real sleep has), a scan
// spends what the test says it spends, and the window closes at `end` —
// the loop is told to stop when its next slot falls past it. Nothing here
// depends on how fast or loaded the machine is.
type virtualLoopClock struct {
	now, end time.Time
	late     time.Duration
}

func (c *virtualLoopClock) Now() time.Time        { return c.now }
func (c *virtualLoopClock) spend(d time.Duration) { c.now = c.now.Add(d) }
func (c *virtualLoopClock) SleepUntil(_ context.Context, deadline time.Time) bool {
	if deadline.After(c.end) {
		return false
	}
	if deadline.After(c.now) {
		c.now = deadline.Add(c.late)
	}
	return true
}

// The loop keeps an ABSOLUTE schedule: slot n is due at start + n·period
// whatever happened before it, so the wake-up latency every sleep has is
// paid once per scan and never adds up. Over 300 ms at 2 ms (150 slots),
// with every wake 50 µs late and every scan spending 200 µs, a loop that
// scheduled from its last wake would slip 250 µs a slot, about 12 % of the
// window. One scan that stalls for five periods loses exactly the four
// slots that went by under it: they are reported as missed, not fired in a
// burst, and the next slot due runs at once.
func TestRunLoopHoldsScheduleAndCountsMissed(t *testing.T) {
	const (
		period = 2 * time.Millisecond
		late   = 50 * time.Microsecond
		exec   = 200 * time.Microsecond
		slots  = 150
		stall  = 50 // the scan that stalls
	)
	start := time.Unix(1_700_000_000, 0)
	clk := &virtualLoopClock{now: start, end: start.Add(slots * period), late: late}
	type scanAt struct{ due, began time.Time }
	var scans []scanAt
	var missed uint64
	runSchedule(context.Background(), period, clk, func(due time.Time) {
		scans = append(scans, scanAt{due, clk.Now()})
		clk.spend(exec)
		if len(scans) == stall {
			clk.spend(5 * period)
		}
	}, func(k uint64) { missed += k })

	// Every slot is either scanned or reported missed: 150 in all, the
	// stall's four missed, and the window's last slot reached.
	if missed != 4 {
		t.Errorf("missed = %d, want 4 (one stall of five periods)", missed)
	}
	if len(scans) != slots-4 {
		t.Fatalf("scans = %d, want %d", len(scans), slots-4)
	}
	if last := scans[len(scans)-1].due; !last.Equal(start.Add(slots * period)) {
		t.Errorf("last slot due at %v, want the window's end %v: the schedule drifted", last.Sub(start), slots*period)
	}
	for i, s := range scans {
		off := s.due.Sub(start)
		if off%period != 0 {
			t.Fatalf("scan %d due at %v, off the start + n·period grid", i+1, off)
		}
		if i > 0 && s.due.Sub(scans[i-1].due) != period && i != stall {
			t.Errorf("scan %d due %v after the one before; want one period", i+1, s.due.Sub(scans[i-1].due))
		}
		// Lateness is one wake-up's worth on every scan, not a sum of
		// them — except the slot due while the stall ran, which starts as
		// soon as the stall ends.
		lateness := s.began.Sub(s.due)
		if i == stall {
			if want := late + exec; lateness != want { // the stalled scan's own lateness and work
				t.Errorf("catch-up scan %d began %v after its slot, want %v", i+1, lateness, want)
			}
			continue
		}
		if lateness != late {
			t.Errorf("scan %d began %v after its slot, want %v", i+1, lateness, late)
		}
	}
}

func TestRunLoopStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runLoop(ctx, time.Hour, func(time.Time) { t.Error("scan ran") }, func(uint64) {}, nil)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("runLoop did not return on cancel")
	}
}

// A loop whose scan overruns its period has a slot already past every time
// it goes to sleep; a cancel must still stop it after the scan in flight.
// The Linux sleeper answered a past deadline with "go" without looking at
// ctx, so the loop scanned on forever and naut run's stop waited out its
// whole timeout with the HTTP port held (nautilus#599).
func TestRunLoopOverrunningStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	scans := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		runLoop(ctx, time.Millisecond, func(time.Time) {
			select {
			case scans <- struct{}{}:
			default:
			}
			time.Sleep(5 * time.Millisecond) // five periods per scan
		}, func(uint64) {}, nil)
		close(done)
	}()
	<-scans
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("an overrunning runLoop did not return on cancel")
	}
}

func TestSleepUntilPastDeadlineAnswersCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	past := time.Now().Add(-time.Millisecond)
	if !sleepUntil(ctx, past) {
		t.Fatal("a past deadline with a live context should go straight on")
	}
	cancel()
	if sleepUntil(ctx, past) {
		t.Fatal("a past deadline after a cancel reported the deadline, not the cancel")
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
