package tiadtest_test

import (
	"bufio"
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

// The deploy record on disk (tools/tiad/core/Deploys.cs): what only the
// C# agent's own file system and log can show.

var deployLoad = tiad.DownloadRequest{Address: "192.0.2.10", Software: true, Announced: true, Decisions: tiad.Decisions{StartAfter: tiad.No()}}

func journalLines(t *testing.T, dir string) []map[string]any {
	t.Helper()
	f, err := os.Open(filepath.Join(dir, "deploys.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []map[string]any
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("journal line %q: %v", sc.Text(), err)
		}
		out = append(out, m)
	}
	return out
}

func fileSHA(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// TestAgentJournalsADeploy: an intent line, then an outcome line, both on
// disk; the archive at its content address holds what its hash says;
// staging is left empty; the archive is made on the TIA thread, before
// TIA's download, and the fingerprint read after the compare; the audit
// lines name the deploy.
func TestAgentJournalsADeploy(t *testing.T) {
	p, s := downloadReady(t)
	req := deployLoad
	req.Source = &tiad.DeploySource{Commit: strings.Repeat("ab", 20)}
	r, err := s.Download(ctx, req)
	if err != nil || r.Deploy == nil {
		t.Fatalf("download: %+v %v", r, err)
	}
	d := r.Deploy
	lines := journalLines(t, p.DeploysDir)
	if len(lines) != 2 || lines[0]["kind"] != "intent" || lines[1]["kind"] != "outcome" || lines[0]["deploy"] != d.ID || lines[1]["deploy"] != d.ID ||
		lines[0]["v"] != float64(1) || lines[1]["status"] != "loaded" {
		t.Fatalf("journal: %+v", lines)
	}
	if blocks, _ := lines[1]["blocks"].([]any); len(blocks) == 0 || lines[1]["state"] != nil && lines[1]["stateSource"] == nil {
		t.Fatalf("the outcome line's evidence: %+v", lines[1])
	}
	if src, _ := lines[0]["source"].(map[string]any); src["commit"] != strings.Repeat("ab", 20) || src["claimedBy"] != "caller" {
		t.Fatalf("the intent's source: %+v", lines[0]["source"])
	}
	if got := fileSHA(t, filepath.Join(p.DeploysDir, filepath.FromSlash(d.Archive.File))); got != d.Archive.SHA256 {
		t.Fatalf("archive %s hashes to %s", d.Archive.File, got)
	}
	if es, err := os.ReadDir(filepath.Join(p.DeploysDir, "staging")); err != nil || len(es) != 0 {
		t.Fatalf("staging after a deploy: %v %v", es, err)
	}
	// Nothing of the record is in the work dir.
	for _, f := range files(t, p.Client()) {
		if strings.HasSuffix(f.Path, ".zap21") {
			t.Fatalf("an archive in the work dir: %s", f.Path)
		}
	}
	var order []string
	var archiveSTA bool
	for _, c := range p.Calls(t) {
		switch c.Call {
		case "Archive", "Download", "Compare", "ReadFingerprint":
			order = append(order, c.Call)
		}
		if c.Call == "Archive" {
			archiveSTA = c.STA
		}
	}
	if strings.Join(order, ",") != "Archive,Download,Compare,ReadFingerprint" || !archiveSTA {
		t.Fatalf("calls %v (archive on the TIA thread: %v), want the archive first, on the TIA thread, and the read last", order, archiveSTA)
	}
	l := p.Log()
	for _, want := range []string{"audit deploy intent deploy=" + d.ID, "source={commit=\"" + strings.Repeat("ab", 20) + "\" hash=unset} deploy=" + d.ID + " kept=", "outcome=ok deploy=" + d.ID} {
		if !strings.Contains(l, want) {
			t.Errorf("the log has no %q:\n%s", want, l)
		}
	}
}

// TestAgentRefusesADeployItCannotRecord: an intent line that cannot be
// written refuses the download before TIA's download is called.
func TestAgentRefusesADeployItCannotRecord(t *testing.T) {
	p, s := downloadReady(t)
	if err := os.Mkdir(filepath.Join(p.DeploysDir, "deploys.jsonl"), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := s.Download(ctx, deployLoad)
	if tiad.Kind(err) != tiad.KindOperationFailed || !strings.Contains(err.Error(), "writing the deploy record's intent line failed") ||
		!strings.Contains(err.Error(), "nothing was sent to the CPU") || tiad.DeployIDOf(err) != "" {
		t.Fatalf("a download whose intent line cannot be written: %v", err)
	}
	if n := countCalls(p.Calls(t), "Download"); n != 0 || len(p.Loaded(t)) != 0 {
		t.Fatalf("TIA's download called %d times, loads %+v", n, p.Loaded(t))
	}
	if !strings.Contains(p.Log(), "audit deploy intent-failed") {
		t.Fatalf("no intent-failed audit line:\n%s", p.Log())
	}
}

// TestAgentSaysAnOutcomeItCouldNotRecord: an outcome line that cannot be
// written leaves the load as it is, says so (recorded: false), and the
// record lists as in doubt.
func TestAgentSaysAnOutcomeItCouldNotRecord(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes a read-only file")
	}
	p, s := downloadReady(t)
	p.SetScript(t, tiadtest.Script{Delays: []tiadtest.ScriptedCall{{Call: "Download", MS: 1500}}})
	journal := filepath.Join(p.DeploysDir, "deploys.jsonl")
	go func() {
		// Once the intent line is on disk, make the journal read-only.
		for i := 0; i < 100; i++ {
			if b, err := os.ReadFile(journal); err == nil && strings.Contains(string(b), `"intent"`) {
				_ = os.Chmod(journal, 0o400)
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	r, err := s.Download(ctx, deployLoad)
	if err != nil || !r.Downloaded || r.Deploy == nil || r.Deploy.Recorded || !strings.Contains(r.Deploy.RecordError, "outcome line failed") {
		t.Fatalf("a load whose outcome line cannot be written: %+v %v", r.Deploy, err)
	}
	l, err := p.Client().Deploys(ctx, "")
	if err != nil || len(l.Deploys) != 1 || l.Deploys[0].Status != tiad.DeployInDoubt || l.Deploys[0].Outcome != nil {
		t.Fatalf("the record without an outcome: %+v %v", l, err)
	}
	if !strings.Contains(p.Log(), "deploy="+r.Deploy.ID+"(outcome-not-recorded)") {
		t.Fatalf("the audit line does not say the outcome went unrecorded:\n%s", p.Log())
	}
}

// TestAgentArchivesByContent: two deploys with the same bytes share one
// archive; a file already at that name with other bytes is replaced, never
// taken by its name.
func TestAgentArchivesByContent(t *testing.T) {
	p, s := downloadReady(t)
	p.SetScript(t, tiadtest.Script{ArchiveSame: true})
	const same = "fake TIA project archive"
	sum := sha256.Sum256([]byte(same))
	sha := hex.EncodeToString(sum[:])
	planted := filepath.Join(p.DeploysDir, "archives", sha+".zap21")
	if err := os.WriteFile(planted, []byte("not the archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	r1, err := s.Download(ctx, deployLoad)
	if err != nil {
		t.Fatal(err)
	}
	if got := fileSHA(t, planted); got != sha || r1.Deploy.Archive.SHA256 != sha {
		t.Fatalf("the planted file hashes to %s after a deploy, record %s, want %s", got, r1.Deploy.Archive.SHA256, sha)
	}
	r2, err := s.Download(ctx, deployLoad)
	if err != nil || r2.Deploy.ID == r1.Deploy.ID || r2.Deploy.Archive != r1.Deploy.Archive {
		t.Fatalf("a second deploy of the same bytes: %+v %v", r2.Deploy, err)
	}
	if es, _ := os.ReadDir(filepath.Join(p.DeploysDir, "archives")); len(es) != 1 {
		t.Fatalf("archives %v, want one", es)
	}
}

// TestAgentKeepsDeploysAcrossRestarts: the record is on disk; a line left
// torn by an agent that died is skipped and counted, and never swallows the
// next line; staging an earlier run left is removed at start.
func TestAgentKeepsDeploysAcrossRestarts(t *testing.T) {
	dir := t.TempDir()
	env := "TIAD_DEPLOYS_DIR=" + dir
	p, s := downloadReady(t, env)
	r, err := s.Download(ctx, deployLoad)
	if err != nil {
		t.Fatal(err)
	}
	p.Stop()
	f, err := os.OpenFile(filepath.Join(dir, "deploys.jsonl"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"v":1,"kind":"intent","deploy":"d-torn","addr`); err != nil {
		t.Fatal(err)
	}
	f.Close()
	left := filepath.Join(dir, "staging", "d-left")
	other := filepath.Join(dir, "staging", "not-a-deploy")
	for _, d := range []string{left, other} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	p2, s2 := downloadReady(t, env)
	if _, err := os.Stat(left); !os.IsNotExist(err) {
		t.Fatalf("staging left by an earlier run: %v", err)
	}
	// Only what the agent names: anything else under staging stays.
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("a directory the agent did not make: %v", err)
	}
	r2, err := s2.Download(ctx, deployLoad)
	if err != nil {
		t.Fatal(err)
	}
	l, err := p2.Client().Deploys(ctx, "")
	if err != nil || l.UnreadableLines != 1 || len(l.Deploys) != 2 || l.Deploys[0].ID != r2.Deploy.ID || l.Deploys[1].ID != r.Deploy.ID ||
		l.Deploys[0].Status != tiad.DeployLoaded || l.Deploys[1].Status != tiad.DeployLoaded {
		t.Fatalf("deploys after a restart and a torn line: %+v %v", l, err)
	}
}

// TestAgentRefusesAnUnusableDeploysDir: the agent will not start on a
// deploys dir it cannot write.
func TestAgentRefusesAnUnusableDeploysDir(t *testing.T) {
	bin := fakeBin(t)
	file := filepath.Join(t.TempDir(), "a-file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	code, out := tiadtest.RunToExit(t, bin, "TIAD_WORKDIR="+t.TempDir(), "TIAD_PINS_DIR="+t.TempDir(), "TIAD_DEPLOYS_DIR="+file, "TIAD_TOKEN=t", "TIAD_ADDR=http://127.0.0.1:0")
	if code != 2 || !strings.Contains(out, "TIAD_DEPLOYS_DIR") || !strings.Contains(out, "is not usable") {
		t.Fatalf("exit %d, want 2 naming TIAD_DEPLOYS_DIR:\n%s", code, out)
	}
}

// TestAgentAuditsARefusedSource: a source the agent refuses is journalled
// quoted, so it cannot forge an audit line.
func TestAgentAuditsARefusedSource(t *testing.T) {
	p, s := downloadReady(t)
	status, b := rawPost(t, p, "/v1/sessions/"+s.ID+"/download", map[string]any{"address": "192.0.2.10", "software": true, "announced": true,
		"source": map[string]any{"commit": "x\naudit download forged"}})
	if status != 400 || !strings.Contains(b, "source.commit") {
		t.Fatalf("HTTP %d %s", status, b)
	}
	l := p.Log()
	if strings.Contains(l, "\naudit download forged") || !strings.Contains(l, `source={commit="x\naudit download forged" hash=unset}`) {
		t.Fatalf("the refused source in the log:\n%s", l)
	}
}

// TestAgentRecordsHowADownloadFailed: every way TIA's part of a recorded
// download can fail gets an outcome line whose status is what the CPU may
// hold, and the error names the record. Only TIA failing before it asked
// anything is not-loaded; an internal error of tiad's, TIA dying inside
// the download, a failure TIA reports in doubt and a compare that differs
// are in doubt.
func TestAgentRecordsHowADownloadFailed(t *testing.T) {
	for _, c := range []struct {
		name   string
		script tiadtest.Script
		kind   string
		status string
	}{
		{"TIA dies preparing", tiadtest.Script{TiaDies: "preparing"}, tiad.KindOperationFailed, tiad.DeployNotLoaded},
		{"TIA dies downloading", tiadtest.Script{TiaDies: "downloading"}, tiad.KindOutcomeUnknown, tiad.DeployInDoubt},
		{"TIA fails in doubt", tiadtest.Script{FailInDoubt: "the connection dropped"}, tiad.KindOutcomeUnknown, tiad.DeployInDoubt},
		{"an internal error", tiadtest.Script{DownloadThrows: "a null reference"}, tiad.KindOutcomeUnknown, tiad.DeployInDoubt},
		{"the compare differs", tiadtest.Script{CompareDiffers: true}, tiad.KindOutcomeUnknown, tiad.DeployInDoubt},
	} {
		t.Run(c.name, func(t *testing.T) {
			p, s := downloadReady(t)
			p.SetScript(t, c.script)
			_, err := s.Download(ctx, deployLoad)
			id := tiad.DeployIDOf(err)
			if tiad.Kind(err) != c.kind || id == "" {
				t.Fatalf("download: %v (kind %s), want %s naming its record", err, tiad.Kind(err), c.kind)
			}
			lines := journalLines(t, p.DeploysDir)
			if len(lines) != 2 || lines[1]["kind"] != "outcome" || lines[1]["deploy"] != id || lines[1]["status"] != c.status || lines[1]["outcome"] != c.kind {
				t.Fatalf("journal: %+v, want an outcome line %s/%s", lines, c.status, c.kind)
			}
		})
	}
}
