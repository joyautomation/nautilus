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
	cmd.Env = append(os.Environ(), "NAUT_RTGUARD_CHILD=1", "NAUTILUS_RT_LIMIT_MS=50")
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

// spinAtFIFO is the child: one locked thread at SCHED_FIFO 1 spinning
// without a blocking call until it is demoted, or 3 s.
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
			if time.Since(start) > 3*time.Second {
				fmt.Println("NOT STOPPED after 3 s")
				os.Exit(1)
			}
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
