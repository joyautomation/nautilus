package tiadtest

import (
	"fmt"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/siemens/tiad"
)

// contractCredentials: a hardware load of a fullAccessUser station turns
// the CPU's access control on, and from then on going online to it asks
// credentials (#789 stage 5 (A)). Every online call (download and the
// compare after it, compare, fingerprint read, verify, upload) logs on as
// the user "nautilus" with the request's secrets.fullAccessPassword.
// Without it, or with a wrong one, the call fails naming why, a wrong one
// is never tried twice, and no password goes out while the agent's TIA
// instance trusts a certificate blindly. Before this, a CPU the agent made
// locked the agent out of it.
func contractCredentials(t *testing.T, start Starter) {
	a, s := compiledBench(t, start, benchCPU, otherCPU)
	c := a.Client()
	wrong := &tiad.OnlineSecrets{FullAccessPassword: "Wrong-access-2026"}
	refused := func(what string, err error, kind, why string) {
		t.Helper()
		if tiad.Kind(err) != kind || !strings.Contains(err.Error(), LoginQuestion+": "+why) {
			t.Fatalf("%s: %v, want %s naming %q", what, err, kind, why)
		}
	}

	// The first hardware load asks nothing going online (the CPU has no
	// access control yet), but the compare right after it would: a
	// download of a fullAccessUser station without the password is
	// refused before anything reaches the CPU, a dry run too.
	hw := AcceptHardware(tiad.DownloadRequest{Address: benchCPU, Hardware: true, Software: true, Announced: true, Decisions: tiad.Decisions{ResetData: tiad.Yes(), StartAfter: tiad.No()}})
	for _, req := range []tiad.DownloadRequest{hw, {Address: benchCPU, Software: true, Announced: true, DryRun: true, Decisions: tiad.Decisions{ResetData: tiad.Yes()}}} {
		if _, err := s.Download(ctx, req); tiad.Kind(err) != tiad.KindBadRequest || !strings.Contains(err.Error(), LoginDownloadNeedsPassword) {
			t.Fatalf("a download without the password (dry run %v): %v", req.DryRun, err)
		}
	}
	if n := len(a.Loaded(t)); n != 0 {
		t.Fatalf("loaded %d, want nothing", n)
	}
	hw.Secrets = BenchDownloadSecrets()
	if r, err := s.Download(ctx, hw); err != nil || !r.Downloaded || !r.CompareEqual {
		t.Fatalf("the first load with the password: %+v %v", r, err)
	}

	// Compare: with the password it is equal; without one, or with a
	// wrong one, refused, naming why.
	var err error
	if r, err := s.CompareWith(ctx, BenchCompare); err != nil || !r.Equal {
		t.Fatalf("compare with the password: %+v %v", r, err)
	}
	_, err = s.Compare(ctx)
	refused("compare without the password", err, tiad.KindOperationFailed, LoginNoPassword)
	_, err = s.CompareWith(ctx, tiad.CompareRequest{Secrets: wrong})
	refused("compare with a wrong password", err, tiad.KindOperationFailed, LoginAskedAgain)
	if strings.Contains(err.Error(), wrong.FullAccessPassword) {
		t.Fatalf("the refusal echoes the password: %v", err)
	}

	// A fingerprint read never logs on (#1336): TIA's fingerprint service
	// would need a blind trust and a password-only logon on a CPU with
	// access control on. Whatever password it carries, and with or
	// without the trust opt-in, it is refused as unavailable, naming
	// verify, trusting nothing and sending nothing: a compare with the
	// password still logs on after it, and nothing is trusted (the
	// downloads below would need acceptBlindTrust).
	a.SetFingerprint(t, FingerprintScript{AsksTLS: true})
	for _, req := range []tiad.FingerprintRequest{{}, {Secrets: BenchLogin}, {Secrets: wrong}, {Secrets: BenchLogin, TrustUnverifiedCertificate: true}, {TrustUnverifiedCertificate: true}} {
		r, err := s.Fingerprint(ctx, req)
		lockedFingerprint(t, fmt.Sprintf("a fingerprint read (password %v, opt-in %v)", req.Secrets != nil, req.TrustUnverifiedCertificate), err, false)
		if len(r.Items) != 0 || r.BlindTrust != nil {
			t.Fatalf("a refused fingerprint read returned %+v", r)
		}
		if strings.Contains(err.Error(), wrong.FullAccessPassword) || strings.Contains(err.Error(), BenchLogin.FullAccessPassword) {
			t.Fatalf("the refusal echoes a password: %v", err)
		}
	}
	a.SetFingerprint(t, FingerprintScript{})
	if r, err := s.CompareWith(ctx, BenchCompare); err != nil || !r.Equal || r.BlindTrust != nil {
		t.Fatalf("a compare with the password after the refused reads: %+v %v", r, err)
	}

	// A download with a wrong password: refused going online (in doubt,
	// as any refusal inside TIA's download call), nothing loaded; with
	// the right one, loaded and compared equal.
	sw := tiad.DownloadRequest{Address: benchCPU, Software: true, Announced: true, Decisions: tiad.Decisions{ResetData: tiad.Yes(), StartAfter: tiad.No()}}
	sw.Secrets = &tiad.Secrets{FullAccessPassword: wrong.FullAccessPassword}
	_, err = s.Download(ctx, sw)
	refused("a download with a wrong password", err, tiad.KindOutcomeUnknown, LoginAskedAgain)
	if n := len(a.Loaded(t)); n != 1 {
		t.Fatalf("loaded %d after the refused downloads, want 1", n)
	}
	sw.Secrets = BenchDownloadSecrets()
	r, err := s.Download(ctx, sw)
	if err != nil || !r.Downloaded || !r.CompareEqual || r.Deploy == nil {
		t.Fatalf("a download with the password: %+v %v", r, err)
	}
	// The deploy's fingerprint read after the load: unavailable, never an
	// error, in the response and in the deploy record; nothing trusted.
	if r.Deploy.Fingerprint != nil || r.Deploy.FingerprintError != "" || !strings.Contains(r.Deploy.FingerprintUnavailable, FingerprintUseVerify) ||
		r.Deploy.BlindTrustAfter != nil || r.BlindTrust != nil {
		t.Fatalf("the deploy's fingerprint read: %+v", r.Deploy)
	}
	if e := deploys(t, c, "").Deploys[0]; e.ID != r.Deploy.ID || e.Outcome == nil || e.Outcome.Status != tiad.DeployLoaded ||
		e.Outcome.FingerprintError != "" || !strings.Contains(e.Outcome.FingerprintUnavailable, FingerprintUseVerify) {
		t.Fatalf("the deploy record: %+v", e)
	}
	if r, err := s.Download(ctx, hw); err != nil || !r.Downloaded {
		t.Fatalf("a second hardware load with the password: %+v %v", r, err)
	}
	r, err = s.Download(ctx, sw)
	if err != nil || r.Deploy == nil {
		t.Fatalf("the last load: %+v %v", r, err)
	}
	id := r.Deploy.ID

	// A verify opens its own session: the project is closed first.
	closeFirst(t, s)
	// A verify compares online: it logs on too. Its fingerprint, asked,
	// is unavailable, never an error: the logged-on verdict stands as
	// the locked CPU's reference.
	_, err = c.VerifyDeploy(ctx, tiad.VerifyRequest{Deploy: id})
	refused("a verify without the password", err, tiad.KindOperationFailed, LoginNoPassword)
	if v, err := c.VerifyDeploy(ctx, tiad.VerifyRequest{Deploy: id, Secrets: BenchLogin}); err != nil || v.Verdict != tiad.VerifyEqual {
		t.Fatalf("a verify with the password: %+v %v", v, err)
	}
	if v, err := c.VerifyDeploy(ctx, tiad.VerifyRequest{Deploy: id, Secrets: BenchLogin, Fingerprint: &tiad.FingerprintRequest{TrustUnverifiedCertificate: true}}); err != nil ||
		v.Verdict != tiad.VerifyEqual || v.Fingerprint != nil || v.FingerprintError != "" || !strings.Contains(v.FingerprintUnavailable, FingerprintUseVerify) {
		t.Fatalf("a verify with its fingerprint: %+v %v", v, err)
	}
	if _, err := c.VerifyDeploy(ctx, tiad.VerifyRequest{Deploy: id, Fingerprint: &tiad.FingerprintRequest{Secrets: BenchLogin}}); tiad.Kind(err) != tiad.KindBadRequest {
		t.Fatalf("a verify with the password in its fingerprint: %v, want the client's bad_request", err)
	}

	// An upload logs on too.
	s2, err := c.CreateProject(ctx, "upload", BenchStation(), BenchSecrets)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s2.UploadWith(ctx, tiad.UploadRequest{})
	refused("an upload without the password", err, tiad.KindOperationFailed, LoginNoPassword)
	if up, err := s2.UploadWith(ctx, tiad.UploadRequest{Secrets: BenchLogin}); err != nil {
		t.Fatalf("an upload with the password: %+v %v", up, err)
	}
	if n := len(a.Loaded(t)); n != 4 {
		t.Fatalf("loaded %d, want the four loads with the password", n)
	}

	closeFirst(t, s2)

	// No password follows a blind trust. A fingerprint read of another
	// CPU, one without access control, trusts blindly; from then on the
	// instance sends no password, so a compare is refused, and a download
	// of a fullAccessUser station, which would have to log on, is refused
	// before anything reaches the CPU.
	s3, err := c.CreateProject(ctx, "trust", BenchStation(), BenchSecrets)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s3.Compile(ctx, true, true); err != nil {
		t.Fatal(err)
	}
	trustBlindlyElsewhere(t, a, s3)
	blocked := func(what string, err error, kind string) {
		t.Helper()
		if tiad.Kind(err) != kind || !strings.Contains(err.Error(), "password not sent: this agent's TIA instance trusted an unverified CPU certificate (") ||
			!strings.Contains(err.Error(), "close every session") {
			t.Fatalf("%s on an instance that trusts blindly: %v", what, err)
		}
	}
	_, err = s3.CompareWith(ctx, BenchCompare)
	blocked("a compare", err, tiad.KindOperationFailed)
	_, err = s3.Download(ctx, AcceptHardware(tiad.DownloadRequest{Secrets: BenchDownloadSecrets(), Address: benchCPU, Hardware: true, Software: true, Announced: true,
		Decisions: tiad.Decisions{ResetData: tiad.Yes(), StartAfter: tiad.No(), AcceptBlindTrust: tiad.Yes()}}))
	blocked("a download of a fullAccessUser station", err, tiad.KindBadRequest)
	if n := len(a.Loaded(t)); n != 4 {
		t.Fatalf("loaded %d after the refused download, want 4", n)
	}
	a.SetFingerprint(t, FingerprintScript{})
	// The last session closing retires the instance and its trust.
	closeFirst(t, s3)
	if v, err := c.VerifyDeploy(ctx, tiad.VerifyRequest{Deploy: id, Secrets: BenchLogin}); err != nil || v.Verdict != tiad.VerifyEqual {
		t.Fatalf("a verify once the trusting instance was retired: %+v %v", v, err)
	}
}

