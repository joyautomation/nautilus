package ir

import (
	"math"
	"strings"
	"testing"
)

func call(t *testing.T, name string, arg Value) (Value, error) {
	t.Helper()
	sig, ok := Builtins[name]
	if !ok {
		t.Fatalf("%s is not registered", name)
	}
	return sig.Fn([]Value{arg})
}

// The matrix is generated: every ordered pair of the 18 elementary types
// except identity and the REAL↔bit-string widths the standard leaves out.
func TestConversionMatrixShape(t *testing.T) {
	names := ConversionTypeNames()
	if len(names) != 18 {
		t.Fatalf("%d elementary types, want 18: %v", len(names), names)
	}
	pairs := 0
	for _, a := range names {
		for _, b := range names {
			_, ok := Builtins[a+"_TO_"+b]
			if a == b {
				if ok {
					t.Errorf("%s_TO_%s is registered", a, b)
				}
				continue
			}
			if ok {
				pairs++
			}
		}
	}
	if pairs != 18*17-12 {
		t.Errorf("%d X_TO_Y conversions, want %d", pairs, 18*17-12)
	}
	for _, missing := range []string{"REAL_TO_WORD", "REAL_TO_BYTE", "REAL_TO_LWORD", "WORD_TO_REAL", "LREAL_TO_DWORD", "DWORD_TO_LREAL"} {
		if _, ok := Builtins[missing]; ok {
			t.Errorf("%s is registered; the standard defines no such conversion", missing)
		}
		if h := ConversionHint(missing); !strings.Contains(h, "bit pattern") {
			t.Errorf("ConversionHint(%s) = %q", missing, h)
		}
	}
	for _, n := range names {
		for _, f := range []string{"TO_" + n} {
			if !IsConversion(f) {
				t.Errorf("%s is not registered", f)
			}
		}
	}
	for _, n := range []string{"SINT", "INT", "DINT", "LINT", "USINT", "UINT", "UDINT", "ULINT"} {
		for _, f := range []string{"TRUNC_" + n, "REAL_TRUNC_" + n, "LREAL_TRUNC_" + n} {
			if !IsConversion(f) {
				t.Errorf("%s is not registered", f)
			}
		}
	}
}

