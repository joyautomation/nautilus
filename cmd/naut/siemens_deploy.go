package main

// naut siemens deploy: the project, written as SCL, compiled by TIA through
// tiad and, on request, downloaded to the CPU (docs/design/siemens-ide.md
// §1). Every step is a tiad route: nobody opens TIA Portal.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/joyautomation/nautilus/internal/project"
	"github.com/joyautomation/nautilus/siemens/station"
	"github.com/joyautomation/nautilus/siemens/tiad"
	swriter "github.com/joyautomation/nautilus/siemens/writer"
)

const siemensDeployUsage = `naut siemens deploy — compile a nautilus project for an S7-1500 and download it

Usage:
  naut siemens deploy [flags] [project-dir]

Reads nautilus.yaml (target: siemens), runs naut check's Siemens rules,
writes the program as SCL and, through tiad (the TIA Portal Openness
agent), builds the TIA project from the station spec, imports the sources
into the block group nautilus owns, makes the cyclic interrupt OBs and
compiles hardware and software. A TIA compile message is printed against
the nautilus file and line it came from.

  (no flag)    Build and compile only. Nothing reaches the CPU, and the
               TIA project is not saved.
  --download   Download to the CPU at target.siemens.station.cpu.address.
               It reaches the CPU: announce it to everyone who shares the
               CPU before you run it. In order:
                 1. the CPU is checked against its last deploy record
                    (tiad's verify); a CPU changed outside nautilus is
                    refused without --accept-outside-changes;
                 2. the plan is printed: the last deploy's verdict, station
                    changes and whether hardware is loaded, what happens to
                    each cyclic interrupt OB (created, kept, recreated with
                    its old and new cycle, removed), and every answer the
                    download will give TIA, in words;
                 3. you confirm (type yes, or pass --yes);
                 4. with --dry-run, a dry run shows TIA's first
                    pre-download check, when the agent gives one; one the
                    answers refuse stops there. It reaches the CPU too, and
                    a TIA that asks no check loads during it (tiad refuses
                    dry runs until its TIA side can promise the cancel);
                 5. the download, which succeeds only when an online
                    compare right after it is equal; the deploy record
                    (archive, source, fingerprint) is printed and the TIA
                    project saved for the next deploy.

Answers are never implied: each acting one has its own flag.
  --allow-stop        TIA may stop the CPU to load (without it, a load that
                      needs STOP is cancelled, and loads nothing)
  --reinit DB,...     the DBs TIA may reinitialize (their values go back to
                      start values, retained ones too). Without the DB it
                      asks for, the download is cancelled there: after a
                      stop that ends in doubt, the CPU possibly in STOP
  --start, --no-start one of them, always: whether TIA starts the CPU after
                      the load (--start starts one that was in STOP before
                      too)
  --hardware          load the hardware configuration too. tiad cannot
                      verify hardware online, so it needs
                      --attest-hardware "<your statement that the CPU's
                      hardware configuration is what you intend to load>";
                      --cpu-access-control disabled|enabled is your
                      observation of the CPU's access control now, which a
                      user-management reset rests on
  --trust-certificate SHA256   the CPU certificate TIA may trust
  --accept-outside-changes     download although the CPU was changed
                      outside nautilus since its last deploy (or the
                      record cannot say)
  --yes               confirm without the prompt (what is printed is what
                      is answered)

Other flags:
  --agent URL   tiad URL (default: $NAUTILUS_TIAD_URL, then
                target.siemens.agent, then ` + tiad.DefaultURL + `)
  --keep DIR    keep the generated SCL sources here

Secrets come from the environment (or NAME_FILE, a file holding it), never
flags or the manifest:
  NAUTILUS_TIAD_TOKEN                      the agent's token
  NAUTILUS_SIEMENS_CONFIG_PASSWORD         cpu configuration-protection password
  NAUTILUS_SIEMENS_FULL_ACCESS_PASSWORD    the CPU user "nautilus" (Full access)
  NAUTILUS_SIEMENS_WEB_PASSWORD_<USER>     each web user's password
                                           (NAUTILUS_SIEMENS_PASSWORD for
                                           target.siemens.online.user)
  NAUTILUS_SIEMENS_PROTECTION_PASSWORD     a protected CPU's password
`

// siemensGSDMLDate is the date in the file name of a PROFINET device's
// GSDML. A deploy never reads the clock: a new date is a new GSD to TIA,
// and so a hardware change. The body does not carry it.
var siemensGSDMLDate = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// siemensSourceDir is where a deploy puts its sources in tiad's work dir.
const siemensSourceDir = "naut-src"

// siemensDeployOptions is a deploy, after flags.
type siemensDeployOptions struct {
	dir                   string
	download, yes, dryRun bool
	allowStop             bool
	start, noStart        bool
	reinit                []string
	hardware              bool
	attest, cpuAccess     string
	trustCertificate      string
	acceptOutside         bool
	keep                  string
	getenv                func(string) string
	stdout, stderr        io.Writer
	stdin                 io.Reader
	terminal              bool // stdin is a terminal: the prompt may be asked
	by                    string
	gitCommit             func(dir string) string
	timeout               time.Duration
	agentFlag             string
	inDoubt               func() // called when the deploy goes in doubt; may be nil
}

func runSiemensDeploy(args []string) int {
	o := siemensDeployOptions{getenv: os.Getenv, stdout: os.Stdout, stderr: os.Stderr, stdin: os.Stdin,
		terminal: stdinIsTerminal(), by: operatorName(), gitCommit: gitHead, timeout: 60 * time.Minute}
	if err := parseSiemensDeployFlags(&o, args, os.Stderr); err != nil {
		return 2
	}
	// Ctrl-C cancels the deploy where it is, and the session is still
	// closed (or, in doubt, named) on the way out.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// Once in doubt, a second Ctrl-C ends the process: the compare that
	// follows must not hold it for minutes.
	o.inDoubt = stop
	return siemensDeploy(ctx, o)
}

