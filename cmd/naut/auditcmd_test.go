package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/audit"
)

// #709: naut audit verify exits 0 on an intact journal and 1 at a break,
// naming the file and line.
func TestAuditVerifyCommand(t *testing.T) {
	dir := t.TempDir()
	j, err := audit.OpenFile(dir, audit.FileOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"A", "B", "C"} {
		if _, err := j.Record(audit.Entry{User: "user:op1", Source: audit.SourceHMI, Action: "tag.write", Target: target, Result: audit.ResultOK}); err != nil {
			t.Fatal(err)
		}
	}
	head, _ := j.Head()
	j.Close()
	var code int
	out := captureStdout(t, func() int { code = runAudit([]string{"verify", dir}); return code })
	if code != 0 || !strings.Contains(out, "ok: 3 entries") || !strings.Contains(out, head) {
		t.Fatalf("intact: exit %d\n%s", code, out)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "audit-*.jsonl"))
	data, _ := os.ReadFile(files[0])
	ls := strings.Split(string(data), "\n")
	os.WriteFile(files[0], []byte(strings.Join(append(ls[:1], ls[2:]...), "\n")), 0o600) // remove line 2
	out = captureStdout(t, func() int { code = runAudit([]string{"verify", dir}); return code })
	if code != 1 || !strings.Contains(out, "BROKEN at "+filepath.Base(files[0])+":2") {
		t.Fatalf("a removed line: exit %d\n%s", code, out)
	}
}

// Flags after the journal directory are parsed, not dropped: `verify dir
// --anchor x` must not run the plain check and exit 0.
func TestAuditVerifyFlagsAfterTheDirectory(t *testing.T) {
	dir := t.TempDir()
	j, err := audit.OpenFile(dir, audit.FileOptions{})
	if err != nil {
		t.Fatal(err)
	}
	j.Record(audit.Entry{User: "u", Source: audit.SourceHMI, Action: "tag.write", Result: audit.ResultOK})
	j.Close()
	for _, c := range []struct {
		args []string
		code int
		err  string
	}{
		{[]string{"verify", dir}, 0, ""},
		{[]string{"verify", dir, "--anchor", "file"}, 2, `--anchor "file": want postgres`},
		{[]string{"verify", "--anchor", "file", dir}, 2, `--anchor "file": want postgres`},
		{[]string{"verify", dir, "--controller", "x"}, 2, "go with --anchor postgres"},
		{[]string{"verify", dir, "other"}, 2, "one journal directory"},
		{[]string{"verify", dir, "--anchor", "postgres", "--dsn-env", "NO_SUCH_AUDIT_DB", "--controller", "c"}, 2, "NO_SUCH_AUDIT_DB is empty"},
	} {
		var out, errb strings.Builder
		code := runAuditTo(c.args, &out, &errb, func(string) string { return "" })
		if code != c.code || !strings.Contains(errb.String(), c.err) {
			t.Errorf("%v: exit %d (%s), want %d with %q", c.args, code, errb.String(), c.code, c.err)
		}
	}
}

// pg-schema prints the tables and grants: SELECT, and INSERT only on the
// columns that are not evidence; it needs the role.
func TestAuditPGSchema(t *testing.T) {
	var out, errb strings.Builder
	if code := runAuditTo([]string{"pg-schema"}, &out, &errb, os.Getenv); code != 2 || !strings.Contains(errb.String(), "--role") {
		t.Errorf("without --role: %d %s", code, errb.String())
	}
	out.Reset()
	if code := runAuditTo([]string{"pg-schema", "--role", `naut"sink`}, &out, &errb, os.Getenv); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	sql := out.String()
	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS audit_anchors",
		`REVOKE ALL ON audit_entries, audit_gaps, audit_anchors FROM "naut""sink"`,
		`GRANT SELECT ON audit_entries, audit_gaps, audit_anchors TO "naut""sink"`,
		`GRANT INSERT (controller, seq, hash, reason) ON audit_anchors TO "naut""sink"`,
		"ADD COLUMN IF NOT EXISTS copied_at",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("schema lacks %q:\n%s", want, sql)
		}
	}
	for _, line := range strings.Split(sql, "\n") {
		if strings.HasPrefix(line, "GRANT INSERT") && (strings.Contains(line, "copied_at") || strings.Contains(line, "writer") ||
			strings.Contains(line, " at,") || strings.Contains(line, "noticed")) {
			t.Errorf("an evidence column granted: %s", line)
		}
	}
}

