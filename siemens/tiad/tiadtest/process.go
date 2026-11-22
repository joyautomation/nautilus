package tiadtest

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/siemens/tiad"
)

// Process is an agent binary running for one test: tiad-fake under Mono,
// or the real agent on a TIA machine. It listens on a loopback port of
// its own choosing, works in its own temporary work dir, and exits when
// the test ends.
type Process struct {
	URL     string
	WorkDir string
	// PinsDir is the agent's own pins directory (TIAD_PINS_DIR).
	PinsDir string
	// DeploysDir is the agent's deploy record (TIAD_DEPLOYS_DIR).
	DeploysDir string

	client *tiad.Client
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	done   chan struct{}

	mu     sync.Mutex
	log    bytes.Buffer
	script Script
}

// Script is what tiad-fake's ITia does (PUT /fake/script): what compile
// returns, and calls that are slow or that fail the way TIA fails.
type Script struct {
	CompileResult *tiad.CompileResult `json:"compileResult,omitempty"`
	Questions     []tiad.Question     `json:"questions,omitempty"`
	// LoadDespiteCancel makes the fake TIA load even when the agent
	// cancels: a misbehaving TIA, to prove the agent reports it.
	LoadDespiteCancel bool `json:"loadDespiteCancel,omitempty"`
	// AskFirst is how many times the fake's TIA asks its first check
	// (nil: once, as TIA does; 0 asks nothing at all and loads; 2 asks
	// the same configuration twice).
	AskFirst *int `json:"askFirst,omitempty"`
	// FailAfterDecide makes the fake fail the way TIA fails once its
	// checks were asked, or after a cancel, with its own exception that
	// carries no decline.
	FailAfterDecide string `json:"failAfterDecide,omitempty"`
	// FailInDoubt makes the fake fail the way TIA fails inside a
	// download it had begun, before asking anything.
	FailInDoubt string `json:"failInDoubt,omitempty"`
	// DownloadThrows makes the fake's download fail with an exception that
	// is not TIA's, before it asks anything: an internal error of tiad's.
	// With TiaDies set too, TIA is gone when it throws.
	DownloadThrows string `json:"downloadThrows,omitempty"`
	// TiaDies makes TIA die inside a download, as it did in #789 stage
	// 2, taking its objects with it so that detaching tiad's handlers
	// fails too: "preparing" before TIA's download call, "downloading"
	// inside it, before anything is asked.
	TiaDies string `json:"tiaDies,omitempty"`
	// SeamTrustBeforeDownload and SeamReserveBeforeRetire turn on tiad-fake's
	// test seams (tools/tiad/core/TestSeams.cs, never in a release build):
	// a blind trust recorded between a download's check and its dispatch,
	// and a session reserved between a close and its retire check.
	SeamTrustBeforeDownload bool `json:"seamTrustBeforeDownload,omitempty"`
	SeamReserveBeforeRetire bool `json:"seamReserveBeforeRetire,omitempty"`
	// ArchiveSame makes every archive for a deploy record the same bytes,
	// so two deploys share one content address.
	ArchiveSame bool `json:"archiveSame,omitempty"`
	// NoDryRun makes the fake's TIA side one that cannot promise a dry
	// run loads nothing (tiad.Openness until S2b): the agent refuses dry
	// runs.
	NoDryRun bool `json:"noDryRun,omitempty"`
	// CleanCancel makes the fake's TIA side answer each check itself,
	// without the decider, and report a clean cancel (nothing loaded,
	// nothing thrown) at the first unanswered one, even after acting
	// answers: a TIA side breaking the ITia contract.
	CleanCancel bool `json:"cleanCancel,omitempty"`
	// CompareDiffers makes every compare differ, even right after a
	// download: a CPU that did not take what was sent.
	CompareDiffers bool `json:"compareDiffers,omitempty"`
	// CompareTree, when set, is the compare tree TIA reports whatever
	// the CPU holds (#789): "empty" (an identical root, nothing under
	// it), "emptyFolders" (an identical root over empty folders only),
	// "folderDiffers" (a folder says its contents differ but shows no
	// differing child), "irrelevantRoot" (a CompareIrrelevant root over
	// an identical block), "dropBlock" (an otherwise identical tree that
	// leaves out the project's last program block) or "noBlocks" (the
	// project's blocks could not be read). None of them may pass as
	// equal.
	CompareTree string `json:"compareTree,omitempty"`
	// DownloadState is the state TIA's download result reports when it
	// ran ("" = Success; "Error" = a failed load).
	DownloadState string `json:"downloadState,omitempty"`
	// UploadAsksTLS makes going online for an upload ask TLS trust, as a
	// CPU with secure PG/PC communication and no known certificate does.
	UploadAsksTLS bool `json:"uploadAsksTls,omitempty"`
	// FingerprintAsksTLS makes a fingerprint read ask TLS trust, as the
	// bench CPU's does, until the fake's TIA trusted once.
	FingerprintAsksTLS bool `json:"fingerprintAsksTls,omitempty"`
	// FingerprintItems, when set, is what every fingerprint read returns;
	// FingerprintNoItems makes it return none.
	FingerprintItems   []tiad.FingerprintItem `json:"fingerprintItems,omitempty"`
	FingerprintNoItems bool                   `json:"fingerprintNoItems,omitempty"`
	// CannotApply is what the TIA side says it cannot apply yet (as
	// tiad.Openness says of rack modules), refusing every apply before
	// any write.
	CannotApply string `json:"cannotApply,omitempty"`
	// Device is the CPU's identity (nil: BenchDevice), as an upload and
	// an online read report it.
	Device *tiad.DeviceIdentity `json:"device,omitempty"`
	// NoIdentify makes the fake's TIA side one that cannot read a CPU's
	// identity online, as tiad.Openness cannot yet.
	NoIdentify bool `json:"noIdentify,omitempty"`
	// PasswordPolicy is the projects' password policy for local users
	// (nil: TIA's default), UploadAccessControl the access control an
	// upload brings ("": Disabled, as the S2a upload found the bench CPU).
	PasswordPolicy      *PasswordPolicy `json:"passwordPolicy,omitempty"`
	UploadAccessControl string          `json:"uploadAccessControl,omitempty"`
	// UploadWebServer makes an upload bring a CPU whose web server is
	// on (the bench CPU's is off).
	UploadWebServer bool `json:"uploadWebServer,omitempty"`
	// UploadAnonymous makes an upload bring an active anonymous user.
	UploadAnonymous bool           `json:"uploadAnonymous,omitempty"`
	AccessRecords   string         `json:"accessRecords,omitempty"`
	Delays          []ScriptedCall `json:"delays,omitempty"`
	Failures        []ScriptedCall `json:"failures,omitempty"`
}

