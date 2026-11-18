package writer

import (
	"fmt"
	"math"
	"math/rand"
	"strings"
	"testing"
)

// TestWrapFormulaMatchesTwosComplement evaluates the arithmetic wrap()
// writes, ((v + h) MOD m + m) MOD m - h for a signed type and
// (v MOD m + m) MOD m for an unsigned one, with SCL's MOD (the sign of the
// dividend, as Go's %), and checks it against Go's own conversion, which
// wraps the way nautilus stores (#800).
func TestWrapFormulaMatchesTwosComplement(t *testing.T) {
	conv := map[string]func(int64) int64{
		"SINT":  func(v int64) int64 { return int64(int8(v)) },
		"INT":   func(v int64) int64 { return int64(int16(v)) },
		"DINT":  func(v int64) int64 { return int64(int32(v)) },
		"USINT": func(v int64) int64 { return int64(uint8(v)) },
		"UINT":  func(v int64) int64 { return int64(uint16(v)) },
		"UDINT": func(v int64) int64 { return int64(uint32(v)) },
	}
	vals := []int64{0, 1, -1, 127, 128, -128, -129, 255, 256, 32767, 32768, -32768, -32769, 65535, 65536,
		math.MaxInt32, math.MaxInt32 + 1, math.MinInt32, math.MinInt32 - 1, math.MaxUint32, math.MaxUint32 + 1,
		1 << 40, -(1 << 40)}
	r := rand.New(rand.NewSource(1))
	for range 2000 {
		vals = append(vals, r.Int63n(1<<41)-(1<<40))
	}
	for typ, want := range conv {
		w := intWidth[typ]
		m := int64(1) << w.bits
		for _, v := range vals {
			var got int64
			if w.signed {
				h := m / 2
				got = ((v+h)%m+m)%m - h
			} else {
				got = (v%m + m) % m
			}
			if got != want(v) {
				t.Fatalf("%s: wrap(%d) = %d, want %d", typ, v, got, want(v))
			}
		}
		// And the text wrap() prints is that formula, with v in parentheses.
		s := wrap("V", typ)
		if w.signed {
			if !strings.Contains(s, fmt.Sprintf("(((V) + LINT#%d) MOD LINT#%d + LINT#%d) MOD LINT#%d - LINT#%d", m/2, m, m, m, m/2)) {
				t.Errorf("%s: %s", typ, s)
			}
		} else if !strings.Contains(s, fmt.Sprintf("((V) MOD LINT#%d + LINT#%d) MOD LINT#%d", m, m, m)) {
			t.Errorf("%s: %s", typ, s)
		}
	}
	if wrap("V", "LINT") != "V" {
		t.Error("LINT needs no wrap")
	}
}
