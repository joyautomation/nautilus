package runtime

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Liveness: are this runtime's scans completing? A redundancy elector asks
// before it renews its lease (leader.Elector.SetHealth), so a replica whose
// scan is hung (a program that never returns, a driver call that blocks)
// stops renewing and a standby takes over (#621). Scan diagnostics cannot
// answer this: lateness and overruns are recorded when a scan FINISHES, and
// a hung scan never does.
//
// Each of Run's loops counts its scan edges, one atomic add as a scan
// begins and one as it ends, so an odd count means a scan is in progress.
// Liveness keeps, per loop, the count it saw last and the caller's time it
// first saw it. A loop whose count is odd and unchanged for longer than its
// bound is stuck. A loop between scans is never stuck, however long its
// period: it is sleeping to its next slot, and a 10 s task that idles for
// 9.99 s is healthy. So the bound depends on how long a scan may take, not
// on how long a task may go without one, and a slow task cannot push it
// past the lease.
//
// All timing is in the caller's clock (the now it passes): the runtime
// stamps nothing, so the elector's lease arithmetic and this check cannot
// disagree, and a test drives both with the same synthetic times.

// loopLive is one Run loop's scan-edge counter and the checker's memory of
// it. edges is written by the loop; seen and seenAt only by Liveness, under
// Runtime.liveMu.
type loopLive struct {
	name   string
	period time.Duration
	edges  atomic.Uint64

	seen   uint64
	seenAt time.Time
}

// begin and end bracket one scan: the count is odd between them. Two
// atomic adds a scan, no allocation.
func (l *loopLive) begin() { l.edges.Add(1) }
func (l *loopLive) end()   { l.edges.Add(1) }

// ErrStopped is what Liveness reports once Run has returned: an orderly
// stop, not a hang. It wraps context.Canceled, so a caller that tells the
// two apart (the elector logs it at info) needs no import of this package.
var ErrStopped = fmt.Errorf("runtime: the scan loops have stopped: %w", context.Canceled)

// Run's lifecycle, as Liveness sees it.
const (
	runNotStarted int32 = iota
	runRunning
	runStopped
)

// liveness is the Runtime's share of this: the loops, Run's lifecycle and
// the start-up grace.
type liveness struct {
	loops []*loopLive // main first, then tasks in declaration order
	state atomic.Int32

	mu         sync.Mutex
	firstAsked time.Time // when Liveness was first asked before Run began
}

// StallBound is how long one scan of a task with this period may be in
// progress before Liveness calls it stuck: twice the period, at least half
// of max and at most max. Twice the period is an overrun of a whole slot;
// the floor keeps a fast task that overruns now and then (a 10 ms task
// whose read takes 40 ms) from looking hung; the cap keeps a slow task's
// bound under the lease, whatever its period. The elector passes max as
// half its lease (2 s of the 4 s lease: a bound of 1 to 2 s).
func StallBound(period, max time.Duration) time.Duration {
	b := 2 * period
	if b < max/2 {
		b = max / 2
	}
	if b > max {
		b = max
	}
	return b
}

// startGrace is how long Liveness answers live before Run has started its
// loops, from the first time it is asked: twice the lease (max is half of
// it), so the elector's first decision (up to one lease of retries) and
// naut run's wait for it (5 s) fit, and a runtime whose loops never start
// does not hold a lease for longer than that.
func startGrace(max time.Duration) time.Duration { return 4 * max }

// Liveness reports whether Run's scans are completing at now: nil when
// they are, else an error naming every stuck task. A loop is stuck when
// one scan has been in progress for longer than StallBound(period, max),
// measured from the first call that saw it in that scan (so the measure
// runs up to one call interval short; the elector calls once a tick).
// Before Run has started, Liveness answers nil for a start-up grace of
// 4·max from its first call, then an error; after Run has returned, an
// error. It watches Run's loops only: a runtime driven by Scan from a
// scheduler of its own reads as not live once the grace is over. It is
// meant for one caller (the elector); it allocates only when it reports a
// problem.
func (r *Runtime) Liveness(now time.Time, max time.Duration) error {
	lv := &r.live
	lv.mu.Lock()
	defer lv.mu.Unlock()
	switch lv.state.Load() {
	case runNotStarted:
		if lv.firstAsked.IsZero() {
			lv.firstAsked = now
		}
		if g := startGrace(max); now.Sub(lv.firstAsked) > g {
			return fmt.Errorf("runtime: no scan loop has started %s after the first liveness check", g)
		}
		return nil
	case runStopped:
		return ErrStopped
	}
	var stuck []string
	for _, l := range lv.loops {
		v := l.edges.Load()
		if v != l.seen || l.seenAt.IsZero() {
			l.seen, l.seenAt = v, now
			continue
		}
		if v&1 == 0 {
			continue // between scans, sleeping to its next slot
		}
		if b := StallBound(l.period, max); now.Sub(l.seenAt) > b {
			// No running duration in the text: the elector logs a change
			// of answer, and this one must read the same every tick.
			stuck = append(stuck, fmt.Sprintf("task %s: a scan has not finished within %s", l.name, b))
		}
	}
	if len(stuck) > 0 {
		return fmt.Errorf("runtime: scans are not completing: %s", strings.Join(stuck, "; "))
	}
	return nil
}
