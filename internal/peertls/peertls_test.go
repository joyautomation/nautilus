package peertls

import (
	"context"
	"crypto/ed25519"
	crand "crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	tokenA  = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	tokenB  = "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
	cluster = "0123456789abcdef0123456789abcdef"
	other   = "fedcba9876543210fedcba9876543210"
)

// tm is a test member: its peer TLS, its state directory, its box and
// the events it reported.
type tm struct {
	t    *testing.T
	p    *Peer
	dir  string
	self string
	mu   sync.Mutex
	box  Box
	evs  []Event
}

// newMember starts a member's peer TLS in a state directory of its own.
func newMember(t *testing.T, token, self string) *tm {
	t.Helper()
	return newMemberIn(t, token, self, t.TempDir())
}

// newMemberIn starts member self of cluster with its files in dir (a
// restart keeps dir).
func newMemberIn(t *testing.T, token, self, dir string) *tm {
	t.Helper()
	m := &tm{t: t, dir: dir, self: self, box: Box{MachineID: "m-" + self, NICs: []string{"02:00:00:00:00:0" + self[:1]}}}
	cl := cluster
	accept := func(c, n string) bool { return c == cluster && n == map[string]string{"a": "b", "b": "a"}[self] }
	pins := func(string) string { return filepath.Join(dir, "peer-pins.json") }
	if self == Witness {
		cl = ""
		accept = func(c, n string) bool { return validCluster(c) && (n == "a" || n == "b") }
		pins = func(c string) string { return filepath.Join(dir, c+".peer-pins.json") }
	}
	p, err := New(Options{Token: token, Cluster: cl, Self: self, KeyFile: filepath.Join(dir, "peer-key.json"),
		Box: func() (Box, error) { m.mu.Lock(); defer m.mu.Unlock(); return m.box, nil }, PinsFile: pins, Accept: accept,
		OnEvent: func(e Event) { m.mu.Lock(); m.evs = append(m.evs, e); m.mu.Unlock() }})
	if err != nil {
		t.Fatal(err)
	}
	m.p = p
	return m
}

func (m *tm) events(k EventKind) []Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Event
	for _, e := range m.evs {
		if e.Kind == k {
			out = append(out, e)
		}
	}
	return out
}

func (m *tm) fp() string {
	m.t.Helper()
	f, err := m.p.Fingerprint()
	if err != nil {
		m.t.Fatal(err)
	}
	return f
}

// serve runs m's peer listener: TLS, Bind, and a handler that answers
// with the member Bind verified ("<cluster>/<name>") and records whether
// the caller's pin was on disk when it ran.
func (m *tm) serve() string {
	m.t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		m.t.Fatal(err)
	}
	type key struct{}
	h := m.p.Bind(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		who := r.Context().Value(key{}).(string)
		_, name, _ := strings.Cut(who, "/")
		c, _, _ := strings.Cut(who, "/")
		path := m.p.o.PinsFile(c)
		b, _ := os.ReadFile(path)
		if !strings.Contains(string(b), `"`+name+`"`) {
			who += " UNPINNED-ON-DISK"
		}
		_, _ = io.WriteString(w, who)
	}), func(r *http.Request, c, n string) *http.Request {
		return r.WithContext(context.WithValue(r.Context(), key{}, c+"/"+n))
	})
	srv := &http.Server{Handler: h, ErrorLog: nil}
	go func() { _ = srv.Serve(tls.NewListener(ln, m.p.Server())) }()
	m.t.Cleanup(func() { srv.Close() })
	return ln.Addr().String()
}

// client is an HTTP client of m's calls to the member named want.
func (m *tm) client(want string) *http.Client {
	return &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			raw, err := (&net.Dialer{}).DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			return m.p.Wrap(ctx, raw, want, addr)
		}}}
}

