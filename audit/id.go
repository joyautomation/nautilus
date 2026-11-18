package audit

import (
	"crypto/rand"
	"time"
)

// crockford is the ULID alphabet (Crockford's base32: no I, L, O, U).
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// NewID returns a ULID for t: 48 bits of Unix milliseconds, then 80 random
// bits, as 26 Crockford base32 characters. IDs sort by time to the
// millisecond; within one, by chance. Only the encoding is written here;
// the randomness is crypto/rand.
func NewID(t time.Time) string {
	var b [16]byte
	ms := uint64(t.UnixMilli())
	for i := 5; i >= 0; i-- {
		b[i] = byte(ms)
		ms >>= 8
	}
	if _, err := rand.Read(b[6:]); err != nil {
		panic("audit: crypto/rand: " + err.Error()) // the platform has no randomness
	}
	// 128 bits as 26 five-bit groups, most significant first (the first
	// group carries the top 3 bits).
	var out [26]byte
	var acc uint32
	bits := 2 // 26*5 = 130 = 128 + 2 leading zero bits
	j := 0
	for _, c := range b {
		acc = acc<<8 | uint32(c)
		bits += 8
		for bits >= 5 {
			bits -= 5
			out[j] = crockford[(acc>>uint(bits))&31]
			j++
		}
	}
	return string(out[:])
}
