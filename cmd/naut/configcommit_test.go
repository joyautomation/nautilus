package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/server"
)

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=dev", "GIT_AUTHOR_EMAIL=dev@x", "GIT_COMMITTER_NAME=dev", "GIT_COMMITTER_EMAIL=dev@x")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// gitProject is projectWith inside a repo with everything committed on main.
func gitProject(t *testing.T, body string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	dir := projectWith(t, "modbus.yaml", body)
	if err := os.WriteFile(filepath.Join(dir, "other.st"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "init", "-q", "-b", "main")
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "-q", "-m", "init")
	return dir
}

func committer(dir string) *gitCommitter { return &gitCommitter{dir: dir, host: "plant-1"} }

func TestConfigCommitCommitsOnlyTheChangedFiles(t *testing.T) {
	dir := gitProject(t, "a: 1\nb: 2\n")
	// Other work in the worktree, staged and not, must stay out of it.
	os.WriteFile(filepath.Join(dir, "other.st"), []byte("changed\n"), 0o644)
	gitIn(t, dir, "add", "other.st")
	os.WriteFile(filepath.Join(dir, "scratch.txt"), []byte("s\n"), 0o644)

	res, err := committer(dir).commit(ioState("a: 1\nb: 2\n", "a: 1\nb: 3\n"), server.IOCommit{By: "user:akhan", ID: "7"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Branch != "main" || len(res.Files) != 1 || res.Files[0] != "modbus.yaml" || res.Commit != gitIn(t, dir, "rev-parse", "HEAD") {
		t.Fatalf("result %+v", res)
	}
	if files := gitIn(t, dir, "show", "--name-only", "--format=", "HEAD"); files != "modbus.yaml" {
		t.Fatalf("commit holds %q, want only modbus.yaml", files)
	}
	if got := gitIn(t, dir, "show", "-s", "--format=%an <%ae>|%cn <%ce>", "HEAD"); got != "user:akhan <akhan@plant-1>|nautilus <nautilus@plant-1>" {
		t.Fatalf("author|committer = %q", got)
	}
	if body := gitIn(t, dir, "show", "-s", "--format=%B", "HEAD"); !strings.Contains(body, "\nNautilus-Staged: 7") || !strings.Contains(body, "user:akhan") {
		t.Fatalf("message %q lacks the trailer or the principal", body)
	}
	if got := readFile(t, dir, "modbus.yaml"); got != "a: 1\nb: 3\n" {
		t.Fatalf("modbus.yaml = %q", got)
	}
	// The staged other.st is still staged, the scratch file untouched.
	if st := gitIn(t, dir, "status", "--porcelain"); !strings.Contains(st, "M  other.st") || !strings.Contains(st, "?? scratch.txt") {
		t.Fatalf("status after: %q", st)
	}
}

func TestConfigCommitMessage(t *testing.T) {
	dir := gitProject(t, "a: 1\n")
	if _, err := committer(dir).commit(ioState("a: 1\n", "a: 2\n"), server.IOCommit{By: "token", ID: "1", Message: "raise setpoint"}); err != nil {
		t.Fatal(err)
	}
	if s := gitIn(t, dir, "show", "-s", "--format=%s", "HEAD"); s != "raise setpoint" {
		t.Fatalf("subject %q", s)
	}
}

func TestConfigCommitRefusals(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, dir string)
		st    server.IOFiles
		want  string
		keeps string // modbus.yaml afterwards
	}{
		{"edited since boot", func(t *testing.T, dir string) {
			os.WriteFile(filepath.Join(dir, "modbus.yaml"), []byte("a: 1\nb: mine\n"), 0o644)
			gitIn(t, dir, "commit", "-qam", "mine")
		}, ioState("a: 1\nb: 2\n", "a: 1\nb: 3\n"), "overwrite your edits", "a: 1\nb: mine\n"},
		{"uncommitted work in the file", func(t *testing.T, dir string) {
			os.WriteFile(filepath.Join(dir, "modbus.yaml"), []byte("a: 1\nb: 2\n# wip\n"), 0o644)
		}, func() server.IOFiles { s := ioState("a: 1\nb: 2\n# wip\n", "a: 1\nb: 3\n"); return s }(), "uncommitted changes", "a: 1\nb: 2\n# wip\n"},
		{"detached HEAD", func(t *testing.T, dir string) { gitIn(t, dir, "checkout", "-q", "--detach") },
			ioState("a: 1\nb: 2\n", "a: 1\nb: 3\n"), "detached HEAD", "a: 1\nb: 2\n"},
		{"standby", nil, func() server.IOFiles { s := ioState("a: 1\nb: 2\n", "a: 1\nb: 3\n"); s.Leading = false; return s }(), "standby", "a: 1\nb: 2\n"},
		{"nothing differs", nil, server.IOFiles{Leading: true}, "nothing to commit", "a: 1\nb: 2\n"},
		{"conflicts", nil, func() server.IOFiles {
			s := ioState("a: 1\nb: 2\n", "a: 1\nb: 3\n")
			s.Conflicts = []string{"modbus.yaml"}
			return s
		}(), "two different contents", "a: 1\nb: 2\n"},
		{"not an I/O file", nil, server.IOFiles{Leading: true, Units: []server.IODiffer{{Driver: "m"}},
			Files: []server.IOFile{{Path: "other.st", Running: "y\n"}}}, "does not name as I/O files", "a: 1\nb: 2\n"},
		{"a file only the applied manifest names", nil, server.IOFiles{Leading: true, Units: []server.IODiffer{{Driver: "m"}},
			Files: []server.IOFile{
				{Path: "nautilus.yaml", Running: "name: p\ndrivers:\n  - {type: modbus, manifest: brand-new.yaml}\n"},
				{Path: "brand-new.yaml", Running: "n: 1\n"},
			}}, "naut pull --io --new", "a: 1\nb: 2\n"},
		{"a file the project does not name", nil, server.IOFiles{Leading: true, Units: []server.IODiffer{{Driver: "m"}},
			Files: []server.IOFile{{Path: "../escape.yaml", Running: "y\n"}}}, "does not name as I/O files", "a: 1\nb: 2\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := gitProject(t, "a: 1\nb: 2\n")
			if c.setup != nil {
				c.setup(t, dir)
			}
			head := gitIn(t, dir, "rev-parse", "HEAD")
			_, err := committer(dir).commit(c.st, server.IOCommit{By: "user:a", ID: "1"})
			if !errors.Is(err, server.ErrIOCommitRefused) || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want a refusal containing %q", err, c.want)
			}
			if got := gitIn(t, dir, "rev-parse", "HEAD"); got != head {
				t.Fatal("a refusal made a commit")
			}
			if got := readFile(t, dir, "modbus.yaml"); got != c.keeps {
				t.Fatalf("modbus.yaml = %q after a refusal, want %q", got, c.keeps)
			}
		})
	}
}

