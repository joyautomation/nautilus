//go:build linux

package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Pre-flight for a real-time request (#836): before a task is pinned to a
// CPU at SCHED_FIFO, look at what already runs real-time there.
//
// A PLCnext lost its network for good when a test task at FIFO 40 was
// pinned to the core the vendor I/O shim ran on at FIFO 80: lower priority
// than the shim, higher than the kernel and firmware work that core also
// carried. So a CPU that already carries a userspace real-time thread of
// another process at a HIGHER priority than the request is refused: that
// thread is a vendor runtime, a shim, something the box depends on, and
// putting our real-time work beside it is the incident's exact setup. The
// task runs unpinned at normal priority with the reason in its scan.sched,
// unless its manifest says share-cpu: true. One at the same or a lower
// priority is a warning. Kernel threads are left out: migration/N runs at
// FIFO 99 on every CPU, and IRQ threads are the kernel's to place.
//
// It also warns when the kernel's real-time throttle is off
// (sched_rt_runtime_us -1, or the whole period): then nothing at all stops
// a real-time thread that never sleeps from owning its core.

// procRoot is /proc; tests point it at a fake tree.
var procRoot = "/proc"

const pfKThread = 0x00200000 // PF_KTHREAD in /proc/<pid>/stat's flags

// rtThread is one real-time thread pre-flight found.
type rtThread struct {
	pid, tid int
	comm     string
	prio     int
	cpus     []int
}

// preflight checks a request to pin to cpus at prio. It returns warnings to
// log and, unless shareCPU, an error when a CPU of cpus carries a userspace
// real-time thread of another process above prio.
func preflight(root string, self int, cpus []int, prio int, shareCPU bool) (warnings []string, refuse error) {
	if prio <= 0 {
		return nil, nil
	}
	if w := throttleWarning(root); w != "" {
		warnings = append(warnings, w)
	}
	if len(cpus) == 0 {
		return warnings, nil
	}
	want := map[int]bool{}
	for _, c := range cpus {
		want[c] = true
	}
	var above []string
	for _, th := range rtThreads(root, self) {
		shared := false
		for _, c := range th.cpus {
			if want[c] {
				shared = true
				break
			}
		}
		if !shared {
			continue
		}
		what := fmt.Sprintf("%s (pid %d, thread %d) at real-time priority %d on cpu %s", th.comm, th.pid, th.tid, th.prio, cpuText(th.cpus))
		if th.prio > prio {
			above = append(above, what)
		} else {
			warnings = append(warnings, "cpu "+cpuText(cpus)+" already runs "+what+", at or below this task's priority "+strconv.Itoa(prio)+": they will take turns")
		}
	}
	if len(above) == 0 {
		return warnings, nil
	}
	if shareCPU {
		return append(warnings, "share-cpu: pinning to cpu "+cpuText(cpus)+" beside "+strings.Join(above, "; ")), nil
	}
	return warnings, fmt.Errorf("pin to cpu %s at priority %d: refused, that CPU already runs %s, a real-time thread of another program above this task (a vendor runtime or I/O shim, most likely); real-time work beside it can starve what that core also carries. Pin to another CPU, or set share-cpu: true on the task to do it anyway; the task is running unpinned at normal priority",
		cpuText(cpus), prio, strings.Join(above, "; "))
}

// throttleWarning is a warning when the kernel's real-time throttle is off.
func throttleWarning(root string) string {
	rt, err1 := readInt(filepath.Join(root, "sys/kernel/sched_rt_runtime_us"))
	period, err2 := readInt(filepath.Join(root, "sys/kernel/sched_rt_period_us"))
	if err1 != nil {
		return ""
	}
	if rt < 0 || (err2 == nil && rt >= period) {
		return fmt.Sprintf("the kernel's real-time throttle is off (sched_rt_runtime_us %d): nothing but nautilus's own guards stops a real-time thread that never sleeps from owning its core", rt)
	}
	return ""
}

// rtThreads lists the userspace SCHED_FIFO and SCHED_RR threads of other
// processes. Unreadable entries (a thread that exited, another user's
// process) are skipped: pre-flight is best effort, never a reason to fail.
func rtThreads(root string, self int) []rtThread {
	pids, _ := os.ReadDir(root)
	var out []rtThread
	for _, p := range pids {
		pid, err := strconv.Atoi(p.Name())
		if err != nil || pid == self {
			continue
		}
		tids, _ := os.ReadDir(filepath.Join(root, p.Name(), "task"))
		for _, t := range tids {
			tid, err := strconv.Atoi(t.Name())
			if err != nil {
				continue
			}
			dir := filepath.Join(root, p.Name(), "task", t.Name())
			comm, flags, prio, policy, ok := parseStat(dir)
			if !ok || flags&pfKThread != 0 || (policy != 1 && policy != 2) || prio <= 0 {
				continue
			}
			cpus, ok := allowedCPUs(dir)
			if !ok {
				continue
			}
			out = append(out, rtThread{pid: pid, tid: tid, comm: comm, prio: prio, cpus: cpus})
		}
	}
	return out
}

// parseStat reads comm, flags (field 9), rt_priority (40) and policy (41)
// from a stat file. comm is in parentheses and may hold spaces and ')', so
// the fields after it are split from the LAST ')'.
func parseStat(dir string) (comm string, flags uint64, prio, policy int, ok bool) {
	b, err := os.ReadFile(filepath.Join(dir, "stat"))
	if err != nil {
		return "", 0, 0, 0, false
	}
	s := string(b)
	open, close := strings.IndexByte(s, '('), strings.LastIndexByte(s, ')')
	if open < 0 || close < open {
		return "", 0, 0, 0, false
	}
	comm = s[open+1 : close]
	f := strings.Fields(s[close+1:]) // f[0] is field 3 (state)
	if len(f) < 39 {
		return "", 0, 0, 0, false
	}
	flags, err1 := strconv.ParseUint(f[9-3], 10, 64)
	prio, err2 := strconv.Atoi(f[40-3])
	policy, err3 := strconv.Atoi(f[41-3])
	if err1 != nil || err2 != nil || err3 != nil {
		return "", 0, 0, 0, false
	}
	return comm, flags, prio, policy, true
}

// allowedCPUs reads Cpus_allowed_list ("0-1,3") from a status file.
func allowedCPUs(dir string) ([]int, bool) {
	b, err := os.ReadFile(filepath.Join(dir, "status"))
	if err != nil {
		return nil, false
	}
	for _, line := range strings.Split(string(b), "\n") {
		v, found := strings.CutPrefix(line, "Cpus_allowed_list:")
		if !found {
			continue
		}
		var cpus []int
		for _, part := range strings.Split(strings.TrimSpace(v), ",") {
			lo, hi, isRange := strings.Cut(part, "-")
			a, err := strconv.Atoi(lo)
			if err != nil {
				return nil, false
			}
			b := a
			if isRange {
				if b, err = strconv.Atoi(hi); err != nil {
					return nil, false
				}
			}
			for c := a; c <= b; c++ {
				cpus = append(cpus, c)
			}
		}
		return cpus, true
	}
	return nil, false
}

func readInt(path string) (int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(b)))
}

func cpuText(cpus []int) string {
	s := make([]string, len(cpus))
	for i, c := range cpus {
		s[i] = strconv.Itoa(c)
	}
	return strings.Join(s, ",")
}
