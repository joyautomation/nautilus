//go:build linux

package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
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

// The incident's exact layout: a vendor I/O shim at SCHED_FIFO 80 allowed
// only on CPU 1, and a task asking for priority 40 pinned to CPU 1. Refused,
// naming the shim, unless the task says share-cpu: true.
func TestPreflightRefusesTheIncidentLayout(t *testing.T) {
	root := fakeProc(t, "950000", "1000000",
		fakeThread{pid: 900, tid: 901, comm: "nautilus_io", prio: 80, policy: 1, cpusAllowedFor: "1"},
		fakeThread{pid: 900, tid: 900, comm: "nautilus_io", prio: 0, policy: 0, cpusAllowedFor: "0-1"},
	)
	warn, err := preflight(root, self, []int{1}, 40, false)
	if err == nil || !strings.Contains(err.Error(), "refused") || !strings.Contains(err.Error(), "nautilus_io (pid 900, thread 901) at real-time priority 80") {
		t.Fatalf("the incident layout: got err %v (warnings %q), want a refusal naming the shim", err, warn)
	}
	warn, err = preflight(root, self, []int{1}, 40, true)
	if err != nil || len(warn) != 1 || !strings.Contains(warn[0], "share-cpu") {
		t.Fatalf("share-cpu: true: got err %v, warnings %q; want it allowed with a warning", err, warn)
	}
	if _, err := preflight(root, self, []int{0}, 40, false); err != nil {
		t.Fatalf("the other CPU: %v", err)
	}
}

// What is not refused: kernel threads (migration/N is FIFO 99 on every
// CPU), this process's own threads, normal-priority threads, a request
// without a priority or without a pin; a real-time thread at or below the
// request's priority is a warning.
func TestPreflightWhatIsNotRefused(t *testing.T) {
	root := fakeProc(t, "950000", "1000000",
		fakeThread{pid: 12, tid: 12, comm: "migration/1", flags: pfKThread, prio: 99, policy: 1, cpusAllowedFor: "1"},
		fakeThread{pid: self, tid: self + 1, comm: "naut", prio: 90, policy: 1, cpusAllowedFor: "1"},
		fakeThread{pid: 700, tid: 700, comm: "sshd", prio: 0, policy: 0, cpusAllowedFor: "0-1"},
		fakeThread{pid: 800, tid: 801, comm: "lower) rt (x", prio: 20, policy: 2, cpusAllowedFor: "0-3"},
	)
	warn, err := preflight(root, self, []int{1}, 40, false)
	if err != nil {
		t.Fatalf("refused: %v", err)
	}
	if len(warn) != 1 || !strings.Contains(warn[0], "lower) rt (x (pid 800, thread 801) at real-time priority 20") {
		t.Fatalf("warnings %q, want one for the lower-priority real-time thread (comm parsed past its ')')", warn)
	}
	if w, err := preflight(root, self, []int{1}, 0, false); err != nil || w != nil {
		t.Fatalf("no priority: %v %q", err, w)
	}
	if w, err := preflight(root, self, nil, 40, false); err != nil || len(w) != 0 {
		t.Fatalf("unpinned: %v %q", err, w)
	}
}

// The kernel's real-time throttle off (-1, or the whole period) is warned
// about; on, it is not.
func TestPreflightThrottleWarning(t *testing.T) {
	for _, c := range []struct {
		runtime, period string
		warn            bool
	}{{"950000", "1000000", false}, {"-1", "1000000", true}, {"1000000", "1000000", true}} {
		w, _ := preflight(fakeProc(t, c.runtime, c.period), self, nil, 40, false)
		if got := len(w) == 1 && strings.Contains(w[0], "throttle is off"); got != c.warn {
			t.Errorf("sched_rt_runtime_us %s / period %s: warnings %q, want throttle warning %v", c.runtime, c.period, w, c.warn)
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
