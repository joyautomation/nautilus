package main

// The agent-backed half of `nautilus logix`.
//
// `logix import|graph|normalize|info` (logix.go) are pure Go: they read an
// L5X that is already on disk and need nothing else. The verbs here drive a
// Logix PROJECT, which means the Studio 5000 SDK, which means Windows and a
// licence — so they talk to a logixd agent (tools/logixd) instead.
//
// Every one of them moves files for you. The agent owns a work directory and
// its API is relative to it, so a local path is uploaded before the operation
// and the result is fetched after. Running on the same machine as the agent
// (a self-hosted CI runner, say) works the same way; the copy is just cheap.

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/joyautomation/nautilus/lang/l5x"
	"github.com/joyautomation/nautilus/logix/logixd"
)

const logixAgentUsage = `nautilus logix — agent-backed verbs (need a logixd agent)

  nautilus logix probe            Is the SDK actually usable on the agent's
                                  machine, and if not, which of the three
                                  licensing gates failed?
  nautilus logix agent            Agent health, open sessions, work directory.
  nautilus logix convert <in> <out>
                                  Convert between ACD, L5K and L5X in either
                                  direction. --detailed adds References,
                                  Context, ProductDefinedTypes and IOTags to
                                  an L5X; the default lean export diffs better.
  nautilus logix build <project>  Compile the controller's routines. No
                                  controller involved and no risk — this is
                                  CI for control logic. Needs a v37+ project.
                                  -o writes the built project back.
  nautilus logix push <project> <rungs.L5X>
                                  Import rungs into a routine. With
                                  --comm-path and --accept or --finalize this
                                  is an ONLINE EDIT of a running controller.
  nautilus logix drift <local.L5X>
                                  Upload what is really in the controller,
                                  normalize both sides, and report whether
                                  the controller matches the repo.

Common flags:
  --agent      logixd base URL (default $NAUTILUS_LOGIXD_URL, else
               http://127.0.0.1:8188)
  --token      bearer token (default $NAUTILUS_LOGIXD_TOKEN)
`

// agentFlags registers the flags every agent-backed verb shares.
func agentFlags(fs *flag.FlagSet) (agent, token *string) {
	agent = fs.String("agent", "", "logixd base URL")
	token = fs.String("token", "", "logixd bearer token")
	return
}

func printEvents(evs []logixd.Event) {
	for _, e := range evs {
		// Progress chatter is noise on a terminal; status and error are
		// the diagnosis, which is the whole reason the agent returns them.
		if e.Kind == "progress" {
			continue
		}
		fmt.Fprintln(os.Stderr, "  "+e.String())
	}
}

// reportErr prints an agent error the way a person needs to read it: the
// message, whether the session died, and the SDK's own events, which are
// where a failed import actually explains itself.
func reportErr(verb string, err error) int {
	fmt.Fprintf(os.Stderr, "nautilus logix %s: %v\n", verb, err)
	var e *logixd.Error
	if errors.As(err, &e) {
		printEvents(e.Events)
		if e.Kind == "unreachable" {
			fmt.Fprintln(os.Stderr,
				"\nIs logixd running? It must live on the licensed Windows machine;\n"+
					"see tools/logixd/README.md. Point nautilus at it with --agent or\n"+
					"NAUTILUS_LOGIXD_URL.")
		}
	}
	return 1
}

// stage uploads a local file into the agent's work directory under a
// per-run prefix and returns the agent-relative path.
func stage(ctx context.Context, c *logixd.Client, runID, local string) (string, error) {
	raw, err := os.ReadFile(local)
	if err != nil {
		return "", err
	}
	rel := path.Join(runID, filepath.Base(local))
	if err := c.PutFile(ctx, rel, raw); err != nil {
		return "", err
	}
	return rel, nil
}