// parseSiemensDeployFlags fills o from the command line.
func parseSiemensDeployFlags(o *siemensDeployOptions, args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("siemens deploy", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, siemensDeployUsage) }
	fs.StringVar(&o.agentFlag, "agent", "", "tiad URL")
	fs.BoolVar(&o.download, "download", false, "download to the CPU")
	fs.BoolVar(&o.yes, "yes", false, "confirm the download")
	fs.BoolVar(&o.dryRun, "dry-run", false, "after the confirmation, ask TIA's first pre-download check (it reaches the CPU)")
	fs.BoolVar(&o.allowStop, "allow-stop", false, "TIA may stop the CPU")
	fs.BoolVar(&o.start, "start", false, "start the CPU after the load")
	fs.BoolVar(&o.noStart, "no-start", false, "leave the CPU in STOP after the load")
	reinit := fs.String("reinit", "", "DBs TIA may reinitialize, comma-separated")
	fs.BoolVar(&o.hardware, "hardware", false, "load the hardware configuration too")
	fs.StringVar(&o.attest, "attest-hardware", "", "your statement that the CPU's hardware configuration is what you intend to load")
	fs.StringVar(&o.cpuAccess, "cpu-access-control", "", "the CPU's access control now, as you observed it: disabled or enabled")
	fs.StringVar(&o.trustCertificate, "trust-certificate", "", "the CPU certificate's SHA-256 TIA may trust")
	fs.BoolVar(&o.acceptOutside, "accept-outside-changes", false, "download although the CPU changed outside nautilus")
	fs.StringVar(&o.keep, "keep", "", "directory to keep the generated sources in")
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
	for _, db := range strings.Split(*reinit, ",") {
		if db = strings.TrimSpace(db); db != "" {
			o.reinit = append(o.reinit, db)
		}
	}
	return nil
}

// operatorName is who the CLI says attests: the local user, as tiad
// records the caller's claim.
func operatorName() string {
	u, err := user.Current()
	if err != nil {
		return ""
	}
	h, _ := os.Hostname()
	if h == "" {
		return u.Username
	}
	return u.Username + "@" + h
}

