// Package acklink mints and checks the links a page carries to
// acknowledge an alarm from the notification (#715).
//
// A link is a token signed with a key that never leaves the controller
// (on a redundant pair, internal/pairkey derives the same key on both
// members, so a link minted by one checks on the other after a takeover).
// The token binds one alarm, one occurrence of it, one principal, the
// channel kind and escalation level it was sent by, and an expiry of at
// most MaxTTL. It also carries a random link ID: not a secret, the name a
// journal, an audit entry and the replicated log use to say the link was
// spent, so the token itself is never written down.
//
// This package only says whether a token is genuine and what it binds.
// Whether the principal may acknowledge, whether the occurrence still
// stands and whether the link was already spent are the server's to
// decide.
package acklink

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

const (
	// MaxTTL is the longest a link lives: a Mint asking for more gets it.
	MaxTTL = 24 * time.Hour
	// MinKey is the shortest key New accepts.
	MinKey = 32
	// Path is where the server answers a link: GET shows the confirm page,
	// POST acknowledges. URL appends the token to it.
	Path = "/alarm-ack/"

	version = 1
	idLen   = 9  // the link ID's random bytes (72 bits)
	macLen  = 16 // HMAC-SHA256, truncated to 128 bits
	maxStr  = 256
	// skew is how far past now+MaxTTL a checked expiry may lie (a mint
	// on the other member of a pair, whose clock runs a little ahead).
	skew = 5 * time.Minute
	// domain separates this MAC from any other use of the key.
	domain = "nautilus ack link v1\x00"
)

var (
	// ErrMalformed: not a token this package minted (wrong length, wrong
	// version, bad encoding). Said the same as a bad signature to the
	// person holding it.
	ErrMalformed = errors.New("acklink: not an acknowledgement link")
	// ErrSignature: the token was not signed with this controller's key
	// (altered, or minted by another controller or pair).
	ErrSignature = errors.New("acklink: the link's signature does not check")
	// ErrExpired: the link's time is up.
	ErrExpired = errors.New("acklink: the link has expired")
)

// Claim is what a genuine token binds.
type Claim struct {
	// ID names the link (hex, not a secret): what the replicated log and
	// the journals record when it is spent.
	ID        string
	Alarm     string
	Occ       string // the occurrence the page was about
	Principal string // a user name or oidc:<name>
	Kind      string // the channel kind it was sent by (email, sms, push, …)
	Level     int    // the escalation level it was sent at (1 is the first)
	Expires   time.Time
}

// Keyring mints and checks tokens under one key.
type Keyring struct {
	key  []byte
	code []byte // the reply codes' key (code.go), derived from key
	now  func() time.Time
	rand io.Reader
}

// New is a Keyring over key, at least MinKey bytes. The key is copied.
func New(key []byte) (*Keyring, error) {
	if len(key) < MinKey {
		return nil, fmt.Errorf("acklink: key is %d bytes, need at least %d", len(key), MinKey)
	}
	k := &Keyring{key: append([]byte(nil), key...), now: time.Now, rand: rand.Reader}
	k.code = k.subkey(codeKeyInfo)
	return k, nil
}

// SetClock replaces the keyring's clock (tests, and a server whose clock
// is injected).
func (k *Keyring) SetClock(now func() time.Time) { k.now = now }