func TestConversionSemantics(t *testing.T) {
	type tc struct {
		fn   string
		in   Value
		want Value
	}
	cases := []tc{
		// integer narrowing wraps to the target width
		{"DINT_TO_INT", IntVal(70000), IntVal(4464)},
		{"DINT_TO_INT", IntVal(32767), IntVal(32767)},
		{"DINT_TO_INT", IntVal(32768), IntVal(-32768)},
		{"DINT_TO_INT", IntVal(-32769), IntVal(32767)},
		{"INT_TO_SINT", IntVal(200), IntVal(-56)},
		{"INT_TO_USINT", IntVal(-1), IntVal(255)},
		{"INT_TO_UINT", IntVal(-1), IntVal(65535)},
		{"BYTE_TO_SINT", IntVal(255), IntVal(-1)},
		{"SINT_TO_WORD", IntVal(-1), IntVal(65535)}, // sign-extends, then keeps 16 bits
		{"SINT_TO_INT", IntVal(-5), IntVal(-5)},
		{"USINT_TO_INT", IntVal(255), IntVal(255)},
		{"LINT_TO_DINT", IntVal(1 << 32), IntVal(0)},
		{"LINT_TO_UDINT", IntVal(-1), IntVal(4294967295)},
		{"LINT_TO_ULINT", IntVal(-1), IntVal(-1)}, // ULINT 2^64-1 held as its bit pattern
		{"DWORD_TO_DINT", IntVal(0xFFFFFFFF), IntVal(-1)},
		// the source is read as its own type first
		{"UINT_TO_DINT", IntVal(-1), IntVal(65535)},
		{"INT_TO_DINT", IntVal(40000), IntVal(-25536)},
		// REAL → integer rounds half to even
		{"REAL_TO_DINT", RealVal(2.5), IntVal(2)},
		{"REAL_TO_DINT", RealVal(3.5), IntVal(4)},
		{"REAL_TO_DINT", RealVal(-2.5), IntVal(-2)},
		{"LREAL_TO_SINT", RealVal(127.4), IntVal(127)},
		{"LREAL_TO_SINT", RealVal(-128.5), IntVal(-128)}, // tie to even stays in range
		{"REAL_TO_USINT", RealVal(255.0), IntVal(255)},
		{"REAL_TO_ULINT", RealVal(1.8446744073709550e19), IntVal(-2048)},
		// TRUNC goes toward zero
		{"TRUNC_INT", RealVal(-2.9), IntVal(-2)},
		{"REAL_TRUNC_DINT", RealVal(2.9), IntVal(2)},
		{"LREAL_TRUNC_SINT", RealVal(-128.9), IntVal(-128)},
		// integers → REAL, unsigned 64-bit read unsigned
		{"DINT_TO_REAL", IntVal(-7), RealVal(-7)},
		{"ULINT_TO_LREAL", IntVal(-1), RealVal(18446744073709551615)},
		{"LINT_TO_LREAL", IntVal(1 << 40), RealVal(1 << 40)},
		// binary transfers
		{"REAL_TO_DWORD", RealVal(1.0), IntVal(0x3F800000)},
		{"DWORD_TO_REAL", IntVal(0x40490FDB), RealVal(float64(math.Float32frombits(0x40490FDB)))},
		{"LREAL_TO_LWORD", RealVal(1.0), IntVal(0x3FF0000000000000)},
		{"LWORD_TO_LREAL", IntVal(0x4000000000000000), RealVal(2)},
		// BOOL
		{"BOOL_TO_DINT", BoolVal(true), IntVal(1)},
		{"BOOL_TO_LREAL", BoolVal(false), RealVal(0)},
		{"BOOL_TO_TIME", BoolVal(true), TimeVal(1)},
		{"WORD_TO_BOOL", IntVal(256), BoolVal(true)},
		{"BYTE_TO_BOOL", IntVal(256), BoolVal(false)}, // 256 is 0 as a BYTE
		{"TIME_TO_BOOL", TimeVal(0), BoolVal(false)},
		{"LREAL_TO_BOOL", RealVal(-0.5), BoolVal(true)},
		// TIME is milliseconds
		{"TIME_TO_DINT", TimeVal(90000), IntVal(90000)},
		{"TIME_TO_INT", TimeVal(70000), IntVal(4464)},
		{"UDINT_TO_TIME", IntVal(1500), TimeVal(1500)},
		{"LREAL_TO_TIME", RealVal(1500.5), TimeVal(1500)},
		{"TIME_TO_LTIME", TimeVal(42), TimeVal(42)},
		{"LTIME_TO_LREAL", TimeVal(250), RealVal(250)},
		// REAL / LREAL share float64
		{"LREAL_TO_REAL", RealVal(0.1), RealVal(0.1)},
		// STRING out
		{"ULINT_TO_STRING", IntVal(-1), StringVal("18446744073709551615")},
		{"SINT_TO_STRING", IntVal(-128), StringVal("-128")},
		{"WORD_TO_STRING", IntVal(-1), StringVal("65535")},
		{"LREAL_TO_STRING", RealVal(0.1), StringVal("0.1")},
		{"LTIME_TO_STRING", TimeVal(5000), StringVal("LTIME#5000ms")},
		{"TIME_TO_STRING", TimeVal(5000), StringVal("T#5000ms")},
		// STRING in
		{"STRING_TO_INT", StringVal(" -32768 "), IntVal(-32768)},
		{"STRING_TO_WORD", StringVal("16#FFFF"), IntVal(65535)},
		{"STRING_TO_BYTE", StringVal("2#1010_1010"), IntVal(0xAA)},
		{"STRING_TO_LINT", StringVal("-9223372036854775808"), IntVal(math.MinInt64)},
		{"STRING_TO_ULINT", StringVal("18446744073709551615"), IntVal(-1)},
		{"STRING_TO_LREAL", StringVal("1_000.5"), RealVal(1000.5)},
		{"STRING_TO_TIME", StringVal("T#1m30s"), TimeVal(90000)},
		{"STRING_TO_LTIME", StringVal("LTIME#5000ms"), TimeVal(5000)},
		{"STRING_TO_BOOL", StringVal("true"), BoolVal(true)},
	}
	for _, c := range cases {
		got, err := call(t, c.fn, c.in)
		if err != nil {
			t.Errorf("%s(%v): %v", c.fn, c.in, err)
			continue
		}
		if got.Kind != c.want.Kind || got.I != c.want.I || got.B != c.want.B || got.S != c.want.S ||
			(got.F != c.want.F && !(math.IsNaN(got.F) && math.IsNaN(c.want.F))) {
			t.Errorf("%s(%+v) = %+v, want %+v", c.fn, c.in, got, c.want)
		}
	}
}

