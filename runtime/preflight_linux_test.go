//go:build linux

package runtime

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// fakeProc is a /proc with the threads given: pid, tid, comm, flags,
// rt_priority, policy (1 FIFO, 0 OTHER), Cpus_allowed_list.
type fakeThread struct {
	pid, tid       int
	comm           string
	flags          uint64
	prio, policy   int
	cpusAllowedFor string
}

func fakeProc(t *testing.T, rtRuntime, rtPeriod string, threads ...fakeThread) string {
	t.Helper()
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "sys/kernel"), 0o755)
	os.WriteFile(filepath.Join(root, "sys/kernel/sched_rt_runtime_us"), []byte(rtRuntime+"\n"), 0o644)
	os.WriteFile(filepath.Join(root, "sys/kernel/sched_rt_period_us"), []byte(rtPeriod+"\n"), 0o644)
	os.WriteFile(filepath.Join(root, "sys/kernel/osrelease"), []byte("5.4.193-rt74\n"), 0o644)
	// This process may run on CPUs 0-1, like the bench controller.
	os.MkdirAll(filepath.Join(root, "self"), 0o755)
	os.WriteFile(filepath.Join(root, "self/status"), []byte("Cpus_allowed_list:\t0-1\n"), 0o644)
	os.WriteFile(filepath.Join(root, "self/mountinfo"), []byte("29 34 0:26 / /proc rw,nosuid,nodev,noexec,relatime shared:13 - proc proc rw\n"), 0o644)
	for _, th := range threads {
		dir := filepath.Join(root, fmt.Sprint(th.pid), "task", fmt.Sprint(th.tid))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		// stat: pid (comm) state ppid … — fields 4..8 filler, 9 flags,
		// 10..39 filler, 40 rt_priority, 41 policy.
		f := make([]string, 0, 42)
		f = append(f, "S") // field 3
		for i := 4; i <= 8; i++ {
			f = append(f, "0")
		}
		f = append(f, fmt.Sprint(th.flags)) // 9
		for i := 10; i <= 39; i++ {
			f = append(f, "0")
		}
		f = append(f, fmt.Sprint(th.prio), fmt.Sprint(th.policy), "0", "0") // 40, 41, …
		stat := fmt.Sprintf("%d (%s) %s\n", th.tid, th.comm, strings.Join(f, " "))
		os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o644)
		os.WriteFile(filepath.Join(dir, "status"), []byte("Name:\t"+th.comm+"\nCpus_allowed_list:\t"+th.cpusAllowedFor+"\n"), 0o644)
	}
	return root
}

const self = 4242

// The incident's exact layout: a vendor I/O shim at SCHED_FIFO 80 confined
// to CPU 1, and a task asking for priority 40 pinned to CPU 1. Refused,
// naming the shim, unless the task says share-cpu: true.
func TestPreflightRefusesTheIncidentLayout(t *testing.T) {
	root := fakeProc(t, "950000", "1000000",
		fakeThread{pid: 900, tid: 901, comm: "nautilus_io", prio: 80, policy: 1, cpusAllowedFor: "1"},
		fakeThread{pid: 900, tid: 900, comm: "nautilus_io", prio: 0, policy: 0, cpusAllowedFor: "0-1"},
	)
	warn, err := preflight(root, self, []int{1}, 40, false)
	if err == nil || !strings.Contains(err.Error(), "refused") || !strings.Contains(err.Error(), "nautilus_io (pid 900, thread 901) at real-time priority 80 on cpu 1") {
		t.Fatalf("the incident layout: got err %v (warnings %q), want a refusal naming the shim", err, warn)
	}
	warn, err = preflight(root, self, []int{1}, 40, true)
	if err != nil || len(warn) != 1 || !strings.Contains(warn[0], "share-cpu") {
		t.Fatalf("share-cpu: true: got err %v, warnings %q; want it allowed with a warning", err, warn)
	}
	if w, err := preflight(root, self, []int{0}, 40, false); err != nil || len(w) != 0 {
		t.Fatalf("the other CPU: %v %q", err, w)
	}
	// A higher thread confined to CPUs 1-2, a request pinned to both: refused.
	root = fakeProc(t, "950000", "1000000",
		fakeThread{pid: 910, tid: 911, comm: "vendor", prio: 90, policy: 1, cpusAllowedFor: "1-2"})
	if _, err := preflight(root, self, []int{1, 2}, 40, false); err == nil {
		t.Fatal("a higher thread confined to the requested CPUs must be refused")
	}
	// SCHED_DEADLINE outranks any FIFO priority.
	root = fakeProc(t, "950000", "1000000",
		fakeThread{pid: 920, tid: 921, comm: "dl", prio: 0, policy: 6, cpusAllowedFor: "1"})
	if _, err := preflight(root, self, []int{1}, 99, false); err == nil || !strings.Contains(err.Error(), "SCHED_DEADLINE") {
		t.Fatalf("a SCHED_DEADLINE thread confined to the CPU: %v, want refused", err)
	}
}

