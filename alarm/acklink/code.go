package acklink

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Reply codes (#715): what a person types back to an SMS page to
// acknowledge it ("ACK 12345678").
//
// A code is stateless: CodeDigits digits of an HMAC, under a key the
// keyring derives from its own for this purpose alone, over one alarm's
// occurrence, one principal and the hour it was made in. Nothing is
// stored when a page is sent, so a code made by one member of a pair
// checks on the other after a takeover. MatchCode finds which of the
// principal's standing occurrences a reply names.
//
// The code is the credential of a reply: the provider's signature says
// only that the provider relayed the message, and a sender's number can
// be forged. So a code is long enough that guessing is hopeless within
// the server's limit on wrong codes: a guess hits with probability
// (occurrences × hours accepted) / 10^CodeDigits.

const (
	// CodeDigits is a reply code's length.
	CodeDigits = 8
	// codeKind is the channel kind a matched code's claim names.
	codeKind = "sms"
	// CodeIDPrefix starts the ID of a claim made from a reply code, so it
	// never collides with a link's (hex) ID.
	CodeIDPrefix = "c"

	codeKeyInfo = "nautilus ack code key v1"
	codeDomain  = "nautilus ack code v1\x00"
	codeIDDom   = "nautilus ack code id v1\x00"
	bucket      = time.Hour
)

var (
	// ErrNoCode: the reply holds no code.
	ErrNoCode = errors.New("acklink: the reply holds no acknowledgement code")
	// ErrNoMatch: the code names none of the principal's standing
	// occurrences (wrong, mistyped, or older than MaxTTL).
	ErrNoMatch = errors.New("acklink: the code matches no alarm waiting on this person")
	// ErrAmbiguous: the code names more than one standing occurrence (a
	// collision; the person must use the link or the HMI).
	ErrAmbiguous = errors.New("acklink: the code matches more than one alarm")
)

// Standing is one occurrence a reply may name: the alarm, its occurrence,
// and when the occurrence activated (no code made before then counts).
type Standing struct {
	Alarm, Occ string
	Since      time.Time
}

// subkey is HMAC(key, info): a key for one purpose only.
func (k *Keyring) subkey(info string) []byte {
	m := hmac.New(sha256.New, k.key)
	m.Write([]byte(info))
	return m.Sum(nil)
}

// Code is the reply code for a page, made now, about alarmID's
// occurrence occ, to principal. "" when an argument is empty.
func (k *Keyring) Code(alarmID, occ, principal string) string {
	if alarmID == "" || occ == "" || principal == "" {
		return ""
	}
	code, _ := k.codeAt(alarmID, occ, principal, bucketOf(k.now()))
	return code
}

func bucketOf(t time.Time) int64 { return t.Unix() / int64(bucket/time.Second) }

func bucketStart(b int64) time.Time { return time.Unix(b*int64(bucket/time.Second), 0) }

// codeAt is the code for (alarm, occ, principal) in hour b, and the ID of
// that code: a second MAC, so the ID (journalled, in the log) never
// reveals the code.
func (k *Keyring) codeAt(alarmID, occ, principal string, b int64) (code, id string) {
	in := codeInput(alarmID, occ, principal, b)
	m := hmac.New(sha256.New, k.code)
	m.Write([]byte(codeDomain))
	m.Write(in)
	sum := m.Sum(nil)
	// 64 bits mod 10^8: the bias is below 10^-11.
	n := binary.BigEndian.Uint64(sum[:8]) % pow10(CodeDigits)
	code = fmt.Sprintf("%0*d", CodeDigits, n)
	m = hmac.New(sha256.New, k.code)
	m.Write([]byte(codeIDDom))
	m.Write(in)
	return code, CodeIDPrefix + hex.EncodeToString(m.Sum(nil)[:idLen])
}

func codeInput(alarmID, occ, principal string, b int64) []byte {
	var p []byte
	for _, s := range []string{alarmID, occ, principal} {
		p = binary.AppendUvarint(p, uint64(len(s)))
		p = append(p, s...)
	}
	return binary.AppendVarint(p, b)
}

func pow10(n int) uint64 {
	v := uint64(1)
	for range n {
		v *= 10
	}
	return v
}

// ParseReply is the code in an SMS reply: CodeDigits digits, alone or
// after "ACK" (any case), spaces and dashes between digits allowed.
func ParseReply(body string) (string, error) {
	s := strings.TrimSpace(body)
	if len(s) > 64 {
		return "", ErrNoCode
	}
	if len(s) >= 3 && strings.EqualFold(s[:3], "ack") {
		s = s[3:]
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '\t' || r == ':':
		default:
			return "", ErrNoCode
		}
	}
	if b.Len() != CodeDigits {
		return "", ErrNoCode
	}
	return b.String(), nil
}

// MatchCode finds the occurrence among standing (each listed once) that
// code names for principal. A code counts from the hour its occurrence activated until
// MaxTTL after the end of the hour it was made in (a page sent late in
// an hour lives a little longer than MaxTTL, never shorter). The claim
// it returns names kind sms, level 0 (a code does not say the level) and
// an ID that is the same for the same code, so spending it once spends
// every page that carried it.
func (k *Keyring) MatchCode(code, principal string, standing []Standing) (Claim, error) {
	if len(code) != CodeDigits || principal == "" {
		return Claim{}, ErrNoMatch
	}
	now := k.now()
	newest := bucketOf(now.Add(skew))
	oldest := bucketOf(now.Add(-MaxTTL - bucket))
	var found Claim
	n := 0
	for _, st := range standing {
		if st.Alarm == "" || st.Occ == "" {
			continue
		}
		from := oldest
		if s := bucketOf(st.Since); !st.Since.IsZero() && s > from {
			from = s
		}
		for b := newest; b >= from; b-- {
			exp := bucketStart(b).Add(bucket + MaxTTL)
			if !now.Before(exp) {
				break
			}
			c, id := k.codeAt(st.Alarm, st.Occ, principal, b)
			if subtle.ConstantTimeCompare([]byte(c), []byte(code)) != 1 {
				continue
			}
			if n++; n == 1 {
				found = Claim{ID: id, Alarm: st.Alarm, Occ: st.Occ, Principal: principal, Kind: codeKind, Expires: exp}
			}
			break // one hour is enough for this occurrence
		}
	}
	switch {
	case n == 0:
		return Claim{}, ErrNoMatch
	case n > 1:
		return Claim{}, ErrAmbiguous
	}
	return found, nil
}
