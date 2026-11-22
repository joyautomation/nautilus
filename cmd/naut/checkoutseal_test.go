package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/checkout"
	"github.com/joyautomation/nautilus/devbind"
	"github.com/joyautomation/nautilus/internal/project"
)

// A seal needs real field I/O behind the points, and the controller works that
// out itself: no field driver, only loopback endpoints, a point on a device
// at a loopback or a virtual address all refuse; a real network device passes;
// and what it saw goes in the record.
func TestSealEvidenceRefusesWhatIsNotRealFieldIO(t *testing.T) {
	sh := &checkout.Sheet{Points: []checkout.Point{{ID: "XV1", Tag: "XV1", Kind: checkout.DO, Method: checkout.Force, Device: "RIO"}}}
	for _, c := range []struct {
		name    string
		m       project.Manifest
		virtual map[string]bool
		refuse  string
		devices int
	}{
		{"no driver at all (the implicit memory loopback)", project.Manifest{}, nil, "no field driver configured", 0},
		{"a memory driver", project.Manifest{Driver: project.DriverConfig{Type: "memory"}}, nil, "no field driver configured", 0},
		{"a modbus driver on localhost", project.Manifest{Driver: project.DriverConfig{Type: "modbus", Host: "127.0.0.1:5020"}}, nil, "loopback or a virtual device", 1},
		{"a driver on a virtual device's listen address", project.Manifest{Driver: project.DriverConfig{Type: "modbus", Host: "10.9.9.9:15020"}}, map[string]bool{"10.9.9.9|15020": true}, "loopback or a virtual device", 1},
		{"a point on a device at a loopback", project.Manifest{
			Driver:  project.DriverConfig{Type: "modbus", Host: "plc.example:502"},
			Devices: []devbind.Device{{Name: "RIO", Endpoint: "localhost:15021"}}}, nil, "point XV1 is on device RIO", 2},
		{"a real network device", project.Manifest{
			Driver:  project.DriverConfig{Type: "modbus", Host: "plc.example:502"},
			Devices: []devbind.Device{{Name: "RIO", Endpoint: "10.0.0.11"}}}, nil, "", 2},
		{"a device row alone, on the network", project.Manifest{Devices: []devbind.Device{{Name: "RIO", Endpoint: "opc.tcp://10.0.0.12:4840"}}}, nil, "", 1},
	} {
		ev, why := deriveEvidence(&c.m, sh, c.virtual)
		if (why == "") != (c.refuse == "") || !strings.Contains(why, c.refuse) {
			t.Errorf("%s: refusal %q, want %q", c.name, why, c.refuse)
		}
		if len(ev.Devices) != c.devices {
			t.Errorf("%s: evidence %+v, want %d device(s)", c.name, ev.Devices, c.devices)
		}
	}
}

func TestClassifyEndpoints(t *testing.T) {
	for ep, want := range map[string]string{
		"localhost:502": "loopback", "127.0.0.1": "loopback", "[::1]:4840": "loopback", "opc.tcp://localhost:4840": "loopback", "0.0.0.0:502": "loopback",
		"10.0.0.11": "network", "plc.example:502": "network", "opc.tcp://10.0.0.12:4840/x": "network", "tcp://broker.example:1883": "network",
	} {
		if got := classify(ep, nil); got != want {
			t.Errorf("classify(%q) = %s, want %s", ep, got, want)
		}
	}
	if classify("10.1.1.1:15020", map[string]bool{"10.1.1.1|15020": true}) != "virtual" {
		t.Errorf("a virtual device's listen address is not virtual")
	}
}

