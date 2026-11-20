package tiadtest_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/siemens/tiad/tiadtest"

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
	if !strings.Contains(p2.Log(), `audit pins at start dir=`) || !strings.Contains(p2.Log(), `count=1 pins=["192.0.2.10" holds=[order,firmware]]`) {
		t.Fatalf("no pin inventory at start:\n%s", p2.Log())
	}
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
				if tiad.Kind(err) != tiad.KindOperationFailed || !strings.Contains(err.Error(), "the device pin for 192.0.2.10 is unreadable") ||
					!strings.Contains(err.Error(), "POST /v1/pins/192.0.2.10 with replace: true") {
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
			// The fix the refusal names: an audited re-pin, never a pin
			// without replace.
			c := p.Client()
			dev := tiad.DeviceIdentity{Order: "6ES7 510-1DK03-0AB0", Firmware: "V3.1"}
			_, err = c.SetPin(ctx, "192.0.2.10", tiad.PinRequest{Device: &dev, Announced: true})
			unreadable("a pin without replace", err)
			if _, err := c.SetPin(ctx, "192.0.2.10", tiad.PinRequest{Device: &dev, Replace: true, Announced: true}); err != nil {
				t.Fatalf("re-pin over a bad pin file: %v", err)
			}
			if !strings.Contains(p.Log(), `audit pin replace address="192.0.2.10" previous=corrupt why=`) || !strings.Contains(p.Log(), "raw=") {
				t.Fatalf("the re-pin's audit line does not keep why the old pin was corrupt and what it held:\n%s", p.Log())
			}
			if _, err := s.Compare(ctx); err != nil {
				t.Fatalf("compare after the re-pin: %v", err)
			}
		})
	}
}

// TestAgentFailsClosedOnAnUnreadablePin: a pin that cannot be read at all
// (its directory or its file locked away) is never "no pin", and is never
// replaced: its identity is unknown, not wrong. Fable's review of #1108
// found the directory case loading a changed CPU.
func TestAgentFailsClosedOnAnUnreadablePin(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads through file modes")
	}
	for _, which := range []string{"dir", "file"} {
		t.Run(which, func(t *testing.T) {
			dir := t.TempDir()
			p, s := downloadReady(t, "TIAD_PINS_DIR="+dir)
			c := p.Client()
			dev := tiad.DeviceIdentity{Order: "6ES7 510-1DK03-0AB0", Firmware: "V3.1"}
			if _, err := c.SetPin(ctx, "192.0.2.10", tiad.PinRequest{Device: &dev, Announced: true}); err != nil {
				t.Fatal(err)
			}
			p.SetDevice(t, tiad.DeviceIdentity{Order: "6ES7 999-9XX99-9XX9", Firmware: "V9.9"}, false)
			locked := dir
			if which == "file" {
				locked = filepath.Join(dir, "192.0.2.10.device")
			}
			if err := os.Chmod(locked, 0); err != nil {
				t.Fatal(err)
			}
			defer os.Chmod(locked, 0o700)
			refused := func(what string, err error) {
				t.Helper()
				if tiad.Kind(err) != tiad.KindOperationFailed || !strings.Contains(err.Error(), "192.0.2.10") && !strings.Contains(err.Error(), "pins directory") {
					t.Errorf("%s with the pin %s locked: %v", what, which, err)
				}
			}
			_, err := s.Compare(ctx)
			refused("compare", err)
			_, err = s.Download(ctx, tiad.DownloadRequest{Address: "192.0.2.10", Software: true, Announced: true, Decisions: tiad.Decisions{StartAfter: tiad.No()}})
			refused("download", err)
			if which == "file" {
				_, err = c.SetPin(ctx, "192.0.2.10", tiad.PinRequest{Device: &dev, Replace: true, Announced: true})
				refused("a re-pin over an unreadable pin", err)
			}
			_, err = c.Pins(ctx)
			refused("listing", err)
			if l := p.Loaded(t); len(l) != 0 {
				t.Fatalf("loaded a changed CPU with the pin %s locked: %+v", which, l)
			}
			if n := countCalls(p.Calls(t), "Download"); n != 0 {
				t.Fatalf("TIA heard of the download %d times", n)
			}
			if !strings.Contains(p.Log(), "audit pin unreadable") {
				t.Fatalf("no audit line for the unreadable pin:\n%s", p.Log())
			}
			os.Chmod(locked, 0o700)
			if b, _ := os.ReadFile(filepath.Join(dir, "192.0.2.10.device")); !strings.Contains(string(b), `"firmware":"V3.1"`) {
				t.Fatalf("the locked pin was replaced: %q", b)
			}
		})
	}
}

// TestAgentRefusesAnUnusablePinsDir: an agent that cannot list and write
// its pins directory does not start, rather than read every pin as missing.
func TestAgentRefusesAnUnusablePinsDir(t *testing.T) {
	bin := fakeBin(t)
	base := []string{"TIAD_WORKDIR=" + t.TempDir(), "TIAD_TOKEN=t", "TIAD_ADDR=http://127.0.0.1:0"}
	file := filepath.Join(t.TempDir(), "a-file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{"a file": file}
	if os.Geteuid() != 0 {
		locked := t.TempDir()
		if err := os.Chmod(locked, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(locked, 0o700) })
		cases["read-only"] = locked
		// Writable but not listable: a probe write alone would pass.
		blind := t.TempDir()
		if err := os.Chmod(blind, 0o300); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(blind, 0o700) })
		cases["write-only"] = blind
	}
	for name, dir := range cases {
		t.Run(name, func(t *testing.T) {
			code, out := tiadtest.RunToExit(t, bin, append(base, "TIAD_PINS_DIR="+dir)...)
			if code != 2 || !strings.Contains(out, "TIAD_PINS_DIR") || !strings.Contains(out, "is not usable") {
				t.Fatalf("exit %d, want 2 naming TIAD_PINS_DIR:\n%s", code, out)
			}
		})
	}
}

// TestAgentAuditsAnUploadWhoseIdentityCannotBeRead: the project is
// replaced once TIA's upload returns, so a failed identity read still
// answers device_unverified and leaves its audit line.
func TestAgentAuditsAnUploadWhoseIdentityCannotBeRead(t *testing.T) {
	p, s := downloadReady(t)
	p.SetScript(t, tiadtest.Script{Failures: []tiadtest.ScriptedCall{{Call: "ProjectCpu", Message: "no CPU attributes"}}})
	if _, err := s.UploadWith(ctx, tiad.UploadRequest{}); tiad.Kind(err) != tiad.KindDeviceUnverified {
		t.Fatalf("upload whose identity read fails: %v", err)
	}
	if !strings.Contains(p.Log(), "audit pin unverified call=upload") {
		t.Fatalf("no audit line:\n%s", p.Log())
	}
	if ps, err := p.Client().Pins(ctx); err != nil || len(ps) != 0 {
		t.Fatalf("pins: %+v %v", ps, err)
	}
}
