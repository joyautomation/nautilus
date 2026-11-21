package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/audit"
	"github.com/joyautomation/nautilus/internal/project"
	"github.com/joyautomation/nautilus/retain"
	"github.com/joyautomation/nautilus/runtime"
)

const wbManifest = `name: wb
tasks:
  - program: main.st
retain: {}
tags:
  # tuned on the plant
  - name: SP
    role: setpoint
    init: 65
    min: 10
    max: 90
  - name: Flow
    role: setpoint
    init: 1.5
`

type wbRecorder struct{ got []audit.Entry }

func (r *wbRecorder) Record(e audit.Entry) (audit.Entry, error) {
	r.got = append(r.got, e)
	return e, nil
}

// wbRig is a git project, a controller running it, and a write-back over it.
type wbRig struct {
	dir   string
	store *memRetain
	rt    *runtime.Runtime
	wb    *paramWriteBack
	rec   *wbRecorder
	now   time.Time
	stop  func()
}

func newWBRig(t *testing.T) *wbRig {
	t.Helper()
	dir := t.TempDir()
	for n, b := range map[string]string{"nautilus.yaml": wbManifest, "main.st": "PROGRAM P\nEND_PROGRAM\n"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(b), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitIn(t, dir, "init", "-q", "-b", "main")
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "-q", "-m", "init")
	r := &wbRig{dir: dir, store: &memRetain{}, rec: &wbRecorder{}, now: time.Now()}
	r.boot(t)
	return r
}