// naut checkout verify: ok on a good record and its file, and a failure (exit
// 1) for each way of altering it; the wording never claims a legal signature.
func TestCheckoutVerifyCommand(t *testing.T) {
	dir := t.TempDir()
	st, err := checkout.Open(filepath.Join(dir, "checkout.jsonl"), nil)
	if err != nil {
		t.Fatal(err)
	}
	sh := &checkout.Sheet{Title: "T", Points: []checkout.Point{{ID: "A", Tag: "A", Kind: checkout.DI, Method: checkout.Observe}}}
	run, _ := st.Begin(false, sh.Hash(), "T", "alice", "user:alice")
	st.Record(run.ID, "A", checkout.Pass, "", 1, "good", "alice", "user:alice")
	if _, err := st.Seal(checkout.SealInput{Run: run.ID, Sheet: sh, By: "bob", Principal: "user:bob", Mode: "two-person"}); err != nil {
		t.Fatal(err)
	}
	fp := st.KeyFingerprint()
	st.Close()
	rec := st.SealedPath(run.ID)
	chain := filepath.Join(dir, "checkout.jsonl")

	var out, errb bytes.Buffer
	if code := checkoutMain([]string{"verify", rec, "--chain", chain, "--fingerprint", fp}, &out, &errb); code != 0 {
		t.Fatalf("verify: %d\n%s%s", code, out.String(), errb.String())
	}
	for _, want := range []string{"OK: run " + run.ID, "signed off by bob", "the results file's chain", "tamper-evident", "not by a personal certificate"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	for _, bad := range []string{"legally binding", "compliant", "21 CFR", "eIDAS"} {
		if strings.Contains(out.String(), bad) {
			t.Errorf("output claims %q", bad)
		}
	}
	// the file before the flags works too, and without --fingerprint it says what it did not check
	out.Reset()
	if code := checkoutMain([]string{"verify", "--chain", chain, rec}, &out, &errb); code != 0 || !strings.Contains(out.String(), "not checked: that this key is the controller's") {
		t.Errorf("no fingerprint: %d %s", code, out.String())
	}
	// --audit: the journal's seal entry carries this record's hash
	sha, _ := sealedPayloadSha(mustReadFile(t, rec))
	aud := filepath.Join(dir, "audit-20261122.jsonl")
	os.WriteFile(aud, []byte(`{"id":"01X","seq":1,"action":"tag.write","result":"ok","user":"anonymous"}
{"id":"01SEAL","seq":7,"ts":"2026-11-22T10:00:00Z","user":"user:bob","action":"checkout.seal","result":"ok","after":{"sealSha256":"`+sha+`"}}
`), 0o600)
	out.Reset()
	if code := checkoutMain([]string{"verify", rec, "--audit", aud}, &out, &errb); code != 0 || !strings.Contains(out.String(), "matches the controller's audit entry 01SEAL (seq 7, user:bob") {
		t.Errorf("--audit: %d %s", code, out.String())
	}
	out.Reset()
	if code := checkoutMain([]string{"verify", rec, "--audit", dir}, &out, &errb); code != 0 || !strings.Contains(out.String(), "01SEAL") {
		t.Errorf("--audit with a directory: %d %s", code, out.String())
	}
	// a refused seal, another record's hash, or an older copy: not a match
	os.WriteFile(aud, []byte(`{"id":"01R","action":"checkout.seal","result":"refused","after":{"sealSha256":"`+sha+`"}}
{"id":"01O","action":"checkout.seal","result":"ok","after":{"sealSha256":"deadbeef"}}
`), 0o600)
	out.Reset()
	errb.Reset()
	if code := checkoutMain([]string{"verify", rec, "--audit", aud}, &out, &errb); code != 1 || !strings.Contains(errb.String(), "no accepted checkout.seal entry") {
		t.Errorf("--audit without the entry: %d %s%s", code, out.String(), errb.String())
	}
	// failures
	for name, args := range map[string][]string{
		"another fingerprint": {"verify", rec, "--fingerprint", "00000000-00000000-00000000-00000000"},
		"a missing file":      {"verify", filepath.Join(dir, "nope.json")},
		"no file":             {"verify"},
	} {
		out.Reset()
		errb.Reset()
		if code := checkoutMain(args, &out, &errb); code == 0 {
			t.Errorf("%s: exit 0\n%s%s", name, out.String(), errb.String())
		}
	}
	b, _ := os.ReadFile(rec)
	edited := filepath.Join(dir, "edited.json")
	os.WriteFile(edited, bytes.Replace(b, []byte("bob"), []byte("eve"), 1), 0o600)
	out.Reset()
	errb.Reset()
	if code := checkoutMain([]string{"verify", edited}, &out, &errb); code != 1 || !strings.Contains(errb.String(), "FAILED") {
		t.Errorf("an edited record: %d %s%s", code, out.String(), errb.String())
	}
}

func mustReadFile(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
