package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/joyautomation/nautilus/internal/project"
	"github.com/joyautomation/nautilus/internal/stproject"
	"github.com/joyautomation/nautilus/lang/l5x"
	"github.com/joyautomation/nautilus/logix/logixd"
	"github.com/joyautomation/nautilus/logix/writer"
)

const logixDeployUsage = `naut logix deploy — put a nautilus ladder program on a Logix controller

Usage:
  naut logix deploy [flags] [project-dir]

Reads the project's nautilus.yaml (target: logix), writes its task program
as a Logix L5X project, imports and builds it with the Studio 5000 SDK
through logixd, and then — on request — puts it on the controller:

  (no flag)    Build only, and say whether the running controller could
               take the change as an online edit or needs a download.
  --online     ONLINE EDIT: replace the routine's rungs in the running
               program and finalize them, keeping the controller in its
               mode. Refused when the tag set changed, because an online
               edit cannot create tags.
  --download   DOWNLOAD: stops the controller and resets its tags to
               project values. Needs --yes. The controller must already be
               in Program mode, or pass --program-mode.

Every path ends with a verification: the controller's program is uploaded
again and its logic compared with what was sent (naut logix drift --logic).

Flags:
  --online, --download, --yes, --program-mode   as above
  --comm-path   FactoryTalk Linx path to the controller (default: the
                manifest's target.logix.comm-path)
  --agent       logixd URL (default: $NAUTILUS_LOGIXD_URL, then the
                manifest's target.logix.agent)
  --token       logixd bearer token (default: $NAUTILUS_LOGIXD_TOKEN)
  --keep DIR    keep the generated L5X, the built ACD and the controller's
                before/after uploads here
`