func TestConversionFaults(t *testing.T) {
	for _, c := range []struct {
		fn  string
		in  Value
		msg string
	}{
		{"REAL_TO_SINT", RealVal(127.5), "REAL_TO_SINT: 127.5 is out of range for SINT (-128..127)"},
		{"REAL_TO_USINT", RealVal(-0.6), "out of range for USINT (0..255)"},
		{"REAL_TO_DINT", RealVal(math.NaN()), "out of range for DINT"},
		{"LREAL_TO_LINT", RealVal(math.Inf(1)), "out of range for LINT"},
		{"LREAL_TO_LINT", RealVal(9223372036854775808.0), "out of range for LINT"},
		{"REAL_TO_ULINT", RealVal(18446744073709551616.0), "out of range for ULINT"},
		{"TRUNC_SINT", RealVal(128.0), "out of range for SINT"},
		{"TRUNC", RealVal(math.NaN()), "TRUNC: NaN is out of range"},
		{"REAL_TO_TIME", RealVal(math.Inf(-1)), "out of range for TIME"},
		{"DWORD_TO_REAL", IntVal(0x7FC00000), "not a finite REAL"},
		{"STRING_TO_SINT", StringVal("128"), `STRING_TO_SINT: "128" is out of range for SINT (-128..127)`},
		{"STRING_TO_UINT", StringVal("-1"), "out of range for UINT"},
		{"STRING_TO_LINT", StringVal("9223372036854775808"), "out of range for LINT"},
		{"STRING_TO_ULINT", StringVal("18446744073709551616"), "out of range for ULINT"},
		{"STRING_TO_INT", StringVal("nope"), `STRING_TO_INT: "nope" is not an integer`},
		{"STRING_TO_INT", StringVal("2.5"), "is not an integer"},
		{"STRING_TO_WORD", StringVal("3#12"), "is not an integer"},
		{"STRING_TO_REAL", StringVal("NaN"), "is not a number"},
		{"STRING_TO_TIME", StringVal("soon"), "is not a duration"},
		{"STRING_TO_BOOL", StringVal("yes"), "is not TRUE/FALSE"},
	} {
		_, err := call(t, c.fn, c.in)
		if err == nil || !strings.Contains(err.Error(), c.msg) {
			t.Errorf("%s(%+v): err = %v, want %q", c.fn, c.in, err, c.msg)
		}
	}
}

// TO_<type> resolves the implementation from the argument's declared type.
func TestOverloadedTo(t *testing.T) {
	sig := Builtins["TO_UINT"]
	for _, c := range []struct {
		arg  *Type
		in   Value
		want int64
	}{
		{IntNamed("SINT"), IntVal(-1), 65535},
		{IntNamed("DINT"), IntVal(70000), 4464},
		{RealT, RealVal(2.5), 2},
		{BoolT, BoolVal(true), 1},
		{StringT, StringVal("16#10"), 16},
		{TimeT, TimeVal(1000), 1000},
		{IntT, IntVal(65536), 0}, // an expression's integer: all 64 bits, then fitted
	} {
		rt, fn, err := sig.Resolve([]*Type{c.arg})
		if err != nil {
			t.Fatalf("TO_UINT(%s): %v", c.arg, err)
		}
		if rt.String() != "UINT" {
			t.Errorf("TO_UINT result type %s", rt)
		}
		got, err := fn([]Value{c.in})
		if err != nil || got.I != c.want {
			t.Errorf("TO_UINT(%s %+v) = %+v, %v; want %d", c.arg, c.in, got, err, c.want)
		}
	}
	if _, _, err := Builtins["TO_WORD"].Resolve([]*Type{RealT}); err == nil || !strings.Contains(err.Error(), "bit pattern") {
		t.Errorf("TO_WORD(REAL): err = %v, want the REAL↔bit-string hint", err)
	}
	if _, _, err := Builtins["TO_INT"].Resolve([]*Type{{Kind: TypeStruct}}); err == nil {
		t.Error("TO_INT(struct) resolved")
	}
	rt, fn, err := Builtins["TO_LREAL"].Resolve([]*Type{IntNamed("ULINT")})
	if err != nil || rt.String() != "LREAL" {
		t.Fatalf("TO_LREAL(ULINT): %v %v", rt, err)
	}
	if got, _ := fn([]Value{IntVal(-1)}); got.F != 18446744073709551615 {
		t.Errorf("TO_LREAL(ULINT max) = %v", got.F)
	}
}

// Every generated STRING_TO_x reads back what x_TO_STRING wrote.
func TestConversionStringRoundTrip(t *testing.T) {
	samples := map[string][]Value{
		"BOOL": {BoolVal(true), BoolVal(false)},
		"SINT": {IntVal(-128), IntVal(127)}, "INT": {IntVal(-32768), IntVal(32767)},
		"DINT": {IntVal(math.MinInt32), IntVal(math.MaxInt32)}, "LINT": {IntVal(math.MinInt64), IntVal(math.MaxInt64)},
		"USINT": {IntVal(255)}, "UINT": {IntVal(65535)}, "UDINT": {IntVal(4294967295)}, "ULINT": {IntVal(-1)},
		"BYTE": {IntVal(0xAB)}, "WORD": {IntVal(0xABCD)}, "DWORD": {IntVal(0xDEADBEEF)}, "LWORD": {IntVal(-2)},
		"REAL": {RealVal(-0.125), RealVal(1e21)}, "LREAL": {RealVal(math.Pi)},
		"TIME": {TimeVal(0), TimeVal(90061001)}, "LTIME": {TimeVal(-5)},
	}
	for typ, vals := range samples {
		for _, v := range vals {
			s, err := call(t, typ+"_TO_STRING", v)
			if err != nil {
				t.Fatal(err)
			}
			back, err := call(t, "STRING_TO_"+typ, s)
			if err != nil {
				t.Errorf("STRING_TO_%s(%q): %v", typ, s.S, err)
				continue
			}
			if back.I != v.I || back.F != v.F || back.B != v.B {
				t.Errorf("%s %+v → %q → %+v", typ, v, s.S, back)
			}
		}
	}
}