// What is warned about, not refused: a higher thread that may float to
// other CPUs (rtkit-daemon's RR 99 on every CPU of a desktop), one at the
// same or a lower priority confined to the requested CPU. Nothing at all
// for a lower one that floats (PipeWire's audio threads), kernel threads,
// this process's own, normal-priority threads, a request without a priority.
func TestPreflightWarnings(t *testing.T) {
	root := fakeProc(t, "950000", "1000000",
		fakeThread{pid: 12, tid: 12, comm: "migration/1", flags: pfKThread, prio: 99, policy: 1, cpusAllowedFor: "1"},
		fakeThread{pid: self, tid: self + 1, comm: "naut", prio: 90, policy: 1, cpusAllowedFor: "1"},
		fakeThread{pid: 700, tid: 700, comm: "sshd", prio: 0, policy: 0, cpusAllowedFor: "0-1"},
		fakeThread{pid: 750, tid: 751, comm: "rtkit-daemon", prio: 99, policy: 2, cpusAllowedFor: "0-31"},
		fakeThread{pid: 760, tid: 761, comm: "twin", prio: 40, policy: 1, cpusAllowedFor: "1"},
		fakeThread{pid: 800, tid: 801, comm: "lower) rt (x", prio: 20, policy: 2, cpusAllowedFor: "1"},
		fakeThread{pid: 850, tid: 851, comm: "AudioIPC", prio: 10, policy: 1, cpusAllowedFor: "0-31"},
	)
	warn, err := preflight(root, self, []int{1}, 40, false)
	if err != nil {
		t.Fatalf("refused: %v", err)
	}
	want := []string{
		"rtkit-daemon (pid 750, thread 751) at real-time priority 99 on cpu 0-31, a real-time thread of another program above this task's priority 40 that can move elsewhere",
		"twin (pid 760, thread 761) at real-time priority 40 on cpu 1, at or below",
		"lower) rt (x (pid 800, thread 801) at real-time priority 20 on cpu 1, at or below",
	}
	if len(warn) != len(want) {
		t.Fatalf("warnings %q, want %d", warn, len(want))
	}
	for i, w := range want {
		if !strings.Contains(warn[i], w) {
			t.Errorf("warning %d: %q, want it to say %q", i, warn[i], w)
		}
	}
	if w, err := preflight(root, self, []int{1}, 0, false); err != nil || w != nil {
		t.Fatalf("no priority: %v %q", err, w)
	}
}

// A task with a priority and no cpu: may run on any of the process's CPUs:
// a higher thread confined to one of them is a warning (never a refusal).
func TestPreflightUnpinnedWarns(t *testing.T) {
	root := fakeProc(t, "950000", "1000000",
		fakeThread{pid: 900, tid: 901, comm: "nautilus_io", prio: 80, policy: 1, cpusAllowedFor: "1"})
	warn, err := preflight(root, self, nil, 40, false)
	if err != nil || len(warn) != 1 || !strings.Contains(warn[0], "no cpu:, so it may run beside nautilus_io") {
		t.Fatalf("unpinned beside a confined higher thread: err %v, warnings %q", err, warn)
	}
	for _, cpus := range []string{"5", "0-1"} { // not ours; floats over all of ours
		root = fakeProc(t, "950000", "1000000",
			fakeThread{pid: 900, tid: 901, comm: "elsewhere", prio: 80, policy: 1, cpusAllowedFor: cpus})
		if warn, err := preflight(root, self, nil, 40, false); err != nil || len(warn) != 0 {
			t.Fatalf("a higher thread on cpu %s, this process on 0-1: %v %q", cpus, err, warn)
		}
	}
}