// get calls addr with c: the verified name the server answered with, or
// the error (an HTTP refusal as an error with its text).
func get(c *http.Client, addr string) (string, error) {
	resp, err := c.Get("https://" + addr + "/")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", &httpErr{resp.StatusCode, resp.Header.Get(RefusedHeader), string(b)}
	}
	return string(b), nil
}

type httpErr struct {
	code   int
	reason string
	body   string
}

func (e *httpErr) Error() string { return e.body }

// The first call between two members pins each one's key at the other,
// on disk before the first request is served, journalled with the
// fingerprint each shows in its own status.
func TestFirstContactPinsBothEnds(t *testing.T) {
	a, b := newMember(t, tokenA, "a"), newMember(t, tokenA, "b")
	addr := b.serve()
	who, err := get(a.client("b"), addr)
	if err != nil {
		t.Fatal(err)
	}
	if who != cluster+"/a" {
		t.Fatalf("b's handler saw %q, want the verified %s/a with its pin on disk", who, cluster)
	}
	if got := a.p.Pinned("")["b"]; got != b.fp() {
		t.Fatalf("a pinned b as %q, b's own key is %q", got, b.fp())
	}
	if got := b.p.Pinned("")["a"]; got != a.fp() {
		t.Fatalf("b pinned a as %q, a's own key is %q", got, a.fp())
	}
	for _, m := range []*tm{a, b} {
		ev := m.events(EventPinned)
		if len(ev) != 1 || ev[0].Fingerprint == "" || ev[0].Addr == "" || ev[0].Time.IsZero() {
			t.Fatalf("%s's pin events: %+v", m.self, ev)
		}
		if nk := m.events(EventNewKey); len(nk) != 1 || nk[0].Fingerprint != m.fp() {
			t.Fatalf("%s's new-key events: %+v", m.self, nk)
		}
		for _, f := range []string{"peer-key.json", "peer-pins.json"} {
			st, err := os.Stat(filepath.Join(m.dir, f))
			if err != nil || st.Mode().Perm() != 0o600 {
				t.Fatalf("%s's %s: %v, mode %v; want 0600", m.self, f, err, st)
			}
		}
	}
	// A restart keeps the key and the pins: the second call pins nothing.
	a2 := newMemberIn(t, tokenA, "a", a.dir)
	if _, err := get(a2.client("b"), addr); err != nil {
		t.Fatal(err)
	}
	if a2.fp() != a.fp() || len(a2.events(EventPinned)) != 0 || len(a2.events(EventNewKey)) != 0 {
		t.Fatalf("after a restart: key %s (was %s), pins %+v, new keys %+v", a2.fp(), a.fp(), a2.events(EventPinned), a2.events(EventNewKey))
	}
}

// Once a name is pinned, another key under it is refused, on both ends,
// loudly: a named refusal that names the unpin command, and an event with
// both fingerprints. The token alone is no longer enough.
func TestAChangedKeyIsRefused(t *testing.T) {
	a, b := newMember(t, tokenA, "a"), newMember(t, tokenA, "b")
	addr := b.serve()
	if _, err := get(a.client("b"), addr); err != nil {
		t.Fatal(err)
	}
	// Another box with the token, calling as a: refused at b.
	imp := newMember(t, tokenA, "a")
	_, err := get(imp.client("b"), addr)
	if err == nil {
		t.Fatal("b took a second key for a")
	}
	ref := b.events(EventRefused)
	if len(ref) != 1 || ref[0].Name != "a" || ref[0].Pinned != a.fp() || ref[0].Fingerprint != imp.fp() || !strings.Contains(ref[0].Detail, "naut redundancy unpin a") {
		t.Fatalf("b's refusal events: %+v", ref)
	}
	// And b's key changed (a new box answering at b's address): a refuses
	// it at the handshake, before the token header is sent.
	b2 := newMember(t, tokenA, "b")
	addr2 := b2.serve()
	_, err = get(a.client("b"), addr2)
	var ie *IdentityError
	if !errors.As(err, &ie) || !ie.PinMismatch() || ie.Pinned != b.fp() || !strings.Contains(err.Error(), "naut redundancy unpin b") {
		t.Fatalf("a's call to a new b: %v, want a pin mismatch naming the unpin", err)
	}
}

