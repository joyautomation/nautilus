package tiadtest

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/siemens/tiad"
)

// The deploy verify half of the contract (siemens-ide.md §3.5, §7, D7;
// #789 D1b): a copy of the archive, hashed against the record, opened in a
// session of its own and compared with the CPU; a swapped archive is
// broken, never equal; a record whose address is not its archived
// project's CPU's is refused before anything goes online.
func contractVerify(t *testing.T, start Starter) {
	t.Run("Equal", func(t *testing.T) { contractVerifyEqual(t, start) })
	t.Run("BrokenNeverEqual", func(t *testing.T) { contractVerifyBroken(t, start) })
	t.Run("AddressMismatch", func(t *testing.T) { contractVerifyMismatch(t, start) })
	t.Run("ChangedOutside", func(t *testing.T) { contractVerifyChangedOutside(t, start) })
	t.Run("Refusals", func(t *testing.T) { contractVerifyRefusals(t, start) })
	t.Run("FingerprintTrust", func(t *testing.T) { contractVerifyFingerprintTrust(t, start) })
	t.Run("DevicePin", func(t *testing.T) { contractVerifyDevicePin(t, start) })
}

// contractVerifyFingerprintTrust: a verify's fingerprint read takes its
// own opt-in; refused, it names the verify's opt-in and the verdict
// stands; trusted, the instance's trust is retired with the verify's
// session, so the next download needs no acceptBlindTrust; a read that
// fails leaves the verdict as it is.
func contractVerifyFingerprintTrust(t *testing.T, start Starter) {
	a, d := deployed(t, start)
	c := a.Client()
	a.SetFingerprint(t, FingerprintScript{AsksTLS: true})
	r := verify(t, c, tiad.VerifyRequest{Deploy: d.ID, Fingerprint: &tiad.FingerprintRequest{}})
	if r.Verdict != tiad.VerifyEqual || r.Fingerprint != nil || !strings.Contains(r.FingerprintError, "fingerprint.trustUnverifiedCertificate: true") ||
		!strings.Contains(r.FingerprintError, "until the verify's session closes") {
		t.Fatalf("a refused read: %+v", r)
	}
	r = verify(t, c, tiad.VerifyRequest{Deploy: d.ID, Fingerprint: &tiad.FingerprintRequest{TrustUnverifiedCertificate: true}})
	if r.Verdict != tiad.VerifyEqual || r.Fingerprint == nil || len(r.Fingerprint.TrustedBlindly) != 1 || r.Fingerprint.BlindTrust == nil {
		t.Fatalf("a trusted read: %+v %+v", r, r.Fingerprint)
	}
	noSession(t, c)
	// Retired with the session: a deploy in a new one rides on nothing.
	s, err := c.CreateProject(ctx, "bench", BenchStation(), BenchSecrets)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Compile(ctx, true, true); err != nil {
		t.Fatal(err)
	}
	if dl, err := s.Download(ctx, benchSoftwareLoad); err != nil || dl.BlindTrust != nil {
		t.Fatalf("a download after the verify's trusted read: %+v %v", dl, err)
	}
	closeFirst(t, s)
	a.SetFingerprint(t, FingerprintScript{Fails: "the CPU went away"})
	r = verify(t, c, tiad.VerifyRequest{Deploy: d.ID, Fingerprint: &tiad.FingerprintRequest{}})
	if r.Fingerprint != nil || !strings.Contains(r.FingerprintError, "the CPU went away") || r.Verdict != tiad.VerifyDifferent {
		t.Fatalf("a failed read: %+v", r)
	}
	noSession(t, c)
}

// contractVerifyDevicePin: a verify is an online call: a pinned CPU that
// changed is refused before the compare.
func contractVerifyDevicePin(t *testing.T, start Starter) {
	a, d := deployed(t, start)
	c := a.Client()
	if _, err := c.SetPin(ctx, benchCPU, benchPinRequest); err != nil {
		t.Fatalf("pin: %v", err)
	}
	a.SetDevice(t, tiad.DeviceIdentity{Order: "6ES7 510-1DK03-0AB0", Firmware: "V3.2"}, false)
	_, err := c.VerifyDeploy(ctx, tiad.VerifyRequest{Deploy: d.ID})
	if tiad.Kind(err) != tiad.KindDeviceChanged || !strings.Contains(err.Error(), "verify") {
		t.Fatalf("verify of a changed CPU: %v", err)
	}
	noSession(t, c)
}

// deployed is an agent with the bench program loaded by a recorded
// deploy, its session closed.
func deployed(t *testing.T, start Starter, targets ...string) (Target, *tiad.DeployResult) {
	t.Helper()
	a, s := compiledBench(t, start, targets...)
	r, err := s.Download(ctx, benchSoftwareLoad)
	if err != nil || r.Deploy == nil {
		t.Fatalf("download: %+v %v", r, err)
	}
	closeFirst(t, s)
	return a, r.Deploy
}

