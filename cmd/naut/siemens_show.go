package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/joyautomation/nautilus/internal/project"
	"github.com/joyautomation/nautilus/siemens/tiad"
)

const siemensShowUsage = `naut siemens show — open a block of the deployed program in TIA Portal

Usage:
  naut siemens show [flags] <block> [dir]
  naut siemens show --close [dir]

Opens the project the last deploy saved (projects/<name>/<name>.ap21) on
tiad, and shows <block> in TIA Portal's own editor, on the agent's screen.
<block> is the TIA block's name, compared without case: naut_<program>
for a program, naut_Tags for the tags. It reads the project and changes
nothing, on the CPU or in the project. It does not go online.

Only an agent installed with install-tiad.ps1 -Ui (TIAD_UI=1) runs TIA
with its window; any other has no editor to show a block in, and show
refuses before it opens anything. An agent with its window refuses every
download: reinstall it without -Ui to deploy again.

The project stays open in TIA, so another show reuses it. tiad holds one
project at a time, so close it (--close) before naut siemens verify or
deploy.

Flags:
  --close       close the project a show left open in TIA
  --agent URL   tiad URL (default: $NAUTILUS_TIAD_URL, then
                target.siemens.agent, then ` + tiad.DefaultURL + `)

The agent's token comes from NAUTILUS_TIAD_TOKEN (or NAUTILUS_TIAD_TOKEN_FILE),
never a flag.
`

type siemensShowOptions struct {
	dir, block, agentFlag string
	close                 bool
	getenv                func(string) string
	stdout, stderr        io.Writer
}

func runSiemensShow(args []string) int {
	o := siemensShowOptions{getenv: os.Getenv, stdout: os.Stdout, stderr: os.Stderr}
	if err := parseSiemensShowFlags(&o, args, os.Stderr); err != nil {
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return siemensShow(ctx, o)
}

// parseSiemensShowFlags fills o from the command line: <block> [dir], or
// --close [dir].
func parseSiemensShowFlags(o *siemensShowOptions, args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("siemens show", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, siemensShowUsage) }
	fs.BoolVar(&o.close, "close", false, "close the project a show left open in TIA")
	fs.StringVar(&o.agentFlag, "agent", "", "tiad URL")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if !o.close {
		if len(rest) == 0 {
			fs.Usage()
			return errors.New("a block to show is required")
		}
		o.block, rest = rest[0], rest[1:]
	}
	if len(rest) > 1 {
		fs.Usage()
		return errors.New("one project directory at most")
	}
	o.dir = "."
	if len(rest) == 1 {
		o.dir = rest[0]
	}
	return nil
}

func siemensShow(ctx context.Context, o siemensShowOptions) int {
	say := func(f string, a ...any) { fmt.Fprintf(o.stdout, f+"\n", a...) }
	fail := func(f string, a ...any) { fmt.Fprintf(o.stderr, "naut siemens show: "+f+"\n", a...) }
	manifest := filepath.Join(o.dir, project.ManifestName)

	m, err := project.ReadManifest(os.DirFS(o.dir), "")
	if err != nil {
		fail("%v", err)
		return 2
	}
	if m.Target == nil || m.Target.Siemens == nil {
		fail("%s declares no target: siemens section", manifest)
		return 2
	}
	client, err := siemensTiadClient(o.getenv, o.agentFlag, m.Target.Siemens.Agent)
	if err != nil {
		fail("%v", err)
		return 2
	}
	name := siemensProjectName(m.Name)
	saved := "projects/" + name + "/" + name + ".ap21"

	sessions, err := client.Sessions(ctx)
	if err != nil {
		fail("the agent's sessions: %v", err)
		return 1
	}
	var open *tiad.Session
	for i := range sessions {
		if sessions[i].Project == saved {
			open = &sessions[i]
		}
	}

	if o.close {
		if open == nil {
			say("%s is not open on the agent: nothing to close", saved)
			return 0
		}
		if err := open.Close(ctx); err != nil {
			fail("closing %s (session %s): %v", saved, open.ID, err)
			return 1
		}
		say("closed %s (session %s)", saved, open.ID)
		return 0
	}

	// Before anything opens: an agent without TIA's window has no editor.
	h, err := client.Health(ctx)
	if err != nil {
		fail("the agent: %v", err)
		return 1
	}
	if !h.UI {
		fail("the agent runs TIA Portal without its window, so there is no editor to show %s in: reinstall it with install-tiad.ps1 -Ui (TIAD_UI=1). Such an agent refuses every download; reinstall without -Ui to deploy again. Nothing was opened", o.block)
		return 1
	}

	opened := false
	if open == nil {
		if len(sessions) > 0 {
			s := sessions[0]
			fail("the agent has %s open (session %s), and tiad holds one project at a time: close it first (DELETE /v1/sessions/%s, or naut siemens show --close in its project)", s.Project, s.ID, s.ID)
			return 1
		}
		if open, err = client.Open(ctx, saved); err != nil {
			if tiad.Kind(err) == tiad.KindNotFound {
				fail("no saved project %s on the agent: deploy first (naut siemens deploy)", saved)
				return 1
			}
			fail("opening %s: %v", saved, err)
			return 1
		}
		opened = true
		say("opened %s in TIA Portal (session %s)", saved, open.ID)
	}
	r, err := open.Show(ctx, o.block)
	if err != nil {
		fail("%s", tiadMessage(err))
		// A session this show opened for nothing is closed again.
		if opened {
			if cerr := open.Close(context.Background()); cerr != nil {
				fail("closing session %s: %v (close it: DELETE /v1/sessions/%s)", open.ID, cerr, open.ID)
			}
		}
		return 1
	}
	where := "the program blocks' root"
	if r.Group != "" {
		where = "group " + r.Group
	}
	say("TIA Portal's editor shows %s [%s%d], in %s", r.Block, r.Type, r.Number, where)
	say("  %s stays open in TIA (session %s); close it with naut siemens show --close before verify or deploy", saved, open.ID)
	return 0
}