// A certificate the token's key did not sign is refused, whatever it
// names: another token's, or one of the box's own making.
func TestAnUnsignedCertificateIsRefused(t *testing.T) {
	a, b := newMember(t, tokenA, "a"), newMember(t, tokenA, "b")
	addr := b.serve()
	if _, err := get(newMember(t, tokenB, "a").client("b"), addr); err == nil {
		t.Fatal("b took another token's a")
	}
	// The other way: a calls a b of another token.
	if _, err := get(a.client("b"), newMember(t, tokenB, "b").serve()); err == nil {
		t.Fatal("a took another token's b")
	}
	// Self-signed, naming a.
	pub, priv, _ := ed25519.GenerateKey(crand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(3), Subject: pkix.Name{CommonName: cluster + "/a"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, _ := x509.CreateCertificate(crand.Reader, tmpl, tmpl, pub, priv)
	own := &tls.Config{MinVersion: tls.VersionTLS13, InsecureSkipVerify: true,
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: priv}}}
	if _, sErr := handshake(t, own, b.p.Server()); sErr == nil {
		t.Fatal("b took a self-signed certificate")
	}
	if len(b.p.Pinned("")) != 0 {
		t.Fatalf("b pinned %v", b.p.Pinned(""))
	}
}

// A certificate must name the member dialed, and a listener takes calls
// only from the members that make them: a controller only from its peer
// of its own cluster, the witness from a or b of any cluster. A nameless
// certificate (#1230's, the token key's own) is refused outright.
func TestNamesMustMatch(t *testing.T) {
	a, b, w := newMember(t, tokenA, "a"), newMember(t, tokenA, "b"), newMember(t, tokenA, Witness)
	bAddr, wAddr := b.serve(), w.serve()
	// a dials the witness's address and finds b there.
	var ie *IdentityError
	if _, err := get(a.client(Witness), bAddr); !errors.As(err, &ie) || ie.Want != Witness {
		t.Fatalf("a dialing the witness, b answering: %v", err)
	}
	// and the other way.
	if _, err := get(a.client("b"), wAddr); !errors.As(err, &ie) || ie.Want == "" {
		t.Fatalf("a dialing b, the witness answering: %v", err)
	}
	// b takes no call from a member named b (its own name) or from
	// another cluster's a.
	if _, err := get(newMember(t, tokenA, "b").client("b"), bAddr); err == nil {
		t.Fatal("b took a call from a member named b")
	}
	oc := newMember(t, tokenA, "a")
	oc.p.o.Cluster = other // its certificate names the other cluster
	if _, err := get(oc.client(Witness), wAddr); err != nil {
		t.Fatalf("the witness refused another cluster's a: %v", err)
	}
	if _, err := get(newMember(t, tokenA, Witness).client(Witness), wAddr); err == nil {
		t.Fatal("the witness took a call from a member named witness")
	}
	// The pins of each cluster are the witness's per cluster.
	if w.p.Pinned(other)["a"] != oc.fp() || len(w.p.Pinned(cluster)) != 0 {
		t.Fatalf("witness pins: %v / %v", w.p.Pinned(other), w.p.Pinned(cluster))
	}
	if _, err := os.Stat(filepath.Join(w.dir, other+".peer-pins.json")); err != nil {
		t.Fatal(err)
	}
	// Nameless: the token key's own certificate, #1230's form.
	seed := a.p.tokenKey.Seed()
	nk := ed25519.NewKeyFromSeed(seed)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "nautilus peer"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, _ := x509.CreateCertificate(crand.Reader, tmpl, tmpl, nk.Public(), nk)
	legacy := &tls.Config{MinVersion: tls.VersionTLS13, InsecureSkipVerify: true,
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: nk}}}
	if _, sErr := handshake(t, legacy, b.p.Server()); !errors.As(sErr, &ie) {
		t.Fatalf("b took a nameless certificate: %v", sErr)
	}
}

