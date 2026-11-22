package tiadtest

import (
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/siemens/tiad"
)

// The deploy record half of the contract (siemens-ide.md §3.5, §7, D7):
// every real download archives the project before TIA's download and
// records an intent and an outcome; an archive that fails refuses the
// download with nothing sent; the CPU's fingerprint is read after a load
// that compared equal, through the fingerprint call's trust path.
func contractDeploys(t *testing.T, start Starter) {
	t.Run("Record", func(t *testing.T) { contractDeployRecord(t, start) })
	t.Run("ArchiveFails", func(t *testing.T) { contractDeployArchiveFails(t, start) })
	t.Run("NoRecord", func(t *testing.T) { contractDeployNoRecord(t, start) })
	t.Run("Cancelled", func(t *testing.T) { contractDeployCancelled(t, start) })
	t.Run("Source", func(t *testing.T) { contractDeploySource(t, start) })
	t.Run("OutsideChanges", func(t *testing.T) { contractDeployOutsideChanges(t, start) })
	t.Run("FingerprintTrust", func(t *testing.T) { contractDeployFingerprintTrust(t, start) })
	t.Run("FingerprintFails", func(t *testing.T) { contractDeployFingerprintFails(t, start) })
	t.Run("ListQuery", func(t *testing.T) { contractDeployListQuery(t, start) })
}

const (
	benchCommit = "0123456789abcdef0123456789abcdef01234567"
	benchHash   = "sha256:" + "89abcdef0123456789abcdef0123456789abcdef0123456789abcdef01234567"
)

var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

