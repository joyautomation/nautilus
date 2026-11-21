package main

// Saving a live I/O change as a git commit (#711 slice 4), and the same
// change as a patch where there is no git to commit with.
//
// The controller commits only where it runs from a checkout that is a git
// worktree (naut run on a project directory). A built controller has no
// repo: it answers 409 and GET /api/config/patch hands the change out as a
// patch to apply where the repo is. The files and the refusals are
// `naut pull --io`'s: only files this project's own manifest names, never
// over a file edited since the controller booted, written through os.Root.
// What is added to the commit is those files and nothing else of the
// worktree.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/joyautomation/nautilus/server"
)

// commitTrailer names the staged candidate a commit saves.
const commitTrailer = "Nautilus-Staged"

type gitCommitter struct {
	dir  string
	host string
	mu   sync.Mutex
}

// inGitWorktree reports whether dir is inside a git worktree.
func inGitWorktree(dir string) bool {
	if dir == "" {
		return false
	}
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--is-inside-work-tree").Output()
	return err == nil && strings.TrimSpace(string(out)) == "true"
}

// wireConfigCommit sets the hook's Commit (where dir is a git worktree) and
// Patch (always, against base).
func wireConfigCommit(h *server.IOHook, base fs.FS, dir string) {
	if h == nil || h.Files == nil {
		return
	}
	h.Patch = func() (string, error) { return ioPatch(base, h.Files()) }
	if !inGitWorktree(dir) {
		return
	}
	host, _ := os.Hostname()
	g := &gitCommitter{dir: dir, host: host}
	h.Commit = func(c server.IOCommit) (server.IOCommitted, error) { return g.commit(h.Files(), c) }
}

var unsafeIdent = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// ident turns a principal ("user:akhan", "token:ci") into a git identity:
// the name as the journal gives it, an address of the principal's name at
// this controller's host (the controller knows no mailbox).
func ident(principal, host string) (name, email string) {
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == '<' || r == '>' || r == 0x7f {
			return -1
		}
		return r
	}, principal)
	if name == "" {
		name = "anonymous"
	}
	local := unsafeIdent.ReplaceAllString(principal[strings.IndexByte(principal, ':')+1:], "-")
	if local = strings.Trim(local, "-."); local == "" {
		local = "anonymous"
	}
	host = unsafeIdent.ReplaceAllString(host, "-")
	if host == "" {
		host = "controller"
	}
	return name, local + "@" + host
}

func refuse(format string, a ...any) error {
	return fmt.Errorf("%w: %s", server.ErrIOCommitRefused, fmt.Sprintf(format, a...))
}

// ioChange is a file the running I/O configuration changes.
type ioChange struct {
	path  string
	was   []byte
	exist bool
	run   string
}

// planIOChange is `naut pull --io`'s check, for a commit: which files differ
// and whether each may be written. Nothing here writes.
func planIOChange(dir string, st server.IOFiles) ([]ioChange, error) {
	if !st.Leading {
		return nil, refuse("this controller is a standby: its running I/O configuration is not the one in force; commit on the leader")
	}
	if len(st.Units) == 0 {
		return nil, refuse("nothing to commit: what runs is the project's I/O configuration")
	}
	if len(st.Conflicts) > 0 {
		return nil, refuse("two different contents are held for %s (from different applies): re-apply one configuration, then commit", strings.Join(st.Conflicts, ", "))
	}
	local, pulledOnly := ioPullAllowed(dir, st.Files)
	var bad, fresh []string
	for _, f := range st.Files {
		switch {
		case local[f.Path]:
		case pulledOnly[f.Path]:
			fresh = append(fresh, f.Path)
		default:
			bad = append(bad, f.Path)
		}
	}
	if len(bad) > 0 {
		return nil, refuse("files this project's manifest does not name as I/O files cannot be committed (never program sources, hidden paths or .git): %s", strings.Join(bad, ", "))
	}
	if len(fresh) > 0 {
		return nil, refuse("the applied manifest names files this project's does not (%s): bring them in with `naut pull --io --new`, review and commit by hand", strings.Join(fresh, ", "))
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	var out []ioChange
	var edited []string
	for _, f := range st.Files {
		was, err := root.ReadFile(filepath.FromSlash(f.Path))
		exist := err == nil
		if err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("%s: %w", f.Path, err)
		}
		if exist && string(was) == f.Running {
			continue
		}
		if exist {
			sum := sha256.Sum256(was)
			if hex.EncodeToString(sum[:]) != f.Booted {
				edited = append(edited, f.Path)
			}
		}
		out = append(out, ioChange{path: f.Path, was: was, exist: exist, run: f.Running})
	}
	if len(edited) > 0 {
		return nil, refuse("not the files the controller booted from, so committing would overwrite your edits: %s. Commit or stash them, or `naut pull --io --dry-run` to review", strings.Join(edited, ", "))
	}
	if len(out) == 0 {
		return nil, refuse("nothing to commit: the project files already hold the running I/O configuration")
	}
	return out, nil
}

