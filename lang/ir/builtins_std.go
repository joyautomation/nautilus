package ir

import (
	"fmt"
	"math"
	"strings"
)

// The rest of the IEC 61131-3 standard-function library: selection
// (SEL/MUX), bit shifts and rotates, the remaining numeric functions, and
// the character-string functions. Registered alongside builtins.go's set —
// one table, both languages (ST and FBD) pick them up.
//
// Deviations from the letter of the spec, chosen deliberately:
//   - Bit shifts and rotates act on the first operand's declared width
//     (ROL of a WORD rotates over 16 bits); an undeclared width is 64.
//   - String position/length arguments out of range CLAMP to the valid
//     range instead of faulting the scan; MUX with K out of range still
//     faults (selecting nothing is a logic error, not a boundary case).

func init() {
	registerSelectionBuiltins()
	registerBitBuiltins()
	registerNumericBuiltins()
	registerStringBuiltins()
}

func registerSelectionBuiltins() {
	RegisterBuiltin(BuiltinSig{
		Name:   "SEL",
		Params: []*Type{BoolT, nil, nil},
		Coerce: func(t []*Type) (*Type, error) {
			if len(t) != 3 {
				return nil, fmt.Errorf("SEL expects (G, IN0, IN1)")
			}
			if t[0].Kind != TypeBool {
				return nil, fmt.Errorf("SEL selector G must be BOOL, got %s", t[0])
			}
			return commonType("SEL", t[1:])
		},
		Fn: func(args []Value) (Value, error) {
			if args[0].B {
				return args[2], nil
			}
			return args[1], nil
		},
	})
	RegisterBuiltin(BuiltinSig{
		Name:     "MUX",
		Params:   []*Type{IntT, nil},
		Variadic: true,
		Coerce: func(t []*Type) (*Type, error) {
			if len(t) < 3 {
				return nil, fmt.Errorf("MUX expects (K, IN0, IN1, ...)")
			}
			if !t[0].IsNumeric() {
				return nil, fmt.Errorf("MUX selector K must be an integer, got %s", t[0])
			}
			return commonType("MUX", t[1:])
		},
		Fn: func(args []Value) (Value, error) {
			k := args[0].I
			ins := args[1:]
			if k < 0 || int(k) >= len(ins) {
				return Value{}, fmt.Errorf("MUX selector %d out of range 0..%d", k, len(ins)-1)
			}
			return ins[k], nil
		},
	})
}

// commonType is the SEL/MUX input-agreement rule: identical kinds pass
// through; mixed numerics promote like MIN/MAX; anything else must match.
func commonType(name string, ts []*Type) (*Type, error) {
	same := true
	for _, t := range ts[1:] {
		if t.Kind != ts[0].Kind {
			same = false
		}
	}
	if same {
		// Integers: the arithmetic common type, computed, so a selected
		// value is stored at its target's width like any result (#782): a
		// ladder assignment is SEL(power, old, new).
		if ts[0].Kind == TypeInt && ts[0].Enum == nil {
			t := ts[0]
			for _, u := range ts[1:] {
				if u.Enum != nil {
					return ts[0], nil
				}
				t = CommonInt(t, u)
			}
			return t, nil
		}
		return ts[0], nil
	}
	allNumeric := true
	for _, t := range ts {
		if !t.IsNumeric() {
			allNumeric = false
		}
	}
	if allNumeric {
		return numericResultOfArgs(name)(ts)
	}
	return nil, fmt.Errorf("%s inputs must share a type", name)
}