// lockedFingerprint fails t unless err is the agent's refusal of a
// fingerprint read of a CPU with access control on (#1336): kind
// fingerprint_unavailable, naming the CPU, how its access control showed
// (service: TIA's fingerprint service asked the password-only logon;
// else the project's connection asked credentials) and verify.
func lockedFingerprint(t *testing.T, what string, err error, service bool) {
	t.Helper()
	how := "(TIA asked credentials going online)"
	if service {
		how = "(TIA's fingerprint service asked a password-only logon (OnlineReadAccessPassword))"
	}
	if tiad.Kind(err) != tiad.KindFingerprintUnavailable || !strings.Contains(err.Error(), "the CPU at "+benchCPU+" has access control on "+how) ||
		!strings.Contains(err.Error(), FingerprintUseVerify) {
		t.Fatalf("%s: %v, want fingerprint_unavailable %s naming verify", what, err, how)
	}
}

// trustBlindlyElsewhere makes the agent's TIA instance trust a CPU
// certificate blindly, on session s (TIA opens one project at a time, and
// closing the last session retires the instance and its trust): s's
// station moves to otherCPU (a target of a, no access control), a
// fingerprint read with the opt-in trusts blindly, and the station moves
// back to the bench CPU, compiled. It leaves the fingerprint script
// asking TLS.
func trustBlindlyElsewhere(t *testing.T, a Target, s *tiad.Session) {
	t.Helper()
	st := BenchStation()
	st.CPU.Address = otherCPU
	if _, err := s.ApplyStation(ctx, st, BenchSecrets); err != nil {
		t.Fatalf("the station at another CPU: %v", err)
	}
	a.SetFingerprint(t, FingerprintScript{AsksTLS: true})
	if r, err := s.Fingerprint(ctx, tiad.FingerprintRequest{TrustUnverifiedCertificate: true}); err != nil || len(r.TrustedBlindly) != 1 || r.BlindTrust == nil {
		t.Fatalf("a read of another CPU that trusts blindly: %+v %v", r, err)
	}
	if _, err := s.ApplyStation(ctx, BenchStation(), BenchSecrets); err != nil {
		t.Fatalf("the station back at the bench CPU: %v", err)
	}
	if _, err := s.Compile(ctx, true, true); err != nil {
		t.Fatal(err)
	}
}
