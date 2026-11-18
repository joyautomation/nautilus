package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	head := j.Head()
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
