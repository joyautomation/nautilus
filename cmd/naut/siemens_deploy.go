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

  (no flag)    Build and compile only. Nothing reaches the CPU (but
               --adopt upload reads it), and the TIA project is not saved
               (an adopted one stays where --adopt placed it).
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

A first deploy to a CPU that already runs a program builds on that CPU's
own TIA project: a new project built from the station spec cannot load
software only onto it (its hardware, users and devices differ), and the
access-control and configuration-protection "keep" settings need a project
to keep from. Name where it comes from; it is never guessed:
  --adopt FILE.zap21   a project archive of the CPU's project (TIA's
                       Project > Archive), usually what you have: it goes
                       to the agent, becomes this deploy's saved project,
                       and with --download is compared with the CPU before
                       anything is imported (a different one is refused);
                       its CPU must be at target.siemens.station.cpu.address
  --adopt last-deploy  the archive of the CPU's last deploy record; with
                       --download the CPU must still compare equal to it
  --adopt upload       an upload of the CPU's program (read-only, it
                       reaches the CPU even without --download);
                       --trust-unverified-certificate answers the
                       upload's TLS question trusted, blindly
--adopt is for a first deploy: once a deploy downloaded an adopted
project, later deploys build on it, and --adopt is refused. Foreign blocks
and devices stay as found.

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
	adopt                 string // "", last-deploy, upload or a .zap21 path
	trustUnverified       bool   // the upload's TLS question, trusted blindly
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
	fs.StringVar(&o.adopt, "adopt", "", "a first deploy builds on the CPU's project: a project archive (.zap21), last-deploy or upload")
	fs.BoolVar(&o.trustUnverified, "trust-unverified-certificate", false, "with --adopt upload: answer the upload's TLS question trusted, blindly")
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
	case o.download && (o.hardware || o.acceptOutside) && strings.TrimSpace(o.by) == "":
		return errors.New("the operator's name (the local user) could not be read, and an attestation or an accept of outside changes must name who gives it")
	case o.cpuAccess != "" && o.cpuAccess != tiad.AttestCPUAccessDisabled && o.cpuAccess != tiad.AttestCPUAccessEnabled:
		return fmt.Errorf("--cpu-access-control %q: want disabled or enabled, as you observed the CPU", o.cpuAccess)
	case len(o.attest) > tiad.MaxAttestationText:
		return fmt.Errorf("--attest-hardware is %d bytes, over %d", len(o.attest), tiad.MaxAttestationText)
	case o.adopt != "" && o.adopt != adoptLastDeploy && o.adopt != adoptUpload && !strings.EqualFold(filepath.Ext(o.adopt), ".zap21"):
		return fmt.Errorf("--adopt %q: want a project archive (.zap21), last-deploy or upload", o.adopt)
	case o.trustUnverified && o.adopt != adoptUpload:
		return errors.New("--trust-unverified-certificate goes with --adopt upload: no other step of a deploy trusts a certificate blindly")
	}
	return nil
}

// The --adopt sources besides a project archive.
const (
	adoptLastDeploy = "last-deploy"
	adoptUpload     = "upload"
)

// siemensAdoption is a deploy's record of the project it adopted (#1272),
// in the agent's work dir beside its sources: what the plan says of it,
// whether a download took it yet, and whether a download since changed
// its station without loading the hardware.
type siemensAdoption struct {
	Source  string `json:"source"` // last-deploy, upload or archive
	Deploy  string `json:"deploy,omitempty"`
	Archive string `json:"archive,omitempty"` // the archive's file name
	SHA256  string `json:"sha256,omitempty"`
	// Address is the CPU the project was adopted from: the project is
	// that CPU's, and a spec naming another is refused.
	Address        string `json:"address"`
	By             string `json:"by"`
	At             string `json:"at"`
	Downloaded     bool   `json:"downloaded"`
	StationChanged bool   `json:"stationChanged,omitempty"`
	// Complete is false from just before an adopt that is not atomic in
	// the agent (an upload into a new project) until it ended: a project
	// left by one that failed is never built on.
	Complete bool `json:"complete"`
}

