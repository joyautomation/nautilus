package tiadtest_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/siemens/tiad"
)

// The pins on disk (siemens-ide.md §3.8): what only a real process shows.

// TestAgentKeepsPinsAcrossRestarts: a pin is a file in TIAD_PINS_DIR, so
// a restarted agent (a reinstall restarts it) still holds it and still
// refuses a changed CPU; first trust and re-pin are audited with the
// fields held and the old and new values.
func TestAgentKeepsPinsAcrossRestarts(t *testing.T) {
	dir := t.TempDir()
	env := "TIAD_PINS_DIR=" + dir
	p1, s1 := downloadReady(t, env)
	if r, err := s1.UploadWith(ctx, tiad.UploadRequest{}); err != nil || r.Pin != tiad.PinFirstTrust {
		t.Fatalf("first upload: %+v %v", r, err)
	}
	for _, want := range []string{`audit pin first-trust`, `address="192.0.2.10"`, `holds=[order,firmware]`, `"serial":null`} {
		if !strings.Contains(p1.Log(), want) {
			t.Fatalf("the first-trust audit line lacks %q:\n%s", want, p1.Log())
		}
	}
	b, err := os.ReadFile(filepath.Join(dir, "192.0.2.10.device"))
	if err != nil || !strings.HasPrefix(string(b), `{"order":"6ES7 510-1DK03-0AB0","firmware":"V3.1","serial":null,"mac":null}`+"\n") ||
		!strings.Contains(string(b), "re-pin with POST /v1/pins/192.0.2.10") {
		t.Fatalf("the pin file: %q %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(p1.WorkDir, "192.0.2.10.device")); err == nil {
		t.Fatal("a pin was written into the work dir")
	}
	p1.Stop()

	p2, s2 := downloadReady(t, env)
	c := p2.Client()
	if ps, err := c.Pins(ctx); err != nil || len(ps) != 1 || ps[0].Address != "192.0.2.10" || ps[0].Device.Firmware != "V3.1" {
		t.Fatalf("pins after a restart: %+v %v", ps, err)
	}
	updated := tiad.DeviceIdentity{Order: "6ES7 510-1DK03-0AB0", Firmware: "V3.2"}
	p2.SetDevice(t, updated, false)
	if _, err := s2.Compare(ctx); tiad.Kind(err) != tiad.KindDeviceChanged {
		t.Fatalf("compare to a changed CPU after a restart: %v", err)
	}
	dl := tiad.DownloadRequest{Address: "192.0.2.10", Software: true, Announced: true, Decisions: tiad.Decisions{StartAfter: tiad.No()}}
	if _, err := s2.Download(ctx, dl); tiad.Kind(err) != tiad.KindDeviceChanged {
		t.Fatalf("download to a changed CPU after a restart: %v", err)
	}
	if l := p2.Loaded(t); len(l) != 0 {
		t.Fatalf("a changed CPU was loaded: %+v", l)
	}
	if n := countCalls(p2.Calls(t), "Download"); n != 0 {
		t.Fatalf("TIA heard of a download to a changed CPU %d times", n)
	}
	if _, err := c.SetPin(ctx, "192.0.2.10", tiad.PinRequest{Device: &updated, Replace: true, Announced: true}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`audit pin replace`, `previous={"order":"6ES7 510-1DK03-0AB0","firmware":"V3.1"`, `device={"order":"6ES7 510-1DK03-0AB0","firmware":"V3.2"`} {
		if !strings.Contains(p2.Log(), want) {
			t.Fatalf("the re-pin audit line lacks %q:\n%s", want, p2.Log())
		}
	}
	if _, err := s2.Download(ctx, dl); err != nil {
		t.Fatalf("download after the re-pin: %v", err)
	}
}

// TestAgentFailsClosedOnABadPinFile: a pin file the agent cannot read is
// never "no pin": every online call to that address is refused, and
// nothing loads.
func TestAgentFailsClosedOnABadPinFile(t *testing.T) {
	for name, body := range map[string]string{
		"not json":    "not json\n",
		"empty order": `{"order":"","firmware":"V3.1","serial":null,"mac":null}` + "\n",
		"empty file":  "",
		"extra field": `{"order":"x","firmware":"V3.1","serial":null,"mac":null,"model":"y"}` + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "192.0.2.10.device"), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			p, s := downloadReady(t, "TIAD_PINS_DIR="+dir)
			unreadable := func(what string, err error) {
				t.Helper()
				if tiad.Kind(err) != tiad.KindOperationFailed || !strings.Contains(err.Error(), "the device pin for 192.0.2.10 is unreadable") {
					t.Errorf("%s with a bad pin file: %v", what, err)
				}
			}
			_, err := s.Compare(ctx)
			unreadable("compare", err)
			_, err = s.Download(ctx, tiad.DownloadRequest{Address: "192.0.2.10", Software: true, Announced: true, Decisions: tiad.Decisions{StartAfter: tiad.No()}})
			unreadable("download", err)
			_, err = s.UploadWith(ctx, tiad.UploadRequest{})
			unreadable("upload", err)
			_, err = p.Client().Pins(ctx)
			unreadable("listing", err)
			if l := p.Loaded(t); len(l) != 0 {
				t.Fatalf("loaded with a bad pin file: %+v", l)
			}
			if b, _ := os.ReadFile(filepath.Join(dir, "192.0.2.10.device")); string(b) != body {
				t.Fatalf("the bad pin file was overwritten: %q", b)
			}
		})
	}
}
