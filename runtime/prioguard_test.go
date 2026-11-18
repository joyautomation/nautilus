package runtime

import (
	"strings"
	"testing"
	"time"
)

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
	g := &guarded{live: l, bound: 100 * time.Millisecond}
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
	g2 := &guarded{live: l2, bound: 100 * time.Millisecond}
	g2.check(at(0))
	for ms := 10; ms < 1000; ms += 10 {
		l2.begin()
		if g2.check(at(ms)) != "" {
			t.Fatalf("a loop scanning normally tripped at %d ms", ms)
		}
		l2.end()
	}
}