// A commit git refuses (a pre-commit hook) puts the files back as they were
// and leaves nothing staged.
func TestConfigCommitFailureRestoresTheFiles(t *testing.T) {
	dir := gitProject(t, "a: 1\nb: 2\n")
	hook := filepath.Join(dir, ".git", "hooks", "pre-commit")
	os.WriteFile(hook, []byte("#!/bin/sh\necho no >&2\nexit 1\n"), 0o755)
	head := gitIn(t, dir, "rev-parse", "HEAD")
	_, err := committer(dir).commit(ioState("a: 1\nb: 2\n", "a: 1\nb: 3\n"), server.IOCommit{By: "user:a", ID: "1"})
	if err == nil || errors.Is(err, server.ErrIOCommitRefused) || !strings.Contains(err.Error(), "back as they were") {
		t.Fatalf("err = %v", err)
	}
	if got := readFile(t, dir, "modbus.yaml"); got != "a: 1\nb: 2\n" {
		t.Fatalf("modbus.yaml = %q, want the original", got)
	}
	if st := gitIn(t, dir, "status", "--porcelain"); st != "" {
		t.Fatalf("status %q, want clean", st)
	}
	if gitIn(t, dir, "rev-parse", "HEAD") != head {
		t.Fatal("a commit was made")
	}
}

