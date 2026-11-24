// Package peertls is a redundant pair's peer TLS (#710): every call on a
// peer port (votes, the handshake, the hello, the operator-intent log's
// replication, the state stream) runs TLS 1.3, both ends authenticated.
//
// Each member has a key of its own, the box key (key.go): made on the box
// at its first start, kept in its state directory, never copied and bound
// to the box, so a disk or VM cloned onto other hardware makes a new one.
// Its certificate is made on the box too, signed by a key every member
// derives from the peer token (NAUTILUS_PEER_TOKEN) and naming the member:
// "<cluster>/a", "<cluster>/b", or "witness" (one witness serves many
// clusters).
//
// A handshake is accepted only if the other end's certificate is signed
// by the token's key (it holds the token, at least at first contact), it
// names a member this end talks to (on a call: the very member dialed),
// and, once that name is pinned, its key is the pinned one (pins.go).
// The first full handshake with a name pins its key (trust on first use,
// under the token), durably, before any request crosses the connection.
// After that the token alone is not enough: a party must also hold that
// box's private key. Nothing re-pins by itself; an operator drops a pin
// with `naut redundancy unpin <name>` on the surviving controller (#710
// 2b), after a box is replaced or its state directory lost.
//
// No CA chain, host name or validity date is checked (the signature, the
// name and the pin are): peers are reached by name, by IP or through a
// floating address, and the design never compares wall clocks across
// hosts. The token's public key is visible to anyone who reaches a peer
// port, so a short token could be guessed offline: New refuses one under
// MinToken.
package peertls

import (
	"bytes"
	"context"
	"crypto/ed25519"
	crand "crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"net"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/joyautomation/nautilus/internal/pairkey"
)

// MinToken is the shortest peer token peer TLS takes, in bytes. `naut
// redundancy new-pair` draws 64 hex digits.
const MinToken = 32

// Witness is the witness's member name: its certificate names no cluster.
const Witness = "witness"

// ShortTokenError is New's refusal of a token under MinToken.
type ShortTokenError struct{ Len int }

func (e *ShortTokenError) Error() string {
	return fmt.Sprintf("the peer token is %d characters; peer TLS needs at least %d: its key is derived from the token, and anyone who reaches a peer port could test guesses at a short one offline", e.Len, MinToken)
}

// IdentityError is a handshake refused because of the other end's
// certificate. Its PeerIdentity method lets a package that can't import
// this one tell it from other errors (quorum/node).
type IdentityError struct {
	// Missing: the other end sent no certificate at all.
	Missing bool
	// Name is the member the certificate names ("" when it names none).
	Name string
	// Want is the member this end dialed ("" on a listener).
	Want string
	// Pinned is set when the certificate is the pair's but its key is not
	// the one pinned for Name: Pinned and Got are the two fingerprints.
	Pinned, Got string
	// Dropped: Got is the key whose pin for Name was dropped.
	Dropped bool
}

func (e *IdentityError) Error() string {
	switch {
	case e.Missing:
		return "the other end sent no TLS certificate: it is not a member of this pair"
	case e.Dropped:
		return fmt.Sprintf("%s's TLS key (%s) is the one whose pin was dropped here (naut redundancy unpin): it is not pinned again, so a box still holding it is refused; the replacement's new key is pinned at its first contact (a restart of this member forgets the drop, if the unpin was a mistake)", e.Name, e.Got)
	case e.Pinned != "":
		return fmt.Sprintf("%s's TLS key (%s) is not the one pinned for it here (%s): another box answers as %s, or %s's box was replaced or lost its state directory. If the box really changed, run `naut redundancy unpin %s` on the surviving controller; otherwise find what answers as %s", e.Name, e.Got, e.Pinned, e.Name, e.Name, e.Name, e.Name)
	case e.Name == "":
		return "the other end's TLS certificate is not this pair's: it holds another NAUTILUS_PEER_TOKEN, runs a build from before per-box keys, or something else answers at this address"
	case e.Want != "":
		return fmt.Sprintf("the other end's TLS certificate names %s, not %s, the member dialed: the pair's addresses point at the wrong member", e.Name, e.Want)
	}
	return fmt.Sprintf("the other end's TLS certificate names %s, which does not call this member", e.Name)
}

// PeerIdentity marks an IdentityError.
func (e *IdentityError) PeerIdentity() bool { return true }

