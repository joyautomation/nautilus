package ir

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// The IEC 61131-3 type-conversion functions (#244), generated from one table
// of the elementary types rather than written out by hand: every
// `<A>_TO_<B>` between BOOL, the eight integers, the four bit strings,
// REAL/LREAL, TIME/LTIME and STRING; the overloaded `TO_<B>(x)`; and
// truncation, `TRUNC_<int>` plus the standard's typed `<real>_TRUNC_<int>`.
// The ST lowering, FBD (which lowers through ST), the LSP's completion and
// signature help all read ir.Builtins, so registering here is the whole job.
//
// The rules (docs/functions.md "Type conversions" is the user-facing copy):
//
//   - The argument is first taken as a value of the source type: an integer
//     variable holds a 64-bit value at run time and does not wrap on
//     arithmetic (st-int-width), so `UINT_TO_STRING(u)` with u at -1 reads
//     it as the UINT it would be on a PLC, 65535.
//   - Integer and bit-string NARROWING WRAPS: the result keeps the target's
//     low bits, read signed or unsigned as the target is (DINT_TO_INT(70000)
//     = 4464, INT_TO_USINT(-1) = 255, BYTE_TO_SINT(255) = -1). Codesys
//     documents exactly this ("the high bytes are lost"), TIA writes the low
//     bits (and drops ENO), the standard calls the integer→bit-string ones a
//     binary transfer. Saturating would match none of them.
//   - REAL→integer and REAL→TIME round to nearest, ties to even (IEC 60559),
//     as REAL_TO_INT always has; TRUNC_* go toward zero. A REAL with no value
//     in the target type (out of range, NaN, Inf) FAULTS the scan, the same
//     rule as a STRING that does not parse: there is no right answer to
//     store, and TIA reports it as an error (ENO FALSE) too.
//   - REAL_TO_DWORD / DWORD_TO_REAL and LREAL_TO_LWORD / LWORD_TO_LREAL are
//     binary transfers (the IEEE 754 bit pattern), as the standard and TIA
//     define them. The standard has no REAL↔bit-string conversion of a
//     different width, so REAL_TO_WORD and its kin do not exist; Codesys's
//     numeric REAL_TO_WORD is REAL_TO_UINT here (ConversionHint says so).
//   - BOOL ↔ anything numeric is 0/1, and anything numeric → BOOL is
//     "nonzero".
//   - TIME and LTIME count milliseconds (LTIME is not nanoseconds here).
//   - REAL and LREAL are both float64 at run time, so REAL_TO_LREAL and
//     LREAL_TO_REAL change the type, not the value.
//   - STRING parses: an integer in decimal or base-prefixed (16#FF, 2#1010,
//     8#17, `_` separators), a REAL in any Go float syntax that is finite, a
//     TIME as a duration literal (T#1m30s, 90s), a BOOL as TRUE/FALSE/1/0.
//     Text that does not parse, or parses to a value the target cannot hold,
//     FAULTS the scan.
//
// DATE, TIME_OF_DAY and DATE_AND_TIME are not runtime types yet, so they
// have no conversions.

// convClass is the family an elementary type converts as.
type convClass uint8

const (
	ccBool convClass = iota
	ccInt            // ANY_INT and ANY_BIT: an integer of some width
	ccReal
	ccTime
	ccString
)

// convType is one elementary type of the conversion table.
type convType struct {
	Name   string
	class  convClass
	width  int  // integers and bit strings: 8/16/32/64; REAL 32, LREAL 64
	signed bool // integers: SINT..LINT
	bits   bool // ANY_BIT: BYTE..LWORD
	T      *Type
}

// LREAL and LTIME are the same run-time kinds as REAL and TIME; the named
// types exist so a signature or a diagnostic says LREAL where the user wrote
// LREAL. Equal ignores the name, so they assign freely with their twins.
var (
	lrealT = &Type{Kind: TypeReal, Name: "LREAL"}
	ltimeT = &Type{Kind: TypeTime, Name: "LTIME"}
)