// The prune gate: an equal or longer retention prunes at once; a lowered
// one waits for its anchor; past the disk threshold (or when the disk's
// use is unknown) it prunes unanchored, saying so; it stops with ctx.
func TestWaitToPrune(t *testing.T) {
	ans := func(a gateAnswer) func(context.Context) gateAnswer {
		return func(context.Context) gateAnswer { return a }
	}
	used := func(u float64, err error) func() (float64, error) { return func() (float64, error) { return u, err } }
	// Bounded, so a wait that never ends fails the test rather than hangs.
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var log strings.Builder
	if ok, un := waitToPrune(ctx, ans(gateAnswer{}), used(0.1, nil), time.Millisecond, &log); !ok || un {
		t.Errorf("not lowered: %v %v", ok, un)
	}
	if ok, un := waitToPrune(ctx, ans(gateAnswer{lowered: true, anchored: true}), used(0.1, nil), time.Millisecond, &log); !ok || un {
		t.Errorf("lowered and anchored: %v %v", ok, un)
	}
	calls := 0
	anchoredLater := func(context.Context) gateAnswer {
		calls++
		return gateAnswer{lowered: true, anchored: calls > 3}
	}
	log.Reset()
	if ok, un := waitToPrune(ctx, anchoredLater, used(0.5, nil), time.Millisecond, &log); !ok || un || calls != 4 {
		t.Errorf("lowered, anchored later: %v %v after %d checks", ok, un, calls)
	}
	if !strings.Contains(log.String(), "waits until Postgres anchors the lowering") {
		t.Errorf("log %q", log.String())
	}
	log.Reset()
	if ok, un := waitToPrune(ctx, ans(gateAnswer{lowered: true}), used(0.95, nil), time.Millisecond, &log); !ok || !un || !strings.Contains(log.String(), "95% full") {
		t.Errorf("disk full: %v %v %q", ok, un, log.String())
	}
	if ok, un := waitToPrune(ctx, ans(gateAnswer{lowered: true}), used(0, errors.New("statfs")), time.Millisecond, &log); !ok || !un {
		t.Errorf("disk unknown: %v %v", ok, un)
	}
	cctx, ccancel := context.WithCancel(ctx)
	ccancel()
	if ok, _ := waitToPrune(cctx, ans(gateAnswer{lowered: true}), used(0.5, nil), time.Millisecond, &log); ok {
		t.Error("a cancelled wait prunes")
	}
}

// naut run's prune loop: a lowered retention that cannot wait (the disk
// full) prunes with its prunes marked unanchored; one that may prunes
// plainly.
func TestPruneAuditMarksAnUnanchoredPrune(t *testing.T) {
	for _, c := range []struct {
		gate gateAnswer
		used float64
		mark bool
	}{
		{gateAnswer{lowered: true}, 0.95, true},
		{gateAnswer{lowered: true, anchored: true}, 0.95, false},
		{gateAnswer{}, 0.95, false},
	} {
		dir := t.TempDir()
		now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
		j, err := audit.OpenFile(dir, audit.FileOptions{Keep: 24 * time.Hour, Now: func() time.Time { return now }})
		if err != nil {
			t.Fatal(err)
		}
		j.Record(audit.Entry{User: "u", Source: audit.SourceHMI, Action: "tag.write", Result: audit.ResultOK})
		now = now.Add(72 * time.Hour)
		j.Record(audit.Entry{User: "u", Source: audit.SourceHMI, Action: "tag.write", Result: audit.ResultOK})
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() {
			defer close(done)
			pruneAuditWith(ctx, j, func(context.Context) gateAnswer { return c.gate }, func() (float64, error) { return c.used, nil }, io.Discard)
		}()
		deadline := time.Now().Add(5 * time.Second)
		var line string
		for line == "" && time.Now().Before(deadline) {
			files, _ := filepath.Glob(filepath.Join(dir, "audit-*.jsonl"))
			for _, f := range files {
				b, _ := os.ReadFile(f)
				for _, l := range strings.Split(string(b), "\n") {
					if strings.Contains(l, `"action":"audit.prune"`) {
						line = l
					}
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
		cancel()
		<-done
		j.Close()
		if line == "" {
			t.Fatalf("%+v: no prune", c)
		}
		if got := strings.Contains(line, `"unanchored":true`); got != c.mark {
			t.Errorf("%+v: unanchored %v, want %v: %s", c, got, c.mark, line)
		}
	}
}