// PinMismatch reports whether a refusal is a key that is not the pinned
// one, the case `naut redundancy unpin` is for.
func (e *IdentityError) PinMismatch() bool { return e.Pinned != "" || e.Dropped }

// Options configure a member's peer TLS.
type Options struct {
	// Token is the peer token.
	Token string
	// Cluster is this controller's cluster ID; "" for the witness.
	Cluster string
	// Self is this member's name: "a", "b" or Witness.
	Self string
	// KeyFile is where the box key is kept (in the state directory).
	KeyFile string
	// Box reads this box (key.go). A Box that is not Known yet (a link
	// that doesn't resolve) holds every handshake until it is.
	Box func() (Box, error)
	// PinsFile is the pins file of a cluster.
	PinsFile func(cluster string) string
	// Accept reports whether this member's listener takes calls from the
	// member named name of cluster.
	Accept func(cluster, name string) bool
	// OnEvent hears every pin, refusal, drop, reset and new key.
	OnEvent func(Event)
	Now     func() time.Time
}

// Peer is a member's peer TLS: its box key, its pins, and the
// configurations its listener serves and its calls dial with.
type Peer struct {
	o        Options
	tokenPub ed25519.PublicKey
	tokenKey ed25519.PrivateKey
	tokenDER []byte // the token key's own certificate: the parent of every member's

	keyMu sync.Mutex
	cert  *tls.Certificate // nil until the box is known
	pub   ed25519.PublicKey

	pins *pins

	chMu     sync.Mutex
	onChange []func(cluster, name string)
}

// New builds a member's peer TLS. The box key is read (or made) at the
// first handshake, once the box is known; the pins of this controller's
// cluster are read now, so an unreadable file is reported at the start.
func New(o Options) (*Peer, error) {
	if len(o.Token) < MinToken && !mut("peer_tls_short_token") {
		return nil, &ShortTokenError{Len: len(o.Token)}
	}
	switch {
	case o.Self == Witness && o.Cluster != "":
		return nil, errors.New("peertls: the witness's peer TLS names no cluster")
	case o.Self != Witness && (o.Self != "a" && o.Self != "b" || !validCluster(o.Cluster)):
		return nil, fmt.Errorf("peertls: member %q of cluster %q: want a or b of a cluster ID", o.Self, o.Cluster)
	case o.KeyFile == "" || o.PinsFile == nil || o.Box == nil || o.Accept == nil:
		return nil, errors.New("peertls: KeyFile, PinsFile, Box and Accept are required")
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	seed, err := pairkey.Derive([]byte(o.Token), pairkey.PeerTLS)
	if err != nil {
		return nil, err
	}
	p := &Peer{o: o, tokenKey: ed25519.NewKeyFromSeed(seed)}
	p.tokenPub = p.tokenKey.Public().(ed25519.PublicKey)
	p.tokenDER, err = x509.CreateCertificate(crand.Reader, tokenTemplate, tokenTemplate, p.tokenPub, p.tokenKey)
	if err != nil {
		return nil, fmt.Errorf("peer TLS certificate: %w", err)
	}
	p.pins = newPins(o.PinsFile, p.event, o.Now)
	if o.Cluster != "" {
		p.pins.load(o.Cluster)
	}
	return p, nil
}

// Server is the configuration of this member's peer listener
// (tls.NewListener): TLS 1.3 only, a client certificate required and
// checked (verifyServer). A name not pinned yet is pinned by Bind, at the
// first request: after the client has proved it holds the key.
func (p *Peer) Server() *tls.Config {
	c := p.serverFor("")
	// Each connection's own configuration, so a refusal names the
	// caller's address.
	c.GetConfigForClient = func(h *tls.ClientHelloInfo) (*tls.Config, error) {
		addr := ""
		if h.Conn != nil {
			addr = h.Conn.RemoteAddr().String()
		}
		return p.serverFor(addr), nil
	}
	return c
}

func (p *Peer) serverFor(addr string) *tls.Config {
	c := &tls.Config{
		MinVersion: tls.VersionTLS13,
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			return p.certificate()
		},
		ClientAuth:             tls.RequireAnyClientCert, // no chain or dates: verifyServer checks
		VerifyConnection:       func(cs tls.ConnectionState) error { return p.verifyServer(cs, addr) },
		SessionTicketsDisabled: true, // every connection proves the key in full
	}
	if mut("peer_tls_server_any_client") {
		c.ClientAuth, c.VerifyConnection = tls.NoClientCert, nil
	}
	return c
}

