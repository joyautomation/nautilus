package node

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
)

// ClientToken is what the client-token check (design §6.1) found about
// the pair's NAUTILUS_TOKEN.
type ClientToken int

const (
	// TokenUnknown: no answer from the peer yet.
	TokenUnknown ClientToken = iota
	// TokenSame: both controllers hold the same client token (or both
	// none).
	TokenSame
	// TokenDiffers: the controllers hold different client tokens. To a
	// client the pair is one controller, so a token one accepts the other
	// refuses after a takeover.
	TokenDiffers
)

func (t ClientToken) String() string {
	switch t {
	case TokenSame:
		return "same"
	case TokenDiffers:
		return "differs"
	}
	return "unknown"
}

// helloAnswer is GET {pair}/hello's JSON answer: the answering node and
// its MAC of its client token for the asker's challenge.
type helloAnswer struct {
	Node string `json:"node"`
	MAC  string `json:"mac"`
}

// tokenMAC is the client-token proof one controller sends the other: an
// HMAC-SHA256 keyed with the peer token over a fresh challenge and the
// client token. The client token itself never leaves; the challenge keeps
// an old answer from being replayed as a new one.
func tokenMAC(peerToken, challenge, clientToken string) string {
	m := hmac.New(sha256.New, []byte(peerToken))
	m.Write([]byte("nautilus client token\x00"))
	m.Write([]byte(challenge))
	m.Write([]byte{0})
	m.Write([]byte(clientToken))
	return hex.EncodeToString(m.Sum(nil))
}

// newChallenge draws a random challenge, hex.
func newChallenge() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// sameMAC compares two hex MACs in constant time.
func sameMAC(a, b string) bool { return hmac.Equal([]byte(a), []byte(b)) }
