package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// selfSigned writes a certificate for 127.0.0.1 and localhost, and its
// key (0600), into dir, and returns their paths and a pool trusting it.
func selfSigned(t *testing.T, dir string) (cert, key string, pool *x509.CertPool) {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "localhost"},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	kb, _ := x509.MarshalECPrivateKey(k)
	cert, key = filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644)
	os.WriteFile(key, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0o600)
	leaf, _ := x509.ParseCertificate(der)
	pool = x509.NewCertPool()
	pool.AddCert(leaf)
	return cert, key, pool
}

// server.tls is a pair of absolute paths outside the project; the
// environment overrides it; one half alone is refused; neither is no TLS.
func TestTLSSource(t *testing.T) {
	proj := t.TempDir()
	cert, key, _ := selfSigned(t, t.TempDir())
	none := func(string) string { return "" }
	if s, err := tlsSource("", "", proj, none); s != nil || err != nil {
		t.Errorf("no certificate: %v %v", s, err)
	}
	if s, err := tlsSource(cert, key, proj, none); s == nil || err != nil {
		t.Errorf("a certificate outside the project: %v %v", s, err)
	}
	icert, ikey, _ := selfSigned(t, proj)
	for _, c := range []struct {
		name, cert, key string
		getenv          func(string) string
		want            string
	}{
		{"cert alone", cert, "", none, "needs its key"},
		{"relative", "tls.crt", "tls.key", none, "relative"},
		{"inside the project", icert, ikey, none, "inside the project"},
		{"the environment's, inside the project", cert, key, func(k string) string {
			return map[string]string{"NAUTILUS_TLS_CERT": icert, "NAUTILUS_TLS_KEY": ikey}[k]
		}, "inside the project"},
	} {
		if _, err := tlsSource(c.cert, c.key, proj, c.getenv); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
	// The environment alone, with nothing in the manifest.
	env := func(k string) string { return map[string]string{"NAUTILUS_TLS_CERT": cert, "NAUTILUS_TLS_KEY": key}[k] }
	if s, err := tlsSource("", "", proj, env); s == nil || err != nil {
		t.Errorf("NAUTILUS_TLS_CERT/_KEY: %v %v", s, err)
	}
}

// naut logix serve and naut siemens serve listen over TLS when they have
// a certificate, and without one warn loudly off loopback (local tools:
// a warning, not a refusal).
func TestFacadeListener(t *testing.T) {
	cert, key, _ := selfSigned(t, t.TempDir())
	src, err := tlsSource(cert, key, "", func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	for _, c := range []struct {
		listen string
		tls    bool
		scheme string
		warn   bool
	}{
		{"0.0.0.0:8080", false, "http", true},
		{"localhost:8080", false, "http", false},
		{"0.0.0.0:8080", true, "https", false},
	} {
		var stderr bytes.Buffer
		s := src
		if !c.tls {
			s = nil
		}
		_, scheme := facadeListener(ln, s, c.listen, "naut logix serve", &stderr)
		if scheme != c.scheme || strings.Contains(stderr.String(), "CLEARTEXT") != c.warn {
			t.Errorf("%+v: scheme %s, stderr %q", c, scheme, stderr.String())
		}
	}
}

// naut siemens serve takes the project's server.tls.
func TestSiemensServeTakesServerTLS(t *testing.T) {
	dir := t.TempDir()
	cert, key, _ := selfSigned(t, t.TempDir())
	m := "name: s7\ntasks:\n  - program: main.st\n    scan: 100ms\nserver:\n  tls: {cert: " + cert + ", key: " + key + "}\n"
	os.WriteFile(filepath.Join(dir, "nautilus.yaml"), []byte(m), 0o644)
	c, err := resolveSiemensServe("192.0.2.10", "naut", "ab", dir, nil, time.Second, "127.0.0.1:0", pw)
	if err != nil {
		t.Fatal(err)
	}
	if o := siemensFacadeOptions(c, nil); o.Server.TLS == nil {
		t.Error("the facade's server has no TLS")
	}
}

// naut run with server.tls serves the tag API over HTTPS, with that
// certificate, and says https in its banner; plain HTTP to it is refused.
func TestNautRunServesHTTPS(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the CLI")
	}
	bin := buildNautCLI(t, t.TempDir(), nil)
	cert, key, pool := selfSigned(t, t.TempDir())
	dir := t.TempDir()
	m := "name: secure\ntasks:\n  - program: main.st\n    scan: 20ms\n" +
		"tags:\n  - { name: SP, role: setpoint, type: REAL, init: 1 }\n" +
		"server:\n  addr: \"127.0.0.1:0\"\n  tls: {cert: " + cert + ", key: " + key + "}\n"
	os.WriteFile(filepath.Join(dir, "nautilus.yaml"), []byte(m), 0o644)
	os.WriteFile(filepath.Join(dir, "main.st"), []byte("PROGRAM Main\nVAR_EXTERNAL\n SP : REAL;\nEND_VAR\nEND_PROGRAM\n"), 0o644)
	cmd := exec.Command(bin, "run")
	cmd.Dir = dir
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "NAUTILUS_") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	stdout, stderr := &lockedBuffer{}, &lockedBuffer{}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() { cmd.Wait(); close(exited) }()
	t.Cleanup(func() { cmd.Process.Kill(); <-exited })
	api := apiAddrFromBanner(t, stdout.String, exited, stderr.String)
	if !strings.Contains(stdout.String(), "tag API on https://") {
		t.Errorf("the banner does not say https: %s", stdout.String())
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}, Timeout: 10 * time.Second}
	res, err := client.Get("https://" + api + "/api/me")
	if err != nil {
		t.Fatalf("GET over TLS: %v\nstderr:\n%s", err, stderr.String())
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK || !strings.Contains(string(body), `"secure":true`) {
		t.Errorf("/api/me over TLS: %d %s", res.StatusCode, body)
	}
	if res, err := http.Get("http://" + api + "/api/me"); err == nil {
		res.Body.Close()
		if res.StatusCode == http.StatusOK {
			t.Error("plain HTTP to the TLS port was answered")
		}
	}
}
