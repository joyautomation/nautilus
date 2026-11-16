// Package deploy puts a nautilus ladder program on a Logix controller: the
// deploy flow of docs/design/logix-authoring.md Phase B, shared by
// `naut logix deploy` and the facade's PUT /api/program (the editor's
// Download button).
//
// The flow, every time:
//
//  1. Write the program as an L5X project (logix/writer), import and build
//     it with the Studio 5000 SDK through logixd. No controller involved,
//     no risk; this is the half that is CI (logix-target.md §15.2).
//  2. Upload what the controller runs and compare LOGIC (lang/l5x.LogicOf):
//     rung differences can go as an online edit; a tag added, removed or
//     retyped needs a download, because an online edit cannot create tags.
//  3. Do the one asked for. An online edit replaces the routine's rungs in
//     the running program with FinalizeEdits and leaves the controller's
//     mode alone. A download stops the controller and replaces its
//     project; the caller confirms it, never this package. Unless asked to
//     reset them, a download keeps the controller's tag values (values.go):
//     the upload of step 2 comes first, and its values go into the project
//     before the build.
//  4. Upload again and compare with what was sent. A deploy that cannot
//     prove it landed is reported as failed.
package deploy

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/joyautomation/nautilus/lang/l5x"
	"github.com/joyautomation/nautilus/logix/logixd"
	"github.com/joyautomation/nautilus/logix/writer"
)

// Mode is what the deploy does after building.
type Mode int

const (
	// BuildOnly imports and builds, then reports what the controller would
	// need (when a comm path is set) and stops.
	BuildOnly Mode = iota
	// Online replaces the routine's rungs in the running program.
	Online
	// Download sends the whole project. Stops the controller.
	Download
)

func (m Mode) String() string {
	switch m {
	case Online:
		return "online edit"
	case Download:
		return "download"
	}
	return "build"
}

// Target is the controller and project envelope, usually from the
// manifest's target: logix section.
type Target struct {
	Controller, Processor, Revision string
	Program, Routine, Task          string
	PeriodMs                        int
	CommPath                        string
	// Libs are library sources in scope for the parse.
	Libs []string
	// Inits and Descs are the manifest's tag seeds and descriptions, for
	// the controller tags the program declares VAR_EXTERNAL.
	Inits   map[string]any
	Descs   map[string]string
	Aliases map[string]string
	// Tags are the manifest's tags and their IEC types (writer.Options.Tags):
	// a rung naming one without VAR_EXTERNAL still gets its controller tag.
	Tags map[string]string
	// AllTags writes every manifest tag, named by the program or not
	// (writer.Options.AllTags); ControllerScoped the program variables
	// written at controller scope (writer.Options.ControllerScoped).
	AllTags          bool
	ControllerScoped []string
	// Channels and Messages are the manifest's channels: and
	// target.logix.messages, for the MSG a READ or WRITE is written as
	// (writer.Options.Channels, .Messages).
	Channels []writer.Channel
	Messages []writer.Message
	// Side is the side code to emit (writer.Side).
	Side writer.Side
	// Hardware is the project's I/O configuration (writer.Options.Hardware);
	// nil writes the controller's Local module only.
	Hardware *writer.Hardware
	// Language is the program's language, "ld" (default) or "st".
	Language string
}

// write lowers src in the target's language.
func (t Target) write(src string, w writer.Options) ([]byte, []writer.Diag, error) {
	if t.Language == "st" {
		return writer.WriteST(src, w)
	}
	return writer.Write(src, w)
}

// Options configure one deploy.
type Options struct {
	Client *logixd.Client
	Target Target
	Mode   Mode
	// ProgramMode asks the SDK to put the controller in Program mode
	// before a download; the SDK will not do it on its own.
	ProgramMode bool
	// ResetValues makes a download start every tag at the project's
	// value. By default a download keeps the controller's values of the
	// tags that survive it unchanged (PlanValues has the rule).
	ResetValues bool
	// Keep, when set, receives every artifact: the generated L5X files,
	// the built ACD, the controller's before/after exports.
	Keep func(name string, raw []byte)
	// Log, when set, receives progress lines as they happen.
	Log func(format string, a ...any)
}