// What the scan cannot see is said: entries it may not read, and a /proc
// that hides other processes. A thread that exits mid-scan is not.
func TestPreflightSaysWhatItCannotSee(t *testing.T) {
	root := fakeProc(t, "950000", "1000000",
		fakeThread{pid: 900, tid: 901, comm: "nautilus_io", prio: 80, policy: 1, cpusAllowedFor: "1"})
	// Gone mid-scan: its directory is there, its stat is not.
	os.MkdirAll(filepath.Join(root, "950", "task", "951"), 0o755)
	if w, err := preflight(root, self, []int{0}, 40, false); err != nil || len(w) != 0 {
		t.Fatalf("a vanished thread must be skipped quietly: %v %q", err, w)
	}
	if os.Geteuid() != 0 {
		os.Chmod(filepath.Join(root, "900", "task", "901", "stat"), 0)
		w, _ := preflight(root, self, []int{0}, 40, false)
		if len(w) != 1 || !strings.Contains(w[0], "1 entries could not be read") {
			t.Fatalf("an unreadable entry: warnings %q, want it said", w)
		}
		os.Chmod(filepath.Join(root, "900", "task", "901", "stat"), 0o644)
	}
	os.WriteFile(filepath.Join(root, "self/mountinfo"), []byte("29 34 0:26 / /proc rw,nosuid - proc proc rw,hidepid=invisible\n"), 0o644)
	w, _ := preflight(root, self, []int{0}, 40, false)
	if len(w) != 1 || !strings.Contains(w[0], "hidepid=invisible") {
		t.Fatalf("/proc with hidepid: warnings %q, want it said", w)
	}
	if hiddenProc(fakeProc(t, "950000", "1000000")) != "" {
		t.Fatal("a plain /proc reported as hiding")
	}
}

// The kernel's real-time throttle off is warned about: -1 on every kernel
// (the fair server does not cover lower kernel FIFO threads), runtime =
// period only before 6.12 (with the fair server it is the normal setting).
func TestPreflightThrottleWarning(t *testing.T) {
	for _, c := range []struct {
		runtime, period, release string
		warn                     bool
	}{
		{"950000", "1000000", "5.4.193-rt74", false},
		{"-1", "1000000", "5.4.193-rt74", true},
		{"1000000", "1000000", "6.1.0", true},
		{"1000000", "1000000", "7.2.6-76070206-generic", false}, // fair server: the normal setting
		{"-1", "1000000", "6.12.0", true},                       // off on every kernel
	} {
		root := fakeProc(t, c.runtime, c.period)
		os.WriteFile(filepath.Join(root, "sys/kernel/osrelease"), []byte(c.release+"\n"), 0o644)
		if got := strings.Contains(throttleWarning(root), "throttle is off"); got != c.warn {
			t.Errorf("sched_rt_runtime_us %s / period %s on %s: warn %v, want %v", c.runtime, c.period, c.release, got, c.warn)
		}
	}
}

func TestPreflightCPUText(t *testing.T) {
	for in, want := range map[string]string{"0-31": "0-31", "1": "1", "0,2-3": "0,2-3", "3,1,2": "1-3"} {
		var cpus []int
		for _, part := range strings.Split(in, ",") {
			lo, hi, r := strings.Cut(part, "-")
			a, _ := strconv.Atoi(lo)
			b := a
			if r {
				b, _ = strconv.Atoi(hi)
			}
			for c := a; c <= b; c++ {
				cpus = append(cpus, c)
			}
		}
		if got := cpuText(cpus); got != want {
			t.Errorf("cpuText(%v) = %q, want %q", cpus, got, want)
		}
	}
}

