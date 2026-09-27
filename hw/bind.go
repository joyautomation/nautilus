package hw

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/joyautomation/nautilus/lang/ir"
)

// Binding is the vocabulary a member binding shares across the three
// manifests — everything but the locator (an OID, a Redfish resource+path,
// a metric selector), which each protocol adds by embedding this struct.
// The YAML keys are the manifest keys; docs/design/it-drivers.md §6.1.
//
// Order of application, raw → member: Const short-circuits everything; Map
// then Eq turn an enum into a value; Rate turns a counter into a per-second
// rate; Scale and Offset re-scale; the result is coerced to the member's
// IEC kind. Derived is evaluated by Base over the tag's other members after
// every poll and takes no wire locator at all.
type Binding struct {
	// Const is a fixed value the generator knew at import time (a port's
	// Index, a sensor's Name) — delivered once, never polled.
	Const any `yaml:"const,omitempty"`
	// Map translates a wire value to the member's value by the wire
	// value's string form: {"1": true, "2": false} on ifAdminStatus,
	// {OK: 0, Warning: 1, Critical: 2} on a Redfish Status.Health.
	Map map[string]any `yaml:"map,omitempty"`
	// Eq makes a BOOL member: true when the wire value equals Eq.
	Eq any `yaml:"eq,omitempty"`
	// Scale and Offset: engineering = wire*Scale + Offset (Scale 0 = 1).
	Scale  float64 `yaml:"scale,omitempty"`
	Offset float64 `yaml:"offset,omitempty"`
	// Rate delivers the counter's per-second rate instead of its value;
	// Width (32|64, default 64) decides whether a step backwards is a wrap
	// or a reset (see Counter).
	Rate  bool `yaml:"rate,omitempty"`
	Width int  `yaml:"width,omitempty"`
	// ScanClass assigns the member to a poll class; "" is the default.
	ScanClass string `yaml:"scan-class,omitempty"`
	// Derived computes the member from its siblings (an Expr), instead of
	// polling anything: "AdminUp && !OperUp".
	Derived string `yaml:"derived,omitempty"`
}

// Validate checks a binding against the member it feeds, offline, so `naut
// check` refuses a manifest whose bindings could never produce the member.
func (b Binding) Validate(f Field) error {
	if b.Derived != "" {
		if b.Const != nil || b.Map != nil || b.Eq != nil || b.Rate || b.Scale != 0 || b.Offset != 0 {
			return fmt.Errorf("member %s: derived: takes no other binding keys", f.Name)
		}
		if _, err := ParseExpr(b.Derived); err != nil {
			return fmt.Errorf("member %s: %w", f.Name, err)
		}
		return nil
	}
	if b.Const != nil {
		if b.Map != nil || b.Eq != nil || b.Rate {
			return fmt.Errorf("member %s: const: takes no map/eq/rate", f.Name)
		}
		if _, err := coerce(b.Const, f); err != nil {
			return fmt.Errorf("member %s: const: %w", f.Name, err)
		}
	}
	if b.Eq != nil {
		if f.Kind != ir.TypeBool {
			return fmt.Errorf("member %s: eq: makes a BOOL, but the member is %s", f.Name, f.Kind)
		}
		if b.Map != nil {
			return fmt.Errorf("member %s: map: and eq: are alternatives", f.Name)
		}
	}
	for k, v := range b.Map {
		if _, err := coerce(v, f); err != nil {
			return fmt.Errorf("member %s: map[%s]: %w", f.Name, k, err)
		}
	}
	if b.Rate && f.Kind != ir.TypeReal && f.Kind != ir.TypeInt {
		return fmt.Errorf("member %s: rate: needs a numeric member, not %s", f.Name, f.Kind)
	}
	if b.Width != 0 && b.Width != 32 && b.Width != 64 {
		return fmt.Errorf("member %s: width: must be 32 or 64", f.Name)
	}
	if (b.Scale != 0 || b.Offset != 0) && f.Kind != ir.TypeReal && f.Kind != ir.TypeInt {
		return fmt.Errorf("member %s: scale/offset: need a numeric member, not %s", f.Name, f.Kind)
	}
	return nil
}