func siemensAdoptionRecord(name string) string {
	return siemensSourceDir + "/" + name + "/adopted.json"
}

// describe says where the adopted project came from.
func (a siemensAdoption) describe() string {
	switch a.Source {
	case adoptLastDeploy:
		return "the archive of deploy " + a.Deploy
	case adoptUpload:
		return "an upload of the CPU"
	}
	return "the project archive " + a.Archive
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
	// adoption is the record of the project this deploy builds on when
	// it was adopted (this deploy or an earlier one), nil otherwise.
	adoption *siemensAdoption
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
	project    string // the record's project, as the agent named its session
	verdict    string // tiad.Verify*, "" without a record
	outside    bool   // the CPU changed outside nautilus, or the record cannot say
	why        string
	// accept is what a download with --accept-outside-changes journals:
	// tiad.OutsideDifferent, OutsideBroken, OutsideOtherProject or
	// OutsideUnreadable.
	accept string
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
			return lastDeploy{outside: true, accept: tiad.OutsideUnreadable, why: fmt.Sprintf("no readable deploy record for this CPU, and %d journal line(s) the agent cannot read", list.UnreadableLines)}, nil
		}
		return lastDeploy{why: why}, nil
	}
	ld := lastDeploy{id: latest.ID, status: latest.Status, project: latest.Project}
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
		ld.outside, ld.accept, ld.why = true, tiad.OutsideBroken, fmt.Sprintf("last deploy %s: its archive is broken (%s), so nothing says what the CPU runs", latest.ID, r.Broken)
	default:
		var diffs []string
		for _, x := range r.Differences {
			diffs = append(diffs, x.Object+" ("+x.State+")")
		}
		ld.accept = tiad.OutsideDifferent
		if latest.Status == tiad.DeployLoaded {
			ld.outside, ld.why = true, fmt.Sprintf("last deploy %s (loaded): the CPU compares DIFFERENT, so it was changed outside nautilus: %s", latest.ID, strings.Join(diffs, ", "))
		} else {
			ld.outside, ld.why = true, fmt.Sprintf("last deploy %s is IN DOUBT and the CPU compares DIFFERENT from it: it may hold that unfinished load or a change made outside nautilus: %s", latest.ID, strings.Join(diffs, ", "))
		}
	}
	return ld, nil
}

// readAdoption reads the deploy's adoption record, nil without one.
func (d *siemensDeployer) readAdoption(ctx context.Context) (*siemensAdoption, error) {
	raw, err := d.client.GetFile(ctx, siemensAdoptionRecord(d.project))
	if tiad.Kind(err) == tiad.KindNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var a siemensAdoption
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, fmt.Errorf("%s is unreadable (%v): remove it, and the project it names, before deploying", siemensAdoptionRecord(d.project), err)
	}
	return &a, nil
}

