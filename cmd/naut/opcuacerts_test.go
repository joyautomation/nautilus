package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	opcuaserver "github.com/joyautomation/nautilus/opcua/server"
)

// naut opcua certs lists a refused client; naut opcua trust approves it by
// a thumbprint prefix, moving it to trusted/, after which the store's own
// check passes it.
func TestOPCUACertsAndTrust(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "pki")
	pki := opcuaserver.PKI{Dir: dir}
	if _, _, _, _, err := pki.Own("urn:joyautomation:nautilus:t", "t", []string{"localhost"}); err != nil {
		t.Fatal(err)
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse("urn:test:scada")
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(7), Subject: pkix.Name{CommonName: "SCADA"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), URIs: []*url.URL{u}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := pki.Verify(der); err == nil {
		t.Fatal("an unknown client certificate was trusted")
	}
	thumb := opcuaserver.Thumbprint(der)

	out := captureStdout(t, func() int { return runOPCUACerts([]string{"--pki", dir}) })
	for _, want := range []string{"Server certificate", "urn:joyautomation:nautilus:t", "Rejected clients", thumb, "urn:test:scada", "naut opcua trust"} {
		if !strings.Contains(out, want) {
			t.Errorf("certs output lacks %q:\n%s", want, out)
		}
	}

	if code := runOPCUATrust([]string{"--pki", dir, "abc"}); code != 1 {
		t.Errorf("a 3-digit prefix: exit %d, want 1", code)
	}
	out = captureStdout(t, func() int { return runOPCUATrust([]string{"--pki", dir, strings.ToUpper(thumb[:8])}) })
	if !strings.Contains(out, "trusted "+thumb) {
		t.Errorf("trust output:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(dir, "trusted", "certs", thumb+".der")); err != nil {
		t.Errorf("not moved to trusted/: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "rejected", "certs", thumb+".der")); err == nil {
		t.Error("still in rejected/")
	}
	if err := pki.Verify(der); err != nil {
		t.Errorf("after trust, Verify: %v", err)
	}
	if code := runOPCUATrust([]string{"--pki", dir, thumb[:8]}); code != 1 {
		t.Errorf("trusting it again: exit %d, want 1 (already trusted)", code)
	}
}
