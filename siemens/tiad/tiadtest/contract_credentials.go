package tiadtest

import (
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
	a, s := compiledBench(t, start)
	c := a.Client()
	wrong := &tiad.OnlineSecrets{FullAccessPassword: "Wrong-access-2026"}
	refused := func(what string, err error, kind, why string) {
		t.Helper()
		if tiad.Kind(err) != kind || !strings.Contains(err.Error(), LoginQuestion+": "+why) {
			t.Fatalf("%s: %v, want %s naming %q", what, err, kind, why)
		}
	}

	// The first hardware load asks nothing going online (the CPU has no
	// access control yet), but the compare right after it does: without
	// the password the load lands and cannot be shown equal.
	hw := AcceptHardware(tiad.DownloadRequest{Address: benchCPU, Hardware: true, Software: true, Announced: true, Decisions: tiad.Decisions{StartAfter: tiad.No()}})
	_, err := s.Download(ctx, hw)
	refused("the first load without the password", err, tiad.KindOutcomeUnknown, LoginNoPassword)
	if n := len(a.Loaded(t)); n != 1 {
		t.Fatalf("loaded %d, want the first load", n)
	}

	// Compare: with the password it is equal; without one, or with a
	// wrong one, refused, naming why.
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

	// A fingerprint read logs on too.
	_, err = s.Fingerprint(ctx, tiad.FingerprintRequest{})
	refused("fingerprint read without the password", err, tiad.KindOperationFailed, LoginNoPassword)
	if r, err := s.Fingerprint(ctx, tiad.FingerprintRequest{Secrets: BenchLogin}); err != nil || len(r.Items) == 0 {
		t.Fatalf("fingerprint read with the password: %+v %v", r, err)
	}

	// A download: refused going online without the password (in doubt,
	// as any refusal inside TIA's download call), nothing loaded; with
	// it, loaded and compared equal.
	sw := tiad.DownloadRequest{Address: benchCPU, Software: true, Announced: true, Decisions: tiad.Decisions{StartAfter: tiad.No()}}
	_, err = s.Download(ctx, sw)
	refused("a download without the password", err, tiad.KindOutcomeUnknown, LoginNoPassword)
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
	// The deploy's fingerprint read after the load logs on with it too.
	if r.Deploy.Fingerprint == nil || r.Deploy.FingerprintError != "" {
		t.Fatalf("the deploy's fingerprint read: %+v", r.Deploy)
	}
	hw.Secrets = BenchDownloadSecrets()
	if r, err := s.Download(ctx, hw); err != nil || !r.Downloaded {
		t.Fatalf("a hardware load with the password: %+v %v", r, err)
	}
	r, err = s.Download(ctx, sw)
	if err != nil || r.Deploy == nil {
		t.Fatalf("the last load: %+v %v", r, err)
	}
	id := r.Deploy.ID

	// A verify opens its own session: the project is closed first.
	closeFirst(t, s)
	// A verify compares online: it logs on too.
	_, err = c.VerifyDeploy(ctx, tiad.VerifyRequest{Deploy: id})
	refused("a verify without the password", err, tiad.KindOperationFailed, LoginNoPassword)
	if v, err := c.VerifyDeploy(ctx, tiad.VerifyRequest{Deploy: id, Secrets: BenchLogin}); err != nil || v.Verdict != tiad.VerifyEqual {
		t.Fatalf("a verify with the password: %+v %v", v, err)
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

	// No password goes over a connection trusted blindly: a read that
	// trusts the certificate logs on to the project's connection first,
	// then trusts, and refuses the fingerprint service's credentials.
	a.SetFingerprint(t, FingerprintScript{AsksTLS: true})
	_, err = s2.Fingerprint(ctx, tiad.FingerprintRequest{Secrets: BenchLogin, TrustUnverifiedCertificate: true})
	refused("a read that trusted blindly", err, tiad.KindOperationFailed, LoginTrustedHere)
	// The instance keeps the trust: every later login is refused, until
	// the last session closes and the instance is retired.
	_, err = s2.CompareWith(ctx, BenchCompare)
	if tiad.Kind(err) != tiad.KindOperationFailed || !strings.Contains(err.Error(), "this agent's TIA instance trusts a CPU certificate blindly (") ||
		!strings.Contains(err.Error(), "close every session") {
		t.Fatalf("compare on an instance that trusts blindly: %v", err)
	}
	a.SetFingerprint(t, FingerprintScript{})
	closeFirst(t, s2)
	if v, err := c.VerifyDeploy(ctx, tiad.VerifyRequest{Deploy: id, Secrets: BenchLogin}); err != nil || v.Verdict != tiad.VerifyEqual {
		t.Fatalf("a verify once the trusting instance was retired: %+v %v", v, err)
	}
}
