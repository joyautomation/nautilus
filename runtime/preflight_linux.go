//go:build linux

package runtime

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

// Pre-flight for a real-time request (#836): before a task's thread is
// raised to SCHED_FIFO, look at what already runs real-time where it is
// going. Once, as each loop starts; best effort.
//
// A PLCnext lost its network for good when a test task at FIFO 40 was
// pinned to the core the vendor I/O shim ran on at FIFO 80: lower priority
// than the shim, higher than the kernel and firmware work that core also
// carried. So a request pinned to CPUs where another program's userspace
// real-time thread above it is CONFINED (its affinity lies within those
// CPUs: a vendor runtime or shim pinned to its core) is refused: the task
// runs unpinned at normal priority with the reason in its scan.sched,
// unless its manifest says share-cpu: true. A higher thread that may float
// elsewhere too (rtkit-daemon's RR 99, allowed on every CPU of a desktop)
// is a warning, as is one at the same or a lower priority, and, for a task
// with a priority but no cpu:, a confined higher thread on a CPU the
// process may run on. SCHED_DEADLINE threads count as above any FIFO.
// Kernel threads are left out: migration/N runs at FIFO 99 on every CPU,
// and IRQ threads are the kernel's to place.
//
// What it cannot see is said, not passed as safe: a /proc mounted with
// hidepid or subset, or entries it may not read. Inside a PID namespace it
// sees only that namespace (the docs say so).

// procRoot is /proc; tests point it at a fake tree.
var procRoot = "/proc"

const (
	pfKThread     = 0x00200000 // PF_KTHREAD in /proc/<pid>/stat's flags
	schedRR       = 2
	schedDeadline = 6
	deadlinePrio  = 100 // a SCHED_DEADLINE thread outranks every FIFO/RR priority
)

// rtThread is one real-time thread pre-flight found.
type rtThread struct {
	pid, tid int
	comm     string
	prio     int // deadlinePrio for SCHED_DEADLINE
	cpus     []int
}

func (th rtThread) String() string {
	p := "real-time priority " + strconv.Itoa(th.prio)
	switch th.prio {
	case deadlinePrio:
		p = "SCHED_DEADLINE"
	case 0:
		p = "normal priority"
	}
	return fmt.Sprintf("%s (pid %d, thread %d) at %s on cpu %s", th.comm, th.pid, th.tid, p, cpuText(th.cpus))
}

// preflight checks a request to run at prio, pinned to cpus or (none) on
// the process's own CPUs. It returns warnings to log and, unless shareCPU,
// an error when cpus are where another program's higher real-time thread
// is confined.
func preflight(root string, self int, cpus []int, prio int, shareCPU bool) (warnings []string, refuse error) {
	if prio <= 0 {
		return nil, nil
	}
	threads, net, blind := rtThreads(root, self)
	if blind != "" {
		warnings = append(warnings, blind)
	}
	pinned := len(cpus) > 0
	// The kernel's network threads confined to the CPUs this task is pinned
	// to, below its priority: the task can starve them. On the PLCnext the
	// NIC's NAPI receive thread runs at FIFO 1 on the shim's core, and a
	// FIFO 40 test task there left the box off the network for hours (#836).
	if pinned {
		for _, th := range net {
			if subset(th.cpus, setOf(cpus)) && th.prio < prio {
				warnings = append(warnings, fmt.Sprintf("cpu %s carries the kernel's network thread %s, below this task's priority %d: while the task runs, network traffic on this box waits (receive can stall altogether). Pin the task to a CPU without network work", cpuText(cpus), th, prio))
			}
		}
	}
	where := cpus
	if !pinned {
		where = processCPUs(root)
	}
	in := setOf(where)
	var above []string
	for _, th := range threads {
		if !overlaps(th.cpus, in) {
			continue
		}
		switch {
		case pinned:
			confined := subset(th.cpus, in) // it runs only where we are going
			switch {
			case th.prio > prio && confined:
				above = append(above, th.String())
			case th.prio > prio:
				warnings = append(warnings, fmt.Sprintf("cpu %s may also run %s, a real-time thread of another program above this task's priority %d that can move elsewhere too", cpuText(cpus), th, prio))
			case confined:
				warnings = append(warnings, fmt.Sprintf("cpu %s already runs %s, at or below this task's priority %d: they will take turns", cpuText(cpus), th, prio))
			}
			// A lower thread that may run anywhere is no concern.
		case th.prio > prio && subset(th.cpus, in) && len(setOf(th.cpus)) < len(in):
			// Unpinned: a higher thread pinned to some of our CPUs (a shim
			// on its core) is where this task may land; one that floats
			// over all of them is everyone's neighbour already.
			warnings = append(warnings, fmt.Sprintf("this task has priority %d and no cpu:, so it may run beside %s, a real-time thread of another program above it; pin it to another CPU", prio, th))
		}
	}
	if len(above) == 0 {
		return warnings, nil
	}
	if shareCPU {
		return append(warnings, "share-cpu: pinning to cpu "+cpuText(cpus)+" beside "+strings.Join(above, "; ")), nil
	}
	return warnings, fmt.Errorf("pin to cpu %s at priority %d: refused, that CPU is where %s, a real-time thread of another program above this task, is pinned (a vendor runtime or I/O shim, most likely); real-time work beside it can starve what that core also carries. Pin to another CPU, or set share-cpu: true on the task to do it anyway; the task is running unpinned at normal priority",
		cpuText(cpus), prio, strings.Join(above, "; "))
}

