package main

// Parameter write-back (#718 slice 2): an operator tunes a setpoint on the
// plant, and the value goes back into the repo as that tag's `init:`.
//
// There is no queue. What is pending is derived from the controller's own
// state each time: the parameters whose value the plant holds ahead of the
// repo (runtime.PendingParameters). That is why a restart, a takeover or a
// redeploy needs no handling here: the new process derives the same set from
// the same retained state. Only plant-ahead values are written. A
// repo-ahead or conflicting one is never written automatically: a person
// decides which side is right.

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/joyautomation/nautilus/audit"
	"github.com/joyautomation/nautilus/runtime"
	"github.com/joyautomation/nautilus/server"
)

// paramTrailer names how many parameters a commit saved.
const paramTrailer = "Nautilus-Parameters"

type paramWriteBack struct {
	rt       *runtime.Runtime
	g        *gitCommitter // nil: no git worktree, so a patch only
	dir      string
	manifest string
	base     fs.FS
	rec      audit.Recorder
	mode     string // patch | commit

	debounce   time.Duration // quiet time after the last edit
	maxPending int           // commit at once from this many pending
	tick       time.Duration
	backoff    time.Duration // after a refusal
	now        func() time.Time

	mu         sync.Mutex
	unwritable map[string]string
	reported   string
	nextTry    time.Time
}

func newParamWriteBack(rt *runtime.Runtime, mode, dir, manifest string, base fs.FS, rec audit.Recorder) *paramWriteBack {
	if manifest == "" {
		manifest = "nautilus.yaml"
	}
	w := &paramWriteBack{
		rt: rt, dir: dir, manifest: manifest, base: base, rec: rec, mode: mode,
		debounce: 30 * time.Second, maxPending: 50, tick: 5 * time.Second, backoff: time.Minute,
		now: time.Now, unwritable: map[string]string{},
	}
	if mode == "commit" && inGitWorktree(dir) {
		w.g = committerFor(dir)
	}
	return w
}

func (w *paramWriteBack) hook() *server.ParamWriteBack {
	return &server.ParamWriteBack{
		Mode:  w.mode,
		Patch: w.patch,
		Unwritable: func() map[string]string {
			w.mu.Lock()
			defer w.mu.Unlock()
			out := make(map[string]string, len(w.unwritable))
			for k, v := range w.unwritable {
				out[k] = v
			}
			return out
		},
	}
}

// patch is every plant-ahead value, written as a patch against the project
// the controller runs, for a controller with no repo to commit to.
func (w *paramWriteBack) patch() (string, error) {
	if !w.rt.Leading() {
		return "", refuse("this controller is a standby: its parameter values are not the plant's; ask the leader")
	}
	plan, err := planParamWriteBack(fsReader(w.base), w.manifest, w.rt.PendingParameters())
	if err != nil {
		return "", err
	}
	w.setUnwritable(plan.Unwritable)
	var sb strings.Builder
	for _, f := range plan.Files {
		fmt.Fprintf(&sb, "diff --git a/%s b/%s\n", f.path, f.path)
		sb.WriteString(unifiedDiff("a/"+f.path, "b/"+f.path, string(f.was), f.run))
	}
	return sb.String(), nil
}

func (w *paramWriteBack) setUnwritable(u []paramUnwritable) {
	m := make(map[string]string, len(u))
	var sig []string
	for _, x := range u {
		m[x.Name] = x.Why
		sig = append(sig, x.Name+": "+x.Why)
	}
	sort.Strings(sig)
	w.mu.Lock()
	w.unwritable = m
	s := strings.Join(sig, "\n")
	changed := s != w.reported
	w.reported = s
	w.mu.Unlock()
	if changed {
		for _, x := range u {
			slog.Warn("parameters: left as drift", "parameter", x.Name, "why", x.Why)
		}
	}
}

// Run commits until ctx ends.
func (w *paramWriteBack) Run(ctx context.Context) {
	t := time.NewTicker(w.tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.step()
		}
	}
}