// IsStatic reports whether the member needs no poll at all (const or
// derived) — the generator and the class planner both skip these.
func (b Binding) IsStatic() bool { return b.Const != nil || b.Derived != "" }

// Raw is one value as it came off the wire, before any binding is applied:
// an integer, unsigned counter, float, string or bool. Protocol packages
// construct it; Apply consumes it.
type Raw struct {
	Kind RawKind
	I    int64
	U    uint64
	F    float64
	S    string
	B    bool
}

// RawKind tags a Raw.
type RawKind uint8

const (
	RawInt RawKind = iota
	RawUint
	RawFloat
	RawString
	RawBool
)

func RawIntVal(v int64) Raw     { return Raw{Kind: RawInt, I: v} }
func RawUintVal(v uint64) Raw   { return Raw{Kind: RawUint, U: v} }
func RawFloatVal(v float64) Raw { return Raw{Kind: RawFloat, F: v} }
func RawStringVal(v string) Raw { return Raw{Kind: RawString, S: v} }
func RawBoolVal(v bool) Raw     { return Raw{Kind: RawBool, B: v} }

// Key is the wire value's string form, the key space of Binding.Map and
// the comparison form of Binding.Eq: integers as decimal, floats via %g,
// bools as true/false, strings as themselves.
func (r Raw) Key() string {
	switch r.Kind {
	case RawInt:
		return strconv.FormatInt(r.I, 10)
	case RawUint:
		return strconv.FormatUint(r.U, 10)
	case RawFloat:
		return strconv.FormatFloat(r.F, 'g', -1, 64)
	case RawBool:
		return strconv.FormatBool(r.B)
	}
	return r.S
}

func (r Raw) float() (float64, bool) {
	switch r.Kind {
	case RawInt:
		return float64(r.I), true
	case RawUint:
		return float64(r.U), true
	case RawFloat:
		return r.F, true
	case RawBool:
		if r.B {
			return 1, true
		}
		return 0, true
	case RawString:
		f, err := strconv.ParseFloat(strings.TrimSpace(r.S), 64)
		return f, err == nil
	}
	return 0, false
}

// Apply turns a wire value into the member's value. c is the member's
// Counter when the binding is a rate (nil otherwise); now stamps the
// sample. ok is false when no value can be delivered yet — a rate with no
// previous sample, a reset — and the caller leaves the member as it was.
func (b Binding) Apply(f Field, raw Raw, c *Counter, now time.Time) (v ir.Value, ok bool, err error) {
	if b.Const != nil {
		v, err := coerce(b.Const, f)
		return v, err == nil, err
	}
	var val any = raw
	if b.Map != nil {
		mv, hit := b.Map[raw.Key()]
		if !hit {
			return ir.Value{}, false, fmt.Errorf("member %s: wire value %q is not in map", f.Name, raw.Key())
		}
		val = mv
	} else if b.Eq != nil {
		return ir.BoolVal(keyOf(b.Eq) == raw.Key()), true, nil
	}
	if b.Rate {
		r, isRaw := val.(Raw)
		if !isRaw {
			return ir.Value{}, false, fmt.Errorf("member %s: rate: over a mapped value", f.Name)
		}
		var u uint64
		switch r.Kind {
		case RawUint:
			u = r.U
		case RawInt:
			if r.I < 0 {
				return ir.Value{}, false, fmt.Errorf("member %s: rate: negative counter %d", f.Name, r.I)
			}
			u = uint64(r.I)
		default: // a float, or a counter that arrived as text
			fv, ok := r.float()
			if !ok || fv < 0 || fv > math.MaxUint64 {
				return ir.Value{}, false, fmt.Errorf("member %s: rate: counter %q is not a non-negative number", f.Name, r.Key())
			}
			u = uint64(fv)
		}
		if c == nil {
			return ir.Value{}, false, fmt.Errorf("member %s: rate: no counter state", f.Name)
		}
		width := b.Width
		if width == 0 {
			width = 64
		}
		rate, ok := c.Observe(u, width, now)
		if !ok {
			return ir.Value{}, false, nil
		}
		val = rate
	}
	if b.Scale != 0 || b.Offset != 0 {
		var fv float64
		var fok bool
		switch x := val.(type) {
		case Raw:
			fv, fok = x.float()
		case float64:
			fv, fok = x, true
		default:
			fv, fok = anyFloat(x)
		}
		if !fok {
			return ir.Value{}, false, fmt.Errorf("member %s: scale/offset: wire value %v is not numeric", f.Name, val)
		}
		scale := b.Scale
		if scale == 0 {
			scale = 1
		}
		val = fv*scale + b.Offset
	}
	v, err = coerce(val, f)
	if err != nil {
		return ir.Value{}, false, fmt.Errorf("member %s: %w", f.Name, err)
	}
	return v, true, nil
}

