package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"flag"
	"math/big"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gopcua/opcua"
	"github.com/gopcua/opcua/id"
	"github.com/gopcua/opcua/ua"
	"github.com/gopcua/opcua/uacp"
	"github.com/gopcua/opcua/uapolicy"
	"github.com/joyautomation/nautilus/internal/gopcuatest"
	"github.com/joyautomation/nautilus/opcua/internal/uasc"
)

// startSecure starts a server over a fresh runtime with its PKI in a temp
// directory, and returns it with that directory.
func startSecure(t *testing.T) (*Server, string) {
	t.Helper()
	return startSecureWith(t, Options{})
}

// startSecureWith is startSecure with more options (Writable, Logger).
func startSecureWith(t *testing.T, o Options) (*Server, string) {
	t.Helper()
	rt, _ := newRuntime(t)
	dir := filepath.Join(t.TempDir(), "pki")
	o.Endpoint, o.Name, o.Sweep, o.PKI = "localhost:0", "lift-station", 20*time.Millisecond, dir
	s, err := New(rt, o)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	return s, dir
}

// clientCert makes a client application instance certificate for uri,
// valid from notBefore for d.
func clientCert(t *testing.T, uri string, notBefore time.Time, d time.Duration) ([]byte, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(uri)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: "test client " + uri},
		NotBefore:             notBefore,
		NotAfter:              notBefore.Add(d),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageContentCommitment | x509.KeyUsageKeyEncipherment | x509.KeyUsageDataEncipherment | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		URIs:                  []*url.URL{u},
		DNSNames:              []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return der, key
}

// endpointFor is the server's endpoint with policy under mode.
func endpointFor(t *testing.T, s *Server, policy string, mode ua.MessageSecurityMode) *ua.EndpointDescription {
	t.Helper()
	eps, err := opcua.GetEndpoints(context.Background(), s.URL())
	if err != nil {
		t.Fatal(err)
	}
	for _, ep := range eps {
		if ep.SecurityPolicyURI == policy && ep.SecurityMode == mode && ep.EndpointURL == s.URL() {
			return ep
		}
	}
	t.Fatalf("no endpoint %s %s at %s", policy, mode, s.URL())
	return nil
}

// secureConnect connects to s under policy and SignAndEncrypt with the
// client certificate given; extra options follow.
func secureConnect(s *Server, ep *ua.EndpointDescription, der []byte, key *rsa.PrivateKey, uri string, extra ...opcua.Option) (*opcua.Client, error) {
	opts := append([]opcua.Option{
		opcua.SecurityFromEndpoint(ep, ua.UserTokenTypeAnonymous),
		opcua.Certificate(der),
		opcua.PrivateKey(key),
		opcua.ApplicationURI(uri),
		opcua.AutoReconnect(false),
		opcua.DialTimeout(3 * time.Second),
		opcua.RequestTimeout(3 * time.Second),
	}, extra...)
	c, err := opcua.NewClient(s.URL(), opts...)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := gopcuatest.Connect(ctx, c); err != nil {
		return nil, err
	}
	return c, nil
}

// openChannel opens a secure channel to s with the fork's own client side,
// which reports a refusing server's Error message as its status code
// (gopcua's client reads it as EOF), and returns Open's error.
func openChannel(t *testing.T, s *Server, ep *ua.EndpointDescription, policy string, mode ua.MessageSecurityMode, der []byte, key *rsa.PrivateKey) error {
	t.Helper()
	_, err := dialChannel(t, s, ep, policy, mode, der, key)
	return err
}

// dialChannel is openChannel, returning the channel (closed at cleanup).
func dialChannel(t *testing.T, s *Server, ep *ua.EndpointDescription, policy string, mode ua.MessageSecurityMode, der []byte, key *rsa.PrivateKey) (*uasc.SecureChannel, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := uacp.Dial(ctx, s.URL())
	if err != nil {
		t.Fatal(err)
	}
	cfg := &uasc.Config{
		SecurityPolicyURI: policy,
		SecurityMode:      mode,
		Certificate:       der,
		LocalKey:          key,
		RemoteCertificate: ep.ServerCertificate,
		Thumbprint:        uapolicy.Thumbprint(ep.ServerCertificate),
		Lifetime:          uint32(time.Hour / time.Millisecond),
		RequestTimeout:    3 * time.Second,
	}
	sc, err := uasc.NewSecureChannel(s.URL(), conn, cfg, make(chan error, 8))
	if err != nil {
		conn.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { sc.Close() })
	return sc, sc.Open(ctx)
}

