package webpush

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// decrypt is the user agent's side of RFC 8291, for the fake push service.
func decrypt(t *testing.T, body []byte, ua *ecdh.PrivateKey, authSecret []byte) []byte {
	t.Helper()
	if len(body) < 86 {
		t.Fatalf("body of %d octets", len(body))
	}
	salt, rs, idlen := body[:16], binary.BigEndian.Uint32(body[16:20]), int(body[20])
	if rs != RecordSize || idlen != 65 {
		t.Fatalf("header rs=%d idlen=%d", rs, idlen)
	}
	asPub, ct := body[21:21+idlen], body[21+idlen:]
	as, err := ecdh.P256().NewPublicKey(asPub)
	if err != nil {
		t.Fatal(err)
	}
	secret, _ := ua.ECDH(as)
	info := append(append([]byte("WebPush: info\x00"), ua.PublicKey().Bytes()...), asPub...)
	ikm, _ := hkdf.Key(sha256.New, secret, authSecret, string(info), 32)
	cek, _ := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: aes128gcm\x00", 16)
	nonce, _ := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: nonce\x00", 12)
	blk, _ := aes.NewCipher(cek)
	g, _ := cipher.NewGCM(blk)
	pt, err := g.Open(nil, nonce, ct, nil)
	if err != nil {
		t.Fatalf("the payload does not decrypt: %v", err)
	}
	if pt[len(pt)-1] != 2 {
		t.Fatalf("no delimiter: % x", pt[len(pt)-1:])
	}
	return pt[:len(pt)-1]
}

type fakePush struct {
	*httptest.Server
	mu     sync.Mutex
	hits   int
	status int
	hdr    http.Header
	body   []byte
	redir  string
}

func newFakePush(t *testing.T) *fakePush {
	t.Helper()
	f := &fakePush{status: 201}
	f.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.hits++
		f.hdr = r.Header.Clone()
		f.body, _ = io.ReadAll(r.Body)
		if f.redir != "" && r.URL.Path == "/p/1" {
			http.Redirect(w, r, f.redir, http.StatusFound)
			return
		}
		w.WriteHeader(f.status)
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakePush) sender(t *testing.T, k *Key, p Policy) *Sender {
	pool := x509.NewCertPool()
	pool.AddCert(f.Certificate())
	return &Sender{Key: k, Subject: "mailto:ops@example.com", Policy: p, TLS: &tls.Config{RootCAs: pool}}
}

func subTo(t *testing.T, f *fakePush) (Subscription, *ecdh.PrivateKey, []byte) {
	ua, _ := ecdh.P256().GenerateKey(rand.Reader)
	au := make([]byte, 16)
	rand.Read(au)
	return Subscription{Endpoint: f.URL + "/p/1", P256dh: b64.EncodeToString(ua.PublicKey().Bytes()), Auth: b64.EncodeToString(au)}, ua, au
}

func hostOf(f *fakePush) string { u, _ := url.Parse(f.URL); return u.Hostname() }

func TestSendRoundTrip(t *testing.T) {
	f := newFakePush(t)
	k, _ := NewKey()
	s := f.sender(t, k, Policy{AllowHosts: []string{hostOf(f)}})
	sub, ua, au := subTo(t, f)
	want := []byte(`{"alarm":"HiTemp","state":"active"}`)
	if err := s.Send(context.Background(), sub, want, Options{Topic: "HiTemp", TTL: 90 * time.Second, Urgency: "high"}); err != nil {
		t.Fatal(err)
	}
	if got := decrypt(t, f.body, ua, au); string(got) != string(want) {
		t.Errorf("decrypted %q", got)
	}
	h := f.hdr
	if h.Get("Content-Encoding") != "aes128gcm" || h.Get("TTL") != "90" || h.Get("Urgency") != "high" || h.Get("Topic") != s.topicFor("HiTemp") || h.Get("Topic") == "HiTemp" {
		t.Errorf("headers %v", h)
	}
	rest, _ := strings.CutPrefix(h.Get("Authorization"), "vapid t=")
	jwt, kk, _ := strings.Cut(rest, ", k=")
	pub := dec(t, k.Public())
	c := verifyJWT(t, jwt, pub[1:33], pub[33:65])
	if kk != k.Public() || c["aud"] != f.URL || c["sub"] != "mailto:ops@example.com" {
		t.Errorf("vapid: k=%v claims=%v", kk == k.Public(), c)
	}
}