// fetch downloads an agent-relative path to a local file.
func fetch(ctx context.Context, c *logixd.Client, rel, local string) error {
	raw, err := c.GetFile(ctx, rel)
	if err != nil {
		return err
	}
	if dir := filepath.Dir(local); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(local, raw, 0o644)
}

func newRunID() string { return "run-" + time.Now().UTC().Format("20060102-150405.000") }

// --- probe / agent --------------------------------------------------------

func runLogixProbe(args []string) int {
	fs := flag.NewFlagSet("logix probe", flag.ContinueOnError)
	agent, token := agentFlags(fs)
	asJSON := fs.Bool("json", false, "emit the raw probe result")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	c := logixd.New(*agent, *token)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	p, err := c.Probe(ctx)
	if err != nil {
		return reportErr("probe", err)
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(p)
		if !p.Usable {
			return 1
		}
		return 0
	}
	for _, g := range p.Gates {
		mark := "ok  "
		if !g.OK {
			mark = "FAIL"
		}
		fmt.Printf("%s  %-22s %s\n", mark, g.Name, g.Detail)
	}
	if p.Usable {
		fmt.Println("\nThe SDK is usable.")
		return 0
	}
	fmt.Fprintln(os.Stderr, "\nThe SDK is NOT usable.")
	if p.Hint != "" {
		fmt.Fprintln(os.Stderr, p.Hint)
	}
	return 1
}

func runLogixAgent(args []string) int {
	fs := flag.NewFlagSet("logix agent", flag.ContinueOnError)
	agent, token := agentFlags(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	c := logixd.New(*agent, *token)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	h, err := c.Health(ctx)
	if err != nil {
		return reportErr("agent", err)
	}
	fmt.Printf("%s %s at %s\n", h.Service, h.Version, c.BaseURL)
	fmt.Printf("SDK client %s, %d open session(s)\n", h.SDKClient, h.Sessions)
	if len(h.CommAllowlist) > 0 {
		fmt.Printf("comm-path allowlist: %s\n", strings.Join(h.CommAllowlist, ", "))
	} else {
		fmt.Println("comm-path allowlist: (empty — any path is accepted; set LOGIXD_COMM_ALLOW)")
	}
	if wd, err := c.WorkDir(ctx); err == nil {
		fmt.Printf("work directory: %s\n", wd)
	}
	if ss, err := c.Sessions(ctx); err == nil && len(ss) > 0 {
		fmt.Println("open sessions:")
		for _, s := range ss {
			fmt.Printf("  %s  %s  (idle %s)\n", s.ID, s.Project,
				time.Since(s.LastUsed).Round(time.Second))
		}
	}
	return 0
}

// --- convert --------------------------------------------------------------

func runLogixConvert(args []string) int {
	fs := flag.NewFlagSet("logix convert", flag.ContinueOnError)
	agent, token := agentFlags(fs)
	detailed := fs.Bool("detailed", false, "include References, Context, ProductDefinedTypes and IOTags in an L5X")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 2 {
		fmt.Fprintln(os.Stderr, "usage: nautilus logix convert [flags] <input> <output>")
		return 2
	}
	in, out := fs.Arg(0), fs.Arg(1)
	c := logixd.New(*agent, *token)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	runID := newRunID()
	inRel, err := stage(ctx, c, runID, in)
	if err != nil {
		return reportErr("convert", err)
	}
	outRel := path.Join(runID, filepath.Base(out))
	res, evs, err := c.Convert(ctx, inRel, outRel, *detailed)
	printEvents(evs)
	if err != nil {
		return reportErr("convert", err)
	}
	if err := fetch(ctx, c, outRel, out); err != nil {
		return reportErr("convert", err)
	}
	fmt.Printf("wrote %s (%d bytes)\n", out, res.Bytes)
	return 0
}

// --- build ----------------------------------------------------------------

func runLogixBuild(args []string) int {
	fs := flag.NewFlagSet("logix build", flag.ContinueOnError)
	agent, token := agentFlags(fs)
	target := fs.String("target", "default", "build target: default, physical, echo")
	outPath := fs.String("o", "", "write the built project back to this path")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: nautilus logix build [flags] <project.ACD>")
		return 2
	}
	var bt logixd.BuildTarget
	switch strings.ToLower(*target) {
	case "", "default":
		bt = logixd.BuildDefault
	case "physical":
		bt = logixd.BuildPhysical
	case "echo":
		bt = logixd.BuildEcho
	default:
		fmt.Fprintln(os.Stderr, "nautilus logix build: --target must be default, physical or echo")
		return 2
	}

	c := logixd.New(*agent, *token)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Minute)
	defer cancel()

	runID := newRunID()
	rel, err := stage(ctx, c, runID, fs.Arg(0))
	if err != nil {
		return reportErr("build", err)
	}
	s, err := c.Open(ctx, rel)
	if err != nil {
		return reportErr("build", err)
	}
	defer s.Close(context.Background())

	res, evs, err := s.Build(ctx, bt)
	printEvents(evs)
	if err != nil {
		return reportErr("build", err)
	}
	fmt.Printf("build ok — target %s, %s\n", res.Target, time.Duration(res.ElapsedMs)*time.Millisecond)
	if *outPath != "" {
		if _, err := s.Save(ctx, "", false); err != nil {
			return reportErr("build", err)
		}
		if err := fetch(ctx, c, rel, *outPath); err != nil {
			return reportErr("build", err)
		}
		fmt.Printf("wrote %s\n", *outPath)
	}
	return 0
}