// The server makes its own certificate on first start, as Part 6 §6.2.2
// describes one, offers None plus the three secure policies under
// SignAndEncrypt with it, and keeps it across restarts.
func TestServerCertificateIsGeneratedAndKept(t *testing.T) {
	s, dir := startSecure(t)
	der, err := os.ReadFile(filepath.Join(dir, "own", "cert.der"))
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dir, "own", "private", "key.pem"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("key mode %o, want 600", fi.Mode().Perm())
	}
	c, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.URIs) != 1 || c.URIs[0].String() != ApplicationURI("lift-station") {
		t.Errorf("URI SANs %v, want [%s]", c.URIs, ApplicationURI("lift-station"))
	}
	if !contains(c.DNSNames, "localhost") {
		t.Errorf("DNS SANs %v lack localhost", c.DNSNames)
	}
	if len(c.IPAddresses) == 0 || c.IPAddresses[0].String() != "127.0.0.1" {
		t.Errorf("IP SANs %v, want 127.0.0.1", c.IPAddresses)
	}
	want := x509.KeyUsageDigitalSignature | x509.KeyUsageContentCommitment | x509.KeyUsageKeyEncipherment | x509.KeyUsageDataEncipherment | x509.KeyUsageCertSign
	if c.KeyUsage != want {
		t.Errorf("key usage %b, want %b", c.KeyUsage, want)
	}
	if len(c.ExtKeyUsage) != 2 {
		t.Errorf("extended key usage %v, want serverAuth and clientAuth", c.ExtKeyUsage)
	}
	if k, ok := c.PublicKey.(*rsa.PublicKey); !ok || k.N.BitLen() != 2048 {
		t.Errorf("public key %T, want RSA-2048", c.PublicKey)
	}
	if c.SignatureAlgorithm != x509.SHA256WithRSA {
		t.Errorf("signature %s, want SHA256-RSA", c.SignatureAlgorithm)
	}
	if years := c.NotAfter.Sub(c.NotBefore).Hours() / 24 / 365; years < 4.99 || years > 5.01 {
		t.Errorf("validity %.2f years, want 5", years)
	}

	eps, err := opcua.GetEndpoints(context.Background(), s.URL())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]ua.MessageSecurityMode{}
	for _, ep := range eps {
		if ep.EndpointURL != s.URL() {
			continue
		}
		got[ep.SecurityPolicyURI] = ep.SecurityMode
		if string(ep.ServerCertificate) != string(der) {
			t.Errorf("%s endpoint carries another certificate", ep.SecurityPolicyURI)
		}
	}
	wantEps := map[string]ua.MessageSecurityMode{
		ua.SecurityPolicyURINone:                ua.MessageSecurityModeNone,
		ua.SecurityPolicyURIBasic256Sha256:      ua.MessageSecurityModeSignAndEncrypt,
		ua.SecurityPolicyURIAes128Sha256RsaOaep: ua.MessageSecurityModeSignAndEncrypt,
		ua.SecurityPolicyURIAes256Sha256RsaPss:  ua.MessageSecurityModeSignAndEncrypt,
	}
	if len(got) != len(wantEps) {
		t.Errorf("endpoints %v, want %v", got, wantEps)
	}
	for p, m := range wantEps {
		if got[p] != m {
			t.Errorf("endpoint %s: mode %v, want %v", p, got[p], m)
		}
	}
	st := s.Status()
	if st.CertThumbprint != Thumbprint(der) || st.CertExpiresMs != c.NotAfter.UnixMilli() || st.CertWarning != "" {
		t.Errorf("status certificate %q %d %q", st.CertThumbprint, st.CertExpiresMs, st.CertWarning)
	}

	// A restart over the same store keeps the certificate.
	s.Stop()
	rt, _ := newRuntime(t)
	s2, err := New(rt, Options{Endpoint: "localhost:0", Name: "lift-station", PKI: dir})
	if err != nil {
		t.Fatal(err)
	}
	if err := s2.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer s2.Stop()
	if s2.Status().CertThumbprint != Thumbprint(der) {
		t.Error("a restart made a new certificate")
	}
}

