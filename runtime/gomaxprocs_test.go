package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The rule (#830): raise GOMAXPROCS only when a loop runs at a priority,
// the operator did not set it, and the box has at most 4 cores; one P per
// such loop beyond the cores plus one, capped at twice the cores plus two.
// The Verdin AM62 Dual's case (2 cores, one FIFO task) gives 4, the
// measured best (docs/design/verdin-bench.md).
func TestProcsForRule(t *testing.T) {
	for _, c := range []struct {
		cur, fifo int
		env       bool
		want      int
		why       string
	}{
		{2, 1, false, 4, "the bench case: 2 cores, one FIFO task"},
		{2, 2, false, 5, "two FIFO tasks"},
		{1, 3, false, 4, "capped at 2*1+2"},
		{4, 1, false, 6, "4 cores still raised"},
		{4, 6, false, 10, "capped at 2*4+2"},
		{2, 0, false, 2, "no task at a priority"},
		{2, 1, true, 2, "GOMAXPROCS set by the operator"},
		{5, 1, false, 5, "more than 4 cores"},
		{24, 3, false, 24, "a desktop"},
	} {
		if got, _ := procsFor(c.cur, c.fifo, c.env); got != c.want {
			t.Errorf("%s: procsFor(%d, %d, %v) = %d, want %d", c.why, c.cur, c.fifo, c.env, got, c.want)
		}
	}
	if _, why := procsFor(2, 1, true); !strings.Contains(why, "environment") {
		t.Errorf("an operator's setting kept should say so: %q", why)
	}
}

// stubProcs replaces the process-wide setting with a recorder that reports
// cur as the current value; it returns the values set.
func stubProcs(t *testing.T, cur int, env bool) *[]int {
	t.Helper()
	var set []int
	oldSet, oldEnv := setGOMAXPROCS, gomaxprocsInEnv
	t.Cleanup(func() { setGOMAXPROCS, gomaxprocsInEnv = oldSet, oldEnv })
	setGOMAXPROCS = func(n int) int {
		if n > 0 {
			set = append(set, n)
		}
		return cur
	}
	gomaxprocsInEnv = func() bool { return env }
	return &set
}

func procsRuntime(t *testing.T, mainPrio, taskPrio int) *Runtime {
	t.Helper()
	r, err := New(Options{
		Program:  "PROGRAM Main\nVAR x : INT; END_VAR\nx := x + 1;\nEND_PROGRAM",
		Scan:     5 * time.Millisecond,
		Priority: mainPrio,
		Tasks: []Task{{Name: "fast", Scan: 5 * time.Millisecond, Priority: taskPrio,
			Program: "PROGRAM Fast\nVAR y : INT; END_VAR\ny := y + 1;\nEND_PROGRAM"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// Run raises GOMAXPROCS before its loops start when a task has a priority
// on a two-core box, once, and reports it in Stats (served as
// /api/stats' gomaxprocs).
func TestRunRaisesGOMAXPROCSForATaskAtPriority(t *testing.T) {
	set := stubProcs(t, 2, false)
	r := procsRuntime(t, 0, 50)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	r.Run(ctx)
	if len(*set) != 1 || (*set)[0] != 4 {
		t.Fatalf("Run set GOMAXPROCS to %v, want [4] (2 cores, one task at a priority)", *set)
	}
	p := r.Stats().Procs
	if p.Value != 4 || p.RaisedFrom != 2 || p.Reason == "" {
		t.Errorf("Stats().Procs = %+v, want 4 raised from 2 with a reason", p)
	}
	r.tuneProcs() // a second call changes nothing
	if len(*set) != 1 {
		t.Errorf("GOMAXPROCS set again: %v", *set)
	}
}

// Left alone: an operator's GOMAXPROCS, no task at a priority, or a box
// with cores to spare. Stats still reports the value the loops run with.
func TestGOMAXPROCSLeftAlone(t *testing.T) {
	for _, c := range []struct {
		name               string
		cur                int
		env                bool
		mainPrio, taskPrio int
	}{
		{"set by the operator", 2, true, 50, 50},
		{"no task at a priority", 2, false, 0, 0},
		{"8 cores", 8, false, 50, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			set := stubProcs(t, c.cur, c.env)
			r := procsRuntime(t, c.mainPrio, c.taskPrio)
			r.tuneProcs()
			if len(*set) != 0 {
				t.Errorf("GOMAXPROCS was set to %v", *set)
			}
			if p := r.Stats().Procs; p.Value != c.cur || p.RaisedFrom != 0 {
				t.Errorf("Stats().Procs = %+v, want value %d, not raised", p, c.cur)
			}
		})
	}
}

// /api/stats serves ScanStats as JSON: the outcome is there as gomaxprocs.
func TestProcsStatsJSON(t *testing.T) {
	b, err := json.Marshal(ScanStats{Procs: ProcsStats{Value: 4, RaisedFrom: 2, Reason: "r"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"gomaxprocs":{"value":4,"raisedFrom":2,"reason":"r"}`) {
		t.Errorf("ScanStats JSON lacks the gomaxprocs outcome: %s", b)
	}
}
