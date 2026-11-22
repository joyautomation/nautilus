package peertls

import (
	"crypto/ed25519"
	crand "crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"
)

const (
	tokenA = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	tokenB = "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
)

func mustPair(t *testing.T, token string) *Pair {
	t.Helper()
	p, err := New(token)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// handshake runs one TLS handshake over loopback TCP (a pipe would block
// on the alert) and returns both ends' errors. A TLS 1.3 client finishes
// before the server has checked its certificate, so it reads once to see
// the server's verdict.
func handshake(t *testing.T, client, server *tls.Config) (cErr, sErr error) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	s, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	defer s.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	_ = s.SetDeadline(time.Now().Add(5 * time.Second))
	sc := tls.Server(s, server)
	done := make(chan error, 1)
	go func() {
		err := sc.Handshake()
		if err == nil {
			_, err = sc.Write([]byte{1})
		}
		if err != nil {
			s.Close()
		}
		done <- err
	}()
	cc := tls.Client(c, client)
	cErr = cc.Handshake()
	if cErr == nil {
		var b [1]byte
		_, cErr = cc.Read(b[:])
	}
	if cErr != nil {
		c.Close()
	}
	return cErr, <-done
}

func TestSameTokenHandshakesBothWays(t *testing.T) {
	a, b := mustPair(t, tokenA), mustPair(t, tokenA)
	if cErr, sErr := handshake(t, a.Client, b.Server); cErr != nil || sErr != nil {
		t.Fatalf("a to b: client %v, server %v", cErr, sErr)
	}
	if cErr, sErr := handshake(t, b.Client, a.Server); cErr != nil || sErr != nil {
		t.Fatalf("b to a: client %v, server %v", cErr, sErr)
	}
}

func TestTheKeyIsTheTokens(t *testing.T) {
	leaf := func(p *Pair) ed25519.PublicKey {
		c, err := x509.ParseCertificate(p.Server.Certificates[0].Certificate[0])
		if err != nil {
			t.Fatal(err)
		}
		return c.PublicKey.(ed25519.PublicKey)
	}
	if !leaf(mustPair(t, tokenA)).Equal(leaf(mustPair(t, tokenA))) {
		t.Fatal("one token gave two keys: the boxes of a pair would not know each other")
	}
	if leaf(mustPair(t, tokenA)).Equal(leaf(mustPair(t, tokenB))) {
		t.Fatal("two tokens gave one key")
	}
}

// A client of another pair (another token) can't pass as a member: the
// server refuses its certificate. The client here accepts any server, as
// one that wants in would, so the server's own check is what refuses it.
func TestServerRefusesAnotherTokensClient(t *testing.T) {
	other := mustPair(t, tokenB).Client.Clone()
	other.VerifyConnection = nil
	_, sErr := handshake(t, other, mustPair(t, tokenA).Server)
	var ie *IdentityError
	if !errors.As(sErr, &ie) {
		t.Fatalf("server: %v, want an IdentityError", sErr)
	}
}

// A server of another pair (or a relay in the middle) can't pass as a
// member: the client refuses its key before it sends a byte of a request.
func TestClientRefusesAnotherTokensServer(t *testing.T) {
	cErr, _ := handshake(t, mustPair(t, tokenA).Client, mustPair(t, tokenB).Server)
	var ie *IdentityError
	if !errors.As(cErr, &ie) || !ie.PeerIdentity() {
		t.Fatalf("client: %v, want an IdentityError", cErr)
	}
}

// A client with no certificate (anyone who reaches the port) gets no TLS
// session.
func TestServerRefusesAClientWithoutACertificate(t *testing.T) {
	bare := &tls.Config{MinVersion: tls.VersionTLS13, InsecureSkipVerify: true}
	_, sErr := handshake(t, bare, mustPair(t, tokenA).Server)
	if sErr == nil {
		t.Fatal("the server took a client with no certificate")
	}
}