// An unreadable pins file is never silent and never an outage: moved
// aside, an event, and every name pins again on first contact.
func TestUnreadablePinsAreMovedAside(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "peer-pins.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	b := newMemberIn(t, tokenA, "b", dir)
	ev := b.events(EventPinsReset)
	if len(ev) != 1 || !strings.Contains(ev[0].Detail, "kept at") {
		t.Fatalf("reset events: %+v", ev)
	}
	kept, _ := filepath.Glob(path + ".unreadable-*")
	if len(kept) != 1 {
		t.Fatalf("moved aside: %v", kept)
	}
	a := newMember(t, tokenA, "a")
	if _, err := get(a.client("b"), b.serve()); err != nil {
		t.Fatalf("a's first contact after the reset: %v", err)
	}
	if b.p.Pinned("")["a"] != a.fp() {
		t.Fatal("a not pinned again")
	}
}

// The pin is checked on every request, not only at the handshake: a
// connection made under a pin that has since been dropped and pinned to
// another key carries no more requests.
func TestThePinIsCheckedOnEveryRequest(t *testing.T) {
	a, b := newMember(t, tokenA, "a"), newMember(t, tokenA, "b")
	addr := b.serve()
	old := a.client("b")
	if _, err := get(old, addr); err != nil {
		t.Fatal(err)
	}
	if err := b.p.Drop("", "a", "test: a replaced"); err != nil {
		t.Fatal(err)
	}
	if d := b.events(EventDropped); len(d) != 1 || d[0].Fingerprint != a.fp() {
		t.Fatalf("drop events: %+v", d)
	}
	a2 := newMember(t, tokenA, "a")
	if _, err := get(a2.client("b"), addr); err != nil {
		t.Fatal(err)
	}
	_, err := get(old, addr) // the old connection, kept alive
	var he *httpErr
	if !errors.As(err, &he) || he.code != http.StatusForbidden || he.reason != RefusedPeerKey {
		t.Fatalf("the old key's kept connection: %v, want a 403 peer-key refusal", err)
	}
}

// A pin that can't be written refuses the contact: no request crosses an
// unpinned connection.
func TestAPinThatCannotBeWrittenRefuses(t *testing.T) {
	a, b := newMember(t, tokenA, "a"), newMember(t, tokenA, "b")
	b.p.pins.write = func(string, []byte) error { return errors.New("disk full") }
	_, err := get(a.client("b"), b.serve())
	var he *httpErr
	if !errors.As(err, &he) || he.code != http.StatusServiceUnavailable || he.reason == RefusedPeerKey || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("call: %v, want the failed pin as a 503 that blames no key", err)
	}
	if len(b.p.Pinned("")) != 0 {
		t.Fatal("pinned in memory without the write")
	}
	if f := b.events(EventPinFailed); len(f) == 0 || !strings.Contains(f[0].Detail, "disk full") {
		t.Fatalf("the failed pin is silent on the box that failed: %+v", b.evs)
	}
}

// A certificate that names a member and is signed by the token's key, but
// whose private key the caller does not hold, pins nothing: the pin is
// written only after the handshake has proved the key.
func TestNoPinWithoutProofOfTheKey(t *testing.T) {
	a, b := newMember(t, tokenA, "a"), newMember(t, tokenA, "b")
	cert, err := a.p.certificate()
	if err != nil {
		t.Fatal(err)
	}
	_, wrong, _ := ed25519.GenerateKey(crand.Reader)
	forged := &tls.Config{MinVersion: tls.VersionTLS13, InsecureSkipVerify: true,
		Certificates: []tls.Certificate{{Certificate: cert.Certificate, PrivateKey: wrong}}}
	_, sErr := handshake(t, forged, b.p.Server())
	if sErr == nil {
		t.Fatal("the handshake passed without the key")
	}
	if len(b.p.Pinned("")) != 0 || len(b.events(EventPinned)) != 0 {
		t.Fatalf("pinned without proof: %v", b.p.Pinned(""))
	}
}

