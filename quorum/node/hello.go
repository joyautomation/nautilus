package node

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"

	"github.com/joyautomation/nautilus/quorum"
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
	// The answerer's part of the peer info (peerinfo.go), bound by
	// InfoMAC. A build before #710 PR 3b-2b sends none: its caps read as
	// none.
	Config  string   `json:"config,omitempty"`
	Nonce   string   `json:"nonce,omitempty"`
	Caps    []string `json:"caps,omitempty"`
	InfoMAC string   `json:"infoMac,omitempty"`
}

// tokenMAC is the client-token proof the answerer sends the asker: an
// HMAC-SHA256 keyed with the peer token over the direction ("answer"),
// who asks, who answers, a fresh challenge and the answerer's client
// token. The client token itself never leaves; the challenge keeps an old
// answer from being replayed, and the names and direction keep an asker's
// own question, reflected back at it, from passing as its peer's answer
// (#1021 review).
func tokenMAC(peerToken, challenge string, asker, answerer quorum.NodeID, clientToken string) string {
	m := hmac.New(sha256.New, []byte(peerToken))
	parts := []string{"nautilus client token answer", string(asker), string(answerer), challenge, clientToken}
	if mut("hello_unbound") {
		parts = []string{challenge, clientToken}
	}
	for _, part := range parts {
		m.Write([]byte(part))
		m.Write([]byte{0})
	}
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
