package main

import (
	"testing"

	"github.com/joyautomation/nautilus/runtime"
)

// A task whose real-time request was refused (the pre-flight on a vendor
// shim's core, or no CAP_SYS_NICE) is named on the startup banner, not
// only in the log and scan.sched.
func TestSchedBannerSuffixNamesRefusedTasks(t *testing.T) {
	s := runtime.ScanStats{
		Sched: runtime.SchedStats{Priority: 40, CPUs: []int{1}, Error: "pin to cpu 1 at priority 40: refused, …"},
		Tasks: []runtime.TaskStats{
			{Name: "fast", Sched: runtime.SchedStats{Priority: 30, Applied: true}},
			{Name: "slow", Sched: runtime.SchedStats{Priority: 20, Error: "operation not permitted"}},
		},
	}
	if got, want := schedBannerSuffix(s), " — REAL-TIME REFUSED for main, slow (see the log)"; got != want {
		t.Errorf("banner suffix %q, want %q", got, want)
	}
	// A cpu: alone that could not be applied is a pin failure, not a
	// real-time refusal.
	s.Tasks = append(s.Tasks, runtime.TaskStats{Name: "pinned", Sched: runtime.SchedStats{CPUs: []int{9}, Error: "pin to cpu 9: invalid argument"}})
	if got, want := schedBannerSuffix(s), " — REAL-TIME REFUSED for main, slow — CPU PIN FAILED for pinned (see the log)"; got != want {
		t.Errorf("banner suffix %q, want %q", got, want)
	}
	if got := schedBannerSuffix(runtime.ScanStats{Sched: runtime.SchedStats{Priority: 40, Applied: true}}); got != "" {
		t.Errorf("all granted: %q, want nothing", got)
	}
	if !schedPending(runtime.SchedStats{Priority: 40}) || schedPending(runtime.SchedStats{Priority: 40, Applied: true}) || schedPending(runtime.SchedStats{}) {
		t.Error("schedPending: asked and not yet answered is pending; answered or not asked is not")
	}
}

// A GOMAXPROCS the runtime raised for a task at a priority (#830) is on the
// banner; one it left alone is not.
func TestSchedBannerSuffixReportsARaisedGOMAXPROCS(t *testing.T) {
	s := runtime.ScanStats{Sched: runtime.SchedStats{Priority: 80, Applied: true},
		Procs: runtime.ProcsStats{Value: 4, RaisedFrom: 2, Reason: "…"}}
	if got, want := schedBannerSuffix(s), " — GOMAXPROCS 4 (raised from 2: a task requests a real-time priority)"; got != want {
		t.Errorf("banner suffix %q, want %q", got, want)
	}
	s.Procs = runtime.ProcsStats{Value: 2, Reason: "GOMAXPROCS set in the environment"}
	if got := schedBannerSuffix(s); got != "" {
		t.Errorf("not raised: %q, want nothing", got)
	}
}