// A client whose certificate is not trusted is refused with
// Bad_CertificateUntrusted, and its certificate lands in rejected/; once
// trusted (no restart) the same client connects, under each policy.
func TestUntrustedClientIsRefusedUntilTrusted(t *testing.T) {
	s, dir := startSecure(t)
	const uri = "urn:test:scada"
	der, key := clientCert(t, uri, time.Now().Add(-time.Hour), 24*time.Hour)
	ep := endpointFor(t, s, ua.SecurityPolicyURIBasic256Sha256, ua.MessageSecurityModeSignAndEncrypt)

	if _, err := secureConnect(s, ep, der, key, uri); err == nil {
		t.Fatal("an untrusted client connected")
	}
	if err := openChannel(t, s, ep, ep.SecurityPolicyURI, ep.SecurityMode, der, key); !errors.Is(err, ua.StatusBadCertificateUntrusted) {
		t.Fatalf("untrusted client: %v, want Bad_CertificateUntrusted", err)
	}
	rejected := filepath.Join(dir, "rejected", "certs", Thumbprint(der)+".der")
	if _, err := os.Stat(rejected); err != nil {
		t.Fatalf("the refused certificate is not in rejected/: %v", err)
	}

	info, err := PKI{Dir: dir}.Trust(Thumbprint(der)[:16])
	if err != nil {
		t.Fatal(err)
	}
	if info.ApplicationURI != uri {
		t.Errorf("trusted %q, want %q", info.ApplicationURI, uri)
	}
	for _, p := range securePolicies {
		c, err := secureConnect(s, endpointFor(t, s, p, ua.MessageSecurityModeSignAndEncrypt), der, key, uri)
		if err != nil {
			t.Fatalf("%s after trust: %v", p, err)
		}
		dv := read(t, c, ua.NewNumericNodeID(0, id.Server_ServerStatus_State), ua.AttributeIDValue)
		if dv.Status != ua.StatusGood {
			t.Errorf("%s: read %s", p, dv.Status)
		}
		c.Close(context.Background())
	}
}

// A policy or mode the endpoints do not offer is refused when the channel
// opens: gopcua's server took whatever a client asked for.
func TestUnofferedSecurityIsRejected(t *testing.T) {
	s, dir := startSecure(t)
	const uri = "urn:test:scada"
	der, key := clientCert(t, uri, time.Now().Add(-time.Hour), 24*time.Hour)
	ep := endpointFor(t, s, ua.SecurityPolicyURIBasic256Sha256, ua.MessageSecurityModeSignAndEncrypt)
	trust(t, dir, der)

	for _, c := range []struct {
		policy string
		mode   ua.MessageSecurityMode
	}{
		{ua.SecurityPolicyURIBasic256, ua.MessageSecurityModeSignAndEncrypt},      // deprecated
		{ua.SecurityPolicyURIBasic128Rsa15, ua.MessageSecurityModeSignAndEncrypt}, // deprecated
		{ua.SecurityPolicyURIBasic256Sha256, ua.MessageSecurityModeSign},          // not offered
	} {
		err := openChannel(t, s, ep, c.policy, c.mode, der, key)
		if !errors.Is(err, ua.StatusBadSecurityPolicyRejected) {
			t.Errorf("%s %s: %v, want Bad_SecurityPolicyRejected", c.policy, c.mode, err)
		}
	}
}

// A certificate outside its validity period is Bad_CertificateTimeInvalid,
// trusted or not, and is not filed as rejected.
func TestExpiredClientCertificateIsRefused(t *testing.T) {
	s, dir := startSecure(t)
	const uri = "urn:test:old"
	der, key := clientCert(t, uri, time.Now().Add(-48*time.Hour), 24*time.Hour)
	trust(t, dir, der)
	ep := endpointFor(t, s, ua.SecurityPolicyURIBasic256Sha256, ua.MessageSecurityModeSignAndEncrypt)
	if err := openChannel(t, s, ep, ep.SecurityPolicyURI, ep.SecurityMode, der, key); !errors.Is(err, ua.StatusBadCertificateTimeInvalid) {
		t.Fatalf("expired client: %v, want Bad_CertificateTimeInvalid", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "rejected", "certs", Thumbprint(der)+".der")); err == nil {
		t.Error("an expired certificate was filed as rejected, to be trusted")
	}
}