func (g *gitCommitter) git(env []string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", g.dir}, args...)...)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// commit writes the running change into the worktree and commits just those
// files, as the principal; the committer is the controller.
func (g *gitCommitter) commit(st server.IOFiles, c server.IOCommit) (server.IOCommitted, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	changes, err := planIOChange(g.dir, st)
	if err != nil {
		return server.IOCommitted{}, err
	}
	branch, err := g.git(nil, "symbolic-ref", "--short", "-q", "HEAD")
	if err != nil {
		return server.IOCommitted{}, refuse("the checkout is on a detached HEAD: check out the branch the change belongs on, then commit")
	}
	paths := make([]string, len(changes))
	for i, ch := range changes {
		paths[i] = ch.path
	}
	// Only a clean path is written over: uncommitted work in a file this
	// commit touches is never swept into it.
	if dirty, err := g.git(nil, append([]string{"status", "--porcelain", "--"}, paths...)...); err != nil {
		return server.IOCommitted{}, fmt.Errorf("git status: %s", dirty)
	} else if dirty != "" {
		return server.IOCommitted{}, refuse("the worktree has uncommitted changes in the files to commit (%s): commit or stash them first", strings.Join(paths, ", "))
	}
	root, err := os.OpenRoot(g.dir)
	if err != nil {
		return server.IOCommitted{}, err
	}
	defer root.Close()
	restore := func(n int) {
		for _, ch := range changes[:n] {
			if ch.exist {
				_ = root.WriteFile(filepath.FromSlash(ch.path), ch.was, 0o644)
			} else {
				_ = root.Remove(filepath.FromSlash(ch.path))
			}
		}
	}
	for i, ch := range changes {
		dst := filepath.FromSlash(ch.path)
		if err := root.MkdirAll(filepath.Dir(dst), 0o755); err == nil {
			err = root.WriteFile(dst, []byte(ch.run), 0o644)
		}
		if err != nil {
			restore(i)
			return server.IOCommitted{}, fmt.Errorf("%s: %w", ch.path, err)
		}
	}
	undo := func(why error, out string) (server.IOCommitted, error) {
		restore(len(changes))
		_, _ = g.git(nil, append([]string{"reset", "-q", "--"}, paths...)...)
		return server.IOCommitted{}, fmt.Errorf("git %v: %s (the files are back as they were)", why, out)
	}
	if out, err := g.git(nil, append([]string{"add", "--"}, paths...)...); err != nil {
		return undo(err, out)
	}
	msg := c.Message
	if msg == "" {
		msg = "config: live I/O change (" + driverNames(st) + ")"
	}
	body := fmt.Sprintf("%s\n\nApplied live by %s over the controller's API and saved from it.\n\n%s: %s\n", msg, c.By, commitTrailer, c.ID)
	an, ae := ident(c.By, g.host)
	env := []string{
		"GIT_AUTHOR_NAME=" + an, "GIT_AUTHOR_EMAIL=" + ae,
		"GIT_COMMITTER_NAME=nautilus", "GIT_COMMITTER_EMAIL=nautilus@" + ident2(g.host),
	}
	// --only: exactly these paths, whatever else is staged.
	if out, err := g.git(env, append([]string{"commit", "-q", "--only", "-m", body, "--"}, paths...)...); err != nil {
		return undo(err, out)
	}
	sha, err := g.git(nil, "rev-parse", "HEAD")
	if err != nil {
		return server.IOCommitted{}, errors.New("committed, but could not read the commit: " + sha)
	}
	return server.IOCommitted{Commit: sha, Branch: branch, Files: paths}, nil
}

func ident2(host string) string {
	if host = unsafeIdent.ReplaceAllString(host, "-"); host != "" {
		return host
	}
	return "controller"
}

func driverNames(st server.IOFiles) string {
	var n []string
	for _, u := range st.Units {
		s := u.Driver
		if u.Device != "" {
			s += "/" + u.Device
		}
		n = append(n, s)
	}
	return strings.Join(n, ", ")
}

// ioPatch is the running I/O configuration's files as a patch against base,
// for `git apply`: what a commit would write, for a controller with no repo.
func ioPatch(base fs.FS, st server.IOFiles) (string, error) {
	if !st.Leading {
		return "", refuse("this controller is a standby: its running I/O configuration is not the one in force; ask the leader")
	}
	if len(st.Conflicts) > 0 {
		return "", refuse("two different contents are held for %s (from different applies): re-apply one configuration first", strings.Join(st.Conflicts, ", "))
	}
	var sb strings.Builder
	for _, f := range st.Files {
		if !ioPullSafePath(f.Path) {
			return "", refuse("the controller offers %s, which is not a project I/O file", f.Path)
		}
		was, err := fs.ReadFile(base, f.Path)
		exist := err == nil
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("%s: %w", f.Path, err)
		}
		if exist && string(was) == f.Running {
			continue
		}
		a := "a/" + f.Path
		fmt.Fprintf(&sb, "diff --git a/%s b/%s\n", f.Path, f.Path)
		if !exist {
			sb.WriteString("new file mode 100644\n")
			a = "/dev/null"
		}
		d := unifiedDiff(a, "b/"+f.Path, string(was), f.Running)
		sb.WriteString(d)
	}
	return sb.String(), nil
}
