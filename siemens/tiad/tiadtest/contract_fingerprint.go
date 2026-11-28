package tiadtest

import (
	"net/http"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/siemens/tiad"
)

// The fingerprint half of the contract (siemens-ide.md §3.8): a read-only
// online read of TIA's fingerprint of a session's CPU, which fails rather
// than answer part of a read, and the TIA instance's blind trust, which
// TIA keeps until the instance ends: every later online call reports it, and a
// download that would ride on it needs decisions.acceptBlindTrust.
func contractFingerprint(t *testing.T, start Starter) {
	t.Run("Read", func(t *testing.T) { contractFingerprintRead(t, start) })
	t.Run("BlindTrust", func(t *testing.T) { contractFingerprintBlindTrust(t, start) })
	t.Run("Retired", func(t *testing.T) { contractFingerprintRetired(t, start) })
	t.Run("FailsClosed", func(t *testing.T) { contractFingerprintFailsClosed(t, start) })
	t.Run("Gates", func(t *testing.T) { contractFingerprintGates(t, start) })
	t.Run("PasswordOnly", func(t *testing.T) { contractFingerprintPasswordOnly(t, start) })
}

// contractFingerprintPasswordOnly: on a CPU with access control on, TIA's
// fingerprint service asks a password-only logon (OnlineReadAccessPassword,
// the bench 1510SP, #1336) after its TLS question. A TIA that kept a logon
// for its instance asks the project's connection nothing, so the read
// reaches the service: the password-only logon is answered no, whatever
// password the request carries, and the read is unavailable, naming
// verify. The trust it answered on the opt-in is recorded (the read failed
// after it); without the opt-in the TLS question refuses it first.
func contractFingerprintPasswordOnly(t *testing.T, start Starter) {
	a, s := compiledBench(t, start)
	a.SetLogon(t, LogonScript{KeepsLogon: true})
	hw := AcceptHardware(tiad.DownloadRequest{Secrets: BenchDownloadSecrets(), Address: benchCPU, Hardware: true, Software: true, Announced: true, Decisions: tiad.Decisions{ResetData: tiad.Yes(), StartAfter: tiad.No()}})
	if r, err := s.Download(ctx, hw); err != nil || !r.Downloaded || r.Deploy == nil || r.Deploy.FingerprintUnavailable == "" {
		t.Fatalf("the load that turns access control on: %+v %v", r, err)
	}
	// The instance keeps the compare's logon.
	if r, err := s.CompareWith(ctx, BenchCompare); err != nil || !r.Equal {
		t.Fatalf("a compare that logs on: %+v %v", r, err)
	}
	a.SetFingerprint(t, FingerprintScript{AsksTLS: true})
	_, err := s.Fingerprint(ctx, tiad.FingerprintRequest{Secrets: BenchLogin})
	if tiad.Kind(err) != tiad.KindOperationFailed || !strings.Contains(err.Error(), "trustUnverifiedCertificate: true") {
		t.Fatalf("no opt-in: %v, want the TLS question refused first", err)
	}
	r, err := s.Fingerprint(ctx, tiad.FingerprintRequest{Secrets: BenchLogin, TrustUnverifiedCertificate: true})
	lockedFingerprint(t, "the service's password-only logon", err, true)
	if !strings.Contains(err.Error(), "No password was sent; ") || strings.Contains(err.Error(), "Nothing was trusted") || len(r.Items) != 0 {
		t.Fatalf("after a trust: %+v %v", r, err)
	}
	// The read failed after its trust: TIA keeps it, so the instance
	// reports it, and a download needs the yes.
	if c, err := s.CompareWith(ctx, BenchCompare); err != nil || c.BlindTrust == nil || c.BlindTrust.Call != "fingerprint read" {
		t.Fatalf("a compare after the read: %+v %v, want the read's trust", c, err)
	}
}

func fpValue(t *testing.T, r tiad.FingerprintResult, id string) string {
	t.Helper()
	v, ok := r.Value(id)
	if !ok {
		t.Fatalf("the read holds no %s: %+v", id, r.Items)
	}
	return v
}

