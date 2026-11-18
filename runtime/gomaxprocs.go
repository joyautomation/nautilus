package runtime

import (
	"log/slog"
	"os"
	goruntime "runtime"
)

// GOMAXPROCS on a box with few cores (#830).
//
// Go runs goroutines on GOMAXPROCS Ps, which default to the number of cores.
// A task at SCHED_FIFO wakes from its absolute sleep on its own thread, but
// still needs a free P before it can run Go code. On a two-core box one long
// scan in another task and the garbage collector can hold both Ps, and the
// FIFO task then waits for Go's own preemption, around 10 ms, whatever its
// OS priority. More Ps than cores leave one free, and the OS scheduler,
// where the priority acts, decides who runs.
//
// Measured on a Verdin AM62 Dual (2x Cortex-A53, stock PREEMPT kernel;
// docs/design/verdin-bench.md): with a 1 ms task at FIFO 80 beside a 100 ms
// task running 20 ms of logic, GOMAXPROCS 2 ran 95.8 % of the 1 ms slots,
// p99.9 9.22 ms late; 4 ran 99.92 %, p99.9 188 µs; 8 was no better than 4
// and its worst case worse. Hence one extra P per FIFO task plus one, capped
// near twice the cores.

// maxCoresToRaise: above this many cores the default leaves Ps to spare.
const maxCoresToRaise = 4

// ProcsStats is what the runtime did with GOMAXPROCS.
type ProcsStats struct {
	// Value is GOMAXPROCS as the scan loops run.
	Value int `json:"value"`
	// RaisedFrom is the default it replaced, when the runtime raised it.
	RaisedFrom int `json:"raisedFrom,omitempty"`
	// Reason says why it was raised, or why the default was kept although
	// a task has a priority.
	Reason string `json:"reason,omitempty"`
}

// procsFor is the rule: the GOMAXPROCS a process should run with, given the
// current value (the default Go chose: the cores, or the cgroup's CPU
// limit), how many loops run at SCHED_FIFO, and whether the operator set
// GOMAXPROCS. It raises only when a loop has a priority, the operator did
// not set it, and there are at most maxCoresToRaise cores.
func procsFor(cur, fifo int, operatorSet bool) (n int, reason string) {
	switch {
	case fifo == 0:
		return cur, ""
	case operatorSet:
		return cur, "GOMAXPROCS set in the environment"
	case cur > maxCoresToRaise:
		return cur, ""
	}
	n = cur + fifo + 1
	if limit := 2*cur + 2; n > limit {
		n = limit
	}
	return n, "a task runs at a priority on a box with few cores: one P for each such task beyond the cores, plus one"
}

// Seams for tests: the process-wide setting, and whether the operator set it.
var (
	setGOMAXPROCS   = goruntime.GOMAXPROCS
	gomaxprocsInEnv = func() bool { _, ok := os.LookupEnv("GOMAXPROCS"); return ok }
)

// tuneProcs applies procsFor once, before the loops start, and records the
// outcome for Stats. Process-wide by nature: one controller is one process.
func (r *Runtime) tuneProcs() {
	r.procsOnce.Do(func() {
		fifo := 0
		if r.prio > 0 {
			fifo++
		}
		for _, tr := range r.tasks {
			if tr.prio > 0 {
				fifo++
			}
		}
		cur := setGOMAXPROCS(0)
		n, reason := procsFor(cur, fifo, gomaxprocsInEnv())
		st := ProcsStats{Value: cur, Reason: reason}
		if n != cur {
			setGOMAXPROCS(n)
			st = ProcsStats{Value: n, RaisedFrom: cur, Reason: reason}
			slog.Info("runtime: GOMAXPROCS raised so a task at a priority is not kept waiting for a P",
				"from", cur, "to", n, "tasksAtPriority", fifo)
		}
		r.mu.Lock()
		r.stats.Procs = st
		r.mu.Unlock()
	})
}
