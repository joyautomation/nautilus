package project

import (
	"strings"
	"testing"
	"testing/fstest"
)

// server.tls takes absolute paths (a private key never in the project),
// and server.trusted-proxies only addresses and prefixes: naut check
// says so before a box does.
func TestLoadServerTLSAndProxies(t *testing.T) {
	load := func(server string) error {
		_, err := Load(fstest.MapFS{
			"nautilus.yaml": &fstest.MapFile{Data: []byte("name: p\ntasks:\n  - program: main.st\n    scan: 100ms\nserver:\n" + server)},
			"main.st":       &fstest.MapFile{Data: []byte("PROGRAM Main\nEND_PROGRAM\n")},
		}, "")
		return err
	}
	for block, want := range map[string]string{
		"  tls: {cert: certs/plc.crt, key: /etc/nautilus/plc.key}\n": `cert "certs/plc.crt"`,
		"  tls: {cert: /etc/nautilus/plc.crt, key: plc.key}\n":       `key "plc.key"`,
		"  tls: {cert: /etc/nautilus/plc.crt}\n":                     `key ""`,
		"  trusted-proxies: [proxy.example]\n":                       `"proxy.example" is not an address`,
	} {
		if err := load(block); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", strings.TrimSpace(block), err, want)
		}
	}
	p, err := Load(fstest.MapFS{
		"nautilus.yaml": &fstest.MapFile{Data: []byte("name: p\ntasks:\n  - program: main.st\n    scan: 100ms\nserver:\n  tls: {cert: /etc/nautilus/plc.crt, key: /etc/nautilus/plc.key}\n  trusted-proxies: [127.0.0.1, 10.0.0.0/24]\n")},
		"main.st":       &fstest.MapFile{Data: []byte("PROGRAM Main\nEND_PROGRAM\n")},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if c, k := p.TLSFiles(); c != "/etc/nautilus/plc.crt" || k != "/etc/nautilus/plc.key" || len(p.Server.TrustedProxies) != 2 {
		t.Errorf("TLSFiles %q %q, proxies %v", c, k, p.Server.TrustedProxies)
	}
}
