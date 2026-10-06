package ir

import (
	"fmt"
	"math"
)

// BuiltinFn implements a stateless ST/IEC built-in function. It receives
// already-coerced argument values (the lowering pass injects the right
// implicit conversions) and returns a single result.
type BuiltinFn func(args []Value) (Value, error)

// BuiltinSig describes a stateless built-in function. The lowering pass
// uses it both to type-check the call and to record Fn on the IR Call
// node so the VM can dispatch without a map lookup per scan.
//
// Coerce, when non-nil, runs at lowering time on the parsed arg types
// and returns the function's result type. It exists for builtins like
// MIN/MAX/ABS whose result depends on operand kinds.
type BuiltinSig struct {
	Name     string
	Params   []*Type // formal parameter types (nil entry means "any numeric")
	Result   *Type   // fixed result type, or nil if Coerce computes it
	Variadic bool    // last param repeats
	Coerce   func(argTypes []*Type) (*Type, error)
	Fn       BuiltinFn

	// Resolve, when non-nil, replaces Coerce for an overloaded function
	// whose implementation depends on the argument types, not only its
	// result (TO_REAL of a STRING parses, of a ULINT reads it unsigned): it
	// returns the result type and the Fn to call for these arguments.
	Resolve func(argTypes []*Type) (*Type, BuiltinFn, error)
	// Generic names the parameter type a nil Params entry accepts, for
	// signature help ("ANY_ELEMENTARY" for TO_<type>); "" means ANY_NUM.
	Generic string
}

// Builtins holds every stateless function the IR knows about. The
// lowering pass consults it; tests register more via RegisterBuiltin.
var Builtins = map[string]BuiltinSig{}

// RegisterBuiltin adds or replaces a builtin entry. Callers that wire up
// new IEC functions go through here so name resolution stays centralised.
func RegisterBuiltin(sig BuiltinSig) {
	Builtins[sig.Name] = sig
}

func init() {
	registerArithBuiltins()
}

func registerArithBuiltins() {
	RegisterBuiltin(BuiltinSig{
		Name:   "ABS",
		Params: []*Type{nil},
		Coerce: func(t []*Type) (*Type, error) {
			if len(t) != 1 || !t[0].IsNumeric() {
				return nil, fmt.Errorf("ABS expects one numeric argument")
			}
			return t[0], nil
		},
		Fn: func(args []Value) (Value, error) {
			x := args[0]
			if x.Kind == TypeReal {
				return RealVal(math.Abs(x.F)), nil
			}
			if x.I < 0 {
				return Value{Kind: x.Kind, I: -x.I}, nil
			}
			return x, nil
		},
	})
	RegisterBuiltin(BuiltinSig{
		Name:     "MIN",
		Params:   []*Type{nil, nil},
		Variadic: true,
		Coerce:   numericResultOfArgs("MIN"),
		Fn: func(args []Value) (Value, error) {
			best := args[0]
			for _, a := range args[1:] {
				if compareValues(a, best) < 0 {
					best = a
				}
			}
			return best, nil
		},
	})
	RegisterBuiltin(BuiltinSig{
		Name:     "MAX",
		Params:   []*Type{nil, nil},
		Variadic: true,
		Coerce:   numericResultOfArgs("MAX"),
		Fn: func(args []Value) (Value, error) {
			best := args[0]
			for _, a := range args[1:] {
				if compareValues(a, best) > 0 {
					best = a
				}
			}
			return best, nil
		},
	})
	RegisterBuiltin(BuiltinSig{
		Name:   "LIMIT",
		Params: []*Type{nil, nil, nil},
		Coerce: func(t []*Type) (*Type, error) {
			if len(t) != 3 {
				return nil, fmt.Errorf("LIMIT expects (MN, IN, MX)")
			}
			return numericResultOfArgs("LIMIT")(t)
		},
		Fn: func(args []Value) (Value, error) {
			lo, x, hi := args[0], args[1], args[2]
			if compareValues(x, lo) < 0 {
				return lo, nil
			}
			if compareValues(x, hi) > 0 {
				return hi, nil
			}
			return x, nil
		},
	})
	RegisterBuiltin(BuiltinSig{
		Name:   "SQRT",
		Params: []*Type{RealT},
		Result: RealT,
		Fn: func(args []Value) (Value, error) {
			return RealVal(math.Sqrt(asFloat(args[0]))), nil
		},
	})
	RegisterBuiltin(BuiltinSig{
		Name:   "LN",
		Params: []*Type{RealT},
		Result: RealT,
		Fn: func(args []Value) (Value, error) {
			return RealVal(math.Log(asFloat(args[0]))), nil
		},
	})
	RegisterBuiltin(BuiltinSig{
		Name:   "LOG",
		Params: []*Type{RealT},
		Result: RealT,
		Fn: func(args []Value) (Value, error) {
			return RealVal(math.Log10(asFloat(args[0]))), nil
		},
	})
	RegisterBuiltin(BuiltinSig{
		Name:   "EXP",
		Params: []*Type{RealT},
		Result: RealT,
		Fn: func(args []Value) (Value, error) {
			return RealVal(math.Exp(asFloat(args[0]))), nil
		},
	})
	RegisterBuiltin(BuiltinSig{
		Name:   "SIN",
		Params: []*Type{RealT},
		Result: RealT,
		Fn:     func(args []Value) (Value, error) { return RealVal(math.Sin(asFloat(args[0]))), nil },
	})
	RegisterBuiltin(BuiltinSig{
		Name:   "COS",
		Params: []*Type{RealT},
		Result: RealT,
		Fn:     func(args []Value) (Value, error) { return RealVal(math.Cos(asFloat(args[0]))), nil },
	})
	RegisterBuiltin(BuiltinSig{
		Name:   "TAN",
		Params: []*Type{RealT},
		Result: RealT,
		Fn:     func(args []Value) (Value, error) { return RealVal(math.Tan(asFloat(args[0]))), nil },
	})
}

// numericResultOfArgs is the standard "promote to REAL if any arg is
// REAL, else INT (or TIME if all TIME)" rule used by MIN/MAX/LIMIT.
func numericResultOfArgs(name string) func([]*Type) (*Type, error) {
	return func(ts []*Type) (*Type, error) {
		if len(ts) == 0 {
			return nil, fmt.Errorf("%s requires at least one argument", name)
		}
		anyReal := false
		allTime := true
		for _, t := range ts {
			if !t.IsNumeric() {
				return nil, fmt.Errorf("%s arg of type %s is not numeric", name, t)
			}
			if t.Kind == TypeReal {
				anyReal = true
			}
			if t.Kind != TypeTime {
				allTime = false
			}
		}
		if anyReal {
			return RealT, nil
		}
		if allTime {
			return TimeT, nil
		}
		return IntT, nil
	}
}

// compareValues returns -1/0/+1 like Go's three-way comparison, treating
// REAL and INT as comparable through float promotion.
func compareValues(a, b Value) int {
	if a.Kind == TypeReal || b.Kind == TypeReal {
		af, bf := asFloat(a), asFloat(b)
		switch {
		case af < bf:
			return -1
		case af > bf:
			return 1
		}
		return 0
	}
	switch {
	case a.I < b.I:
		return -1
	case a.I > b.I:
		return 1
	}
	return 0
}

// CompareValues is compareValues for the lowering pass (CASE label
// overlap): -1/0/+1, INT and REAL compared through float promotion.
func CompareValues(a, b Value) int { return compareValues(a, b) }
