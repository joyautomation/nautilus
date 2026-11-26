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
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/joyautomation/nautilus/server"
)

// commitTrailer names the staged candidate a commit saves.
const commitTrailer = "Nautilus-Staged"

type gitCommitter struct {
	dir  string
	host string
	// boot is HEAD when the controller started; wrote is the content hash of
	// each file this controller last committed. A file is "as booted" when
	// it holds what boot had or what this controller last wrote: anything
	// else was edited since (the files' own Booted hash cannot say, a
	// checkout's boot FS being the live directory).
	boot  string
	wrote map[string]string
	// pending is a file a save committed for the next restart: the commit and
	// the content hash it wrote. Another candidate (staged from what runs,
	// which lacks that change) must not overwrite it while the file still
	// holds it; a hand edit or revert releases it, and a restart clears it
	// (a new process).
	pending map[string]pendingSave
	mu      sync.Mutex
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
	g := committerFor(dir)
	h.Commit = func(c server.IOCommit) (server.IOCommitted, error) { return g.commit(h.Files(), c) }
	h.Save = g.save
}

var (
	committersMu sync.Mutex
	committers   = map[string]*gitCommitter{}
)

// committerFor is the one committer for a worktree: the live I/O commit and
// the parameter write-back change files in the same repo, so they share a
// lock (git's index) and a record of what the controller itself wrote. Its
// boot is HEAD when it was first asked for, at startup.
func committerFor(dir string) *gitCommitter {
	committersMu.Lock()
	defer committersMu.Unlock()
	if g, ok := committers[dir]; ok {
		return g
	}
	host, _ := os.Hostname()
	g := &gitCommitter{dir: dir, host: host, wrote: map[string]string{}}
	g.boot, _ = g.git(nil, "rev-parse", "HEAD")
	committers[dir] = g
	return g
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
func planIOChange(dir string, st server.IOFiles, asBooted func(f server.IOFile, was []byte) bool) ([]ioChange, error) {
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
		if exist && !asBooted(f, was) {
			edited = append(edited, f.Path)
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

// gitTimeout bounds one git run: a hook or a signing prompt that never
// answers must not hold the commit lock, with files written, forever.
var gitTimeout = 30 * time.Second

// asBooted reports whether was, the file on disk, is what the controller
// booted with or last committed itself.
func (g *gitCommitter) asBooted(f server.IOFile, was []byte) bool {
	sum := sha256.Sum256(was)
	h := hex.EncodeToString(sum[:])
	if g.wrote[f.Path] == h {
		return true
	}
	if g.boot == "" {
		return false
	}
	// "./" is relative to dir, which may be inside a bigger repository.
	b, err := exec.Command("git", "-C", g.dir, "show", g.boot+":./"+f.Path).Output()
	if err != nil {
		return false
	}
	bs := sha256.Sum256(b)
	return hex.EncodeToString(bs[:]) == h
}

func (g *gitCommitter) git(env []string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", g.dir}, args...)...)
	cmd.WaitDelay = 2 * time.Second
	// Paths are literal names, never globs or pathspec magic; the repo is
	// the one at dir, whatever the controller's environment says; nothing
	// may wait for a person at a terminal.
	cmd.Env = append(slices.DeleteFunc(os.Environ(), func(e string) bool {
		return strings.HasPrefix(e, "GIT_DIR=") || strings.HasPrefix(e, "GIT_INDEX_FILE=") || strings.HasPrefix(e, "GIT_WORK_TREE=")
	}), append([]string{"GIT_LITERAL_PATHSPECS=1", "GIT_TERMINAL_PROMPT=0"}, env...)...)
	out, err := cmd.CombinedOutput()
	if err != nil && ctx.Err() != nil {
		err = fmt.Errorf("timed out after %s: %w", gitTimeout, context.DeadlineExceeded)
	}
	return strings.TrimSpace(string(out)), err
}

// commit writes the running change into the worktree and commits just those
// files, as the principal; the committer is the controller.
func (g *gitCommitter) commit(st server.IOFiles, c server.IOCommit) (server.IOCommitted, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	changes, err := planIOChange(g.dir, st, g.asBooted)
	if err != nil {
		return server.IOCommitted{}, err
	}
	if err := g.checkPending(changes); err != nil {
		return server.IOCommitted{}, err
	}
	msg := c.Message
	if msg == "" {
		msg = "config: live I/O change (" + driverNames(st) + ")"
	}
	body := fmt.Sprintf("%s\n\nSaved by %s from the controller's live I/O configuration.\n\n%s: %s\n", msg, c.By, commitTrailer, c.ID)
	return g.commitFiles(changes, c.By, body)
}

// pendingSave is a saved file's commit and the hash of the content it wrote.
type pendingSave struct{ commit, hash string }

// checkPending refuses a write over a file a save committed for the next
// restart, while the file still holds what the save wrote: the candidate was
// made from what runs, which lacks that change, so writing it would silently
// undo the saved one. The caller holds g.mu.
func (g *gitCommitter) checkPending(changes []ioChange) error {
	for _, ch := range changes {
		if p, waiting := g.pending[ch.path]; waiting && ch.exist && hashHex(string(ch.was)) == p.hash {
			return refuse("an earlier saved change to %s (commit %s) is waiting for a restart, and this candidate does not include it: restart the controller (the saved change applies then), then make this change", ch.path, p.commit)
		}
	}
	return nil
}

// save commits a candidate that applies at the next restart: the files it
// changes, written and committed in the principal's name. Nothing runs
// differently until the controller restarts. The same refusals as a live
// commit: only files the manifest names, never over a file edited since the
// controller booted, never on a detached HEAD.
func (g *gitCommitter) save(c server.IOSave) (server.IOCommitted, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	// A save commits to this checkout whatever this controller's role: on a
	// pair, the standby is restarted first, and its checkout is its own.
	st := server.IOFiles{Leading: true, Units: []server.IODiffer{{Driver: "configuration"}}}
	paths := make([]string, 0, len(c.Files))
	for p := range c.Files {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	for _, p := range paths {
		st.Files = append(st.Files, server.IOFile{Path: p, Running: c.Files[p]})
	}
	changes, err := planIOChange(g.dir, st, g.asBooted)
	if err != nil {
		// A save writes only files the project's manifest already names: a new
		// profile or driver manifest is vendored in the repository first.
		const wayOut = "a save writes only files the project's manifest already names: vendor a new profile in the repository first (naut devices add <profile>), commit it, then save this change"
		switch {
		case errors.Is(err, server.ErrIOCommitRefused) && strings.Contains(err.Error(), "bring them in with"):
			err = refuse("the candidate brings in files this project's manifest does not name; %s", wayOut)
		case errors.Is(err, server.ErrIOCommitRefused) && strings.Contains(err.Error(), "cannot be committed"):
			err = fmt.Errorf("%w; %s", err, wayOut)
		}
		return server.IOCommitted{}, err
	}
	if err := g.checkPending(changes); err != nil {
		return server.IOCommitted{}, err
	}
	msg := c.Message
	if msg == "" {
		msg = "config: saved for the next restart (" + strings.Join(paths, ", ") + ")"
	}
	body := fmt.Sprintf("%s\n\nSaved by %s from the controller's configuration screen. It applies when the controller next restarts; nothing running changed.\n\n%s: %s\n", msg, c.By, commitTrailer, c.ID)
	res, err := g.commitFiles(changes, c.By, body)
	if err == nil {
		if g.pending == nil {
			g.pending = map[string]pendingSave{}
		}
		for _, ch := range changes {
			g.pending[ch.path] = pendingSave{commit: res.Commit[:min(12, len(res.Commit))], hash: hashHex(ch.run)}
		}
	}
	return res, err
}

// commitFiles writes changes into the worktree and commits just those
// files with message body, authored by the principal; the committer is the
// controller. The caller holds g.mu and has decided the files may change.
// A detached HEAD and uncommitted work in a file to be written are refused;
// any failure puts the files back as they were.
func (g *gitCommitter) commitFiles(changes []ioChange, by, body string) (server.IOCommitted, error) {
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
		if errors.Is(why, context.DeadlineExceeded) {
			// The killed git left its index lock behind, and nothing else
			// of ours is running (the commit lock is held).
			if lock, err := g.git(nil, "rev-parse", "--git-path", "index.lock"); err == nil {
				if !filepath.IsAbs(lock) {
					lock = filepath.Join(g.dir, lock)
				}
				_ = os.Remove(lock)
			}
		}
		_, _ = g.git(nil, append([]string{"reset", "-q", "--"}, paths...)...)
		return server.IOCommitted{}, fmt.Errorf("git %v: %s (the files are back as they were)", why, out)
	}
	if out, err := g.git(nil, append([]string{"add", "--"}, paths...)...); err != nil {
		return undo(err, out)
	}
	an, ae := ident(by, g.host)
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
	for _, ch := range changes {
		g.wrote[ch.path] = hashHex(ch.run)
	}
	return server.IOCommitted{Commit: sha, Branch: branch, Files: paths}, nil
}

func hashHex(s string) string {
	b := sha256.Sum256([]byte(s))
	return hex.EncodeToString(b[:])
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