// convTypes is the conversion table's elementary types in the order the
// docs and the conformance matrix list them.
var convTypes = func() []convType {
	out := []convType{{Name: "BOOL", class: ccBool, T: BoolT}}
	for _, n := range []struct {
		name   string
		width  int
		signed bool
		bits   bool
	}{
		{"SINT", 8, true, false}, {"INT", 16, true, false}, {"DINT", 32, true, false}, {"LINT", 64, true, false},
		{"USINT", 8, false, false}, {"UINT", 16, false, false}, {"UDINT", 32, false, false}, {"ULINT", 64, false, false},
		{"BYTE", 8, false, true}, {"WORD", 16, false, true}, {"DWORD", 32, false, true}, {"LWORD", 64, false, true},
	} {
		out = append(out, convType{Name: n.name, class: ccInt, width: n.width, signed: n.signed, bits: n.bits, T: IntNamed(n.name)})
	}
	return append(out,
		convType{Name: "REAL", class: ccReal, width: 32, T: RealT},
		convType{Name: "LREAL", class: ccReal, width: 64, T: lrealT},
		convType{Name: "TIME", class: ccTime, T: TimeT},
		convType{Name: "LTIME", class: ccTime, T: ltimeT},
		convType{Name: "STRING", class: ccString, T: StringT},
	)
}()

func convTypeNamed(name string) (convType, bool) {
	for _, c := range convTypes {
		if c.Name == name {
			return c, true
		}
	}
	return convType{}, false
}

// ConversionTypeNames lists the elementary types the conversion functions
// cover, in table order (BOOL, SINT … LWORD, REAL, LREAL, TIME, LTIME,
// STRING).
func ConversionTypeNames() []string {
	out := make([]string, len(convTypes))
	for i, c := range convTypes {
		out[i] = c.Name
	}
	return out
}

// convAllowed reports whether the standard defines <a>_TO_<b>. Everything
// converts to everything except a REAL and a bit string of a different
// width (the standard's only REAL↔ANY_BIT conversions are the same-width
// binary transfers).
func convAllowed(a, b convType) bool {
	if a.Name == b.Name {
		return false
	}
	if a.class == ccReal && b.bits {
		return a.width == b.width
	}
	if b.class == ccReal && a.bits {
		return a.width == b.width
	}
	return true
}

// IsConversion reports whether name is one of the generated conversion
// functions (X_TO_Y, TO_Y, TRUNC_Y, X_TRUNC_Y).
func IsConversion(name string) bool {
	_, ok := conversionNames[NameKey(name)]
	return ok
}

var conversionNames = map[string]struct{}{}

func init() { registerConversions() }

func registerConversions() {
	for _, a := range convTypes {
		for _, b := range convTypes {
			if !convAllowed(a, b) {
				continue
			}
			a, b := a, b
			name := a.Name + "_TO_" + b.Name
			conversionNames[name] = struct{}{}
			RegisterBuiltin(BuiltinSig{
				Name:   name,
				Params: []*Type{a.T},
				Result: b.T,
				Fn:     func(args []Value) (Value, error) { return convert(name, a, b, args[0]) },
			})
		}
	}
	for _, b := range convTypes {
		b := b
		name := "TO_" + b.Name
		conversionNames[name] = struct{}{}
		RegisterBuiltin(BuiltinSig{
			Name:    name,
			Params:  []*Type{nil},
			Result:  b.T,
			Generic: "ANY_ELEMENTARY",
			Resolve: func(ts []*Type) (*Type, BuiltinFn, error) {
				if len(ts) != 1 {
					return nil, nil, fmt.Errorf("%s converts one value", name)
				}
				a, ok := sourceConvType(ts[0])
				if !ok {
					return nil, nil, fmt.Errorf("%s converts an elementary value (BOOL, an integer, a bit string, REAL, TIME, STRING or an enumeration), got %s", name, ts[0])
				}
				if a.Name == b.Name {
					return b.T, func(args []Value) (Value, error) { return convert(name, a, b, args[0]) }, nil
				}
				if !convAllowed(a, b) {
					return nil, nil, fmt.Errorf("%s: there is no %s_TO_%s%s", name, a.Name, b.Name, ConversionHint(a.Name+"_TO_"+b.Name))
				}
				return b.T, func(args []Value) (Value, error) { return convert(name, a, b, args[0]) }, nil
			},
			// Fn is only reached by a caller that skips Resolve; it converts
			// from whatever kind the value carries.
			Fn: func(args []Value) (Value, error) { return convert(name, kindConvType(args[0]), b, args[0]) },
		})
	}
	for _, b := range convTypes {
		if b.class != ccInt || b.bits {
			continue
		}
		b := b
		for _, form := range []struct {
			name string
			from *Type
		}{
			{"TRUNC_" + b.Name, RealT},
			{"REAL_TRUNC_" + b.Name, RealT},
			{"LREAL_TRUNC_" + b.Name, lrealT},
		} {
			name := form.name
			conversionNames[name] = struct{}{}
			RegisterBuiltin(BuiltinSig{
				Name:   name,
				Params: []*Type{form.from},
				Result: b.T,
				Fn:     func(args []Value) (Value, error) { return realToInt(name, b, args[0].F, math.Trunc) },
			})
		}
	}
}