func deploys(t *testing.T, c *tiad.Client, address string) tiad.DeployList {
	t.Helper()
	l, err := c.Deploys(ctx, address)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// contractDeployRecord: a load's record, in the response and in the list:
// the archive by its content address, the source as the caller's claim,
// the fingerprint after the load with its DownloadId; a second load gets a
// record and an archive of its own, listed first.
func contractDeployRecord(t *testing.T, start Starter) {
	a, s := compiledBench(t, start)
	c := a.Client()
	req := benchSoftwareLoad
	req.Source = &tiad.DeploySource{Commit: benchCommit, Hash: benchHash}
	r, err := s.Download(ctx, req)
	if err != nil || !r.Downloaded {
		t.Fatalf("download: %+v %v", r, err)
	}
	d := r.Deploy
	if d == nil || !strings.HasPrefix(d.ID, "d-") || !d.Recorded || d.RecordError != "" {
		t.Fatalf("deploy: %+v", d)
	}
	if !sha256Hex.MatchString(d.Archive.SHA256) || d.Archive.File != "archives/"+d.Archive.SHA256+".zap21" || d.Archive.Bytes <= 0 {
		t.Fatalf("archive: %+v", d.Archive)
	}
	if d.Source == nil || d.Source.Commit != benchCommit || d.Source.Hash != benchHash || d.Source.ClaimedBy != "caller" {
		t.Fatalf("source: %+v, want the caller's claim", d.Source)
	}
	// The fingerprint after the load: its DownloadId names this load.
	if d.Fingerprint == nil || d.FingerprintError != "" || d.DownloadID != "fake-download-1" {
		t.Fatalf("fingerprint: %+v %q %q", d.Fingerprint, d.FingerprintError, d.DownloadID)
	}
	if v, ok := d.Fingerprint.Value("Hardware"); !ok || v != "fake-hardware-0" {
		t.Fatalf("the record's Hardware item: %q %v", v, ok)
	}
	if d.Fingerprint.BlindTrust != nil || len(d.Fingerprint.TrustedBlindly) != 0 {
		t.Fatalf("a read that asked nothing trusted: %+v", d.Fingerprint)
	}

	l := deploys(t, c, "")
	if l.UnreadableLines != 0 || len(l.Deploys) != 1 {
		t.Fatalf("deploys: %+v", l)
	}
	e := l.Deploys[0]
	if e.ID != d.ID || e.Status != tiad.DeployLoaded || e.Address != benchCPU || e.Session != s.ID || !e.Software || e.Hardware ||
		e.Archive != d.Archive || e.Source == nil || *e.Source != *d.Source || e.TIA.Version != "V21" || e.At == "" || e.Attestation != nil {
		t.Fatalf("the record: %+v", e)
	}
	o := e.Outcome
	if o == nil || o.Status != tiad.DeployLoaded || o.Outcome != tiad.DeployLoaded || !o.CompareEqual || o.DownloadID != "fake-download-1" ||
		o.Fingerprint == nil || o.HardwareVerified != nil || o.At == "" {
		t.Fatalf("the record's outcome: %+v", o)
	}

	// A hardware load: its own record and archive, first in the list,
	// with the attestation, unverified hardware and the new Hardware item.
	r2, err := s.Download(ctx, AcceptHardware(tiad.DownloadRequest{Secrets: BenchDownloadSecrets(), Address: benchCPU, Hardware: true, Software: true, Announced: true, Decisions: tiad.Decisions{StartAfter: tiad.No()}}))
	if err != nil {
		t.Fatal(err)
	}
	d2 := r2.Deploy
	if d2 == nil || d2.ID == d.ID || d2.Archive.SHA256 == d.Archive.SHA256 || d2.Source != nil || d2.DownloadID != "fake-download-2" {
		t.Fatalf("a second deploy: %+v, want a record and an archive of its own", d2)
	}
	if v, _ := d2.Fingerprint.Value("Hardware"); v != "fake-hardware-1" {
		t.Fatalf("the hardware load's Hardware item: %q", v)
	}
	l = deploys(t, c, benchCPU)
	if len(l.Deploys) != 2 || l.Deploys[0].ID != d2.ID || l.Deploys[1].ID != d.ID {
		t.Fatalf("deploys of %s: %+v, want newest first", benchCPU, l.Deploys)
	}
	h := l.Deploys[0]
	if !h.Hardware || h.Attestation == nil || h.Attestation.By != BenchAttestation.By || h.Attestation.At == "" ||
		h.Outcome == nil || h.Outcome.HardwareVerified == nil || *h.Outcome.HardwareVerified {
		t.Fatalf("the hardware load's record: %+v %+v", h, h.Outcome)
	}
	if l := deploys(t, c, "192.0.2.99"); len(l.Deploys) != 0 {
		t.Fatalf("deploys of another CPU: %+v", l)
	}
}

// contractDeployArchiveFails: an archive that fails refuses the download
// before TIA's download: nothing loaded, no record, no id.
func contractDeployArchiveFails(t *testing.T, start Starter) {
	a, s := compiledBench(t, start)
	a.SetArchiveFails(t, "the disk is full")
	_, err := s.Download(ctx, benchSoftwareLoad)
	if tiad.Kind(err) != tiad.KindOperationFailed || !strings.Contains(err.Error(), "archiving the project for the deploy record failed") ||
		!strings.Contains(err.Error(), "the disk is full") || !strings.Contains(err.Error(), "nothing was sent to the CPU") || tiad.DeployIDOf(err) != "" {
		t.Fatalf("a download whose archive fails: %v", err)
	}
	if l := a.Loaded(t); len(l) != 0 {
		t.Fatalf("a download whose archive failed loaded %+v", l)
	}
	if l := deploys(t, a.Client(), ""); len(l.Deploys) != 0 {
		t.Fatalf("a download whose archive failed left a record: %+v", l)
	}
	a.SetArchiveFails(t, "")
	if r, err := s.Download(ctx, benchSoftwareLoad); err != nil || r.Deploy == nil {
		t.Fatalf("a download once the archive works: %+v %v", r, err)
	}
}

// contractDeployNoRecord: a dry run, and a download tiad refuses before
// anything reaches the CPU, leave no record.
func contractDeployNoRecord(t *testing.T, start Starter) {
	a, s := compiledBench(t, start)
	a.SetQuestions(t, []tiad.Question{qStop})
	dry := tiad.DownloadRequest{Secrets: BenchDownloadSecrets(), Address: benchCPU, Software: true, Announced: true, DryRun: true, Source: &tiad.DeploySource{Commit: benchCommit}}
	if r, err := s.Download(ctx, dry); err != nil || r.Deploy != nil {
		t.Fatalf("dry run: %+v %v", r, err)
	}
	noStart := tiad.DownloadRequest{Secrets: BenchDownloadSecrets(), Address: benchCPU, Software: true, Announced: true}
	if _, err := s.Download(ctx, noStart); tiad.Kind(err) != tiad.KindDecisionRequired || tiad.DeployIDOf(err) != "" {
		t.Fatalf("no start-after: %v", err)
	}
	if l := deploys(t, a.Client(), ""); len(l.Deploys) != 0 {
		t.Fatalf("refusals left records: %+v", l)
	}
}

// contractDeployCancelled: a download TIA's checks cancel after the
// archive is a record too, not loaded, and its error names it; one that
// ends in doubt is in doubt.
func contractDeployCancelled(t *testing.T, start Starter) {
	a, s := compiledBench(t, start)
	c := a.Client()
	a.SetQuestions(t, []tiad.Question{qStop})
	_, err := s.Download(ctx, benchSoftwareLoad)
	id := tiad.DeployIDOf(err)
	if tiad.Kind(err) != tiad.KindDecisionRequired || id == "" {
		t.Fatalf("a download TIA's stop-cpu check cancels: %v, want decision_required naming its record", err)
	}
	l := deploys(t, c, "")
	if len(l.Deploys) != 1 || l.Deploys[0].ID != id || l.Deploys[0].Status != tiad.DeployNotLoaded ||
		l.Deploys[0].Outcome == nil || l.Deploys[0].Outcome.Outcome != tiad.KindDecisionRequired || !strings.Contains(l.Deploys[0].Outcome.Message, "does not answer") {
		t.Fatalf("the cancelled download's record: %+v", l.Deploys)
	}
	// stop-cpu answered yes, then a check the request does not answer:
	// TIA may have stopped the CPU, so the record is in doubt.
	a.SetQuestions(t, []tiad.Question{qStop, {ID: tiad.QuestionOverwrite, Message: "Overwrite", Detail: "OverwriteSystemData"}})
	req := benchSoftwareLoad
	req.Decisions.StopCPU = tiad.Yes()
	_, err = s.Download(ctx, req)
	id2 := tiad.DeployIDOf(err)
	if tiad.Kind(err) != tiad.KindOutcomeUnknown || id2 == "" || id2 == id {
		t.Fatalf("a cancel after an acting answer: %v", err)
	}
	e := deploys(t, c, "").Deploys[0]
	if e.ID != id2 || e.Status != tiad.DeployInDoubt || e.Outcome == nil || e.Outcome.Outcome != tiad.KindOutcomeUnknown || strings.Join(e.Outcome.ActingAnswers, ",") != "stop-cpu" {
		t.Fatalf("the in-doubt record: %+v %+v", e, e.Outcome)
	}
}

// contractDeploySource: a source of the wrong shape is a bad request over
// the wire, and loads nothing.
func contractDeploySource(t *testing.T, start Starter) {
	a, s := compiledBench(t, start)
	path := "/v1/sessions/" + s.ID + "/download"
	base := func(src any) map[string]any {
		return map[string]any{"address": benchCPU, "software": true, "announced": true, "decisions": map[string]any{"startAfter": false}, "source": src}
	}
	for _, rc := range []struct {
		name string
		src  any
		want string
	}{
		{"no commit", map[string]any{"hash": benchHash}, "source.commit"},
		{"a short commit", map[string]any{"commit": benchCommit[:39]}, "source.commit"},
		{"an upper-case commit", map[string]any{"commit": strings.ToUpper(benchCommit)}, "source.commit"},
		{"a branch for a commit", map[string]any{"commit": "main"}, "source.commit"},
		{"a hash without its prefix", map[string]any{"commit": benchCommit, "hash": benchHash[len("sha256:"):]}, "source.hash"},
		{"a short hash", map[string]any{"commit": benchCommit, "hash": benchHash[:70]}, "source.hash"},
		{"a hash with a newline", map[string]any{"commit": benchCommit, "hash": benchHash[:70] + "\n"}, "source.hash"},
		{"an unknown field", map[string]any{"commit": benchCommit, "branch": "main"}, "unknown field"},
		{"a fingerprint option misspelt", nil, "unknown field"},
	} {
		body := base(rc.src)
		if rc.src == nil {
			delete(body, "source")
			body["fingerprint"] = map[string]any{"trustUnverified": true}
		}
		status, b := rawJSON(t, a.Client(), "POST", path, body)
		if status != http.StatusBadRequest || envelopeKind(b) != tiad.KindBadRequest || !strings.Contains(errMessage(b), rc.want) {
			t.Errorf("%s: HTTP %d %s, want 400 %q", rc.name, status, b, rc.want)
		}
	}
	// A 64-digit commit (SHA-256 git) and no hash are fine.
	req := benchSoftwareLoad
	req.Source = &tiad.DeploySource{Commit: benchCommit + benchCommit[:24]}
	if r, err := s.Download(ctx, req); err != nil || r.Deploy == nil || r.Deploy.Source == nil || r.Deploy.Source.Hash != "" {
		t.Fatalf("a 64-digit commit: %+v %v", r, err)
	}
	if l := a.Loaded(t); len(l) != 1 {
		t.Fatalf("loads %+v, want the good one only", l)
	}
}

// contractDeployOutsideChanges: the operator's accept of a download onto a
// CPU changed outside nautilus is in the record, in the response and the
// list, as the caller's claim; a deploy without one has none; one of the
// wrong shape is a bad request over the wire and loads nothing.
func contractDeployOutsideChanges(t *testing.T, start Starter) {
	a, s := compiledBench(t, start)
	c := a.Client()
	req := benchSoftwareLoad
	if r, err := s.Download(ctx, req); err != nil || r.Deploy == nil || r.Deploy.OutsideChanges != nil {
		t.Fatalf("a deploy without an accept: %+v %v", r, err)
	}
	req.OutsideChanges = &tiad.OutsideChanges{By: "bench operator", Verdict: tiad.OutsideDifferent, Deploy: "d-20261122T000000000Z-00000000"}
	r, err := s.Download(ctx, req)
	want := tiad.OutsideChangesRecord{By: "bench operator", Verdict: "different", Deploy: "d-20261122T000000000Z-00000000", ClaimedBy: "caller"}
	if err != nil || r.Deploy == nil || r.Deploy.OutsideChanges == nil || *r.Deploy.OutsideChanges != want {
		t.Fatalf("download: %+v %v", r.Deploy, err)
	}
	l := deploys(t, c, benchCPU)
	if len(l.Deploys) != 2 || l.Deploys[0].OutsideChanges == nil || *l.Deploys[0].OutsideChanges != want || l.Deploys[1].OutsideChanges != nil {
		t.Fatalf("the list: %+v", l.Deploys)
	}
	// Unreadable journal lines name no record to verify.
	req.OutsideChanges = &tiad.OutsideChanges{By: "bench operator", Verdict: tiad.OutsideUnreadable}
	if r, err := s.Download(ctx, req); err != nil || r.Deploy.OutsideChanges == nil || r.Deploy.OutsideChanges.Deploy != "" {
		t.Fatalf("unreadable: %+v %v", r.Deploy, err)
	}

	path := "/v1/sessions/" + s.ID + "/download"
	for _, rc := range []struct {
		name string
		oc   map[string]any
		want string
	}{
		{"no by", map[string]any{"verdict": "different", "deploy": "d-1"}, "outsideChanges.by is empty"},
		{"a blank by", map[string]any{"by": " \t", "verdict": "different", "deploy": "d-1"}, "outsideChanges.by is empty"},
		{"a no-break space by", map[string]any{"by": "\u00a0\u2003", "verdict": "different", "deploy": "d-1"}, "outsideChanges.by is empty"},
		{"a long by", map[string]any{"by": strings.Repeat("x", tiad.MaxOutsideBy+1), "verdict": "different", "deploy": "d-1"}, "outsideChanges.by is longer"},
		{"another verdict", map[string]any{"by": "op", "verdict": "equal", "deploy": "d-1"}, "outsideChanges.verdict"},
		{"no deploy for different", map[string]any{"by": "op", "verdict": "different"}, "outsideChanges.deploy is empty"},
		{"no deploy for broken", map[string]any{"by": "op", "verdict": "broken"}, "outsideChanges.deploy is empty"},
		{"a long deploy", map[string]any{"by": "op", "verdict": "broken", "deploy": strings.Repeat("d", tiad.MaxOutsideDeploy+1)}, "outsideChanges.deploy is longer"},
		{"an unknown field", map[string]any{"by": "op", "verdict": "broken", "deploy": "d-1", "reason": "x"}, "unknown field"},
	} {
		body := map[string]any{"address": benchCPU, "software": true, "announced": true, "decisions": map[string]any{"startAfter": false}, "outsideChanges": rc.oc}
		status, b := rawJSON(t, c, "POST", path, body)
		if status != http.StatusBadRequest || envelopeKind(b) != tiad.KindBadRequest || !strings.Contains(errMessage(b), rc.want) {
			t.Errorf("%s: HTTP %d %s, want 400 %q", rc.name, status, b, rc.want)
		}
	}
	if l := a.Loaded(t); len(l) != 3 {
		t.Fatalf("loads %+v, want the three good ones only", l)
	}
}

// contractDeployFingerprintTrust: the post-load read of a CPU that asks
// TLS trust fails without its opt-in, naming it, and the load stands with
// nothing trusted; with it, TIA's instance trusts blindly, recorded as a
// fingerprint call's trust: the next download needs acceptBlindTrust.
func contractDeployFingerprintTrust(t *testing.T, start Starter) {
	a, s := compiledBench(t, start)
	a.SetFingerprint(t, FingerprintScript{AsksTLS: true})
	r, err := s.Download(ctx, benchSoftwareLoad)
	if err != nil || !r.Downloaded || r.Deploy == nil || r.Deploy.Fingerprint != nil || r.Deploy.DownloadID != "" ||
		!strings.Contains(r.Deploy.FingerprintError, "trust the TLS certificate of PLC_1") || !strings.Contains(r.Deploy.FingerprintError, "fingerprint.trustUnverifiedCertificate: true") {
		t.Fatalf("a load whose read asks TLS, no opt-in: %+v %v", r.Deploy, err)
	}
	if e := deploys(t, a.Client(), "").Deploys[0]; e.Status != tiad.DeployLoaded || e.Outcome.FingerprintError == "" {
		t.Fatalf("the record of a load whose read failed: %+v", e.Outcome)
	}
	// Nothing trusted: the next download needs no yes.
	req := benchSoftwareLoad
	req.Fingerprint = &tiad.FingerprintRequest{TrustUnverifiedCertificate: true}
	r, err = s.Download(ctx, req)
	if err != nil || r.BlindTrust != nil || r.Deploy == nil || r.Deploy.Fingerprint == nil || len(r.Deploy.Fingerprint.TrustedBlindly) != 1 {
		t.Fatalf("a load with the read's opt-in: %+v %v", r.Deploy, err)
	}
	bt := r.Deploy.Fingerprint.BlindTrust
	if bt == nil || bt.Call != "fingerprint read" || bt.Session != s.ID || r.Deploy.DownloadID != "fake-download-2" {
		t.Fatalf("the trust the read gave: %+v", r.Deploy)
	}
	if e := deploys(t, a.Client(), "").Deploys[0]; e.Outcome.Fingerprint == nil || e.Outcome.Fingerprint.BlindTrust == nil {
		t.Fatalf("the record of the trusting read: %+v", e.Outcome)
	}
	// The trust is the instance's now: a download rides on it only with
	// a yes, refused before anything is archived.
	_, err = s.Download(ctx, benchSoftwareLoad)
	if tiad.Kind(err) != tiad.KindDecisionRequired || !strings.Contains(err.Error(), "trusted a CPU's certificate blindly") || tiad.DeployIDOf(err) != "" {
		t.Fatalf("a download after the trusting read: %v", err)
	}
	if n := len(deploys(t, a.Client(), "").Deploys); n != 2 {
		t.Fatalf("records %d, want the refused download to leave none", n)
	}
}

// contractDeployFingerprintFails: a read that fails inside TIA's side
// leaves the load as it is.
func contractDeployFingerprintFails(t *testing.T, start Starter) {
	a, s := compiledBench(t, start)
	a.SetFingerprint(t, FingerprintScript{Fails: "the fingerprint service is busy"})
	r, err := s.Download(ctx, benchSoftwareLoad)
	if err != nil || !r.Downloaded || !r.CompareEqual || r.Deploy == nil || r.Deploy.Fingerprint != nil || !strings.Contains(r.Deploy.FingerprintError, "the fingerprint service is busy") || !r.Deploy.Recorded {
		t.Fatalf("a load whose read fails: %+v %v", r.Deploy, err)
	}
	if e := deploys(t, a.Client(), "").Deploys[0]; e.Status != tiad.DeployLoaded {
		t.Fatalf("the record: %+v", e)
	}
	if r.Deploy.BlindTrustAfter != nil {
		t.Fatalf("a failed read without the opt-in trusted: %+v", r.Deploy.BlindTrustAfter)
	}
	// With the opt-in, a read that fails may have trusted: the instance
	// counts it as trust, and the record says so.
	req := benchSoftwareLoad
	req.Fingerprint = &tiad.FingerprintRequest{TrustUnverifiedCertificate: true}
	r, err = s.Download(ctx, req)
	if err != nil || r.Deploy == nil || r.Deploy.Fingerprint != nil || r.Deploy.BlindTrustAfter == nil || r.Deploy.BlindTrustAfter.Call != "fingerprint read" {
		t.Fatalf("a load whose read with the opt-in fails: %+v %v", r.Deploy, err)
	}
	if e := deploys(t, a.Client(), "").Deploys[0]; e.Outcome == nil || e.Outcome.BlindTrustAfter == nil {
		t.Fatalf("the record of a failed trusting read: %+v", e.Outcome)
	}
	if _, err := s.Download(ctx, benchSoftwareLoad); tiad.Kind(err) != tiad.KindDecisionRequired || !strings.Contains(err.Error(), "trusted a CPU's certificate blindly") {
		t.Fatalf("a download after a failed trusting read: %v", err)
	}
}

// contractDeployListQuery: the list takes an IPv4 address and nothing
// else, and never needs TIA.
func contractDeployListQuery(t *testing.T, start Starter) {
	a := start(t, Config{Targets: []string{benchCPU}})
	c := a.Client()
	a.SetGates(t, []tiad.Gate{{Name: "tia", State: tiad.GateFailed, Detail: "no TIA"}})
	if l := deploys(t, c, ""); len(l.Deploys) != 0 || l.Deploys == nil {
		t.Fatalf("no deploys yet, TIA gated: %+v", l)
	}
	for _, q := range []string{"?address=plc-1", "?addr=192.0.2.10", "?address=192.0.2.10&address=192.0.2.11"} {
		status, b := raw(t, c, "GET", "/v1/deploys"+q, nil)
		if status != http.StatusBadRequest || envelopeKind(b) != tiad.KindBadRequest {
			t.Errorf("GET /v1/deploys%s: HTTP %d %s, want 400", q, status, b)
		}
	}
}
