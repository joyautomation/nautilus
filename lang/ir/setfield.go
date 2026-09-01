package ir

import (
	"fmt"
	"sort"
)

// Writing INTO a value by member path — the operator-write counterpart of
// SeedFromInit, and the one primitive behind every dotted write in the
// product: `POST /api/tags {"name":"P101.Drive.Speed"}`, a test manifest's
// `given: { P101.Speed: 12.5 }`, and a Sparkplug command naming a template
// member. One implementation so all three agree on what resolves, what
// coerces, and what the failure reads like.

// SetField returns a copy of val with the member addressed by path set to v.
//
// path is the member path already split on "." (["Drive", "Speed"]); an
// empty path assigns val itself. Copies rather than mutates: the tag store
// hands out values whose Fld slice may still back the last scan's snapshot,
// so an in-place write would be visible to a reader mid-scan.
//
//   - A LEAF is coerced to the type the value already carries, never
//     retyped: a REAL member takes any number, an INT member an integer (or
//     a whole-numbered float, as YAML and JSON both spell 5 as 5.0), a BOOL
//     member only a boolean. Anything else is an error rather than a silent
//     conversion — a DINT setpoint quietly becoming a REAL is exactly the
//     kind of thing that costs an afternoon.
//   - A MAP against a struct is a PARTIAL MERGE: the members the map names
//     are set, every other member KEEPS ITS CURRENT VALUE. That is
//     deliberately unlike SeedFromInit, which zero-fills the members its
//     mapping omits — seeding builds a value from nothing, a write edits one
//     the plant is already running on, and zeroing the members an operator
//     didn't mention would drop a pump's setpoints on the way to its
//     start bit.
//   - An ir.Value assigns directly (a driver with typed values), as long as
//     its kind matches what is already there.
//
// sofar names val for error messages and grows with the path, so a caller
// passing "tag P101" gets seed.go's message style back:
//
//	tag P101: unknown member Spede (did you mean Speed?)
//	tag P101.Drive: unknown member Rpm (did you mean RPM?)
//	tag P101.Speed: want REAL, got a string
func SetField(val Value, path []string, v any, sofar string) (Value, error) {
	if len(path) == 0 {
		return assignInto(val, v, sofar)
	}
	name := path[0]
	if name == "" {
		return Value{}, fmt.Errorf("%s: empty member name in the path", sofar)
	}
	if val.Kind != TypeStruct || val.Struct == nil {
		return Value{}, fmt.Errorf("%s is a %s, not a struct — it has no member %s",
			sofar, val.Kind, name)
	}
	i, ok := val.Struct.FieldIndex[name]
	if !ok || i >= len(val.Fld) {
		return Value{}, fmt.Errorf("%s: unknown member %s%s", sofar, name, didYouMean(name, val.Struct))
	}
	fv, err := SetField(val.Fld[i], path[1:], v, sofar+"."+name)
	if err != nil {
		return Value{}, err
	}
	out := val
	out.Fld = append([]Value(nil), val.Fld...)
	out.Fld[i] = fv
	return out, nil
}

// assignInto replaces a whole value: a mapping merges onto a struct, an
// ir.Value is taken as-is when its kind agrees, and anything else is a
// scalar coerced to the value's own kind.
func assignInto(cur Value, v any, sofar string) (Value, error) {
	if nv, ok := v.(Value); ok {
		if cur.Kind != TypeVoid && nv.Kind != cur.Kind {
			return Value{}, fmt.Errorf("%s: want %s, got %s", sofar, cur.Kind, nv.Kind)
		}
		return CopyValue(nv), nil
	}
	if cur.Kind == TypeStruct {
		m, ok := asStringMap(v)
		if !ok {
			return Value{}, fmt.Errorf(
				"%s: %s is a struct — a write must be a mapping of member: value, not %s",
				sofar, structName(cur), describeKind(v))
		}
		// Sorted so several bad keys report in the same order every run, and
		// so a partial merge is deterministic — same reason SeedFromInit
		// sorts.
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := cur
		for _, k := range keys {
			nv, err := SetField(out, []string{k}, m[k], sofar)
			if err != nil {
				return Value{}, err
			}
			out = nv
		}
		return out, nil
	}
	return coerceLeaf(cur, v, sofar)
}

// coerceLeaf converts a written scalar to the kind the value already has.
// It is the value-side twin of scalarFromInit (which works from a *Type,
// because a seed has no current value to take its type from).
func coerceLeaf(cur Value, v any, sofar string) (Value, error) {
	switch cur.Kind {
	case TypeBool:
		if b, ok := v.(bool); ok {
			return BoolVal(b), nil
		}
		return Value{}, fmt.Errorf("%s: want BOOL, got %s", sofar, describeKind(v))
	case TypeReal:
		if f, ok := toFloat(v); ok {
			return RealVal(f), nil
		}
		return Value{}, fmt.Errorf("%s: want REAL, got %s", sofar, describeKind(v))
	case TypeInt:
		if i, ok := toInt(v); ok {
			return IntVal(i), nil
		}
		return Value{}, fmt.Errorf("%s: want INT, got %s", sofar, describeKind(v))
	case TypeTime:
		if i, ok := toInt(v); ok {
			return TimeVal(i), nil
		}
		return Value{}, fmt.Errorf("%s: want TIME (milliseconds), got %s", sofar, describeKind(v))
	case TypeString:
		if s, ok := v.(string); ok {
			return StringVal(s), nil
		}
		return Value{}, fmt.Errorf("%s: want STRING, got %s", sofar, describeKind(v))
	case TypeArray, TypeFB:
		// No settled literal form for either in a write payload (an array
		// needs indices, an FB instance is identity, not a value). Refuse
		// rather than guess — the same line scalarFromInit draws.
		return Value{}, fmt.Errorf("%s: a %s cannot be written by path", sofar, cur.Kind)
	}
	return Value{}, fmt.Errorf("%s: nothing here to write (the tag has no value yet)", sofar)
}

// structName is the UDT name of a struct value, for an error message that
// says which type's members were expected.
func structName(v Value) string {
	if v.Struct != nil && v.Struct.Name != "" {
		return v.Struct.Name
	}
	return "STRUCT"
}