// sourceConvType is the conversion-table type a value of t converts from:
// its declared elementary type; an expression's undeclared-width integer
// (a + b) as LINT, which keeps every bit; an enumeration as its integer.
func sourceConvType(t *Type) (convType, bool) {
	if t == nil {
		return convType{}, false
	}
	switch t.Kind {
	case TypeBool:
		return convTypeNamed("BOOL")
	case TypeInt:
		if t.Enum != nil || t.Name == "" {
			return convTypeNamed("LINT")
		}
		return convTypeNamed(t.Name)
	case TypeReal:
		if t.Name == "LREAL" {
			return convTypeNamed("LREAL")
		}
		return convTypeNamed("REAL")
	case TypeTime:
		if t.Name == "LTIME" {
			return convTypeNamed("LTIME")
		}
		return convTypeNamed("TIME")
	case TypeString:
		return convTypeNamed("STRING")
	}
	return convType{}, false
}

// kindConvType is sourceConvType for a bare run-time value.
func kindConvType(v Value) convType {
	var c convType
	switch v.Kind {
	case TypeBool:
		c, _ = convTypeNamed("BOOL")
	case TypeReal:
		c, _ = convTypeNamed("LREAL")
	case TypeTime:
		c, _ = convTypeNamed("TIME")
	case TypeString:
		c, _ = convTypeNamed("STRING")
	default:
		c, _ = convTypeNamed("LINT")
	}
	return c
}

// ConversionHint explains, for a call the registry does not know that is
// shaped like a conversion between two elementary types, why it is missing
// and what to write instead — "" when name is not such a call.
func ConversionHint(name string) string {
	from, to, ok := strings.Cut(NameKey(name), "_TO_")
	if !ok {
		return ""
	}
	a, okA := convTypeNamed(from)
	b, okB := convTypeNamed(to)
	if !okA || !okB {
		return ""
	}
	if a.Name == b.Name {
		return fmt.Sprintf(" — the value is already a %s", a.Name)
	}
	rl, bits := a, b
	if b.class == ccReal {
		rl, bits = b, a
	}
	if rl.class == ccReal && bits.bits {
		same := "DWORD"
		if rl.width == 64 {
			same = "LWORD"
		}
		num := "U" + map[string]string{"BYTE": "SINT", "WORD": "INT", "DWORD": "DINT", "LWORD": "LINT"}[bits.Name]
		if a.class == ccReal {
			return fmt.Sprintf(" — IEC 61131-3 converts %s only to %s, as its bit pattern (%s_TO_%s); for the number, rounded, use %s_TO_%s", rl.Name, same, rl.Name, same, rl.Name, num)
		}
		return fmt.Sprintf(" — IEC 61131-3 converts %s only from %s, as its bit pattern (%s_TO_%s); for the number, use %s_TO_%s", rl.Name, same, same, rl.Name, num, rl.Name)
	}
	return ""
}