// applySched runs the pre-flight first: on the incident's layout the
// thread is neither pinned nor raised, and the error is the refusal.
func TestPreflightApplySchedRefuses(t *testing.T) {
	old := procRoot
	procRoot = fakeProc(t, "950000", "1000000",
		fakeThread{pid: 900, tid: 901, comm: "nautilus_io", prio: 80, policy: 1, cpusAllowedFor: "1"})
	defer func() { procRoot = old }()

	goruntime.LockOSThread()
	defer goruntime.UnlockOSThread()
	before := affinityOfThisThread(t)
	err := applySched([]int{1}, 40, false, nil)
	if err == nil || !strings.Contains(err.Error(), "nautilus_io") {
		t.Fatalf("applySched on the incident layout: %v, want the pre-flight refusal", err)
	}
	if after := affinityOfThisThread(t); fmt.Sprint(after) != fmt.Sprint(before) {
		t.Fatalf("a refused request still pinned the thread: %v → %v", before, after)
	}
}

// The kernel's throttle is one setting for the whole box: warned about
// once per process, not once per task.
func TestPreflightThrottleWarnedOnce(t *testing.T) {
	old, oldLog := procRoot, slog.Default()
	procRoot = fakeProc(t, "-1", "1000000")
	throttleOnce = sync.Once{}
	var buf bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer func() { procRoot = old; slog.SetDefault(oldLog); throttleOnce = sync.Once{} }()
	for range 3 {
		func() {
			goruntime.LockOSThread()
			defer goruntime.UnlockOSThread()
			_ = applySched(nil, 1, false, nil)
			releaseSched()
		}()
	}
	if n := strings.Count(buf.String(), "throttle is off"); n != 1 {
		t.Fatalf("throttle warned %d times over three tasks, want once:\n%s", n, buf.String())
	}
}

// The PLCnext's network layout on the shim's core, as read off the bench
// after the incident (#836): the NIC's NAPI receive thread at SCHED_FIFO 1
// confined to CPU 1, its IRQ thread at FIFO 50 and stmmacRxTsk at FIFO 95
// there too, and ksoftirqd/1. A task pinned to CPU 1 at priority 40 is
// warned, naming the NAPI thread below it; not the ones above it, not a
// floating napi thread, not another CPU, and not ksoftirqd/N, which every
// CPU has.
func TestPreflightWarnsAboutNetworkThreadsBelow(t *testing.T) {
	root := fakeProc(t, "950000", "1000000",
		fakeThread{pid: 730, tid: 730, comm: "stmmacNapiRxTsk", flags: pfKThread, prio: 1, policy: 1, cpusAllowedFor: "1"},
		fakeThread{pid: 20, tid: 20, comm: "ksoftirqd/1", flags: pfKThread, prio: 0, policy: 0, cpusAllowedFor: "1"},
		fakeThread{pid: 18603, tid: 18603, comm: "irq/31-eth0", flags: pfKThread, prio: 50, policy: 1, cpusAllowedFor: "1"},
		fakeThread{pid: 729, tid: 729, comm: "stmmacRxTsk", flags: pfKThread, prio: 95, policy: 1, cpusAllowedFor: "1"},
		fakeThread{pid: 725, tid: 725, comm: "napi/eth1-8193", flags: pfKThread, prio: 0, policy: 0, cpusAllowedFor: "0-1"},
	)
	warn, err := preflight(root, self, []int{1}, 40, false)
	if err != nil {
		t.Fatalf("refused: %v", err)
	}
	var net []string
	for _, w := range warn {
		if strings.Contains(w, "kernel's network thread") {
			net = append(net, w)
		}
	}
	if len(net) != 1 || !strings.Contains(net[0], "stmmacNapiRxTsk (pid 730, thread 730) at real-time priority 1 on cpu 1") {
		t.Fatalf("network warnings %q, want exactly stmmacNapiRxTsk named", net)
	}
	if w, err := preflight(root, self, []int{0}, 40, false); err != nil || len(w) != 0 {
		t.Fatalf("CPU 0, where no network thread is confined: %v %q", err, w)
	}
	if !netKThread("irq/31-eth0") || !netKThread("napi/eth0-8193") || netKThread("ksoftirqd/3") || netKThread("migration/1") || netKThread("stmmac_wq") {
		t.Error("netKThread classifies network kernel threads by name")
	}
}