// gitHead is dir's git commit, "" outside a repository or when the
// project has uncommitted changes: the deploy record must never claim a
// commit the CPU does not run.
func gitHead(dir string) string {
	if out, err := exec.Command("git", "-C", dir, "status", "--porcelain", "--", ".").Output(); err != nil || len(out) > 0 {
		return ""
	}
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// siemensDeployFlagsError refuses flag combinations before anything is read.
func (o siemensDeployOptions) flagsError() error {
	acting := o.yes || o.dryRun || o.allowStop || o.start || o.noStart || len(o.reinit) > 0 || o.hardware || o.attest != "" ||
		o.cpuAccess != "" || o.trustCertificate != "" || o.acceptOutside
	switch {
	case !o.download && acting:
		return errors.New("--yes, --dry-run, --allow-stop, --start, --no-start, --reinit, --hardware, --attest-hardware, --cpu-access-control, --trust-certificate and --accept-outside-changes go with --download")
	case o.download && o.start == o.noStart:
		return errors.New("--download needs --start or --no-start: whether TIA starts the CPU after the load (also one that was in STOP before) is never implied")
	case !o.hardware && (o.attest != "" || o.cpuAccess != ""):
		return errors.New("--attest-hardware and --cpu-access-control go with --hardware")
	case o.hardware && strings.TrimSpace(o.attest) == "":
		return errors.New("--hardware needs --attest-hardware \"<your statement that the CPU's hardware configuration is what you intend to load>\": tiad cannot verify hardware online")
	case o.cpuAccess != "" && o.cpuAccess != tiad.AttestCPUAccessDisabled && o.cpuAccess != tiad.AttestCPUAccessEnabled:
		return fmt.Errorf("--cpu-access-control %q: want disabled or enabled, as you observed the CPU", o.cpuAccess)
	case len(o.attest) > tiad.MaxAttestationText:
		return fmt.Errorf("--attest-hardware is %d bytes, over %d", len(o.attest), tiad.MaxAttestationText)
	}
	return nil
}

// siemensSecret reads name, or the file name_FILE names.
func siemensSecret(getenv func(string) string, name string) (string, error) {
	if v := getenv(name); v != "" {
		return v, nil
	}
	f := getenv(name + "_FILE")
	if f == "" {
		return "", nil
	}
	b, err := os.ReadFile(f)
	if err != nil {
		return "", fmt.Errorf("%s_FILE: %w", name, err)
	}
	return strings.TrimRight(string(b), "\r\n"), nil
}

// webPasswordEnv is the variable holding web user u's password.
func webPasswordEnv(u string) string {
	return "NAUTILUS_SIEMENS_WEB_PASSWORD_" + strings.ToUpper(regexp.MustCompile(`[^A-Za-z0-9]`).ReplaceAllString(u, "_"))
}

// siemensDeploySecrets reads the station's secrets from the environment,
// and refuses (naming each variable) what the station needs and lacks.
func siemensDeploySecrets(st tiad.Station, onlineUser string, getenv func(string) string) (tiad.Secrets, error) {
	var sec tiad.Secrets
	var err error
	read := func(name string, into *string) {
		if err == nil {
			*into, err = siemensSecret(getenv, name)
		}
	}
	read("NAUTILUS_SIEMENS_CONFIG_PASSWORD", &sec.ConfigurationPassword)
	read("NAUTILUS_SIEMENS_FULL_ACCESS_PASSWORD", &sec.FullAccessPassword)
	read("NAUTILUS_SIEMENS_PROTECTION_PASSWORD", &sec.Protection)
	rep := []string{
		"secrets.configurationPassword", "NAUTILUS_SIEMENS_CONFIG_PASSWORD",
		"secrets.fullAccessPassword", "NAUTILUS_SIEMENS_FULL_ACCESS_PASSWORD",
		"secrets.protection", "NAUTILUS_SIEMENS_PROTECTION_PASSWORD",
	}
	if w := st.WebServer; w != nil {
		for _, u := range w.Users {
			var pw string
			env := webPasswordEnv(u.Name)
			read(env, &pw)
			if pw == "" && u.Name == onlineUser {
				env = "NAUTILUS_SIEMENS_PASSWORD"
				read(env, &pw)
			}
			if pw != "" {
				if sec.WebUsers == nil {
					sec.WebUsers = map[string]string{}
				}
				sec.WebUsers[u.Name] = pw
			}
			rep = append(rep, fmt.Sprintf("secrets.webUsers[%q]", u.Name), env,
				fmt.Sprintf("web user %s has no password in secrets.webUsers", u.Name), fmt.Sprintf("web user %s has no password: set %s", u.Name, env))
		}
	}
	if err != nil {
		return sec, err
	}
	if err := sec.CheckFor(st); err != nil {
		return sec, errors.New(strings.NewReplacer(rep...).Replace(err.Error()))
	}
	return sec, nil
}

// siemensStation is the manifest's station as tiad applies it, with the
// GSDML files its PROFINET references need installed first.
func siemensStation(dir string, st station.Station) (tiad.Station, []gsdFile, error) {
	out := tiad.Station{CPU: stationCPU(st.CPU)}
	if w := st.WebServer; w != nil {
		ws := &tiad.WebServer{Enabled: w.Enabled}
		for _, u := range w.Users {
			ws.Users = append(ws.Users, tiad.WebUser{Name: u.Name, Rights: u.Rights})
		}
		out.WebServer = ws
	}
	for _, m := range st.Modules {
		out.Modules = append(out.Modules, tiad.Module{Slot: m.Slot, Order: m.Order, Firmware: m.Firmware, InputStart: m.InputStart, OutputStart: m.OutputStart})
	}
	pn, files, err := expandProfinet(dir, st.CPU, st.Profinet, siemensGSDMLDate)
	if err != nil {
		return out, nil, err
	}
	out.Profinet = pn
	if err := out.Validate(); err != nil {
		return out, nil, err
	}
	return out, files, nil
}

// siemensProjectName is the TIA project a manifest deploys through.
func siemensProjectName(name string) string {
	n := regexp.MustCompile(`[^A-Za-z0-9_-]`).ReplaceAllString(name, "_")
	if n == "" || tiad.CheckProjectName(n) != nil {
		return "nautilus"
	}
	return n
}

// sourcesHash is the sha256 of the generated sources, in the deploy
// record's form.
func sourcesHash(files []swriter.File) string {
	h := sha256.New()
	for _, f := range files {
		fmt.Fprintf(h, "%s\x00%d\x00", f.Path, len(f.Data))
		h.Write(f.Data)
	}
	return tiad.DeploySourceHashPrefix + hex.EncodeToString(h.Sum(nil))
}

// siemensDeployer carries a deploy's state between its steps.
type siemensDeployer struct {
	o            siemensDeployOptions
	manifestFile string
	out          swriter.Output
	client       *tiad.Client
	// created is set when this deploy made the TIA project, changes are
	// what the station PUT changed on a saved one: the plan says the
	// hardware differs from what the CPU was last loaded with.
	created bool
	changes []string
	// stationSame says whether the spec is the one the last successful
	// deploy loaded (its copy in the agent's work dir); nil when there is
	// no copy to compare with.
	stationSame *bool
	project     string // the TIA project's name
	// keepSession is set when a download ended unreachable: the agent may
	// still be loading, so the session is named, not closed.
	keepSession bool
}

// siemensStationRecord is where a successful deploy leaves the station
// spec it loaded, in the agent's work dir: tiad's apply on a saved project
// reports only that it took the CPU over, so the next deploy's plan reads
// whether the spec changed from here.
func siemensStationRecord(name string) string {
	return siemensSourceDir + "/" + name + "/station.json"
}

func (d *siemensDeployer) say(f string, a ...any) { fmt.Fprintf(d.o.stdout, f+"\n", a...) }
func (d *siemensDeployer) fail(f string, a ...any) {
	fmt.Fprintf(d.o.stderr, "naut siemens deploy: "+f+"\n", a...)
}

// where names a writer path the way check does: the manifest for a block
// with no nautilus file of its own.
func (d *siemensDeployer) where(p string) string {
	if p == "" || p == project.ManifestName {
		return d.manifestFile
	}
	return p
}

// compileMessage prints one TIA compile message against the nautilus file
// and line it came from, and reports whether it was placed.
func (d *siemensDeployer) compileMessage(m tiad.CompileMessage) {
	sev := m.Severity
	if sev == "" {
		sev = "info"
	}
	if block, line, ok := m.Block(); ok {
		if file, src, ok := d.out.Locate(block, line); ok {
			if src > 0 {
				d.say("%s:%d:1: %s: TIA: %s [%s]", d.where(file), src, sev, m.Text, block)
			} else {
				d.say("%s: %s: TIA: %s [%s]", d.where(file), sev, m.Text, block)
			}
			return
		}
		d.say("%s: %s: TIA: %s [%s, not a block nautilus generated]", d.manifestFile, sev, m.Text, block)
		return
	}
	where := strings.Join(append(slices.Clone(m.Path), m.Object), " > ")
	where = strings.Trim(where, " >")
	if where == "" {
		d.say("TIA: %s: %s", sev, m.Text)
		return
	}
	d.say("TIA: %s: %s [%s]", sev, m.Text, where)
}

// lastDeploy is what the CPU's deploy record says before a download.
type lastDeploy struct {
	id, status string
	verdict    string // tiad.Verify*, "" without a record
	outside    bool   // the CPU changed outside nautilus, or the record cannot say
	why        string
}

func (d *siemensDeployer) checkLastDeploy(ctx context.Context, address string, online *tiad.OnlineSecrets) (lastDeploy, error) {
	list, err := d.client.Deploys(ctx, address)
	if err != nil {
		return lastDeploy{}, err
	}
	var latest *tiad.Deploy
	for i := range list.Deploys {
		if list.Deploys[i].Status != tiad.DeployNotLoaded {
			latest = &list.Deploys[i]
			break
		}
	}
	if latest == nil {
		why := "no deploy record for this CPU: whatever it runs now is replaced"
		if list.UnreadableLines > 0 {
			return lastDeploy{outside: true, why: fmt.Sprintf("no readable deploy record for this CPU, and %d journal line(s) the agent cannot read", list.UnreadableLines)}, nil
		}
		return lastDeploy{why: why}, nil
	}
	ld := lastDeploy{id: latest.ID, status: latest.Status}
	r, err := d.client.VerifyDeploy(ctx, tiad.VerifyRequest{Deploy: latest.ID, Secrets: online})
	if err != nil {
		return ld, fmt.Errorf("verify of the last deploy %s: %w", latest.ID, err)
	}
	ld.verdict = r.Verdict
	if r.CloseError != "" {
		return ld, fmt.Errorf("the verify of the last deploy %s left its session open (%s): close it (DELETE /v1/sessions/%s) before deploying", latest.ID, r.CloseError, r.Session)
	}
	switch {
	case r.Verdict == tiad.VerifyEqual && latest.Status == tiad.DeployLoaded:
		ld.why = fmt.Sprintf("last deploy %s (loaded): the CPU still compares equal to it", latest.ID)
	case r.Verdict == tiad.VerifyEqual:
		ld.why = fmt.Sprintf("last deploy %s (%s): the CPU compares equal to it", latest.ID, latest.Status)
	case r.Verdict == tiad.VerifyBroken:
		ld.outside, ld.why = true, fmt.Sprintf("last deploy %s: its archive is broken (%s), so nothing says what the CPU runs", latest.ID, r.Broken)
	default:
		var diffs []string
		for _, x := range r.Differences {
			diffs = append(diffs, x.Object+" ("+x.State+")")
		}
		if latest.Status == tiad.DeployLoaded {
			ld.outside, ld.why = true, fmt.Sprintf("last deploy %s (loaded): the CPU compares DIFFERENT, so it was changed outside nautilus: %s", latest.ID, strings.Join(diffs, ", "))
		} else {
			ld.outside, ld.why = true, fmt.Sprintf("last deploy %s is IN DOUBT and the CPU compares DIFFERENT from it: it may hold that unfinished load or a change made outside nautilus: %s", latest.ID, strings.Join(diffs, ", "))
		}
	}
	return ld, nil
}

// decisions is every answer the download gives, from the flags and the
// session; plan says them in words.
func (d *siemensDeployer) decisions(st tiad.Station, kept []tiad.KeptDevice) (tiad.Decisions, []string) {
	o := d.o
	var dec tiad.Decisions
	var plan []string
	if o.allowStop {
		dec.StopCPU = tiad.Yes()
		plan = append(plan, "stop the CPU if TIA needs to: yes (--allow-stop)")
	} else {
		dec.StopCPU = tiad.No()
		plan = append(plan, "stop the CPU: no. If TIA asks, the download is cancelled there and loads nothing (pass --allow-stop to let it stop the CPU)")
	}
	if len(o.reinit) > 0 {
		dec.Reinitialize = slices.Clone(o.reinit)
		plan = append(plan, "reinitialize, if TIA asks: "+strings.Join(o.reinit, ", ")+" (values go back to start values, retained ones too). TIA asking for any other DB cancels the download there, IN DOUBT after a stop")
	} else {
		plan = append(plan, "reinitialize a DB: no. If TIA asks, the download is cancelled there: before the CPU was stopped that loads nothing, but after a stop it ends IN DOUBT with the CPU possibly left in STOP (pass --reinit <DB,...> naming each DB you accept to reset)")
	}
	if o.start {
		dec.StartAfter = tiad.Yes()
		plan = append(plan, "start the CPU after the load: yes (--start), also a CPU that was in STOP before")
	} else {
		dec.StartAfter = tiad.No()
		plan = append(plan, "start the CPU after the load: no, it stays in STOP (--no-start)")
	}
	dec.ConsistentDownload, dec.AlarmTexts, dec.AllBlocks = tiad.Yes(), tiad.Yes(), tiad.Yes()
	plan = append(plan, "load the software as one consistent download, with its alarm texts and every block TIA asks to load: yes")
	dec.IdentificationDataLoadNothing = tiad.Yes()
	plan = append(plan, "identification data: load none")
	if len(kept) > 0 {
		names := keptDeviceNames(kept)
		dec.KeptDevices = names
		plan = append(plan, "load the devices the project keeps as found: "+strings.Join(names, ", "))
	}
	if o.trustCertificate != "" {
		dec.TrustCertificate = o.trustCertificate
		plan = append(plan, "trust the CPU certificate "+o.trustCertificate)
	}
	switch {
	case o.hardware:
		dec.AcceptUnverifiedHardware, dec.Overwrite = tiad.Yes(), tiad.Yes()
		plan = append(plan, fmt.Sprintf("load the HARDWARE configuration, UNVERIFIED (tiad cannot compare hardware online), replacing the CPU's system data; attested by %s: %q", o.by, o.attest))
		if o.cpuAccess != "" {
			dec.UserManagementResetToProject = tiad.Yes()
			plan = append(plan, fmt.Sprintf("user management: RESET the CPU's to the project's (deletes the CPU's users), resting on your observation that its access control is %s now", o.cpuAccess))
		} else {
			plan = append(plan, "user management: not answered. If TIA asks (a hardware load does), the download is cancelled there, IN DOUBT after a stop (pass --cpu-access-control with what you observed)")
		}
	case st.CPU.AccessControl == tiad.AccessKeep:
		dec.UserManagementKeepOnline = tiad.Yes()
		plan = append(plan, "user management: keep the CPU's users as they are")
	default:
		plan = append(plan, "user management: not answered (tiad keeps the CPU's users only for access-control keep, and resets them only on a hardware load). If TIA asks, the download is cancelled there, IN DOUBT after a stop")
	}
	return dec, plan
}

func keptDeviceNames(kept []tiad.KeptDevice) []string {
	seen := map[string]bool{}
	var names []string
	for _, k := range kept {
		if !seen[k.Name] {
			seen[k.Name] = true
			names = append(names, k.Name)
		}
	}
	sort.Strings(names)
	return names
}

// describeOB says an OB step in words.
func describeOB(s tiad.OBStep) string {
	switch s.Action {
	case tiad.OBRecreated:
		return fmt.Sprintf("%s: RECREATED, was %s, now %s", s.Name, s.Was, s.Now)
	case tiad.OBRemoved:
		return fmt.Sprintf("%s: REMOVED (was %s)", s.Name, s.Was)
	case tiad.OBCreated:
		return fmt.Sprintf("%s: created, %s", s.Name, s.Now)
	}
	if s.Now != "" {
		return fmt.Sprintf("%s: %s, %s", s.Name, s.Action, s.Now)
	}
	return s.Name + ": " + s.Action
}

// confirm asks the operator, or takes --yes.
func (d *siemensDeployer) confirm(ctx context.Context, address string) bool {
	if d.o.yes {
		d.say("confirmed by --yes")
		return true
	}
	if !d.o.terminal {
		d.fail("refusing the download without a confirmation: re-run with --yes to answer exactly as printed above")
		return false
	}
	fmt.Fprintf(d.o.stdout, "Download to %s, answering exactly as above? Type yes: ", address)
	got := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(d.o.stdin).ReadString('\n')
		got <- line
	}()
	var line string
	select {
	case line = <-got:
	case <-ctx.Done():
		fmt.Fprintln(d.o.stdout)
	}
	if strings.TrimSpace(line) != "yes" {
		d.fail("not confirmed; nothing was downloaded")
		return false
	}
	return true
}

