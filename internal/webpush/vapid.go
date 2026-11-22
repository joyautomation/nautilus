package webpush

import (
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/url"
	"strings"
	"time"
)

var b64 = base64.RawURLEncoding

// Key is the application server's VAPID key pair (RFC 8292).
type Key struct {
	priv *ecdsa.PrivateKey
}

// NewKey generates a key pair.
func NewKey() (*Key, error) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	return &Key{priv: k}, nil
}

// ParseKey reads a private key as Private writes it: the 32-octet scalar,
// base64url without padding.
func ParseKey(s string) (*Key, error) {
	raw, err := b64.DecodeString(strings.TrimSpace(s))
	if err != nil || len(raw) != 32 {
		return nil, errors.New("webpush: a VAPID private key is 32 octets of unpadded base64url")
	}
	ek, err := ecdh.P256().NewPrivateKey(raw)
	if err != nil {
		return nil, errors.New("webpush: not a P-256 private key")
	}
	pub := ek.PublicKey().Bytes()
	k := &ecdsa.PrivateKey{D: new(big.Int).SetBytes(raw)}
	k.Curve = elliptic.P256()
	k.X = new(big.Int).SetBytes(pub[1:33])
	k.Y = new(big.Int).SetBytes(pub[33:65])
	return &Key{priv: k}, nil
}

// Private is the key's secret form, for the secrets file.
func (k *Key) Private() string {
	d := k.priv.D.FillBytes(make([]byte, 32))
	return b64.EncodeToString(d)
}

// Public is the key the browser subscribes with (applicationServerKey):
// the 65-octet uncompressed point, unpadded base64url.
func (k *Key) Public() string { return b64.EncodeToString(k.publicBytes()) }

func (k *Key) publicBytes() []byte {
	out := make([]byte, 65)
	out[0] = 4
	k.priv.X.FillBytes(out[1:33])
	k.priv.Y.FillBytes(out[33:65])
	return out
}

// Fingerprint names the key without revealing it: the first 8 octets of
// the SHA-256 of the public key, hex.
func (k *Key) Fingerprint() string {
	sum := sha256.Sum256(k.publicBytes())
	const hexd = "0123456789abcdef"
	out := make([]byte, 16)
	for i, b := range sum[:8] {
		out[2*i], out[2*i+1] = hexd[b>>4], hexd[b&15]
	}
	return string(out)
}

// Authorization is the RFC 8292 header value for a push to endpoint: a JWT
// naming the endpoint's origin, valid until exp, signed by the key, and
// the key. subject is a mailto: or https: URL the site owns.
func (k *Key) Authorization(endpoint, subject string, exp time.Time) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", errors.New("webpush: not a push endpoint URL")
	}
	jwt, err := k.jwt(strings.ToLower(u.Scheme+"://"+u.Host), subject, exp)
	if err != nil {
		return "", err
	}
	return "vapid t=" + jwt + ", k=" + k.Public(), nil
}

func (k *Key) jwt(aud, sub string, exp time.Time) (string, error) {
	hdr, _ := json.Marshal(map[string]string{"typ": "JWT", "alg": "ES256"})
	claims := map[string]any{"aud": aud, "exp": exp.Unix()}
	if sub != "" {
		claims["sub"] = sub
	}
	body, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signing := b64.EncodeToString(hdr) + "." + b64.EncodeToString(body)
	h := sha256.Sum256([]byte(signing))
	r, s, err := ecdsa.Sign(rand.Reader, k.priv, h[:])
	if err != nil {
		return "", err
	}
	sig := append(r.FillBytes(make([]byte, 32)), s.FillBytes(make([]byte, 32))...) // r||s, not DER
	return signing + "." + b64.EncodeToString(sig), nil
}
