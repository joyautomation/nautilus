package pairkey

import (
	"bytes"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

// The derivation is pinned: both members of a pair, on any two builds,
// must derive the same key, so a change here is a change to the wire.
func TestDeriveIsPinned(t *testing.T) {
	k, err := Derive([]byte("peer-token-for-tests"), AckLink)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := hkdf.Key(sha256.New, []byte("peer-token-for-tests"), nil, "nautilus pairkey v1 alarm-ack-link", 32)
	if !bytes.Equal(k, want) {
		t.Fatalf("Derive = %s, want %s", hex.EncodeToString(k), hex.EncodeToString(want))
	}
	if len(k) != Size {
		t.Fatalf("len = %d", len(k))
	}
}

func TestDeriveSeparatesLabelsAndTokens(t *testing.T) {
	a, _ := Derive([]byte("t"), "one")
	b, _ := Derive([]byte("t"), "two")
	c, _ := Derive([]byte("u"), "one")
	a2, _ := Derive([]byte("t"), "one")
	if bytes.Equal(a, b) || bytes.Equal(a, c) {
		t.Fatal("two purposes or two tokens share a key")
	}
	if !bytes.Equal(a, a2) {
		t.Fatal("the same token and label derived two keys: the pair's members would disagree")
	}
	if bytes.Equal(a, []byte("t")) {
		t.Fatal("the key is the token")
	}
}

func TestDeriveRefusesNothing(t *testing.T) {
	if _, err := Derive(nil, AckLink); err != ErrNoToken {
		t.Fatalf("empty token: %v", err)
	}
	if _, err := Derive([]byte("t"), ""); err == nil {
		t.Fatal("empty label accepted")
	}
}
