package server

import (
	"bufio"
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The secure endpoints against asyncua (testdata/foreign/asyncua_secure.py):
// a client certificate the server does not trust is refused and filed;
// trusted, it connects under each of the three policies, keeps reading
// across channel renewals, and is still refused Sign-only security and a
// session that claims another ApplicationUri; None stays open. Gated on
// NAUTILUS_OPCUA_PYTHON like the rest of the foreign suite.
func TestForeignAsyncuaSecure(t *testing.T) {
	python := os.Getenv("NAUTILUS_OPCUA_PYTHON")
	if python == "" {
		t.Skip("set NAUTILUS_OPCUA_PYTHON=/path/to/python (with asyncua installed) to run the foreign-stack test")
	}
	s, dir := startSecure(t)
	const uri = "urn:test:asyncua"
	der, key := clientCert(t, uri, time.Now().Add(-time.Hour), 24*time.Hour)
	tmp := t.TempDir()
	certFile, keyFile := filepath.Join(tmp, "client.der"), filepath.Join(tmp, "client-key.pem")
	if err := os.WriteFile(certFile, der, 0o600); err != nil {
		t.Fatal(err)
	}
	pk, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pk}), 0o600); err != nil {
		t.Fatal(err)
	}

	script, err := filepath.Abs(filepath.Join("testdata", "foreign", "asyncua_secure.py"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, python, "-I", script, s.URL(), certFile, keyFile, uri, filepath.Join(dir, "own", "cert.der"))
	var stderr strings.Builder
	cmd.Stderr = &stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Wait()
	lines := bufio.NewScanner(stdout)
	next := func() map[string]any {
		t.Helper()
		if !lines.Scan() {
			cmd.Wait()
			t.Fatalf("asyncua client ended early (%v); stderr:\n%s", cmd.ProcessState, stderr.String())
		}
		var m map[string]any
		if err := json.Unmarshal(lines.Bytes(), &m); err != nil {
			t.Fatalf("asyncua client printed %q: %v", lines.Text(), err)
		}
		return m
	}
	refused := func(m map[string]any, code string) {
		t.Helper()
		if m["ok"] == true || !strings.Contains(fmt.Sprint(m["error"]), code) {
			t.Errorf("%s: %v, want refused with %s", m["step"], m, code)
		}
	}
	connected := func(m map[string]any) {
		t.Helper()
		if m["ok"] != true || m["state"] != float64(0) { // ServerState Running
			t.Errorf("%s %v: %v, want connected and Running", m["step"], m["policy"], m)
		}
	}

	refused(next(), "BadCertificateUntrusted")
	if _, err := os.Stat(filepath.Join(dir, "rejected", "certs", Thumbprint(der)+".der")); err != nil {
		t.Fatalf("asyncua's certificate is not in rejected/: %v", err)
	}
	if _, err := (PKI{Dir: dir}).Trust(Thumbprint(der)[:16]); err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(stdin, "trusted")

	for range securePolicies {
		connected(next())
	}
	ren := next()
	connected(ren)
	if ren["reads"] != float64(20) {
		t.Errorf("renewals: %v, want 20 reads", ren)
	}
	refused(next(), "BadSecurityPolicyRejected")
	refused(next(), "BadCertificateUriInvalid")
	connected(next())
	if done := next(); done["done"] != true {
		t.Errorf("expected the done line, got %v", done)
	}
}
