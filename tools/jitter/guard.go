package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/joyautomation/nautilus/runtime"
)

// The run guard (#836): jitter puts real-time load on a box, and a box
// that cannot keep up must not be left carrying it. On a PLCnext, a 1 ms
// task at SCHED_FIFO 40 on the shim's core went 1.84 h without a scan while
// the box was off the network, and the "3-minute" run lasted 1 h 52 m. The
// runtime's own watchdog did not trip: the task was not overrunning or
// stuck in a scan, it never got to run. jitter's other goroutines did run,
// so jitter watches its own run from one and stops it when:
//
//   - the main task's scan count makes no progress for Stall (2 s);
//   - the runtime withdrew a task's priority (its priority guard or the
//     kernel's CPU limit): what is left would not measure what was asked;
//   - the run is past Deadline (duration × 1.5 + 10 s).
//
// Stopping cancels the run. A starved loop may not see that, so if the run
// has not ended Grace later, the caller exits: the process ending is what
// takes the load off the box. A stopped run's report says INVALID.

type guardConfig struct {
	Stall    time.Duration // no main-task scan for this long stops the run
	Every    time.Duration // how often to look
	Deadline time.Duration // the whole run may take no longer
	Grace    time.Duration // after stopping, how long the run may take to end
}

func defaultGuard(duration time.Duration) guardConfig {
	return guardConfig{Stall: 2 * time.Second, Every: 250 * time.Millisecond, Deadline: duration*3/2 + 10*time.Second, Grace: 2 * time.Second}
}

// runGuarded runs run(ctx) under the guard. reason is why it was stopped
// ("" when it ran its course); finished is false when, stopped, the run did
// not end within Grace and the caller must exit to end it.
func runGuarded(ctx context.Context, run func(context.Context), stats func() runtime.ScanStats, g guardConfig) (reason string, finished bool) {
	runCtx, stopRun := context.WithCancel(ctx)
	defer stopRun()
	var once sync.Once
	why := make(chan string, 1)
	trip := func(r string) {
		once.Do(func() {
			why <- r
			stopRun()
		})
	}
	done := make(chan struct{})
	go func() { run(runCtx); close(done) }()
	go watchRun(runCtx, stats, g, trip)

	deadline := time.NewTimer(g.Deadline)
	defer deadline.Stop()
	select {
	case <-done:
	case <-deadline.C:
		trip(fmt.Sprintf("the run is past %s, longer than its duration allows: the box cannot keep up", g.Deadline))
	case r := <-why:
		why <- r // put it back for below
	}
	select {
	case reason = <-why:
	default:
		return "", true
	}
	select {
	case <-done:
		return reason, true
	case <-time.After(g.Grace):
		return reason, false
	}
}

// watchRun stops the run (trip) when the main task makes no progress for
// g.Stall, or a task's priority was withdrawn.
func watchRun(ctx context.Context, stats func() runtime.ScanStats, g guardConfig, trip func(string)) {
	t := time.NewTicker(g.Every)
	defer t.Stop()
	var last uint64
	lastMoved := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			s := stats()
			if s.Sched.Demoted {
				trip("the runtime withdrew the main task's priority mid-run: " + s.Sched.Error)
				return
			}
			for _, tk := range s.Tasks {
				if tk.Sched.Demoted {
					trip("the runtime withdrew task " + tk.Name + "'s priority mid-run: " + tk.Sched.Error)
					return
				}
			}
			if s.Count != last {
				last, lastMoved = s.Count, now
			} else if now.Sub(lastMoved) > g.Stall {
				trip(fmt.Sprintf("the main task has not scanned for %s: the box cannot keep up", now.Sub(lastMoved).Round(100*time.Millisecond)))
				return
			}
		}
	}
}