// adopt places the CPU's own project as this deploy's saved project, from
// the source --adopt names, and records where it came from. replace is set
// for an adoption no download took yet.
func (d *siemensDeployer) adopt(ctx context.Context, name string, st tiad.Station, sec tiad.Secrets, online *tiad.OnlineSecrets, last lastDeploy, replace bool) int {
	o := d.o
	rec := siemensAdoption{By: o.by, At: time.Now().UTC().Format(time.RFC3339), Address: st.CPU.Address}
	switch o.adopt {
	case adoptLastDeploy, adoptUpload:
		rec.Source = o.adopt
	default:
		rec.Source, rec.Archive = "archive", filepath.Base(o.adopt)
	}
	// Until the adopt ended, the project it may leave is marked
	// unfinished (Complete false): nothing builds on it uncompared, even
	// when naut dies between the agent's adopt and the record.
	pending, _ := json.Marshal(rec)
	if _, err := d.client.PutFile(ctx, siemensAdoptionRecord(name), pending); err != nil {
		return d.tiadErr("the adoption record", err)
	}
	// The station record speaks of a hardware load of another project.
	if err := d.client.DeleteFile(ctx, siemensStationRecord(name)); err != nil && tiad.Kind(err) != tiad.KindNotFound {
		return d.tiadErr("the station record", err)
	}
	var res tiad.AdoptResult
	var err error
	switch o.adopt {
	case adoptLastDeploy:
		id := last.id
		switch {
		case o.download && id == "":
			d.fail("--adopt last-deploy: %s, so there is no archive to adopt; adopt the CPU's project archive (--adopt FILE.zap21) or an upload (--adopt upload)", last.why)
			return 1
		case o.download && last.verdict != tiad.VerifyEqual:
			d.fail("--adopt last-deploy: the CPU does not compare equal to its last deploy %s, so that archive is not what it runs; adopt the CPU's project archive (--adopt FILE.zap21) or an upload (--adopt upload)", id)
			return 1
		case !o.download:
			list, err := d.client.Deploys(ctx, st.CPU.Address)
			if err != nil {
				return d.tiadErr("deploys", err)
			}
			for _, x := range list.Deploys {
				if x.Status != tiad.DeployNotLoaded {
					id = x.ID
					break
				}
			}
			if id == "" {
				d.fail("--adopt last-deploy: no deploy record for the CPU at %s, so there is no archive to adopt", st.CPU.Address)
				return 1
			}
			d.say("adopting deploy %s's archive, not checked against the CPU (built only: a --download compares it first)", id)
		}
		rec.Deploy = id
		res, err = d.client.AdoptProject(ctx, tiad.AdoptRequest{Name: name, Deploy: id, Replace: replace, Address: st.CPU.Address})
		if err != nil {
			return d.tiadErr("adopt the archive of deploy "+id, err)
		}
	case adoptUpload:
		d.say("uploading the CPU's program from %s (read-only: it reaches the CPU)...", st.CPU.Address)
		// The upload replaces the project's station with the CPU's: the
		// project it goes into holds the spec's CPU only, with settings a
		// new CPU takes without secrets (keep would have nothing to keep).
		host := tiad.Station{CPU: st.CPU}
		host.CPU.ConfigurationProtection = tiad.ProtectNone
		host.CPU.AccessControl = tiad.AccessKeep
		up, err := d.client.CreateProject(ctx, name, host, tiad.Secrets{})
		if err != nil {
			return d.tiadErr("the project to upload into", err)
		}
		req := tiad.UploadRequest{TrustUnverifiedCertificate: o.trustUnverified}
		if !o.trustUnverified {
			req.Secrets = online
		}
		ur, err := up.UploadWith(ctx, req)
		if err == nil {
			_, err = up.Save(ctx, false)
		}
		d.closeSession(up)
		if err != nil {
			return d.tiadErr("upload", err)
		}
		for _, q := range ur.TrustedBlindly {
			d.say("  trusted blindly (--trust-unverified-certificate): %s", q)
		}
		d.say("  CPU %s %s %s (device pin: %s)", ur.CPU.Name, ur.Device.Order, ur.Device.Firmware, ur.Pin)
		cpu := ur.CPU
		res = tiad.AdoptResult{Project: "projects/" + name + "/" + name + ".ap21", CPU: &cpu, CPUError: ur.CPUError, CPUAddress: st.CPU.Address}
	default:
		b, err := os.ReadFile(o.adopt)
		if err != nil {
			d.fail("--adopt: %v", err)
			return 2
		}
		wp := siemensSourceDir + "/" + name + "/adopt.zap21"
		if _, err := d.client.PutFile(ctx, wp, b); err != nil {
			return d.tiadErr("put "+rec.Archive, err)
		}
		res, err = d.client.AdoptProject(ctx, tiad.AdoptRequest{Name: name, Archive: wp, Replace: replace, Address: st.CPU.Address})
		if derr := d.client.DeleteFile(ctx, wp); derr != nil && err == nil {
			d.say("WARNING: the copy of the archive was not removed from the agent (%v)", derr)
		}
		if err != nil {
			return d.tiadErr("adopt "+rec.Archive, err)
		}
	}
	if res.CPU == nil || res.CPU.Name == "" || res.CPUAddress != st.CPU.Address {
		d.fail("the adopted project's CPU could not be read, or is not at %s (%s%s): nothing builds on it; adopt the CPU's own project", st.CPU.Address, res.CPUAddress, res.CPUError)
		return 1
	}
	rec.SHA256 = res.Source.SHA256
	rec.Complete = true
	d.say("adopted %s as %s", rec.describe(), res.Project)
	d.say("  its CPU: %s %s %s at %s", res.CPU.Name, res.CPU.Order, res.CPU.Firmware, res.CPUAddress)
	if names := keptDeviceNames(res.KeptDevices); len(names) > 0 {
		d.say("  its devices besides the CPU, kept as found: %s", strings.Join(names, ", "))
	}
	raw, _ := json.Marshal(rec)
	if _, err := d.client.PutFile(ctx, siemensAdoptionRecord(name), raw); err != nil {
		return d.tiadErr("the adoption record", err)
	}
	d.adoption = &rec
	return 0
}

