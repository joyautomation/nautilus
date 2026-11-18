//go:build linux

// Package ctest holds tests for the PLCnext in-process tier's C
// programs, ../*.c (build.sh builds them for the controller; these build them for
// the test machine with its own cc).
package ctest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// naut-launch bounds what it starts (#836): RLIMIT_RTTIME is set before the
// exec, so a real-time thread of the controller (or of tools/jitter, which
// sets none of its own) gets SIGXCPU and then SIGKILL instead of owning
// its core. NAUTILUS_RT_LIMIT_MS sets it; a stricter limit in force is kept.
func TestNautLaunchBoundsRealTime(t *testing.T) {
	cc, err := exec.LookPath("cc")
	if err != nil {
		missing(t, "no cc to build naut-launch for this machine")
	}
	dir := t.TempDir()
	os.Chmod(dir, 0o755) // the launcher refuses a directory others may write
	launch := filepath.Join(dir, "naut-launch")
	if out, err := exec.Command(cc, "-Wall", "-Wextra", "-Werror", "-std=c11", "-O2", "-o", launch, "../naut_launch.c").CombinedOutput(); err != nil {
		t.Fatalf("cc naut_launch.c: %v\n%s", err, out)
	}
	rel := filepath.Join(dir, "releases", "r1")
	os.MkdirAll(rel, 0o755)
	os.Chmod(filepath.Join(dir, "releases"), 0o755)
	os.WriteFile(filepath.Join(rel, "controller"), []byte("#!/bin/sh\ngrep '^Max realtime timeout' /proc/self/limits\n"), 0o755)
	os.Symlink("releases/r1", filepath.Join(dir, "current"))

	var lastStderr string
	limits := func(env ...string) (soft, hard string) {
		t.Helper()
		c := exec.Command(launch)
		if len(env) > 0 && strings.HasPrefix(env[0], "prlimit=") {
			// Start it under a limit already in force.
			c = exec.Command("prlimit", "--rttime="+strings.TrimPrefix(env[0], "prlimit="), launch)
			env = env[1:]
		}
		c.Env = append(os.Environ(), env...)
		var stderr strings.Builder
		c.Stderr = &stderr
		out, err := c.Output()
		if err != nil {
			t.Fatalf("naut-launch: %v\n%s%s", err, out, stderr.String())
		}
		lastStderr = stderr.String()
		f := strings.Fields(string(out)) // Max realtime timeout <soft> <hard> us
		if len(f) < 5 {
			t.Fatalf("limits line %q", out)
		}
		return f[3], f[4]
	}
	// 50 ms is stricter than any limit a shell or CI runner sets.
	if soft, hard := limits("NAUTILUS_RT_LIMIT_MS=50"); soft != "50000" || hard != "125000" {
		t.Fatalf("NAUTILUS_RT_LIMIT_MS=50: the controller ran with RLIMIT_RTTIME %s/%s µs, want 50000/125000", soft, hard)
	}
	defSoft, defHard := limits("NAUTILUS_RT_LIMIT_MS=")
	if defSoft == "unlimited" || defHard == "unlimited" {
		t.Fatalf("default: the controller ran with RLIMIT_RTTIME %s/%s, want a bound", defSoft, defHard)
	}
	// Out of range: above the 60 s cap (4 000 000 ms would wrap a 32-bit
	// rlim_t on the controller), or past what strtol can hold. Both fall
	// back to the default rather than to a huge or wrapped limit.
	// (A shell's own stricter hard limit can hide a too-large value in the
	// numbers, so the launcher's refusal is checked as well.)
	for _, v := range []string{"60001", "4000000", "99999999999999999999999", "9", "12ms"} {
		soft, hard := limits("NAUTILUS_RT_LIMIT_MS=" + v)
		if soft != defSoft || hard != defHard || !strings.Contains(lastStderr, "from 10 to 60000; using 200") {
			t.Errorf("NAUTILUS_RT_LIMIT_MS=%s: the controller ran with %s/%s (default %s/%s), launcher said %q; want it refused for the default", v, soft, hard, defSoft, defHard, lastStderr)
		}
	}
	if limits("NAUTILUS_RT_LIMIT_MS=60000"); strings.Contains(lastStderr, "using 200") {
		t.Errorf("NAUTILUS_RT_LIMIT_MS=60000, the cap itself, was refused: %q", lastStderr)
	}
	// A stricter limit already in force (a launcher above this one, an
	// operator's prlimit) is kept, soft and hard.
	if _, err := exec.LookPath("prlimit"); err != nil {
		missing(t, "no prlimit (util-linux) to start the launcher under a stricter limit")
	} else {
		if soft, hard := limits("prlimit=30000:100000", "NAUTILUS_RT_LIMIT_MS=50"); soft != "30000" || hard != "100000" {
			t.Fatalf("under a stricter 30000/100000: the controller ran with %s/%s, want it kept", soft, hard)
		}
		// Hard stricter, soft looser: the soft is lowered to 50 ms under
		// the kept hard (raising a hard limit would fail, and with it the
		// whole setrlimit).
		if soft, hard := limits("prlimit=60000:100000", "NAUTILUS_RT_LIMIT_MS=50"); soft != "50000" || hard != "100000" {
			t.Fatalf("under 60000/100000: the controller ran with %s/%s, want 50000/100000", soft, hard)
		}
	}
}

// missing skips a test that cannot run here, except in CI, where a skip
// would pass a check that never ran: there it fails.
func missing(t *testing.T, why string) {
	t.Helper()
	if os.Getenv("CI") != "" {
		t.Fatal(why + " (CI is set: a skip here would hide that this never ran)")
	}
	t.Skip(why)
}