// siemensDeploy runs one deploy and returns the exit code.
func siemensDeploy(ctx context.Context, o siemensDeployOptions) int {
	d := &siemensDeployer{o: o, manifestFile: filepath.Join(o.dir, project.ManifestName)}
	if err := o.flagsError(); err != nil {
		d.fail("%v", err)
		return 2
	}

	// 1. The project, through naut check's Siemens rules.
	m, err := project.ReadManifest(os.DirFS(o.dir), "")
	if err != nil {
		d.fail("%v", err)
		return 2
	}
	if m.Target == nil || m.Target.Siemens == nil {
		d.fail("%s declares no target: siemens section", d.manifestFile)
		return 2
	}
	tgt := m.Target.Siemens
	problems := m.Target.Problems()
	for _, p := range problems {
		d.say("%s: error: %s", d.manifestFile, p)
	}
	in, err := siemensWriterInput(o.dir, "")
	if err != nil {
		d.fail("%v", err)
		return 2
	}
	out, diags, err := swriter.Write(in)
	if err != nil {
		d.fail("%v", err)
		return 2
	}
	for _, x := range diags {
		if x.Line > 0 {
			d.say("%s:%d:1: siemens target: %s [%s]", d.where(x.Path), x.Line, x.Message, x.Rule)
		} else {
			d.say("%s: siemens target: %s [%s]", d.where(x.Path), x.Message, x.Rule)
		}
	}
	if len(problems)+len(diags) > 0 {
		d.fail("%d problem(s): nothing sent to tiad", len(problems)+len(diags))
		return 1
	}
	d.out = out
	if o.keep != "" {
		if err := os.MkdirAll(o.keep, 0o755); err != nil {
			d.fail("--keep: %v", err)
			return 1
		}
		for _, f := range out.Files {
			if err := os.WriteFile(filepath.Join(o.keep, f.Path), f.Data, 0o644); err != nil {
				d.fail("--keep: %v", err)
				return 1
			}
		}
	}

	// 2. The station and its secrets.
	st, gsds, err := siemensStation(o.dir, tgt.Station)
	if err != nil {
		d.fail("target.siemens.station: %v", err)
		return 2
	}
	onlineUser := ""
	if tgt.Online != nil {
		onlineUser = tgt.Online.User
	}
	sec, err := siemensDeploySecrets(st, onlineUser, o.getenv)
	if err != nil {
		d.fail("%v", err)
		return 2
	}
	var online *tiad.OnlineSecrets
	if sec.FullAccessPassword != "" {
		online = &tiad.OnlineSecrets{FullAccessPassword: sec.FullAccessPassword}
	}

	agent := o.agentFlag
	if agent == "" {
		agent = o.getenv("NAUTILUS_TIAD_URL")
	}
	if agent == "" {
		agent = tgt.Agent
	}
	if agent == "" {
		agent = tiad.DefaultURL
	}
	token, err := siemensSecret(o.getenv, "NAUTILUS_TIAD_TOKEN")
	if err != nil {
		d.fail("%v", err)
		return 2
	}
	if token == "" {
		d.fail("set NAUTILUS_TIAD_TOKEN or NAUTILUS_TIAD_TOKEN_FILE to the agent's token (it is never a flag)")
		return 2
	}
	d.client = tiad.New(agent, token)
	address := st.CPU.Address

	// 3. Before the session: what the CPU's deploy record says. tiad's
	// verify opens a session of its own, so it goes first.
	var last lastDeploy
	if o.download {
		d.say("checking the CPU at %s against its last deploy...", address)
		if last, err = d.checkLastDeploy(ctx, address, online); err != nil {
			d.fail("%v", err)
			return 1
		}
		d.say("  %s", last.why)
		if last.outside && !o.acceptOutside {
			d.fail("refusing the download: %s. A deploy would replace what was changed. Pass --accept-outside-changes once you know what the CPU runs and mean to replace it", last.why)
			return 1
		}
	}

	// 4. The project: the one the last successful deploy saved, or a new
	// one built from the station spec.
	for _, g := range gsds {
		p := siemensSourceDir + "/gsd/" + g.Name
		if _, err := d.client.PutFile(ctx, p, g.Body); err != nil {
			return d.tiadErr("GSDML "+g.Name, err)
		}
		if _, err := d.client.InstallGSD(ctx, p); err != nil {
			return d.tiadErr("GSDML "+g.Name, err)
		}
	}
	name := siemensProjectName(m.Name)
	d.project = name
	saved := "projects/" + name + "/" + name + ".ap21"
	s, err := d.client.Open(ctx, saved)
	var kept []tiad.KeptDevice
	switch {
	case err == nil:
		d.say("opened %s as last saved", saved)
		defer d.closeSession(s)
		r, err := s.ApplyStation(ctx, st, sec)
		if err != nil {
			return d.tiadErr("station", err)
		}
		kept = r.KeptDevices
		d.changes = r.Changes
		if raw, err := d.client.GetFile(ctx, siemensStationRecord(name)); err == nil {
			want, _ := json.Marshal(st)
			same := bytes.Equal(bytes.TrimSpace(raw), want)
			d.stationSame = &same
		}
		if len(r.Changes) == 0 {
			d.say("station: no change")
		}
		for _, c := range r.Changes {
			d.say("station: %s", c)
		}
		for _, n := range r.Notes {
			d.say("station note: %s", n)
		}
	case tiad.Kind(err) == tiad.KindNotFound:
		if s, err = d.client.CreateProject(ctx, name, st, sec); err != nil {
			return d.tiadErr("create project", err)
		}
		defer d.closeSession(s)
		d.created = true
		d.say("created %s from the station spec", s.Project)
	default:
		return d.tiadErr("open "+saved, err)
	}

	// 5. The software: sources into nautilus's group, the cyclic
	// interrupt OBs made first, blocks no source made pruned.
	group := tgt.Group
	if group == "" {
		group = project.DefaultSiemensGroup
	}
	req := tiad.SoftwareRequest{Group: group, Prune: true, TagTables: []string{}}
	byWork := map[string]string{}
	for _, f := range out.Files {
		p := siemensSourceDir + "/" + name + "/" + f.Path
		if _, err := d.client.PutFile(ctx, p, f.Data); err != nil {
			return d.tiadErr("put "+f.Path, err)
		}
		req.Sources = append(req.Sources, p)
		byWork[p] = f.Path
	}
	for _, ob := range out.OBs {
		req.OBs = append(req.OBs, tiad.CyclicOB{Name: ob.Name, Number: ob.Number, CycleMS: int(ob.Cycle / time.Millisecond)})
	}
	sw, err := s.Software(ctx, req)
	failed := 0
	for _, r := range sw.Results {
		if r.OK {
			continue
		}
		failed++
		file := byWork[r.Path]
		block := strings.TrimSuffix(file, path.Ext(file))
		at := d.manifestFile
		if src, _, ok := d.out.Locate(block, 0); ok {
			at = d.where(src)
		}
		d.say("%s: error: TIA did not import %s: %s", at, file, strings.Join(r.Messages, " "))
	}
	if err != nil {
		return d.tiadErr("software import", err)
	}
	if failed > 0 {
		d.fail("%d source(s) did not import; nothing downloaded", failed)
		return 1
	}
	var blocks []string
	for _, b := range sw.Blocks {
		blocks = append(blocks, b.Name+" ["+b.Kind+"]")
	}
	d.say("blocks in group %s: %s", group, strings.Join(blocks, ", "))
	for _, ob := range sw.OBs {
		d.say("cyclic interrupt OB %s", describeOB(ob))
	}

	// 6. Compile, every message against its nautilus line.
	cr, err := s.Compile(ctx, true, true)
	if r, ok := tiad.CompileErrors(err); ok {
		cr, err = r, nil
	}
	if err != nil {
		return d.tiadErr("compile", err)
	}
	for _, msg := range cr.Messages {
		if msg.Severity == "info" {
			continue
		}
		d.compileMessage(msg)
	}
	if cr.Errors > 0 || cr.Result == "error" {
		d.fail("TIA's compile: %d error(s), %d warning(s); nothing downloaded", cr.Errors, cr.Warnings)
		return 1
	}
	d.say("compiled: %d error(s), %d warning(s)", cr.Errors, cr.Warnings)
	if !o.download {
		d.say("built only: nothing reached the CPU, and the TIA project was not saved (--download to load it)")
		return 0
	}
	return d.download(ctx, s, st, sec, kept, sw, last)
}