// ScriptedCall names an ITia call ("Compile", "OpenProject", ...).
type ScriptedCall struct {
	Call    string `json:"call"`
	MS      int    `json:"ms,omitempty"`
	Message string `json:"message,omitempty"`
}

// TiaCall is one ITia call tiad-fake made, and the thread it ran on.
// "start" is when the ITia was made: when TIA would start.
type TiaCall struct {
	Call   string `json:"call"`
	Thread int    `json:"thread"`
	STA    bool   `json:"sta"`
}

// Command is how bin runs here: a .exe off Windows runs under Mono
// (TIAD_MONO, default "mono").
func Command(bin string, args ...string) *exec.Cmd {
	if strings.HasSuffix(strings.ToLower(bin), ".exe") && runtime.GOOS != "windows" {
		mono := os.Getenv("TIAD_MONO")
		if mono == "" {
			mono = "mono"
		}
		return exec.Command(mono, append([]string{"--debug", bin}, args...)...)
	}
	return exec.Command(bin, args...)
}

// AgentEnv is the process environment for an agent: this process's own,
// without any TIAD_ variable, then env (later entries win).
func AgentEnv(env ...string) []string {
	var out []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "TIAD_") {
			out = append(out, kv)
		}
	}
	return append(out, env...)
}

// syncBuffer is a bytes.Buffer behind a Process's lock.
type logWriter struct{ p *Process }

