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
	if got := schedBannerSuffix(runtime.ScanStats{Sched: runtime.SchedStats{Priority: 40, Applied: true}}); got != "" {
		t.Errorf("all granted: %q, want nothing", got)
	}
	if !schedPending(runtime.SchedStats{Priority: 40}) || schedPending(runtime.SchedStats{Priority: 40, Applied: true}) || schedPending(runtime.SchedStats{}) {
		t.Error("schedPending: asked and not yet answered is pending; answered or not asked is not")
	}
}