func keyOf(v any) string {
	switch x := v.(type) {
	case Raw:
		return x.Key()
	case string:
		return x
	case bool:
		return strconv.FormatBool(x)
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case uint64:
		return strconv.FormatUint(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	}
	return fmt.Sprint(v)
}

func anyFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case uint64:
		return float64(x), true
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f, err == nil
	case Raw:
		return x.float()
	}
	return 0, false
}

// coerce turns a Go value (a manifest literal, a mapped value, an applied
// number, a Raw) into the member's IEC kind. Strings are never parsed into
// numbers here except from a Raw off the wire — a manifest literal of the
// wrong shape is a manifest error, not a runtime guess.
func coerce(v any, f Field) (ir.Value, error) {
	switch f.Kind {
	case ir.TypeBool:
		switch x := v.(type) {
		case bool:
			return ir.BoolVal(x), nil
		case Raw:
			switch x.Kind {
			case RawBool:
				return ir.BoolVal(x.B), nil
			case RawInt, RawUint, RawFloat:
				fv, _ := x.float()
				return ir.BoolVal(fv != 0), nil
			}
			return ir.Value{}, fmt.Errorf("want BOOL, got string %q", x.S)
		case ir.Value:
			if x.Kind == ir.TypeBool {
				return x, nil
			}
		}
		return ir.Value{}, fmt.Errorf("want BOOL, got %v", v)
	case ir.TypeString:
		switch x := v.(type) {
		case string:
			return ir.StringVal(x), nil
		case Raw:
			return ir.StringVal(x.Key()), nil
		case ir.Value:
			if x.Kind == ir.TypeString {
				return x, nil
			}
		}
		return ir.Value{}, fmt.Errorf("want STRING, got %v", v)
	case ir.TypeInt:
		if iv, ok := v.(ir.Value); ok {
			switch iv.Kind {
			case ir.TypeInt:
				return iv, nil
			case ir.TypeReal:
				return ir.IntVal(int64(math.Round(iv.F))), nil
			}
			return ir.Value{}, fmt.Errorf("want DINT, got %s", iv.Kind)
		}
		if _, isStr := v.(string); isStr {
			return ir.Value{}, fmt.Errorf("want DINT, got string %q", v)
		}
		fv, ok := anyFloat(v)
		if !ok {
			return ir.Value{}, fmt.Errorf("want DINT, got %v", v)
		}
		return ir.IntVal(int64(math.Round(fv))), nil
	case ir.TypeReal:
		if iv, ok := v.(ir.Value); ok {
			switch iv.Kind {
			case ir.TypeReal:
				return iv, nil
			case ir.TypeInt:
				return ir.RealVal(float64(iv.I)), nil
			}
			return ir.Value{}, fmt.Errorf("want REAL, got %s", iv.Kind)
		}
		if _, isStr := v.(string); isStr {
			return ir.Value{}, fmt.Errorf("want REAL, got string %q", v)
		}
		fv, ok := anyFloat(v)
		if !ok {
			return ir.Value{}, fmt.Errorf("want REAL, got %v", v)
		}
		return ir.RealVal(fv), nil
	}
	return ir.Value{}, fmt.Errorf("unsupported member kind %s", f.Kind)
}

// Coerce is coerce for the protocol packages: a manifest literal or an
// ir.Value into the member's kind.
func Coerce(v any, f Field) (ir.Value, error) { return coerce(v, f) }
