package main

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/checkout"
	"github.com/joyautomation/nautilus/devbind"
	"github.com/joyautomation/nautilus/internal/project"
)

// A seal needs real field I/O behind the points, and the controller works that
// out itself, failing closed: no field driver, ANY loopback, own-box or virtual
// endpoint, ANY endpoint it cannot read, or a point on such a device refuses; a
// manifest that attests real-io lifts all but "no driver"; what it saw goes in
// the record.
func TestSealEvidenceFailsClosed(t *testing.T) {
	defer stubDNS()()
	sh := &checkout.Sheet{Points: []checkout.Point{{ID: "XV1", Tag: "XV1", Kind: checkout.DO, Method: checkout.Force, Device: "RIO"}}}
	own := map[netip.Addr]bool{netip.MustParseAddr("192.0.2.7"): true}
	modbus := func(host string) project.DriverConfig { return project.DriverConfig{Type: "modbus", Host: host} }
	for _, c := range []struct {
		name     string
		m        project.Manifest
		virtual  map[string]bool
		attested []string
		refuse   string
		devices  int
	}{
		{"no driver at all (the implicit memory loopback)", project.Manifest{}, nil, nil, "no field driver configured", 0},
		{"a memory driver", project.Manifest{Driver: project.DriverConfig{Type: "memory"}}, nil, nil, "no field driver configured", 0},
		{"a runtime driver alone has no field I/O", project.Manifest{Driver: project.DriverConfig{Type: "runtime"}}, nil, nil, "no field driver configured", 0},
		{"a modbus driver on localhost", project.Manifest{Driver: modbus("127.0.0.1:5020")}, nil, nil, "loopback address", 1},
		{"a driver on a virtual device's listen port", project.Manifest{Driver: modbus("10.9.9.9:15020")}, map[string]bool{"10.9.9.9|15020": true}, nil, "virtual address", 1},
		{"one simulator among real devices", project.Manifest{
			Drivers: []project.DriverConfig{modbus("plc.example:502"), {Type: "eip", Name: "sim", Host: "localhost"}}}, nil, nil, "driver sim connects to a loopback", 2},
		{"a driver whose hosts are in a manifest file", project.Manifest{Driver: project.DriverConfig{Type: "modbus", Manifest: "m.yaml"}}, nil, nil, "cannot read where driver modbus connects", 1},
		{"a device row with no endpoint", project.Manifest{Driver: modbus("plc.example:502"), Devices: []devbind.Device{{Name: "RIO"}}}, nil, nil, "device RIO is at a unknown address", 2},
		{"a point on a device at a loopback", project.Manifest{
			Driver:  modbus("plc.example:502"),
			Devices: []devbind.Device{{Name: "RIO", Endpoint: "localhost:15021"}}}, nil, nil, "1 point(s) (XV1) are on device RIO", 2},
		{"this box's own address", project.Manifest{Driver: modbus("192.0.2.7:502")}, nil, nil, "loopback address", 1},
		{"a real network device", project.Manifest{Driver: modbus("plc.example:502"), Devices: []devbind.Device{{Name: "RIO", Endpoint: "10.0.0.11"}}}, nil, nil, "", 2},
		{"a device row alone, on the network", project.Manifest{Devices: []devbind.Device{{Name: "RIO", Endpoint: "opc.tcp://10.0.0.12:4840"}}}, nil, nil, "", 1},
		{"attested: a gateway on a loopback", project.Manifest{Driver: modbus("127.0.0.1:502")}, nil, []string{"modbus"}, "", 1},
		{"attested: a driver whose hosts are in a file", project.Manifest{Driver: project.DriverConfig{Type: "modbus", Manifest: "m.yaml"}}, nil, []string{"modbus"}, "", 1},
		{"attested does not make a driver up", project.Manifest{}, nil, []string{"modbus"}, "no field driver configured", 0},
		{"attesting one driver does not excuse another simulator", project.Manifest{Drivers: []project.DriverConfig{
			{Type: "modbus", Host: "127.0.0.1:502"}, {Type: "eip", Name: "sim", Host: "localhost"}}}, nil, []string{"modbus"}, "driver sim connects to a loopback", 2},
		{"a replay alone is not field I/O", project.Manifest{Driver: project.DriverConfig{Type: "replay"}}, nil, []string{"replay"}, "no field driver configured", 0},
		{"a broker's password stays out of the evidence", project.Manifest{Driver: project.DriverConfig{Type: "sparkplug-host", Broker: "tcp://user:secret@broker.example:1883"}}, nil, nil, "", 1},
		{"a host list with a loopback in it", project.Manifest{Driver: modbus("10.0.0.5:502,127.0.0.1:502")}, nil, nil, "loopback address", 2},
		{"a name that resolves to the box", project.Manifest{Driver: modbus("gateway.lan:502")}, nil, nil, "loopback address", 1},
		{"a name that does not resolve", project.Manifest{Driver: modbus("nowhere.example:502")}, nil, nil, "unknown address", 1},
	} {
		ev, why := deriveEvidence(&c.m, sh, c.virtual, own, c.attested)
		if (why == "") != (c.refuse == "") || !strings.Contains(why, c.refuse) {
			t.Errorf("%s: refusal %q, want %q", c.name, why, c.refuse)
		}
		if len(ev.Devices) != c.devices {
			t.Errorf("%s: evidence %+v, want %d device(s)", c.name, ev.Devices, c.devices)
		}
		if c.refuse == "" && (len(ev.Attested) > 0) != (len(c.attested) > 0) {
			t.Errorf("%s: attested %v", c.name, ev.Attested)
		}
	}
}

