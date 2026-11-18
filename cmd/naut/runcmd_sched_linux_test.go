//go:build linux

package main

import (
	"bufio"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Through naut run itself: a task whose priority the OS refuses is named
// on the banner. Run where SCHED_FIFO is not allowed (no CAP_SYS_NICE, no
// rtprio), which is every dev box and CI runner; skipped elsewhere.
func TestRunBannerSaysRealTimeWasRefused(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs naut")
	}
	var lim syscall.Rlimit
	if syscall.Getrlimit(14 /* RLIMIT_RTPRIO */, &lim) == nil && lim.Cur > 0 || os.Geteuid() == 0 {
		t.Skip("SCHED_FIFO may be allowed here, so nothing would be refused")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go toolchain not on PATH")
	}
	dir := t.TempDir()
	naut := filepath.Join(dir, "naut")
	if out, err := exec.Command(goBin, "build", "-o", naut, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build ./cmd/naut: %v\n%s", err, out)
	}
	proj := filepath.Join(dir, "rt")
	os.MkdirAll(proj, 0o755)
	os.WriteFile(filepath.Join(proj, "nautilus.yaml"), []byte("name: rt\ntasks:\n  - program: main.st\n    scan: 10ms\n    priority: 40\n"), 0o644)
	os.WriteFile(filepath.Join(proj, "main.st"), []byte("PROGRAM Main\nEND_PROGRAM\n"), 0o644)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	run := exec.CommandContext(ctx, naut, "run", proj)
	// naut run keeps its audit journal in its working directory: the
	// test's, not the package's.
	run.Dir = dir
	run.Env = append(os.Environ(), "NAUTILUS_ADDR=127.0.0.1:0")
	stdout, _ := run.StdoutPipe()
	run.Stderr = io.Discard
	if err := run.Start(); err != nil {
		t.Fatal(err)
	}
	defer run.Process.Kill()
	sc := bufio.NewScanner(stdout)
	for sc.Scan() {
		line := sc.Text()
		if strings.Contains(line, "Ctrl+C to stop") || strings.Contains(line, "REAL-TIME") {
			if !strings.Contains(line, "REAL-TIME REFUSED for main") {
				t.Fatalf("banner without the refusal: %q", line)
			}
			return
		}
	}
	t.Fatal("naut run printed no banner")
}
