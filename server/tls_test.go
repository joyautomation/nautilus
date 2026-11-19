package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/leader"
)

// writeCert writes a fresh self-signed certificate for name and its key
// (mode 0600) to dir/<base>.crt and .key, and returns the paths and the
// leaf's DER.
func writeCert(t *testing.T, dir, base, name string) (certPath, keyPath string, der []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: name},
		DNSNames:     []string{name},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err = x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kb, _ := x509.MarshalECPrivateKey(key)
	certPath, keyPath = filepath.Join(dir, base+".crt"), filepath.Join(dir, base+".key")
	// Written beside and renamed in, as a renewal would land.
	for p, b := range map[string][]byte{
		certPath: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		keyPath:  pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}),
	} {
		if err := os.WriteFile(p+".new", b, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(p+".new", p); err != nil {
			t.Fatal(err)
		}
	}
	return certPath, keyPath, der
}

// The certificate is re-read when its files change, no more than once a
// minute; the one it replaced stays pinned until the next renewal, and a
// renewal that cannot be loaded keeps the one served.
func TestCertSourceRenewal(t *testing.T) {
	dir := t.TempDir()
	cp, kp, first := writeCert(t, dir, "plc", "plc.example")
	c, err := NewCertSource(cp, kp)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Now()
	c.now = func() time.Time { return clock }
	served := func() []byte {
		cert, _ := c.GetCertificate(nil)
		return cert.Certificate[0]
	}
	if string(served()) != string(first) || c.Pin() != sha256.Sum256(first) || !c.Pinned(first) {
		t.Fatal("the first certificate")
	}
	_, _, second := writeCert(t, dir, "plc", "plc.example")
	if string(served()) != string(first) {
		t.Error("re-read sooner than a minute")
	}
	clock = clock.Add(certRecheck)
	if string(served()) != string(second) {
		t.Fatal("a renewal was not picked up after a minute")
	}
	if !c.Pinned(first) || !c.Pinned(second) {
		t.Error("the current and the replaced leaf must both be pinned")
	}
	_, _, third := writeCert(t, dir, "plc", "plc.example")
	clock = clock.Add(certRecheck)
	if string(served()) != string(third) || c.Pinned(first) || !c.Pinned(second) {
		t.Error("after a second renewal only the current and the one before stay pinned")
	}
	// A half-copied renewal (the key of another certificate) keeps the
	// one served.
	otherCert, otherKey, _ := writeCert(t, dir, "other", "plc.example")
	b, _ := os.ReadFile(otherKey)
	os.WriteFile(kp, b, 0o600)
	clock = clock.Add(certRecheck)
	if string(served()) != string(third) {
		t.Error("a key that does not match replaced the served certificate")
	}
	if runtime.GOOS != "windows" {
		os.Chmod(kp, 0o604)
		if _, err := NewCertSource(cp, kp); err == nil || !strings.Contains(err.Error(), "chmod o-r") {
			t.Errorf("a world-readable key: %v", err)
		}
		os.Chmod(otherKey, 0o640)
		if _, err := NewCertSource(otherCert, otherKey); err != nil {
			t.Errorf("a group-readable key is the ssl-cert convention: %v", err)
		}
	}
}

