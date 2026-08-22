package ir

import (
	"fmt"
	"sort"
	"strings"
)

// SeedFromInit builds a tag's seed value from a manifest `init:` value,
// honoring t's shape.
//
//   - init == nil returns Zero(t) — the existing zero-of-type seed.
//   - For a struct type, init must be a map[string]any (as the YAML
//     decoder hands back a mapping node): named members are set, in the
//     member's own type recursively for a nested struct; every member the
//     map omits keeps Zero of its field type. A struct type given anything
//     else (a scalar) is an error — struct tags need a mapping, not a
//     bare value.
//   - For a scalar type, init is the scalar itself (bool/int/int64/float64/
//     string), converted to t's kind the way a plain (untyped) tag's init
//     always has been.
//
// Errors name the member path so a load failure points at what to fix:
// SeedFromInit itself is called with path "init", and a nested member
// extends it dotted ("init.LVL"), matching how the manifest addresses a
// field elsewhere (`given:`/`expect:` dotted paths, tag-meta: keys).
func SeedFromInit(t *Type, init any) (Value, error) {
	return seedFromInit(t, init, "init")
}

func seedFromInit(t *Type, init any, path string) (Value, error) {
	if init == nil {
		return Zero(t), nil
	}
	if t == nil {
		return Value{}, fmt.Errorf("%s: no type to seed against", path)
	}
	if t.Kind != TypeStruct {
		return scalarFromInit(t, init, path)
	}
	m, ok := asStringMap(init)
	if !ok {
		return Value{}, fmt.Errorf(
			"%s: %s is a struct — init must be a mapping of member: value, not %s",
			path, t.String(), describeKind(init))
	}
	v := Zero(t)
	// Sorted so a manifest with several bad keys reports them in the same
	// order every run, instead of map-iteration order.
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		idx, ok := t.Struct.FieldIndex[k]
		if !ok {
			return Value{}, fmt.Errorf("%s: unknown member %s%s", path, k, didYouMean(k, t.Struct))
		}
		fv, err := seedFromInit(t.Struct.Fields[idx].Type, m[k], path+"."+k)
		if err != nil {
			return Value{}, err
		}
		v.Fld[idx] = fv
	}
	return v, nil
}

// scalarFromInit converts a leaf init value to t's kind. Accepted Go shapes
// mirror what a YAML decoder ever hands back for a scalar node.
func scalarFromInit(t *Type, v any, path string) (Value, error) {
	switch t.Kind {
	case TypeBool:
		b, ok := v.(bool)
		if !ok {
			return Value{}, fmt.Errorf("%s: want BOOL, got %s", path, describeKind(v))
		}
		return BoolVal(b), nil
	case TypeReal:
		f, ok := toFloat(v)
		if !ok {
			return Value{}, fmt.Errorf("%s: want REAL, got %s", path, describeKind(v))
		}
		return RealVal(f), nil
	case TypeInt:
		i, ok := toInt(v)
		if !ok {
			return Value{}, fmt.Errorf("%s: want INT, got %s", path, describeKind(v))
		}
		return IntVal(i), nil
	case TypeString:
		s, ok := v.(string)
		if !ok {
			return Value{}, fmt.Errorf("%s: want STRING, got %s", path, describeKind(v))
		}
		return StringVal(s), nil
	default:
		// TIME, ARRAY, FB — none of the manifest UDTs use these as a member
		// today, and there's no settled literal form for them in a mapping
		// (a TIME needs T#-style parsing, an ARRAY needs indices). Refuse
		// rather than guess.
		return Value{}, fmt.Errorf("%s: %s members cannot be seeded by init:", path, t.String())
	}
}

// asStringMap normalizes what a YAML decoder hands back for a mapping node
// targeting `any`: gopkg.in/yaml.v3 always produces map[string]any for a
// mapping with string (or stringable) keys.
func asStringMap(v any) (map[string]any, bool) {
	m, ok := v.(map[string]any)
	return m, ok
}

// toFloat accepts every numeric shape a YAML decoder produces.
func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case uint64:
		return float64(x), true
	}
	return 0, false
}

// toInt accepts an integer, or a float that carries no fraction (so
// `init: 5.0` on an INT member still works — YAML makes ".0" the natural
// way to keep a REAL sibling from decoding as an int, and a member typed
// INT should not have to be spelled without one).
func toInt(v any) (int64, bool) {
	switch x := v.(type) {
	case int:
		return int64(x), true
	case int64:
		return x, true
	case uint64:
		return int64(x), true
	case float64:
		if x == float64(int64(x)) {
			return int64(x), true
		}
	}
	return 0, false
}

// describeKind names v's shape for an error message, in the vocabulary of
// what a manifest author actually typed, not a Go type name.
func describeKind(v any) string {
	switch v.(type) {
	case nil:
		return "nothing"
	case bool:
		return "a boolean"
	case string:
		return "a string"
	case int, int64, uint64, float64:
		return "a number"
	case map[string]any:
		return "a mapping"
	case []any:
		return "a list"
	default:
		return fmt.Sprintf("a %T", v)
	}
}

// didYouMean suggests the nearest struct member within a two-edit
// distance, formatted for a direct append onto an "unknown member NAME"
// message — "" when nothing is close enough to be worth guessing.
func didYouMean(name string, sd *StructDef) string {
	best, bestDist := "", 3
	for _, f := range sd.Fields {
		if strings.EqualFold(f.Name, name) {
			return " (did you mean " + f.Name + "?)"
		}
		if d := editDistance(name, f.Name); d < bestDist {
			best, bestDist = f.Name, d
		}
	}
	if best == "" {
		return ""
	}
	return " (did you mean " + best + "?)"
}

// editDistance is Levenshtein over two short identifiers, one row at a
// time — the same shape as internal/lsp's tag-name suggester, kept as a
// separate small copy rather than an import so lang/ir stays free of a
// dependency on tooling that itself depends on this package's callers.
func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	curr := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		curr[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			curr[j] = min(prev[j]+1, min(curr[j-1]+1, prev[j-1]+cost))
		}
		prev, curr = curr, prev
	}
	return prev[len(b)]
}