// The secure channel renews its token under the same security: a client
// with a short channel lifetime keeps reading across several renewals. Each
// renewal is a new token beside the old one; the live one is never touched
// by the reader (it was: the reader wrote the renewal's keys into the live
// instance while the handlers encrypted with it, and about half these runs
// timed out mid-read).
//
// The client is gopcua's own, whose renewal races itself (go test -race
// points into github.com/gopcua/opcua/uasc renew vs sendRequestWithTimeout,
// never into this server), so under the race detector the test renews less
// often, on Basic256Sha256 with a 2 s lifetime, and the server's side is
// still checked for races.
//
// That race can also panic: renew waits on a sync.WaitGroup that every
// request adds to, and nothing stops a request adding while a woken Wait has
// yet to return ("sync: WaitGroup is reused before previous Wait has
// returned", in the renewal goroutine, where no test can recover it). It
// killed the whole package's test binary in CI. So the test runs in a child
// process (this test binary, re-run for this test alone): the child's panic
// is that child's, and a run that dies of exactly that panic is run again.
// Any other failure fails the test as before, and the child is the same
// binary, so under -race the server is still race-checked and under -cover
// its coverage still counts.
func TestSecureChannelRenews(t *testing.T) {
	if os.Getenv(renewChildEnv) == "" {
		runRenewalChild(t)
		return
	}
	s, dir := startSecure(t)
	const uri = "urn:test:renew"
	der, key := clientCert(t, uri, time.Now().Add(-time.Hour), 24*time.Hour)
	trust(t, dir, der)
	policy, lifetime := ua.SecurityPolicyURIAes256Sha256RsaPss, time.Second
	if raceEnabled {
		policy, lifetime = ua.SecurityPolicyURIBasic256Sha256, 2*time.Second
	}
	ep := endpointFor(t, s, policy, ua.MessageSecurityModeSignAndEncrypt)
	c, err := secureConnect(s, ep, der, key, uri, opcua.Lifetime(lifetime))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(context.Background())
	deadline := time.Now().Add(5 * time.Second) // two renewals at least
	for time.Now().Before(deadline) {
		dv := read(t, c, ua.NewNumericNodeID(0, id.Server_ServerStatus_State), ua.AttributeIDValue)
		if dv.Status != ua.StatusGood {
			t.Fatalf("read across renewal: %s", dv.Status)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// renewChildEnv marks the child process TestSecureChannelRenews runs in.
const renewChildEnv = "NAUTILUS_TEST_RENEW_CHILD"

// runRenewalChild runs TestSecureChannelRenews in a child process and fails
// t as the child failed, running it again (three tries in all) only when the
// child died of gopcua's renewal WaitGroup panic.
func runRenewalChild(t *testing.T) {
	t.Helper()
	args := []string{"-test.run=^TestSecureChannelRenews$", "-test.count=1", "-test.v"}
	// Under -cover the child writes its counters where the parent's are
	// merged from.
	if f := flag.Lookup("test.gocoverdir"); f != nil && f.Value.String() != "" {
		args = append(args, "-test.gocoverdir="+f.Value.String())
	}
	const tries = 3
	for try := 1; try <= tries; try++ {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		cmd := exec.CommandContext(ctx, os.Args[0], args...)
		cmd.Env = append(os.Environ(), renewChildEnv+"=1")
		out, err := cmd.CombinedOutput()
		cancel()
		if err == nil && bytes.Contains(out, []byte("--- PASS: TestSecureChannelRenews")) {
			return
		}
		if !gopcuaRenewalPanic(out) {
			t.Fatalf("child run: %v\n%s", err, out)
		}
		t.Logf("try %d of %d: gopcua's client renewal panicked on its own WaitGroup (not this server):\n%s", try, tries, panicLines(out))
	}
	t.Fatalf("gopcua's client renewal panicked in all %d tries", tries)
}

// gopcuaRenewalPanic reports whether out is a process dying of gopcua's
// secure channel WaitGroup misuse: a sync.WaitGroup panic whose goroutine is
// in github.com/gopcua/opcua/uasc's SecureChannel.
func gopcuaRenewalPanic(out []byte) bool {
	i := bytes.Index(out, []byte("panic: sync: WaitGroup"))
	return i >= 0 && bytes.Contains(out[i:], []byte("github.com/gopcua/opcua/uasc.(*SecureChannel)"))
}

// panicLines is the panic message and the frames under it.
func panicLines(out []byte) string {
	s := string(out[bytes.Index(out, []byte("panic: ")):])
	lines := strings.SplitN(s, "\n", 12)
	return strings.Join(lines[:len(lines)-1], "\n")
}

// The status row warns 30 days before the server certificate expires.
func TestStatusWarnsBeforeTheCertificateExpires(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "pki")
	der, key, err := newServerCertValid(ApplicationURI("lift-station"), time.Now().Add(-time.Hour), 10*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	writeOwn(t, dir, der, key)
	rt, _ := newRuntime(t)
	s, err := New(rt, Options{Endpoint: "localhost:0", Name: "lift-station", PKI: dir})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	if w := s.Status().CertWarning; !strings.Contains(w, "expires in 10 days") {
		t.Errorf("warning %q, want one saying it expires in 10 days", w)
	}
}

func trust(t *testing.T, dir string, der []byte) {
	t.Helper()
	d := filepath.Join(dir, "trusted", "certs")
	if err := os.MkdirAll(d, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "client.der"), der, 0o644); err != nil {
		t.Fatal(err)
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// newServerCertValid is a server certificate for appURI valid from
// notBefore for d, for a store a test fills by hand.
func newServerCertValid(appURI string, notBefore time.Time, d time.Duration) ([]byte, *rsa.PrivateKey, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, err
	}
	u, _ := url.Parse(appURI)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test server"},
		NotBefore: notBefore, NotAfter: notBefore.Add(d),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageDataEncipherment | x509.KeyUsageCertSign,
		BasicConstraintsValid: true, URIs: []*url.URL{u}, DNSNames: []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	return der, key, err
}

func writeOwn(t *testing.T, dir string, der []byte, key *rsa.PrivateKey) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "own", "private"), 0o700); err != nil {
		t.Fatal(err)
	}
	pk, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "own", "private", "key.pem"), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pk}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "own", "cert.der"), der, 0o644); err != nil {
		t.Fatal(err)
	}
}

