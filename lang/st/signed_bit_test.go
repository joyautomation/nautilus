package st

import (
	"fmt"
	"math"
	"testing"

	"github.com/joyautomation/nautilus/lang/ir"
)

// #611: the top bit of a signed integer is its sign. Setting it gives the
// negative value two's complement has at the declared width (INT.15 :=
// TRUE on 0 is -32768, not 32768), and clearing it on a negative value
// gives the positive one (-1 with .15 := FALSE is 32767), on a plain
// variable, a local, a struct member, an array element, through an IEC
// partial access (%B, %W, %D) and through a bit of one. Unsigned and
// bit-string types keep the unsigned value they always had.

var signedTypes = []struct {
	name     string
	width    int
	min, max int64
}{
	{"SINT", 8, math.MinInt8, math.MaxInt8},
	{"INT", 16, math.MinInt16, math.MaxInt16},
	{"DINT", 32, math.MinInt32, math.MaxInt32},
	{"LINT", 64, math.MinInt64, math.MaxInt64},
}

// runOne runs src once with x seeded and returns the global named out.
func runOne(t *testing.T, src string, seed map[string]ir.Value, out string) int64 {
	t.Helper()
	h, _, _ := scanN(t, src, 1, seed)
	return h.globals[out].I
}

func TestSignedTopBitIsTheSign(t *testing.T) {
	for _, ty := range signedTypes {
		top := ty.width - 1
		head := fmt.Sprintf(`
TYPE R : STRUCT A : BOOL; V : %[1]s; END_STRUCT; END_TYPE
PROGRAM P
VAR_EXTERNAL x : %[1]s; out : %[1]s; neg : BOOL; END_VAR
VAR l : %[1]s; r : R; a : ARRAY[1..3] OF %[1]s; END_VAR
`, ty.name)
		for _, tc := range []struct {
			name string
			body string
			seed int64
			want int64
		}{
			{"global set", fmt.Sprintf("x := 0; x.%d := TRUE; out := x;", top), 0, ty.min},
			{"global clear", fmt.Sprintf("x := -1; x.%d := FALSE; out := x;", top), 0, ty.max},
			{"global set, seeded", fmt.Sprintf("x.%d := TRUE; out := x;", top), 5, ty.min + 5},
			{"global clear, seeded", fmt.Sprintf("x.%d := FALSE; out := x;", top), -6, ty.max - 5},
			{"%X spelling", fmt.Sprintf("x := 0; x.%%X%d := TRUE; out := x;", top), 0, ty.min},
			{"local set", fmt.Sprintf("l := 0; l.%d := TRUE; out := l;", top), 0, ty.min},
			{"local clear", fmt.Sprintf("l := -1; l.%d := FALSE; out := l;", top), 0, ty.max},
			{"member set", fmt.Sprintf("r.V := 0; r.V.%d := TRUE; out := r.V;", top), 0, ty.min},
			{"member clear", fmt.Sprintf("r.V := -1; r.V.%d := FALSE; out := r.V;", top), 0, ty.max},
			{"element set", fmt.Sprintf("a[2] := 0; a[2].%d := TRUE; out := a[2];", top), 0, ty.min},
			{"element clear", fmt.Sprintf("a[2] := -1; a[2].%d := FALSE; out := a[2];", top), 0, ty.max},
			{"same scan sees the sign", fmt.Sprintf("x := 0; x.%d := TRUE; neg := x < 0; out := x;", top), 0, ty.min},
			{"other bits of a negative value", "x := -1; x.3 := FALSE; out := x;", 0, -9},
		} {
			t.Run(ty.name+"/"+tc.name, func(t *testing.T) {
				src := head + tc.body + "\nEND_PROGRAM"
				h, _, _ := scanN(t, src, 1, map[string]ir.Value{"x": ir.IntVal(tc.seed)})
				if got := h.globals["out"].I; got != tc.want {
					t.Errorf("%s = %d, want %d", tc.body, got, tc.want)
				}
				if got := h.globals["x"].I; tc.body[0] == 'x' && got != tc.want {
					t.Errorf("x = %d, want %d", got, tc.want)
				}
				if tc.name == "same scan sees the sign" && !h.globals["neg"].B {
					t.Errorf("x < 0 after setting the sign bit was FALSE")
				}
			})
		}
	}
}