// client is the configuration of a call to the member named want.
func (p *Peer) client(want, addr string) *tls.Config {
	c := &tls.Config{
		MinVersion: tls.VersionTLS13,
		// No CA and no host name to check: VerifyConnection checks the
		// signature, the name and the pin, on every handshake.
		InsecureSkipVerify: true,
		GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			return p.certificate()
		},
		VerifyConnection: func(cs tls.ConnectionState) error { _, err := p.verifyClient(cs, want, addr); return err },
	}
	if mut("peer_tls_client_any_server") {
		c.VerifyConnection = nil
	}
	return c
}

// Wrap runs a call's handshake over raw, a connection dialed to the
// member named want, and returns it once the other end is that member:
// signed, named want, its key the pinned one or, at first contact, pinned
// now, durably, before the connection carries a request. ctx bounds the
// handshake. addr is for the journal.
func (p *Peer) Wrap(ctx context.Context, raw net.Conn, want, addr string) (net.Conn, error) {
	tc := tls.Client(raw, p.client(want, addr))
	if err := tc.HandshakeContext(ctx); err != nil {
		raw.Close()
		return nil, err
	}
	if mut("peer_tls_client_any_server") {
		return tc, nil
	}
	cs := tc.ConnectionState()
	who, err := p.verifyClient(cs, want, addr)
	if err == nil {
		err = p.pins.check(p.pinCluster(who), who.name, leafKey(cs), addr, true)
	}
	if err != nil {
		tc.Close()
		return nil, err
	}
	return tc, nil
}

// member is a certificate's name.
type member struct{ cluster, name string }

func (m member) String() string {
	if m.cluster == "" {
		return m.name
	}
	return m.cluster + "/" + m.name
}

var clusterRE = regexp.MustCompile(`^[0-9a-f]{32}$`)

func validCluster(c string) bool { return clusterRE.MatchString(c) }

// parseName reads a certificate's name: exactly "<cluster>/a",
// "<cluster>/b" or "witness".
func parseName(cn string) (member, bool) {
	if cn == Witness {
		return member{name: Witness}, true
	}
	c, n, ok := strings.Cut(cn, "/")
	if !ok || !validCluster(c) || (n != "a" && n != "b") {
		return member{}, false
	}
	return member{cluster: c, name: n}, true
}

// signed reads the other end's certificate: it must be signed by the
// token's key and name a member.
func (p *Peer) signed(cs tls.ConnectionState) (member, error) {
	if len(cs.PeerCertificates) == 0 {
		return member{}, &IdentityError{Missing: true}
	}
	leaf := cs.PeerCertificates[0]
	if _, ok := leaf.PublicKey.(ed25519.PublicKey); !ok {
		return member{}, &IdentityError{}
	}
	if !mut("pin_unsigned_accepted") && (leaf.SignatureAlgorithm != x509.PureEd25519 ||
		!ed25519.Verify(p.tokenPub, leaf.RawTBSCertificate, leaf.Signature)) {
		return member{}, &IdentityError{}
	}
	m, ok := parseName(leaf.Subject.CommonName)
	if !ok {
		return member{}, &IdentityError{}
	}
	return m, nil
}

// verifyClient checks a called member's certificate: signed, naming the
// member dialed, and its key the pinned one if the name is pinned.
func (p *Peer) verifyClient(cs tls.ConnectionState, want, addr string) (member, error) {
	m, err := p.signed(cs)
	if err != nil {
		return m, err
	}
	wantM := member{cluster: p.o.Cluster, name: want}
	if want == Witness {
		wantM.cluster = ""
	}
	if m != wantM && !mut("pin_any_name") {
		return m, &IdentityError{Name: m.String(), Want: wantM.String()}
	}
	return m, p.pins.match(p.pinCluster(m), m.name, leafKey(cs), addr)
}

// pinCluster is the cluster whose pins hold m's: the witness's name is
// pinned among this controller's own.
func (p *Peer) pinCluster(m member) string {
	if m.cluster == "" {
		return p.o.Cluster
	}
	return m.cluster
}

// verifyServer checks a caller's certificate: signed, naming a member this
// listener takes calls from, and its key the pinned one if the name is
// pinned. A name not pinned yet passes here and is pinned by Bind.
func (p *Peer) verifyServer(cs tls.ConnectionState, addr string) error {
	m, err := p.signed(cs)
	if err != nil {
		return err
	}
	if !p.o.Accept(m.cluster, m.name) && !mut("pin_any_name") {
		return &IdentityError{Name: m.String()}
	}
	return p.pins.match(p.pinCluster(m), m.name, leafKey(cs), addr)
}