// X-Forwarded-Proto and -For are believed only from a trusted proxy; the
// client is the right-most forwarded address that is not one.
func TestTrustedProxies(t *testing.T) {
	p, err := ParseTrustedProxies([]string{"127.0.0.1", "10.1.0.0/16", "::1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseTrustedProxies([]string{"proxy.example"}); err == nil {
		t.Error("a name is not an address")
	}
	srv := New(newTestRuntime(t), Options{TrustedProxies: p})
	for _, c := range []struct {
		remote, proto, xff string
		tls                bool
		secure             bool
		from               string
	}{
		{"192.0.2.7:5000", "", "", false, false, "192.0.2.7"},
		{"192.0.2.7:5000", "https", "198.51.100.1", false, false, "192.0.2.7"}, // not a proxy: both ignored
		{"192.0.2.7:5000", "", "", true, true, "192.0.2.7"},                    // its own TLS
		{"127.0.0.1:5000", "https", "198.51.100.1", false, true, "198.51.100.1"},
		{"[::1]:5000", "HTTPS", "198.51.100.1", false, true, "198.51.100.1"},
		{"127.0.0.1:5000", "http", "203.0.113.9, 198.51.100.1", false, false, "198.51.100.1"},
		{"127.0.0.1:5000", "https", "203.0.113.9, 198.51.100.1, 10.1.2.3", false, true, "198.51.100.1"}, // a chain of proxies
		{"127.0.0.1:5000", "https", "198.51.100.1, garbage", false, true, "127.0.0.1"},
		{"127.0.0.1:5000", "http, https", "", false, true, "127.0.0.1"}, // the proxy's own is the right-most
		{"127.0.0.1:5000", "https, http", "", false, false, "127.0.0.1"},
		{"127.0.0.1:5000", "", "", false, false, "127.0.0.1"},
	} {
		r := httptest.NewRequest("GET", "http://plc.example/api/me", nil)
		r.RemoteAddr = c.remote
		if c.proto != "" {
			r.Header.Set("X-Forwarded-Proto", c.proto)
		}
		if c.xff != "" {
			r.Header.Set("X-Forwarded-For", c.xff)
		}
		if c.tls {
			r.TLS = &tls.ConnectionState{}
		}
		if got := srv.secure(r); got != c.secure {
			t.Errorf("%+v: secure %v", c, got)
		}
		if got := srv.clientAddr(r); got != c.from {
			t.Errorf("%+v: from %q", c, got)
		}
	}
	// The journal and /api/me use them.
	r := httptest.NewRequest("POST", "http://localhost/api/tags", strings.NewReader(`{"name":"SP","value":70}`))
	r.RemoteAddr = "127.0.0.1:5000"
	r.Header.Set("X-Forwarded-For", "198.51.100.1")
	r.Header.Set("X-Forwarded-Proto", "https")
	asrv := auditServer(t, Options{TrustedProxies: p})
	asrv.Handler().ServeHTTP(httptest.NewRecorder(), r)
	if es := asrv.audit.ring.Recent(); len(es) == 0 || es[0].From != "198.51.100.1" {
		t.Errorf("the journal's from behind a trusted proxy: %+v", es)
	}
	r = httptest.NewRequest("GET", "http://localhost/api/me", nil)
	r.RemoteAddr = "127.0.0.1:5000"
	r.Header.Set("X-Forwarded-Proto", "https")
	rec := httptest.NewRecorder()
	asrv.Handler().ServeHTTP(rec, r)
	if !strings.Contains(rec.Body.String(), `"secure":true`) {
		t.Errorf("/api/me behind a trusted TLS proxy: %s", rec.Body)
	}
}

// tlsLeader is a stand-in leader serving cert over TLS the way naut run
// does (tls.NewListener with CertSource.Config), recording what reached
// it.
func tlsLeader(t *testing.T, cp, kp string) (addr string, hits func() int) {
	t.Helper()
	var mu sync.Mutex
	var got []string
	src, err := NewCertSource(cp, kp)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	hs := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, r.Method+" "+r.URL.Path)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})}
	go hs.Serve(tls.NewListener(ln, src.Config()))
	t.Cleanup(func() { hs.Close() })
	return ln.Addr().String(), func() int { mu.Lock(); defer mu.Unlock(); return len(got) }
}

// With server.tls, a standby forwards to the leader over TLS and accepts
// it only if it serves this controller's certificate, current or the one
// it replaced: no name check (the leader is addressed by IP), a pin. A
// leader serving another certificate gets nothing, a 502 that says why,
// and one journal entry until a forward succeeds again.
func TestStandbyForwardsOverTLSToItsOwnCertificate(t *testing.T) {
	dir := t.TempDir()
	cp, kp, _ := writeCert(t, dir, "plc", "plc.example")
	leaderAddr, hits := tlsLeader(t, cp, kp)
	mine, err := NewCertSource(cp, kp)
	if err != nil {
		t.Fatal(err)
	}
	cluster := &movingCluster{st: leader.Status{Mode: "cluster", Pod: "plc-1", Leader: "plc-0", LeaderAddr: leaderAddr}}
	srv := auditServer(t, Options{TLS: mine, Cluster: cluster})
	write := func() *httptest.ResponseRecorder {
		return call(t, srv, "POST", "/api/tags", `{"name":"SP","value":70}`)
	}
	if rec := write(); rec.Code != http.StatusNoContent || hits() != 1 {
		t.Fatalf("forward to a leader serving our certificate: %d %s, leader saw %d", rec.Code, rec.Body, hits())
	}

	// The leader serves another certificate: refused, never sent.
	ocp, okp, _ := writeCert(t, dir, "other", "plc.example")
	otherAddr, otherHits := tlsLeader(t, ocp, okp)
	cluster.set(leader.Status{Mode: "cluster", Pod: "plc-1", Leader: "plc-0", LeaderAddr: otherAddr})
	for range 3 {
		if rec := write(); rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "not this controller's") {
			t.Errorf("a leader with another certificate: %d %s", rec.Code, rec.Body)
		}
	}
	if otherHits() != 0 {
		t.Fatalf("a request reached a leader whose certificate is not ours: %d", otherHits())
	}
	n := 0
	for _, e := range srv.audit.ring.Recent() {
		if e.Action == "cluster.forward-refused" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("the mismatch is journalled %d times, want once", n)
	}

	// A rolling renewal: this box renewed first, the leader still serves
	// the previous certificate, which is still accepted.
	cluster.set(leader.Status{Mode: "cluster", Pod: "plc-1", Leader: "plc-0", LeaderAddr: leaderAddr})
	clock := time.Now()
	mine.now = func() time.Time { return clock }
	writeCert(t, dir, "plc", "plc.example")
	clock = clock.Add(certRecheck)
	mine.Pin() // picks the renewal up
	if rec := write(); rec.Code != http.StatusNoContent {
		t.Errorf("forward to a leader still on our previous certificate: %d %s", rec.Code, rec.Body)
	}
}

