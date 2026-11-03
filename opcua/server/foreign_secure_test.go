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
// session that claims another ApplicationUri; None stays open. The trusted
// client writes each type with asyncua's plain write_value, which sends a
// Good StatusCode, a SourceTimestamp and an Int64 for a Python int, across
// renewals too, and is refused what does not fit; anonymous on None is
// refused writes. Gated on NAUTILUS_OPCUA_PYTHON like the rest of the
// foreign suite.
func TestForeignAsyncuaSecure(t *testing.T) {
	python := os.Getenv("NAUTILUS_OPCUA_PYTHON")
	if python == "" {
		t.Skip("set NAUTILUS_OPCUA_PYTHON=/path/to/python (with asyncua installed) to run the foreign-stack test")
	}
	s, dir := startSecureWith(t, Options{Writable: []string{"Running", "Count", "Level", "Label", "Elapsed", "State", "P101.Drive", "Levels"}})
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
	if ren["reads"] != float64(20) || ren["writes"] != float64(20) || ren["count"] != float64(19) {
		t.Errorf("renewals: %v, want 20 reads and 20 writes, Count 19", ren)
	}
	wr := next()
	connected(wr)
	got, _ := wr["writes"].(map[string]any)
	lands := map[string]any{
		"Running": false, "Count": float64(7), "Count32": float64(8), "Level": 4.25, "Label": "Pump 9", "Empty": "",
		"Elapsed": 1500.0, "State": float64(10), "P101.Drive.Speed": 1450.0,
	}
	for name, want := range lands {
		r, _ := got[name].(map[string]any)
		if r["ok"] != true || r["value"] != want {
			t.Errorf("asyncua write %s: %v, want it to land as %v", name, r, want)
		}
	}
	refusals := map[string]string{
		"CountFraction": "BadTypeMismatch", "CountTooBig": "BadOutOfRange", "StateNotMember": "BadOutOfRange",
		"ElapsedFraction": "BadOutOfRange", "LevelNaN": "BadOutOfRange", "Array": "BadWriteNotSupported", "Unmatched": "BadNotWritable",
	}
	for name, code := range refusals {
		r, _ := got[name].(map[string]any)
		if r["ok"] == true || !strings.Contains(fmt.Sprint(r["error"]), code) {
			t.Errorf("asyncua write %s: %v, want %s", name, r, code)
		}
	}
	if got["userAccessLevel"] != float64(3) {
		t.Errorf("the writer reads UserAccessLevel %v on Level, want 3", got["userAccessLevel"])
	}
	if v, _ := s.rt.Tags().ReadPath("P101.Drive.Speed"); v != 1450.0 {
		t.Errorf("the store holds P101.Drive.Speed = %v", v)
	}
	refused(next(), "BadSecurityPolicyRejected")
	refused(next(), "BadCertificateUriInvalid")
	none := next()
	connected(none)
	if w, _ := none["write"].(map[string]any); !strings.Contains(fmt.Sprint(w["error"]), "BadUserAccessDenied") {
		t.Errorf("anonymous on None wrote: %v", none)
	}
	if none["userAccessLevel"] != float64(1) {
		t.Errorf("anonymous reads UserAccessLevel %v, want 1", none["userAccessLevel"])
	}
	if done := next(); done["done"] != true {
		t.Errorf("expected the done line, got %v", done)
	}
}
