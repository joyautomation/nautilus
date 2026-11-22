package tiadtest_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/siemens/tiad"
	"github.com/joyautomation/nautilus/siemens/tiad/tiadtest"
)

// A deploy's verify on the C# agent (tools/tiad/core/Verify.cs): what only
// its file system, its calls and its log show.

// deployedProcess is an agent with one recorded deploy, its session closed.
func deployedProcess(t *testing.T, env ...string) (*tiadtest.Process, *tiad.DeployResult) {
	t.Helper()
	p, s := downloadReady(t, env...)
	r, err := s.Download(ctx, deployLoad)
	if err != nil || r.Deploy == nil {
		t.Fatalf("download: %+v %v", r, err)
	}
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	return p, r.Deploy
}

func stagingEntries(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(filepath.Join(dir, "staging"))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range es {
		out = append(out, e.Name())
	}
	return out
}

// TestAgentVerifiesFromACopy: the verify opens a copy in its own staging
// (never the archive itself, never the work dir), on the TIA thread, reads
// the project CPU's address before the compare, closes the session and
// removes its staging; the archive is unchanged; the audit line names the
// verdict; the journal gets nothing.
func TestAgentVerifiesFromACopy(t *testing.T) {
	p, d := deployedProcess(t)
	before := len(journalLines(t, p.DeploysDir))
	n := len(p.Calls(t))
	r, err := p.Client().VerifyDeploy(ctx, tiad.VerifyRequest{Secrets: tiadtest.BenchLogin, Deploy: d.ID})
	if err != nil || r.Verdict != tiad.VerifyEqual || len(r.Blocks) == 0 {
		t.Fatalf("verify: %+v %v", r, err)
	}
	var order []string
	for _, c := range p.Calls(t)[n:] {
		switch c.Call {
		case "OpenProject", "ProjectCpuAddress", "Compare", "CloseProject":
			order = append(order, c.Call)
			if !c.STA {
				t.Errorf("%s off the TIA thread", c.Call)
			}
		}
	}
	if strings.Join(order, ",") != "OpenProject,ProjectCpuAddress,Compare,CloseProject" {
		t.Fatalf("calls %v", order)
	}
	if es := stagingEntries(t, p.DeploysDir); len(es) != 0 {
		t.Fatalf("staging after a verify: %v", es)
	}
	if got := fileSHA(t, filepath.Join(p.DeploysDir, filepath.FromSlash(d.Archive.File))); got != d.Archive.SHA256 {
		t.Fatalf("the archive changed: %s", got)
	}
	if n := len(journalLines(t, p.DeploysDir)); n != before {
		t.Fatalf("the journal grew from %d to %d lines", before, n)
	}
	for _, f := range files(t, p.Client()) {
		if strings.HasSuffix(f.Path, ".zap21") || strings.HasPrefix(f.Path, "deploy") {
			t.Fatalf("the verify wrote into the work dir: %s", f.Path)
		}
	}
	if l := p.Log(); !strings.Contains(l, `audit verify deploy="`+d.ID+`" session=`) || !strings.Contains(l, "verdict=equal changedOutside=no") {
		t.Fatalf("no verify audit line:\n%s", l)
	}
}

// TestAgentVerifyRefusesAMismatchOffline: a record naming another CPU
// than its archived project's goes nowhere near TIA's online calls.
func TestAgentVerifyRefusesAMismatchOffline(t *testing.T) {
	p, d := deployedProcess(t)
	p.SetDeployAddress(t, d.ID, "192.0.2.99")
	n := len(p.Calls(t))
	_, err := p.Client().VerifyDeploy(ctx, tiad.VerifyRequest{Secrets: tiadtest.BenchLogin, Deploy: d.ID, Fingerprint: &tiad.FingerprintRequest{}})
	if tiad.Kind(err) != tiad.KindCPUMismatch {
		t.Fatalf("verify: %v", err)
	}
	for _, c := range p.Calls(t)[n:] {
		if c.Call == "Compare" || c.Call == "ReadFingerprint" || c.Call == "Identify" {
			t.Fatalf("%s called after a mismatch", c.Call)
		}
	}
	if es := stagingEntries(t, p.DeploysDir); len(es) != 0 {
		t.Fatalf("staging after a refused verify: %v", es)
	}
	if l := p.Log(); !strings.Contains(l, "refused: the archived project's CPU is at \"192.0.2.10\"") || !strings.Contains(l, `audit verify deploy="`+d.ID+`" session=`) ||
		!strings.Contains(l, "failed kind=cpu_mismatch") {
		t.Fatalf("no refusal audit line:\n%s", p.Log())
	}
}