// throttleWarning is a warning when the kernel's real-time throttle is off.
// sched_rt_runtime_us -1 turns it off on every kernel: from 6.12 the fair
// server still reserves normal tasks a share, but not a lower-priority
// kernel FIFO thread on the core. runtime = period is "off" only before
// 6.12; with the fair server it is the normal setting.
func throttleWarning(root string) string {
	rt, err1 := readInt(filepath.Join(root, "sys/kernel/sched_rt_runtime_us"))
	period, err2 := readInt(filepath.Join(root, "sys/kernel/sched_rt_period_us"))
	if err1 != nil {
		return ""
	}
	fairServer := false
	if v, err := os.ReadFile(filepath.Join(root, "sys/kernel/osrelease")); err == nil {
		fairServer = kernelAtLeast(string(v), 6, 12)
	}
	if rt < 0 || (err2 == nil && rt >= period && !fairServer) {
		return fmt.Sprintf("the kernel's real-time throttle is off (sched_rt_runtime_us %d): nothing but nautilus's own guards stops a real-time thread that never sleeps from owning its core", rt)
	}
	return ""
}

func kernelAtLeast(release string, major, minor int) bool {
	parts := strings.SplitN(strings.TrimSpace(release), ".", 3)
	if len(parts) < 2 {
		return false
	}
	ma, err1 := strconv.Atoi(parts[0])
	mi, err2 := strconv.Atoi(strings.TrimFunc(parts[1], func(r rune) bool { return r < '0' || r > '9' }))
	if err1 != nil || err2 != nil {
		return false
	}
	return ma > major || ma == major && mi >= minor
}

// rtThreads lists the userspace SCHED_FIFO, SCHED_RR and SCHED_DEADLINE
// threads of other processes. A thread that exited meanwhile is skipped
// quietly; anything it may not read, or a /proc that hides processes, comes
// back as blind: the scan cannot vouch for what it did not see.
func rtThreads(root string, self int) (out, net []rtThread, blind string) {
	var reasons []string
	if opt := hiddenProc(root); opt != "" {
		reasons = append(reasons, "/proc is mounted with "+opt+", which hides other programs' processes")
	}
	pids, err := os.ReadDir(root)
	if err != nil {
		return nil, nil, "could not list " + root + " (" + err.Error() + "): no other program's real-time threads were checked"
	}
	denied := 0
	for _, p := range pids {
		pid, err := strconv.Atoi(p.Name())
		if err != nil || pid == self {
			continue
		}
		tids, err := os.ReadDir(filepath.Join(root, p.Name(), "task"))
		if err != nil {
			denied += countDenied(err)
			continue
		}
		for _, t := range tids {
			tid, err := strconv.Atoi(t.Name())
			if err != nil {
				continue
			}
			dir := filepath.Join(root, p.Name(), "task", t.Name())
			comm, flags, prio, policy, err := parseStat(dir)
			if err != nil {
				denied += countDenied(err)
				continue
			}
			if flags&pfKThread != 0 {
				// Kernel threads are the kernel's to place, except that
				// a network one below us on our CPU is worth a warning.
				if netKThread(comm) {
					// rt_priority is 0 for a normal-priority thread.
					if cpus, err := allowedCPUs(dir); err == nil {
						net = append(net, rtThread{pid: pid, tid: tid, comm: comm, prio: prio, cpus: cpus})
					}
				}
				continue
			}
			switch {
			case policy == schedDeadline:
				prio = deadlinePrio
			case (policy == schedFIFO || policy == schedRR) && prio > 0:
			default:
				continue
			}
			cpus, err := allowedCPUs(dir)
			if err != nil {
				denied += countDenied(err)
				continue
			}
			out = append(out, rtThread{pid: pid, tid: tid, comm: comm, prio: prio, cpus: cpus})
		}
	}
	if denied > 0 {
		reasons = append(reasons, fmt.Sprintf("%d entries could not be read (permission)", denied))
	}
	if len(reasons) > 0 {
		blind = "the real-time pre-flight could not see every program: " + strings.Join(reasons, "; ") + "; a vendor's real-time thread there would not be found"
	}
	return out, net, blind
}