// Report is what happened.
type Report struct {
	Controller, Program, Routine string
	Rungs                        int
	// Build is the SDK's own build time; ImportBuild the whole import +
	// build leg as seen from here.
	Build, ImportBuild time.Duration
	// Diffs are the logic differences between the controller (first) and
	// the program (second) before anything was sent. Same means none.
	Diffs          []string
	Same           bool
	TagsChanged    bool
	RoutineMissing bool
	// Applied is what was done to the controller, BuildOnly for nothing.
	Applied               Mode
	ModeBefore, ModeAfter string
	Replaced              uint32
	Verified              bool
	Elapsed               time.Duration
	// Values is what a download did with the controller's tag values (for
	// a build, what one would do); nil when they were not looked at — an
	// online edit, a reset, or a controller with nothing to upload.
	Values *ValuePlan
}

// DiagError carries the writer's rule diagnostics: the program is outside
// the v1 subset and nothing was sent.
type DiagError struct{ Diags []writer.Diag }

func (e *DiagError) Error() string {
	var parts []string
	for _, d := range e.Diags {
		parts = append(parts, d.String())
	}
	return fmt.Sprintf("%d construct(s) outside the Logix v1 subset: %s", len(e.Diags), strings.Join(parts, "; "))
}

// NeedsDownloadError says an online edit was asked for but the change
// needs a download.
type NeedsDownloadError struct {
	Diffs          []string
	RoutineMissing bool
}

func (e *NeedsDownloadError) Error() string {
	if e.RoutineMissing {
		return "the controller does not run this program's routine yet: a download is needed"
	}
	return fmt.Sprintf("the tag set changed (%d difference(s)); an online edit cannot create or retype tags, so a download is needed", len(e.Diffs))
}

// VerifyError says the controller does not run what was sent.
type VerifyError struct{ Diffs []string }

func (e *VerifyError) Error() string {
	return fmt.Sprintf("verification failed: the controller does not run what was sent (%d difference(s))", len(e.Diffs))
}

// StartStateError says the controller refused a download because of the
// mode it is in: the SDK's RxE_INVALID_START_STATE ("Invalid start
// state. Internal error."), which is what a download to a controller in
// Run answers when it was not asked to switch to Program mode first. Raw
// is the SDK's own error with its status lines, for the record.
type StartStateError struct {
	// Mode is the controller's mode before the download, when it was read.
	Mode string
	Raw  error
}

func (e *StartStateError) Error() string {
	mode := "is not in Program mode"
	if e.Mode != "" {
		mode = "is in " + e.Mode + " mode"
	}
	return "the controller " + mode + ", and a download needs it in Program mode: download again with " +
		"\"Switch to Program Mode and Download\" (naut logix deploy --download --program-mode), " +
		"or put the controller in Program mode first"
}

func (e *StartStateError) Unwrap() error { return e.Raw }

// controllerMode reads the controller's mode through a session on its
// uploaded project, "" when it cannot be read: a failed read must not
// block a download.
func controllerMode(ctx context.Context, c *logixd.Client, acdRel, commPath string) string {
	s, err := c.Open(ctx, acdRel)
	if err != nil {
		return ""
	}
	defer s.Close(context.Background())
	if _, err := s.SetCommPath(ctx, commPath); err != nil {
		return ""
	}
	mode, err := s.Mode(ctx)
	if err != nil {
		return ""
	}
	return string(mode)
}

// inRun says a controller mode is Run or Remote Run.
func inRun(mode string) bool {
	m := strings.ToLower(strings.TrimSpace(mode))
	return m == "run" || m == "remote run"
}

// invalidStartState recognizes the SDK's refusal to download to a
// controller in the wrong mode.
func invalidStartState(err error) bool {
	return err != nil && strings.Contains(err.Error(), "RxE_INVALID_START_STATE")
}