// The same, the other way: a server that shows a member's certificate
// without its private key pins nothing at the caller.
func TestNoPinWithoutProofOfTheKeyOnACall(t *testing.T) {
	a, b := newMember(t, tokenA, "a"), newMember(t, tokenA, "b")
	cert, err := b.p.certificate()
	if err != nil {
		t.Fatal(err)
	}
	_, wrong, _ := ed25519.GenerateKey(crand.Reader)
	srv := b.p.serverFor("")
	srv.GetCertificate = func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
		return &tls.Certificate{Certificate: cert.Certificate, PrivateKey: wrong}, nil
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err == nil {
			_ = tls.Server(c, srv).Handshake()
			c.Close()
		}
	}()
	raw, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.p.Wrap(t.Context(), raw, "b", ln.Addr().String()); err == nil {
		t.Fatal("the call took a server that did not prove the key")
	}
	if len(a.p.Pinned("")) != 0 {
		t.Fatalf("pinned without proof: %v", a.p.Pinned(""))
	}
}

// A dropped pin's key is not pinned again, by a connection kept from
// before the drop either: the next contact with another key is (#710 2b's
// unpin, where the dropped key may be a token holder's).
func TestADroppedKeyIsNotPinnedAgain(t *testing.T) {
	a, b := newMember(t, tokenA, "a"), newMember(t, tokenA, "b")
	addr := b.serve()
	old := a.client("b")
	if _, err := get(old, addr); err != nil {
		t.Fatal(err)
	}
	if err := b.p.Drop("", "a", "test"); err != nil {
		t.Fatal(err)
	}
	var he *httpErr
	for range 3 {
		if _, err := get(old, addr); !errors.As(err, &he) || he.code != http.StatusForbidden || !strings.Contains(he.body, "pin was dropped") {
			t.Fatalf("the dropped key's kept connection: %v, want refused as the dropped key", err)
		}
	}
	if n := len(b.events(EventRefused)); n != 1 {
		t.Fatalf("%d refusal events for the dropped key's three requests, want 1", n)
	}
	if p := b.p.Pinned("")["a"]; p != "" {
		t.Fatalf("the dropped key was pinned again: %s", p)
	}
	a2 := newMember(t, tokenA, "a")
	if _, err := get(a2.client("b"), addr); err != nil {
		t.Fatalf("the replacement's first contact: %v", err)
	}
	if b.p.Pinned("")["a"] != a2.fp() {
		t.Fatal("the replacement was not pinned")
	}
}

// A refusal at the handshake comes before the caller proved its key (a
// member's certificate is public): one key's refusals of one name are
// reported once a minute, not once per attempt.
func TestARefusalIsReportedOnceAMinute(t *testing.T) {
	a, b := newMember(t, tokenA, "a"), newMember(t, tokenA, "b")
	addr := b.serve()
	if _, err := get(a.client("b"), addr); err != nil {
		t.Fatal(err)
	}
	imp := newMember(t, tokenA, "a")
	for range 5 {
		if _, err := get(imp.client("b"), addr); err == nil {
			t.Fatal("the impostor got through")
		}
	}
	if n := len(b.events(EventRefused)); n != 1 {
		t.Fatalf("%d refusal events for one key, want 1", n)
	}
}