// convert is the one conversion routine every generated function calls.
func convert(name string, a, b convType, v Value) (Value, error) {
	switch b.class {
	case ccBool:
		switch a.class {
		case ccBool:
			return BoolVal(v.B), nil
		case ccInt:
			return BoolVal(fitInt(v.I, a) != 0), nil
		case ccTime:
			return BoolVal(v.I != 0), nil
		case ccReal:
			return BoolVal(v.F != 0), nil
		case ccString:
			switch strings.ToUpper(strings.TrimSpace(v.S)) {
			case "TRUE", "1", "BOOL#TRUE":
				return BoolVal(true), nil
			case "FALSE", "0", "BOOL#FALSE":
				return BoolVal(false), nil
			}
			return Value{}, fmt.Errorf("%s: %q is not TRUE/FALSE", name, v.S)
		}
	case ccInt:
		switch a.class {
		case ccBool:
			return boolInt(v.B), nil
		case ccInt:
			return IntVal(fitInt(fitInt(v.I, a), b)), nil
		case ccTime:
			return IntVal(fitInt(v.I, b)), nil
		case ccReal:
			if b.bits {
				// Same width (convAllowed): the IEEE 754 bit pattern.
				if b.width == 32 {
					return IntVal(int64(math.Float32bits(float32(v.F)))), nil
				}
				return IntVal(int64(math.Float64bits(v.F))), nil
			}
			return realToInt(name, b, v.F, math.RoundToEven)
		case ccString:
			return parseIntInto(name, b, v.S)
		}
	case ccReal:
		switch a.class {
		case ccBool:
			if v.B {
				return RealVal(1), nil
			}
			return RealVal(0), nil
		case ccInt:
			if a.bits {
				// DWORD_TO_REAL / LWORD_TO_LREAL: the bit pattern back.
				var f float64
				if a.width == 32 {
					f = float64(math.Float32frombits(uint32(v.I)))
				} else {
					f = math.Float64frombits(uint64(v.I))
				}
				if math.IsNaN(f) || math.IsInf(f, 0) {
					return Value{}, fmt.Errorf("%s: 16#%X is not a finite %s (NaN or infinity)", name, uint64(fitInt(v.I, a)), b.Name)
				}
				return RealVal(f), nil
			}
			x := fitInt(v.I, a)
			if a.width == 64 && !a.signed {
				return RealVal(float64(uint64(x))), nil
			}
			return RealVal(float64(x)), nil
		case ccTime:
			return RealVal(float64(v.I)), nil
		case ccReal:
			return RealVal(v.F), nil
		case ccString:
			s := strings.ReplaceAll(strings.TrimSpace(v.S), "_", "")
			f, err := strconv.ParseFloat(s, 64)
			if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
				return Value{}, fmt.Errorf("%s: %q is not a number", name, v.S)
			}
			return RealVal(f), nil
		}
	case ccTime:
		switch a.class {
		case ccBool:
			if v.B {
				return TimeVal(1), nil
			}
			return TimeVal(0), nil
		case ccInt:
			return TimeVal(fitInt(v.I, a)), nil
		case ccTime:
			return TimeVal(v.I), nil
		case ccReal:
			r := math.RoundToEven(v.F)
			if math.IsNaN(r) || r < -(1<<63) || r >= 1<<63 {
				return Value{}, fmt.Errorf("%s: %v is out of range for %s", name, v.F, b.Name)
			}
			return TimeVal(int64(r)), nil
		case ccString:
			ms, err := ParseDuration(v.S)
			if err != nil {
				return Value{}, fmt.Errorf("%s: %q is not a duration (T#1m30s, 90s, 500ms)", name, v.S)
			}
			return TimeVal(ms), nil
		}
	case ccString:
		switch a.class {
		case ccBool:
			if v.B {
				return StringVal("TRUE"), nil
			}
			return StringVal("FALSE"), nil
		case ccInt:
			x := fitInt(v.I, a)
			if !a.signed {
				return StringVal(strconv.FormatUint(uint64(x), 10)), nil
			}
			return StringVal(strconv.FormatInt(x, 10)), nil
		case ccTime:
			prefix := "T#"
			if a.Name == "LTIME" {
				prefix = "LTIME#"
			}
			return StringVal(prefix + strconv.FormatInt(v.I, 10) + "ms"), nil
		case ccReal:
			return StringVal(strconv.FormatFloat(v.F, 'g', -1, 64)), nil
		case ccString:
			return StringVal(v.S), nil
		}
	}
	return Value{}, fmt.Errorf("%s: cannot convert %s to %s", name, a.Name, b.Name)
}