func (w logWriter) Write(b []byte) (int, error) {
	w.p.mu.Lock()
	defer w.p.mu.Unlock()
	return w.p.log.Write(b)
}

// StartProcess starts bin with cfg and waits until it listens. env adds
// to, or overrides, the agent's environment.
func StartProcess(t testing.TB, bin string, cfg Config, env ...string) *Process {
	t.Helper()
	if cfg.Token == "" {
		cfg.Token = RandomToken()
	}
	p := &Process{WorkDir: t.TempDir(), PinsDir: t.TempDir(), DeploysDir: t.TempDir(), done: make(chan struct{})}
	p.cmd = Command(bin)
	p.cmd.Env = AgentEnv(append([]string{
		"TIAD_ADDR=http://127.0.0.1:0",
		"TIAD_WORKDIR=" + p.WorkDir,
		// Never the machine's own pins: a test's pins live and die with it.
		"TIAD_PINS_DIR=" + p.PinsDir,
		// Nor the machine's own deploy record.
		"TIAD_DEPLOYS_DIR=" + p.DeploysDir,
		"TIAD_TOKEN=" + cfg.Token,
		"TIAD_TARGETS=" + strings.Join(cfg.Targets, ","),
		// The real agent exits when its stdin closes only when asked:
		// under a scheduled task stdin may be closed from the start.
		"TIAD_EXIT_ON_STDIN=1",
	}, env...)...)
	p.cmd.Stderr = logWriter{p}
	stdout, err := p.cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if p.stdin, err = p.cmd.StdinPipe(); err != nil {
		t.Fatal(err)
	}
	if err := p.cmd.Start(); err != nil {
		t.Fatalf("starting %s: %v", bin, err)
	}
	ready := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			line := sc.Text()
			if u, ok := strings.CutPrefix(line, "tiad: listening on "); ok {
				select {
				case ready <- u:
				default:
				}
				continue
			}
			fmt.Fprintln(logWriter{p}, line)
		}
	}()
	go func() {
		_ = p.cmd.Wait()
		close(p.done)
	}()
	t.Cleanup(p.Stop)

	select {
	case p.URL = <-ready:
	case <-p.done:
		t.Fatalf("%s exited before listening:\n%s", bin, p.Log())
	case <-time.After(60 * time.Second):
		t.Fatalf("%s did not listen within 60s:\n%s", bin, p.Log())
	}
	p.client = tiad.New(p.URL, cfg.Token)
	p.client.Token = cfg.Token
	return p
}

// CloseStdin closes the agent's standard input, which ends it.
func (p *Process) CloseStdin() { _ = p.stdin.Close() }

// Stop closes the agent's standard input and waits for it to exit; after
// 10 s it kills it.
func (p *Process) Stop() {
	p.CloseStdin()
	select {
	case <-p.done:
	case <-time.After(10 * time.Second):
		_ = p.cmd.Process.Kill()
		<-p.done
	}
}

// Exited reports whether the process has ended, waiting up to d.
func (p *Process) Exited(d time.Duration) bool {
	select {
	case <-p.done:
		return true
	default:
	}
	select {
	case <-p.done:
		return true
	case <-time.After(d):
		return false
	}
}

// Log is what the agent wrote to stderr (and to stdout apart from its
// ready line) so far.
func (p *Process) Log() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.log.String()
}

// Client talks to the agent with its token.
func (p *Process) Client() *tiad.Client { return p.client }

// SetGates scripts tiad-fake's machine gates (PUT /fake/gates).
func (p *Process) SetGates(t testing.TB, gates []tiad.Gate) {
	t.Helper()
	p.control(t, http.MethodPut, "/fake/gates", map[string]any{"gates": gates})
}

// SetCompileResult scripts what compile returns.
func (p *Process) SetCompileResult(t testing.TB, r tiad.CompileResult) {
	t.Helper()
	p.mu.Lock()
	p.script.CompileResult = &r
	s := p.script
	p.mu.Unlock()
	p.SetScript(t, s)
}

// SetQuestions scripts TIA's pre-download checks for every download.
func (p *Process) SetQuestions(t testing.TB, qs []tiad.Question) {
	t.Helper()
	p.mu.Lock()
	p.script.Questions = qs
	s := p.script
	p.mu.Unlock()
	p.SetScript(t, s)
}

