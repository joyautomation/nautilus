//go:build linux

package runtime

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	goruntime "runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// A stricter limit already in force (set by a launcher, say) is kept, and
// the soft limit never exceeds the hard one.
func TestRTGuardLimits(t *testing.T) {
	inf := uint64(1<<64 - 1) // RLIM_INFINITY
	for _, c := range []struct {
		cur, want syscall.Rlimit
	}{
		{syscall.Rlimit{Cur: inf, Max: inf}, syscall.Rlimit{Cur: 200000, Max: 500000}},
		{syscall.Rlimit{Cur: 100000, Max: inf}, syscall.Rlimit{Cur: 100000, Max: 500000}},
		{syscall.Rlimit{Cur: inf, Max: 150000}, syscall.Rlimit{Cur: 150000, Max: 150000}},
	} {
		if got := rtLimits(c.cur, DefaultRTLimit); got != c.want {
			t.Errorf("in force %+v: set %+v, want %+v", c.cur, got, c.want)
		}
	}
}

// Arming the guard sets RLIMIT_RTTIME on the process (a lower limit needs
// no privilege, so this runs anywhere). In a child with
// NAUTILUS_RT_LIMIT_MS=50, stricter than any limit a shell or launcher
// already set: the limit must come out 50 ms soft, 125 ms hard.
func TestRTGuardSetsTheLimit(t *testing.T) {
	if os.Getenv("NAUT_RTGUARD_LIMIT_CHILD") == "1" {
		if err := armRTGuard(); err != nil {
			fmt.Println("ARM:", err)
			return
		}
		var lim syscall.Rlimit
		if err := syscall.Getrlimit(rlimitRTTime, &lim); err != nil {
			fmt.Println("GET:", err)
			return
		}
		fmt.Printf("LIMIT %d %d\n", lim.Cur, lim.Max)
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestRTGuardSetsTheLimit$", "-test.count=1")
	cmd.Env = append(os.Environ(), "NAUT_RTGUARD_LIMIT_CHILD=1", "NAUTILUS_RT_LIMIT_MS=50")
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "LIMIT 50000 125000\n") {
		t.Fatalf("want RLIMIT_RTTIME 50000/125000 µs after arming, got (err %v):\n%s", err, out)
	}
}