func boolInt(b bool) Value {
	if b {
		return IntVal(1)
	}
	return IntVal(0)
}

// fitInt wraps v into the integer type t: its low t.width bits, read signed
// or unsigned. A 64-bit type keeps the int64 as is (ULINT and LWORD values
// at or above 2⁶³ are held as their two's-complement bit pattern).
func fitInt(v int64, t convType) int64 {
	if t.class != ccInt || t.width >= 64 || t.width == 0 {
		return v
	}
	mask := uint64(1)<<t.width - 1
	u := uint64(v) & mask
	if t.signed && u&(uint64(1)<<(t.width-1)) != 0 {
		return int64(u | ^mask)
	}
	return int64(u)
}

// intRange is t's value range as float64 bounds [lo, hiExcl).
func intRange(t convType) (lo, hiExcl float64) {
	if t.signed {
		return -math.Ldexp(1, t.width-1), math.Ldexp(1, t.width-1)
	}
	return 0, math.Ldexp(1, t.width)
}

// intRangeText is t's range for a message: SINT (-128..127).
func intRangeText(t convType) string {
	if t.signed {
		hi := uint64(1)<<(t.width-1) - 1
		return fmt.Sprintf("%s (-%d..%d)", t.Name, hi+1, hi)
	}
	if t.width == 64 {
		return t.Name + " (0..18446744073709551615)"
	}
	return fmt.Sprintf("%s (0..%d)", t.Name, uint64(1)<<t.width-1)
}

// realToInt rounds f (RoundToEven for X_TO_Y, Trunc for TRUNC) and stores
// it as integer type t; a REAL the type cannot hold faults.
func realToInt(name string, t convType, f float64, round func(float64) float64) (Value, error) {
	r := round(f)
	lo, hi := intRange(t)
	if math.IsNaN(r) || r < lo || r >= hi {
		return Value{}, fmt.Errorf("%s: %v is out of range for %s", name, f, intRangeText(t))
	}
	if r >= 1<<63 { // ULINT above LINT's range: keep the bit pattern
		return IntVal(int64(uint64(r))), nil
	}
	return IntVal(int64(r)), nil
}

// parseIntInto reads s as an integer literal — decimal or base-prefixed
// (2#, 8#, 16#), `_` separators allowed, optionally signed — and stores it
// as t. Text that is no integer, or one t cannot hold, faults.
func parseIntInto(name string, t convType, s string) (Value, error) {
	txt := strings.ReplaceAll(strings.TrimSpace(s), "_", "")
	neg := false
	if strings.HasPrefix(txt, "-") || strings.HasPrefix(txt, "+") {
		neg = txt[0] == '-'
		txt = txt[1:]
	}
	base := 10
	if b, digits, ok := strings.Cut(txt, "#"); ok {
		switch b {
		case "2":
			base = 2
		case "8":
			base = 8
		case "16":
			base = 16
		default:
			return Value{}, fmt.Errorf("%s: %q is not an integer", name, s)
		}
		txt = digits
	}
	mag, err := strconv.ParseUint(txt, base, 64)
	if err != nil {
		if ne, ok := err.(*strconv.NumError); ok && ne.Err == strconv.ErrRange {
			return Value{}, fmt.Errorf("%s: %q is out of range for %s", name, s, intRangeText(t))
		}
		return Value{}, fmt.Errorf("%s: %q is not an integer", name, s)
	}
	// The largest magnitude t holds on each side, as a uint64.
	var maxPos, maxNeg uint64
	switch {
	case t.signed:
		maxPos, maxNeg = uint64(1)<<(t.width-1)-1, uint64(1)<<(t.width-1)
	case t.width == 64:
		maxPos = math.MaxUint64
	default:
		maxPos = uint64(1)<<t.width - 1
	}
	if neg && mag > maxNeg || !neg && mag > maxPos {
		return Value{}, fmt.Errorf("%s: %q is out of range for %s", name, s, intRangeText(t))
	}
	if neg {
		return IntVal(int64(-mag)), nil // two's complement: -(2^63) wraps to MinInt64
	}
	return IntVal(int64(mag)), nil
}
