//go:build linux

package runtime

import (
	"context"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

func affinityOfThisThread(t *testing.T) []int {
	t.Helper()
	var mask [16]uint64
	if _, _, errno := syscall.RawSyscall(syscall.SYS_SCHED_GETAFFINITY, 0, uintptr(len(mask)*8), uintptr(unsafe.Pointer(&mask[0]))); errno != 0 {
		t.Fatal(errno)
	}
	var cpus []int
	for i := range mask {
		for b := 0; b < 64; b++ {
			if mask[i]&(1<<uint(b)) != 0 {
				cpus = append(cpus, i*64+b)
			}
		}
	}
	return cpus
}

// Affinity needs no privilege: pin to the CPU this thread is allowed on
// and read it back.
func TestApplySchedAffinity(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	allowed := affinityOfThisThread(t)
	if len(allowed) < 2 {
		t.Skip("only one CPU allowed; nothing to pin to")
	}
	target := allowed[len(allowed)-1]
	if err := applySched([]int{target}, 0); err != nil {
		t.Fatal(err)
	}
	if got := affinityOfThisThread(t); len(got) != 1 || got[0] != target {
		t.Errorf("affinity after pin = %v, want [%d]", got, target)
	}
	if err := applySched(allowed, 0); err != nil { // put it back
		t.Fatal(err)
	}
	if err := applySched([]int{100000}, 0); err == nil {
		t.Error("pinning to a non-existent CPU did not fail")
	}
}

// SCHED_FIFO without the capability must fail and say what to do — the
// loud failure the design asks for. (Skipped where it is actually granted.)
func TestApplySchedFIFORefusalIsExplicit(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	err := applySched(nil, 50)
	if err == nil {
		t.Skip("SCHED_FIFO was granted here (root or rtprio rlimit)")
	}
	for _, want := range []string{"CAP_SYS_NICE", "setcap", "ulimit -r", "unpinned"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not mention %q", err, want)
		}
	}
	if err := applySched(nil, 100); err == nil || !strings.Contains(err.Error(), "1–99") {
		t.Errorf("priority 100: %v", err)
	}
}

// Run reports the placement in Stats for the main task and each task, and
// a refused request shows up there rather than disappearing.
func TestRunReportsSched(t *testing.T) {
	runtime.LockOSThread()
	allowed := affinityOfThisThread(t)
	runtime.UnlockOSThread()
	r, err := New(Options{
		Program: "PROGRAM Main\nVAR x : INT; END_VAR\nx := x + 1;\nEND_PROGRAM",
		Scan:    5 * time.Millisecond,
		CPUs:    []int{allowed[0]},
		Tasks: []Task{{Name: "rt", Scan: 5 * time.Millisecond, Priority: 50,
			Program: "PROGRAM RT\nVAR y : INT; END_VAR\ny := y + 1;\nEND_PROGRAM"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	r.Run(ctx)
	s := r.Stats()
	if !s.Sched.Applied || s.Sched.Error != "" || len(s.Sched.CPUs) != 1 {
		t.Errorf("main sched = %+v, want applied to %v", s.Sched, allowed[:1])
	}
	rt := s.Tasks[0].Sched
	if rt.Priority != 50 {
		t.Errorf("task sched = %+v", rt)
	}
	if rt.Applied == (rt.Error != "") {
		t.Errorf("task sched must be either applied or carry the refusal: %+v", rt)
	}
}
