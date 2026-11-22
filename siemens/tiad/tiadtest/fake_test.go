package tiadtest_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/joyautomation/nautilus/siemens/tiad"
	"github.com/joyautomation/nautilus/siemens/tiad/tiadtest"
)

func TestCancelledContextKeepsItsError(t *testing.T) {
	f := tiadtest.New()
	_, c := f.Serve(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.Health(ctx)
	if !errors.Is(err, context.Canceled) || tiad.Kind(err) != tiad.KindUnreachable {
		t.Fatalf("cancelled: %v (errors.Is canceled = %v)", err, errors.Is(err, context.Canceled))
	}
}

// TestClientChecksBeforeSending: a request the client can tell is wrong
// never leaves it. The agent would refuse it too; this is about not
// sending it at all.
func TestClientChecksBeforeSending(t *testing.T) {
	ctx := context.Background()
	f := tiadtest.New()
	var sent atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent.Add(1)
		f.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	c := tiad.New(srv.URL, f.Token)
	const g = "GSDML-V2.4-Example-Device-20261117.xml"
	c.PutFile(ctx, "gsd/"+g, []byte("<ISO15745Profile/>"))
	c.InstallGSD(ctx, "gsd/"+g)
	s, err := c.CreateProject(ctx, "p", tiadtest.BenchStation(), tiadtest.BenchSecrets)
	if err != nil {
		t.Fatal(err)
	}
	bad := tiadtest.BenchStation()
	bad.CPU.ProfinetName = "PLC 1"
	before := sent.Load()
	calls := map[string]func() error{
		"PutFile":    func() error { _, err := c.PutFile(ctx, "NUL", nil); return err },
		"GetFile":    func() error { _, err := c.GetFile(ctx, "../x"); return err },
		"InstallGSD": func() error { _, err := c.InstallGSD(ctx, "../GSDML-x.xml"); return err },
		"Open":       func() error { _, err := c.Open(ctx, "C:/p.ap21"); return err },
		"CreateProject": func() error {
			_, err := c.CreateProject(ctx, "a/b", tiadtest.BenchStation(), tiadtest.BenchSecrets)
			return err
		},
		"CreateProject 2": func() error { _, err := c.CreateProject(ctx, "q", bad, tiadtest.BenchSecrets); return err },
		"ApplyStation":    func() error { _, err := s.ApplyStation(ctx, bad, tiadtest.BenchSecrets); return err },
		"Software": func() error {
			_, err := s.Software(ctx, tiad.SoftwareRequest{Group: "g", Sources: []string{"aux.scl"}})
			return err
		},
		"Export": func() error { _, err := s.Export(ctx, tiad.ExportRequest{Dir: "out.", Blocks: "scl"}); return err },
		"Download": func() error {
			_, err := s.Download(ctx, tiad.DownloadRequest{Address: "192.0.2.10", Software: true})
			return err
		},
	}
	for name, call := range calls {
		if err := call(); tiad.Kind(err) != tiad.KindBadRequest {
			t.Errorf("%s: %v, want bad_request", name, err)
		}
	}
	if n := sent.Load() - before; n != 0 {
		t.Fatalf("%d refused requests were sent", n)
	}
}

func TestFakeJSONNeedsItsContentType(t *testing.T) {
	f := tiadtest.New()
	srv, _ := f.Serve(t)
	for _, ct := range []string{"", "text/plain", "application/x-www-form-urlencoded"} {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/sessions", strings.NewReader(`{"path":"p.ap21"}`))
		req.Header.Set("Authorization", "Bearer "+f.Token)
		if ct != "" {
			req.Header.Set("Content-Type", ct)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnsupportedMediaType {
			t.Errorf("JSON body as %q = HTTP %d, want 415", ct, resp.StatusCode)
		}
	}
}

// TestFakeNeverRunsWithoutAToken: like the agent, a fake with no token
// refuses everything rather than serving everyone.
func TestFakeNeverRunsWithoutAToken(t *testing.T) {
	f := tiadtest.New()
	f.Token = ""
	_, c := f.Serve(t)
	c.Token = ""
	if _, err := c.Health(context.Background()); tiad.Kind(err) != tiad.KindUnauthorized {
		t.Fatalf("health from a fake with no token: %v", err)
	}
	c.Token = "anything"
	if _, err := c.Health(context.Background()); tiad.Kind(err) != tiad.KindUnauthorized {
		t.Fatalf("health with some token: %v", err)
	}
}

// TestFakeCarriesTheModeOfADoubtfulDownload: the Go fake's compare that
// differs after a load says the mode TIA's messages gave, as the agent
// does (TestAgentCarriesTheModeOfADoubtfulDownload).
func TestFakeCarriesTheModeOfADoubtfulDownload(t *testing.T) {
	f := tiadtest.New()
	f.Targets = []string{"192.0.2.10"}
	_, c := f.Serve(t)
	installBench(t, c)
	s, err := c.CreateProject(context.Background(), "bench", tiadtest.BenchStation(), tiadtest.BenchSecrets)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Compile(ctx, true, true); err != nil {
		t.Fatal(err)
	}
	f.PreDownload = func(tiad.DownloadRequest) []tiad.Question { return []tiad.Question{{ID: tiad.QuestionStopCPU}} }
	f.CompareResult = &tiad.CompareResult{Differences: []tiad.Difference{{Object: "PLC_1", State: "different"}}}
	_, err = s.Download(ctx, tiad.DownloadRequest{Address: "192.0.2.10", Software: true, Announced: true, Decisions: tiad.Decisions{StopCPU: tiad.Yes(), StartAfter: tiad.No()}})
	if state, source := tiad.StateOf(err); tiad.Kind(err) != tiad.KindOutcomeUnknown || state != "STOP" || source != tiad.StateFromTIAMessage {
		t.Fatalf("%v (state %q %q)", err, state, source)
	}
}

// TestFakeKeepsATrustingInstanceWhileASessionIsReserved is the agent's
// retire guard on the Go fake, through its test seam: a session reserved
// between a close and the retire check keeps the trusting instance.
func TestFakeKeepsATrustingInstanceWhileASessionIsReserved(t *testing.T) {
	f := tiadtest.New()
	f.Targets = []string{"192.0.2.10"}
	_, c := f.Serve(t)
	f.SetFingerprint(tiadtest.FingerprintScript{AsksTLS: true})
	if _, err := c.PutFile(context.Background(), "gsd/GSDML-V2.4-Example-Device-20261117.xml", []byte("<ISO15745Profile/>")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.InstallGSD(context.Background(), "gsd/GSDML-V2.4-Example-Device-20261117.xml"); err != nil {
		t.Fatal(err)
	}
	s, err := c.CreateProject(context.Background(), "bench", tiadtest.BenchStation(), tiadtest.BenchSecrets)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Fingerprint(context.Background(), tiad.FingerprintRequest{TrustUnverifiedCertificate: true}); err != nil {
		t.Fatal(err)
	}
	f.SeamReserveBeforeRetire = true
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.BlindTrust() == nil {
		t.Fatal("the trusting instance was retired with a session reserved")
	}
	f.SeamReserveBeforeRetire = false
	s2, err := c.CreateProject(context.Background(), "bench", tiadtest.BenchStation(), tiadtest.BenchSecrets)
	if err != nil {
		t.Fatal(err)
	}
	if err := s2.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.BlindTrust() != nil {
		t.Fatal("not retired once no session was left")
	}
}