// CreateSession on a secured channel is held to the channel's client: the
// clientCertificate must be the one the channel opened with, and the
// applicationUri the one that certificate names (Part 4 §5.6.2).
func TestCreateSessionIsHeldToTheChannelCertificate(t *testing.T) {
	s, dir := startSecure(t)
	const uri = "urn:test:scada"
	der, key := clientCert(t, uri, time.Now().Add(-time.Hour), 24*time.Hour)
	other, _ := clientCert(t, "urn:test:other", time.Now().Add(-time.Hour), 24*time.Hour)
	trust(t, dir, der)
	ep := endpointFor(t, s, ua.SecurityPolicyURIBasic256Sha256, ua.MessageSecurityModeSignAndEncrypt)
	sc, err := dialChannel(t, s, ep, ep.SecurityPolicyURI, ep.SecurityMode, der, key)
	if err != nil {
		t.Fatal(err)
	}
	create := func(cert []byte, appURI string) error {
		req := &ua.CreateSessionRequest{
			ClientDescription:       &ua.ApplicationDescription{ApplicationURI: appURI, ApplicationName: ua.NewLocalizedText("test"), ApplicationType: ua.ApplicationTypeClient},
			EndpointURL:             s.URL(),
			SessionName:             "test",
			ClientNonce:             make([]byte, 32),
			ClientCertificate:       cert,
			RequestedSessionTimeout: 60000,
		}
		return sc.SendRequest(context.Background(), req, nil, func(ua.Response) error { return nil })
	}
	if err := create(other, uri); !errors.Is(err, ua.StatusBadSecurityChecksFailed) {
		t.Errorf("another certificate: %v, want Bad_SecurityChecksFailed", err)
	}
	if err := create(nil, uri); !errors.Is(err, ua.StatusBadSecurityChecksFailed) {
		t.Errorf("no certificate: %v, want Bad_SecurityChecksFailed", err)
	}
	if err := create(der, "urn:test:impostor"); !errors.Is(err, ua.StatusBadCertificateURIInvalid) {
		t.Errorf("another ApplicationUri: %v, want Bad_CertificateUriInvalid", err)
	}
	if err := create(der, uri); err != nil {
		t.Errorf("the channel's own certificate and URI: %v", err)
	}
}

