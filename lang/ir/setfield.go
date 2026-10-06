package ir

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
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
	return SetFieldTyped(val, nil, path, v, sofar)
}

// SetFieldTyped is SetField for a caller that knows val's declared type t
// (nil when it does not). The type is what makes a write land the way a
// program's assignment would: an enumerated member takes its member NAME
// ("Run", "Mode#Run") or integer and stores the named value (#247), an
// array index counts from the declared lower bound. Below a struct the
// member types come from the struct's own definition, so only the root's
// type is ever needed — and only for a root that is an enumeration or an
// array.
//
// A path segment "[n]" indexes an array (PathSegments splits
// "Steps[2].Mode" into "Steps", "[2]", "Mode"). Without a type the index
// counts from 0, which is what a driver-delivered array is.
func SetFieldTyped(val Value, t *Type, path []string, v any, sofar string) (Value, error) {
	if len(path) == 0 {
		return assignInto(val, t, v, sofar)
	}
	name := path[0]
	if name == "" {
		return Value{}, fmt.Errorf("%s: empty member name in the path", sofar)
	}
	if idx, isIndex, err := indexSegment(name, sofar); isIndex {
		if err != nil {
			return Value{}, err
		}
		i, et, err := elementAt(val, t, idx, sofar)
		if err != nil {
			return Value{}, err
		}
		ev, err := SetFieldTyped(val.Arr[i], et, path[1:], v, sofar+name)
		if err != nil {
			return Value{}, err
		}
		out := val
		out.Arr = append([]Value(nil), val.Arr...)
		out.Arr[i] = ev
		return out, nil
	}
	if val.Kind != TypeStruct || val.Struct == nil {
		return Value{}, fmt.Errorf("%s is a %s, not a struct — it has no member %s",
			sofar, val.Kind, name)
	}
	i, ok := val.Struct.FieldOf(name)
	if !ok || i >= len(val.Fld) {
		return Value{}, fmt.Errorf("%s: unknown member %s%s", sofar, name, didYouMean(name, val.Struct))
	}
	fv, err := SetFieldTyped(val.Fld[i], val.Struct.Fields[i].Type, path[1:], v, sofar+"."+name)
	if err != nil {
		return Value{}, err
	}
	out := val
	out.Fld = append([]Value(nil), val.Fld...)
	out.Fld[i] = fv
	return out, nil
}

// FieldAt reads the member or element of val addressed by path — the read
// twin of SetFieldTyped, with the same segments and the same errors — and
// returns it with its declared type (nil where the walk had none).
func FieldAt(val Value, t *Type, path []string, sofar string) (Value, *Type, error) {
	for _, name := range path {
		if idx, isIndex, err := indexSegment(name, sofar); isIndex {
			if err != nil {
				return Value{}, nil, err
			}
			i, et, err := elementAt(val, t, idx, sofar)
			if err != nil {
				return Value{}, nil, err
			}
			val, t, sofar = val.Arr[i], et, sofar+name
			continue
		}
		if val.Kind != TypeStruct || val.Struct == nil {
			return Value{}, nil, fmt.Errorf("%s is a %s, not a struct — it has no member %s", sofar, val.Kind, name)
		}
		i, ok := val.Struct.FieldOf(name)
		if !ok || i >= len(val.Fld) {
			return Value{}, nil, fmt.Errorf("%s: unknown member %s%s", sofar, name, didYouMean(name, val.Struct))
		}
		val, t, sofar = val.Fld[i], val.Struct.Fields[i].Type, sofar+"."+val.Struct.Fields[i].Name
	}
	return val, t, nil
}

// PathSegments splits a member path below a tag into segments: member
// names, and "[n]" for each array index — "Steps[2].Mode" is "Steps",
// "[2]", "Mode"; "[1][0]" and "[1,0]" are both "[1]", "[0]". "" is no
// segments.
func PathSegments(path string) []string {
	if path == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(path, ".") {
		name, idx, hasIdx := strings.Cut(part, "[")
		if name != "" || !hasIdx {
			out = append(out, name)
		}
		if !hasIdx {
			continue
		}
		rest := "[" + idx
		for rest != "" {
			if rest[0] != '[' {
				out = append(out, rest) // malformed; indexSegment reports it
				break
			}
			end := strings.IndexByte(rest, ']')
			if end < 0 {
				out = append(out, rest)
				break
			}
			for _, n := range strings.Split(rest[1:end], ",") {
				out = append(out, "["+strings.TrimSpace(n)+"]")
			}
			rest = rest[end+1:]
		}
	}
	return out
}

// SplitAddress splits a full address — "P101.Drive.Speed",
// "Recipes[2].Mode" — into its root tag name and the segments below it.
func SplitAddress(addr string) (root string, path []string) {
	cut := strings.IndexAny(addr, ".[")
	if cut < 0 {
		return addr, nil
	}
	return addr[:cut], PathSegments(strings.TrimPrefix(addr[cut:], "."))
}

