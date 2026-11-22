package tiadtest_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/siemens/tiad"
	"github.com/joyautomation/nautilus/siemens/tiad/tiadtest"
)

// loggedOnBench is downloadReady with the bench station's hardware loaded,
// with its password: from then on going online to the CPU asks
// credentials.
func loggedOnBench(t *testing.T) (*tiadtest.Process, *tiad.Session) {
	t.Helper()
	p, s := downloadReady(t)
	hw := tiadtest.AcceptHardware(tiad.DownloadRequest{Secrets: tiadtest.BenchDownloadSecrets(), Address: "192.0.2.10", Hardware: true, Software: true, Announced: true,
		Decisions: tiad.Decisions{StartAfter: tiad.No()}})
	if r, err := s.Download(ctx, hw); err != nil || !r.Downloaded {
		t.Fatalf("the first hardware load: %+v %v", r, err)
	}
	return p, s
}

// TestAgentLogsOnWithoutLoggingThePassword: every online call that logs on,
// or refuses to, is an audit line naming the call and the user; neither
// the right password nor a wrong one is in the log, the deploy record or
// any file the agent wrote (#789 stage 5 (A)).
func TestAgentLogsOnWithoutLoggingThePassword(t *testing.T) {
	p, s := loggedOnBench(t)
	c := p.Client()
	const wrongPw = "Wrong-access-must-not-leak-1"
	wrong := &tiad.OnlineSecrets{FullAccessPassword: wrongPw}
	if r, err := s.CompareWith(ctx, tiadtest.BenchCompare); err != nil || !r.Equal {
		t.Fatalf("compare: %+v %v", r, err)
	}
	if _, err := s.CompareWith(ctx, tiad.CompareRequest{Secrets: wrong}); tiad.Kind(err) != tiad.KindOperationFailed {
		t.Fatalf("compare with a wrong password: %v", err)
	}
	if _, err := s.Fingerprint(ctx, tiad.FingerprintRequest{Secrets: tiadtest.BenchLogin}); err != nil {
		t.Fatalf("fingerprint read: %v", err)
	}
	if _, err := s.Fingerprint(ctx, tiad.FingerprintRequest{Secrets: wrong}); tiad.Kind(err) != tiad.KindOperationFailed {
		t.Fatalf("fingerprint read with a wrong password: %v", err)
	}
	sw := tiad.DownloadRequest{Address: "192.0.2.10", Software: true, Announced: true, Decisions: tiad.Decisions{StartAfter: tiad.No()},
		Secrets: &tiad.Secrets{FullAccessPassword: wrongPw}}
	if _, err := s.Download(ctx, sw); tiad.Kind(err) != tiad.KindOutcomeUnknown {
		t.Fatalf("a download with a wrong password: %v", err)
	}
	sw.Secrets = tiadtest.BenchDownloadSecrets()
	r, err := s.Download(ctx, sw)
	if err != nil || r.Deploy == nil {
		t.Fatalf("a download: %+v %v", r, err)
	}
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := c.VerifyDeploy(ctx, tiad.VerifyRequest{Deploy: r.Deploy.ID, Secrets: wrong}); tiad.Kind(err) != tiad.KindOperationFailed {
		t.Fatalf("a verify with a wrong password: %v", err)
	}
	if v, err := c.VerifyDeploy(ctx, tiad.VerifyRequest{Deploy: r.Deploy.ID, Secrets: tiadtest.BenchLogin}); err != nil || v.Verdict != tiad.VerifyEqual {
		t.Fatalf("a verify: %+v %v", v, err)
	}
	s2, err := c.CreateProject(ctx, "up", tiadtest.BenchStation(), tiadtest.BenchSecrets)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s2.UploadWith(ctx, tiad.UploadRequest{Secrets: wrong}); tiad.Kind(err) != tiad.KindOperationFailed {
		t.Fatalf("an upload with a wrong password: %v", err)
	}
	if _, err := s2.UploadWith(ctx, tiad.UploadRequest{Secrets: tiadtest.BenchLogin}); err != nil {
		t.Fatalf("an upload: %v", err)
	}

	log := waitLog(t, p, `audit online-login call=upload address="192.0.2.10" user="nautilus": answering`)
	for _, call := range []string{"compare", "fingerprint read", "download", "upload"} {
		if !strings.Contains(log, "audit online-login call="+call+` address="192.0.2.10" user="nautilus": answering TIA's credentials question`) {
			t.Errorf("no logon audit line for the %s", call)
		}
		if !strings.Contains(log, "audit online-login call="+call+` address="192.0.2.10" user="nautilus" refused: "password not sent again: TIA asked credentials again`) {
			t.Errorf("no refusal audit line for the %s with a wrong password", call)
		}
	}
	for _, pw := range []string{tiadtest.BenchFullAccessPassword, wrongPw} {
		if strings.Contains(log, pw) {
			t.Fatalf("the log holds a password (%q):\n%s", pw, log)
		}
		for _, dir := range []string{p.DeploysDir, p.WorkDir, p.PinsDir} {
			err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return err
				}
				b, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				if strings.Contains(string(b), pw) {
					t.Errorf("%s holds a password (%q)", path, pw)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		}
	}
}

