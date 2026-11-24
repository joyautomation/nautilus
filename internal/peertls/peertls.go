// Package peertls is a redundant pair's peer TLS (#710): every call on a
// peer port (votes, the handshake, the hello, the operator-intent log's
// replication, the state stream) runs TLS 1.3, both ends authenticated.
//
// Each member has a key of its own, the box key (key.go): made on the box
// at its first start, kept in its state directory, never copied and bound
// to the box, so a disk or VM cloned onto other hardware makes a new one.
// Its certificate is made on the box too, signed by a key every member
// derives from the peer token (NAUTILUS_PEER_TOKEN) and naming the member:
// "<cluster>/a", "<cluster>/b", or "witness@<incarnation>". One witness
// serves many clusters, each with an incarnation of its own (drawn at the
// pair's first vote, again when the witness is rebuilt): a controller's
// call names its cluster in the handshake (ALPN, PairProto) and the
// witness answers with a certificate naming that pair's incarnation
// ("witness@0" before the pair's first vote). A controller pins each
// incarnation's name on its own, counts a vote reply only when it carries
// the incarnation its certificate names (quorum/node), and drops the
// other incarnations' pins when the pair adopts one (Promote).
//
// A handshake is accepted only if the other end's certificate is signed
// by the token's key (it holds the token, at least at first contact), it
// names a member this end talks to (on a call: the very member dialed),
// and, once that name is pinned, its key is the pinned one (pins.go).
// The first full handshake with a name pins its key (trust on first use,
// under the token), durably, before any request crosses the connection.
// After that the token alone is not enough: a party must also hold that
// box's private key. Nothing re-pins a controller by itself: an operator
// drops a pin with `naut redundancy unpin <name>` on the surviving
// controller (#710 2b), after a box is replaced or its state directory
// lost, and the dropped key stays refused (in the pins file) until a new
// key is pinned for the name or a second unpin lifts it. "witness@0" is
// never pinned: nothing it answers counts.
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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/joyautomation/nautilus/internal/pairkey"
)

// MinToken is the shortest peer token peer TLS takes, in bytes. `naut
// redundancy new-pair` draws 64 hex digits.
const MinToken = 32

// Witness is the witness's member name (Options.Self, and a call's
// want): its certificates name no cluster, and an incarnation each
// (WitnessName).
const Witness = "witness"

// PairProto prefixes the cluster a controller's call to the witness names
// in its handshake (ALPN), so the witness answers with that pair's
// incarnation. Never negotiated: the witness's listener offers no
// protocol (net/http drops a connection that negotiated one it doesn't
// serve), it only reads the client's.
const PairProto = "nautilus-pair/"

// WitnessName is the witness's certificate name for a pair whose
// incarnation is inc ("witness@0" before the pair's first vote).
func WitnessName(inc uint64) string { return Witness + "@" + strconv.FormatUint(inc, 10) }

// WitnessInc reads a WitnessName.
func WitnessInc(name string) (uint64, bool) { return witnessInc(name) }

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
	// Full: Name is a witness incarnation past MaxWitnessNames.
	Full bool
	// NewWitness: Name is a new witness incarnation under another key
	// than the counted one's (Pinned, when known): a new witness box, or
	// a token holder at its address, allowed only by an operator.
	NewWitness bool
}