// --- push (the online edit) -----------------------------------------------

func runLogixPush(args []string) int {
	fs := flag.NewFlagSet("logix push", flag.ContinueOnError)
	agent, token := agentFlags(fs)
	program := fs.String("program", "", "program holding the routine (required)")
	routine := fs.String("routine", "", "RLL routine to import into (required)")
	at := fs.Uint("at", 0, "rung number to insert at")
	replace := fs.Uint("replace", 0, "how many existing rungs to replace, starting at --at")
	commPath := fs.String("comm-path", "", "controller to go online to; omit to stay offline")
	accept := fs.Bool("accept", false, "online: accept the edits and send them to the controller")
	finalize := fs.Bool("finalize", false, "online: accept, send, and assemble if the controller is in Run")
	outPath := fs.String("o", "", "write the modified project back to this path")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 2 || *program == "" || *routine == "" {
		fmt.Fprintln(os.Stderr,
			"usage: nautilus logix push [flags] <project.ACD> <rungs.L5X>\n"+
				"       --program and --routine are required")
		return 2
	}
	if *accept && *finalize {
		fmt.Fprintln(os.Stderr, "nautilus logix push: --accept and --finalize are alternatives")
		return 2
	}
	opt := logixd.LeaveEdits
	switch {
	case *finalize:
		opt = logixd.FinalizeEdits
	case *accept:
		opt = logixd.AcceptEdits
	}
	if opt != logixd.LeaveEdits && *commPath == "" {
		fmt.Fprintln(os.Stderr,
			"nautilus logix push: --accept and --finalize change a RUNNING controller, so\n"+
				"--comm-path is required. Without it the import is offline and the option\n"+
				"would be silently ignored.")
		return 2
	}

	c := logixd.New(*agent, *token)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	runID := newRunID()
	projRel, err := stage(ctx, c, runID, fs.Arg(0))
	if err != nil {
		return reportErr("push", err)
	}
	rungRel, err := stage(ctx, c, runID, fs.Arg(1))
	if err != nil {
		return reportErr("push", err)
	}
	s, err := c.Open(ctx, projRel)
	if err != nil {
		return reportErr("push", err)
	}
	defer s.Close(context.Background())

	if *commPath != "" {
		if _, err := s.SetCommPath(ctx, *commPath); err != nil {
			return reportErr("push", err)
		}
		st, err := s.GoOnline(ctx)
		if err != nil {
			return reportErr("push", err)
		}
		mode, err := s.Mode(ctx)
		if err != nil {
			return reportErr("push", err)
		}
		fmt.Printf("online to %s — controller is %s, connection %s\n", *commPath, mode, st.Connected)
	}

	xpath := logixd.RoutinePath(*program, *routine)
	res, evs, err := s.ImportRungs(ctx, xpath, uint32(*at), uint32(*replace), rungRel, opt)
	printEvents(evs)
	if err != nil {
		return reportErr("push", err)
	}
	fmt.Printf("imported rungs at %d (replacing %d) in %s/%s — %s, %s\n",
		res.InsertPosition, res.ReplaceCount, *program, *routine, res.OnlineOption,
		time.Duration(res.ElapsedMs)*time.Millisecond)

	if *commPath != "" {
		if mode, err := s.Mode(ctx); err == nil {
			fmt.Printf("controller is %s\n", mode)
		}
		if _, err := s.GoOffline(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "warning: going offline: %v\n", err)
		}
	}
	if *outPath != "" {
		if _, err := s.Save(ctx, "", false); err != nil {
			return reportErr("push", err)
		}
		if err := fetch(ctx, c, projRel, *outPath); err != nil {
			return reportErr("push", err)
		}
		fmt.Printf("wrote %s\n", *outPath)
	}
	return 0
}

