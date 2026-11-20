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

// Credentials is what a controller serves its clients with (#708),
// compared with the peer's at every hello (report only: it never holds a
// campaign or closes the gate). Zero parts are "none".
type Credentials struct {
	// Users is auth/users.Digest of the users file (zero: no users, or
	// none file); UsersErr says why it could not be read.
	Users    [32]byte
	UsersErr string
	// TLS is the tag API's certificate leaf, server.CertSource.Pin (zero:
	// no server.tls).
	TLS [32]byte
}

// CredState is what the credential check found about one part.
type CredState int

const (
	// CredUnknown: no answer from the peer yet, or a build that sends
	// none.
	CredUnknown CredState = iota
	// CredSame: both controllers hold the same.
	CredSame
	// CredDiffers: they hold different ones.
	CredDiffers
	// CredUnreadable: one of them can't read its own (Detail says which).
	CredUnreadable
	// CredOff: neither has any (no users, no server.tls).
	CredOff
	// CredUnverified: the peer's answer did not prove itself under the
	// peer token (a forged, reflected or replayed answer, or the two
	// controllers hold different peer tokens): nothing can be said about
	// its credentials.
	CredUnverified
)

func (c CredState) String() string {
	switch c {
	case CredSame:
		return "same"
	case CredDiffers:
		return "differs"
	case CredUnreadable:
		return "unreadable"
	case CredOff:
		return "off"
	case CredUnverified:
		return "unverified"
	}
	return "unknown"
}

// The states an answerer reports for a part, bound in its MAC.
const (
	credOK         = "ok"
	credUnreadable = "unreadable"
)

// credMAC proves one part of the answerer's credentials to the asker, as
// tokenMAC proves the client token: keyed with the peer token, over its
// own label, the part, who asks, who answers, a fresh challenge, the
// answerer's state for the part and, when it could read it, the digest.
// The digest itself never leaves.
func credMAC(peerToken, challenge string, asker, answerer quorum.NodeID, part, state string, digest [32]byte) string {
	m := hmac.New(sha256.New, []byte(peerToken))
	parts := []string{"nautilus credential answer", part, string(asker), string(answerer), challenge, state}
	switch {
	case mut("creds_names_unbound"):
		parts = []string{"nautilus credential answer", part, challenge, state}
	case mut("creds_challenge_unbound"):
		parts = []string{"nautilus credential answer", part, string(asker), string(answerer), state}
	}
	if state == credOK && !mut("creds_unbound") {
		parts = append(parts, hex.EncodeToString(digest[:]))
	}
	for _, p := range parts {
		m.Write([]byte(p))
		m.Write([]byte{0})
	}
	return hex.EncodeToString(m.Sum(nil))
}

// judgeCred is what the asker makes of one part: the answerer's state and
// MAC against its own digest (or its own read error). verified says the
// answer as a whole proved itself under the peer token for this challenge
// and these names (its peer-info MAC): only then does a credential MAC
// that does not match mean the credentials differ; otherwise nothing is
// known (CredUnverified), and the files are not blamed.
func judgeCred(peerToken, challenge string, asker, answerer quorum.NodeID, part, peerState, peerMAC string, own [32]byte, ownErr string, verified bool) CredState {
	switch {
	case peerState == "":
		return CredUnknown // a build before the check
	case peerState != credOK && peerState != credUnreadable:
		return CredUnverified
	case peerState == credOK && sameMAC(peerMAC, credMAC(peerToken, challenge, asker, answerer, part, credOK, own)):
		if ownErr != "" {
			return CredUnreadable
		}
		if own == ([32]byte{}) {
			return CredOff
		}
		return CredSame
	case peerState == credUnreadable && sameMAC(peerMAC, credMAC(peerToken, challenge, asker, answerer, part, credUnreadable, [32]byte{})):
		return CredUnreadable
	case !verified:
		return CredUnverified
	case ownErr != "":
		return CredUnreadable
	}
	return CredDiffers
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
	// Log is the answerer's operator-intent log report (logReport), sent
	// only to an asker that asks for it (log=1), so a build before it
	// still verifies the answer. Bound by InfoMAC.
	Log string `json:"log,omitempty"`
	// The answerer's credentials (Credentials), each part as its state
	// and its MAC (credMAC). A build before #708's check sends none.
	Users    string `json:"users,omitempty"`
	UsersMAC string `json:"usersMac,omitempty"`
	TLS      string `json:"tls,omitempty"`
	TLSMAC   string `json:"tlsMac,omitempty"`
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