func TestSendGoneAndErrorsNeverNameTheEndpoint(t *testing.T) {
	f := newFakePush(t)
	k, _ := NewKey()
	s := f.sender(t, k, Policy{AllowHosts: []string{hostOf(f)}})
	sub, _, _ := subTo(t, f)
	for _, c := range []struct {
		status int
		gone   bool
	}{{404, true}, {410, true}, {429, false}, {500, false}, {403, false}} {
		f.status = c.status
		err := s.Send(context.Background(), sub, []byte("x"), Options{})
		se, ok := err.(*SendError)
		if !ok || se.Status != c.status || se.Gone != c.gone {
			t.Errorf("%d: %v", c.status, err)
		}
		if ok && (strings.Contains(se.Error(), sub.Endpoint) || strings.Contains(se.Error(), "/p/1")) {
			t.Errorf("error names the endpoint: %v", se)
		}
	}
	// Not reached: the error is still free of the endpoint.
	f.Close()
	err := s.Send(context.Background(), sub, []byte("x"), Options{})
	if err == nil || strings.Contains(err.Error(), "/p/1") || strings.Contains(err.Error(), hostOf(f)) {
		t.Errorf("unreached: %v", err)
	}
}

func TestSendFollowsNoRedirect(t *testing.T) {
	f := newFakePush(t)
	k, _ := NewKey()
	s := f.sender(t, k, Policy{AllowHosts: []string{hostOf(f)}})
	sub, _, _ := subTo(t, f)
	f.redir = f.URL + "/elsewhere"
	err := s.Send(context.Background(), sub, []byte("x"), Options{})
	if se, ok := err.(*SendError); !ok || se.Status != 302 {
		t.Fatalf("a redirect is a failure: %v", err)
	}
	if f.hits != 1 {
		t.Errorf("the redirect was followed: %d requests", f.hits)
	}
}

// A name that is acceptable when it subscribes cannot be turned on the
// controller's network later: the dial refuses what it resolves to.
func TestDialRefusesPrivateResolution(t *testing.T) {
	f := newFakePush(t)
	u, _ := url.Parse(f.URL)
	cli := newClient(Policy{}, nil)
	resp, err := cli.Get("https://localhost:" + u.Port() + "/p/1")
	if err == nil || !strings.Contains(err.Error(), "refused") {
		if resp != nil {
			resp.Body.Close()
		}
		t.Fatalf("the client was not refused at the dial: %v", err)
	}
	if f.hits != 0 {
		t.Errorf("the service was reached %d times", f.hits)
	}
	// The same host, named in the allow list, is the site's own decision.
	pool := x509.NewCertPool()
	pool.AddCert(f.Certificate())
	cli = newClient(Policy{AllowHosts: []string{"127.0.0.1"}}, &tls.Config{RootCAs: pool})
	resp, err = cli.Get("https://127.0.0.1:" + u.Port() + "/p/2")
	if err != nil {
		t.Fatalf("a named host was refused: %v", err)
	}
	resp.Body.Close()
}

func TestSendPolicyRefusalSendsNothing(t *testing.T) {
	f := newFakePush(t)
	k, _ := NewKey()
	s := f.sender(t, k, Policy{}) // loopback, not named
	sub, _, _ := subTo(t, f)
	if err := s.Send(context.Background(), sub, []byte("x"), Options{}); err == nil || f.hits != 0 {
		t.Errorf("err=%v hits=%d", err, f.hits)
	}
}

// The topic names an alarm to the push service by a keyed hash: stable,
// opaque and valid (22 characters of base64url), never the alarm's id.
func TestTopicIsKeyedAndOpaque(t *testing.T) {
	k1, _ := NewKey()
	k2, _ := NewKey()
	a, b := &Sender{Key: k1}, &Sender{Key: k2}
	got := a.topicFor("HiTemp")
	if got == "HiTemp" || len(got) != 22 || got != a.topicFor("HiTemp") || got == a.topicFor("LoTemp") || got == b.topicFor("HiTemp") {
		t.Errorf("topic %q", got)
	}
}

// A body the service would refuse (over 4096 octets) is refused here, and
// the largest message that fits is exactly 4096.
func TestEncryptStaysUnderTheServiceLimit(t *testing.T) {
	ua, _ := ecdh.P256().GenerateKey(rand.Reader)
	au := make([]byte, 16)
	body, err := Encrypt(make([]byte, MaxPayload), ua.PublicKey().Bytes(), au)
	if err != nil || len(body) != 4096 {
		t.Fatalf("largest: %d, %v", len(body), err)
	}
	if _, err := Encrypt(make([]byte, MaxPayload+1), ua.PublicKey().Bytes(), au); err == nil {
		t.Error("one over was accepted")
	}
}
