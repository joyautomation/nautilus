package hw

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"time"

	"github.com/joyautomation/nautilus/lang/ir"
)

// ErrStatic is Invert's answer for a const or derived member: it reads no
// wire value, so there is nothing to serve.
var ErrStatic = errors.New("static member: no wire value")

// Invert is Apply run backwards: the wire value a device would have to
// report for the member to read v. It is what a stand-in serving a plant
// simulation answers (`naut snmp serve --from`), so the manifest is the one
// description of a device in both directions: Apply(Invert(v)) == v.
//
// cur is the wire value the stand-in holds now (from its recording). If it
// already reads v it is the answer — a tie (a map with two keys to one
// value, an eq: that is false, a scaled INT that rounds) keeps what the
// device really said. Otherwise it picks the wire kind of the answer: an
// INTEGER stays an INTEGER, a Counter64 a counter.
//
// A rate: member inverts to the per-second rate of its counter, as a
// RawFloat; the caller integrates it into the counter it serves (a rate
// has no single wire value). A const or derived member is ErrStatic.
func (b Binding) Invert(f Field, v ir.Value, cur Raw) (Raw, error) {
	if b.IsStatic() {
		return Raw{}, ErrStatic
	}
	if !b.Rate {
		if got, ok, err := b.Apply(f, cur, nil, time.Time{}); err == nil && ok && sameValue(got, v) {
			return cur, nil
		}
	}
	if b.Eq != nil {
		if v.Kind != ir.TypeBool {
			return Raw{}, fmt.Errorf("member %s: eq: wants a BOOL, got %s", f.Name, v.Kind)
		}
		eq := keyOf(b.Eq)
		if v.B {
			return rawLike(cur, eq)
		}
		// The wire value that is "not eq". The status enums eq: is used
		// on name up(1) and down(2), true(1) and false(2) — IF-MIB
		// ifOper/AdminStatus, SNMPv2-TC TruthValue — so the next integer
		// is the one a device reports; a bool flips.
		switch x := b.Eq.(type) {
		case bool:
			return rawLike(cur, strconv.FormatBool(!x))
		case string:
			if not, ok := complements[x]; ok {
				return rawLike(cur, not)
			}
			return Raw{}, fmt.Errorf("member %s: eq %q: no wire value is known to read false", f.Name, x)
		}
		if n, err := strconv.ParseInt(eq, 10, 64); err == nil {
			return rawLike(cur, strconv.FormatInt(n+1, 10))
		}
		return Raw{}, fmt.Errorf("member %s: eq %s: no wire value is known to read false", f.Name, eq)
	}
	if b.Map != nil {
		var hits []string
		for k, mv := range b.Map {
			want, err := coerce(mv, f)
			if err == nil && sameValue(want, v) {
				hits = append(hits, k)
			}
		}
		if len(hits) == 0 {
			return Raw{}, fmt.Errorf("member %s: no map key reads %s", f.Name, valueString(v))
		}
		sortKeys(hits)
		return rawLike(cur, hits[0])
	}
	switch v.Kind {
	case ir.TypeString:
		return rawLike(cur, v.S)
	case ir.TypeBool:
		if b.Scale != 0 || b.Offset != 0 || b.Rate {
			return Raw{}, fmt.Errorf("member %s: a BOOL with scale/offset/rate", f.Name)
		}
		if cur.Kind == RawBool || cur.Kind == RawString {
			return rawLike(cur, strconv.FormatBool(v.B))
		}
		if v.B {
			return rawLike(cur, "1")
		}
		return rawLike(cur, "0")
	case ir.TypeInt, ir.TypeReal:
	default:
		return Raw{}, fmt.Errorf("member %s: cannot serve a %s", f.Name, v.Kind)
	}
	fv := v.F
	if v.Kind == ir.TypeInt {
		fv = float64(v.I)
	}
	scale := b.Scale
	if scale == 0 {
		scale = 1
	}
	fv = (fv - b.Offset) / scale
	if b.Rate {
		return RawFloatVal(fv), nil
	}
	return rawLike(cur, strconv.FormatFloat(fv, 'g', -1, 64))
}

// complements are the string enums eq: is used on, with the value a device
// reports for "not that": DMTF PowerState and State, link and presence.
var complements = map[string]string{
	"On": "Off", "Off": "On",
	"Enabled": "Disabled", "Disabled": "Enabled",
	"LinkUp": "LinkDown", "LinkDown": "LinkUp",
	"Up": "Down", "Down": "Up",
	"Present": "Absent", "Absent": "Present",
}

// rawLike parses key into cur's kind: the wire value a device of this
// shape would send. Integers round (a scaled value rarely lands on one);
// an unsigned wire value cannot go below zero.
func rawLike(cur Raw, key string) (Raw, error) {
	switch cur.Kind {
	case RawString:
		return RawStringVal(key), nil
	case RawBool:
		b, err := strconv.ParseBool(key)
		if err != nil {
			return Raw{}, fmt.Errorf("wire value %q is not a bool", key)
		}
		return RawBoolVal(b), nil
	}
	f, err := strconv.ParseFloat(key, 64)
	if err != nil {
		if b, berr := strconv.ParseBool(key); berr == nil {
			f = 0
			if b {
				f = 1
			}
		} else {
			return Raw{}, fmt.Errorf("wire value %q is not a number", key)
		}
	}
	switch cur.Kind {
	case RawInt:
		return RawIntVal(int64(math.Round(f))), nil
	case RawUint:
		return RawUintVal(uint64(math.Max(0, math.Round(f)))), nil
	}
	return RawFloatVal(f), nil
}

func sameValue(a, b ir.Value) bool {
	if a.Kind != b.Kind {
		if a.Kind == ir.TypeInt && b.Kind == ir.TypeReal {
			return float64(a.I) == b.F
		}
		if a.Kind == ir.TypeReal && b.Kind == ir.TypeInt {
			return a.F == float64(b.I)
		}
		return false
	}
	switch a.Kind {
	case ir.TypeBool:
		return a.B == b.B
	case ir.TypeInt:
		return a.I == b.I
	case ir.TypeReal:
		return a.F == b.F
	case ir.TypeString:
		return a.S == b.S
	}
	return false
}

func valueString(v ir.Value) string {
	switch v.Kind {
	case ir.TypeBool:
		return strconv.FormatBool(v.B)
	case ir.TypeInt:
		return strconv.FormatInt(v.I, 10)
	case ir.TypeReal:
		return strconv.FormatFloat(v.F, 'g', -1, 64)
	}
	return strconv.Quote(v.S)
}

// sortKeys orders map keys numerically where they are numbers, so the
// lowest wire code wins a tie ("2" before "10").
func sortKeys(keys []string) {
	sort.Slice(keys, func(i, j int) bool {
		a, aerr := strconv.ParseFloat(keys[i], 64)
		b, berr := strconv.ParseFloat(keys[j], 64)
		if aerr == nil && berr == nil && a != b {
			return a < b
		}
		return keys[i] < keys[j]
	})
}