// compareAdopted refuses a download that would build on an adopted
// project no download took yet that does not compare equal with the CPU,
// as adopted: an archive of another state, an upload or a record the CPU
// moved away from since.
func (d *siemensDeployer) compareAdopted(ctx context.Context, s *tiad.Session, address string, online *tiad.OnlineSecrets) int {
	if a := d.adoption; a != nil {
		d.say("comparing the adopted project with the CPU before anything is imported...")
		c, err := s.CompareWith(ctx, tiad.CompareRequest{Secrets: online})
		if err != nil {
			return d.tiadErr("compare the adopted project with the CPU", err)
		}
		// Compared with the deploy's CPU, never only with the CPU the
		// project names: an archive of another CPU equals that one.
		if c.Address != address {
			d.fail("refusing the download: the adopted project (%s) has its CPU at %q, not at %s, the deploy's CPU: it is another CPU's project, or one from before the address changed; nothing was imported or downloaded", a.describe(), c.Address, address)
			return 1
		}
		if !c.Equal {
			var diffs []string
			for _, x := range c.Differences {
				diffs = append(diffs, x.Object+" ("+x.State+")")
			}
			d.fail("refusing the download: the adopted project (%s) is not what the CPU runs: %s. A deploy built on it would put that back on the CPU. Adopt what it runs: --adopt upload, or a current project archive; nothing was imported or downloaded", a.describe(), strings.Join(diffs, ", "))
			return 1
		}
		d.say("  equal: the adopted project is what the CPU runs")
	}
	return 0
}