// TestAgentVerifySwappedArchive: a swapped archive is broken, and the
// verify's copy of it is removed: nothing is left in staging.
func TestAgentVerifySwappedArchive(t *testing.T) {
	p, d := deployedProcess(t)
	if err := os.WriteFile(filepath.Join(p.DeploysDir, filepath.FromSlash(d.Archive.File)), []byte("another archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := p.Client().VerifyDeploy(ctx, tiad.VerifyRequest{Secrets: tiadtest.BenchLogin, Deploy: d.ID})
	if err != nil || r.Verdict != tiad.VerifyBroken || !strings.Contains(r.Broken, "not the record's") {
		t.Fatalf("verify: %+v %v", r, err)
	}
	if es := stagingEntries(t, p.DeploysDir); len(es) != 0 {
		t.Fatalf("staging after a broken verify: %v", es)
	}
}

// TestAgentVerifyArchiveGone: an archive that is gone is broken, and TIA
// is never asked to open anything.
func TestAgentVerifyArchiveGone(t *testing.T) {
	p, d := deployedProcess(t)
	if err := os.Remove(filepath.Join(p.DeploysDir, filepath.FromSlash(d.Archive.File))); err != nil {
		t.Fatal(err)
	}
	n := len(p.Calls(t))
	r, err := p.Client().VerifyDeploy(ctx, tiad.VerifyRequest{Secrets: tiadtest.BenchLogin, Deploy: d.ID})
	if err != nil || r.Verdict != tiad.VerifyBroken || !strings.Contains(r.Broken, "is gone") || r.ChangedOutside != nil {
		t.Fatalf("verify: %+v %v", r, err)
	}
	if c := countCalls(p.Calls(t)[n:], "OpenProject"); c != 0 {
		t.Fatalf("TIA opened %d projects for a broken archive", c)
	}
	if es := stagingEntries(t, p.DeploysDir); len(es) != 0 {
		t.Fatalf("staging after a broken verify: %v", es)
	}
}

// TestAgentVerifiesAfterARestart: the record and the archive outlive the
// agent: a new agent opens the archive with its CPU where the record says
// (the fake's CPU forgot its load with the old process, so it differs;
// loaded again with that archive, it is equal). Verify staging an earlier
// run left is removed at start.
func TestAgentVerifiesAfterARestart(t *testing.T) {
	dir := t.TempDir()
	env := "TIAD_DEPLOYS_DIR=" + dir
	p, d := deployedProcess(t, env)
	p.Stop()
	left := filepath.Join(dir, "staging", "v-left")
	if err := os.MkdirAll(filepath.Join(left, "deploy"), 0o700); err != nil {
		t.Fatal(err)
	}
	p2 := tiadtest.StartProcess(t, fakeBin(t), tiadtest.Config{Targets: []string{"192.0.2.10"}}, env)
	p2.DeploysDir = dir
	if _, err := os.Stat(left); !os.IsNotExist(err) {
		t.Fatalf("verify staging left by an earlier run: %v", err)
	}
	c := p2.Client()
	r, err := c.VerifyDeploy(ctx, tiad.VerifyRequest{Secrets: tiadtest.BenchLogin, Deploy: d.ID})
	if err != nil || r.Verdict != tiad.VerifyDifferent || !isTrue(r.ChangedOutside) {
		t.Fatalf("verify after a restart: %+v %v", r, err)
	}
	p2.LoadOutside(t, "192.0.2.10", d.Archive.File)
	if r, err := c.VerifyDeploy(ctx, tiad.VerifyRequest{Secrets: tiadtest.BenchLogin, Deploy: d.ID}); err != nil || r.Verdict != tiad.VerifyEqual {
		t.Fatalf("verify once the CPU holds the archive's load: %+v %v", r, err)
	}
}

// TestAgentVerifyCloseFails: a verify whose close fails still answers its
// verdict, says the session is kept broken, and keeps its staging until a
// later close succeeds.
func TestAgentVerifyCloseFails(t *testing.T) {
	p, d := deployedProcess(t)
	c := p.Client()
	p.SetScript(t, tiadtest.Script{Failures: []tiadtest.ScriptedCall{{Call: "CloseProject", Message: "TIA is busy"}}})
	r, err := c.VerifyDeploy(ctx, tiad.VerifyRequest{Secrets: tiadtest.BenchLogin, Deploy: d.ID})
	if err != nil || r.Verdict != tiad.VerifyEqual || !strings.Contains(r.CloseError, "TIA is busy") || !strings.Contains(r.CloseError, "DELETE /v1/sessions/"+r.Session) {
		t.Fatalf("a verify whose close fails: %+v %v", r, err)
	}
	ss, err := c.Sessions(ctx)
	if err != nil || len(ss) != 1 || ss[0].ID != r.Session {
		t.Fatalf("sessions: %+v %v, want the broken verify session", ss, err)
	}
	if es := stagingEntries(t, p.DeploysDir); len(es) != 1 {
		t.Fatalf("staging while the session is kept: %v", es)
	}
	// A project of the agent's is busy meanwhile: the verify's session
	// holds TIA's one project.
	if _, err := c.VerifyDeploy(ctx, tiad.VerifyRequest{Secrets: tiadtest.BenchLogin, Deploy: d.ID}); tiad.Kind(err) != tiad.KindBusy {
		t.Fatalf("a second verify while the first's session is kept: %v", err)
	}
	p.SetScript(t, tiadtest.Script{})
	if err := ss[0].Close(ctx); err != nil {
		t.Fatal(err)
	}
	if es := stagingEntries(t, p.DeploysDir); len(es) != 0 {
		t.Fatalf("staging after the session closed: %v", es)
	}
}

func isTrue(b *bool) bool { return b != nil && *b }

// setDeployArchive rewrites the archive entry of deploy id's intent line.
func setDeployArchive(t *testing.T, dir, id string, archive map[string]any) {
	t.Helper()
	path := filepath.Join(dir, "deploys.jsonl")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(b), "\n")
	for i, l := range lines {
		if !strings.Contains(l, `"kind":"intent"`) || !strings.Contains(l, `"deploy":"`+id+`"`) {
			continue
		}
		var o map[string]any
		if err := json.Unmarshal([]byte(l), &o); err != nil {
			t.Fatal(err)
		}
		o["archive"] = archive
		nb, _ := json.Marshal(o)
		lines[i] = string(nb)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestAgentVerifyRefusesAnArchiveWithNoCPUAddress: an archived project
// whose CPU has no address (or no CPU) names no CPU: refused, though the
// CPU runs what the archive holds.
func TestAgentVerifyRefusesAnArchiveWithNoCPUAddress(t *testing.T) {
	p, d := deployedProcess(t)
	b := []byte(`{"fakeArchive":1,"of":"x","key":"no-cpu","blocks":[],"protection":"none","accessControl":"disabled"}`)
	sum := sha256.Sum256(b)
	sha := hex.EncodeToString(sum[:])
	file := "archives/" + sha + ".zap21"
	if err := os.WriteFile(filepath.Join(p.DeploysDir, "archives", sha+".zap21"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	setDeployArchive(t, p.DeploysDir, d.ID, map[string]any{"file": file, "sha256": sha, "bytes": len(b)})
	p.LoadOutside(t, "192.0.2.10", file)
	_, err := p.Client().VerifyDeploy(ctx, tiad.VerifyRequest{Secrets: tiadtest.BenchLogin, Deploy: d.ID})
	rec, proj, ok := tiad.VerifyMismatchOf(err)
	if tiad.Kind(err) != tiad.KindCPUMismatch || !ok || rec != "192.0.2.10" || proj != "" || !strings.Contains(err.Error(), "at no address") {
		t.Fatalf("an archive with no CPU address: %v", err)
	}
}

// TestAgentVerifyArchiveEntryShape: an archive entry tiad does not write
// (a file name that is not its sha256's) is broken, even when the file
// the sha256 names is whole.
func TestAgentVerifyArchiveEntryShape(t *testing.T) {
	p, d := deployedProcess(t)
	setDeployArchive(t, p.DeploysDir, d.ID, map[string]any{"file": "archives/../" + d.Archive.SHA256 + ".zap21", "sha256": d.Archive.SHA256, "bytes": d.Archive.Bytes})
	r, err := p.Client().VerifyDeploy(ctx, tiad.VerifyRequest{Secrets: tiadtest.BenchLogin, Deploy: d.ID})
	if err != nil || r.Verdict != tiad.VerifyBroken || !strings.Contains(r.Broken, "not one tiad writes") {
		t.Fatalf("a record with another archive file: %+v %v", r, err)
	}
	setDeployArchive(t, p.DeploysDir, d.ID, map[string]any{"file": d.Archive.File, "sha256": strings.ToUpper(d.Archive.SHA256), "bytes": d.Archive.Bytes})
	if r, err := p.Client().VerifyDeploy(ctx, tiad.VerifyRequest{Secrets: tiadtest.BenchLogin, Deploy: d.ID}); err != nil || r.Verdict != tiad.VerifyBroken {
		t.Fatalf("a record with an upper-case sha256: %+v %v", r, err)
	}
}

// TestAgentVerifyLatestAndDoubt: latest is the newest record that may have
// loaded the CPU: a newer not-loaded record does not count, a newer
// in-doubt one does; an in-doubt record says nothing of changes outside.
func TestAgentVerifyLatestAndDoubt(t *testing.T) {
	p, d := deployedProcess(t)
	c := p.Client()
	s, err := c.CreateProject(ctx, "bench", tiadtest.BenchStation(), tiadtest.BenchSecrets)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Compile(ctx, true, true); err != nil {
		t.Fatal(err)
	}
	p.SetScript(t, tiadtest.Script{Failures: []tiadtest.ScriptedCall{{Call: "Download", Message: "TIA refused"}}})
	_, err = s.Download(ctx, deployLoad)
	notLoaded := tiad.DeployIDOf(err)
	p.SetScript(t, tiadtest.Script{})
	if l, _ := c.Deploys(ctx, ""); notLoaded == "" || len(l.Deploys) != 2 || l.Deploys[0].Status != tiad.DeployNotLoaded {
		t.Fatalf("a not-loaded record: %v %+v", err, l)
	}
	p.LoadOutside(t, "192.0.2.10", "")
	r, err := c.VerifyDeploy(ctx, tiad.VerifyRequest{Secrets: tiadtest.BenchLogin, Deploy: d.ID})
	if tiad.Kind(err) != tiad.KindBusy {
		t.Fatalf("verify while the project is open: %+v %v", r, err)
	}
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if r, err = c.VerifyDeploy(ctx, tiad.VerifyRequest{Secrets: tiadtest.BenchLogin, Deploy: d.ID}); err != nil || r.Latest != d.ID || !isTrue(r.ChangedOutside) {
		t.Fatalf("past a not-loaded record: %+v %v", r, err)
	}
	s, err = c.CreateProject(ctx, "bench", tiadtest.BenchStation(), tiadtest.BenchSecrets)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Compile(ctx, true, true); err != nil {
		t.Fatal(err)
	}
	p.SetScript(t, tiadtest.Script{FailInDoubt: "the connection dropped"})
	_, err = s.Download(ctx, deployLoad)
	doubt := tiad.DeployIDOf(err)
	p.SetScript(t, tiadtest.Script{})
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if r, err = c.VerifyDeploy(ctx, tiad.VerifyRequest{Secrets: tiadtest.BenchLogin, Deploy: d.ID}); err != nil || r.Latest != doubt || !isFalse(r.ChangedOutside) {
		t.Fatalf("under a newer in-doubt record: %+v %v", r, err)
	}
	if r, err = c.VerifyDeploy(ctx, tiad.VerifyRequest{Secrets: tiadtest.BenchLogin, Deploy: doubt}); err != nil || r.Status != tiad.DeployInDoubt || r.ChangedOutside != nil {
		t.Fatalf("the in-doubt record: %+v %v", r, err)
	}
}

// TestAgentVerifyOpenFails: an archive TIA cannot open leaves no session
// and no staging; the next verify runs.
func TestAgentVerifyOpenFails(t *testing.T) {
	p, d := deployedProcess(t)
	c := p.Client()
	p.SetScript(t, tiadtest.Script{Failures: []tiadtest.ScriptedCall{{Call: "OpenProject", Message: "the archive is from a newer TIA"}}})
	if _, err := c.VerifyDeploy(ctx, tiad.VerifyRequest{Secrets: tiadtest.BenchLogin, Deploy: d.ID}); tiad.Kind(err) != tiad.KindOperationFailed || !strings.Contains(err.Error(), "newer TIA") {
		t.Fatalf("verify: %v", err)
	}
	if ss, _ := c.Sessions(ctx); len(ss) != 0 {
		t.Fatalf("sessions after a failed open: %+v", ss)
	}
	if es := stagingEntries(t, p.DeploysDir); len(es) != 0 {
		t.Fatalf("staging after a failed open: %v", es)
	}
	p.SetScript(t, tiadtest.Script{})
	if r, err := c.VerifyDeploy(ctx, tiad.VerifyRequest{Secrets: tiadtest.BenchLogin, Deploy: d.ID}); err != nil || r.Verdict != tiad.VerifyEqual {
		t.Fatalf("the next verify: %+v %v", r, err)
	}
}

func isFalse(b *bool) bool { return b != nil && !*b }

// TestAgentVerifyReaperRemovesStaging: a verify session kept broken by a
// failed close is closed by the idle reaper once TIA lets go, and its
// staging goes with it.
func TestAgentVerifyReaperRemovesStaging(t *testing.T) {
	// Long enough that the setup's own session is never reaped.
	p, d := deployedProcess(t, "TIAD_SESSION_IDLE=5s")
	c := p.Client()
	p.SetScript(t, tiadtest.Script{Failures: []tiadtest.ScriptedCall{{Call: "CloseProject", Message: "TIA is busy"}}})
	r, err := c.VerifyDeploy(ctx, tiad.VerifyRequest{Secrets: tiadtest.BenchLogin, Deploy: d.ID})
	if err != nil || r.CloseError == "" {
		t.Fatalf("verify: %+v %v", r, err)
	}
	if es := stagingEntries(t, p.DeploysDir); len(es) != 1 {
		t.Fatalf("staging while the session is kept: %v", es)
	}
	p.SetScript(t, tiadtest.Script{})
	deadline := time.Now().Add(30 * time.Second)
	for len(stagingEntries(t, p.DeploysDir)) != 0 {
		if time.Now().After(deadline) {
			ss, _ := c.Sessions(ctx)
			t.Fatalf("staging %v, sessions %+v: the reaper did not remove it", stagingEntries(t, p.DeploysDir), ss)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if ss, _ := c.Sessions(ctx); len(ss) != 0 {
		t.Fatalf("sessions after the reaper: %+v", ss)
	}
}

// TestAgentVerifyRecordShapes: a record whose address is not an IPv4
// address is broken, never compared; a journal line tiad cannot read may
// be a newer deploy, so a difference says nothing of changes outside.
func TestAgentVerifyRecordShapes(t *testing.T) {
	p, d := deployedProcess(t)
	c := p.Client()
	p.SetDeployAddress(t, d.ID, "plc-1")
	r, err := c.VerifyDeploy(ctx, tiad.VerifyRequest{Secrets: tiadtest.BenchLogin, Deploy: d.ID})
	if err != nil || r.Verdict != tiad.VerifyBroken || !strings.Contains(r.Broken, "not an IPv4 address") {
		t.Fatalf("a record naming no IPv4 address: %+v %v", r, err)
	}
	p.SetDeployAddress(t, d.ID, "192.0.2.10")
	f, err := os.OpenFile(filepath.Join(p.DeploysDir, "deploys.jsonl"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("{\"v\":1,\"kind\":\"intent\",\"deploy\":\"d-torn\",\"addr\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	p.LoadOutside(t, "192.0.2.10", "")
	r, err = c.VerifyDeploy(ctx, tiad.VerifyRequest{Secrets: tiadtest.BenchLogin, Deploy: d.ID})
	if err != nil || r.Verdict != tiad.VerifyDifferent || r.UnreadableLines != 1 || r.ChangedOutside != nil {
		t.Fatalf("a difference beside an unreadable line: %+v %v", r, err)
	}
	if !strings.Contains(p.Log(), "changedOutside=unknown") {
		t.Fatalf("the audit line claims to know:\n%s", p.Log())
	}
}

// TestAgentVerifyFailsAndCannotClose: a verify that is refused and whose
// close fails says both: a broken session now holds TIA's one project.
func TestAgentVerifyFailsAndCannotClose(t *testing.T) {
	p, d := deployedProcess(t)
	p.SetDeployAddress(t, d.ID, "192.0.2.99")
	p.SetScript(t, tiadtest.Script{Failures: []tiadtest.ScriptedCall{{Call: "CloseProject", Message: "TIA is busy"}}})
	_, err := p.Client().VerifyDeploy(ctx, tiad.VerifyRequest{Secrets: tiadtest.BenchLogin, Deploy: d.ID})
	if tiad.Kind(err) != tiad.KindCPUMismatch || !strings.Contains(err.Error(), "closing the verify's session") || !strings.Contains(err.Error(), "TIA is busy") {
		t.Fatalf("verify: %v", err)
	}
	if rec, _, ok := tiad.VerifyMismatchOf(err); !ok || rec != "192.0.2.99" {
		t.Fatalf("the mismatch data is lost: %v", err)
	}
}
