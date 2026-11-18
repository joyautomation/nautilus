package runtime

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sync/atomic"
	"time"
)

// The priority guard (#836): a task granted SCHED_FIFO that stops behaving
// like a periodic task loses the priority, so it cannot starve its core's
// other work (the kernel's threads, the network, a vendor runtime there).
//
// RLIMIT_RTTIME (sched_linux.go) is the kernel's backstop, but it counts
// real-time CPU without a blocking call, and Go's own preemption parks a
// task's thread for a moment about every 10 ms, which resets the count: a
// Go scan that never ends is not caught by it. This guard is the runtime's
// own, deterministic check, two ways:
//
//   - after each scan, on the task's thread: Overruns scans in a row that
//     each took longer than the period;
//   - while a scan runs, from a monitor goroutine: one scan in progress for
//     longer than ScanMultiple periods (a scan that never ends never
//     reaches the first check).
//
// Either moves that task's thread, and only it, to SCHED_OTHER through the
// same path as the kernel limit: logged as an error, shown in the task's
// scan.sched (applied: false, the reason), until a restart.

// PriorityGuard bounds a task that runs at a priority. Zero fields take the
// defaults.
type PriorityGuard struct {
	Overruns     int     `json:"overruns"`     // scans in a row longer than the period; default 50
	ScanMultiple float64 `json:"scanMultiple"` // one scan longer than this many periods; default 20
}

const (
	DefaultGuardOverruns     = 50
	DefaultGuardScanMultiple = 20.0
	// MinGuardBound is the shortest a scan may run before the in-flight
	// check demotes it, whatever the period: one stall of a few tens of
	// milliseconds (a page fault, VM steal, a slow observer) is not a
	// runaway, and a demotion lasts until restart.
	MinGuardBound = 50 * time.Millisecond
	// maxGuardBound caps the in-flight bound, so a huge or non-finite
	// multiple cannot overflow into a negative duration.
	maxGuardBound = time.Hour
)

// guardBound is the in-flight bound for a period: multiple × period, no
// less than MinGuardBound and no more than an hour.
func guardBound(period time.Duration, multiple float64) time.Duration {
	b := multiple * float64(period)
	if math.IsNaN(b) || b > float64(maxGuardBound) {
		return maxGuardBound
	}
	if b < float64(MinGuardBound) {
		return MinGuardBound
	}
	return time.Duration(b)
}

func (g PriorityGuard) withDefaults() PriorityGuard {
	if g.Overruns <= 0 {
		g.Overruns = DefaultGuardOverruns
	}
	if g.ScanMultiple <= 0 {
		g.ScanMultiple = DefaultGuardScanMultiple
	}
	return g
}

// overrunWatch is the after-scan half, kept by the task's own loop.
type overrunWatch struct {
	limit   int
	consec  int
	tripped bool
}

// observe takes one finished scan and returns why the task must lose its
// priority, once, or "".
func (w *overrunWatch) observe(scan, period time.Duration) string {
	if w.tripped || w.limit <= 0 {
		return ""
	}
	if scan > period {
		w.consec++
	} else {
		w.consec = 0
	}
	if w.consec >= w.limit {
		w.tripped = true
		return fmt.Sprintf("%d scans in a row overran the %s period (priority guard, overruns: %d)", w.consec, period, w.limit)
	}
	return ""
}

// guarded is one loop the monitor watches: its edge counter (liveness.go),
// the bound on one scan, its thread, and whether that thread still holds
// a priority of ours.
type guarded struct {
	live   *loopLive
	bound  time.Duration
	tid    *atomic.Int64
	held   func(tid int) bool // the registry: still at real-time priority
	seen   uint64
	seenAt time.Time
	done   bool
}

// check returns the reason to demote the loop at now, once, or "": a scan
// in progress (odd edge count) that has not ended for longer than the
// bound, measured from the first check that saw it, as Liveness does. A
// loop whose thread holds no priority once it has started (refused, not
// Linux, or demoted already by another path) is done: nothing to guard.
func (g *guarded) check(now time.Time) string {
	if g.done {
		return ""
	}
	v := g.live.edges.Load()
	if v > 0 && !g.held(int(g.tid.Load())) { // its setup has run (runLoop)
		g.done = true
		return ""
	}
	if v != g.seen || g.seenAt.IsZero() {
		g.seen, g.seenAt = v, now
		return ""
	}
	if v&1 == 1 && now.Sub(g.seenAt) > g.bound {
		g.done = true
		return fmt.Sprintf("one scan has run for more than %s, %g × its %s period (priority guard, scan-multiple)", g.bound, float64(g.bound)/float64(g.live.period), g.live.period)
	}
	return ""
}

// monitorPriority checks the guarded loops until ctx ends or none is left
// to guard, at a tenth of the shortest bound (no faster than 1 ms, no
// slower than 100 ms). demote moves a thread, returning false when it held
// no priority of ours.
func monitorPriority(ctx context.Context, loops []*guarded, demote func(tid int, reason string) bool) {
	if len(loops) == 0 {
		return
	}
	tick := 100 * time.Millisecond
	for _, g := range loops {
		if t := g.bound / 10; t < tick {
			tick = t
		}
	}
	if tick < time.Millisecond {
		tick = time.Millisecond
	}
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			left := 0
			for _, g := range loops {
				if reason := g.check(now); reason != "" && demote(int(g.tid.Load()), reason) {
					slog.Error("runtime: real-time priority withdrawn", "task", g.live.name, "reason", reason)
				}
				if !g.done {
					left++
				}
			}
			if left == 0 {
				return
			}
		}
	}
}