func (e *IdentityError) Error() string {
	switch {
	case e.Missing:
		return "the other end sent no TLS certificate: it is not a member of this pair"
	case e.NewWitness:
		was := ""
		if e.Pinned != "" {
			was = " (the witness this pair counts has " + e.Pinned + ")"
		}
		return fmt.Sprintf("the witness answers as %s with a new TLS key, %s%s: a new witness box, its state directory lost, or something else at the witness's address holding the peer token. A rebuilt witness keeps its key and needs nothing. If the witness box really changed, compare this key with the one the witness prints at its start, then run `naut redundancy unpin witness` on BOTH controllers: each then takes this key, once", e.Name, e.Got, was)
	case e.Dropped:
		return fmt.Sprintf("%s's TLS key (%s) is the one whose pin was dropped here (naut redundancy unpin): it stays refused, so a box still holding it never comes back as %s; the replacement's new key is pinned at its first contact. If the unpin was a mistake and this is the same box, run `naut redundancy unpin %s` again on the surviving controller: a second unpin lifts the refusal", e.Name, e.Got, e.Name, e.Name)
	case e.Full:
		return fmt.Sprintf("the witness answers as %s, and %d other witness incarnations are pinned here while this controller has adopted none: a witness makes a new incarnation only when it is rebuilt, so something else holds the peer token and answers at the witness's address. Find it; an adoption, or a restart of this controller while it has adopted none, drops the incarnations it does not count", e.Name, MaxWitnessNames)
	case e.Pinned != "" && strings.HasPrefix(e.Name, Witness+"@"):
		return fmt.Sprintf("the witness answers as %s with a TLS key (%s) that is not the one pinned for it here (%s): another box answers as the witness, or the witness's state directory was moved to another box (never restore or copy it: start the witness on a fresh state directory, a new incarnation its pairs adopt together)", e.Name, e.Got, e.Pinned)
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

// SaysWhy reports whether the refusal's own text says what happened (a
// key that is not the pinned one, a dropped one, a witness incarnation
// past MaxWitnessNames), so a caller shows it as it is.
func (e *IdentityError) SaysWhy() bool { return e.PinMismatch() || e.Full || e.NewWitness }

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
	// Incarnation is, on the witness, the incarnation of cluster ("" when
	// the call named none): what its certificate names to that pair's
	// controllers. Read only: a handshake never starts a pair. Required on
	// the witness.
	Incarnation func(cluster string) uint64
	// Counted is, on a controller, the witness incarnation its pair counts
	// (0 when none yet): its name is not one of the MaxWitnessNames.
	Counted func() uint64
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
	priv  ed25519.PrivateKey // nil until the box is known
	pub   ed25519.PublicKey
	certs map[string]*tls.Certificate // by name

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
	case o.Self == Witness && o.Incarnation == nil:
		return nil, errors.New("peertls: the witness's peer TLS needs Incarnation")
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	seed, err := pairkey.Derive([]byte(o.Token), pairkey.PeerTLS)
	if err != nil {
		return nil, err
	}
	p := &Peer{o: o, tokenKey: ed25519.NewKeyFromSeed(seed), certs: map[string]*tls.Certificate{}}
	p.tokenPub = p.tokenKey.Public().(ed25519.PublicKey)
	p.tokenDER, err = x509.CreateCertificate(crand.Reader, tokenTemplate, tokenTemplate, p.tokenPub, p.tokenKey)
	if err != nil {
		return nil, fmt.Errorf("peer TLS certificate: %w", err)
	}
	p.pins = newPins(o.PinsFile, p.event, o.Now, o.Counted)
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
		GetCertificate: func(h *tls.ClientHelloInfo) (*tls.Certificate, error) {
			return p.certificate(p.serverName(h))
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

// serverName is the name this member's certificate gives the caller of
// h: a controller's own, or the witness's for the pair the caller named
// (PairProto), witness@0 for one that named none.
func (p *Peer) serverName(h *tls.ClientHelloInfo) string {
	if p.o.Self != Witness {
		return p.selfName()
	}
	var inc uint64
	if !mut("witness_cert_one_inc") {
		for _, proto := range h.SupportedProtos {
			if c, ok := strings.CutPrefix(proto, PairProto); ok && validCluster(c) {
				inc = p.o.Incarnation(c)
				break
			}
		}
	}
	return WitnessName(inc)
}

func (p *Peer) selfName() string {
	if p.o.Self == Witness {
		return WitnessName(0) // the witness never calls: no listener takes it
	}
	return p.o.Cluster + "/" + p.o.Self
}

// client is the configuration of a call to the member named want; on a
// call to the witness, incOK says which incarnations it takes.
func (p *Peer) client(want, addr string, incOK func(uint64) bool) *tls.Config {
	c := &tls.Config{
		MinVersion: tls.VersionTLS13,
		// No CA and no host name to check: VerifyConnection checks the
		// signature, the name and the pin, on every handshake.
		InsecureSkipVerify: true,
		GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			return p.certificate(p.selfName())
		},
		VerifyConnection: func(cs tls.ConnectionState) error { _, err := p.verifyClient(cs, want, addr, incOK); return err },
	}
	if want == Witness {
		c.NextProtos = []string{PairProto + p.o.Cluster} // which pair's incarnation the witness names
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
	return p.wrap(ctx, raw, want, addr, nil)
}

// WrapWitness is Wrap to the witness, taking only an incarnation incOK
// takes: a call that is not a vote goes only to the incarnation the pair
// counts (a vote reply's own incarnation is checked against the
// certificate's, quorum/node).
func (p *Peer) WrapWitness(ctx context.Context, raw net.Conn, addr string, incOK func(uint64) bool) (net.Conn, error) {
	return p.wrap(ctx, raw, Witness, addr, incOK)
}

func (p *Peer) wrap(ctx context.Context, raw net.Conn, want, addr string, incOK func(uint64) bool) (net.Conn, error) {
	tc := tls.Client(raw, p.client(want, addr, incOK))
	if err := tc.HandshakeContext(ctx); err != nil {
		raw.Close()
		return nil, err
	}
	if mut("peer_tls_client_any_server") {
		return tc, nil
	}
	cs := tc.ConnectionState()
	who, err := p.verifyClient(cs, want, addr, incOK)
	if err == nil && who.name != WitnessName(0) {
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
// "<cluster>/b" or "witness@<incarnation>".
func parseName(cn string) (member, bool) {
	if _, ok := witnessInc(cn); ok {
		return member{name: cn}, true
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
// member dialed (the witness: an incarnation of it incOK takes, any when
// nil), and its key the pinned one if the name is pinned. witness@0 is
// never pinned: nothing it answers counts.
func (p *Peer) verifyClient(cs tls.ConnectionState, want, addr string, incOK func(uint64) bool) (member, error) {
	m, err := p.signed(cs)
	if err != nil {
		return m, err
	}
	if want == Witness {
		inc, ok := witnessInc(m.name)
		switch {
		case (!ok || m.cluster != "") && !mut("pin_any_name"):
			return m, &IdentityError{Name: m.String(), Want: Witness}
		case ok && incOK != nil && !incOK(inc) && !mut("witness_call_any_inc"):
			return m, &IncarnationError{Got: inc}
		case inc == 0:
			return m, nil
		}
		return m, p.pins.match(p.pinCluster(m), m.name, leafKey(cs), addr)
	}
	wantM := member{cluster: p.o.Cluster, name: want}
	if m != wantM && !mut("pin_any_name") {
		return m, &IdentityError{Name: m.String(), Want: wantM.String()}
	}
	return m, p.pins.match(p.pinCluster(m), m.name, leafKey(cs), addr)
}

// IncarnationError is a call to the witness that is not a vote refused
// because the witness answered as an incarnation the pair does not count.
type IncarnationError struct{ Got uint64 }

func (e *IncarnationError) Error() string {
	return fmt.Sprintf("the witness answers as incarnation %d, which this pair does not count: only votes go to an incarnation the pair has not adopted (a rebuilt witness is adopted by both controllers together)", e.Got)
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

// key is this box's key: read or made at the first handshake once the
// box is known, then kept. keyMu is held.
func (p *Peer) keyLocked() (ed25519.PrivateKey, error) {
	if p.priv != nil {
		return p.priv, nil
	}
	priv, err := p.boxKey()
	if err != nil {
		return nil, err
	}
	p.priv, p.pub = priv, priv.Public().(ed25519.PublicKey)
	return priv, nil
}

// certificate is this member's certificate naming it name: its box key,
// signed by the token's key. Made once per name, then kept (a witness
// makes one per incarnation it names).
func (p *Peer) certificate(name string) (*tls.Certificate, error) {
	p.keyMu.Lock()
	defer p.keyMu.Unlock()
	if c := p.certs[name]; c != nil {
		return c, nil
	}
	priv, err := p.keyLocked()
	if err != nil {
		return nil, err
	}
	tmpl := *memberTemplate
	tmpl.Subject = pkix.Name{CommonName: name}
	parent, err := x509.ParseCertificate(p.tokenDER)
	if err != nil {
		return nil, err
	}
	der, err := x509.CreateCertificate(crand.Reader, &tmpl, parent, p.pub, p.tokenKey)
	if err != nil {
		return nil, fmt.Errorf("peer TLS certificate: %w", err)
	}
	if len(p.certs) >= 256 {
		clear(p.certs) // bounded: names are remade on demand
	}
	c := &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv}
	p.certs[name] = c
	return c, nil
}

// Fingerprint is this member's own key's fingerprint, waiting for the key
// to be read or made; "" while the box is not known yet (or its key could
// not be made: the error says why).
func (p *Peer) Fingerprint() (string, error) {
	p.keyMu.Lock()
	defer p.keyMu.Unlock()
	if _, err := p.keyLocked(); err != nil {
		return "", err
	}
	return Fingerprint(p.pub), nil
}

// FingerprintNow is Fingerprint for a status call: it doesn't wait behind
// a handshake that is reading the box (a link's name may be resolving).
func (p *Peer) FingerprintNow() (string, error) {
	if !p.keyMu.TryLock() {
		return "", errors.New("this box's peer key is being read")
	}
	made, pub := p.priv != nil, p.pub
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

// Refused is cluster's dropped keys that stay refused ("" for this
// controller's own), name to key fingerprint.
func (p *Peer) Refused(cluster string) map[string]string {
	if cluster == "" {
		cluster = p.o.Cluster
	}
	return p.pins.droppedList(cluster)
}

// Drop removes name's pin in cluster ("" for this controller's own), and
// refuses its key from then on: `naut redundancy unpin` (#710 2b).
// OnChange hears a drop.
func (p *Peer) Drop(cluster, name, why string) (DropOutcome, error) {
	if cluster == "" {
		cluster = p.o.Cluster
	}
	return p.DropKey(cluster, name, why, "")
}

// DropKey is Drop only while name holds the key whose fingerprint is
// expect (pinned, or refused already): one pinned or refused since stays,
// OtherKey ("" expects any). The witness's side of an unpin.
func (p *Peer) DropKey(cluster, name, why, expect string) (DropOutcome, error) {
	if cluster == "" {
		cluster = p.o.Cluster
	}
	out, err := p.pins.drop(cluster, name, why, true, expect)
	if err == nil && out == Dropped {
		p.changed(cluster, name)
	}
	return out, err
}

// Lift ends the refusal of name's dropped key in cluster ("" for this
// controller's own): the next key to contact as name is pinned. It
// reports whether a key was refused.
func (p *Peer) Lift(cluster, name, why string) (bool, error) {
	if cluster == "" {
		cluster = p.o.Cluster
	}
	return p.pins.lift(cluster, name, why, "")
}

// LiftKey is Lift only while the refused key is the one whose
// fingerprint is expect.
func (p *Peer) LiftKey(cluster, name, why, expect string) (bool, error) {
	if cluster == "" {
		cluster = p.o.Cluster
	}
	return p.pins.lift(cluster, name, why, expect)
}

// UnpinPending is the fingerprint of the key an unpin of name on this
// controller dropped whose witness leg is not confirmed yet ("" none):
// SetUnpinPending.
func (p *Peer) UnpinPending(name string) string {
	p.pins.mu.Lock()
	defer p.pins.mu.Unlock()
	return p.pins.loadLocked(p.o.Cluster).pending[name]
}

// SetUnpinPending records, durably, that an unpin of name dropped the key
// whose fingerprint is key and has its witness leg to confirm ("" clears
// it): a retry finishes it, about that key, and never lifts the refusal
// (cmd/naut).
func (p *Peer) SetUnpinPending(name, key string) error {
	if mut("unpin_pending_volatile") {
		return nil
	}
	return p.pins.update(p.o.Cluster, func(m *clusterPins) bool {
		if m.pending[name] == key {
			return false
		}
		if key != "" {
			m.pending[name] = key
		} else {
			delete(m.pending, name)
		}
		return true
	})
}

// ErrNoWitnessWaiting: naut redundancy unpin witness with no new witness
// key refused here yet; an allowance is always of the one key waiting.
var ErrNoWitnessWaiting = errors.New("no new witness key has called this controller yet: start the new witness first, then run this once naut redundancy status shows its key waiting, after comparing that key with the one the witness prints at its start")

// AllowWitnessKey allows, durably and once, the new witness key waiting
// here (`naut redundancy unpin witness`), and returns its fingerprint:
// that very key pins at its next contact; another stays refused. The
// allowance ends when it pins, or at an adoption of an incarnation whose
// name is pinned here.
func (p *Peer) AllowWitnessKey(why string) (string, error) {
	waiting := p.WitnessWaiting()
	if waiting == "" {
		return "", ErrNoWitnessWaiting
	}
	err := p.pins.update(p.o.Cluster, func(m *clusterPins) bool {
		if m.allowWitness == waiting {
			return false
		}
		m.allowWitness = waiting
		return true
	})
	if err != nil {
		return "", err
	}
	p.event(Event{Kind: EventWitnessAllowed, Name: Witness, Fingerprint: waiting, Detail: why})
	p.changed(p.o.Cluster, Witness)
	return waiting, nil
}

// WitnessWaiting is the fingerprint of the last new witness key refused
// for want of an operator's allowance ("" none since it was pinned or this
// process started).
func (p *Peer) WitnessWaiting() string {
	p.pins.mu.Lock()
	defer p.pins.mu.Unlock()
	if k := p.pins.waiting[p.o.Cluster]; k != nil {
		return Fingerprint(k)
	}
	return ""
}

// WitnessAllowed is the fingerprint of the new witness key allowed here
// ("" none).
func (p *Peer) WitnessAllowed() string {
	p.pins.mu.Lock()
	defer p.pins.mu.Unlock()
	return p.pins.loadLocked(p.o.Cluster).allowWitness
}

// PinEpoch is name's pin epoch in cluster: the count of the cluster's
// drops when its key was pinned (kept after its pin is dropped), and
// whether name was ever pinned there.
func (p *Peer) PinEpoch(cluster, name string) (uint64, bool) {
	return p.pins.sinceOf(cluster, name)
}

// Promote keeps the witness incarnation inc the pair now counts: every
// other incarnation's pin is dropped (not refused: nothing an
// incarnation the pair does not count says counts, and a rebuilt one is
// adopted together). A controller runs it at each adoption and at its
// start. OnChange hears it.
func (p *Peer) Promote(inc uint64) error {
	// The counted incarnation's key, kept for the next one to match, and
	// the allowance ends: both once its name is pinned here. One adopted
	// on the peer's proposal, never heard here, keeps the old key, and an
	// allowance (of that one key) stands until its name pins.
	keep := WitnessName(inc)
	if err := p.pins.update(p.o.Cluster, func(m *clusterPins) bool {
		k := m.keys[keep]
		if k == nil && !mut("allowance_cleared_unheard") {
			return false
		}
		changed := false
		if k != nil && !equalKey(k, m.witnessKey) {
			m.witnessKey, changed = k, true
		}
		if m.allowWitness != "" && !mut("allowance_kept_at_adoption") {
			m.allowWitness, changed = "", true
		}
		return changed
	}); err != nil {
		return err
	}
	if mut("adopt_keeps_witness_pins") {
		return nil
	}
	for _, n := range p.pins.witnessPins(p.o.Cluster) {
		if n == keep {
			continue
		}
		out, err := p.pins.drop(p.o.Cluster, n, fmt.Sprintf("the pair counts witness incarnation %d: this one's pin is dropped", inc), false, "")
		if err != nil {
			return err
		}
		if out == Dropped {
			p.changed(p.o.Cluster, n)
		}
	}
	return nil
}

// PromoteAtStart is Promote at a controller's start: one that has
// adopted nothing and knows no witness key (a wiped state directory) drops
// every witness incarnation's pin, so names pinned before never hold
// places under the cap; one that knows the key keeps them (they are
// gated by it).
func (p *Peer) PromoteAtStart(inc uint64) error {
	if inc != 0 {
		return p.Promote(inc)
	}
	if p.pins.load(p.o.Cluster).witnessKey != nil {
		return nil
	}
	for _, n := range p.pins.witnessPins(p.o.Cluster) {
		if _, err := p.pins.drop(p.o.Cluster, n, "this controller adopted no witness incarnation and knows no witness key: its witness pins are dropped at its start", false, ""); err != nil {
			return err
		}
	}
	return nil
}

func (p *Peer) changed(cluster, name string) {
	p.chMu.Lock()
	fs := append([]func(string, string){}, p.onChange...)
	p.chMu.Unlock()
	for _, f := range fs {
		f(cluster, name)
	}
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