// A certificate followed by its issuer (a chain) is judged, filed and
// trusted by its leaf.
func TestVerifyUsesTheLeafOfAChain(t *testing.T) {
	pki := PKI{Dir: filepath.Join(t.TempDir(), "pki")}
	leaf, _ := clientCert(t, "urn:test:leaf", time.Now().Add(-time.Hour), 24*time.Hour)
	issuer, _ := clientCert(t, "urn:test:issuer", time.Now().Add(-time.Hour), 24*time.Hour)
	chain := append(append([]byte(nil), leaf...), issuer...)
	if err := pki.Verify(chain); !errors.Is(err, ua.StatusBadCertificateUntrusted) {
		t.Fatalf("an untrusted chain: %v, want Bad_CertificateUntrusted", err)
	}
	raw, err := os.ReadFile(filepath.Join(pki.Dir, "rejected", "certs", Thumbprint(leaf)+".der"))
	if err != nil || string(raw) != string(leaf) {
		t.Fatalf("rejected/ does not hold the leaf under its thumbprint: %v", err)
	}
	if _, err := pki.Trust(Thumbprint(leaf)); err != nil {
		t.Fatal(err)
	}
	if err := pki.Verify(chain); err != nil {
		t.Errorf("the trusted leaf's chain: %v", err)
	}
}

// rejected/ keeps the newest 100: anyone who reaches the port can file one.
func TestRejectedIsCapped(t *testing.T) {
	pki := PKI{Dir: filepath.Join(t.TempDir(), "pki")}
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	for i := range maxRejected + 5 {
		tmpl := &x509.Certificate{SerialNumber: big.NewInt(int64(i + 1)), Subject: pkix.Name{CommonName: "flood"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
		if err != nil {
			t.Fatal(err)
		}
		_ = pki.Verify(der)
	}
	ents, _ := os.ReadDir(filepath.Join(pki.Dir, "rejected", "certs"))
	if len(ents) != maxRejected {
		t.Errorf("rejected/ holds %d certificates, want %d", len(ents), maxRejected)
	}
}

// Approving needs 16 hex digits of the thumbprint: a shorter prefix is cheap
// to match with a certificate made for the purpose.
func TestTrustNeedsSixteenDigits(t *testing.T) {
	pki := PKI{Dir: filepath.Join(t.TempDir(), "pki")}
	der, _ := clientCert(t, "urn:test:scada", time.Now().Add(-time.Hour), 24*time.Hour)
	_ = pki.Verify(der)
	if _, err := pki.Trust(Thumbprint(der)[:15]); err == nil {
		t.Error("a 15-digit prefix was enough to trust")
	}
	if _, err := pki.Trust(Thumbprint(der)[:16]); err != nil {
		t.Errorf("16 digits: %v", err)
	}
}

// A certificate that does not parse is refused with Bad_CertificateInvalid,
// said to the client (the refusal comes from the reader, before the OPN
// handler).
func TestUnparseableClientCertificateIsRefused(t *testing.T) {
	s, _ := startSecure(t)
	_, key := clientCert(t, "urn:test:junk", time.Now().Add(-time.Hour), 24*time.Hour)
	ep := endpointFor(t, s, ua.SecurityPolicyURIBasic256Sha256, ua.MessageSecurityModeSignAndEncrypt)
	if err := openChannel(t, s, ep, ep.SecurityPolicyURI, ep.SecurityMode, []byte("not a certificate"), key); !errors.Is(err, ua.StatusBadCertificateInvalid) {
		t.Fatalf("junk certificate: %v, want Bad_CertificateInvalid", err)
	}
}
