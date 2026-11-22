// Package peertls is a redundant pair's peer TLS (#710): every call on a
// peer port (votes, the handshake, the hello, the operator-intent log's
// replication) runs TLS 1.3, both ends authenticated by one key that the
// two controllers and the witness derive from the peer token
// (NAUTILUS_PEER_TOKEN) they already share. Nothing is distributed,
// written to disk or re-pinned when a box is replaced.
//
// Each end checks the other's public key, and only that: no CA, no host
// name (peers are reached by name, by IP or through a floating address)
// and no validity dates, since the design never compares wall clocks
// across hosts (a controller's clock can be years off). A party without
// the token can neither read peer traffic nor pass as a voter, on either
// end of a connection.
//
// The key is the token's: anyone who holds the token can still act as any
// member, as today. Per-box keys pinned at commissioning follow (#710 PR
// 2). The public key is visible to anyone who reaches a peer port, so a
// short token could be guessed offline: New refuses one under MinToken.
package peertls

import (
	"crypto/ed25519"
	crand "crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"time"

	"github.com/joyautomation/nautilus/internal/pairkey"
)

// MinToken is the shortest peer token peer TLS takes, in bytes. `naut
// redundancy new-pair` draws 64 hex digits.
const MinToken = 32

// ShortTokenError is New's refusal of a token under MinToken.
type ShortTokenError struct{ Len int }

func (e *ShortTokenError) Error() string {
	return fmt.Sprintf("the peer token is %d characters; peer TLS needs at least %d: its key is derived from the token, and anyone who reaches a peer port could test guesses at a short one offline", e.Len, MinToken)
}

// IdentityError is a handshake refused because the other end's key is not
// the pair's: it holds another peer token, or something else answers. Its
// PeerIdentity method lets a package that can't import this one tell it
// from other errors (quorum/node).
type IdentityError struct {
	// Missing: the other end sent no certificate at all.
	Missing bool
}

func (e *IdentityError) Error() string {
	if e.Missing {
		return "the other end sent no TLS certificate: it is not a member of this pair"
	}
	return "the other end's TLS key is not this pair's: it holds another NAUTILUS_PEER_TOKEN, or something else answers at this address"
}

// PeerIdentity marks an IdentityError.
func (e *IdentityError) PeerIdentity() bool { return true }

// Pair is a pair's peer TLS: the configuration a peer listener serves and
// the one its calls dial with. Both carry the same certificate.
type Pair struct {
	// Server is for a peer listener (tls.NewListener): TLS 1.3 only, a
	// client certificate required and checked against the pair's key.
	Server *tls.Config
	// Client is for peer calls (http.Transport.TLSClientConfig): it sends
	// the pair's certificate and accepts only a server holding the key.
	Client *tls.Config
}

// New derives the pair's key from the peer token and builds both
// configurations. Equal tokens give equal keys on every box.
func New(token string) (*Pair, error) {
	if len(token) < MinToken && !mut("peer_tls_short_token") {
		return nil, &ShortTokenError{Len: len(token)}
	}
	seed, err := pairkey.Derive([]byte(token), pairkey.PeerTLS)
	if err != nil {
		return nil, err
	}
	priv := ed25519.NewKeyFromSeed(seed)
	pub := priv.Public().(ed25519.PublicKey)
	der, err := x509.CreateCertificate(crand.Reader, template, template, pub, priv)
	if err != nil {
		return nil, fmt.Errorf("peer TLS certificate: %w", err)
	}
	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv}
	verify := func(cs tls.ConnectionState) error { return checkKey(cs, pub) }
	server := &tls.Config{
		MinVersion:             tls.VersionTLS13,
		Certificates:           []tls.Certificate{cert},
		ClientAuth:             tls.RequireAnyClientCert, // chain and dates unchecked: the key is
		VerifyConnection:       verify,
		SessionTicketsDisabled: true, // every connection proves the key in full
	}
	if mut("peer_tls_server_any_client") {
		server.ClientAuth, server.VerifyConnection = tls.NoClientCert, nil
	}
	client := &tls.Config{
		MinVersion: tls.VersionTLS13,
		// No CA and no host name to check: VerifyConnection checks the
		// key, and runs on every handshake, resumed ones included.
		InsecureSkipVerify: true,
		GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			return &cert, nil
		},
		VerifyConnection: verify,
	}
	if mut("peer_tls_client_any_server") {
		client.VerifyConnection = nil
	}
	return &Pair{Server: server, Client: client}, nil
}

// checkKey accepts a connection only if the other end's leaf holds pub.
func checkKey(cs tls.ConnectionState, pub ed25519.PublicKey) error {
	if len(cs.PeerCertificates) == 0 {
		return &IdentityError{Missing: true}
	}
	k, ok := cs.PeerCertificates[0].PublicKey.(ed25519.PublicKey)
	if !ok || !k.Equal(pub) {
		return &IdentityError{}
	}
	return nil
}

// template is every pair's certificate. Its dates are never checked (the
// key is); they span any clock a box may have.
var template = &x509.Certificate{
	SerialNumber: big.NewInt(1),
	Subject:      pkix.Name{CommonName: "nautilus peer"},
	NotBefore:    time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC),
	NotAfter:     time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC),
	KeyUsage:     x509.KeyUsageDigitalSignature,
	ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
}