func registerBitBuiltins() {
	// A shift or rotate acts on the first operand's declared width (#782):
	// ROL of a WORD rotates over 16 bits, as IEC 61131-3 has it, and a
	// result goes back into that width. An operand of no declared width (a
	// literal, an arithmetic result) is 64 bits, the VM's own.
	shift := func(name string, fn func(v uint64, n, w uint) uint64) {
		run := func(w uint) BuiltinFn {
			mask := ^uint64(0)
			if w < 64 {
				mask = uint64(1)<<w - 1
			}
			return func(args []Value) (Value, error) {
				n := args[1].I
				if n < 0 {
					return Value{}, fmt.Errorf("%s shift count %d is negative", name, n)
				}
				u := fn(uint64(args[0].I)&mask, uint(n), w) & mask
				if w < 64 && u&(uint64(1)<<(w-1)) != 0 {
					u |= ^mask // a signed type's sign, from its top bit
				}
				return IntVal(int64(u)), nil
			}
		}
		RegisterBuiltin(BuiltinSig{
			Name:    name,
			Params:  []*Type{nil, IntT},
			Result:  IntT,
			Generic: "ANY_BIT",
			Resolve: func(ts []*Type) (*Type, BuiltinFn, error) {
				if len(ts) != 2 || !ts[0].IsInteger() || !ts[1].IsInteger() {
					return nil, nil, fmt.Errorf("%s takes an integer and a count", name)
				}
				t := ts[0]
				if t.Name == "" || t.Enum != nil {
					return IntT, run(64), nil
				}
				w := uint(t.BitWidth())
				if c, _ := intClass(t.Name); c == 'u' || c == 'b' {
					return t, runUnsigned(name, fn, w), nil
				}
				return t, run(w), nil
			},
			Fn: run(64),
		})
	}
	shift("SHL", func(v uint64, n, w uint) uint64 {
		if n >= w {
			return 0
		}
		return v << n
	})
	shift("SHR", func(v uint64, n, w uint) uint64 { // logical: zero-fill
		if n >= w {
			return 0
		}
		return v >> n
	})
	shift("ROL", func(v uint64, n, w uint) uint64 {
		n %= w
		if n == 0 {
			return v
		}
		return v<<n | v>>(w-n)
	})
	shift("ROR", func(v uint64, n, w uint) uint64 {
		n %= w
		if n == 0 {
			return v
		}
		return v>>n | v<<(w-n)
	})
}

// runUnsigned is a shift over w bits whose result stays unsigned (a WORD,
// a UINT).
func runUnsigned(name string, fn func(v uint64, n, w uint) uint64, w uint) BuiltinFn {
	mask := ^uint64(0)
	if w < 64 {
		mask = uint64(1)<<w - 1
	}
	return func(args []Value) (Value, error) {
		n := args[1].I
		if n < 0 {
			return Value{}, fmt.Errorf("%s shift count %d is negative", name, n)
		}
		return IntVal(int64(fn(uint64(args[0].I)&mask, uint(n), w) & mask)), nil
	}
}

func registerNumericBuiltins() {
	real1 := func(name string, fn func(float64) float64) {
		RegisterBuiltin(BuiltinSig{
			Name:   name,
			Params: []*Type{RealT},
			Result: RealT,
			Fn:     func(args []Value) (Value, error) { return RealVal(fn(asFloat(args[0]))), nil },
		})
	}
	real1("ASIN", math.Asin)
	real1("ACOS", math.Acos)
	real1("ATAN", math.Atan)
	RegisterBuiltin(BuiltinSig{
		Name:   "ATAN2",
		Params: []*Type{RealT, RealT},
		Result: RealT,
		Fn: func(args []Value) (Value, error) {
			return RealVal(math.Atan2(asFloat(args[0]), asFloat(args[1]))), nil
		},
	})
	RegisterBuiltin(BuiltinSig{
		Name:   "EXPT",
		Params: []*Type{RealT, RealT},
		Result: RealT,
		Fn: func(args []Value) (Value, error) {
			return RealVal(math.Pow(asFloat(args[0]), asFloat(args[1]))), nil
		},
	})
	RegisterBuiltin(BuiltinSig{
		Name:   "TRUNC",
		Params: []*Type{RealT},
		Result: IntT,
		Fn: func(args []Value) (Value, error) {
			// Toward zero, into the 64-bit integer an undeclared INT is;
			// a REAL with no integer value (NaN, ±Inf, beyond 64 bits)
			// faults, as TRUNC_<int> does (conversions.go).
			lint, _ := convTypeNamed("LINT")
			return realToInt("TRUNC", lint, asFloat(args[0]), math.Trunc)
		},
	})
}