// SetDevice scripts the CPU's identity and whether it can be read online.
func (p *Process) SetDevice(t testing.TB, d tiad.DeviceIdentity, noIdentify bool) {
	t.Helper()
	p.mu.Lock()
	p.script.Device = &d
	p.script.NoIdentify = noIdentify
	s := p.script
	p.mu.Unlock()
	p.SetScript(t, s)
}

// SetAccess scripts the projects' password policy for local users and the
// access control an upload brings.
func (p *Process) SetAccess(t testing.TB, a AccessScript) {
	t.Helper()
	p.mu.Lock()
	p.script.PasswordPolicy = a.Policy
	p.script.UploadAccessControl = a.UploadAccessControl
	p.script.UploadWebServer = a.UploadWebServer
	p.script.UploadAnonymous = a.UploadAnonymous
	p.script.AccessRecords = a.Records
	s := p.script
	p.mu.Unlock()
	p.SetScript(t, s)
}

// SetFingerprint scripts the fingerprint reads; Fails is a scripted
// failure of the ReadFingerprint call.
func (p *Process) SetFingerprint(t testing.TB, fs FingerprintScript) {
	t.Helper()
	p.mu.Lock()
	p.script.FingerprintAsksTLS = fs.AsksTLS
	p.script.FingerprintItems = fs.Items
	p.script.FingerprintNoItems = fs.NoItems
	var failures []ScriptedCall
	for _, f := range p.script.Failures {
		if f.Call != "ReadFingerprint" {
			failures = append(failures, f)
		}
	}
	if fs.Fails != "" {
		failures = append(failures, ScriptedCall{Call: "ReadFingerprint", Message: fs.Fails})
	}
	p.script.Failures = failures
	s := p.script
	p.mu.Unlock()
	p.SetScript(t, s)
}

// SetArchiveFails makes every archive for a deploy record fail inside
// TIA's side with msg ("" archives again).
func (p *Process) SetArchiveFails(t testing.TB, msg string) {
	t.Helper()
	p.mu.Lock()
	var failures []ScriptedCall
	for _, f := range p.script.Failures {
		if f.Call != "Archive" {
			failures = append(failures, f)
		}
	}
	if msg != "" {
		failures = append(failures, ScriptedCall{Call: "Archive", Message: msg})
	}
	p.script.Failures = failures
	s := p.script
	p.mu.Unlock()
	p.SetScript(t, s)
}

// Loaded lists every download tiad-fake loaded.
func (p *Process) Loaded(t testing.TB) []Loaded {
	t.Helper()
	var out struct {
		Downloads []struct {
			Address            string `json:"address"`
			Hardware, Software bool
			State              string `json:"state"`
		} `json:"downloads"`
	}
	p.controlInto(t, http.MethodGet, "/fake/downloads", nil, &out)
	var l []Loaded
	for _, d := range out.Downloads {
		l = append(l, Loaded{Address: d.Address, Hardware: d.Hardware, Software: d.Software, State: d.State})
	}
	return l
}

// SetScript replaces tiad-fake's whole script.
func (p *Process) SetScript(t testing.TB, s Script) {
	t.Helper()
	p.mu.Lock()
	p.script = s
	p.mu.Unlock()
	p.control(t, http.MethodPut, "/fake/script", s)
}

// Machine is a scripted machine for tiad-fake (PUT /fake/machine): probe
// then runs the agent's real gate evaluator over it instead of reporting
// scripted gates. Registry keys are under HKLM, 64-bit view.
type Machine struct {
	NoTIA    bool       `json:"noTia,omitempty"`
	Registry []RegValue `json:"registry,omitempty"`
	SubKeys  []RegKey   `json:"subkeys,omitempty"`
	Files    []string   `json:"files,omitempty"`
	ExePath  string     `json:"exePath"`
	// ExeModified is the executable's last write time, sent in UTC with
	// 100 ns precision, as Windows keeps it.
	ExeModified time.Time `json:"-"`
	ExeSHA256   []byte    `json:"exeSha256"`
	// Group is "member", "not-in-token" or "missing".
	Group string `json:"group"`
}

