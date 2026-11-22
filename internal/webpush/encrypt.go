// Package webpush sends Web Push messages (RFC 8030): the aes128gcm payload
// encryption of RFC 8291, the VAPID identification of RFC 8292, and a
// sender that refuses to be pointed at the controller's own network.
//
// It imports only the standard library (docs/design/context-map.md §2).
package webpush

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
)

// RecordSize is the one record size every message uses; a payload that
// does not fit in one record is refused (a push service takes 4096 bytes).
const RecordSize = 4096

// MaxPayload is the longest plaintext Encrypt takes. A push service
// refuses a body over 4096 octets (RFC 8030 §7.2), and the body is the
// 86-octet header, the plaintext, the delimiter octet and the 16-octet tag.
const MaxPayload = 4096 - 86 - 1 - 16

// Encrypt encrypts payload for the user agent whose subscription carries
// uaPublic (65-byte uncompressed P-256 point) and authSecret (16 bytes),
// RFC 8291 §3.4 with a single record, and returns the whole body: the
// 86-octet header and the ciphertext, under a fresh salt and sender key.
func Encrypt(payload, uaPublic, authSecret []byte) ([]byte, error) {
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	return encrypt(payload, uaPublic, authSecret, priv, salt)
}

// encrypt is Encrypt with the sender key and salt given, so the RFC's
// Appendix A vector reproduces.
func encrypt(payload, uaPublic, authSecret []byte, as *ecdh.PrivateKey, salt []byte) ([]byte, error) {
	if len(payload) > MaxPayload {
		return nil, fmt.Errorf("webpush: payload is %d bytes; at most %d fit one message", len(payload), MaxPayload)
	}
	if len(authSecret) != 16 {
		return nil, errors.New("webpush: the subscription's auth secret is not 16 bytes")
	}
	ua, err := ecdh.P256().NewPublicKey(uaPublic) // validates the point is on the curve
	if err != nil {
		return nil, errors.New("webpush: the subscription's p256dh is not a P-256 point")
	}
	secret, err := as.ECDH(ua)
	if err != nil {
		return nil, err
	}
	asPub := as.PublicKey().Bytes()

	keyInfo := append(append([]byte("WebPush: info\x00"), uaPublic...), asPub...)
	ikm, err := hkdf.Key(sha256.New, secret, authSecret, string(keyInfo), 32)
	if err != nil {
		return nil, err
	}
	cek, err := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, err
	}
	nonce, err := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, err
	}
	blk, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(blk)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, 86+len(payload)+1+16)
	out = append(out, salt...)
	out = binary.BigEndian.AppendUint32(out, RecordSize)
	out = append(out, byte(len(asPub)))
	out = append(out, asPub...)
	plain := append(append([]byte{}, payload...), 0x02) // last-record delimiter
	return gcm.Seal(out, nonce, plain, nil), nil
}