// netKThread: a kernel thread doing a NIC's work: an Ethernet IRQ thread
// (irq/31-eth0) or a NAPI poll thread (stmmacNapiRxTsk, napi/eth0-…).
// ksoftirqd/N is not counted: it is on every CPU of every box, so a pinned
// real-time task always outranks one, and a warning on every start would be
// ignored, the real one with it. The real-time guide says so instead.
func netKThread(comm string) bool {
	c := strings.ToLower(comm)
	return strings.HasPrefix(c, "irq/") && strings.Contains(c, "-eth") ||
		strings.Contains(c, "napi")
}

// countDenied is 1 for an error that hides a thread from us (permission),
// 0 for one that only means it is gone (ENOENT, ESRCH).
func countDenied(err error) int {
	if errors.Is(err, fs.ErrPermission) || errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM) {
		return 1
	}
	return 0
}

// hiddenProc is the option /proc is mounted with that hides processes
// (hidepid=1|2|invisible|noaccess, subset=pid), from /proc/self/mountinfo.
func hiddenProc(root string) string {
	b, err := os.ReadFile(filepath.Join(root, "self/mountinfo"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 5 || f[4] != "/proc" {
			continue
		}
		sep := -1
		for i, x := range f {
			if x == "-" {
				sep = i
			}
		}
		if sep < 0 || sep+3 >= len(f) || f[sep+1] != "proc" {
			continue
		}
		for _, o := range strings.Split(f[sep+3], ",") {
			if o == "subset=pid" || (strings.HasPrefix(o, "hidepid=") && o != "hidepid=0" && o != "hidepid=off") {
				return o
			}
		}
	}
	return ""
}

// processCPUs is the CPUs this process may run on.
func processCPUs(root string) []int {
	cpus, err := allowedCPUs(filepath.Join(root, "self"))
	if err != nil {
		return nil
	}
	return cpus
}

// parseStat reads comm, flags (field 9), rt_priority (40) and policy (41)
// from a stat file. comm is in parentheses and may hold spaces and ')', so
// the fields after it are split from the LAST ')'.
func parseStat(dir string) (comm string, flags uint64, prio, policy int, err error) {
	b, err := os.ReadFile(filepath.Join(dir, "stat"))
	if err != nil {
		return "", 0, 0, 0, err
	}
	s := string(b)
	open, close := strings.IndexByte(s, '('), strings.LastIndexByte(s, ')')
	if open < 0 || close < open {
		return "", 0, 0, 0, errors.New("unparsable stat")
	}
	comm = s[open+1 : close]
	f := strings.Fields(s[close+1:]) // f[0] is field 3 (state)
	if len(f) < 39 {
		return "", 0, 0, 0, errors.New("short stat")
	}
	flags, err1 := strconv.ParseUint(f[9-3], 10, 64)
	prio, err2 := strconv.Atoi(f[40-3])
	policy, err3 := strconv.Atoi(f[41-3])
	if err1 != nil || err2 != nil || err3 != nil {
		return "", 0, 0, 0, errors.New("unparsable stat")
	}
	return comm, flags, prio, policy, nil
}

// allowedCPUs reads Cpus_allowed_list ("0-1,3") from a status file.
func allowedCPUs(dir string) ([]int, error) {
	b, err := os.ReadFile(filepath.Join(dir, "status"))
	if err != nil {
		return nil, err
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
				return nil, err
			}
			b := a
			if isRange {
				if b, err = strconv.Atoi(hi); err != nil {
					return nil, err
				}
			}
			for c := a; c <= b; c++ {
				cpus = append(cpus, c)
			}
		}
		return cpus, nil
	}
	return nil, errors.New("no Cpus_allowed_list")
}

func readInt(path string) (int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(b)))
}

func setOf(cpus []int) map[int]bool {
	m := make(map[int]bool, len(cpus))
	for _, c := range cpus {
		m[c] = true
	}
	return m
}

func overlaps(cpus []int, in map[int]bool) bool {
	for _, c := range cpus {
		if in[c] {
			return true
		}
	}
	return false
}

// subset: every CPU of cpus is in in (and there is at least one).
func subset(cpus []int, in map[int]bool) bool {
	if len(cpus) == 0 {
		return false
	}
	for _, c := range cpus {
		if !in[c] {
			return false
		}
	}
	return true
}

// cpuText prints a CPU list as ranges: 0-31, 1, 0,2-3.
func cpuText(cpus []int) string {
	c := append([]int(nil), cpus...)
	sort.Ints(c)
	var parts []string
	for i := 0; i < len(c); {
		j := i
		for j+1 < len(c) && c[j+1] == c[j]+1 {
			j++
		}
		if j > i {
			parts = append(parts, strconv.Itoa(c[i])+"-"+strconv.Itoa(c[j]))
		} else {
			parts = append(parts, strconv.Itoa(c[i]))
		}
		i = j + 1
	}
	return strings.Join(parts, ",")
}