// boot starts the controller as a restart or redeploy would, from the
// project on disk and the retained store.
func (r *wbRig) boot(t *testing.T) {
	t.Helper()
	proj, err := project.Load(os.DirFS(r.dir), "")
	if err != nil {
		t.Fatal(err)
	}
	if r.stop != nil {
		r.stop() // the old controller stops before the new one starts
		r.stop = nil
	}
	proj.Runtime.Retain = r.store
	rt, err := runtime.New(proj.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	// As naut run does: the store is read before the API serves, and Run
	// owns the saver SaveRetained asks.
	rt.LoadRetained()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { rt.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	if r.stop != nil {
		r.stop()
	}
	r.stop = func() { cancel(); <-done }
	for i := 0; rt.SaveRetained(context.Background()) != nil; i++ { // leads once it has scanned
		if i > 500 {
			t.Fatal("the controller never came up")
		}
		time.Sleep(10 * time.Millisecond)
	}
	r.rt = rt
	r.wb = newParamWriteBack(rt, "commit", r.dir, "", os.DirFS(r.dir), r.rec)
	r.wb.debounce, r.wb.maxPending = 30*time.Second, 50
	r.wb.now = func() time.Time { return r.now }
	if r.wb.g == nil {
		t.Fatal("no committer in a git worktree")
	}
	r.wb.g.boot = gitIn(t, r.dir, "rev-parse", "HEAD")
}

func (r *wbRig) write(t *testing.T, by, name string, v float64) {
	t.Helper()
	if err := r.rt.Tags().WriteAs([]runtime.WriteOp{{Path: name, Value: v}}, by)[0]; err != nil {
		t.Fatal(err)
	}
	r.now = time.Now() // the write happened "now"
}

func (r *wbRig) commits(t *testing.T) []string {
	t.Helper()
	out := gitIn(t, r.dir, "log", "--format=%an|%s", "main")
	return strings.Split(out, "\n")
}

// The whole loop: an edit waits out the quiet period, lands as one commit
// per editor authored by that editor, is journalled, is not committed
// again, and a redeploy of the committed repo puts the parameter in step.
func TestParamWriteBackCommitLoop(t *testing.T) {
	r := newWBRig(t)
	r.write(t, "user:akhan", "SP", 70)
	if n := r.wb.step(); n != 0 {
		t.Fatalf("committed %d inside the quiet period", n)
	}
	r.write(t, "user:bchen", "Flow", 2.25)
	r.now = r.now.Add(31 * time.Second)
	if n := r.wb.step(); n != 2 {
		t.Fatalf("commits = %d, want one per editor (2)", n)
	}
	log := r.commits(t)
	if len(log) != 3 || !strings.HasPrefix(log[0], "user:bchen|parameters: tune Flow: 1.5 -> 2.25") || !strings.HasPrefix(log[1], "user:akhan|parameters: tune SP: 65 -> 70") {
		t.Fatalf("log: %q", log)
	}
	got, _ := os.ReadFile(filepath.Join(r.dir, "nautilus.yaml"))
	want := strings.Replace(strings.Replace(wbManifest, "init: 65", "init: 70", 1), "init: 1.5", "init: 2.25", 1)
	if string(got) != want {
		t.Fatalf("manifest:\n%s", got)
	}
	if len(r.rec.got) != 2 || r.rec.got[0].Action != "param.commit" || r.rec.got[0].User != "system" || r.rec.got[0].Detail["by"] != "user:akhan" {
		t.Fatalf("journal: %+v", r.rec.got)
	}
	// Not again: the plant is still ahead of the *running* init, but
	// committed.
	r.now = r.now.Add(time.Hour)
	if n := r.wb.step(); n != 0 {
		t.Fatalf("committed the same values again: %d", n)
	}
	if len(r.rt.PendingParameters()) != 0 {
		t.Fatalf("pending after commit: %+v", r.rt.PendingParameters())
	}
	// Restart (no redeploy): still not pending, so still not re-committed.
	r.boot(t)
	r.now = r.now.Add(time.Hour)
	if n := r.wb.step(); n != 0 || len(r.commits(t)) != 3 {
		t.Fatalf("a restart committed again: %d, %q", n, r.commits(t))
	}
	// A new edit after the commit is pending again.
	r.write(t, "user:akhan", "SP", 71)
	r.now = r.now.Add(31 * time.Second)
	if n := r.wb.step(); n != 1 {
		t.Fatalf("new edit: %d", n)
	}
	// Redeploy from the committed repo: in step.
	r.boot(t)
	for _, p := range r.rt.Parameters() {
		if p.State != runtime.ParamInStep {
			t.Errorf("after redeploy: %+v", p)
		}
	}
}

// Many pending parameters commit at once, without waiting for quiet.
func TestParamWriteBackBatchThreshold(t *testing.T) {
	r := newWBRig(t)
	r.wb.maxPending = 2
	r.write(t, "user:akhan", "SP", 70)
	if n := r.wb.step(); n != 0 {
		t.Fatal("committed one below the threshold")
	}
	r.write(t, "user:akhan", "Flow", 3)
	if n := r.wb.step(); n != 1 {
		t.Fatalf("at the threshold: %d commits, want 1", n)
	}
	if log := r.commits(t); !strings.Contains(log[0], "2 setpoints tuned") {
		t.Fatalf("log %q", log)
	}
}

// Only a plant-ahead value is ever written: a repo that moved on is left
// for a person.
func TestParamWriteBackNeverWritesRepoAheadOrConflict(t *testing.T) {
	r := newWBRig(t)
	r.write(t, "user:akhan", "SP", 70)
	r.rt.SaveRetained(context.Background())
	// Someone commits a new init for SP; the controller redeploys.
	m, _ := os.ReadFile(filepath.Join(r.dir, "nautilus.yaml"))
	os.WriteFile(filepath.Join(r.dir, "nautilus.yaml"), []byte(strings.Replace(string(m), "init: 65", "init: 40", 1)), 0o644)
	gitIn(t, r.dir, "commit", "-q", "-am", "someone retuned")
	r.boot(t)
	r.now = r.now.Add(time.Hour)
	if n := r.wb.step(); n != 0 {
		t.Fatalf("wrote a conflicting value: %d", n)
	}
	for _, p := range r.rt.Parameters() {
		if p.Name == "SP" && p.State != runtime.ParamConflict {
			t.Fatalf("SP: %+v", p)
		}
	}
}

// A refusal (uncommitted work in the file) leaves everything as it was,
// retries later, and commits once the tree is clean.
func TestParamWriteBackRetriesAfterARefusal(t *testing.T) {
	r := newWBRig(t)
	r.write(t, "user:akhan", "SP", 70)
	m, _ := os.ReadFile(filepath.Join(r.dir, "nautilus.yaml"))
	os.WriteFile(filepath.Join(r.dir, "nautilus.yaml"), append(m, "# wip\n"...), 0o644)
	r.now = r.now.Add(31 * time.Second)
	if n := r.wb.step(); n != 0 {
		t.Fatalf("committed over uncommitted work: %d", n)
	}
	if got, _ := os.ReadFile(filepath.Join(r.dir, "nautilus.yaml")); !strings.HasSuffix(string(got), "# wip\n") || strings.Contains(string(got), "init: 70") {
		t.Fatalf("the file was touched:\n%s", got)
	}
	gitIn(t, r.dir, "commit", "-q", "-am", "wip")
	r.now = r.now.Add(30 * time.Second) // inside the backoff
	if n := r.wb.step(); n != 0 {
		t.Fatalf("retried inside the backoff")
	}
	r.now = r.now.Add(2 * time.Minute)
	if n := r.wb.step(); n != 1 {
		t.Fatalf("did not retry after the backoff: %d", n)
	}
}

// What cannot be edited stays drift and the listing says why; a standby
// and a mode that is not commit do nothing.
func TestParamWriteBackUnwritableAndModes(t *testing.T) {
	r := newWBRig(t)
	m, _ := os.ReadFile(filepath.Join(r.dir, "nautilus.yaml"))
	os.WriteFile(filepath.Join(r.dir, "nautilus.yaml"), []byte(strings.Replace(string(m), "init: 1.5", `init: "1.5"`, 1)), 0o644)
	gitIn(t, r.dir, "commit", "-q", "-am", "quote")
	r.boot(t)
	// A quoted init is text, so Flow is not a parameter at all; SP is.
	r.write(t, "user:akhan", "SP", 70)
	r.wb.mode = "patch"
	r.now = r.now.Add(time.Hour)
	if n := r.wb.step(); n != 0 {
		t.Fatal("patch mode committed")
	}
	r.wb.mode = "commit"
	if n := r.wb.step(); n != 1 {
		t.Fatalf("commit mode: %d", n)
	}
}

func TestParamWriteBackPatchForARepolessController(t *testing.T) {
	r := newWBRig(t)
	r.wb.g = nil
	r.write(t, "user:akhan", "SP", 70)
	p, err := r.wb.patch()
	if err != nil || !strings.Contains(p, "-    init: 65\n+    init: 70\n") || !strings.HasPrefix(p, "diff --git a/nautilus.yaml b/nautilus.yaml\n") {
		t.Fatalf("patch %q err %v", p, err)
	}
	if got, _ := os.ReadFile(filepath.Join(r.dir, "nautilus.yaml")); string(got) != wbManifest {
		t.Fatal("a patch wrote to the project")
	}
	if errors.Is(err, os.ErrNotExist) {
		t.Fatal()
	}
	var _ retain.State
}

// After a commit the file holds the committed value while the running init
// is the old one. A second edit is written over it (the file moved by our
// own commit, not by anyone else), and tuning back to the running init is
// committed too, so the repo never silently disagrees with the plant.
func TestParamWriteBackAfterItsOwnCommit(t *testing.T) {
	r := newWBRig(t)
	quiet := func() { r.now = r.now.Add(31 * time.Second) }
	r.write(t, "user:akhan", "SP", 70)
	quiet()
	if n := r.wb.step(); n != 1 {
		t.Fatalf("first commit: %d", n)
	}
	// A second edit: 70 -> 72, running init still 65.
	r.write(t, "user:akhan", "SP", 72)
	quiet()
	if n := r.wb.step(); n != 1 {
		t.Fatalf("second edit after a commit: %d commits, unwritable %v", n, r.wb.hook().Unwritable())
	}
	if m, _ := os.ReadFile(filepath.Join(r.dir, "nautilus.yaml")); !strings.Contains(string(m), "init: 72\n") {
		t.Fatalf("manifest:\n%s", m)
	}
	// Back to the running init (65): in step with what runs, yet the repo
	// says 72, so the init goes back.
	r.write(t, "user:akhan", "SP", 65)
	quiet()
	if n := r.wb.step(); n != 1 {
		t.Fatalf("tuned back: %d", n)
	}
	if m, _ := os.ReadFile(filepath.Join(r.dir, "nautilus.yaml")); !strings.Contains(string(m), "init: 65\n") {
		t.Fatalf("manifest:\n%s", m)
	}
	quiet()
	if n := r.wb.step(); n != 0 || len(r.rt.PendingParameters()) != 0 {
		t.Fatalf("still pending after the revert: %d %+v", n, r.rt.PendingParameters())
	}
	if log := r.commits(t); len(log) != 4 {
		t.Fatalf("log %q", log)
	}
}

// The I/O commit and the write-back share one committer for a worktree: one
// lock over git's index, one record of what the controller wrote.
func TestCommitterIsSharedPerWorktree(t *testing.T) {
	dir := gitProject(t, "a: 1\n")
	if committerFor(dir) != committerFor(dir) {
		t.Fatal("two committers for one worktree")
	}
	if newParamWriteBack(nil, "commit", dir, "", nil, nil).g != committerFor(dir) {
		t.Fatal("the write-back has its own committer")
	}
}
