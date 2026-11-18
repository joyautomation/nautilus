//go:build linux

package runtime

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The soft limit to arm: the stricter of NAUTILUS_RT_LIMIT_MS (or the
// default) and a runtime's RTLimit, never below MinRTLimit (#907).
func TestRTLimitTakesTheStrictest(t *testing.T) {
	for _, c := range []struct {
		env  string
		want time.Duration
		lim  time.Duration
	}{
		{"", 0, DefaultRTLimit},
		{"", 150 * time.Millisecond, 150 * time.Millisecond},
		{"", time.Second, DefaultRTLimit}, // looser than the default: the default
		{"", time.Millisecond, MinRTLimit},
		{"50", 0, 50 * time.Millisecond},
		{"50", 30 * time.Millisecond, 30 * time.Millisecond},
		{"50", 80 * time.Millisecond, 50 * time.Millisecond},
		{"5", 0, DefaultRTLimit}, // under the env var's floor: ignored, warned
	} {
		t.Setenv("NAUTILUS_RT_LIMIT_MS", c.env)
		if got := rtLimit(c.want); got != c.lim {
			t.Errorf("NAUTILUS_RT_LIMIT_MS=%q, RTLimit %s: armed %s, want %s", c.env, c.want, got, c.lim)
		}
	}
}

// rtChild runs this test binary's test name in a child with env set, so
// the process-wide limit starts fresh, and returns its output.
func rtChild(t *testing.T, name string, env ...string) string {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^"+name+"$", "-test.count=1")
	cmd.Env = append(append(os.Environ(), "NAUT_RTLIMIT_CHILD=1"), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("child %s: %v\n%s", name, err, out)
	}
	return string(out)
}

func printLimit() {
	var lim syscall.Rlimit
	if err := syscall.Getrlimit(rlimitRTTime, &lim); err != nil {
		fmt.Println("GET:", err)
		return
	}
	fmt.Printf("LIMIT %d %d\n", lim.Cur, lim.Max)
}

// The strictest request before arming is armed; one after tightens the
// limit in force, and a looser one never loosens it (another runtime may
// have asked).
func TestRTLimitRequestBeforeAndAfterArming(t *testing.T) {
	if os.Getenv("NAUT_RTLIMIT_CHILD") == "1" {
		requestRTLimit(80 * time.Millisecond)
		requestRTLimit(120 * time.Millisecond) // looser, before arming: the 80 ms stands
		if err := armRTGuard(); err != nil {
			fmt.Println("ARM:", err)
			return
		}
		printLimit()
		requestRTLimit(30 * time.Millisecond)
		printLimit()
		requestRTLimit(150 * time.Millisecond)
		printLimit()
		fmt.Println("IN FORCE", rtLimitInForce())
		return
	}
	out := rtChild(t, "TestRTLimitRequestBeforeAndAfterArming")
	want := "LIMIT 80000 200000\nLIMIT 30000 75000\nLIMIT 30000 75000\nIN FORCE 30ms\n"
	if !strings.Contains(out, want) {
		t.Fatalf("want\n%s\ngot\n%s", want, out)
	}
}

// Through Run: the strictest RTLimit of the tasks at a priority is armed
// before any loop takes its priority, and their stats show it once Run
// has placed them (New shows nothing yet). The guard
// arms before SCHED_FIFO is asked for, so this holds without CAP_SYS_NICE
// too (the priority is then refused, the limit still set). A task with no
// priority asks for nothing.
func TestRTLimitThroughRun(t *testing.T) {
	if os.Getenv("NAUT_RTLIMIT_CHILD") == "1" {
		r, err := New(Options{
			Program:  "PROGRAM Main\nVAR x : DINT; END_VAR\nx := x + 1;\nEND_PROGRAM",
			Scan:     10 * time.Millisecond,
			Priority: 1,
			Guard:    PriorityGuard{RTLimit: 60 * time.Millisecond},
			Tasks: []Task{
				{Name: "fast", Scan: 10 * time.Millisecond, Priority: 1, Guard: PriorityGuard{RTLimit: 40 * time.Millisecond},
					Program: "PROGRAM Fast\nVAR y : DINT; END_VAR\ny := y + 1;\nEND_PROGRAM"},
				{Name: "plain", Scan: 10 * time.Millisecond, Guard: PriorityGuard{RTLimit: 15 * time.Millisecond},
					Program: "PROGRAM Plain\nVAR z : DINT; END_VAR\nz := z + 1;\nEND_PROGRAM"},
			},
		})
		if err != nil {
			fmt.Println("NEW:", err)
			return
		}
		fmt.Printf("BEFORE %v\n", r.Stats().Sched.Guard.RTLimitMs)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { r.Run(ctx); close(done) }()
		time.Sleep(100 * time.Millisecond)
		cancel()
		<-done
		printLimit()
		s := r.Stats()
		fmt.Printf("MAIN %v\n", s.Sched.Guard.RTLimitMs)
		for _, ts := range s.Tasks {
			if ts.Sched.Guard != nil {
				fmt.Printf("TASK %s %v\n", ts.Name, ts.Sched.Guard.RTLimitMs)
			} else {
				fmt.Printf("TASK %s none\n", ts.Name)
			}
		}
		return
	}
	out := rtChild(t, "TestRTLimitThroughRun")
	for _, want := range []string{"BEFORE 0\n", "LIMIT 40000 100000\n", "MAIN 40\n", "TASK fast 40\n", "TASK plain none\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("want %q in\n%s", want, out)
		}
	}
}
