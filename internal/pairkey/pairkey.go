// Package pairkey derives the keys a redundant pair's members share from
// the peer token (NAUTILUS_PEER_TOKEN, the secret both members already
// hold), one per purpose (#715).
//
// Each purpose gets its own key, by its label, so no two uses ever share
// one: the peer token itself authenticates the peer channel and keys the
// contacts digest, and a key derived here for ack links says nothing
// about either. The token must be random and long (naut redundancy
// new-pair makes one): a key derived here may sign things that leave the
// box (an ack link in an email), and anyone holding one can test guesses
// at a short token offline. A key is never stored: both members derive the same one
// from the same token, so it survives a takeover with nothing to copy.
package pairkey

import (
	"crypto/hkdf"
	"crypto/sha256"
	"errors"
)

// Size is the length of a derived key in bytes.
const Size = 32

// info prefixes every label: a key from another version of this
// derivation, or another program's HKDF over the same token, differs.
const info = "nautilus pairkey v1 "

// Label names a purpose. Add one here per purpose, never reuse one.
const (
	// AckLink keys the alarm acknowledgement links (alarm/acklink).
	AckLink = "alarm-ack-link"
	// PeerTLS seeds the pair's peer TLS key (internal/peertls, #710).
	PeerTLS = "peer-tls"
)

// ErrNoToken is Derive's answer for an empty peer token.
var ErrNoToken = errors.New("pairkey: no peer token")

// Derive is the Size-byte key for label: HKDF-SHA256 over peerToken, no
// salt, info "nautilus pairkey v1 "+label. Equal inputs give equal keys on
// every member; an empty token or label is refused (a key derived from
// nothing would be a key anyone can derive).
func Derive(peerToken []byte, label string) ([]byte, error) {
	if len(peerToken) == 0 {
		return nil, ErrNoToken
	}
	if label == "" {
		return nil, errors.New("pairkey: no label")
	}
	return hkdf.Key(sha256.New, peerToken, nil, info+label, Size)
}