// IEC strings are 1-indexed; positions and lengths clamp to the valid
// range (see the header note). Operations are byte-based, matching the
// spec's single-byte STRING.
func registerStringBuiltins() {
	RegisterBuiltin(BuiltinSig{
		Name:   "LEN",
		Params: []*Type{StringT},
		Result: IntT,
		Fn:     func(args []Value) (Value, error) { return IntVal(int64(len(args[0].S))), nil },
	})
	RegisterBuiltin(BuiltinSig{
		Name:   "LEFT",
		Params: []*Type{StringT, IntT},
		Result: StringT,
		Fn: func(args []Value) (Value, error) {
			s := args[0].S
			return StringVal(s[:clampN(args[1].I, len(s))]), nil
		},
	})
	RegisterBuiltin(BuiltinSig{
		Name:   "RIGHT",
		Params: []*Type{StringT, IntT},
		Result: StringT,
		Fn: func(args []Value) (Value, error) {
			s := args[0].S
			return StringVal(s[len(s)-clampN(args[1].I, len(s)):]), nil
		},
	})
	RegisterBuiltin(BuiltinSig{
		Name:   "MID",
		Params: []*Type{StringT, IntT, IntT},
		Result: StringT,
		Fn: func(args []Value) (Value, error) {
			s := args[0].S
			start := clampN(args[2].I-1, len(s)) // P is 1-based
			n := clampN(args[1].I, len(s)-start)
			return StringVal(s[start : start+n]), nil
		},
	})
	RegisterBuiltin(BuiltinSig{
		Name:     "CONCAT",
		Params:   []*Type{StringT, StringT},
		Result:   StringT,
		Variadic: true,
		Fn: func(args []Value) (Value, error) {
			// Size the result once: exactly one allocation, the string.
			n := 0
			for _, a := range args {
				n += len(a.S)
			}
			var b strings.Builder
			b.Grow(n)
			for _, a := range args {
				b.WriteString(a.S)
			}
			return StringVal(b.String()), nil
		},
	})
	RegisterBuiltin(BuiltinSig{
		Name:   "INSERT",
		Params: []*Type{StringT, StringT, IntT},
		Result: StringT,
		Fn: func(args []Value) (Value, error) {
			s, ins := args[0].S, args[1].S
			p := clampN(args[2].I, len(s)) // insert AFTER position P; P=0 prepends
			return StringVal(s[:p] + ins + s[p:]), nil
		},
	})
	RegisterBuiltin(BuiltinSig{
		Name:   "DELETE",
		Params: []*Type{StringT, IntT, IntT},
		Result: StringT,
		Fn: func(args []Value) (Value, error) {
			s := args[0].S
			start := clampN(args[2].I-1, len(s))
			n := clampN(args[1].I, len(s)-start)
			return StringVal(s[:start] + s[start+n:]), nil
		},
	})
	RegisterBuiltin(BuiltinSig{
		Name:   "REPLACE",
		Params: []*Type{StringT, StringT, IntT, IntT},
		Result: StringT,
		Fn: func(args []Value) (Value, error) {
			s, rep := args[0].S, args[1].S
			start := clampN(args[3].I-1, len(s))
			n := clampN(args[2].I, len(s)-start)
			return StringVal(s[:start] + rep + s[start+n:]), nil
		},
	})
	RegisterBuiltin(BuiltinSig{
		Name:   "FIND",
		Params: []*Type{StringT, StringT},
		Result: IntT,
		Fn: func(args []Value) (Value, error) {
			if args[1].S == "" {
				return IntVal(0), nil
			}
			return IntVal(int64(strings.Index(args[0].S, args[1].S) + 1)), nil
		},
	})
}

// clampN clamps a length/position argument into [0, max].
func clampN(n int64, max int) int {
	if n < 0 {
		return 0
	}
	if n > int64(max) {
		return max
	}
	return int(n)
}
