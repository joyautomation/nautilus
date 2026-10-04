package deploy

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/joyautomation/nautilus/lang/l5x"
	"github.com/joyautomation/nautilus/logix/logixd"
	"github.com/joyautomation/nautilus/logix/writer"
)

// The warm path. Run (deploy.go) opens the SDK project three times per
// edit — the build, the upload before, the upload after — and each open
// costs 15–20 s, which is where a 2½-minute online edit goes. An editor's
// Download button cannot wait for that, and does not have to: a Session
// keeps ONE project, uploaded from the controller and therefore correlated
// with it, open and online across edits. An edit is then a rung import
// with FinalizeEdits (about a second) and a partial export of the routine
// to prove the controller runs what was sent.
//
// What the warm path gives up is the full-project SDK build before every
// edit. The controller still verifies every rung it accepts — an import
// that does not compile fails at FinalizeEdits and nothing changes — and
// the build runs in CI on every change (§5.4). The tag set cannot change
// online at all, so a program whose tags differ from the controller's is
// refused with NeedsDownloadError, exactly as the cold path refuses it.

// Session is one controller held open and online for repeated edits.
type Session struct {
	c      *logixd.Client
	t      Target
	log    func(string, ...any)
	mu     sync.Mutex
	s      *logixd.Session
	tags   map[string]string // the controller's tag shapes, from LogicOf
	rungs  []l5x.Rung        // what the routine runs now
	prog   string
	rout   string
	opened time.Time
}

