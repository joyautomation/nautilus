//go:build linux

package ctest

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The shim's log ring (../logring.h, #1051): the cycle logs without a system
// call, so a log sink that blocks (a flash under writeback, /dev/log) cannot
// stall it. The sink here is a full pipe nobody reads.
func TestLogRingKeepsABlockedSinkOutOfTheCycle(t *testing.T) {
	cc, err := exec.LookPath("cc")
	if err != nil {
		missing(t, "no cc to build the log ring's harness")
	}
	bin := filepath.Join(t.TempDir(), "logring_harness")
	if out, err := exec.Command(cc, "-Wall", "-Wextra", "-Werror", "-std=c11", "-O2", "-pthread", "-o", bin, "testdata/logring_harness.c").CombinedOutput(); err != nil {
		t.Fatalf("cc logring_harness.c: %v\n%s", err, out)
	}
	run := func(mode string, limit time.Duration) (string, bool) {
		ctx, cancel := context.WithTimeout(context.Background(), limit)
		defer cancel()
		out, _ := exec.CommandContext(ctx, bin, mode).CombinedOutput()
		return string(out), ctx.Err() == nil
	}

	// Records reach a sink that works, whole and in order.
	file := filepath.Join(t.TempDir(), "sink")
	if out, err := exec.Command(bin, "deliver", file).CombinedOutput(); err != nil {
		t.Fatalf("deliver: %v\n%s", err, out)
	}
	got, _ := os.ReadFile(file)
	var want strings.Builder
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&want, "line %d\n", i)
	}
	if string(got) != want.String() {
		t.Fatalf("deliver: sink holds %d bytes, want the 100 lines in order:\n%s", len(got), got)
	}

	// The control: a plain write() to the blocked sink never returns.
	if out, finished := run("direct", 3*time.Second); finished {
		t.Fatalf("control: a write() to the blocked sink returned; the harness proves nothing:\n%s", out)
	}

	out, finished := run("ring", 10*time.Second)
	if !finished {
		t.Fatalf("ring: the cycle did not finish: a blocked sink stalled it")
	}
	m := regexp.MustCompile(`DONE worst_us=(\d+) dropped=(\d+)`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("ring: no result line:\n%s", out)
	}
	worst, _ := strconv.Atoi(m[1])
	dropped, _ := strconv.Atoi(m[2])
	// A shared CI host is not an RT box: the bound only has to be far
	// below a blocked write's (unbounded).
	if worst > 50000 {
		t.Errorf("ring: worst lateness %d us with the sink blocked, want under 50000", worst)
	}
	if dropped == 0 {
		t.Errorf("ring: no records dropped though the sink never drained; the ring is not bounded")
	}
}
