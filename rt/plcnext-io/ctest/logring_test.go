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

	tctx, tcancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer tcancel()
	// Records reach a sink that works, whole and in order.
	file := filepath.Join(t.TempDir(), "sink")
	if out, err := exec.CommandContext(tctx, bin, "deliver", file).CombinedOutput(); err != nil {
		t.Fatalf("deliver: %v\n%s", err, out)
	}
	got := dropReports.ReplaceAll(mustRead(t, file), nil)
	var want strings.Builder
	for i := 0; i < 2000; i++ {
		fmt.Fprintf(&want, "line %d %020d\n", i, i*7)
	}
	if string(got) != want.String() {
		t.Fatalf("deliver: sink holds %d bytes, want the 2000 lines in order:\n%s", len(got), got)
	}

	// Four producers: no line lost, torn or reordered within a producer.
	mfile := filepath.Join(t.TempDir(), "msink")
	if out, err := exec.CommandContext(tctx, bin, "multi", mfile).CombinedOutput(); err != nil {
		t.Fatalf("multi: %v\n%s", err, out)
	}
	mgot := dropReports.ReplaceAll(mustRead(t, mfile), nil)
	next := map[int]int{}
	lines := strings.Split(strings.TrimSuffix(string(mgot), "\n"), "\n")
	for _, l := range lines {
		var k, i, v int
		if _, err := fmt.Sscanf(l, "t%d %d %d", &k, &i, &v); err != nil || v != i*7 || i != next[k] {
			t.Fatalf("multi: bad or out-of-order line %q (producer %d expects %d)", l, k, next[k])
		}
		next[k]++
	}
	if len(lines) != 2000 {
		t.Fatalf("multi: %d lines, want 2000", len(lines))
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

// dropReports: the drain thread's own "log ring full" notes, which a producer
// that retries a full ring provokes; not part of what the test sends.
var dropReports = regexp.MustCompile(`log ring full: \d+ records dropped so far\n`)

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
