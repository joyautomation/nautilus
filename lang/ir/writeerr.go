package ir

import (
	"errors"
	"fmt"
	"math"
	"strings"
)

// A write from outside a program — an operator's HTTP write, an OPC UA
// client's, a test's given:, a retained restore — lands exactly as sent or
// is refused (#421). The refusals a protocol must tell apart carry one of
// these, so errors.Is maps them to its own codes (HTTP 422/400, OPC UA
// Bad_TypeMismatch/Bad_OutOfRange) while the message stays the descriptive
// one a person reads.
var (
	// ErrTypeMismatch: the value is not of a kind the target takes — text
	// for a REAL, a number for a BOOL, a fraction for an integer.
	ErrTypeMismatch = errors.New("type mismatch")
	// ErrOutOfRange: the kind is right but the target cannot hold it — an
	// integer outside its declared width, a NaN or infinite REAL, a TIME
	// with a fraction of a millisecond.
	ErrOutOfRange = errors.New("out of range")
)

// writeErr is a refusal with a kind (ErrTypeMismatch or ErrOutOfRange) and
// a message naming the target.
type writeErr struct {
	kind error
	msg  string
}

func (e *writeErr) Error() string { return e.msg }
func (e *writeErr) Unwrap() error { return e.kind }

func mismatchf(format string, a ...any) error {
	return &writeErr{ErrTypeMismatch, fmt.Sprintf(format, a...)}
}

func rangef(format string, a ...any) error {
	return &writeErr{ErrOutOfRange, fmt.Sprintf(format, a...)}
}

// unsigned64 reports whether t is ULINT or LWORD.
func unsigned64(t *Type) bool {
	if t == nil || t.Kind != TypeInt || t.Enum != nil || t.Name == "" {
		return false
	}
	c, ok := convTypeNamed(strings.ToUpper(t.Name))
	return ok && c.class == ccInt && c.width == 64 && !c.signed
}

// checkFits refuses a converted scalar its declared type cannot hold: an
// integer outside the width t names (every integer is an int64 in the
// store, so nothing else would stop an INT from holding 70000), and a NaN
// or infinite REAL (a NaN setpoint poisons every comparison the logic
// makes, #429). An enumeration, a computed or unnamed integer, and every
// other kind pass.
func checkFits(t *Type, v Value, sofar string) error {
	switch v.Kind {
	case TypeReal:
		if math.IsNaN(v.F) || math.IsInf(v.F, 0) {
			return rangef("%s: %v is not a number a REAL can hold", sofar, v.F)
		}
	case TypeInt:
		if t == nil || t.Kind != TypeInt || t.Enum != nil || t.Computed || t.Name == "" {
			return nil
		}
		c, ok := convTypeNamed(strings.ToUpper(t.Name))
		if !ok || c.class != ccInt || c.width >= 64 {
			return nil // LINT is the store; ULINT/LWORD hold any 64-bit pattern
		}
		lo, hi := intRange(c)
		if f := float64(v.I); f < lo || f >= hi {
			return rangef("%s: %d is outside %s", sofar, v.I, intRangeText(c))
		}
	}
	return nil
}

// wantErr is coerceLeaf's refusal of v for a target of kind k. A number
// for an integer or TIME is the right kind of thing: when it still did not
// convert it carries a fraction or is past int64, which for an integer is
// a type mismatch only if it has a fraction (a Double for an Int16 node),
// and for a TIME is always out of range (a fraction of a millisecond is
// beyond its resolution). Anything else is the wrong kind.
func wantErr(k TypeKind, v any, format string, a ...any) error {
	if f, isNum := toFloat(v); isNum {
		switch k {
		case TypeTime:
			return rangef(format, a...)
		case TypeInt:
			if f == math.Trunc(f) {
				return rangef(format, a...)
			}
		}
	}
	return mismatchf(format, a...)
}

// EnumMemberOnly refuses an integer written to an enumerated type t when no
// member has that value. EnumFromAny keeps such a number (unnamed), which
// is what TO_Mode(n), a field-bus delivery and a test's given: need; a
// write from a client is held to the members, so a SCADA screen cannot
// park a state machine on a state the program has no branch for. Nil for
// a non-enumerated t, a name (EnumFromAny judges it) or a member's value.
func EnumMemberOnly(t *Type, v any, sofar string) error {
	if t == nil || t.Enum == nil {
		return nil
	}
	if x, ok := v.(Value); ok {
		if x.Kind != TypeInt {
			return nil
		}
		v = x.I
	}
	i, ok := toInt(v)
	if !ok || t.Enum.NameOf(i) != "" {
		return nil
	}
	return rangef("%s: %d is not the value of any member of %s (%s)", sofar, i, t.Enum.Name, enumNames(t.Enum))
}