// TestAgentSendsNoPasswordAfterABlindTrust pins what a blind trust costs
// on a CPU that asks credentials. A deploy whose fingerprint read trusts
// blindly logs on first (the project's connection) and goes through, but
// from then on the TIA instance sends no password until it is retired: a
// compare is refused, and so is a download of a fullAccessUser station,
// before anything reaches the CPU. An upload whose CPU asks TLS trust
// before credentials, with the opt-in, is refused at the logon.
func TestAgentSendsNoPasswordAfterABlindTrust(t *testing.T) {
	p, s := loggedOnBench(t)
	p.SetScript(t, tiadtest.Script{FingerprintAsksTLS: true})
	sw := tiad.DownloadRequest{Secrets: tiadtest.BenchDownloadSecrets(), Address: "192.0.2.10", Software: true, Announced: true,
		Decisions: tiad.Decisions{StartAfter: tiad.No()}, Fingerprint: &tiad.FingerprintRequest{TrustUnverifiedCertificate: true}}
	r, err := s.Download(ctx, sw)
	if err != nil || !r.Downloaded || !r.CompareEqual || r.Deploy == nil || r.Deploy.Fingerprint == nil {
		t.Fatalf("a download whose read trusts blindly: %+v %v", r, err)
	}
	const blocked = "password not sent: this agent's TIA instance trusted an unverified CPU certificate ("
	if _, err := s.CompareWith(ctx, tiadtest.BenchCompare); tiad.Kind(err) != tiad.KindOperationFailed || !strings.Contains(err.Error(), blocked) {
		t.Fatalf("a compare on the trusting instance: %v", err)
	}
	loads := len(p.Loaded(t))
	hw := tiadtest.AcceptHardware(sw)
	hw.Hardware, hw.Fingerprint = true, nil
	hw.Decisions.AcceptBlindTrust = tiad.Yes()
	if _, err := s.Download(ctx, hw); tiad.Kind(err) != tiad.KindBadRequest || !strings.Contains(err.Error(), blocked) {
		t.Fatalf("a hardware load of a fullAccessUser station on the trusting instance: %v", err)
	}
	// A software load cannot know whether its CPU asks: it goes on, and
	// the logon is refused inside TIA's call (in doubt).
	sw.Fingerprint = nil
	sw.Decisions.AcceptBlindTrust = tiad.Yes()
	if _, err := s.Download(ctx, sw); tiad.Kind(err) != tiad.KindOutcomeUnknown || !strings.Contains(err.Error(), blocked) {
		t.Fatalf("a software load on the trusting instance, to a CPU that asks credentials: %v", err)
	}
	if n := countCalls(p.Calls(t), "Archive"); len(p.Loaded(t)) != loads || n != 3 {
		t.Fatalf("the refused downloads loaded: loads %d (want %d), archives %d", len(p.Loaded(t)), loads, n)
	}
	// Closing the last session retires the instance; the next logs on.
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	c := p.Client()
	s2, err := c.CreateProject(ctx, "up", tiadtest.BenchStation(), tiadtest.BenchSecrets)
	if err != nil {
		t.Fatal(err)
	}
	p.SetScript(t, tiadtest.Script{UploadAsksTLS: true})
	_, err = s2.UploadWith(ctx, tiad.UploadRequest{Secrets: tiadtest.BenchLogin, TrustUnverifiedCertificate: true})
	if tiad.Kind(err) != tiad.KindOperationFailed || !strings.Contains(err.Error(), tiadtest.LoginQuestion+": "+tiadtest.LoginTrustedHere) {
		t.Fatalf("an upload that trusts blindly, from a CPU that asks credentials: %v", err)
	}
}