// notLoadedKinds are the agent's refusals that come before anything
// reaches the CPU (and that, after an acting answer, the agent turns into
// outcome_unknown itself). Any other failure of a download, a reply the
// client cannot read included, may follow a load: it is in doubt.
var notLoadedKinds = map[string]bool{
	tiad.KindBadRequest: true, tiad.KindUnauthorized: true, tiad.KindNotFound: true, tiad.KindBusy: true,
	tiad.KindGate: true, tiad.KindTargetRefused: true, tiad.KindDecisionRequired: true, tiad.KindDeclined: true,
	tiad.KindNotCompiled: true, tiad.KindDeviceChanged: true, tiad.KindDeviceUnverified: true,
	tiad.KindCPUMismatch: true, tiad.KindUncleanSession: true,
}

// loadedNothing is whether a failed download (or dry run) certainly left
// the CPU as it was.
func loadedNothing(err error) bool {
	return !tiad.OutcomeUnknown(err) && !tiad.IsFatal(err) && notLoadedKinds[tiad.Kind(err)]
}

// download is steps 7 to 10: the plan, the confirmation, the dry run on
// request and the download itself.
func (d *siemensDeployer) download(ctx context.Context, s *tiad.Session, st tiad.Station, sec tiad.Secrets,
	kept []tiad.KeptDevice, sw tiad.SoftwareResult, last lastDeploy) int {
	o := d.o
	address := st.CPU.Address
	dec, plan := d.decisions(st, kept)
	req := tiad.DownloadRequest{Address: address, Hardware: o.hardware, Software: true, Decisions: dec, Announced: true}
	if sec.Protection != "" || sec.ConfigurationPassword != "" {
		req.Secrets = &tiad.Secrets{Protection: sec.Protection, ConfigurationPassword: sec.ConfigurationPassword, FullAccessPassword: sec.FullAccessPassword}
	} else if sec.FullAccessPassword != "" {
		req.Secrets = &tiad.Secrets{FullAccessPassword: sec.FullAccessPassword}
	}
	if o.hardware {
		req.Attestation = &tiad.Attestation{By: o.by, Text: o.attest, CPUAccessControl: o.cpuAccess}
	}

	// 7. The plan, and the operator's yes: nothing has reached the CPU
	// yet but the verify's read-only compare.
	d.printPlan(address, last, sw, plan)
	if !d.confirm(ctx, address) {
		return 1
	}
	// The download's own time starts now, not while the prompt waited.
	ctx, cancel := context.WithTimeout(ctx, o.timeout)
	defer cancel()

	// 8. The dry run, on request: TIA's first check, when the agent gives
	// one. It reaches the CPU, and a TIA that asks no check loads.
	if o.dryRun {
		dry := req
		dry.DryRun = true
		dr, err := s.Download(ctx, dry)
		var qs []tiad.Question
		switch {
		case err == nil:
			qs = dr.Questions
			switch {
			case len(qs) == 0:
				d.say("the dry run: TIA asked no check")
			case dr.QuestionsIncomplete:
				d.say("the dry run (provisional; TIA shows only its first check, the answers above cover the rest):")
			default:
				d.say("the dry run (provisional):")
			}
		case !loadedNothing(err):
			return d.outcomeUnknown(ctx, s, sec, "dry run", err)
		case tiad.Kind(err) == tiad.KindBadRequest:
			// The agent's own refusal of a dry run (tiad.Openness gives
			// none until S2b): nothing reached the CPU.
			d.say("no dry run from the agent: %s", tiadMessage(err))
		case tiad.Kind(err) == tiad.KindDecisionRequired || tiad.Kind(err) == tiad.KindDeclined:
			return d.tiadErr("dry run (refused before it reached TIA; nothing was loaded)", err)
		default:
			return d.tiadErr("dry run (nothing was loaded)", err)
		}
		for _, q := range qs {
			line := "  TIA asks " + q.ID + ": " + q.Message
			if len(q.Objects) > 0 {
				line += " [" + strings.Join(q.Objects, ", ") + "]"
			}
			d.say("%s", line)
		}
		// What the dry run shows the answers refuse stops here: the
		// download would only be cancelled at it.
		for _, q := range qs {
			switch q.ID {
			case tiad.QuestionStopCPU:
				if !o.allowStop {
					d.fail("the dry run says TIA stops the CPU to load this: pass --allow-stop to let it (the CPU's outputs stop while it loads); nothing was loaded")
					return 1
				}
			case tiad.QuestionReinitialize:
				var missing []string
				for _, db := range q.Objects {
					if !slices.ContainsFunc(o.reinit, func(r string) bool { return strings.EqualFold(r, db) }) {
						missing = append(missing, db)
					}
				}
				if len(missing) > 0 {
					d.fail("the dry run says TIA reinitializes %s (values back to start values, retained ones too): pass --reinit %s to let it; nothing was loaded", strings.Join(missing, ", "), strings.Join(q.Objects, ","))
					return 1
				}
			}
		}
	}

	// 9. The download.
	if c := o.gitCommit(o.dir); c != "" {
		src := tiad.DeploySource{Commit: c, Hash: sourcesHash(d.out.Files)}
		if src.Validate() == nil {
			req.Source = &src
		}
	} else {
		d.say("the deploy record names no commit: the project is not in git, or has uncommitted changes")
	}
	d.say("downloading to %s...", address)
	r, err := s.Download(ctx, req)
	if err != nil {
		if !loadedNothing(err) {
			return d.outcomeUnknown(ctx, s, sec, "download", err)
		}
		return d.tiadErr("download (refused; nothing was loaded)", err)
	}
	if !r.Downloaded || !r.CompareEqual {
		return d.outcomeUnknown(ctx, s, sec, "download",
			fmt.Errorf("the agent answered success without saying it loaded and compared equal (downloaded=%v compareEqual=%v)", r.Downloaded, r.CompareEqual))
	}

	// 10. What landed, and the record.
	d.say("downloaded; the online compare right after is equal")
	if r.State != "" {
		d.say("CPU %s (from %s)", r.State, r.StateSource)
	}
	if r.HardwareVerified != nil && !*r.HardwareVerified {
		d.say("WARNING: the hardware configuration was loaded UNVERIFIED: %s", r.Warning)
	}
	if r.UserManagementReset != "" {
		d.say("user management reset to the project's (route %s)", r.UserManagementReset)
	}
	var missing []string
	for _, f := range d.out.Files {
		name := strings.TrimSuffix(f.Path, path.Ext(f.Path))
		if !slices.ContainsFunc(r.Blocks, func(l string) bool { return strings.EqualFold(strings.SplitN(l, " [", 2)[0], name) }) {
			missing = append(missing, name)
		}
	}
	if dep := r.Deploy; dep != nil {
		d.say("deploy record %s: archive %s", dep.ID, dep.Archive.SHA256)
		if dep.DownloadID != "" {
			d.say("  DownloadId %s", dep.DownloadID)
		}
		if dep.Source != nil {
			d.say("  source %s %s (the caller's claim)", dep.Source.Commit, dep.Source.Hash)
		}
		if !dep.Recorded {
			d.say("WARNING: the agent could not write the outcome line (%s): the record lists this deploy as in doubt", dep.RecordError)
		}
	}
	code := 0
	if p, err := s.Save(ctx, false); err != nil {
		d.fail("the CPU is loaded, but the TIA project was not saved (%v): the next deploy would build on the last saved one, so fix this first", err)
		code = 1
	} else {
		d.say("saved %s for the next deploy", p)
		if o.hardware {
			// The spec the CPU's hardware now holds: a software-only
			// load leaves the record of the last hardware load.
			raw, _ := json.Marshal(st)
			if _, err := d.client.PutFile(ctx, siemensStationRecord(d.project), raw); err != nil {
				d.say("WARNING: the station record was not written (%v): the next plan cannot say whether the spec changed", err)
			}
		}
	}
	if len(missing) > 0 {
		d.fail("VERIFY: the compare does not name %s among the blocks it saw identical", strings.Join(missing, ", "))
		return 1
	}
	return code
}