func TestClassifyEndpoints(t *testing.T) {
	defer stubDNS()()
	own := map[netip.Addr]bool{netip.MustParseAddr("192.0.2.7"): true}
	for ep, want := range map[string]string{
		"localhost:502": "loopback", "127.0.0.1": "loopback", "[::1]:4840": "loopback", "opc.tcp://localhost:4840": "loopback", "0.0.0.0:502": "loopback",
		"localhost.": "loopback", "LOCALHOST:502": "loopback", "127.1": "loopback", "0x7f.1": "loopback", "2130706433": "loopback", "[::ffff:127.0.0.1]:502": "loopback",
		":502": "unknown", "": "unknown", "192.0.2.7:502": "loopback", "tcp://user@127.0.0.1:1883": "loopback", "foo.localhost": "loopback",
		"localhost.localdomain": "loopback", "ip6-localhost": "loopback", "[fe80::1%lo]:502": "loopback", "gateway.lan:502": "loopback", "nowhere.example": "unknown",
		"10.0.0.11": "network", "plc.example:502": "network", "opc.tcp://10.0.0.12:4840/x": "network", "tcp://broker.example:1883": "network", "192.0.2.8:502": "network",
	} {
		if got := classify(ep, nil, own); got != want {
			t.Errorf("classify(%q) = %s, want %s", ep, got, want)
		}
	}
	virtual := map[string]bool{"10.1.1.1|15020": true, "0.0.0.0|15021": true}
	for ep, want := range map[string]string{
		"10.1.1.1:15020": "virtual", "127.0.0.1:15021": "virtual", "localhost:15021": "virtual", "10.1.1.1:502": "network", "10.1.1.2:15020": "network",
	} {
		if got := classify(ep, virtual, nil); got != want {
			t.Errorf("classify(%q, virtual) = %s, want %s", ep, got, want)
		}
	}
}

// naut checkout verify: ok on a good record and its file, and a failure (exit
// 1) for each way of altering it; the wording never claims a legal signature.
func TestCheckoutVerifyCommand(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0o700) // a state directory only its owner can write
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

// stubDNS makes the resolver the test's: plc.example and broker.example are
// real-looking addresses, gateway.lan is this box, nothing else resolves.
func stubDNS() func() {
	old := lookupHost
	lookupHost = func(h string) ([]net.IP, error) {
		switch h {
		case "plc.example", "broker.example":
			return []net.IP{net.ParseIP("203.0.113.10")}, nil
		case "gateway.lan":
			return []net.IP{net.ParseIP("127.0.1.1")}, nil
		}
		return nil, errors.New("no such host")
	}
	return func() { lookupHost = old }
}

func TestRedactRemovesCredentials(t *testing.T) {
	for in, want := range map[string]string{
		"tcp://user:secret@broker.example:1883": "tcp://broker.example:1883", "ssl://u@h:8883/x": "ssl://h:8883/x",
		"plc.example:502": "plc.example:502", "user:pw@10.0.0.1": "10.0.0.1", "tcp://h/p@th": "tcp://h/p@th",
	} {
		if got := redact(in); got != want {
			t.Errorf("redact(%q) = %q, want %q", in, got, want)
		}
	}
}

// A large sheet on a loopback devices: row gives a short reason (one line per
// device), not one clause per point: a run's entry has a size limit.
func TestEvidenceReasonStaysShortOnALargeSheet(t *testing.T) {
	defer stubDNS()()
	sh := &checkout.Sheet{}
	for i := 0; i < 3000; i++ {
		sh.Points = append(sh.Points, checkout.Point{ID: fmt.Sprintf("P%d", i), Tag: fmt.Sprintf("P%d", i), Kind: checkout.DI, Method: checkout.Observe, Device: "RIO"})
	}
	m := project.Manifest{Driver: project.DriverConfig{Type: "modbus", Host: "plc.example:502"}, Devices: []devbind.Device{{Name: "RIO", Endpoint: "localhost:15021"}}}
	_, why := deriveEvidence(&m, sh, nil, nil, nil)
	if why == "" || len(why) > 2000 || !strings.Contains(why, "3000 point(s) (P0, P1, P2 and 2997 more) are on device RIO") {
		t.Errorf("reason (%d bytes): %.300s", len(why), why)
	}
}