// Connect uploads the running project, opens it and goes online. It
// returns an error for a controller with no project (download first).
func Connect(ctx context.Context, o Options) (*Session, error) {
	if o.Client == nil {
		return nil, errors.New("deploy: no logixd client")
	}
	if o.Target.CommPath == "" {
		return nil, errors.New("deploy: a session needs a comm path")
	}
	logf := o.Log
	if logf == nil {
		logf = func(string, ...any) {}
	}
	s := &Session{c: o.Client, t: o.Target, log: logf}
	if err := s.open(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Session) open(ctx context.Context) error {
	t0 := time.Now()
	run := "session-" + time.Now().UTC().Format("20060102-150405.000")
	acd, lx := path.Join(run, "controller.ACD"), path.Join(run, "controller.L5X")
	if evs, err := s.c.UploadToNew(ctx, s.t.CommPath, acd); err != nil {
		if controllerEmpty(err) {
			return &NeedsDownloadError{RoutineMissing: true}
		}
		return withEvents("upload", err, evs)
	}
	if _, evs, err := s.c.Convert(ctx, acd, lx, false); err != nil {
		return withEvents("convert", err, evs)
	}
	raw, err := s.c.GetFile(ctx, lx)
	if err != nil {
		return err
	}
	f, err := l5x.Parse(raw)
	if err != nil {
		return fmt.Errorf("controller export: %w", err)
	}
	logic := l5x.LogicOf(f)
	sess, err := s.c.Open(ctx, acd)
	if err != nil {
		return err
	}
	if _, err := sess.SetCommPath(ctx, s.t.CommPath); err != nil {
		_ = sess.Close(context.Background())
		return err
	}
	if _, err := sess.GoOnline(ctx); err != nil {
		_ = sess.Close(context.Background())
		return fmt.Errorf("go online: %w", err)
	}
	s.s, s.tags, s.opened = sess, logic.Tags, time.Now()
	s.prog, s.rout = "", ""
	s.rungs = nil
	// The routine the target names, if the controller has it.
	prog := s.t.Program
	rout := s.t.Routine
	if rout == "" {
		rout = "MainRoutine"
	}
	for k, r := range logic.Routines {
		p, name, _ := strings.Cut(k, "/")
		if (prog == "" || strings.EqualFold(p, prog)) && strings.EqualFold(name, rout) {
			s.prog, s.rout, s.rungs = p, name, r.Rungs
		}
	}
	s.log("session open and online to %s in %s (%d rungs in %s/%s)", s.t.CommPath, time.Since(t0).Round(time.Millisecond), len(s.rungs), s.prog, s.rout)
	return nil
}

// Close goes offline and releases the project.
func (s *Session) Close(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.s == nil {
		return nil
	}
	_, _ = s.s.GoOffline(ctx)
	err := s.s.Close(ctx)
	s.s = nil
	return err
}

// Edit replaces the routine's rungs with the program's as an online edit
// and verifies. A session the agent has dropped (idle timeout, a restart)
// is reopened once.
func (s *Session) Edit(ctx context.Context, src string) (*Report, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	started := time.Now()
	rep := &Report{}
	done := func(err error) (*Report, error) {
		rep.Elapsed = time.Since(started)
		return rep, err
	}
	major, minor, _ := strings.Cut(s.t.Revision, ".")
	wopts := writer.Options{
		Controller: s.t.Controller, Program: s.t.Program, Routine: s.t.Routine, Task: s.t.Task,
		PeriodMs: s.t.PeriodMs, ProcessorType: s.t.Processor, MajorRev: major, MinorRev: minor, Libs: s.t.Libs,
		Inits: s.t.Inits, Descs: s.t.Descs, Side: s.t.Side,
	}
	full, diags, err := writer.Write(src, wopts)
	if err != nil {
		return done(err)
	}
	if len(diags) > 0 {
		return done(&DiagError{Diags: diags})
	}
	rungs, _, _ := writer.WriteRungs(src, wopts)
	gen, err := l5x.Parse(full)
	if err != nil {
		return done(err)
	}
	rep.Controller = gen.Controller.Name
	rep.Program = gen.Controller.Programs[0].Name
	rep.Routine = gen.Controller.Programs[0].Routines[0].Name
	want := l5x.LogicOf(gen)
	rep.Rungs = len(want.Routines[rep.Program+"/"+rep.Routine].Rungs)

	if s.s == nil {
		if err := s.open(ctx); err != nil {
			return done(err)
		}
	}
	if s.prog == "" || !strings.EqualFold(s.prog, rep.Program) || !strings.EqualFold(s.rout, rep.Routine) {
		rep.RoutineMissing = true
		return done(&NeedsDownloadError{RoutineMissing: true})
	}
	// Tags cannot change online.
	for _, d := range l5x.LogicDiff(&l5x.Logic{Tags: s.tags, Routines: map[string]*l5x.RoutineLogic{}}, &l5x.Logic{Tags: want.Tags, Routines: map[string]*l5x.RoutineLogic{}}) {
		rep.Diffs = append(rep.Diffs, d)
		rep.TagsChanged = true
	}
	if rep.TagsChanged {
		return done(&NeedsDownloadError{Diffs: rep.Diffs})
	}
	have := &l5x.Logic{Tags: s.tags, Routines: map[string]*l5x.RoutineLogic{rep.Program + "/" + rep.Routine: {Type: "RLL", Rungs: s.rungs}}}
	rep.Diffs = l5x.LogicDiff(have, want)
	if len(rep.Diffs) == 0 {
		rep.Same = true
		s.log("the controller already runs this logic")
		return done(nil)
	}

	run := "edit-" + time.Now().UTC().Format("20060102-150405.000")
	rungsRel := path.Join(run, rep.Controller+".rungs.L5X")
	if err := s.c.PutFile(ctx, rungsRel, rungs); err != nil {
		return done(err)
	}
	xpath := logixd.RoutinePath(s.prog, s.rout)
	mode, _ := s.s.Mode(ctx)
	rep.ModeBefore = string(mode)
	ir, evs, err := s.s.ImportRungs(ctx, xpath, 0, uint32(len(s.rungs)), rungsRel, logixd.FinalizeEdits)
	if err != nil {
		if logixd.IsFatal(err) {
			// The session is gone; the next edit reopens it.
			_ = s.s.Close(context.Background())
			s.s = nil
		}
		return done(withEvents("online rung import", err, evs))
	}
	after, _ := s.s.Mode(ctx)
	rep.Applied, rep.Replaced, rep.ModeAfter = Online, ir.ReplaceCount, string(after)

	// Verify from the controller: export the routine and compare rungs.
	out := path.Join(run, "routine.L5X")
	if _, err := s.s.PartialExport(ctx, xpath, out); err != nil {
		return done(fmt.Errorf("verify export: %w", err))
	}
	raw, err := s.c.GetFile(ctx, out)
	if err != nil {
		return done(fmt.Errorf("verify: %w", err))
	}
	exp, err := l5x.Parse(raw)
	if err != nil {
		return done(fmt.Errorf("verify: %w", err))
	}
	got := l5x.LogicOf(exp)
	now := got.Routines[s.prog+"/"+s.rout]
	if now == nil {
		return done(&VerifyError{Diffs: []string{"routine " + s.prog + "/" + s.rout + ": not in the controller's export"}})
	}
	wantR := want.Routines[rep.Program+"/"+rep.Routine]
	if d := l5x.LogicDiff(
		&l5x.Logic{Tags: map[string]string{}, Routines: map[string]*l5x.RoutineLogic{"r": {Type: "RLL", Rungs: wantR.Rungs}}},
		&l5x.Logic{Tags: map[string]string{}, Routines: map[string]*l5x.RoutineLogic{"r": {Type: "RLL", Rungs: now.Rungs}}},
	); len(d) > 0 {
		return done(&VerifyError{Diffs: d})
	}
	s.rungs = now.Rungs
	rep.Verified = true
	s.log("online edit live and verified: %d rung(s) replaced in %s/%s, controller %s, %s", ir.ReplaceCount, s.prog, s.rout, after, time.Since(started).Round(time.Millisecond))
	return done(nil)
}