func runLogixDeploy(args []string) int {
	fs := flag.NewFlagSet("logix deploy", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, logixDeployUsage) }
	agent, token := agentFlags(fs)
	online := fs.Bool("online", false, "online edit of the running program")
	download := fs.Bool("download", false, "download: stops the controller")
	yes := fs.Bool("yes", false, "confirm a download")
	programMode := fs.Bool("program-mode", false, "put the controller in Program mode before a download")
	commPath := fs.String("comm-path", "", "FactoryTalk Linx path to the controller")
	keep := fs.String("keep", "", "directory to keep artifacts in")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 1 {
		fs.Usage()
		return 2
	}
	if *online && *download {
		fmt.Fprintln(os.Stderr, "naut logix deploy: --online and --download are alternatives")
		return 2
	}
	dir := "."
	if fs.NArg() == 1 {
		dir = fs.Arg(0)
	}
	m, err := project.ReadManifest(os.DirFS(dir), "")
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut logix deploy:", err)
		return 2
	}
	if m.Target == nil || m.Target.Logix == nil {
		fmt.Fprintf(os.Stderr, "naut logix deploy: %s declares no target: logix section\n", filepath.Join(dir, project.ManifestName))
		return 2
	}
	tgt := m.Target.Logix
	if *commPath == "" {
		*commPath = tgt.CommPath
	}
	if *agent == "" && os.Getenv("NAUTILUS_LOGIXD_URL") == "" {
		*agent = tgt.Agent
	}
	if (*online || *download) && *commPath == "" {
		fmt.Fprintln(os.Stderr, "naut logix deploy: no comm path — set target.logix.comm-path or pass --comm-path")
		return 2
	}
	if *download && !*yes {
		fmt.Fprintf(os.Stderr,
			"naut logix deploy: refusing a download without --yes.\n\n"+
				"  A download STOPS the controller at %s and RESETS its tags to\n"+
				"  project values. Re-run with --yes when you mean it; use --online\n"+
				"  for a rung change that keeps the controller running.\n", *commPath)
		return 2
	}

	// One task, one ladder program: the v1 shape (logix-authoring.md §4).
	if len(m.Tasks) != 1 {
		fmt.Fprintf(os.Stderr, "naut logix deploy: the Logix v1 target takes exactly one task; %s declares %d\n", project.ManifestName, len(m.Tasks))
		return 2
	}
	task := m.Tasks[0]
	progPath := filepath.Join(dir, filepath.FromSlash(task.Program))
	src, err := os.ReadFile(progPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut logix deploy:", err)
		return 2
	}
	if !strings.EqualFold(filepath.Ext(progPath), ".ld") {
		fmt.Fprintf(os.Stderr, "naut logix deploy: %s: only ladder (.ld) programs are in the Logix v1 subset\n", progPath)
		return 1
	}
	major, minor, _ := strings.Cut(tgt.Revision, ".")
	_, libs, _ := stproject.PreludeSources(progPath, nil)
	wopts := writer.Options{
		Controller: tgt.Controller, Program: tgt.Program, Routine: tgt.Routine, Task: tgt.Task,
		PeriodMs:      int(time.Duration(task.Scan) / time.Millisecond),
		ProcessorType: tgt.Processor, MajorRev: major, MinorRev: minor, Libs: libs,
	}
	started := time.Now()
	full, diags, err := writer.Write(string(src), wopts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", progPath, err)
		return 1
	}
	if len(diags) > 0 {
		for _, d := range diags {
			fmt.Printf("%s:%d:1: logix target: %s [%s]\n", progPath, d.Line, d.Message, d.Rule)
		}
		fmt.Fprintf(os.Stderr, "naut logix deploy: %d construct(s) outside the Logix v1 subset; nothing sent\n", len(diags))
		return 1
	}
	rungs, _, _ := writer.WriteRungs(string(src), wopts)
	genFile, err := l5x.Parse(full)
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut logix deploy: generated L5X does not parse:", err)
		return 1
	}
	controller := genFile.Controller.Name
	program := genFile.Controller.Programs[0].Name
	routine := genFile.Controller.Programs[0].Routines[0].Name
	keepFile := func(name string, raw []byte) {
		if *keep == "" {
			return
		}
		_ = os.MkdirAll(*keep, 0o755)
		if err := os.WriteFile(filepath.Join(*keep, name), raw, 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "warning: --keep: %v\n", err)
		}
	}
	keepFile(controller+".L5X", full)
	keepFile(controller+".rungs.L5X", rungs)
	fmt.Printf("wrote %s: program %s, routine %s, %d rung(s)\n", controller, program, routine, len(genFile.Controller.Programs[0].Routines[0].Rungs))

	c := logixd.New(*agent, *token)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Minute)
	defer cancel()
	runID := newRunID()
	rel := func(name string) string { return path.Join(runID, name) }

	// 1. Import + build: the half of CI that carries no risk (§15.2).
	if err := c.PutFile(ctx, rel(controller+".L5X"), full); err != nil {
		return reportErr("deploy", err)
	}
	if err := c.PutFile(ctx, rel(controller+".rungs.L5X"), rungs); err != nil {
		return reportErr("deploy", err)
	}
	t0 := time.Now()
	_, evs, err := c.Convert(ctx, rel(controller+".L5X"), rel(controller+".ACD"), false)
	if err != nil {
		printEvents(evs)
		return reportErr("deploy (SDK import)", err)
	}
	built, err := c.Open(ctx, rel(controller+".ACD"))
	if err != nil {
		return reportErr("deploy", err)
	}
	res, evs, err := built.Build(ctx, logixd.BuildDefault)
	if err != nil {
		printEvents(evs)
		_ = built.Close(context.Background())
		return reportErr("deploy (build)", err)
	}
	fmt.Printf("imported and built in %s (build %s)\n", time.Since(t0).Round(time.Millisecond), time.Duration(res.ElapsedMs)*time.Millisecond)
	if *keep != "" {
		if _, err := built.Save(ctx, "", false); err == nil {
			if raw, err := c.GetFile(ctx, rel(controller+".ACD")); err == nil {
				keepFile(controller+".ACD", raw)
			}
		}
	}
	_ = built.Close(context.Background())
	if *commPath == "" {
		fmt.Println("no comm path: built only")
		return 0
	}

	// 2. What is running: upload before touching anything (§15.2 step 5).
	upload := func(name string) (*l5x.File, []byte, error) {
		acd, l5xRel := rel(name+".ACD"), rel(name+".L5X")
		if evs, err := c.UploadToNew(ctx, *commPath, acd); err != nil {
			printEvents(evs)
			return nil, nil, err
		}
		if _, evs, err := c.Convert(ctx, acd, l5xRel, false); err != nil {
			printEvents(evs)
			return nil, nil, err
		}
		raw, err := c.GetFile(ctx, l5xRel)
		if err != nil {
			return nil, nil, err
		}
		f, err := l5x.Parse(raw)
		return f, raw, err
	}
	before, beforeRaw, err := upload("before")
	if err != nil {
		return reportErr("deploy (upload before)", err)
	}
	keepFile("before.L5X", beforeRaw)
	want := l5x.LogicOf(genFile)
	have := l5x.LogicOf(before)
	diffs := l5x.LogicDiff(have, want)
	tagChange, rungChange := false, false
	for _, d := range diffs {
		if strings.HasPrefix(d, "tag ") {
			tagChange = true
		} else {
			rungChange = true
		}
	}
	running := have.Routines[program+"/"+routine]
	switch {
	case len(diffs) == 0:
		fmt.Printf("the controller at %s already runs this logic\n", *commPath)
		if !*download {
			return 0
		}
	case running == nil:
		fmt.Printf("the controller has no %s/%s: a download is needed\n", program, routine)
	case tagChange:
		fmt.Printf("%d difference(s); the tag set changed, so this needs a download:\n", len(diffs))
		for _, d := range diffs {
			fmt.Println("  " + d)
		}
	default:
		fmt.Printf("%d rung difference(s); this can go as an online edit:\n", len(diffs))
		for _, d := range diffs {
			fmt.Println("  " + d)
		}
	}
	if !*online && !*download {
		return 0
	}
	if *online && (tagChange || running == nil) {
		fmt.Fprintln(os.Stderr, "naut logix deploy: an online edit cannot create or retype tags; use --download --yes")
		return 1
	}
	_ = rungChange

	// 3. Put it on the controller.
	if *online {
		s, err := c.Open(ctx, rel("before.ACD"))
		if err != nil {
			return reportErr("deploy", err)
		}
		defer s.Close(context.Background())
		if _, err := s.SetCommPath(ctx, *commPath); err != nil {
			return reportErr("deploy", err)
		}
		if _, err := s.GoOnline(ctx); err != nil {
			return reportErr("deploy (go online)", err)
		}
		mode, _ := s.Mode(ctx)
		xpath := logixd.RoutinePath(program, routine)
		ir, evs, err := s.ImportRungs(ctx, xpath, 0, uint32(len(running.Rungs)), rel(controller+".rungs.L5X"), logixd.FinalizeEdits)
		printEvents(evs)
		if err != nil {
			_, _ = s.GoOffline(context.Background())
			return reportErr("deploy (online rung import)", err)
		}
		after, _ := s.Mode(ctx)
		_, _ = s.GoOffline(context.Background())
		fmt.Printf("online edit live: %d rung(s) replaced in %s/%s, %s, controller %s → %s, %s since the command started\n",
			ir.ReplaceCount, program, routine, ir.OnlineOption, mode, after, time.Since(started).Round(time.Millisecond))
	} else {
		s, err := c.Open(ctx, rel(controller+".ACD"))
		if err != nil {
			return reportErr("deploy", err)
		}
		defer s.Close(context.Background())
		if _, err := s.SetCommPath(ctx, *commPath); err != nil {
			return reportErr("deploy", err)
		}
		if mode, err := s.Mode(ctx); err == nil {
			fmt.Printf("controller at %s is %s\n", *commPath, mode)
		}
		evs, err := s.Download(ctx, *programMode)
		printEvents(evs)
		if err != nil {
			return reportErr("deploy (download)", err)
		}
		mode, _ := s.Mode(ctx)
		_, _ = s.GoOffline(context.Background())
		fmt.Printf("downloaded %s to %s — controller is %s (the SDK does not put it back in Run), %s since the command started\n",
			controller, *commPath, mode, time.Since(started).Round(time.Millisecond))
	}

	// 4. Verify: what runs now is what was sent.
	afterFile, afterRaw, err := upload("after")
	if err != nil {
		return reportErr("deploy (upload after)", err)
	}
	keepFile("after.L5X", afterRaw)
	if d := l5x.LogicDiff(want, l5x.LogicOf(afterFile)); len(d) > 0 {
		fmt.Printf("VERIFY FAILED — the controller does not run what was sent (%d difference(s)):\n", len(d))
		for _, x := range d {
			fmt.Println("  " + x)
		}
		return 1
	}
	fmt.Printf("verified: the controller at %s runs %s\n", *commPath, filepath.Base(progPath))
	return 0
}
