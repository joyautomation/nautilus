package ir

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// The rest of the IEC 61131-3 standard-function library: selection
// (SEL/MUX), bit shifts and rotates, the remaining numeric functions, and
// the character-string functions. Registered alongside builtins.go's set —
// one table, both languages (ST and FBD) pick them up.
//
// Deviations from the letter of the spec, chosen deliberately:
//   - Bit operations act on the VM's 64-bit integer values (the IR does
//     not track declared bit widths), so ROL/ROR rotate over 64 bits.
//   - String position/length arguments out of range CLAMP to the valid
//     range instead of faulting the scan; MUX with K out of range still
//     faults (selecting nothing is a logic error, not a boundary case).

func init() {
	registerSelectionBuiltins()
	registerBitBuiltins()
	registerNumericBuiltins()
	registerStringBuiltins()
	registerStringConversions()
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
	shift := func(name string, fn func(v uint64, n uint) uint64) {
		RegisterBuiltin(BuiltinSig{
			Name:   name,
			Params: []*Type{IntT, IntT},
			Result: IntT,
			Fn: func(args []Value) (Value, error) {
				n := args[1].I
				if n < 0 {
					return Value{}, fmt.Errorf("%s shift count %d is negative", name, n)
				}
				return IntVal(int64(fn(uint64(args[0].I), uint(n)))), nil
			},
		})
	}
	shift("SHL", func(v uint64, n uint) uint64 {
		if n >= 64 {
			return 0
		}
		return v << n
	})
	shift("SHR", func(v uint64, n uint) uint64 { // logical: zero-fill
		if n >= 64 {
			return 0
		}
		return v >> n
	})
	shift("ROL", func(v uint64, n uint) uint64 {
		n %= 64
		return v<<n | v>>(64-n)
	})
	shift("ROR", func(v uint64, n uint) uint64 {
		n %= 64
		return v>>n | v<<(64-n)
	})
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
			return IntVal(int64(math.Trunc(asFloat(args[0])))), nil
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

func registerStringConversions() {
	conv := []struct {
		name string
		from *Type
		to   *Type
		fn   BuiltinFn
	}{
		{"BOOL_TO_REAL", BoolT, RealT, func(a []Value) (Value, error) {
			if a[0].B {
				return RealVal(1), nil
			}
			return RealVal(0), nil
		}},
		{"REAL_TO_BOOL", RealT, BoolT, func(a []Value) (Value, error) { return BoolVal(a[0].F != 0), nil }},
		{"INT_TO_STRING", IntT, StringT, func(a []Value) (Value, error) {
			return StringVal(strconv.FormatInt(a[0].I, 10)), nil
		}},
		{"REAL_TO_STRING", RealT, StringT, func(a []Value) (Value, error) {
			return StringVal(strconv.FormatFloat(a[0].F, 'g', -1, 64)), nil
		}},
		{"BOOL_TO_STRING", BoolT, StringT, func(a []Value) (Value, error) {
			if a[0].B {
				return StringVal("TRUE"), nil
			}
			return StringVal("FALSE"), nil
		}},
		{"TIME_TO_STRING", TimeT, StringT, func(a []Value) (Value, error) {
			return StringVal("T#" + strconv.FormatInt(a[0].I, 10) + "ms"), nil
		}},
		{"STRING_TO_INT", StringT, IntT, func(a []Value) (Value, error) {
			v, err := strconv.ParseInt(strings.TrimSpace(a[0].S), 10, 64)
			if err != nil {
				return Value{}, fmt.Errorf("STRING_TO_INT: %q is not an integer", a[0].S)
			}
			return IntVal(v), nil
		}},
		{"STRING_TO_REAL", StringT, RealT, func(a []Value) (Value, error) {
			v, err := strconv.ParseFloat(strings.TrimSpace(a[0].S), 64)
			if err != nil {
				return Value{}, fmt.Errorf("STRING_TO_REAL: %q is not a number", a[0].S)
			}
			return RealVal(v), nil
		}},
		{"STRING_TO_BOOL", StringT, BoolT, func(a []Value) (Value, error) {
			switch strings.ToUpper(strings.TrimSpace(a[0].S)) {
			case "TRUE", "1":
				return BoolVal(true), nil
			case "FALSE", "0":
				return BoolVal(false), nil
			}
			return Value{}, fmt.Errorf("STRING_TO_BOOL: %q is not TRUE/FALSE", a[0].S)
		}},
	}
	for _, c := range conv {
		c := c
		RegisterBuiltin(BuiltinSig{
			Name:   c.name,
			Params: []*Type{c.from},
			Result: c.to,
			Fn:     c.fn,
		})
	}
}