// Run deploys one ladder program. The Report is returned with whatever
// was measured even when err is set.
func Run(ctx context.Context, src string, o Options) (*Report, error) {
	started := time.Now()
	rep := &Report{}
	done := func(err error) (*Report, error) {
		rep.Elapsed = time.Since(started)
		return rep, err
	}
	logf := o.Log
	if logf == nil {
		logf = func(string, ...any) {}
	}
	keep := o.Keep
	if keep == nil {
		keep = func(string, []byte) {}
	}
	if o.Client == nil {
		return done(errors.New("deploy: no logixd client"))
	}
	t := o.Target
	major, minor, _ := strings.Cut(t.Revision, ".")
	wopts := writer.Options{
		Controller: t.Controller, Program: t.Program, Routine: t.Routine, Task: t.Task,
		PeriodMs: t.PeriodMs, ProcessorType: t.Processor, MajorRev: major, MinorRev: minor, Libs: t.Libs,
		Inits: t.Inits, Descs: t.Descs, Aliases: t.Aliases, Tags: t.Tags, Side: t.Side,
		AllTags: t.AllTags, ControllerScoped: t.ControllerScoped, Hardware: t.Hardware,
		Channels: t.Channels, Messages: t.Messages,
	}
	full, diags, err := t.write(src, wopts)
	if err != nil {
		return done(err)
	}
	if len(diags) > 0 {
		return done(&DiagError{Diags: diags})
	}
	var rungs []byte
	if t.Language != "st" {
		rungs, _, _ = writer.WriteRungs(src, wopts)
	}
	gen, err := l5x.Parse(full)
	if err != nil {
		return done(fmt.Errorf("generated L5X does not parse: %w", err))
	}
	rep.Controller = gen.Controller.Name
	rep.Program = gen.Controller.Programs[0].Name
	rep.Routine = gen.Controller.Programs[0].Routines[0].Name
	routine := gen.Controller.Programs[0].Routines[0]
	rep.Rungs = len(routine.Rungs)
	if t.Language == "st" {
		rungs, _, _ = writer.WriteRoutine("program.st", src, wopts)
		keep(rep.Controller+".routine.L5X", rungs)
		logf("wrote %s: program %s, ST routine %s, %d line(s)", rep.Controller, rep.Program, rep.Routine, strings.Count(routine.Text, "\n"))
	} else {
		keep(rep.Controller+".rungs.L5X", rungs)
		logf("wrote %s: program %s, routine %s, %d rung(s)", rep.Controller, rep.Program, rep.Routine, rep.Rungs)
	}

	c := o.Client
	runID := "deploy-" + time.Now().UTC().Format("20060102-150405.000")
	rel := func(name string) string { return path.Join(runID, name) }
	partialName := rep.Controller + ".rungs.L5X"
	if t.Language == "st" {
		partialName = rep.Controller + ".routine.L5X"
	}
	fullRel, rungsRel, acdRel := rel(rep.Controller+".L5X"), rel(partialName), rel(rep.Controller+".ACD")

	upload := func(name string) (*l5x.File, []byte, error) {
		acd, lx := rel(name+".ACD"), rel(name+".L5X")
		if evs, err := c.UploadToNew(ctx, t.CommPath, acd); err != nil {
			return nil, nil, withEvents("upload "+name, err, evs)
		}
		if _, evs, err := c.Convert(ctx, acd, lx, false); err != nil {
			return nil, nil, withEvents("convert "+name, err, evs)
		}
		raw, err := c.GetFile(ctx, lx)
		if err != nil {
			return nil, nil, err
		}
		keep(name+".L5X", raw)
		f, err := l5x.Parse(raw)
		return f, raw, err
	}
	var before *l5x.File
	var beforeRaw []byte
	uploadBefore := func() error {
		var err error
		before, beforeRaw, err = upload("before")
		if err != nil {
			if !controllerEmpty(err) {
				return err
			}
			// A controller with no project in it cannot be uploaded from
			// (RxE_NOT_FOUND). That is not a failure: it is the first
			// download, and the comparison below is against nothing.
			logf("the controller at %s has no project in it", t.CommPath)
			before, beforeRaw = &l5x.File{Controller: &l5x.Controller{}}, nil
		}
		return nil
	}

	// 0. A download keeps the controller's values: upload what runs
	// first and write its values into the project before the build, so
	// the download carries them. (The values are the upload's: a value
	// the program changes between the upload and the download, about
	// the import + build's time, is not carried.)
	var plan *ValuePlan
	if o.Mode == Download && t.CommPath != "" && (!o.ResetValues || !o.ProgramMode) {
		if err := uploadBefore(); err != nil {
			return done(err)
		}
		// A plain download to a controller in Run is refused by the SDK
		// only after the whole import and build (~76 s on a real
		// controller): read the mode now and refuse in seconds (#391).
		if !o.ProgramMode && beforeRaw != nil {
			if mode := controllerMode(ctx, c, rel("before.ACD"), t.CommPath); inRun(mode) {
				rep.ModeBefore = mode
				logf("the controller at %s is in %s mode; a download needs Program mode", t.CommPath, mode)
				return done(&StartStateError{Mode: mode})
			}
		}
		if beforeRaw != nil && !o.ResetValues {
			if plan, err = PlanValues(gen, beforeRaw); err != nil {
				return done(err)
			}
			if full, err = plan.Apply(full); err != nil {
				return done(fmt.Errorf("keeping tag values: %w", err))
			}
			rep.Values = plan
			logf("%s", plan.Summary())
		}
	}
	keep(rep.Controller+".L5X", full)

	// 1. Import + build.
	if err := c.PutFile(ctx, fullRel, full); err != nil {
		return done(err)
	}
	if err := c.PutFile(ctx, rungsRel, rungs); err != nil {
		return done(err)
	}
	t0 := time.Now()
	var built *logixd.Session
	if writer.UsesAOIs("program."+langOf(t), src, wopts) {
		// The SDK takes an Add-On Instruction instance into an existing
		// project, not as part of a whole-project import. So: the
		// project with the program emptied, then the program over it.
		skeleton, _, _ := writer.WriteSkeleton("program."+langOf(t), src, wopts)
		program, _, _ := writer.WriteProgramPartial("program."+langOf(t), src, wopts)
		if plan != nil {
			if skeleton, err = plan.Apply(skeleton); err != nil {
				return done(fmt.Errorf("keeping tag values: %w", err))
			}
			if program, err = plan.Apply(program); err != nil {
				return done(fmt.Errorf("keeping tag values: %w", err))
			}
		}
		skelRel, progRel := rel(rep.Controller+".skeleton.L5X"), rel(rep.Controller+".program.L5X")
		keep(rep.Controller+".skeleton.L5X", skeleton)
		keep(rep.Controller+".program.L5X", program)
		if err := c.PutFile(ctx, skelRel, skeleton); err != nil {
			return done(err)
		}
		if err := c.PutFile(ctx, progRel, program); err != nil {
			return done(err)
		}
		if _, evs, err := c.Convert(ctx, skelRel, acdRel, false); err != nil {
			return done(withEvents("SDK import (skeleton)", err, evs))
		}
		built, err = c.Open(ctx, acdRel)
		if err != nil {
			return done(err)
		}
		if evs, err := built.PartialImport(ctx, "Controller/Programs", progRel, logixd.Overwrite, true); err != nil {
			_ = built.Close(context.Background())
			return done(withEvents("SDK import (program)", err, evs))
		}
		if _, err := built.Save(ctx, "", false); err != nil {
			_ = built.Close(context.Background())
			return done(err)
		}
	} else {
		if _, evs, err := c.Convert(ctx, fullRel, acdRel, false); err != nil {
			return done(withEvents("SDK import", err, evs))
		}
		built, err = c.Open(ctx, acdRel)
		if err != nil {
			return done(err)
		}
	}
	res, evs, err := built.Build(ctx, logixd.BuildDefault)
	if err != nil {
		_ = built.Close(context.Background())
		return done(withEvents("build", err, evs))
	}
	rep.Build = time.Duration(res.ElapsedMs) * time.Millisecond
	rep.ImportBuild = time.Since(t0)
	logf("imported and built in %s (build %s)", rep.ImportBuild.Round(time.Millisecond), rep.Build)
	if o.Keep != nil {
		if _, err := built.Save(ctx, "", false); err == nil {
			if raw, err := c.GetFile(ctx, acdRel); err == nil {
				keep(rep.Controller+".ACD", raw)
			}
		}
	}
	_ = built.Close(context.Background())
	if t.CommPath == "" {
		return done(nil)
	}

	// 2. What is running.
	if before == nil {
		if err := uploadBefore(); err != nil {
			return done(err)
		}
		if o.Mode == BuildOnly && beforeRaw != nil {
			// What a download would do with the values, for the report.
			if p, err := PlanValues(gen, beforeRaw); err == nil {
				rep.Values = p
				logf("on a download, %s", p.Summary())
			}
		}
	}
	want := l5x.LogicOf(gen)
	have := l5x.LogicOf(before)
	rep.Diffs = l5x.LogicDiff(have, want)
	rep.Same = len(rep.Diffs) == 0
	for _, d := range rep.Diffs {
		// A tag difference, or any difference in an Add-On Instruction,
		// is structural: an online rung edit cannot carry it.
		if strings.HasPrefix(d, "tag ") || strings.Contains(d, "AOI:") {
			rep.TagsChanged = true
		}
	}
	running := have.Routines[rep.Program+"/"+rep.Routine]
	rep.RoutineMissing = running == nil
	switch {
	case rep.Same:
		logf("the controller at %s already runs this logic", t.CommPath)
	case rep.RoutineMissing:
		logf("the controller has no %s/%s: a download is needed", rep.Program, rep.Routine)
	case rep.TagsChanged:
		logf("%d difference(s); the tag set changed, so this needs a download", len(rep.Diffs))
	default:
		logf("%d logic difference(s); this can go as an online edit", len(rep.Diffs))
	}
	if o.Mode == BuildOnly || (rep.Same && o.Mode == Online) {
		return done(nil)
	}
	if o.Mode == Online && (rep.TagsChanged || rep.RoutineMissing) {
		return done(&NeedsDownloadError{Diffs: rep.Diffs, RoutineMissing: rep.RoutineMissing})
	}

	// 3. Put it on the controller.
	if o.Mode == Online {
		s, err := c.Open(ctx, rel("before.ACD"))
		if err != nil {
			return done(err)
		}
		defer s.Close(context.Background())
		if _, err := s.SetCommPath(ctx, t.CommPath); err != nil {
			return done(err)
		}
		if _, err := s.GoOnline(ctx); err != nil {
			return done(fmt.Errorf("go online: %w", err))
		}
		mode, _ := s.Mode(ctx)
		rep.ModeBefore = string(mode)
		xpath := logixd.RoutinePath(rep.Program, rep.Routine)
		var replaced uint32
		if t.Language == "st" {
			// A routine imports INTO its program (the container), never
			// onto its own path.
			evs, err := s.ImportWithTarget(ctx, logixd.ProgramPath(rep.Program), rep.Routine, rungsRel, logixd.FinalizeEdits)
			if err != nil {
				_, _ = s.GoOffline(context.Background())
				return done(withEvents("online routine import", err, evs))
			}
		} else {
			ir, evs, err := s.ImportRungs(ctx, xpath, 0, uint32(len(running.Rungs)), rungsRel, logixd.FinalizeEdits)
			if err != nil {
				_, _ = s.GoOffline(context.Background())
				return done(withEvents("online rung import", err, evs))
			}
			replaced = ir.ReplaceCount
		}
		after, _ := s.Mode(ctx)
		_, _ = s.GoOffline(context.Background())
		rep.Applied, rep.Replaced, rep.ModeAfter = Online, replaced, string(after)
		logf("online edit live: %s/%s replaced online (FinalizeEdits), controller %s → %s", rep.Program, rep.Routine, mode, after)
	} else {
		s, err := c.Open(ctx, acdRel)
		if err != nil {
			return done(err)
		}
		defer s.Close(context.Background())
		if _, err := s.SetCommPath(ctx, t.CommPath); err != nil {
			return done(err)
		}
		if mode, err := s.Mode(ctx); err == nil {
			rep.ModeBefore = string(mode)
		}
		evs, err := s.Download(ctx, o.ProgramMode)
		if err != nil {
			err = withEvents("download", err, evs)
			// Asked for Program mode and still refused (a keyswitch in
			// RUN, a fault): the hint would be wrong, so the SDK's
			// answer goes as it is.
			if !o.ProgramMode && invalidStartState(err) {
				err = &StartStateError{Mode: rep.ModeBefore, Raw: err}
			}
			return done(err)
		}
		mode, _ := s.Mode(ctx)
		_, _ = s.GoOffline(context.Background())
		rep.Applied, rep.ModeAfter = Download, string(mode)
		logf("downloaded %s to %s — controller is %s (the SDK does not put it back in Run)", rep.Controller, t.CommPath, mode)
	}

	// 4. Verify.
	after, _, err := upload("after")
	if err != nil {
		return done(fmt.Errorf("verify: %w", err))
	}
	if d := l5x.LogicDiff(want, l5x.LogicOf(after)); len(d) > 0 {
		return done(&VerifyError{Diffs: d})
	}
	rep.Verified = true
	logf("verified: the controller at %s runs what was sent", t.CommPath)
	return done(nil)
}

// controllerEmpty recognizes the SDK's answer to uploading from a
// controller that has never been downloaded to.
func controllerEmpty(err error) bool {
	return err != nil && strings.Contains(err.Error(), "RxE_NOT_FOUND")
}

// withEvents keeps the SDK's own event stream on an error: a failed
// import explains itself in the events, not in the exception.
func withEvents(step string, err error, evs []logixd.Event) error {
	var lines []string
	for _, e := range evs {
		if e.Kind != "progress" {
			lines = append(lines, e.String())
		}
	}
	if len(lines) == 0 {
		return fmt.Errorf("%s: %w", step, err)
	}
	return fmt.Errorf("%s: %w\n  %s", step, err, strings.Join(lines, "\n  "))
}

// langOf is the extension the writer dispatches on for a target.
func langOf(t Target) string {
	if t.Language == "st" {
		return "st"
	}
	return "ld"
}
