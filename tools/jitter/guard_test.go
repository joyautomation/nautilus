package main

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/runtime"
)

// guarded is runGuarded that fails the test instead of hanging when the
// guard does not end the run.
func guarded(t *testing.T, run func(context.Context), stats func() runtime.ScanStats, g guardConfig) (string, bool) {
	t.Helper()
	type res struct {
		reason   string
		finished bool
	}
	ch := make(chan res, 1)
	go func() { r, f := runGuarded(context.Background(), run, stats, g); ch <- res{r, f} }()
	select {
	case r := <-ch:
		return r.reason, r.finished
	case <-time.After(3 * time.Second):
		t.Fatal("the guard did not end the run within 3 s")
		return "", false
	}
}

func fastGuard() guardConfig {
	return guardConfig{Stall: 80 * time.Millisecond, Every: 5 * time.Millisecond, Deadline: 2 * time.Second, Grace: 100 * time.Millisecond}
}

// A run that keeps scanning runs its course; one whose main task stops
// scanning between scans (the PLCnext incident: starved, not overrunning)
// is stopped, and a run that does not end when stopped is reported so.
func TestGuardStall(t *testing.T) {
	var count atomic.Uint64
	stats := func() runtime.ScanStats { return runtime.ScanStats{Count: count.Load()} }

	// Healthy: scans every ms, ends by itself after 200 ms.
	reason, finished := guarded(t, func(ctx context.Context) {
		end := time.After(200 * time.Millisecond)
		for {
			select {
			case <-end:
				return
			case <-ctx.Done():
				return
			case <-time.After(time.Millisecond):
				count.Add(1)
			}
		}
	}, stats, fastGuard())
	if reason != "" || !finished {
		t.Fatalf("a healthy run was stopped: %q, finished %v", reason, finished)
	}

	// Starved: scans stop; the loop notices the cancel and ends.
	reason, finished = guarded(t, func(ctx context.Context) { <-ctx.Done() }, stats, fastGuard())
	if !strings.Contains(reason, "has not scanned") || !finished {
		t.Fatalf("a stalled run: %q, finished %v; want stopped for the stall", reason, finished)
	}

	// Starved for good: the loop never sees the cancel. Not finished: the
	// caller exits to end it.
	block := make(chan struct{})
	defer close(block)
	start := time.Now()
	reason, finished = guarded(t, func(context.Context) { <-block }, stats, fastGuard())
	if !strings.Contains(reason, "has not scanned") || finished || time.Since(start) > time.Second {
		t.Fatalf("a run that ignores the stop: %q, finished %v after %s; want stopped, not finished, within the grace", reason, finished, time.Since(start))
	}
}

// A priority withdrawn mid-run stops it: what is left would not measure
// what was asked.
func TestGuardDemotion(t *testing.T) {
	var count atomic.Uint64
	var demoted atomic.Bool
	stats := func() runtime.ScanStats {
		count.Add(1) // always progressing
		s := runtime.ScanStats{Count: count.Load()}
		s.Tasks = []runtime.TaskStats{{Name: "fast2", Sched: runtime.SchedStats{Priority: 30, Demoted: demoted.Load(), Error: "5 scans in a row overran"}}}
		return s
	}
	go func() { time.Sleep(30 * time.Millisecond); demoted.Store(true) }()
	reason, finished := guarded(t, func(ctx context.Context) { <-ctx.Done() }, stats, fastGuard())
	if !strings.Contains(reason, "withdrew task fast2's priority") || !finished {
		t.Fatalf("a demotion: %q, finished %v", reason, finished)
	}
}

// The deadline stops a run that is progressing but will not end.
func TestGuardDeadline(t *testing.T) {
	var count atomic.Uint64
	stats := func() runtime.ScanStats { count.Add(1); return runtime.ScanStats{Count: count.Load()} }
	g := fastGuard()
	g.Deadline = 60 * time.Millisecond
	reason, finished := guarded(t, func(ctx context.Context) { <-ctx.Done() }, stats, g)
	if !strings.Contains(reason, "past") || !finished {
		t.Fatalf("past the deadline: %q, finished %v", reason, finished)
	}
}

// A stopped or demoted run's report says INVALID at the top.
func TestInvalidReportSaysSo(t *testing.T) {
	r := report{Invalid: "the main task has not scanned for 2s: the box cannot keep up"}
	if md := markdown(&r, time.Second); !strings.Contains(md, "**INVALID: the main task has not scanned for 2s") {
		t.Fatalf("report does not say INVALID:\n%s", md)
	}
	if md := markdown(&report{}, time.Second); strings.Contains(md, "INVALID") {
		t.Fatal("a valid report says INVALID")
	}
}
