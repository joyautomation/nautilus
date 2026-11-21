package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	goruntime "runtime"
	"syscall"
	"testing"
	"time"
)

// The audit-trail demo (tools/audit-demo/run.sh, #709 PR 10) runs clean
// against a real Postgres: each person's actions journalled under them, a
// refusal and an expiry by system included; verify says verified, then
// BROKEN for an edited line and for a deleted file while the copy holds
// the original, then verified again, and verified with a WARNING once the
// retention is lowered. The script checks each of those itself and exits
// non-zero on the first that does not hold; this test runs it, so the
// demo and its guide can't drift from what the controller does.
func TestPostgresAuditDemoScript(t *testing.T) {
	dsn := os.Getenv("NAUTILUS_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set NAUTILUS_TEST_DATABASE_URL to run the audit demo against a real Postgres")
	}
	if testing.Short() {
		t.Skip("builds and runs the CLI")
	}
	if goruntime.GOOS == "windows" {
		t.Skip("a bash script")
	}
	for _, tool := range []string{"bash", "curl", "jq", "psql"} {
		if _, err := exec.LookPath(tool); err != nil {
			if os.Getenv("CI") != "" {
				t.Fatalf("the audit demo needs %s on the CI runner", tool)
			}
			t.Skipf("the audit demo needs %s", tool)
		}
	}
	bin := t.TempDir()
	naut := buildNautCLI(t, bin, nil)
	seed := filepath.Join(bin, "seed")
	if out, err := exec.Command("go", "build", "-o", seed, "../../tools/audit-demo/seed").CombinedOutput(); err != nil {
		t.Fatalf("go build seed: %v\n%s", err, out)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "../../tools/audit-demo/run.sh", "--no-pause")
	// On a timeout, SIGTERM first: the script's trap then stops naut run
	// and drops its database and role.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 30 * time.Second
	cmd.Env = append(os.Environ(), "NAUT="+naut, "SEED="+seed, "DEMO_ADMIN_DSN="+dsn, "DEMO_DIR="+filepath.Join(t.TempDir(), "demo"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the audit demo failed: %v\n%s", err, out)
	}
	// The script fails on its own checks; these pin that it reached them.
	for _, want := range []string{
		`user:alice\s+force\.set\s+refused`,
		`system\s+substitute\.expire\s+PIT101_Pressure`,
		`BROKEN: the journal's chain breaks`,
		`the line was rewritten after it was copied`,
		`seq \d+: user:alice alarm\.ack`,
		`WARNING: retention LOWERED from 30d to 7d`,
		`the audit demo ran clean`,
	} {
		if !regexp.MustCompile(want).Match(out) {
			t.Errorf("the demo's output has no %q:\n%s", want, out)
		}
	}
}