// RegValue is one registry string value.
type RegValue struct {
	Key   string `json:"key"`
	Name  string `json:"name"`
	Value string `json:"value"`
}

// RegKey is a registry key and its subkey names.
type RegKey struct {
	Key   string   `json:"key"`
	Names []string `json:"names"`
}

// MarshalJSON adds exeModified in .NET's round-trip form.
func (m Machine) MarshalJSON() ([]byte, error) {
	type plain Machine
	return json.Marshal(struct {
		plain
		ExeModified string `json:"exeModified"`
	}{plain(m), m.ExeModified.UTC().Format("2006-01-02T15:04:05.0000000Z")})
}

// SetMachine scripts tiad-fake's machine (PUT /fake/machine).
func (p *Process) SetMachine(t testing.TB, m Machine) {
	t.Helper()
	p.control(t, http.MethodPut, "/fake/machine", m)
}

// Calls lists every ITia call tiad-fake made so far.
func (p *Process) Calls(t testing.TB) []TiaCall {
	t.Helper()
	var out struct {
		Calls []TiaCall `json:"calls"`
	}
	p.controlInto(t, http.MethodGet, "/fake/calls", nil, &out)
	return out.Calls
}

// LoadOutside is the Target's: POST /fake/load.
func (p *Process) LoadOutside(t testing.TB, address, file string) {
	t.Helper()
	body := map[string]string{"address": address, "archive": ""}
	if file != "" {
		body["archive"] = filepath.Join(p.DeploysDir, filepath.FromSlash(file))
	}
	p.control(t, http.MethodPost, "/fake/load", body)
}

// SwapArchive is the Target's: the file in TIAD_DEPLOYS_DIR rewritten.
func (p *Process) SwapArchive(t testing.TB, file, with string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(p.DeploysDir, filepath.FromSlash(with)))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p.DeploysDir, filepath.FromSlash(file)), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// SetDeployAddress is the Target's: the intent line of deploy id in the
// journal rewritten with another address.
func (p *Process) SetDeployAddress(t testing.TB, id, address string) {
	t.Helper()
	path := filepath.Join(p.DeploysDir, "deploys.jsonl")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(b), "\n")
	found := false
	for i, l := range lines {
		if l == "" {
			continue
		}
		var o map[string]any
		if err := json.Unmarshal([]byte(l), &o); err != nil {
			t.Fatal(err)
		}
		if o["kind"] != "intent" || o["deploy"] != id {
			continue
		}
		o["address"] = address
		nb, err := json.Marshal(o)
		if err != nil {
			t.Fatal(err)
		}
		lines[i] = string(nb)
		found = true
	}
	if !found {
		t.Fatalf("no intent line for deploy %s", id)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (p *Process) control(t testing.TB, method, path string, body any) {
	t.Helper()
	p.controlInto(t, method, path, body, nil)
}

func (p *Process) controlInto(t testing.TB, method, path string, body, out any) {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(method, p.URL+path, bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+p.client.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s %s: HTTP %d %s", method, path, resp.StatusCode, rb)
	}
	if out != nil {
		var env struct {
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(rb, &env); err != nil || json.Unmarshal(env.Data, out) != nil {
			t.Fatalf("%s %s: reply %s", method, path, rb)
		}
	}
}

// RunToExit runs bin with env (see AgentEnv) and no listener expected,
// for a configuration the agent must refuse. It returns the exit code and
// everything the process wrote.
func RunToExit(t testing.TB, bin string, env ...string) (int, string) {
	t.Helper()
	cmd := Command(bin)
	cmd.Env = AgentEnv(env...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		_ = stdin.Close()
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode(), out.String()
		}
		if err != nil {
			t.Fatal(err)
		}
		return 0, out.String()
	case <-time.After(60 * time.Second):
		// A refused configuration exits at once; one that is not
		// refused starts listening and would run until stdin closes.
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		<-done
		t.Fatalf("%s did not exit (it accepted the configuration?):\n%s", bin, out.String())
		return 0, ""
	}
}
