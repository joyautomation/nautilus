package webpush

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"math/big"
	"strings"
	"testing"
	"time"
)

func dec(t *testing.T, s string) []byte {
	t.Helper()
	b, err := b64.DecodeString(strings.Join(strings.Fields(s), ""))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The RFC 8291 Appendix A vector, byte for byte (values copied from the RFC).
func TestEncryptRFC8291AppendixA(t *testing.T) {
	plain := dec(t, "V2hlbiBJIGdyb3cgdXAsIEkgd2FudCB0byBiZSBhIHdhdGVybWVsb24")
	asPriv := dec(t, "yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw")
	uaPub := dec(t, "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4")
	salt := dec(t, "DGv6ra1nlYgDCS1FRnbzlw")
	auth := dec(t, "BTBZMqHH6r4Tts7J_aSIgg")
	as, err := ecdh.P256().NewPrivateKey(asPriv)
	if err != nil {
		t.Fatal(err)
	}
	got, err := encrypt(plain, uaPub, auth, as, salt)
	if err != nil {
		t.Fatal(err)
	}
	// The RFC prints the 86-octet header and the ciphertext separately.
	want := append(dec(t, "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A8"),
		dec(t, "8pfeW0KbunFT06SuDKoJH9Ql87S1QUrdirN6GcG7sFz1y1sqLgVi1VhjVkHsUoEsbI_0LpXMuGvnzQ")...)
	if !bytes.Equal(got, want) {
		t.Fatalf("body differs from RFC 8291 Appendix A:\n got %x\nwant %x", got, want)
	}
}

func TestEncryptRefusals(t *testing.T) {
	k, _ := ecdh.P256().GenerateKey(rand.Reader)
	pub := k.PublicKey().Bytes()
	auth := make([]byte, 16)
	if _, err := Encrypt(make([]byte, MaxPayload+1), pub, auth); err == nil {
		t.Error("an oversize payload was accepted")
	}
	if _, err := Encrypt([]byte("x"), pub, auth[:15]); err == nil {
		t.Error("a short auth secret was accepted")
	}
	bad := append([]byte{4}, make([]byte, 64)...) // not on the curve
	if _, err := Encrypt([]byte("x"), bad, auth); err == nil {
		t.Error("a point off the curve was accepted")
	}
	if _, err := Encrypt(make([]byte, MaxPayload), pub, auth); err != nil {
		t.Errorf("the largest payload was refused: %v", err)
	}
}

// verifyJWT checks the ES256 signature (raw r||s) against x, y.
func verifyJWT(t *testing.T, jwt string, x, y []byte) map[string]any {
	t.Helper()
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("jwt has %d parts", len(parts))
	}
	sig := dec(t, parts[2])
	if len(sig) != 64 {
		t.Fatalf("signature is %d octets, want 64 (r||s)", len(sig))
	}
	pub := &ecdsa.PublicKey{Curve: elliptic.P256(), X: new(big.Int).SetBytes(x), Y: new(big.Int).SetBytes(y)}
	h := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if !ecdsa.Verify(pub, h[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Fatal("signature does not verify")
	}
	var claims map[string]any
	if err := json.Unmarshal(dec(t, parts[1]), &claims); err != nil {
		t.Fatal(err)
	}
	return claims
}

// The RFC 8292 §2.4 example JWT verifies under our verifier, so the
// format we sign (raw r||s over the same signing input) is the RFC's.
func TestVAPIDRFC8292ExampleVerifies(t *testing.T) {
	jwt := "eyJ0eXAiOiJKV1QiLCJhbGciOiJFUzI1NiJ9.eyJhdWQiOiJodHRwczovL3" +
		"B1c2guZXhhbXBsZS5uZXQiLCJleHAiOjE0NTM1MjM3NjgsInN1YiI6Im1ha" +
		"Wx0bzpwdXNoQGV4YW1wbGUuY29tIn0.i3CYb7t4xfxCDquptFOepC9GAu_H" +
		"LGkMlMuCGSK2rpiUfnK9ojFwDXb1JrErtmysazNjjvW2L9OkSSHzvoD1oA"
	x := dec(t, "DUfHPKLVFQzVvnCPGyfucbECzPDa7rWbXriLcysAjEc")
	y := dec(t, "F6YK5h4SDYic-dRuU_RCPCfA5aq9ojSwk5Y2EmClBPs")
	c := verifyJWT(t, jwt, x, y)
	if c["aud"] != "https://push.example.net" || c["sub"] != "mailto:push@example.com" {
		t.Fatalf("claims %v", c)
	}
}

func TestAuthorizationSignsWhatTheRFCNames(t *testing.T) {
	k, err := NewKey()
	if err != nil {
		t.Fatal(err)
	}
	exp := time.Unix(1453523768, 0)
	h, err := k.Authorization("https://push.example.net/p/abc", "mailto:ops@example.com", exp)
	if err != nil {
		t.Fatal(err)
	}
	rest, ok := strings.CutPrefix(h, "vapid t=")
	if !ok {
		t.Fatal(h)
	}
	jwt, kk, _ := strings.Cut(rest, ", k=")
	if kk != k.Public() {
		t.Error("k is not the public key")
	}
	pub := dec(t, k.Public())
	c := verifyJWT(t, jwt, pub[1:33], pub[33:65])
	if c["aud"] != "https://push.example.net" || c["exp"].(float64) != 1453523768 || c["sub"] != "mailto:ops@example.com" {
		t.Fatalf("claims %v", c)
	}
}

func TestKeyRoundTrip(t *testing.T) {
	k, _ := NewKey()
	k2, err := ParseKey(k.Private())
	if err != nil {
		t.Fatal(err)
	}
	if k2.Public() != k.Public() || k2.Fingerprint() != k.Fingerprint() || len(k.Fingerprint()) != 16 {
		t.Error("a parsed key is not the key")
	}
	for _, bad := range []string{"", "AAAA", strings.Repeat("A", 43)} {
		if _, err := ParseKey(bad); err == nil {
			t.Errorf("ParseKey(%q) accepted", bad)
		}
	}
}