// A key file that exists but can't be read now (its mode, an I/O error)
// is never replaced: that would lock the box out until an unpin. Nor is
// a box whose NICs can't be read yet another box. The handshakes wait.
func TestAnUnreadableKeyIsNeverReplaced(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a file of mode 0")
	}
	m := newMember(t, tokenA, "a")
	first := m.fp()
	path := filepath.Join(m.dir, "peer-key.json")
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	m2 := newMemberIn(t, tokenA, "a", m.dir)
	if _, err := m2.p.Fingerprint(); err == nil || !strings.Contains(err.Error(), "could not be read") {
		t.Fatalf("an unreadable key: %v, want the handshakes held", err)
	}
	if len(m2.events(EventNewKey)) != 0 {
		t.Fatal("an unreadable key was replaced")
	}
	_ = os.Chmod(path, 0o600)
	m3 := newMemberIn(t, tokenA, "a", m.dir)
	m3.mu.Lock()
	m3.box.NICs = nil // the interfaces don't read yet
	m3.mu.Unlock()
	if _, err := m3.p.Fingerprint(); err == nil {
		t.Fatal("a box with no NIC read yet used or made a key")
	}
	if len(m3.events(EventNewKey)) != 0 {
		t.Fatal("a box with no NIC read yet made a new key")
	}
	if m4 := newMemberIn(t, tokenA, "a", m.dir); m4.fp() != first {
		t.Fatal("the key changed")
	}
	// A key file of a newer release's version (a downgrade): kept.
	b, _ := os.ReadFile(path)
	newer := strings.Replace(string(b), `"v": 1`, `"v": 2`, 1)
	if newer == string(b) {
		t.Fatalf("no version in %s", b)
	}
	_ = os.WriteFile(path, []byte(newer), 0o600)
	m5 := newMemberIn(t, tokenA, "a", m.dir)
	if _, err := m5.p.Fingerprint(); err == nil || len(m5.events(EventNewKey)) != 0 {
		t.Fatalf("a newer key file: %v, new keys %v; want it held, not replaced", err, m5.events(EventNewKey))
	}
}

// The key is bound to its box: adding NICs (docker, a bridge, a USB NIC)
// or losing some keeps it; a box with the same machine-id but none of its
// NICs (a cloned disk on other hardware, a cloned VM) makes a new key and
// keeps the old file aside; so does another machine-id.
func TestTheKeyIsBoundToItsBox(t *testing.T) {
	dir := t.TempDir()
	set := func(m *tm, b Box) { m.mu.Lock(); m.box = b; m.mu.Unlock() }
	m := newMemberIn(t, tokenA, "a", dir)
	set(m, Box{MachineID: "mid", NICs: []string{"02:00:00:00:00:01", "02:00:00:00:00:02"}})
	first := m.fp()
	for _, nics := range [][]string{
		{"02:00:00:00:00:01", "02:00:00:00:00:02", "02:42:ac:11:00:01", "02:00:00:00:00:99"}, // docker, a USB NIC
		{"02:00:00:00:00:02"}, // one NIC gone
	} {
		m2 := newMemberIn(t, tokenA, "a", dir)
		set(m2, Box{MachineID: "mid", NICs: nics})
		if m2.fp() != first {
			t.Fatalf("NICs %v: a new key", nics)
		}
	}
	for i, b := range []Box{
		{MachineID: "mid", NICs: []string{"02:00:00:00:00:77"}},   // cloned onto other hardware
		{MachineID: "other", NICs: []string{"02:00:00:00:00:77"}}, // and its machine-id redrawn
	} {
		m2 := newMemberIn(t, tokenA, "a", dir)
		set(m2, b)
		if f := m2.fp(); f == first {
			t.Fatalf("box %d: the key written on another box was used", i)
		} else {
			first = f
		}
		if nk := m2.events(EventNewKey); len(nk) != 1 || !strings.Contains(nk[0].Detail, "another box") {
			t.Fatalf("box %d: new-key events %+v", i, nk)
		}
	}
	if kept, _ := filepath.Glob(filepath.Join(dir, "peer-key.json.other-box-*")); len(kept) == 0 {
		t.Fatal("the other box's key file was not kept aside")
	}
}