// The leader renewed first: it serves a certificate this standby has in
// its files but has not loaded yet (its minute is not up). The mismatch
// sends the standby to its own files at once, and the forward goes
// through.
func TestStandbyForwardsWhenTheLeaderRenewedFirst(t *testing.T) {
	dir := t.TempDir()
	cp, kp, _ := writeCert(t, dir, "plc", "plc.example")
	mine, err := NewCertSource(cp, kp)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Now()
	mine.now = func() time.Time { return clock } // never a minute later
	writeCert(t, dir, "plc", "plc.example")      // the renewal, on both boxes
	leaderAddr, hits := tlsLeader(t, cp, kp)     // the leader has loaded it
	srv := auditServer(t, Options{TLS: mine, Cluster: &movingCluster{st: leader.Status{Mode: "cluster", Pod: "plc-1", Leader: "plc-0", LeaderAddr: leaderAddr}}})
	if rec := call(t, srv, "POST", "/api/tags", `{"name":"SP","value":70}`); rec.Code != http.StatusNoContent || hits() != 1 {
		t.Errorf("forward to a leader that renewed first: %d %s", rec.Code, rec.Body)
	}
}

// A renewal the controller cannot load is journalled once however long it
// lasts (it keeps serving the certificate it has), and the first one that
// loads after it once.
func TestCertReloadFailureIsJournalled(t *testing.T) {
	dir := t.TempDir()
	cp, kp, first := writeCert(t, dir, "plc", "plc.example")
	src, err := NewCertSource(cp, kp)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Now()
	src.now = func() time.Time { return clock }
	srv := auditServer(t, Options{TLS: src})
	count := func(action string) int {
		n := 0
		for _, e := range srv.audit.ring.Recent() {
			if e.Action == action {
				n++
			}
		}
		return n
	}
	_, otherKey, _ := writeCert(t, dir, "other", "plc.example")
	b, _ := os.ReadFile(otherKey)
	os.WriteFile(kp, b, 0o600) // a key that is not the certificate's
	for range 3 {
		clock = clock.Add(certRecheck)
		if cert, _ := src.GetCertificate(nil); string(cert.Certificate[0]) != string(first) {
			t.Fatal("a renewal that cannot load replaced the certificate")
		}
	}
	if count("tls.reload-failed") != 1 || count("tls.reloaded") != 0 {
		t.Errorf("a failing renewal: failed %d, reloaded %d", count("tls.reload-failed"), count("tls.reloaded"))
	}
	writeCert(t, dir, "plc", "plc.example")
	clock = clock.Add(certRecheck)
	src.GetCertificate(nil)
	if count("tls.reloaded") != 1 {
		t.Errorf("the renewal that loaded: reloaded %d", count("tls.reloaded"))
	}
}

// A standby forwards no X-Forwarded-* a client wrote itself: the leader
// sees only the hop the standby adds, and https only when the request
// reached the standby securely.
func TestStandbyForwardDropsClientForwardedHeaders(t *testing.T) {
	var got http.Header
	var mu sync.Mutex
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = r.Header.Clone()
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer backend.Close()
	srv := New(newTestRuntime(t), Options{Cluster: fakeCluster{status: leader.Status{
		Mode: "cluster", Pod: "plc-1", Leader: "plc-0", LeaderAddr: standbyAddr(backend),
	}}})
	call(t, srv, "POST", "/api/tags", `{"name":"SP","value":1}`, func(r *http.Request) {
		r.Header.Set("X-Forwarded-For", "203.0.113.66")
		r.Header.Set("X-Forwarded-Proto", "https")
		r.Header.Set("X-Forwarded-Host", "evil.example")
	})
	mu.Lock()
	defer mu.Unlock()
	if xff := got.Get("X-Forwarded-For"); strings.Contains(xff, "203.0.113.66") || got.Get("X-Forwarded-Proto") != "" || got.Get("X-Forwarded-Host") != "" {
		t.Errorf("the leader saw a client's own forwarded headers: %v", got)
	}
}
