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

const siemensVerifyUsage = `naut siemens verify — does the CPU still run its last deploy?

Usage:
  naut siemens verify [flags] [dir]

Asks tiad to compare the CPU at target.siemens.station.cpu.address with a
deploy record: tiad opens a copy of that deploy's project archive and runs
TIA Portal's own online compare on it, through Openness, then closes it.
Read-only: nothing is loaded, saved or changed, on the CPU or in a project.
It goes online to the CPU.

tiad opens one project at a time: close any open session first (an agent
showing a block in TIA's editor holds one).

Flags:
  --deploy ID   the deploy record to compare with (default: the CPU's
                newest record that may have loaded it, loaded or in doubt)
  --agent URL   tiad URL (default: $NAUTILUS_TIAD_URL, then
                target.siemens.agent, then ` + tiad.DefaultURL + `)
` + deviceAttestUsage + `
Secrets come from the environment (or NAME_FILE), never flags:
  NAUTILUS_TIAD_TOKEN                      the agent's token
  NAUTILUS_SIEMENS_FULL_ACCESS_PASSWORD    the CPU user "nautilus", when the
                                           CPU asks credentials going online

Exit status: 0 the CPU compares equal to the deploy, 1 it does not (or the
compare could not say), 2 a usage or configuration error.
`

type siemensVerifyOptions struct {
	dir, deploy, agentFlag string
	// attest is the operator's device attestation (#1325), nil without.
	attest *tiad.DeviceAttestation
	getenv                 func(string) string
	stdout, stderr         io.Writer
}

func runSiemensVerify(args []string) int {
	o := siemensVerifyOptions{getenv: os.Getenv, stdout: os.Stdout, stderr: os.Stderr}
	if err := parseSiemensVerifyFlags(&o, args, os.Stderr); err != nil {
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return siemensVerify(ctx, o)
}

// parseSiemensVerifyFlags fills o from the command line.
func parseSiemensVerifyFlags(o *siemensVerifyOptions, args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("siemens verify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, siemensVerifyUsage) }
	fs.StringVar(&o.deploy, "deploy", "", "the deploy record to compare with")
	fs.StringVar(&o.agentFlag, "agent", "", "tiad URL")
	var dev deviceAttestFlags
	dev.register(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		fs.Usage()
		return errors.New("one project directory at most")
	}
	o.dir = "."
	if fs.NArg() == 1 {
		o.dir = fs.Arg(0)
	}
	a, err := dev.attestation(operatorName())
	if err != nil {
		fmt.Fprintf(stderr, "naut siemens verify: %v\n", err)
		return err
	}
	o.attest = a
	return nil
}

func siemensVerify(ctx context.Context, o siemensVerifyOptions) int {
	say := func(f string, a ...any) { fmt.Fprintf(o.stdout, f+"\n", a...) }
	fail := func(f string, a ...any) { fmt.Fprintf(o.stderr, "naut siemens verify: "+f+"\n", a...) }
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
	tgt := m.Target.Siemens
	address := tgt.Station.CPU.Address
	if address == "" {
		fail("%s: target.siemens.station.cpu.address is empty: no CPU to compare", manifest)
		return 2
	}
	client, err := siemensTiadClient(o.getenv, o.agentFlag, tgt.Agent)
	if err != nil {
		fail("%v", err)
		return 2
	}
	password, err := siemensSecret(o.getenv, "NAUTILUS_SIEMENS_FULL_ACCESS_PASSWORD")
	if err != nil {
		fail("%v", err)
		return 2
	}
	var online *tiad.OnlineSecrets
	if password != "" {
		online = &tiad.OnlineSecrets{FullAccessPassword: password}
	}

	list, err := client.Deploys(ctx, address)
	if err != nil {
		fail("the deploy record: %v", err)
		return 1
	}
	var rec *tiad.Deploy
	if o.deploy != "" {
		for i := range list.Deploys {
			if list.Deploys[i].ID == o.deploy {
				rec = &list.Deploys[i]
			}
		}
		if rec == nil {
			fail("no deploy %s for the CPU at %s in the agent's record", o.deploy, address)
			return 1
		}
	} else if rec = latestLoad(list); rec == nil {
		if list.UnreadableLines > 0 {
			fail("no readable deploy record for the CPU at %s, and %d journal line(s) the agent cannot read: nothing to compare with", address, list.UnreadableLines)
		} else {
			fail("no deploy record for the CPU at %s: nothing to compare with (naut siemens deploy --download makes one)", address)
		}
		return 1
	}

	say("verifying the CPU at %s against deploy %s (%s, %s, project %s)", address, rec.ID, rec.Status, rec.At, rec.Project)
	say("  TIA Portal's own online compare, through Openness on the agent: read-only")
	r, err := client.VerifyDeploy(ctx, tiad.VerifyRequest{Deploy: rec.ID, Secrets: online, DeviceAttestation: o.attest})
	if err != nil {
		if rp, pp, ok := tiad.VerifyMismatchOf(err); ok {
			fail("deploy %s's archived project has its CPU at %s, and the record says %s: refused, nothing compared", rec.ID, pp, rp)
			return 1
		}
		if tiad.Kind(err) == tiad.KindBusy {
			fail("%s", tiadMessage(err))
			return 1
		}
		fail("verify of deploy %s: %v", rec.ID, err)
		return 1
	}
	for _, b := range r.Blocks {
		say("  block %s", b)
	}
	for _, x := range r.Differences {
		say("  %s: %s", x.State, x.Object)
	}
	code := 1
	switch r.Verdict {
	case tiad.VerifyEqual:
		code = 0
		say("EQUAL: the CPU at %s runs deploy %s", address, rec.ID)
	case tiad.VerifyBroken:
		say("BROKEN: deploy %s's archive is not the one its record names (%s), so it was not compared", rec.ID, r.Broken)
	default:
		say("DIFFERENT: the CPU at %s does not run deploy %s as recorded", address, rec.ID)
	}
	switch {
	case r.ChangedOutside != nil && *r.ChangedOutside:
		say("  it is the CPU's latest deploy, so the CPU was changed outside nautilus since")
	case r.Latest != "" && r.Latest != rec.ID:
		say("  the CPU's latest deploy is %s, not this one", r.Latest)
	}
	if list.UnreadableLines > 0 {
		say("  %d journal line(s) the agent cannot read: one may be a newer deploy", list.UnreadableLines)
	}
	if r.CloseError != "" {
		// The verdict stands; the session is left open, broken.
		fail("the verify left its session %s open (%s): close it (DELETE /v1/sessions/%s) before anything else", r.Session, r.CloseError, r.Session)
		return 1
	}
	return code
}