// leafKey is the other end's public key (signed checked its type).
func leafKey(cs tls.ConnectionState) ed25519.PublicKey {
	k, _ := cs.PeerCertificates[0].PublicKey.(ed25519.PublicKey)
	return k
}

// certificate is this member's certificate: its box key, signed by the
// token's key, naming it. Made at the first handshake once the box is
// known, then kept.
func (p *Peer) certificate() (*tls.Certificate, error) {
	p.keyMu.Lock()
	defer p.keyMu.Unlock()
	if p.cert != nil {
		return p.cert, nil
	}
	priv, err := p.boxKey()
	if err != nil {
		return nil, err
	}
	pub := priv.Public().(ed25519.PublicKey)
	name := p.o.Self
	if p.o.Cluster != "" {
		name = p.o.Cluster + "/" + p.o.Self
	}
	tmpl := *memberTemplate
	tmpl.Subject = pkix.Name{CommonName: name}
	parent, err := x509.ParseCertificate(p.tokenDER)
	if err != nil {
		return nil, err
	}
	der, err := x509.CreateCertificate(crand.Reader, &tmpl, parent, pub, p.tokenKey)
	if err != nil {
		return nil, fmt.Errorf("peer TLS certificate: %w", err)
	}
	p.cert, p.pub = &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv}, pub
	return p.cert, nil
}

// Fingerprint is this member's own key's fingerprint, waiting for the key
// to be read or made; "" while the box is not known yet (or its key could
// not be made: the error says why).
func (p *Peer) Fingerprint() (string, error) {
	if _, err := p.certificate(); err != nil {
		return "", err
	}
	p.keyMu.Lock()
	defer p.keyMu.Unlock()
	return Fingerprint(p.pub), nil
}

// FingerprintNow is Fingerprint for a status call: it doesn't wait behind
// a handshake that is reading the box (a link's name may be resolving).
func (p *Peer) FingerprintNow() (string, error) {
	if !p.keyMu.TryLock() {
		return "", errors.New("this box's peer key is being read")
	}
	made, pub := p.cert != nil, p.pub
	p.keyMu.Unlock()
	if made {
		return Fingerprint(pub), nil
	}
	return p.Fingerprint()
}

// Pinned is the pins of cluster ("" for this controller's own), name to
// key fingerprint.
func (p *Peer) Pinned(cluster string) map[string]string {
	if cluster == "" {
		cluster = p.o.Cluster
	}
	return p.pins.list(cluster)
}

// Drop removes name's pin in cluster ("" for this controller's own): its
// next contact pins again. The store-level drop `naut redundancy unpin`
// runs (#710 2b); OnChange hears it.
func (p *Peer) Drop(cluster, name, why string) error {
	if cluster == "" {
		cluster = p.o.Cluster
	}
	if err := p.pins.drop(cluster, name, why); err != nil {
		return err
	}
	p.chMu.Lock()
	fs := append([]func(string, string){}, p.onChange...)
	p.chMu.Unlock()
	for _, f := range fs {
		f(cluster, name)
	}
	return nil
}

// OnChange registers f to hear every dropped pin: a caller closes its idle
// connections to that member, so its next call meets the new key.
func (p *Peer) OnChange(f func(cluster, name string)) {
	p.chMu.Lock()
	p.onChange = append(p.onChange, f)
	p.chMu.Unlock()
}

// Fingerprint is a key's fingerprint as an operator compares it: SHA256:
// and the base64 of the key's SHA-256, as ssh shows one.
func Fingerprint(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
}

func (p *Peer) event(e Event) {
	if e.Time.IsZero() {
		e.Time = p.o.Now()
	}
	if p.o.OnEvent != nil {
		p.o.OnEvent(e)
	}
}

// equalKey compares two keys.
func equalKey(a, b ed25519.PublicKey) bool { return bytes.Equal(a, b) }

// The token key's own certificate is the parent every member's names; its
// dates and those of a member's are never checked (the signature, the
// name and the pin are): they span any clock a box may have.
var (
	tokenTemplate = &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "nautilus peer token"},
		NotBefore:             time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC),
		NotAfter:              time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	memberTemplate = &x509.Certificate{
		SerialNumber: big.NewInt(2),
		NotBefore:    time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC),
		NotAfter:     time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
)
