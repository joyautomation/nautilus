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
	"fmt"
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

	"github.com/joyautomation/nautilus/audit"
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
	if _, err := ParseTrustedProxies([]string{"10.0.0.5/8"}); err == nil || !strings.Contains(err.Error(), "host bits") {
		t.Errorf("10.0.0.5/8 is a typo, not a /8: %v", err)
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
	// A new connection, so the leader's leaf is checked now: it is this
	// box's PREVIOUS one.
	srv.leaderTLS.CloseIdleConnections()
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

// Every change of the certificate served is journalled (tls.changed)
// with the new and the replaced fingerprint, once (#1114 review: a
// renewal must never swap it unannounced); a recheck that finds nothing
// new, or the replaced leaf dropped later, journals nothing more.
func TestCertChangeIsJournalled(t *testing.T) {
	dir := t.TempDir()
	cp, kp, first := writeCert(t, dir, "plc", "plc.example")
	src, err := NewCertSource(cp, kp)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Now()
	src.now = func() time.Time { return clock }
	srv := auditServer(t, Options{TLS: src})
	changes := func() []audit.Entry {
		var out []audit.Entry
		for _, e := range srv.audit.ring.Recent() {
			if e.Action == "tls.changed" {
				out = append(out, e)
			}
		}
		return out
	}
	clock = clock.Add(certRecheck)
	src.GetCertificate(nil)
	if n := len(changes()); n != 0 {
		t.Fatalf("no change, %d entries", n)
	}
	_, _, second := writeCert(t, dir, "plc", "plc.example")
	clock = clock.Add(certRecheck)
	src.GetCertificate(nil)
	clock = clock.Add(certRecheck)
	src.GetCertificate(nil)
	clock = clock.Add(prevKeep + certRecheck)
	src.Pinned(second) // drops the replaced leaf: the pins change, the leaf served does not
	got := changes()
	if len(got) != 1 {
		t.Fatalf("%d tls.changed entries, want 1: %+v", len(got), got)
	}
	d := got[0].Detail
	if d["fingerprint"] != fingerprint(sha256.Sum256(second)) || d["replaced"] != fingerprint(sha256.Sum256(first)) || d["not_after"] == nil {
		t.Errorf("detail %v", d)
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

// A connection a standby keeps to the leader was checked against the
// leaves accepted then: after two renewals here (neither of which the
// leader has), the next forward is checked again and refused, not sent on
// the old connection.
func TestStandbyRechecksPooledConnectionsAfterARenewal(t *testing.T) {
	dir := t.TempDir()
	cp, kp, _ := writeCert(t, dir, "plc", "plc.example")
	leaderAddr, hits := tlsLeader(t, cp, kp) // stays on the first
	mine, err := NewCertSource(cp, kp)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Now()
	mine.now = func() time.Time { return clock }
	srv := auditServer(t, Options{TLS: mine, Cluster: &movingCluster{st: leader.Status{Mode: "cluster", Pod: "plc-1", Leader: "plc-0", LeaderAddr: leaderAddr}}})
	write := func() int { return call(t, srv, "POST", "/api/tags", `{"name":"SP","value":70}`).Code }
	if code := write(); code != http.StatusNoContent {
		t.Fatalf("the first forward: %d", code)
	}
	for range 2 {
		writeCert(t, dir, "plc", "plc.example")
		clock = clock.Add(certRecheck)
		mine.Pin()
	}
	if code := write(); code != http.StatusBadGateway || hits() != 1 {
		t.Errorf("after two renewals the leader's leaf is neither: %d, leader saw %d", code, hits())
	}
}

// The replaced leaf is accepted only until the leader shows the current
// one, and for a day at most.
func TestPreviousLeafIsBounded(t *testing.T) {
	dir := t.TempDir()
	cp, kp, first := writeCert(t, dir, "plc", "plc.example")
	c, err := NewCertSource(cp, kp)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Now()
	c.now = func() time.Time { return clock }
	_, _, second := writeCert(t, dir, "plc", "plc.example")
	clock = clock.Add(certRecheck)
	c.Pin()
	if !c.Pinned(first) {
		t.Fatal("the previous leaf, right after the renewal")
	}
	if !c.Pinned(second) || c.Pinned(first) {
		t.Error("once the leader shows the current leaf, the previous one is no longer accepted")
	}
	_, _, third := writeCert(t, dir, "plc", "plc.example")
	clock = clock.Add(certRecheck)
	c.Pin()
	if !c.Pinned(second) {
		t.Fatal("the previous leaf after another renewal")
	}
	clock = clock.Add(prevKeep)
	if c.Pinned(second) || !c.Pinned(third) {
		t.Error("the previous leaf a day later")
	}
}

// The key's mode is checked on a renewal too: one every user may read is
// refused, the certificate served is kept, and the failure journalled.
func TestRenewalWithAWorldReadableKeyIsRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mode bits")
	}
	dir := t.TempDir()
	cp, kp, first := writeCert(t, dir, "plc", "plc.example")
	src, err := NewCertSource(cp, kp)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Now()
	src.now = func() time.Time { return clock }
	srv := auditServer(t, Options{TLS: src})
	writeCert(t, dir, "plc", "plc.example")
	os.Chmod(kp, 0o644)
	clock = clock.Add(certRecheck)
	if cert, _ := src.GetCertificate(nil); string(cert.Certificate[0]) != string(first) {
		t.Error("a renewal with a world-readable key replaced the certificate")
	}
	found := false
	for _, e := range srv.audit.ring.Recent() {
		found = found || (e.Action == "tls.reload-failed" && strings.Contains(fmt.Sprint(e.Detail), "chmod o-r"))
	}
	if !found {
		t.Error("the refused renewal is not journalled")
	}
}

// Sign-in, sign-out and /api/me are decided at the box the client
// reached, never forwarded (the #1043 review's must-do): at the leader,
// secure() would judge the standby's TLS hop, not the client's plain one.
// A forwarding standby answers /api/me itself, and refers sign-in,
// sign-out and any request with the sign-in cookie (and no bearer) to the
// leader: 503, its address in Location and "leader", the cookie left as it
// is. The leader sees none of them; a bearer request is still forwarded.
// The address is the leader's configured public URL, never its peer
// address (TestStandbyReferralWithoutAPublicURL).
func TestStandbyDecidesSignInAndMeItself(t *testing.T) {
	dir := t.TempDir()
	cp, kp, _ := writeCert(t, dir, "plc", "plc.example")
	addr, hits := tlsLeader(t, cp, kp)
	mine, err := NewCertSource(cp, kp)
	if err != nil {
		t.Fatal(err)
	}
	status := leader.Status{Mode: "cluster", Pod: "plc-1", Leader: "plc-0", LeaderAddr: addr}
	standby := New(newTestRuntime(t), Options{TLS: mine, AuthToken: "tok", Cluster: fakeCluster{status: status},
		LeaderURLs: map[string]string{"plc-0": "https://plc-0.plant.example", "plc-1": "https://plc-1.plant.example"}})
	req := func(method, path string, mod ...func(*http.Request)) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://10.0.0.7/api/"+path, strings.NewReader(`{"name":"akhan","password":"pw"}`))
		r.RemoteAddr = "192.0.2.9:5555" // plain HTTP, not loopback
		r.Header.Set("Origin", "http://10.0.0.7")
		for _, m := range mod {
			m(r)
		}
		return serve(standby, r)
	}
	cookie := func(r *http.Request) { r.AddCookie(&http.Cookie{Name: plainCookie, Value: "some-session"}) }
	secureOne := func(r *http.Request) { r.AddCookie(&http.Cookie{Name: secureCookie, Value: "some-session"}) }

	rec := req("GET", "me")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"secure":false`) {
		t.Errorf("/api/me without a cookie: %d %s, want the standby's own answer (secure false)", rec.Code, rec.Body)
	}
	for _, c := range []struct {
		method, path string
		mod          []func(*http.Request)
	}{
		{"POST", "signin", nil},
		{"POST", "signout", []func(*http.Request){cookie}},
		// Never forwarded, whatever else it carries.
		{"POST", "signout", nil},
		{"GET", "me", []func(*http.Request){cookie}},
		{"GET", "state", []func(*http.Request){cookie}},
		{"POST", "tags", []func(*http.Request){cookie}},
		{"GET", "state", []func(*http.Request){secureOne}},
	} {
		rec := req(c.method, c.path, c.mod...)
		want := "https://plc-0.plant.example"
		if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Location") != want+"/" || !strings.Contains(rec.Body.String(), `"leader":"`+want+`"`) ||
			!strings.Contains(rec.Body.String(), `"standby":true`) {
			t.Errorf("%s /api/%s on the standby: %d Location %q %s; want 503 naming %s", c.method, c.path, rec.Code, rec.Header().Get("Location"), rec.Body, want)
		}
		if sc := rec.Header().Values("Set-Cookie"); len(sc) > 0 {
			t.Errorf("%s /api/%s on the standby set a cookie: %v", c.method, c.path, sc)
		}
	}
	if n := hits(); n != 0 {
		t.Fatalf("the leader received %d requests, want none", n)
	}
	if strings.Contains(req("POST", "signin").Body.String(), addr) {
		t.Error("the referral names the leader's peer address")
	}
	// A cookie-carrying write the standby refers is journalled through the
	// route's seam, as its other own answers are.
	if es := standby.audit.ring.Recent(); len(es) == 0 || es[0].Action != "tag.write" || es[0].Result != audit.ResultError {
		t.Errorf("the referred write is not journalled: %+v", es)
	}
	// A bearer credential is forwarded as before, cookie or not.
	req("GET", "state", cookie, func(r *http.Request) { r.Header.Set("Authorization", "Bearer tok") })
	if n := hits(); n != 1 {
		t.Errorf("a bearer request with a cookie: the leader received %d, want 1", n)
	}
}

// Without redundancy.public-urls a standby's referral names no address:
// the peer address is the pair's, and the answer goes to anyone. The
// sign-in page says to open the leader, with no link and no form.
func TestStandbyReferralWithoutAPublicURL(t *testing.T) {
	standby, _, _ := patServer(t)
	standby.cluster = fakeCluster{status: leader.Status{Mode: "quorum", Pod: "b", Leader: "a", LeaderAddr: "10.9.8.7:8086"}}
	r := httptest.NewRequest("POST", "http://10.0.0.7/api/signin", strings.NewReader(`{}`))
	r.Header.Set("Origin", "http://10.0.0.7")
	rec := serve(standby, r)
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Location") != "" || strings.Contains(rec.Body.String(), "10.9.8.7") ||
		strings.Contains(rec.Body.String(), `"leader"`) || !strings.Contains(rec.Body.String(), "open the leader") || !strings.Contains(rec.Body.String(), `"standby":true`) {
		t.Errorf("sign-in on a standby with no public URL: %d Location %q %s", rec.Code, rec.Header().Get("Location"), rec.Body)
	}
	page := func() string {
		r := httptest.NewRequest("GET", "http://10.0.0.7/signin", nil)
		r.RemoteAddr = "127.0.0.1:1" // where the form would otherwise show
		return serve(standby, r).Body.String()
	}
	body := page()
	if !strings.Contains(body, "standby") || strings.Contains(body, "10.9.8.7") || strings.Contains(body, "<form") || strings.Contains(body, "<a ") {
		t.Errorf("the sign-in page on a standby with no public URL: %s", body)
	}
	standby.leaderURLs = map[string]string{"a": "https://plc-a.plant.example"}
	body = page()
	if !strings.Contains(body, `href="https://plc-a.plant.example/signin"`) || strings.Contains(body, "<form") {
		t.Errorf("the sign-in page on a standby with a public URL: %s", body)
	}
}