// A box that is not known yet (a link that doesn't resolve) never makes
// or replaces a key: its handshakes wait, and nothing is written.
func TestNoKeyWhileTheBoxIsUnknown(t *testing.T) {
	a, b := newMember(t, tokenA, "a"), newMember(t, tokenA, "b")
	a.mu.Lock()
	a.box = Box{}
	a.mu.Unlock()
	if _, err := get(a.client("b"), b.serve()); err == nil {
		t.Fatal("a called with no box")
	}
	if _, err := os.Stat(filepath.Join(a.dir, "peer-key.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a key file while the box is unknown: %v", err)
	}
	if _, err := a.p.Fingerprint(); err == nil {
		t.Fatal("a fingerprint while the box is unknown")
	}
}

// handshake runs one TLS handshake over loopback TCP and returns both
// ends' errors. A TLS 1.3 client finishes before the server has checked
// its certificate, so it reads once to see the server's verdict.
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

// A client without a certificate is refused.
func TestServerRefusesAClientWithoutACertificate(t *testing.T) {
	bare := &tls.Config{MinVersion: tls.VersionTLS13, InsecureSkipVerify: true}
	_, sErr := handshake(t, bare, newMember(t, tokenA, "b").p.Server())
	if sErr == nil {
		t.Fatal("the server took a client with no certificate")
	}
}

// TLS 1.3 only, on both ends.
func TestOnlyTLS13(t *testing.T) {
	a, b := newMember(t, tokenA, "a"), newMember(t, tokenA, "b")
	old := a.p.client("b", "")
	old.MinVersion, old.MaxVersion = tls.VersionTLS12, tls.VersionTLS12
	if _, sErr := handshake(t, old, b.p.Server()); sErr == nil {
		t.Fatal("the server took TLS 1.2")
	}
	oldSrv := b.p.serverFor("") // without the per-connection configuration, which is 1.3 again
	oldSrv.MinVersion, oldSrv.MaxVersion = tls.VersionTLS12, tls.VersionTLS12
	if cErr, _ := handshake(t, a.p.client("b", ""), oldSrv); cErr == nil {
		t.Fatal("the client took TLS 1.2")
	}
}

// The server issues no session tickets, so every connection proves the
// key in a full handshake.
func TestNoSessionResumption(t *testing.T) {
	a, b := newMember(t, tokenA, "a"), newMember(t, tokenA, "b")
	client := a.p.client("b", "")
	client.ClientSessionCache = tls.NewLRUClientSessionCache(4)
	srv := b.p.Server()
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
			sc := tls.Server(c, srv)
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

// The design never compares wall clocks across hosts: a handshake with
// each end's clock far from the other's and from the certificates' dates
// still passes.
func TestClocksYearsApartStillHandshake(t *testing.T) {
	a, b := newMember(t, tokenA, "a"), newMember(t, tokenA, "b")
	for _, ct := range []time.Time{time.Date(1999, 1, 1, 0, 0, 0, 0, time.UTC), time.Unix(0, 0).Add(-time.Hour)} {
		c := a.p.client("b", "")
		c.Time = func() time.Time { return ct }
		s := b.p.serverFor("")
		s.Time = func() time.Time { return time.Date(2075, 6, 1, 0, 0, 0, 0, time.UTC) }
		if cErr, sErr := handshake(t, c, s); cErr != nil || sErr != nil {
			t.Fatalf("client clock %v: client %v, server %v", ct, cErr, sErr)
		}
	}
}

func TestShortTokenRefused(t *testing.T) {
	o := func(tok string) Options {
		return Options{Token: tok, Cluster: cluster, Self: "a", KeyFile: "k", PinsFile: func(string) string { return "p" },
			Box: func() (Box, error) { return Box{}, nil }, Accept: func(string, string) bool { return false }}
	}
	_, err := New(o(strings.Repeat("x", MinToken-1)))
	var se *ShortTokenError
	if !errors.As(err, &se) || se.Len != MinToken-1 {
		t.Fatalf("New: %v, want a ShortTokenError", err)
	}
	if _, err := New(o(strings.Repeat("x", MinToken))); err != nil {
		t.Fatalf("a token of MinToken characters: %v", err)
	}
}
