package runtime

import (
	"context"
	"math"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// held stands in for the registry: thread 7 holds a priority.
func heldBy7(tid int) bool { return tid == 7 }

func tid7() *atomic.Int64 { var t atomic.Int64; t.Store(7); return &t }

// The after-scan half: N scans in a row over the period trip it, once; a
// scan within the period starts the count again.
func TestPriorityGuardOverrunWatch(t *testing.T) {
	const period = 10 * time.Millisecond
	w := overrunWatch{limit: 3}
	steps := []struct {
		scan time.Duration
		trip bool
	}{
		{11 * time.Millisecond, false},
		{12 * time.Millisecond, false},
		{5 * time.Millisecond, false}, // back within: the count starts over
		{11 * time.Millisecond, false},
		{11 * time.Millisecond, false},
		{11 * time.Millisecond, true},  // third in a row
		{11 * time.Millisecond, false}, // once only
	}
	for i, s := range steps {
		why := w.observe(s.scan, period)
		if (why != "") != s.trip {
			t.Fatalf("step %d (%s): reason %q, want trip %v", i, s.scan, why, s.trip)
		}
		if s.trip && !strings.Contains(why, "3 scans in a row") {
			t.Errorf("reason %q", why)
		}
	}
	if (&overrunWatch{}).observe(time.Hour, period) != "" {
		t.Error("an unset watch must not trip")
	}
	if g := (PriorityGuard{}).withDefaults(); g.Overruns != DefaultGuardOverruns || g.ScanMultiple != DefaultGuardScanMultiple {
		t.Errorf("defaults %+v", g)
	}
}

// The in-flight half: a scan in progress (odd edge count) unchanged for
// longer than the bound trips it, once; a loop between scans (even count)
// never does, however long it sleeps.
func TestPriorityGuardStuckScan(t *testing.T) {
	l := &loopLive{name: "fast", period: 5 * time.Millisecond}
	g := &guarded{live: l, bound: 100 * time.Millisecond, tid: tid7(), held: heldBy7}
	t0 := time.Unix(0, 0)
	at := func(ms int) time.Time { return t0.Add(time.Duration(ms) * time.Millisecond) }

	if g.check(at(0)) != "" { // first sight
		t.Fatal("first check tripped")
	}
	if g.check(at(500)) != "" {
		t.Fatal("a loop between scans (even count) must never trip")
	}
	l.begin() // a scan starts and does not end
	if g.check(at(510)) != "" {
		t.Fatal("first sight of the scan tripped")
	}
	if g.check(at(600)) != "" {
		t.Fatal("tripped at the bound, before it was passed")
	}
	why := g.check(at(611))
	if !strings.Contains(why, "more than 100ms") {
		t.Fatalf("a scan past the bound: reason %q", why)
	}
	if g.check(at(900)) != "" {
		t.Fatal("tripped twice")
	}

	// A scan that ends in time and the next that starts are new counts.
	l2 := &loopLive{name: "ok", period: 5 * time.Millisecond}
	g2 := &guarded{live: l2, bound: 100 * time.Millisecond, tid: tid7(), held: heldBy7}
	g2.check(at(0))
	for ms := 10; ms < 1000; ms += 10 {
		l2.begin()
		if g2.check(at(ms)) != "" {
			t.Fatalf("a loop scanning normally tripped at %d ms", ms)
		}
		l2.end()
	}
}

// The in-flight bound: multiple × period, never under MinGuardBound (one
// stall of tens of ms is not a runaway), and a huge, infinite or NaN
// multiple caps at an hour instead of overflowing into a negative bound
// that would demote at the first check.
func TestPriorityGuardBound(t *testing.T) {
	for _, c := range []struct {
		period   time.Duration
		multiple float64
		want     time.Duration
	}{
		{time.Millisecond, 20, MinGuardBound},               // 20 ms → the floor
		{10 * time.Millisecond, 20, 200 * time.Millisecond}, // over the floor
		{time.Millisecond, 1e300, time.Hour},
		{time.Millisecond, math.Inf(1), time.Hour},
		{time.Millisecond, math.NaN(), time.Hour},
	} {
		if got := guardBound(c.period, c.multiple); got != c.want {
			t.Errorf("guardBound(%s, %g) = %s, want %s", c.period, c.multiple, got, c.want)
		}
	}
}

// A loop whose thread holds no priority once it has started (refused, not
// Linux, demoted by another path) is not guarded, and the monitor stops
// when no loop is left to guard rather than ticking for the whole run.
func TestPriorityGuardMonitorStops(t *testing.T) {
	l := &loopLive{name: "refused", period: time.Millisecond}
	l.begin()
	l.end() // started: its setup has run
	var none atomic.Int64
	g := &guarded{live: l, bound: MinGuardBound, tid: &none, held: heldBy7}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		monitorPriority(ctx, []*guarded{g}, func(int, string) bool { t.Error("demoted a loop with no priority"); return false })
		close(done)
	}()
	select {
	case <-done:
		if ctx.Err() != nil {
			t.Fatal("the monitor ran until its context ended instead of stopping")
		}
	case <-time.After(time.Second):
		t.Fatal("the monitor did not stop with nothing to guard")
	}
}
