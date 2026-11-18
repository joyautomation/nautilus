package runtime

import (
	"bufio"
	"log/slog"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
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
// current value (the default Go chose), how many loops request SCHED_FIFO,
// whether the operator set GOMAXPROCS, and whether a CPU limit (a cgroup
// quota, as `docker run --cpus` sets) is in force. It raises only when a
// loop requests a priority, the operator did not set it, no CPU limit
// applies, and there are at most maxCoresToRaise cores. Under a CPU quota
// more Ps than the quota allows means throttling: spikes, the opposite of
// what a real-time task wants.
func procsFor(cur, fifo int, operatorSet, cpuLimited bool) (n int, reason string) {
	switch {
	case fifo == 0:
		return cur, ""
	case operatorSet:
		return cur, "GOMAXPROCS set in the environment"
	case cpuLimited:
		return cur, "a CPU limit (cgroup quota) applies: more Ps than it allows would be throttled"
	case cur > maxCoresToRaise:
		return cur, ""
	}
	n = cur + fifo + 1
	if limit := 2*cur + 2; n > limit {
		n = limit
	}
	return n, "a task requests a real-time priority on a box with few cores: one P for each such task beyond the cores, plus one"
}

// operatorGOMAXPROCS reports a GOMAXPROCS Go itself would use: a positive
// whole number. An empty or invalid value is ignored by Go, and here too.
func operatorGOMAXPROCS(v string) bool {
	n, err := strconv.Atoi(v)
	return err == nil && n > 0
}

// cgroupCPUQuota reports whether this process's cgroup sets a CPU quota:
// cpu.max other than "max" (cgroup v2), or cpu.cfs_quota_us other than -1
// (v1). Absent files (not Linux, no cgroup mounted) mean no quota.
func cgroupCPUQuota() bool { return cgroupCPUQuotaAt("/proc/self/cgroup", "/sys/fs/cgroup") }

func cgroupCPUQuotaAt(procCgroup, sysRoot string) bool {
	f, err := os.Open(procCgroup)
	if err != nil {
		return false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		// hierarchy-ID:controller-list:path
		parts := strings.SplitN(sc.Text(), ":", 3)
		if len(parts) != 3 {
			continue
		}
		switch {
		case parts[0] == "0" && parts[1] == "": // v2
			if b, err := os.ReadFile(filepath.Join(sysRoot, parts[2], "cpu.max")); err == nil {
				if q := strings.Fields(string(b)); len(q) > 0 && q[0] != "max" {
					return true
				}
			}
		case strings.Contains(","+parts[1]+",", ",cpu,"): // v1
			for _, base := range []string{filepath.Join(sysRoot, "cpu,cpuacct"), filepath.Join(sysRoot, "cpu")} {
				if b, err := os.ReadFile(filepath.Join(base, parts[2], "cpu.cfs_quota_us")); err == nil {
					if q := strings.TrimSpace(string(b)); q != "-1" && q != "" {
						return true
					}
					break
				}
			}
		}
	}
	return false
}

// Seams for tests: the process-wide setting, the environment, the CPUs this
// process may run on, and the cgroup's quota.
var (
	setGOMAXPROCS   = goruntime.GOMAXPROCS
	gomaxprocsInEnv = func() bool { return operatorGOMAXPROCS(os.Getenv("GOMAXPROCS")) }
	numCPU          = goruntime.NumCPU
	cpuQuota        = cgroupCPUQuota
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
		// Go's own default below the CPUs this process may use means a
		// CPU limit already lowered it; a quota is a limit even at the
		// core count.
		limited := fifo > 0 && (cur < numCPU() || cpuQuota())
		n, reason := procsFor(cur, fifo, gomaxprocsInEnv(), limited)
		st := ProcsStats{Value: cur, Reason: reason}
		if n != cur {
			setGOMAXPROCS(n)
			st = ProcsStats{Value: n, RaisedFrom: cur, Reason: reason}
			slog.Info("runtime: GOMAXPROCS raised so a task requesting a real-time priority is not kept waiting for a P",
				"from", cur, "to", n, "tasksRequestingPriority", fifo)
		}
		if limited {
			slog.Info("runtime: GOMAXPROCS not raised for a task at a priority", "value", cur, "reason", reason)
		}
		r.mu.Lock()
		r.stats.Procs = st
		r.mu.Unlock()
	})
}