var benchSoftwareLoad = tiad.DownloadRequest{Secrets: BenchDownloadSecrets(), Address: benchCPU, Software: true, Announced: true, Decisions: tiad.Decisions{ResetData: tiad.Yes(), StartAfter: tiad.No()}}

// contractFingerprintRead: TIA's items in TIA's order; DownloadId and the
// counters change with every load, Hardware not with a software load (#789
// stage (C)); no trust asked, none recorded. The hardware load that turns
// access control on leaves no fingerprint to read (#1336).
func contractFingerprintRead(t *testing.T, start Starter) {
	a, s := compiledBench(t, start)
	r, err := s.Fingerprint(ctx, tiad.FingerprintRequest{Secrets: BenchLogin})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, it := range r.Items {
		ids = append(ids, it.Identifier)
	}
	if got := strings.Join(ids, ","); got != "TextLists,Hardware,Certificates,Routing,Connection,DownloadId,ChangeCounterPersistent,ChangeCounterRetain" {
		t.Fatalf("items %s", got)
	}
	if r.Address != benchCPU || r.TrustedBlindly == nil || len(r.TrustedBlindly) != 0 || r.BlindTrust != nil {
		t.Fatalf("a read that asked nothing: %+v", r)
	}
	if _, err := s.Download(ctx, benchSoftwareLoad); err != nil {
		t.Fatal(err)
	}
	r2, err := s.Fingerprint(ctx, tiad.FingerprintRequest{Secrets: BenchLogin})
	if err != nil {
		t.Fatal(err)
	}
	if fpValue(t, r2, "DownloadId") == fpValue(t, r, "DownloadId") || fpValue(t, r2, "ChangeCounterRetain") == fpValue(t, r, "ChangeCounterRetain") {
		t.Errorf("a software load left DownloadId or the counters as they were: %+v -> %+v", r.Items, r2.Items)
	}
	if fpValue(t, r2, "Hardware") != fpValue(t, r, "Hardware") {
		t.Errorf("a software load changed Hardware: %+v -> %+v", r.Items, r2.Items)
	}
	// The bench station's hardware load turns its access control on: from
	// then on the CPU has no fingerprint to read (#1336).
	if _, err := s.Download(ctx, AcceptHardware(tiad.DownloadRequest{Secrets: BenchDownloadSecrets(), Address: benchCPU, Hardware: true, Software: true, Announced: true, Decisions: tiad.Decisions{ResetData: tiad.Yes(), StartAfter: tiad.No()}})); err != nil {
		t.Fatal(err)
	}
	_, err = s.Fingerprint(ctx, tiad.FingerprintRequest{Secrets: BenchLogin})
	lockedFingerprint(t, "a read after the hardware load", err, false)
	if l := a.Loaded(t); len(l) != 2 {
		t.Fatalf("loads %+v, want the two downloads only", l)
	}
}

