package acklink

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 22, 3, 0, 0, 0, time.UTC)

func ring(t *testing.T, key string) *Keyring {
	t.Helper()
	k, err := New([]byte(strings.Repeat(key, 32)[:32]))
	if err != nil {
		t.Fatal(err)
	}
	k.SetClock(func() time.Time { return t0 })
	return k
}

func TestMintVerifyRoundTrip(t *testing.T) {
	k := ring(t, "k")
	tok, err := k.Mint("Tank1.HighLevel", "0123456789abcdef", "oidc:alice", "email", 2, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	c, err := k.Verify(tok)
	if err != nil {
		t.Fatal(err)
	}
	if c.Alarm != "Tank1.HighLevel" || c.Occ != "0123456789abcdef" || c.Principal != "oidc:alice" || c.Kind != "email" || c.Level != 2 {
		t.Fatalf("claim = %+v", c)
	}
	if !c.Expires.Equal(t0.Add(time.Hour)) {
		t.Fatalf("expires %v", c.Expires)
	}
	if len(c.ID) != 2*idLen {
		t.Fatalf("id %q", c.ID)
	}
	if strings.Contains(tok, "/") || strings.Contains(tok, "+") || strings.Contains(tok, "=") {
		t.Fatalf("token not URL-safe: %s", tok)
	}
	tok2, _ := k.Mint("Tank1.HighLevel", "0123456789abcdef", "oidc:alice", "email", 2, time.Hour)
	c2, _ := k.Verify(tok2)
	if c2.ID == c.ID || tok2 == tok {
		t.Fatal("two mints share a link id: spending one would spend both")
	}
}

// Every byte of a token is covered: flipping any one is refused.
func TestEveryByteIsSigned(t *testing.T) {
	k := ring(t, "k")
	tok, _ := k.Mint("a", "o", "alice", "sms", 1, time.Hour)
	b, _ := base64.RawURLEncoding.DecodeString(tok)
	for i := range b {
		c := bytes.Clone(b)
		c[i] ^= 0x01
		if _, err := k.Verify(base64.RawURLEncoding.EncodeToString(c)); err == nil {
			t.Fatalf("byte %d flipped: accepted", i)
		}
	}
	if _, err := k.Verify(tok[:len(tok)-1]); err == nil {
		t.Fatal("truncated token accepted")
	}
	if _, err := k.Verify(tok + "AA"); err == nil {
		t.Fatal("extended token accepted")
	}
}

func TestAnotherKeyRefuses(t *testing.T) {
	tok, _ := ring(t, "k").Mint("a", "o", "alice", "sms", 1, time.Hour)
	if _, err := ring(t, "x").Verify(tok); !errors.Is(err, ErrSignature) {
		t.Fatalf("other key: %v", err)
	}
}

// Fields are length-prefixed: moving a boundary changes the signature.
func TestFieldBoundariesAreUnambiguous(t *testing.T) {
	k := ring(t, "k")
	k.rand = bytes.NewReader(make([]byte, 2*idLen)) // the same id twice
	a, _ := k.Mint("ab", "c", "alice", "sms", 1, time.Hour)
	b, _ := k.Mint("a", "bc", "alice", "sms", 1, time.Hour)
	if a == b {
		t.Fatal("alarm ab / occurrence c and alarm a / occurrence bc mint the same token")
	}
	ca, _ := k.Verify(a)
	cb, _ := k.Verify(b)
	if ca.Alarm != "ab" || cb.Alarm != "a" || ca.Occ != "c" || cb.Occ != "bc" {
		t.Fatalf("%+v %+v", ca, cb)
	}
}

func TestExpiry(t *testing.T) {
	k := ring(t, "k")
	tok, _ := k.Mint("a", "o", "alice", "sms", 1, time.Minute)
	k.SetClock(func() time.Time { return t0.Add(time.Minute - time.Second) })
	if _, err := k.Verify(tok); err != nil {
		t.Fatalf("before expiry: %v", err)
	}
	k.SetClock(func() time.Time { return t0.Add(time.Minute) })
	if _, err := k.Verify(tok); !errors.Is(err, ErrExpired) {
		t.Fatalf("at expiry: %v", err)
	}
}

func TestTTLIsClampedToADay(t *testing.T) {
	k := ring(t, "k")
	tok, _ := k.Mint("a", "o", "alice", "sms", 1, 30*24*time.Hour)
	c, err := k.Verify(tok)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Expires.Sub(t0); got != MaxTTL {
		t.Fatalf("lifetime %v, want %v", got, MaxTTL)
	}
	if _, err := k.Mint("a", "o", "alice", "sms", 1, 0); err == nil {
		t.Fatal("zero lifetime minted")
	}
}

// A genuine token whose expiry lies beyond any Mint's reach (a clock far
// ahead when it was minted) is refused, not honoured for days.
func TestOverlongExpiryRefused(t *testing.T) {
	k := ring(t, "k")
	k.SetClock(func() time.Time { return t0.Add(10 * 24 * time.Hour) })
	tok, _ := k.Mint("a", "o", "alice", "sms", 1, time.Hour)
	k.SetClock(func() time.Time { return t0 })
	if _, err := k.Verify(tok); !errors.Is(err, ErrMalformed) {
		t.Fatalf("overlong: %v", err)
	}
}

func TestMintRefusesBadFields(t *testing.T) {
	k := ring(t, "k")
	long := strings.Repeat("x", maxStr+1)
	for _, f := range [][4]string{{"", "o", "p", "k"}, {"a", "", "p", "k"}, {"a", "o", "", "k"}, {"a", "o", "p", ""}, {long, "o", "p", "k"}} {
		if _, err := k.Mint(f[0], f[1], f[2], f[3], 1, time.Hour); err == nil {
			t.Fatalf("minted %q", f)
		}
	}
	if _, err := New(make([]byte, MinKey-1)); err == nil {
		t.Fatal("short key accepted")
	}
}

func TestURL(t *testing.T) {
	if got := URL("https://hmi.example:8443/", "TOK"); got != "https://hmi.example:8443/alarm-ack/TOK" {
		t.Fatal(got)
	}
	if URL("", "TOK") != "" {
		t.Fatal("no ack-url must mean no link")
	}
}