// --- drift ----------------------------------------------------------------

func runLogixDrift(args []string) int {
	fs := flag.NewFlagSet("logix drift", flag.ContinueOnError)
	agent, token := agentFlags(fs)
	commPath := fs.String("comm-path", "", "controller to upload from (required)")
	keep := fs.String("keep", "", "also write the controller's L5X here")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 || *commPath == "" {
		fmt.Fprintln(os.Stderr, "usage: nautilus logix drift --comm-path <path> <repo.L5X>")
		return 2
	}
	repoPath := fs.Arg(0)
	repoRaw, err := os.ReadFile(repoPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "nautilus logix drift:", err)
		return 1
	}

	c := logixd.New(*agent, *token)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Minute)
	defer cancel()

	runID := newRunID()
	acdRel := path.Join(runID, "controller.ACD")
	l5xRel := path.Join(runID, "controller.L5X")

	// Upload what is ACTUALLY in the controller, then render it as L5X so
	// it can be compared with the repo on equal terms.
	evs, err := c.UploadToNew(ctx, *commPath, acdRel)
	printEvents(evs)
	if err != nil {
		return reportErr("drift", err)
	}
	res, evs, err := c.Convert(ctx, acdRel, l5xRel, false)
	printEvents(evs)
	if err != nil {
		return reportErr("drift", err)
	}
	ctrlRaw, err := c.GetFile(ctx, l5xRel)
	if err != nil {
		return reportErr("drift", err)
	}
	if *keep != "" {
		if err := os.WriteFile(*keep, ctrlRaw, 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "warning: --keep: %v\n", err)
		}
	}
	_ = res

	// Normalization is what makes this a comparison rather than a diff of
	// timestamps: every export stamps a new ExportDate.
	opts := l5x.NormalizeOptions{}
	if l5x.Equivalent(repoRaw, ctrlRaw, opts) {
		fmt.Printf("no drift — the controller at %s matches %s\n", *commPath, repoPath)
		return 0
	}
	fmt.Printf("DRIFT — the controller at %s does not match %s\n", *commPath, repoPath)
	a, b := l5x.Normalize(repoRaw, opts), l5x.Normalize(ctrlRaw, opts)
	fmt.Printf("  repo:       %d bytes normalized\n", len(a))
	fmt.Printf("  controller: %d bytes normalized\n", len(b))
	if *keep != "" {
		fmt.Printf("  the controller's export is at %s — diff it against %s\n", *keep, repoPath)
	} else {
		fmt.Println("  re-run with --keep <path> to get the controller's export and diff it")
	}
	return 1
}