// checkLastDeployIsThisProject refuses a download onto a CPU whose last
// deploy loaded another project than this deploy's saved one (a restore,
// another deploy): the CPU runs that, a change outside this deploy's
// project, accepted only with --accept-outside-changes.
func (d *siemensDeployer) checkLastDeployIsThisProject(saved string, last *lastDeploy) int {
	if last.id == "" || last.project == "" || strings.EqualFold(last.project, saved) || last.outside {
		return 0
	}
	last.outside, last.accept = true, tiad.OutsideOtherProject
	last.why = fmt.Sprintf("the CPU's last deploy %s loaded another project (%s), not this deploy's saved one (%s): the CPU runs what that deploy loaded, not what this project was last downloaded as", last.id, last.project, saved)
	if !d.o.acceptOutside {
		d.fail("refusing the download: %s. A deploy would replace it. Pass --accept-outside-changes once you know what the CPU runs and mean to replace it", last.why)
		return 1
	}
	d.say("  %s", last.why)
	return 0
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
		if last.outside && o.adopt == adoptLastDeploy {
			d.fail("--adopt last-deploy: %s, so that archive is not what the CPU runs; adopt the CPU's project archive (--adopt FILE.zap21) or an upload (--adopt upload)", last.why)
			return 1
		}
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
	if err == nil {
		if d.adoption, err = d.readAdoption(ctx); err != nil {
			d.closeSession(s)
			return d.tiadErr("the adoption record", err)
		}
		// An adopted project is its CPU's: a spec naming another CPU (a
		// copy of the project for a like machine, the name unchanged)
		// would build on it, foreign blocks and all.
		if a := d.adoption; a != nil && a.Address != "" && a.Address != st.CPU.Address && (o.adopt == "" || a.Downloaded) {
			d.closeSession(s)
			d.fail("this deploy's saved project %s was adopted from the CPU at %s, and target.siemens.station.cpu.address is %s: it is that CPU's project. Give this CPU's deploy its own name (name: in %s) and adopt this CPU's project", saved, a.Address, st.CPU.Address, project.ManifestName)
			return 1
		}
		if d.adoption != nil && !d.adoption.Complete && o.adopt == "" {
			d.closeSession(s)
			d.fail("the last --adopt (%s) did not finish, so %s is no project to build on: run the deploy with --adopt again", d.adoption.describe(), saved)
			return 1
		}
		if o.adopt != "" {
			d.closeSession(s)
			if d.adoption == nil || d.adoption.Downloaded {
				d.fail("tiad holds this deploy's saved project already (%s): --adopt makes a deploy's first project, and nothing was adopted. Deploy without --adopt to build on it", saved)
				return 1
			}
			// An adoption no download took yet is replaced by the one
			// asked for now.
			d.say("replacing the adopted project (from %s), which no download took yet", d.adoption.describe())
			if code := d.adopt(ctx, name, st, sec, online, last, true); code != 0 {
				return code
			}
			s, err = d.client.Open(ctx, saved)
		}
	} else if tiad.Kind(err) == tiad.KindNotFound && o.adopt != "" {
		if code := d.adopt(ctx, name, st, sec, online, last, false); code != 0 {
			return code
		}
		s, err = d.client.Open(ctx, saved)
	}
	var kept []tiad.KeptDevice
	switch {
	case err == nil:
		defer d.closeSession(s)
		if d.adoption != nil && !d.adoption.Downloaded {
			d.say("opened %s, adopted from %s (no download took it yet)", saved, d.adoption.describe())
		} else {
			d.say("opened %s as last saved", saved)
		}
		// An adopted project no download took yet is compared with the
		// CPU as adopted, before the spec touches it.
		if o.download && d.adoption != nil && !d.adoption.Downloaded {
			if code := d.compareAdopted(ctx, s, st.CPU.Address, online); code != 0 {
				return code
			}
		}
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
		if o.download && (d.adoption == nil || d.adoption.Downloaded) {
			if code := d.checkLastDeployIsThisProject(saved, &last); code != 0 {
				return code
			}
		}
	case tiad.Kind(err) == tiad.KindNotFound:
		// A new project, built from the spec: nothing to keep from, and
		// it cannot load software only onto a CPU that runs one.
		var keeps []string
		if st.CPU.AccessControl == tiad.AccessKeep {
			keeps = append(keeps, "access-control")
		}
		if st.CPU.ConfigurationProtection == tiad.ProtectKeep {
			keeps = append(keeps, "configuration-protection")
		}
		if len(keeps) > 0 {
			d.fail("target.siemens.station.cpu %s: keep keeps what a project holds, and tiad has no project for this deploy yet, so a new one would have nothing to keep (TIA's compile refuses it). Adopt the CPU's project first: --adopt <its project archive .zap21>, --adopt last-deploy or --adopt upload", strings.Join(keeps, ", "))
			return 1
		}
		if o.download && !o.hardware {
			d.fail("refusing the download: this deploy would build a NEW TIA project from the station spec, and a new project cannot load software only onto a CPU that already runs a configuration (its hardware, users and devices differ). Build on the CPU's own project instead (--adopt <its project archive .zap21>, --adopt last-deploy or --adopt upload), or load the hardware too (--hardware --attest-hardware \"...\" --cpu-access-control ...), which replaces the CPU's configuration and users")
			return 1
		}
		if err := d.client.DeleteFile(ctx, siemensAdoptionRecord(name)); err != nil && tiad.Kind(err) != tiad.KindNotFound {
			return d.tiadErr("the adoption record", err)
		}
		if s, err = d.client.CreateProject(ctx, name, st, sec); err != nil {
			return d.tiadErr("create project", err)
		}
		defer d.closeSession(s)
		d.created = true
		d.say("created %s from the station spec", s.Project)
		if !o.download {
			d.say("note: a new project downloads only with --hardware (it cannot load software only onto a CPU that runs a configuration); to build on the CPU's own project, --adopt it")
		}
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
		switch {
		case o.adopt == adoptUpload:
			d.say("built only: the upload read the CPU, nothing was downloaded, and the build was not saved; the adopted project stays as adopted (--download to load it)")
		case o.adopt != "":
			d.say("built only: nothing reached the CPU, and the build was not saved; the adopted project stays as adopted (--download to load it)")
		default:
			d.say("built only: nothing reached the CPU, and the TIA project was not saved (--download to load it)")
		}
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
	if last.outside {
		// Only reached with --accept-outside-changes: the overwrite of an
		// outside change is journalled with the deploy (#789).
		req.OutsideChanges = &tiad.OutsideChanges{By: o.by, Verdict: last.accept, Deploy: last.id}
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
		if oc := dep.OutsideChanges; oc != nil {
			d.say("  outside change replaced, accepted by %s (verdict %s, deploy %s)", oc.By, oc.Verdict, oc.Deploy)
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
		if a := d.adoption; a != nil {
			// Taken by a download now; a station change loaded without
			// its hardware leaves the CPU's own station behind.
			a.Downloaded = true
			a.StationChanged = a.StationChanged || (len(d.changes) > 0 && !o.hardware)
			raw, _ := json.Marshal(a)
			if _, err := d.client.PutFile(ctx, siemensAdoptionRecord(d.project), raw); err != nil {
				d.say("WARNING: the adoption record was not updated (%v): the next deploy compares the project with the CPU again, and its plan cannot tell that this download changed the station", err)
			}
		}
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
		d.say("  replacing it anyway (--accept-outside-changes): the deploy record journals your accept, by %s", d.o.by)
	}
	for _, c := range d.changes {
		d.say("  station: %s", c)
	}
	if a := d.adoption; a != nil {
		if a.Downloaded {
			d.say("  project: adopted from %s by %s at %s", a.describe(), a.By, a.At)
		} else {
			d.say("  project: adopted from %s by %s at %s, compared equal with the CPU above; this download is its first", a.describe(), a.By, a.At)
		}
	}
	differs := ""
	switch {
	case d.created:
		d.say("  station: a new TIA project, built from the spec")
		differs = "the project is new"
	case d.stationSame != nil && !*d.stationSame:
		d.say("  station: the spec CHANGED since the last hardware load")
		differs = "the station spec changed since the last hardware load"
	case len(d.changes) > 0:
		d.say("  station: this deploy CHANGES the project's station (above)")
		differs = "this deploy changes the project's station"
	case d.stationSame != nil:
		d.say("  station: the spec is the one the last hardware load loaded")
	case d.adoption != nil && !d.adoption.StationChanged && len(d.changes) == 0 && d.adoption.Source == adoptUpload:
		d.say("  station: as uploaded from the CPU: the spec changes nothing in it")
	case d.adoption != nil && !d.adoption.StationChanged && len(d.changes) == 0:
		d.say("  station: as adopted: the spec changes nothing in the adopted project's station, which is taken as the CPU's (the compare covers the software: tiad cannot compare hardware or users online)")
	default:
		differs = "no record of the station spec the last hardware load loaded"
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
