package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/joyautomation/nautilus/internal/project"
	"github.com/joyautomation/nautilus/internal/stproject"
	"github.com/joyautomation/nautilus/logix/deploy"
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

// logixProject is a manifest project with a Logix target, resolved to
// what a deploy needs: the program source and the target.
type logixProject struct {
	dir, program string
	source       string
	target       deploy.Target
	host         string
	slot, port   int
	agent        string
}

// loadLogixProject reads nautilus.yaml and the one task program the v1
// target takes.
func loadLogixProject(dir string) (*logixProject, error) {
	m, err := project.ReadManifest(os.DirFS(dir), "")
	if err != nil {
		return nil, err
	}
	if m.Target == nil || m.Target.Logix == nil {
		return nil, fmt.Errorf("%s declares no target: logix section", filepath.Join(dir, project.ManifestName))
	}
	tgt := m.Target.Logix
	if len(m.Tasks) != 1 {
		return nil, fmt.Errorf("the Logix v1 target takes exactly one task; %s declares %d", project.ManifestName, len(m.Tasks))
	}
	task := m.Tasks[0]
	progPath := filepath.Join(dir, filepath.FromSlash(task.Program))
	if writer.Language(progPath) == "" {
		return nil, fmt.Errorf("%s: only ladder (.ld) and structured text (.st) programs are in the Logix subset", progPath)
	}
	src, err := os.ReadFile(progPath)
	if err != nil {
		return nil, err
	}
	_, libs, _ := stproject.PreludeSources(progPath, nil)
	inits, descs, aliases := map[string]any{}, map[string]string{}, map[string]string{}
	for _, tg := range m.Tags {
		if tg.Init != nil {
			inits[tg.Name] = tg.Init
		}
		if tg.Desc != "" {
			descs[tg.Name] = tg.Desc
		}
		if tg.Alias != "" {
			aliases[tg.Name] = tg.Alias
		}
	}
	var side writer.Side
	if tgt.Side != nil {
		side.Heartbeat = tgt.Side.Heartbeat
	}
	return &logixProject{
		dir: dir, program: progPath, source: string(src),
		target: deploy.Target{
			Controller: tgt.Controller, Processor: tgt.Processor, Revision: tgt.Revision,
			Program: tgt.Program, Routine: tgt.Routine, Task: tgt.Task,
			PeriodMs: int(time.Duration(task.Scan) / time.Millisecond),
			CommPath: tgt.CommPath, Libs: libs, Inits: inits, Descs: descs, Aliases: aliases, Side: side,
			Language: writer.Language(progPath),
		},
		host: tgt.Host, slot: tgt.Slot, port: tgt.Port, agent: tgt.Agent,
	}, nil
}

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
	p, err := loadLogixProject(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut logix deploy:", err)
		return 2
	}
	if *commPath != "" {
		p.target.CommPath = *commPath
	}
	if *agent == "" && os.Getenv("NAUTILUS_LOGIXD_URL") == "" {
		*agent = p.agent
	}
	if (*online || *download) && p.target.CommPath == "" {
		fmt.Fprintln(os.Stderr, "naut logix deploy: no comm path — set target.logix.comm-path or pass --comm-path")
		return 2
	}
	if *download && !*yes {
		fmt.Fprintf(os.Stderr,
			"naut logix deploy: refusing a download without --yes.\n\n"+
				"  A download STOPS the controller at %s and RESETS its tags to\n"+
				"  project values. Re-run with --yes when you mean it; use --online\n"+
				"  for a rung change that keeps the controller running.\n", p.target.CommPath)
		return 2
	}
	mode := deploy.BuildOnly
	switch {
	case *online:
		mode = deploy.Online
	case *download:
		mode = deploy.Download
	}
	opts := deploy.Options{
		Client: logixd.New(*agent, *token), Target: p.target, Mode: mode, ProgramMode: *programMode,
		Log: func(format string, a ...any) { fmt.Printf(format+"\n", a...) },
	}
	if *keep != "" {
		opts.Keep = func(name string, raw []byte) {
			_ = os.MkdirAll(*keep, 0o755)
			if err := os.WriteFile(filepath.Join(*keep, name), raw, 0o644); err != nil {
				fmt.Fprintf(os.Stderr, "warning: --keep: %v\n", err)
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Minute)
	defer cancel()
	rep, err := deploy.Run(ctx, p.source, opts)
	for _, d := range rep.Diffs {
		fmt.Println("  " + d)
	}
	if err != nil {
		var de *deploy.DiagError
		if errors.As(err, &de) {
			for _, d := range de.Diags {
				fmt.Printf("%s:%d:1: logix target: %s [%s]\n", p.program, d.Line, d.Message, d.Rule)
			}
			fmt.Fprintf(os.Stderr, "naut logix deploy: %d construct(s) outside the Logix v1 subset; nothing sent\n", len(de.Diags))
			return 1
		}
		var ve *deploy.VerifyError
		if errors.As(err, &ve) {
			fmt.Printf("VERIFY FAILED — %d difference(s) between what was sent and what runs:\n", len(ve.Diffs))
			for _, d := range ve.Diffs {
				fmt.Println("  " + d)
			}
			return 1
		}
		var nd *deploy.NeedsDownloadError
		if errors.As(err, &nd) {
			fmt.Fprintf(os.Stderr, "naut logix deploy: %v; use --download --yes\n", err)
			return 1
		}
		return reportErr("deploy", err)
	}
	if rep.Applied != deploy.BuildOnly {
		fmt.Printf("%s done and verified in %s since the command started\n", rep.Applied, rep.Elapsed.Round(time.Millisecond))
	} else if p.target.CommPath == "" {
		fmt.Println("no comm path: built only")
	}
	return 0
}