func (d *siemensDeployer) printPlan(address string, last lastDeploy, sw tiad.SoftwareResult, plan []string) {
	d.say("")
	d.say("PLAN for the CPU at %s:", address)
	d.say("  %s", last.why)
	if last.outside {
		d.say("  replacing it anyway (--accept-outside-changes)")
	}
	for _, c := range d.changes {
		d.say("  station: %s", c)
	}
	differs := ""
	switch {
	case d.created:
		d.say("  station: a new TIA project, built from the spec")
		differs = "the project is new"
	case d.stationSame == nil:
		differs = "no record of the station spec the last hardware load loaded"
	case !*d.stationSame:
		d.say("  station: the spec CHANGED since the last hardware load")
		differs = "the station spec changed since the last hardware load"
	default:
		d.say("  station: the spec is the one the last hardware load loaded")
	}
	switch {
	case d.o.hardware:
		d.say("  hardware: LOADED (--hardware), unverified")
	case differs != "":
		d.say("  hardware: NOT loaded, although it may differ from the CPU's (%s): pass --hardware with --attest-hardware to load it", differs)
	default:
		d.say("  hardware: not loaded")
	}
	if len(sw.OBs) == 0 {
		d.say("  cyclic interrupt OBs: none")
	}
	for _, ob := range sw.OBs {
		d.say("  cyclic interrupt OB %s", describeOB(ob))
	}
	if d.o.dryRun {
		d.say("  after your yes: a dry run first (--dry-run). It reaches the CPU, and a TIA that asks no check LOADS during it; what it shows the answers below refuse stops the deploy there")
	} else {
		d.say("  no dry run (--dry-run asks TIA's first check after your yes; it reaches the CPU too)")
	}
	d.say("  announced: you have told everyone who shares this CPU (the download records announced: true)")
	d.say("  the download answers:")
	for _, p := range plan {
		d.say("    - %s", p)
	}
}