func verify(t *testing.T, c *tiad.Client, req tiad.VerifyRequest) tiad.VerifyResult {
	t.Helper()
	r, err := c.VerifyDeploy(ctx, req)
	if err != nil {
		t.Fatalf("verify %s: %v", req.Deploy, err)
	}
	return r
}

// noSession checks the verify left no session: TIA opens one project at
// a time, so a verify that kept one would block the next deploy.
func noSession(t *testing.T, c *tiad.Client) {
	t.Helper()
	ss, err := c.Sessions(ctx)
	if err != nil || len(ss) != 0 {
		t.Fatalf("sessions after a verify: %+v %v, want none", ss, err)
	}
}

func isTrue(b *bool) bool  { return b != nil && *b }
func isFalse(b *bool) bool { return b != nil && !*b }

// contractVerifyEqual: right after a deploy the CPU runs it: equal, the
// record loaded and latest, not changed outside; with a fingerprint read,
// the DownloadId and Hardware are the record's. The session is closed
// after, so the next project opens.
func contractVerifyEqual(t *testing.T, start Starter) {
	a, d := deployed(t, start)
	c := a.Client()
	r := verify(t, c, tiad.VerifyRequest{Deploy: d.ID})
	if r.Verdict != tiad.VerifyEqual || r.Deploy != d.ID || r.Address != benchCPU || r.Status != tiad.DeployLoaded || r.Latest != d.ID ||
		!isFalse(r.ChangedOutside) || len(r.Differences) != 0 || r.Broken != "" || r.Session == "" || r.Fingerprint != nil || r.CloseError != "" {
		t.Fatalf("verify right after the deploy: %+v", r)
	}
	noSession(t, c)
	r = verify(t, c, tiad.VerifyRequest{Deploy: d.ID, Fingerprint: &tiad.FingerprintRequest{}})
	f := r.Fingerprint
	if r.Verdict != tiad.VerifyEqual || f == nil || r.FingerprintError != "" || !isTrue(f.DownloadIDSame) || !isTrue(f.HardwareSame) {
		t.Fatalf("verify with a fingerprint read: %+v %+v", r, f)
	}
	if v, _ := (tiad.FingerprintResult{Items: f.Items}).Value("DownloadId"); v != d.DownloadID {
		t.Fatalf("the verify's DownloadId %q, the record's %q", v, d.DownloadID)
	}
	noSession(t, c)
	// The verify left nothing behind: the next project opens.
	s, err := c.CreateProject(ctx, "bench", BenchStation(), BenchSecrets)
	if err != nil {
		t.Fatalf("a project after the verify: %v", err)
	}
	// While a project is open, a verify is busy, naming it.
	_, err = c.VerifyDeploy(ctx, tiad.VerifyRequest{Deploy: d.ID})
	if tiad.Kind(err) != tiad.KindBusy || !strings.Contains(err.Error(), s.ID) {
		t.Fatalf("verify while %s is open: %v", s.ID, err)
	}
}

// contractVerifyBroken: an archive swapped for another valid one, which
// the CPU runs right now, is broken, never equal, and is not opened:
// were it opened, it would compare equal.
func contractVerifyBroken(t *testing.T, start Starter) {
	a, s := compiledBench(t, start)
	c := a.Client()
	r1, err := s.Download(ctx, benchSoftwareLoad)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := s.Download(ctx, benchSoftwareLoad)
	if err != nil {
		t.Fatal(err)
	}
	closeFirst(t, s)
	d1, d2 := r1.Deploy, r2.Deploy
	// Before the swap: the older deploy is superseded, so its CPU differs
	// as expected (not changed outside); the newer one is equal.
	if r := verify(t, c, tiad.VerifyRequest{Deploy: d1.ID}); r.Verdict != tiad.VerifyDifferent || r.Latest != d2.ID || !isFalse(r.ChangedOutside) {
		t.Fatalf("the superseded deploy: %+v", r)
	}
	if r := verify(t, c, tiad.VerifyRequest{Deploy: d2.ID}); r.Verdict != tiad.VerifyEqual {
		t.Fatalf("the latest deploy: %+v", r)
	}
	a.SwapArchive(t, d1.Archive.File, d2.Archive.File)
	r := verify(t, c, tiad.VerifyRequest{Deploy: d1.ID})
	if r.Verdict != tiad.VerifyBroken || r.ChangedOutside != nil || r.Session != "" || r.Differences != nil ||
		!strings.Contains(r.Broken, d1.Archive.File) || !strings.Contains(r.Broken, "not the record's") {
		t.Fatalf("a swapped archive: %+v, want broken, never compared", r)
	}
	noSession(t, c)
}