// contractFingerprintBlindTrust: a read the CPU asks TLS trust for fails
// without the opt-in, naming it, and records no trust; with it, TIA's
// instance trusts for good: a later read asks nothing yet reports the
// trust it rides on, a compare reports it, and a download, a dry run too,
// is refused before anything reaches the CPU until it says
// decisions.acceptBlindTrust yes.
func contractFingerprintBlindTrust(t *testing.T, start Starter) {
	a, s := compiledBench(t, start)
	a.SetFingerprint(t, FingerprintScript{AsksTLS: true})
	_, err := s.Fingerprint(ctx, tiad.FingerprintRequest{Secrets: BenchLogin})
	if tiad.Kind(err) != tiad.KindOperationFailed || !strings.Contains(err.Error(), "trust the TLS certificate of PLC_1") || !strings.Contains(err.Error(), "trustUnverifiedCertificate: true") {
		t.Fatalf("a read the CPU asks TLS trust for, no opt-in: %v, want operation_failed naming the question and the opt-in", err)
	}
	// Refused, so nothing is trusted: a download needs no yes.
	if r, err := s.Download(ctx, benchSoftwareLoad); err != nil || r.BlindTrust != nil {
		t.Fatalf("a download after a refused read: %+v %v", r, err)
	}
	r, err := s.Fingerprint(ctx, tiad.FingerprintRequest{Secrets: BenchLogin, TrustUnverifiedCertificate: true})
	if err != nil || len(r.TrustedBlindly) != 1 || !strings.Contains(r.TrustedBlindly[0], "trust the TLS certificate of PLC_1") {
		t.Fatalf("a read with the opt-in: %+v %v, want the question trusted blindly", r, err)
	}
	bt := r.BlindTrust
	if bt == nil || bt.Call != "fingerprint read" || bt.Session != s.ID || bt.Address != benchCPU || bt.Since == "" || len(bt.Questions) != 1 {
		t.Fatalf("the instance's blind trust after the read: %+v", bt)
	}
	// TIA asks nothing again, and the read says what it rides on.
	r2, err := s.Fingerprint(ctx, tiad.FingerprintRequest{Secrets: BenchLogin})
	if err != nil || len(r2.TrustedBlindly) != 0 || r2.BlindTrust == nil || r2.BlindTrust.Since != bt.Since {
		t.Fatalf("a later read without the opt-in: %+v %v, want it to succeed riding on the earlier trust, and say so", r2, err)
	}
	if cmp, err := s.CompareWith(ctx, BenchCompare); err != nil || cmp.BlindTrust == nil || cmp.BlindTrust.Since != bt.Since {
		t.Fatalf("a compare in the trusting instance: %+v %v, want the trust reported", cmp, err)
	}
	loaded := len(a.Loaded(t))
	for _, c := range []struct {
		name   string
		accept *bool
		dryRun bool
		kind   string
	}{
		{"unanswered", nil, false, tiad.KindDecisionRequired},
		{"no", tiad.No(), false, tiad.KindDeclined},
		{"dry run, unanswered", nil, true, tiad.KindDecisionRequired},
	} {
		req := benchSoftwareLoad
		req.DryRun = c.dryRun
		req.Decisions.AcceptBlindTrust = c.accept
		_, err := s.Download(ctx, req)
		if tiad.Kind(err) != c.kind || !strings.Contains(err.Error(), "trusted a CPU's certificate blindly") || !strings.Contains(err.Error(), "restart the agent") ||
			!strings.Contains(err.Error(), "nothing was sent to the CPU") {
			t.Errorf("%s: %v, want %s naming the trust and the fix", c.name, err, c.kind)
			continue
		}
		if qs := tiad.Questions(err); len(qs) != 1 || qs[0].ID != tiad.QuestionBlindTrust {
			t.Errorf("%s: questions %+v, want the blind-trust question", c.name, qs)
		}
		if b := tiad.BlindTrustOf(err); b == nil || b.Since != bt.Since {
			t.Errorf("%s: blind trust %+v, want the record", c.name, b)
		}
	}
	if l := a.Loaded(t); len(l) != loaded {
		t.Fatalf("a refused download loaded: %+v", l)
	}
	req := benchSoftwareLoad
	req.Decisions.AcceptBlindTrust = tiad.Yes()
	dr, err := s.Download(ctx, req)
	if err != nil || !dr.Downloaded || dr.BlindTrust == nil || dr.BlindTrust.Since != bt.Since {
		t.Fatalf("a download that accepts the blind trust: %+v %v, want it loaded with the trust it rode on", dr, err)
	}
	// The yes is a decision like any other: a bad type is a bad request.
	status, b := rawJSON(t, a.Client(), "POST", "/v1/sessions/"+s.ID+"/download", map[string]any{"address": benchCPU, "software": true, "announced": true,
		"decisions": map[string]any{"resetData": true, "startAfter": false, "acceptBlindTrust": "yes"}})
	if status != http.StatusBadRequest || envelopeKind(b) != tiad.KindBadRequest {
		t.Errorf("acceptBlindTrust as a string: HTTP %d %s", status, b)
	}
}