func TestConfigCommitIdentity(t *testing.T) {
	for _, c := range []struct{ by, name, email string }{
		{"user:akhan", "user:akhan", "akhan@h"},
		{"token:ci bot", "token:ci bot", "ci-bot@h"},
		{"token", "token", "token@h"},
		{"user:a<b>", "user:ab", "a-b@h"},
		{"", "anonymous", "anonymous@h"},
	} {
		if n, e := ident(c.by, "h"); n != c.name || e != c.email {
			t.Errorf("ident(%q) = %q <%q>, want %q <%q>", c.by, n, e, c.name, c.email)
		}
	}
}

// The patch applies to the project it was made against.
func TestConfigPatchApplies(t *testing.T) {
	dir := gitProject(t, "a: 1\nb: 2\n")
	st := ioState("a: 1\nb: 2\n", "a: 1\nb: 3\n")
	st.Files = append(st.Files, server.IOFile{Path: "profiles/new.yaml", Running: "n: 1\n"})
	p, err := ioPatch(os.DirFS(dir), st)
	if err != nil {
		t.Fatal(err)
	}
	pf := filepath.Join(t.TempDir(), "x.patch")
	os.WriteFile(pf, []byte(p), 0o644)
	gitIn(t, dir, "apply", "--check", pf)
	gitIn(t, dir, "apply", pf)
	if got := readFile(t, dir, "modbus.yaml"); got != "a: 1\nb: 3\n" {
		t.Fatalf("modbus.yaml = %q", got)
	}
	if got := readFile(t, dir, "profiles/new.yaml"); got != "n: 1\n" {
		t.Fatalf("new.yaml = %q", got)
	}
	// A file already as it runs is left out; a standby and a conflict refuse.
	if p, _ := ioPatch(os.DirFS(dir), st); p != "" {
		t.Fatalf("patch after applying: %q, want empty", p)
	}
	st.Leading = false
	if _, err := ioPatch(os.DirFS(dir), st); !errors.Is(err, server.ErrIOCommitRefused) {
		t.Fatalf("standby: %v", err)
	}
	st.Leading, st.Conflicts = true, []string{"modbus.yaml"}
	if _, err := ioPatch(os.DirFS(dir), st); !errors.Is(err, server.ErrIOCommitRefused) {
		t.Fatalf("conflict: %v", err)
	}
	st.Conflicts, st.Files = nil, []server.IOFile{{Path: "../x", Running: "y"}}
	if _, err := ioPatch(os.DirFS(dir), st); !errors.Is(err, server.ErrIOCommitRefused) {
		t.Fatalf("unsafe path: %v", err)
	}
}

func TestWireConfigCommitOnlyInAWorktree(t *testing.T) {
	h := &server.IOHook{Files: func() server.IOFiles { return server.IOFiles{} }}
	wireConfigCommit(h, os.DirFS(t.TempDir()), "")
	if h.Commit != nil || h.Patch == nil {
		t.Fatal("a built controller (no dir) must have a patch and no commit")
	}
	h = &server.IOHook{Files: func() server.IOFiles { return server.IOFiles{} }}
	wireConfigCommit(h, os.DirFS(t.TempDir()), t.TempDir())
	if h.Commit != nil {
		t.Fatal("a directory that is no worktree must have no commit")
	}
	dir := gitProject(t, "a: 1\n")
	h = &server.IOHook{Files: func() server.IOFiles { return server.IOFiles{} }}
	wireConfigCommit(h, os.DirFS(dir), dir)
	if h.Commit == nil {
		t.Fatal("a git worktree must have a commit")
	}
}
