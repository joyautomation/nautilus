package main

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// isolateState points every state directory a test's commands could use
// at a temporary one: never the developer's own ~/.local/state.
func isolateState(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	t.Setenv("XDG_STATE_HOME", d)
	t.Setenv("STATE_DIRECTORY", "")
	t.Setenv("INVOCATION_ID", "")
	return d
}

func leafOf(t *testing.T, path string) *x509.Certificate {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair(raw, raw)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	return leaf
}

// The first start makes the certificate: one file holding the key and the
// certificate, mode 0600, valid for the names given and under 398 days,
// a server certificate a client that trusts it verifies by name. Later
// starts keep it (same fingerprint: a browser that accepted it keeps
// accepting it), until one within 30 days of its expiry makes a new one.
func TestEnsureSelfSigned(t *testing.T) {
	sd := t.TempDir()
	now := time.Now()
	c, err := ensureSelfSigned(sd, "plant", []string{"localhost", "127.0.0.1", "plant-ipc"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Made || c.Renewed || c.Path != filepath.Join(sd, "tls", "self-signed.pem") {
		t.Errorf("first: %+v", c)
	}
	if st, _ := os.Stat(c.Path); runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", st.Mode())
	}
	leaf := leafOf(t, c.Path)
	if certFingerprint(leaf.Raw) != c.Fingerprint || len(c.Fingerprint) != 95 {
		t.Errorf("fingerprint %q", c.Fingerprint)
	}
	if d := leaf.NotAfter.Sub(leaf.NotBefore); d > 398*24*time.Hour {
		t.Errorf("valid for %v: browsers refuse a server certificate over 398 days", d)
	}
	roots := x509.NewCertPool()
	roots.AddCert(leaf)
	for _, h := range []string{"localhost", "127.0.0.1", "plant-ipc"} {
		if _, err := leaf.Verify(x509.VerifyOptions{DNSName: h, Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
			t.Errorf("not valid for %s: %v", h, err)
		}
	}
	if _, err := leaf.Verify(x509.VerifyOptions{DNSName: "elsewhere", Roots: roots}); err == nil {
		t.Error("valid for a name it was not made for")
	}
	again, err := ensureSelfSigned(sd, "plant", []string{"localhost"}, now.Add(300*24*time.Hour))
	if err != nil || again.Made || again.Renewed || again.Fingerprint != c.Fingerprint {
		t.Errorf("a later start did not keep it: %+v %v", again, err)
	}
	renewed, err := ensureSelfSigned(sd, "plant", []string{"localhost"}, c.NotAfter.Add(-29*24*time.Hour))
	if err != nil || !renewed.Renewed || renewed.Fingerprint == c.Fingerprint {
		t.Errorf("within 30 days of expiry: %+v %v", renewed, err)
	}
	if st, _ := os.Stat(c.Path); runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Errorf("renewed mode %v", st.Mode())
	}
	if left, _ := filepath.Glob(filepath.Join(sd, "tls", ".self-signed-*")); len(left) != 0 {
		t.Errorf("temporary files left: %v", left)
	}
}

// A file that is not a certificate and its key is someone's: refused,
// naming the fix, never replaced. A second start that finds the file made
// between its look and its link uses that one (the link fails; it does not
// overwrite).
func TestEnsureSelfSignedKeepsWhatIsThere(t *testing.T) {
	sd := t.TempDir()
	os.MkdirAll(filepath.Join(sd, "tls"), 0o700)
	path := filepath.Join(sd, "tls", "self-signed.pem")
	os.WriteFile(path, []byte("not a certificate"), 0o600)
	if _, err := ensureSelfSigned(sd, "p", nil, time.Now()); err == nil || !strings.Contains(err.Error(), "delete it") {
		t.Errorf("a broken file: %v", err)
	}
	if raw, _ := os.ReadFile(path); string(raw) != "not a certificate" {
		t.Error("a broken file was replaced")
	}
	os.Remove(path)
	first, _ := makeSelfSigned("p", nil, time.Now())
	if err := placeFile(path, first, false); err != nil {
		t.Fatal(err)
	}
	second, _ := makeSelfSigned("p", nil, time.Now())
	if err := placeFile(path, second, false); !os.IsExist(err) {
		t.Errorf("a second create: %v, want it to find the file there", err)
	}
	if raw, _ := os.ReadFile(path); string(raw) != string(first) {
		t.Error("a second create replaced the first")
	}
}

// tlsSource with self-signed: the certificate in the state directory,
// told with its fingerprint; NAUTILUS_TLS_CERT/_KEY win over it; under
// systemd with no state directory it refuses, naming the fix.
func TestTLSSourceSelfSigned(t *testing.T) {
	state := isolateState(t)
	proj := t.TempDir()
	var said string
	spec := tlsSpec{selfSigned: true, name: "plant", devFallback: true, listen: "127.0.0.1:8443", say: func(s string) { said += s }}
	src, err := tlsSource(spec, proj, os.Getenv)
	if err != nil || src == nil {
		t.Fatalf("%v", err)
	}
	path := filepath.Join(state, "nautilus", "plant", "tls", "self-signed.pem")
	leaf := leafOf(t, path)
	if src.Pin() != sha256.Sum256(leaf.Raw) {
		t.Error("the source serves another certificate than the file's")
	}
	if !strings.Contains(said, certFingerprint(leaf.Raw)) || !strings.Contains(said, "Browsers warn") || !strings.Contains(said, "server.tls: {cert:") {
		t.Errorf("notice:\n%s", said)
	}
	// The environment's pair wins.
	cert, key, _ := selfSigned(t, t.TempDir())
	env := func(k string) string {
		switch k {
		case "NAUTILUS_TLS_CERT":
			return cert
		case "NAUTILUS_TLS_KEY":
			return key
		}
		return os.Getenv(k)
	}
	said = ""
	if src, err := tlsSource(spec, proj, env); err != nil || src.Pin() == sha256.Sum256(leaf.Raw) || said != "" {
		t.Errorf("NAUTILUS_TLS_CERT/_KEY did not win: %v %q", err, said)
	}
	// Under systemd, no state directory: refused, naming it.
	sysd := func(k string) string {
		if k == "INVOCATION_ID" {
			return "x"
		}
		if k == "STATE_DIRECTORY" {
			return ""
		}
		return os.Getenv(k)
	}
	if _, err := tlsSource(spec, proj, sysd); err == nil || !strings.Contains(err.Error(), "StateDirectory=") {
		t.Errorf("no state directory under systemd: %v", err)
	}
}