// A partial access that covers the sign bit sets the sign with it.
func TestSignedPartialAccessCarriesTheSign(t *testing.T) {
	for _, tc := range []struct {
		typ, body string
		seed      int64
		want      int64
	}{
		{"SINT", "x.%B0 := 16#FF;", 0, -1},
		{"SINT", "x.%B0 := 16#7F;", -1, 127},
		{"INT", "x.%B1 := 16#80;", 0, math.MinInt16},
		{"INT", "x.%B1 := 16#7F;", -1, math.MaxInt16},
		{"INT", "x.%W0 := 16#FFFF;", 0, -1},
		{"INT", "x.%B1.7 := TRUE;", 0, math.MinInt16},
		{"INT", "x.%B1.%X7 := FALSE;", -1, math.MaxInt16},
		{"INT", "x.%B0 := 16#FF;", 0, 255}, // the sign bit is not in byte 0
		{"INT", "x.%B0 := 0;", -1, -256},
		{"DINT", "x.%W1 := 16#8000;", 0, math.MinInt32},
		{"DINT", "x.%B3 := 16#7F;", -1, math.MaxInt32},
		{"DINT", "x.%D0 := 16#FFFFFFFF;", 0, -1},
		{"LINT", "x.%D1 := 16#80000000;", 0, math.MinInt64},
		{"LINT", "x.%L0 := -1;", 0, -1},
	} {
		t.Run(tc.typ+" "+tc.body, func(t *testing.T) {
			src := "PROGRAM P\nVAR_EXTERNAL x : " + tc.typ + "; END_VAR\n" + tc.body + "\nEND_PROGRAM"
			if got := runOne(t, src, map[string]ir.Value{"x": ir.IntVal(tc.seed)}, "x"); got != tc.want {
				t.Errorf("%s from %d = %d, want %d", tc.body, tc.seed, got, tc.want)
			}
		})
	}
}

// Unsigned and bit-string types have no sign: their top bit is a value bit,
// as before #611.
func TestUnsignedTopBitIsAValueBit(t *testing.T) {
	for _, tc := range []struct {
		typ  string
		top  int
		want int64
	}{
		{"USINT", 7, 128}, {"BYTE", 7, 128},
		{"UINT", 15, 32768}, {"WORD", 15, 32768},
		{"UDINT", 31, 1 << 31}, {"DWORD", 31, 1 << 31},
		{"ULINT", 63, math.MinInt64}, {"LWORD", 63, math.MinInt64}, // the 64-bit pattern
	} {
		t.Run(tc.typ, func(t *testing.T) {
			src := fmt.Sprintf("PROGRAM P\nVAR_EXTERNAL x : %s; END_VAR\nx := 0; x.%d := TRUE;\nEND_PROGRAM", tc.typ, tc.top)
			if got := runOne(t, src, map[string]ir.Value{"x": ir.IntVal(0)}, "x"); got != tc.want {
				t.Errorf("x.%d := TRUE = %d, want %d", tc.top, got, tc.want)
			}
			src = fmt.Sprintf("PROGRAM P\nVAR_EXTERNAL x : %s; END_VAR\nx.%d := FALSE;\nEND_PROGRAM", tc.typ, tc.top)
			if got := runOne(t, src, map[string]ir.Value{"x": ir.IntVal(tc.want | 5)}, "x"); got != 5 {
				t.Errorf("x.%d := FALSE on %d = %d, want 5", tc.top, tc.want|5, got)
			}
		})
	}
}