// contractFingerprintRetired (#789 D1): the TIA instance that trusted
// blindly is retired once no session is left on it, and the next one
// trusts nothing, so a deploy's own download never rides on an earlier
// read's trust. (TIA opens one project at a time, so no other session can
// keep the instance up: closing the reading session retires it.)
func contractFingerprintRetired(t *testing.T, start Starter) {
	a, s := compiledBench(t, start)
	c := a.Client()
	a.SetFingerprint(t, FingerprintScript{AsksTLS: true})
	if r, err := s.Fingerprint(ctx, tiad.FingerprintRequest{Secrets: BenchLogin, TrustUnverifiedCertificate: true}); err != nil || r.BlindTrust == nil {
		t.Fatalf("a read with the opt-in: %+v %v", r, err)
	}
	// The gate holds while the reading session is open.
	if _, err := s.Download(ctx, benchSoftwareLoad); tiad.Kind(err) != tiad.KindDecisionRequired || !strings.Contains(err.Error(), "close every session") {
		t.Fatalf("a download while the trusting instance is still up: %v, want decision_required naming the fix", err)
	}
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	// No session left: the next instance trusts nothing.
	loaded := len(a.Loaded(t))
	s2, err := c.CreateProject(ctx, "bench", BenchStation(), BenchSecrets)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.PutFile(ctx, "src/Main.scl", []byte(programSCL)); err != nil {
		t.Fatal(err)
	}
	if _, err := s2.Software(ctx, tiad.SoftwareRequest{Group: "nautilus", Sources: []string{"src/Main.scl"}, Prune: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := s2.Compile(ctx, true, true); err != nil {
		t.Fatal(err)
	}
	if cmp, err := s2.CompareWith(ctx, BenchCompare); err != nil || cmp.BlindTrust != nil {
		t.Fatalf("a compare in the next instance: %+v %v, want no trust", cmp, err)
	}
	dr, err := s2.Download(ctx, benchSoftwareLoad)
	if err != nil || !dr.Downloaded || dr.BlindTrust != nil {
		t.Fatalf("a download in the next instance, no acceptBlindTrust: %+v %v, want it loaded riding on nothing", dr, err)
	}
	if l := a.Loaded(t); len(l) != loaded+1 {
		t.Fatalf("loads %+v, want the one download", l)
	}
	// The new instance asks the TLS question again: TIA's trust went with
	// the old one, not only tiad's record of it.
	if _, err := s2.Fingerprint(ctx, tiad.FingerprintRequest{Secrets: BenchLogin}); tiad.Kind(err) != tiad.KindOperationFailed || !strings.Contains(err.Error(), "trust the TLS certificate of PLC_1") {
		t.Fatalf("a read without the opt-in in the next instance: %v, want TIA to ask the TLS question again", err)
	}
	if r, err := s2.Fingerprint(ctx, tiad.FingerprintRequest{Secrets: BenchLogin, TrustUnverifiedCertificate: true}); err != nil || r.BlindTrust == nil || r.BlindTrust.Session != s2.ID {
		t.Fatalf("a read with the opt-in in the next instance: %+v %v, want a new record of this session's trust", r, err)
	}
}

// contractFingerprintFailsClosed: a read that is not whole fails; a read
// that failed inside TIA's side after the opt-in counts as trust, since
// TIA may have trusted before it failed.
func contractFingerprintFailsClosed(t *testing.T, start Starter) {
	a, s := compiledBench(t, start)
	for _, c := range []struct {
		name string
		fs   FingerprintScript
		says string
	}{
		{"no items", FingerprintScript{NoItems: true}, "no items"},
		{"an identifier twice", FingerprintScript{Items: []tiad.FingerprintItem{{Identifier: "Hardware", Value: "a"}, {Identifier: "Hardware", Value: "b"}}}, `"Hardware" twice`},
		{"no identifier", FingerprintScript{Items: []tiad.FingerprintItem{{Identifier: "", Value: "a"}}}, "no identifier"},
		{"a value too long", FingerprintScript{Items: []tiad.FingerprintItem{{Identifier: "Hardware", Value: strings.Repeat("x", 1025)}}}, "longer than 1024 bytes"},
		{"TIA fails", FingerprintScript{Fails: "the fingerprint service failed"}, "the fingerprint service failed"},
	} {
		a.SetFingerprint(t, c.fs)
		r, err := s.Fingerprint(ctx, tiad.FingerprintRequest{Secrets: BenchLogin})
		if tiad.Kind(err) != tiad.KindOperationFailed || !strings.Contains(err.Error(), c.says) || len(r.Items) != 0 {
			t.Errorf("%s: %+v %v, want operation_failed saying %q", c.name, r, err, c.says)
		}
	}
	// None took the opt-in, so nothing is trusted.
	if _, err := s.Download(ctx, benchSoftwareLoad); err != nil {
		t.Fatalf("a download after failed reads without the opt-in: %v", err)
	}
	a.SetFingerprint(t, FingerprintScript{Fails: "the fingerprint service failed"})
	if _, err := s.Fingerprint(ctx, tiad.FingerprintRequest{Secrets: BenchLogin, TrustUnverifiedCertificate: true}); tiad.Kind(err) != tiad.KindOperationFailed {
		t.Fatalf("a failing read with the opt-in: %v", err)
	}
	_, err := s.Download(ctx, benchSoftwareLoad)
	b := tiad.BlindTrustOf(err)
	if tiad.Kind(err) != tiad.KindDecisionRequired || b == nil || len(b.Questions) != 1 || !strings.HasPrefix(b.Questions[0], "unknown:") {
		t.Fatalf("a download after a read that failed after its opt-in: %v %+v, want decision_required on an unknown trust", err, b)
	}
}

// contractFingerprintGates: the read is an online call like a compare: a
// CPU outside TIAD_TARGETS is never gone online to, a pin that cannot be
// checked refuses it, and the request takes only its one option.
func contractFingerprintGates(t *testing.T, start Starter) {
	a2 := start(t, Config{})
	c2 := a2.Client()
	installGSD(t, c2)
	s2, err := c2.CreateProject(ctx, "p", BenchStation(), BenchSecrets)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s2.Fingerprint(ctx, tiad.FingerprintRequest{Secrets: BenchLogin, TrustUnverifiedCertificate: true}); tiad.Kind(err) != tiad.KindTargetRefused {
		t.Fatalf("a read from an unlisted CPU: %v", err)
	}
	a, s := compiledBench(t, start)
	c := a.Client()
	status, b := rawJSON(t, c, "POST", "/v1/sessions/"+s.ID+"/fingerprint", map[string]any{"address": benchCPU})
	if status != http.StatusBadRequest || envelopeKind(b) != tiad.KindBadRequest || !strings.Contains(string(b), "address") {
		t.Errorf("a read naming an address: HTTP %d %s, want 400 bad_request naming it", status, b)
	}
	a.SetDevice(t, BenchDevice(), true)
	if _, err := c.SetPin(ctx, benchCPU, benchPinRequest); err != nil {
		t.Fatal(err)
	}
	a.SetFingerprint(t, FingerprintScript{AsksTLS: true})
	if _, err := s.Fingerprint(ctx, tiad.FingerprintRequest{Secrets: BenchLogin, TrustUnverifiedCertificate: true}); tiad.Kind(err) != tiad.KindDeviceUnverified {
		t.Fatalf("a read from a pinned CPU it cannot identify: %v", err)
	}
	// Refused before TIA: nothing was trusted.
	a.SetDevice(t, BenchDevice(), false)
	if _, err := s.Download(ctx, benchSoftwareLoad); err != nil {
		t.Fatalf("a download after a refused read: %v", err)
	}
}