// JoinPath is the inverse of SplitAddress: root.Member[2].Sub.
func JoinPath(root string, path []string) string {
	var b strings.Builder
	b.WriteString(root)
	for _, seg := range path {
		if !strings.HasPrefix(seg, "[") {
			b.WriteByte('.')
		}
		b.WriteString(seg)
	}
	return b.String()
}

// indexSegment reads a "[n]" segment; isIndex is false for a member name.
func indexSegment(seg, sofar string) (int, bool, error) {
	if !strings.HasPrefix(seg, "[") {
		return 0, false, nil
	}
	if !strings.HasSuffix(seg, "]") {
		return 0, true, fmt.Errorf("%s: %q is not an index (write [n])", sofar, seg)
	}
	n, err := strconv.Atoi(strings.TrimSpace(seg[1 : len(seg)-1]))
	if err != nil {
		return 0, true, fmt.Errorf("%s: %q is not an index (write [n])", sofar, seg)
	}
	return n, true, nil
}

// elementAt resolves index idx of the array val (declared t, or nil) to a
// slot in val.Arr and the element type.
func elementAt(val Value, t *Type, idx int, sofar string) (int, *Type, error) {
	if val.Kind != TypeArray {
		return 0, nil, fmt.Errorf("%s is a %s, not an array — it has no element [%d]", sofar, val.Kind, idx)
	}
	lo := 0
	var et *Type
	if t != nil && t.Kind == TypeArray {
		lo, et = t.ArrLoBound, t.Elem
	}
	i := idx - lo
	if i < 0 || i >= len(val.Arr) {
		return 0, nil, fmt.Errorf("%s[%d]: index out of bounds %d..%d", sofar, idx, lo, lo+len(val.Arr)-1)
	}
	return i, et, nil
}

// assignInto replaces a whole value: a mapping merges onto a struct, a list
// onto an array, an ir.Value is taken as-is when its kind agrees, and
// anything else is a scalar coerced to the value's own kind (an
// enumeration's member by name or integer, when t says it is one).
func assignInto(cur Value, t *Type, v any, sofar string) (Value, error) {
	if t != nil && t.Enum != nil {
		return EnumFromAny(t, v, sofar)
	}
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
			nv, err := SetFieldTyped(out, t, []string{k}, m[k], sofar)
			if err != nil {
				return Value{}, err
			}
			out = nv
		}
		return out, nil
	}
	if cur.Kind == TypeArray {
		// A list merges element by element from the lower bound, the way
		// a mapping merges onto a struct: a shorter list leaves the rest,
		// a null entry leaves that element.
		list, ok := v.([]any)
		if !ok {
			return Value{}, fmt.Errorf("%s: an array is written as a list of elements, or one element by index (%s[n]), not %s", sofar, sofar, describeKind(v))
		}
		if len(list) > len(cur.Arr) {
			return Value{}, fmt.Errorf("%s: %d values for an array of %d", sofar, len(list), len(cur.Arr))
		}
		lo := 0
		if t != nil && t.Kind == TypeArray {
			lo = t.ArrLoBound
		}
		out := cur
		for i, e := range list {
			if e == nil {
				continue
			}
			nv, err := SetFieldTyped(out, t, []string{"[" + strconv.Itoa(lo+i) + "]"}, e, sofar)
			if err != nil {
				return Value{}, err
			}
			out = nv
		}
		return out, nil
	}
	return coerceLeaf(cur, v, sofar)
}

// EnumFromAny converts a written value to the enumerated type t: a member
// name, optionally qualified (Run, Mode#Run), any case; or an integer —
// a member's, or one no member has, which stays that number and shows
// unnamed (what TO_Mode(n) and a field-bus write give). Any other name is
// an error that lists the members. This is the one rule for every write
// of an enumeration from outside a program: a whole tag, a struct member
// by path, a force, a test's given:.
func EnumFromAny(t *Type, v any, sofar string) (Value, error) {
	switch x := v.(type) {
	case string:
		member := x
		if q, after, qualified := strings.Cut(x, "#"); qualified {
			if !SameName(q, t.Enum.Name) {
				return Value{}, fmt.Errorf("%s: %q is not a member of %s (%s)", sofar, x, t.Enum.Name, enumNames(t.Enum))
			}
			member = after
		}
		m, ok := t.Enum.Member(strings.TrimSpace(member))
		if !ok {
			return Value{}, fmt.Errorf("%s: %q is not a member of %s (%s)", sofar, x, t.Enum.Name, enumNames(t.Enum))
		}
		return t.Enum.Val(m.Value), nil
	case Value:
		if x.Kind == TypeString {
			return EnumFromAny(t, x.S, sofar)
		}
		return CoerceValue(x, t), nil
	}
	if i, ok := toInt(v); ok {
		return t.Enum.Val(i), nil
	}
	return Value{}, fmt.Errorf("%s: want a member of %s (%s), got %s", sofar, t.Enum.Name, enumNames(t.Enum), describeKind(v))
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