// step makes the commits that are due, and says how many it made.
func (w *paramWriteBack) step() int {
	if w.mode != "commit" || w.g == nil || !w.rt.Leading() {
		return 0
	}
	pend := w.rt.PendingParameters()
	if len(pend) == 0 {
		w.setUnwritable(nil)
		return 0
	}
	now := w.now()
	if now.Before(w.nextTry) {
		return 0
	}
	var last int64
	for _, p := range pend {
		last = max(last, p.EditedAtMs)
	}
	if len(pend) < w.maxPending && now.UnixMilli()-last < w.debounce.Milliseconds() {
		return 0 // still being tuned: wait for a quiet moment
	}
	byEditor := map[string][]runtime.Parameter{}
	for _, p := range pend {
		by := p.EditedBy
		if by == "" {
			by = "unknown"
		}
		byEditor[by] = append(byEditor[by], p)
	}
	editors := make([]string, 0, len(byEditor))
	for e := range byEditor {
		editors = append(editors, e)
	}
	sort.Strings(editors)

	read, closeRoot, err := dirReader(w.dir)
	if err != nil {
		slog.Warn("parameters: cannot read the project", "err", err)
		w.nextTry = now.Add(w.backoff)
		return 0
	}
	defer closeRoot()
	var unwritable []paramUnwritable
	made := 0
	for _, by := range editors {
		w.g.mu.Lock()
		plan, err := planParamWriteBack(read, w.manifest, byEditor[by])
		if err != nil {
			w.g.mu.Unlock()
			slog.Warn("parameters: not written", "by", by, "err", err)
			w.nextTry = now.Add(w.backoff)
			break
		}
		unwritable = append(unwritable, plan.Unwritable...)
		if len(plan.Files) == 0 {
			w.g.mu.Unlock()
			if len(plan.Written) > 0 { // the repo already says it: settle it
				w.rt.MarkParametersCommitted(plan.Written)
				made++
			}
			continue
		}
		res, err := w.g.commitFiles(plan.Files, by, paramCommitMessage(by, plan))
		w.g.mu.Unlock()
		if err != nil {
			slog.Warn("parameters: commit refused, will retry", "by", by, "err", err)
			w.nextTry = now.Add(w.backoff)
			break
		}
		made++
		w.rt.MarkParametersCommitted(plan.Written)
		w.record(by, plan, res)
		slog.Info("parameters: committed", "by", by, "parameters", len(plan.Written), "commit", res.Commit, "branch", res.Branch)
	}
	w.setUnwritable(unwritable)
	if made > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = w.rt.SaveRetained(ctx) // a restart must not commit these again
	}
	return made
}

func paramCommitMessage(by string, plan *paramPlan) string {
	var subject string
	if len(plan.Lines) == 1 {
		subject = "parameters: tune " + plan.Lines[0]
	} else {
		subject = fmt.Sprintf("parameters: %d setpoints tuned", len(plan.Lines))
	}
	return fmt.Sprintf("%s\n\n%s\n\nSaved from the controller's live parameter values; tuned by %s.\n\n%s: %d\n",
		subject, strings.Join(plan.Lines, "\n"), by, paramTrailer, len(plan.Lines))
}

// record journals the commit: the controller made it, so the entry is the
// system's, naming the editor and the commit.
func (w *paramWriteBack) record(by string, plan *paramPlan, res server.IOCommitted) {
	if w.rec == nil {
		return
	}
	names := make([]string, 0, len(plan.Written))
	for n := range plan.Written {
		names = append(names, n)
	}
	sort.Strings(names)
	target := strings.Join(names, ",")
	if len(target) > 200 {
		target = fmt.Sprintf("%d parameters", len(names))
	}
	_, err := w.rec.Record(audit.Entry{
		User: "system", Source: audit.SourceSystem, Action: "param.commit", Target: target,
		Result: audit.ResultOK, After: res.Commit,
		Detail: map[string]any{"by": by, "commit": res.Commit, "branch": res.Branch, "files": strings.Join(res.Files, ","), "parameters": len(names)},
	})
	if err != nil {
		slog.Warn("parameters: the audit journal did not take the commit's entry", "err", err)
	}
}