// contractVerifyMismatch: a record whose address is another CPU than its
// archived project's is refused, naming both; the other CPU runs the same
// program, so a verify that went online there would say equal.
func contractVerifyMismatch(t *testing.T, start Starter) {
	a, d := deployed(t, start, benchCPU, otherCPU)
	c := a.Client()
	a.LoadOutside(t, otherCPU, d.Archive.File)
	a.SetDeployAddress(t, d.ID, otherCPU)
	_, err := c.VerifyDeploy(ctx, tiad.VerifyRequest{Deploy: d.ID})
	rec, proj, ok := tiad.VerifyMismatchOf(err)
	if tiad.Kind(err) != tiad.KindCPUMismatch || statusOf(err) != http.StatusConflict || !ok || rec != otherCPU || proj != benchCPU ||
		!strings.Contains(err.Error(), otherCPU) || !strings.Contains(err.Error(), benchCPU) || !strings.Contains(err.Error(), "before anything went online") {
		t.Fatalf("a record naming another CPU: %v (%q %q %v)", err, rec, proj, ok)
	}
	noSession(t, c)
}

// contractVerifyChangedOutside: a CPU loaded without the agent since its
// latest deploy compares different: changed outside, its DownloadId not
// the record's.
func contractVerifyChangedOutside(t *testing.T, start Starter) {
	a, d := deployed(t, start)
	c := a.Client()
	a.LoadOutside(t, benchCPU, "")
	r := verify(t, c, tiad.VerifyRequest{Deploy: d.ID, Fingerprint: &tiad.FingerprintRequest{}})
	if r.Verdict != tiad.VerifyDifferent || !isTrue(r.ChangedOutside) || len(r.Differences) == 0 || r.Fingerprint == nil ||
		!isFalse(r.Fingerprint.DownloadIDSame) || !isTrue(r.Fingerprint.HardwareSame) {
		t.Fatalf("a CPU loaded outside the agent: %+v %+v", r, r.Fingerprint)
	}
	// The deploy again: equal once more.
	s, err := c.CreateProject(ctx, "bench", BenchStation(), BenchSecrets)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.PutFile(ctx, "src/Main.scl", []byte(programSCL)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Software(ctx, tiad.SoftwareRequest{Group: "nautilus", Sources: []string{"src/Main.scl"}, Prune: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Compile(ctx, true, true); err != nil {
		t.Fatal(err)
	}
	r2, err := s.Download(ctx, benchSoftwareLoad)
	if err != nil {
		t.Fatal(err)
	}
	closeFirst(t, s)
	if r := verify(t, c, tiad.VerifyRequest{Deploy: r2.Deploy.ID}); r.Verdict != tiad.VerifyEqual || !isFalse(r.ChangedOutside) {
		t.Fatalf("the redeploy: %+v", r)
	}
	if r := verify(t, c, tiad.VerifyRequest{Deploy: d.ID}); r.Verdict != tiad.VerifyDifferent || !isFalse(r.ChangedOutside) || r.Latest != r2.Deploy.ID {
		t.Fatalf("the first deploy after the redeploy: %+v", r)
	}
}

// contractVerifyRefusals: an unknown deploy, an unknown field, a record
// whose CPU is not a target, a gate.
func contractVerifyRefusals(t *testing.T, start Starter) {
	a, d := deployed(t, start)
	c := a.Client()
	if _, err := c.VerifyDeploy(ctx, tiad.VerifyRequest{Deploy: "d-20260101T000000000Z-00000000"}); tiad.Kind(err) != tiad.KindNotFound {
		t.Fatalf("an unknown deploy: %v", err)
	}
	if _, err := c.VerifyDeploy(ctx, tiad.VerifyRequest{}); tiad.Kind(err) != tiad.KindBadRequest {
		t.Fatalf("no deploy: %v", err)
	}
	for body, want := range map[string]string{`{"deploy":"` + d.ID + `","deplyo":"x"}`: "unknown field", `{}`: "deploy is required"} {
		st, b := rawWith(t, c, http.MethodPost, "/v1/deploys/verify", http.Header{"Authorization": {"Bearer " + c.Token}, "Content-Type": {"application/json"}}, []byte(body))
		if st != http.StatusBadRequest || !strings.Contains(string(b), want) {
			t.Fatalf("verify %s: HTTP %d %s", body, st, b)
		}
	}
	a.SetDeployAddress(t, d.ID, "192.0.2.77")
	if _, err := c.VerifyDeploy(ctx, tiad.VerifyRequest{Deploy: d.ID}); tiad.Kind(err) != tiad.KindTargetRefused {
		t.Fatalf("a record whose CPU is not a target: %v", err)
	}
	a.SetDeployAddress(t, d.ID, benchCPU)
	a.SetGates(t, []tiad.Gate{{Name: "tia", State: tiad.GateFailed, Detail: "no TIA"}})
	if _, err := c.VerifyDeploy(ctx, tiad.VerifyRequest{Deploy: d.ID}); tiad.Kind(err) != tiad.KindGate {
		t.Fatalf("a verify while a gate fails: %v", err)
	}
	a.SetGates(t, nil)
	if r := verify(t, c, tiad.VerifyRequest{Deploy: d.ID}); r.Verdict != tiad.VerifyEqual {
		t.Fatalf("once the gate passes: %+v", r)
	}
	noSession(t, c)
}

func statusOf(err error) int {
	var e *tiad.Error
	if !errors.As(err, &e) {
		return 0
	}
	return e.Status
}