// outcomeUnknown is a download that may or may not have loaded: compare
// on the same session before anything else, and say what TIA may have
// done.
func (d *siemensDeployer) outcomeUnknown(ctx context.Context, s *tiad.Session, sec tiad.Secrets, what string, err error) int {
	d.fail("%s: OUTCOME UNKNOWN: %s", what, tiadMessage(err))
	if st, src := tiad.StateOf(err); st != "" {
		d.fail("  the CPU was %s last (from %s)", st, src)
	}
	if a := tiad.ActingAnswers(err); len(a) > 0 {
		d.fail("  TIA may already have acted on: %s (check the CPU's mode and start it if it stopped)", strings.Join(a, ", "))
	}
	if id := tiad.DeployIDOf(err); id != "" {
		d.fail("  deploy record %s", id)
	}
	// Fatal from the agent ends the session; the client's own fatal kinds
	// (no reply, an unreadable one) say nothing of it.
	if d.o.inDoubt != nil {
		d.o.inDoubt()
	}
	if tiad.IsFatal(err) && tiad.Kind(err) != tiad.KindUnreachable && tiad.Kind(err) != tiad.KindBadReply && tiad.Kind(err) != tiad.KindUnauthorized {
		d.fail("  the session is gone: restart the agent, then compare in a fresh session before anything else")
		return 1
	}
	if tiad.Kind(err) == tiad.KindUnreachable {
		// The agent may still be loading: never close under it.
		d.keepSession = true
		d.fail("  session %s is left open: compare on it (POST /v1/sessions/%s/compare) before anything else, then close it", s.ID, s.ID)
	}
	// The compare gets its own time: ctx may be what ended the download.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Minute)
	defer cancel()
	var cr tiad.CompareRequest
	if sec.FullAccessPassword != "" {
		cr.Secrets = &tiad.OnlineSecrets{FullAccessPassword: sec.FullAccessPassword}
	}
	c, cerr := s.CompareWith(ctx, cr)
	switch {
	case cerr != nil:
		d.fail("  the compare after it failed too: %v", cerr)
	case c.Equal:
		d.fail("  the compare after it is EQUAL: the CPU runs this project, but the deploy is not recorded as loaded")
	default:
		var diffs []string
		for _, x := range c.Differences {
			diffs = append(diffs, x.Object+" ("+x.State+")")
		}
		d.fail("  the compare after it is DIFFERENT: %s", strings.Join(diffs, ", "))
	}
	return 1
}

func (d *siemensDeployer) closeSession(s *tiad.Session) {
	if d.keepSession {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := s.Close(ctx); err != nil {
		d.fail("closing session %s: %v (close it: DELETE /v1/sessions/%s)", s.ID, err, s.ID)
	}
}

// tiadMessage is an agent error's own message.
func tiadMessage(err error) string {
	var e *tiad.Error
	if errors.As(err, &e) {
		return e.Message
	}
	return err.Error()
}

func (d *siemensDeployer) tiadErr(what string, err error) int {
	d.fail("%s: %v", what, err)
	if q := tiad.Questions(err); len(q) > 0 {
		for _, x := range q {
			d.fail("  %s: %s %s", x.ID, x.Message, strings.Join(x.Objects, ", "))
		}
	}
	return 1
}
