//go:build linux

package ctest

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

const capSysNice = 23

// The shim's real-time guard (../rtguard.h, #836): RLIMIT_RTTIME is set
// before SCHED_FIFO is taken, SIGXCPU moves the cycle back to normal
// scheduling instead of killing the process (a dead shim is the PLCnext
// runtime's problem too), and a cycle that spins at FIFO is demoted. The
// spin needs CAP_SYS_NICE: CI's RT guard step gives this test binary the
// capability with setcap and NAUTILUS_REQUIRE_RT=1, and the test passes it
// on to the harness as an ambient capability.
func TestRTGuardShim(t *testing.T) {
	cc, err := exec.LookPath("cc")
	if err != nil {
		missing(t, "no cc to build the guard's harness")
	}
	harness := filepath.Join(t.TempDir(), "rtguard_harness")
	if out, err := exec.Command(cc, "-Wall", "-Wextra", "-Werror", "-std=c11", "-O2", "-o", harness, "testdata/rtguard_harness.c").CombinedOutput(); err != nil {
		t.Fatalf("cc rtguard_harness.c: %v\n%s", err, out)
	}
	run := func(mode string, ambient bool, under ...string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		c := exec.CommandContext(ctx, harness, mode)
		if len(under) > 0 { // started under a limit already in force
			c = exec.CommandContext(ctx, "prlimit", "--rttime="+under[0], harness, mode)
		}
		if ambient {
			c.SysProcAttr = &syscall.SysProcAttr{AmbientCaps: []uintptr{capSysNice}}
		}
		out, err := c.CombinedOutput()
		if ctx.Err() != nil {
			t.Fatalf("%s: still running after 5 s:\n%s", mode, out)
		}
		return string(out), err
	}

	if out, err := run("limit", false); err != nil || !strings.Contains(out, "50000") || !strings.Contains(out, "125000") {
		t.Fatalf("limit: want RLIMIT_RTTIME 50000/125000 µs, got %v:\n%s", err, out)
	}
	// A stricter limit in force is kept: both halves; and only the hard,
	// with the soft lowered under it (raising a hard limit would fail the
	// whole setrlimit).
	if _, err := exec.LookPath("prlimit"); err != nil {
		missing(t, "no prlimit (util-linux) to start the harness under a stricter limit")
	} else {
		for under, want := range map[string][2]string{"30000:100000": {"30000", "100000"}, "60000:100000": {"50000", "100000"}} {
			out, err := run("limit", false, under)
			f := strings.Fields(out)
			if err != nil || len(f) < 5 || f[3] != want[0] || f[4] != want[1] {
				t.Errorf("limit under %s: want %s/%s, got %v:\n%s", under, want[0], want[1], err, out)
			}
		}
	}
	// SIGXCPU's default action kills: the handler must keep it alive.
	if out, err := run("signal", false); err != nil || !strings.Contains(out, "alive demoted=1 policy=0") {
		t.Fatalf("signal: want the process alive and the thread demoted, got %v:\n%s", err, out)
	}

	out, err := run("spin", true)
	if err != nil && strings.Contains(err.Error(), "operation not permitted") {
		out, err = run("spin", false) // no capability to pass on
	}
	switch {
	case strings.Contains(out, "NOFIFO") && strings.Contains(out, "bound=0"):
		msg := "no SCHED_FIFO here (needs CAP_SYS_NICE): " + out
		if os.Getenv("NAUTILUS_REQUIRE_RT") == "1" {
			t.Fatal("NAUTILUS_REQUIRE_RT=1 but " + msg)
		}
		t.Skip(msg)
	case err != nil:
		if ee, ok := err.(*exec.ExitError); ok {
			if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
				t.Fatalf("spin: killed by %v: the soft-limit demotion did not happen:\n%s", ws.Signal(), out)
			}
		}
		t.Fatalf("spin: %v\n%s", err, out)
	case !strings.Contains(out, "DEMOTED policy=0"):
		t.Fatalf("spin: want the spinning FIFO thread demoted to SCHED_OTHER, got:\n%s", out)
	}
}
