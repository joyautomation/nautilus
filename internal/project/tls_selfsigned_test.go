package project

import (
	"strings"
	"testing"
	"testing/fstest"
)

// server.tls is self-signed or {cert, key}: any other scalar, a misspelt
// key or a key given twice is refused; self-signed needs no absolute
// paths; on a redundant pair it is refused, naming the fix (#708 DX-B).
func TestServerTLSSelfSigned(t *testing.T) {
	base := "name: p\ntasks:\n  - program: main.st\n    scan: 100ms\n"
	load := func(extra string) (*Project, error) {
		return Load(fstest.MapFS{
			"nautilus.yaml": {Data: []byte(base + extra)},
			"main.st":       {Data: []byte("PROGRAM Main\nEND_PROGRAM\n")},
		}, "")
	}
	p, err := load("server:\n  tls: self-signed\n")
	if err != nil {
		t.Fatal(err)
	}
	if !p.TLSSelfSigned() {
		t.Error("TLSSelfSigned is false")
	}
	if c, k := p.TLSFiles(); c != "" || k != "" {
		t.Errorf("TLSFiles for self-signed: %q %q", c, k)
	}
	if p, err := load("server:\n  tls: {cert: /etc/c.pem, key: /etc/k.pem}\n"); err != nil || p.TLSSelfSigned() {
		t.Errorf("{cert, key}: %v", err)
	} else if c, k := p.TLSFiles(); c != "/etc/c.pem" || k != "/etc/k.pem" {
		t.Errorf("TLSFiles: %q %q", c, k)
	}
	if p, err := load(""); err != nil || p.TLSSelfSigned() {
		t.Errorf("no server.tls: %v", err)
	}
	for _, tc := range []struct{ extra, want string }{
		{"server:\n  tls: selfsigned\n", "not \"selfsigned\""},
		{"server:\n  tls: true\n", "not \"true\""},
		{"server:\n  tls: {cert: /a, kye: /b}\n", "field kye not found"},
		{"server:\n  tls: {cert: /a, cert: /b, key: /c}\n", "given twice"},
		{"server:\n  tls: [self-signed]\n", "server.tls is self-signed, or"},
		{"server:\n  tls: {cert: rel.pem, key: /b}\n", "absolute path"},
		{"server:\n  tls: self-signed\nredundancy: {}\n", "cannot be used with redundancy"},
	} {
		if _, err := load(tc.extra); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: %v, want %q", tc.extra, err, tc.want)
		}
	}
	// The pair refusal is the manifest's, so naut check and every command
	// that edits the manifest see it, and it names the fix.
	_, err = ReadManifest(fstest.MapFS{"nautilus.yaml": {Data: []byte(base + "server:\n  tls: self-signed\nredundancy: {}\n")}}, "")
	if err == nil || !strings.Contains(err.Error(), "server.tls: {cert:") {
		t.Errorf("ReadManifest, self-signed on a pair: %v", err)
	}
}
