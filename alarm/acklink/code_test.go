package acklink

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func standing(since time.Time) []Standing {
	return []Standing{
		{Alarm: "Tank1.High", Occ: "occ-1", Since: since},
		{Alarm: "Pump2.Fault", Occ: "occ-9", Since: since},
	}
}

func TestCodeRoundTrip(t *testing.T) {
	k := ring(t, "k")
	code := k.Code("Pump2.Fault", "occ-9", "alice")
	if len(code) != CodeDigits || strings.Trim(code, "0123456789") != "" {
		t.Fatalf("code %q", code)
	}
	c, err := k.MatchCode(code, "alice", standing(t0.Add(-time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	if c.Alarm != "Pump2.Fault" || c.Occ != "occ-9" || c.Principal != "alice" || c.Kind != "sms" || !strings.HasPrefix(c.ID, CodeIDPrefix) {
		t.Fatalf("claim %+v", c)
	}
	if strings.Contains(c.ID, code) {
		t.Fatal("the code's id reveals the code")
	}
	// The same code later in the hour: the same ID (spent once, for all
	// pages that carried it).
	k.SetClock(func() time.Time { return t0.Add(59 * time.Minute) })
	c2, err := k.MatchCode(code, "alice", standing(t0))
	if err != nil || c2.ID != c.ID {
		t.Fatalf("same code, other minute: %+v %v", c2, err)
	}
}

// A code binds the occurrence and the principal: another person, a new
// occurrence of the same alarm, or another alarm does not match.
func TestCodeBindsOccurrenceAndPrincipal(t *testing.T) {
	k := ring(t, "k")
	code := k.Code("Tank1.High", "occ-1", "alice")
	if _, err := k.MatchCode(code, "bob", standing(t0)); !errors.Is(err, ErrNoMatch) {
		t.Fatalf("bob used alice's code: %v", err)
	}
	if _, err := k.MatchCode(code, "alice", []Standing{{Alarm: "Tank1.High", Occ: "occ-2", Since: t0}}); !errors.Is(err, ErrNoMatch) {
		t.Fatalf("a new occurrence matched an old code: %v", err)
	}
	if _, err := k.MatchCode(code, "alice", []Standing{{Alarm: "Pump2.Fault", Occ: "occ-1", Since: t0}}); !errors.Is(err, ErrNoMatch) {
		t.Fatalf("another alarm matched: %v", err)
	}
	if _, err := ring(t, "x").MatchCode(code, "alice", standing(t0)); !errors.Is(err, ErrNoMatch) {
		t.Fatalf("another key matched: %v", err)
	}
}

// A code lives MaxTTL past the end of its hour, never before the
// occurrence activated.
func TestCodeWindow(t *testing.T) {
	k := ring(t, "k")
	code := k.Code("Tank1.High", "occ-1", "alice") // t0 is on the hour
	at := func(d time.Duration, since time.Time) error {
		k.SetClock(func() time.Time { return t0.Add(d) })
		_, err := k.MatchCode(code, "alice", standing(since))
		return err
	}
	if err := at(MaxTTL+bucket-time.Second, t0); err != nil {
		t.Fatalf("a code in its last second refused: %v", err)
	}
	if err := at(MaxTTL+bucket, t0); !errors.Is(err, ErrNoMatch) {
		t.Fatalf("a code past MaxTTL matched: %v", err)
	}
	// Made before the occurrence activated (an hour before): not this
	// occurrence's code.
	if err := at(2*time.Hour, t0.Add(time.Hour)); !errors.Is(err, ErrNoMatch) {
		t.Fatalf("a code from before the activation matched: %v", err)
	}
	// Made by a member whose clock runs a little ahead (next hour, within
	// skew).
	k.SetClock(func() time.Time { return t0.Add(59*time.Minute + 58*time.Second) })
	k2 := ring(t, "k")
	k2.SetClock(func() time.Time { return t0.Add(time.Hour) })
	ahead := k2.Code("Tank1.High", "occ-1", "alice")
	if _, err := k.MatchCode(ahead, "alice", standing(t0)); err != nil {
		t.Fatalf("a code from a clock a second ahead refused: %v", err)
	}
}

func TestCodeMatchingTwoOccurrencesIsRefused(t *testing.T) {
	k := ring(t, "k")
	code := k.Code("Tank1.High", "occ-1", "alice")
	// The same occurrence listed under two alarms stands in for a
	// collision.
	two := []Standing{{Alarm: "Tank1.High", Occ: "occ-1", Since: t0}, {Alarm: "Tank1.High", Occ: "occ-1", Since: t0}}
	if _, err := k.MatchCode(code, "alice", two); !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("err = %v", err)
	}
}

// The code key is not the link key: a code is not a link MAC's prefix.
func TestCodeKeyIsItsOwn(t *testing.T) {
	k := ring(t, "k")
	if string(k.code) == string(k.key) || len(k.code) != 32 {
		t.Fatal("codes are made under the link key")
	}
}

func TestParseReply(t *testing.T) {
	for in, want := range map[string]string{
		"ACK 12345678":    "12345678",
		"ack 1234-5678":   "12345678",
		"  12345678 \n":   "12345678",
		"Ack:12 34 56 78": "12345678",
		"ACK12345678":     "12345678",
		"ACK 1234567":     "",
		"ACK 123456789":   "",
		"ACK":             "",
		"yes 12345678":    "",
		"12345678 thanks": "",
		"ACK １２３４５６７８":    "",
	} {
		got, err := ParseReply(in)
		if got != want || (want == "") != (err != nil) {
			t.Errorf("ParseReply(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}