// SIGXCPU, which the kernel sends when a thread passes the soft limit,
// demotes every registered thread and tells its task why. Signalled by
// hand here, so it runs without real-time privilege.
func TestRTGuardSIGXCPUDemotes(t *testing.T) {
	if err := armRTGuard(); err != nil {
		t.Fatal(err)
	}
	told := make(chan string, 1)
	tid := make(chan int)
	release := make(chan struct{})
	defer close(release)
	go func() { // a thread of its own to register, as a task loop's
		goruntime.LockOSThread()
		defer goruntime.UnlockOSThread()
		tid <- syscall.Gettid()
		<-release
	}()
	rtGuard.mu.Lock()
	rtGuard.threads[<-tid] = func(reason string) { told <- reason }
	rtGuard.mu.Unlock()
	if err := syscall.Kill(os.Getpid(), syscall.SIGXCPU); err != nil {
		t.Fatal(err)
	}
	select {
	case reason := <-told:
		if !strings.Contains(reason, "RLIMIT_RTTIME") {
			t.Errorf("reason %q should name the limit", reason)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SIGXCPU did not demote the registered thread")
	}
}

// The real thing: a thread at SCHED_FIFO that never blocks is moved to
// normal scheduling once it passes the soft limit (50 ms here), well before
// the hard limit's SIGKILL. It needs CAP_SYS_NICE (CI grants it to the test
// binary with setcap and sets NAUTILUS_REQUIRE_RT=1, so there a missing
// capability fails rather than skips); elsewhere it skips.
func TestRTGuardStopsASpinningFIFOThread(t *testing.T) {
	if os.Getenv("NAUT_RTGUARD_CHILD") == "1" {
		spinAtFIFO()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRTGuardStopsASpinningFIFOThread$", "-test.count=1")
	// asyncpreemptoff: Go preempts a spinning goroutine about every 10 ms,
	// and a locked thread then waits on a futex for a moment, which resets
	// RLIMIT_RTTIME's count; without it the limit trips only when a
	// preemption happens to miss (seconds, measured). The child's spin has
	// no calls, so with it the thread runs flat out at FIFO, as a runaway in
	// code Go cannot preempt would.
	cmd.Env = append(os.Environ(), "NAUT_RTGUARD_CHILD=1", "NAUTILUS_RT_LIMIT_MS=50", "GODEBUG=asyncpreemptoff=1")
	out, err := cmd.CombinedOutput()
	text := string(out)
	switch {
	case strings.Contains(text, "NOFIFO:"):
		msg := "no SCHED_FIFO here (needs CAP_SYS_NICE): " + text
		if os.Getenv("NAUTILUS_REQUIRE_RT") == "1" {
			t.Fatal("NAUTILUS_REQUIRE_RT=1 but " + msg)
		}
		t.Skip(msg)
	case strings.Contains(text, "DEMOTED") && err == nil:
		t.Log(strings.TrimSpace(text))
	case ctx.Err() != nil:
		t.Fatalf("the spinning FIFO thread was never stopped:\n%s", text)
	default:
		if ee, ok := err.(*exec.ExitError); ok {
			if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() && ws.Signal() == syscall.SIGKILL {
				t.Fatalf("the hard limit killed the process: the soft-limit demotion did not happen:\n%s", text)
			}
		}
		t.Fatalf("child: %v\n%s", err, text)
	}
}

// spinAtFIFO is the child: one locked thread at SCHED_FIFO 1 spinning,
// with no calls and so no preemption point, until it is demoted. The parent
// bounds it at 5 s; the hard limit, at 125 ms.
func spinAtFIFO() {
	var demoted atomic.Bool
	done := make(chan struct{})
	go func() {
		goruntime.LockOSThread()
		var err error
		if os.Getenv("NAUT_RTGUARD_FORCE_NOFIFO") == "1" {
			err = fmt.Errorf("forced by the test")
		} else {
			err = applySched(nil, 1, func(string) { demoted.Store(true) })
		}
		if err != nil {
			fmt.Println("NOFIFO:", err)
			close(done)
			return
		}
		start := time.Now()
		for !demoted.Load() {
		}
		// Told is not enough: the thread itself must have left SCHED_FIFO.
		if policy, _, _ := syscall.RawSyscall(syscall.SYS_SCHED_GETSCHEDULER, 0, 0, 0); policy != schedOther {
			fmt.Println("STILL FIFO after the demotion: policy", policy)
			os.Exit(1)
		}
		fmt.Println("DEMOTED after", time.Since(start).Round(time.Millisecond))
		close(done)
	}()
	<-done
}

// NAUTILUS_REQUIRE_RT=1 turns the spinning test's skip into a failure, so
// a CI step whose setcap silently did not take cannot pass by skipping.
func TestRTGuardRequireFlag(t *testing.T) {
	if os.Getenv("NAUT_RTGUARD_CHILD") == "1" || os.Getenv("NAUT_RTGUARD_META") == "1" {
		t.Skip("inside a child")
	}
	run := func(require string) (string, error) {
		cmd := exec.Command(os.Args[0], "-test.run=^TestRTGuardStopsASpinningFIFOThread$", "-test.count=1", "-test.v")
		cmd.Env = append(os.Environ(), "NAUT_RTGUARD_FORCE_NOFIFO=1", "NAUT_RTGUARD_META=1", "NAUTILUS_REQUIRE_RT="+require)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	if out, err := run("1"); err == nil || !strings.Contains(out, "NAUTILUS_REQUIRE_RT=1 but") {
		t.Errorf("required and no FIFO: want a failure, got err %v:\n%s", err, out)
	}
	if out, err := run(""); err != nil || !strings.Contains(out, "--- SKIP") {
		t.Errorf("not required and no FIFO: want a skip, got err %v:\n%s", err, out)
	}
}

// A loop's thread goes back to Go when the loop ends; a thread left at
// SCHED_FIFO would then run any goroutine at real-time priority. runLoop
// releases what its setup registered (the registry stands in for FIFO,
// which needs privilege).
func TestRTGuardLoopEndReleasesItsThread(t *testing.T) {
	if err := armRTGuard(); err != nil {
		t.Fatal(err)
	}
	var tid int
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runLoop(ctx, time.Millisecond, func(time.Time) { cancel() }, func(uint64) {}, func() {
			tid = syscall.Gettid()
			rtGuard.mu.Lock()
			rtGuard.threads[tid] = nil
			rtGuard.mu.Unlock()
		})
		close(done)
	}()
	<-done
	rtGuard.mu.Lock()
	_, still := rtGuard.threads[tid]
	rtGuard.mu.Unlock()
	if still {
		t.Fatal("the loop ended with its thread still registered at real-time priority")
	}
}

// setOther reports a failure (it is logged by its callers) instead of
// letting the stats say "applied: false" over a thread still at FIFO.
func TestRTGuardSetOtherReportsFailure(t *testing.T) {
	if err := setOther(1 << 30); err == nil { // no such thread
		t.Fatal("setOther on a thread that does not exist reported success")
	}
	if err := setOther(syscall.Gettid()); err != nil {
		t.Fatalf("setOther on this (normal) thread: %v", err)
	}
}

// demoteTid (the priority guard's path) moves one registered thread, tells
// its task, and leaves the others; a second call finds nothing.
func TestRTGuardDemoteOneThread(t *testing.T) {
	if err := armRTGuard(); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	defer close(release)
	tids := make(chan int, 2)
	for range 2 {
		go func() {
			goruntime.LockOSThread()
			defer goruntime.UnlockOSThread()
			tids <- syscall.Gettid()
			<-release
		}()
	}
	a, b := <-tids, <-tids
	var toldA, toldB atomic.Bool
	rtGuard.mu.Lock()
	rtGuard.threads[a] = func(string) { toldA.Store(true) }
	rtGuard.threads[b] = func(string) { toldB.Store(true) }
	rtGuard.mu.Unlock()
	defer func() { rtGuard.mu.Lock(); delete(rtGuard.threads, b); rtGuard.mu.Unlock() }()
	if !demoteTid(a, "test") || !toldA.Load() {
		t.Fatal("demoteTid did not demote and tell the registered thread")
	}
	if toldB.Load() {
		t.Fatal("demoteTid touched another thread")
	}
	if demoteTid(a, "test") || demoteTid(0, "test") {
		t.Fatal("a thread demoted already, or none, must report false")
	}
}

// The priority guard against real SCHED_FIFO (CI's setcap'd step): a main
// task that overruns its period every scan loses the priority after its
// guard's count; a task whose one scan runs far past its period loses it
// while that scan is still running. Each thread must be back at
// SCHED_OTHER, not only reported so.
func TestRTGuardPriorityWatchdog(t *testing.T) {
	// Main: a loop the VM runs for milliseconds, far over its 1 ms period.
	// Stuck: one scan of hundreds of milliseconds, far past 4 × 5 ms, but
	// short enough that Run returns soon after the test cancels it.
	const busy = "FOR i := 1 TO 300000 DO x := x + 1; END_FOR;"
	r, err := New(Options{
		Program:  "PROGRAM Main\nVAR x : DINT; i : DINT; END_VAR\n" + busy + "\nEND_PROGRAM",
		Scan:     time.Millisecond,
		Priority: 1,
		// Overruns only: a runner's VM speed varies, and a scan past 20 ×
		// the period would trip the in-flight check first (it did, on CI).
		Guard: PriorityGuard{Overruns: 5, ScanMultiple: 1e6},
		Tasks: []Task{{Name: "stuck", Scan: 5 * time.Millisecond, Priority: 1,
			Guard:   PriorityGuard{ScanMultiple: 4},
			Program: "PROGRAM Stuck\nVAR y : DINT; j : DINT; END_VAR\nFOR j := 1 TO 5000000 DO y := y + 1; END_FOR;\nEND_PROGRAM"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	// Did the priority take at all? Without CAP_SYS_NICE it is refused and
	// there is nothing to guard.
	time.Sleep(30 * time.Millisecond)
	if s := r.Stats(); !s.Sched.Applied && !strings.Contains(s.Sched.Error, "priority guard") {
		msg := "no SCHED_FIFO here (needs CAP_SYS_NICE): " + s.Sched.Error
		if os.Getenv("NAUTILUS_REQUIRE_RT") == "1" {
			t.Fatal("NAUTILUS_REQUIRE_RT=1 but " + msg)
		}
		t.Skip(msg)
	}
	deadline := time.Now().Add(3 * time.Second)
	var s ScanStats
	for time.Now().Before(deadline) {
		s = r.Stats()
		if !s.Sched.Applied && !s.Tasks[0].Sched.Applied {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if s.Sched.Applied || !strings.Contains(s.Sched.Error, "5 scans in a row overran") {
		t.Errorf("main task: %+v, want demoted for 5 overruns in a row", s.Sched)
	}
	if st := s.Tasks[0].Sched; st.Applied || !strings.Contains(st.Error, "has run for more than") {
		t.Errorf("stuck task: %+v, want demoted while its scan ran", st)
	}
	for name, tid := range map[string]int64{"main": r.tid.Load(), "stuck": r.tasks[0].tid.Load()} {
		if policy, _, _ := syscall.RawSyscall(syscall.SYS_SCHED_GETSCHEDULER, uintptr(tid), 0, 0); policy != schedOther {
			t.Errorf("%s task thread %d: policy %d after the demotion, want SCHED_OTHER", name, tid, policy)
		}
	}
}