// A client holding a certificate of its own making, with the server's key
// unknown to it, is refused too: the key is what counts, not having one.
func TestServerRefusesASelfMadeClientCertificate(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(crand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "nautilus peer"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(crand.Reader, tmpl, tmpl, pub, priv)
	if err != nil {
		t.Fatal(err)
	}
	own := &tls.Config{MinVersion: tls.VersionTLS13, InsecureSkipVerify: true,
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: priv}}}
	_, sErr := handshake(t, own, mustPair(t, tokenA).Server)
	var ie *IdentityError
	if !errors.As(sErr, &ie) {
		t.Fatalf("server: %v, want an IdentityError", sErr)
	}
}

// TLS 1.3 only, on both ends: a server refuses a 1.2 client, and a client
// a 1.2 server (#1230 review: the client side was untested).
func TestOnlyTLS13(t *testing.T) {
	p := mustPair(t, tokenA)
	old := p.Client.Clone()
	old.MinVersion, old.MaxVersion = tls.VersionTLS12, tls.VersionTLS12
	if _, sErr := handshake(t, old, p.Server); sErr == nil {
		t.Fatal("the server took TLS 1.2")
	}
	oldSrv := p.Server.Clone()
	oldSrv.MinVersion, oldSrv.MaxVersion = tls.VersionTLS12, tls.VersionTLS12
	if cErr, _ := handshake(t, p.Client, oldSrv); cErr == nil {
		t.Fatal("the client took TLS 1.2")
	}
}

// The server issues no session tickets, so every connection proves the
// key in a full handshake: a client that keeps a session cache never
// resumes (#1230 review: the claim was untested).
func TestNoSessionResumption(t *testing.T) {
	p := mustPair(t, tokenA)
	client := p.Client.Clone()
	client.ClientSessionCache = tls.NewLRUClientSessionCache(4)
	for i := range 3 {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		go func() {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			sc := tls.Server(c, p.Server)
			if sc.Handshake() == nil {
				_, _ = sc.Write([]byte{1}) // after the handshake, where a ticket would go
			}
			time.Sleep(50 * time.Millisecond)
			sc.Close()
		}()
		cc, err := tls.Dial("tcp", ln.Addr().String(), client)
		if err != nil {
			t.Fatal(err)
		}
		var b [1]byte
		_, _ = cc.Read(b[:])
		if cc.ConnectionState().DidResume {
			t.Fatalf("connection %d resumed a session", i)
		}
		cc.Close()
		ln.Close()
	}
}

// The design never compares wall clocks across hosts, and a controller's
// clock can be years off (a bench box's RTC was): a handshake with each
// end's clock far from the other's, and from the certificate's dates,
// still passes.
func TestClocksYearsApartStillHandshake(t *testing.T) {
	a, b := mustPair(t, tokenA), mustPair(t, tokenA)
	a.Client.Time = func() time.Time { return time.Date(1999, 1, 1, 0, 0, 0, 0, time.UTC) }
	b.Server.Time = func() time.Time { return time.Date(2075, 6, 1, 0, 0, 0, 0, time.UTC) }
	if cErr, sErr := handshake(t, a.Client, b.Server); cErr != nil || sErr != nil {
		t.Fatalf("client %v, server %v", cErr, sErr)
	}
	// Before the certificate's NotBefore, as a box whose clock reset to 0
	// would be: still only the key counts.
	a.Client.Time = func() time.Time { return time.Unix(0, 0).Add(-time.Hour) }
	if cErr, sErr := handshake(t, a.Client, b.Server); cErr != nil || sErr != nil {
		t.Fatalf("clock before 1970: client %v, server %v", cErr, sErr)
	}
}

func TestShortTokenRefused(t *testing.T) {
	_, err := New(strings.Repeat("x", MinToken-1))
	var se *ShortTokenError
	if !errors.As(err, &se) || se.Len != MinToken-1 {
		t.Fatalf("New: %v, want a ShortTokenError", err)
	}
	if _, err := New(strings.Repeat("x", MinToken)); err != nil {
		t.Fatalf("a token of MinToken characters: %v", err)
	}
}