// Mint is a token for one alarm's occurrence, sent to principal by kind
// at level, valid for ttl (MaxTTL at most; zero or less is refused).
// Every argument but level must be non-empty and at most 256 bytes.
func (k *Keyring) Mint(alarmID, occ, principal, kind string, level int, ttl time.Duration) (string, error) {
	if ttl <= 0 {
		return "", errors.New("acklink: a link needs a lifetime")
	}
	ttl = min(ttl, MaxTTL)
	if level < 0 {
		return "", errors.New("acklink: negative level")
	}
	for _, s := range []struct{ name, v string }{{"alarm", alarmID}, {"occurrence", occ}, {"principal", principal}, {"kind", kind}} {
		if s.v == "" || len(s.v) > maxStr {
			return "", fmt.Errorf("acklink: %s must be 1 to %d bytes", s.name, maxStr)
		}
	}
	var id [idLen]byte
	if _, err := io.ReadFull(k.rand, id[:]); err != nil {
		return "", fmt.Errorf("acklink: link id: %w", err)
	}
	// Expiry in whole seconds, rounded down: a link never outlives ttl.
	exp := k.now().Add(ttl).Unix()
	p := make([]byte, 0, 1+idLen+2*binary.MaxVarintLen64+4*2+len(alarmID)+len(occ)+len(principal)+len(kind)+macLen)
	p = append(p, version)
	p = append(p, id[:]...)
	p = binary.AppendUvarint(p, uint64(exp))
	p = binary.AppendUvarint(p, uint64(level))
	for _, s := range []string{alarmID, occ, principal, kind} {
		p = binary.AppendUvarint(p, uint64(len(s)))
		p = append(p, s...)
	}
	p = append(p, k.mac(p)...)
	return base64.RawURLEncoding.EncodeToString(p), nil
}

func (k *Keyring) mac(payload []byte) []byte {
	m := hmac.New(sha256.New, k.key)
	m.Write([]byte(domain))
	m.Write(payload)
	return m.Sum(nil)[:macLen]
}

// Verify checks token's signature, then its expiry, and returns what it
// binds. The signature is checked before any field is read.
func (k *Keyring) Verify(token string) (Claim, error) {
	if len(token) > 2048 {
		return Claim{}, ErrMalformed
	}
	b, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(b) < 1+idLen+macLen {
		return Claim{}, ErrMalformed
	}
	payload, sig := b[:len(b)-macLen], b[len(b)-macLen:]
	if !hmac.Equal(sig, k.mac(payload)) {
		return Claim{}, ErrSignature
	}
	if payload[0] != version {
		return Claim{}, ErrMalformed
	}
	c, err := parse(payload[1:])
	if err != nil {
		return Claim{}, err
	}
	now := k.now()
	if !now.Before(c.Expires) {
		return c, ErrExpired
	}
	if c.Expires.Sub(now) > MaxTTL+skew {
		// Signed, but longer-lived than any Mint makes: refused rather
		// than honoured for days.
		return Claim{}, ErrMalformed
	}
	return c, nil
}

func parse(p []byte) (Claim, error) {
	var c Claim
	c.ID = hex.EncodeToString(p[:idLen])
	p = p[idLen:]
	exp, n := binary.Uvarint(p)
	if n <= 0 || exp > 1<<40 {
		return Claim{}, ErrMalformed
	}
	p = p[n:]
	lvl, n := binary.Uvarint(p)
	if n <= 0 || lvl > 1<<16 {
		return Claim{}, ErrMalformed
	}
	p = p[n:]
	var f [4]string
	for i := range f {
		l, n := binary.Uvarint(p)
		if n <= 0 || l == 0 || l > maxStr || uint64(len(p)-n) < l {
			return Claim{}, ErrMalformed
		}
		f[i] = string(p[n : n+int(l)])
		p = p[n+int(l):]
	}
	if len(p) != 0 {
		return Claim{}, ErrMalformed
	}
	c.Expires = time.Unix(int64(exp), 0)
	c.Level = int(lvl)
	c.Alarm, c.Occ, c.Principal, c.Kind = f[0], f[1], f[2], f[3]
	return c, nil
}

// URL is the link a page carries: ackURL (alarms.oncall.ack-url, the HMI
// as the recipient reaches it) followed by Path and the token. "" when
// ackURL is: a notification then carries no link.
func URL(ackURL, token string) string {
	if ackURL == "" || token == "" {
		return ""
	}
	return strings.TrimRight(ackURL, "/") + Path + token
}
