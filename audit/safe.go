package audit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strings"
	"unicode/utf8"
)

// jsonSafe returns v with every NaN and ±Inf float as the strings "NaN",
// "+Inf" and "-Inf", so a REAL that holds one can still be journalled
// (JSON has no such numbers, and encoding/json refuses the whole entry).
//
// It reaches them in map[string]any and []any trees, and in typed
// containers a caller may hand over: []float64, [3]float32,
// map[string]float64, a struct, and pointers and interfaces to them. A
// container with no such float in it is returned as is, so its JSON is
// exactly what encoding/json makes of it; one that has one is rebuilt as a
// map[string]any or []any (a struct by its JSON field names). A type with
// its own MarshalJSON is left to it.
func jsonSafe(v any) any {
	switch x := v.(type) {
	case nil, bool, string, int, int64, uint64, json.Number:
		return v
	case float64:
		return safeFloat(x, v)
	case float32:
		return safeFloat(float64(x), v)
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = jsonSafe(e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = jsonSafe(e)
		}
		return out
	}
	rv := reflect.ValueOf(v)
	if !nonFinite(rv, 0) {
		return v
	}
	return rebuild(rv, 0)
}

func safeFloat(f float64, v any) any {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "+Inf"
	case math.IsInf(f, -1):
		return "-Inf"
	}
	return v
}

// maxDepth bounds the walk, so a cyclic value cannot recurse forever.
const maxDepth = 32

var marshalerType = reflect.TypeFor[json.Marshaler]()

// nonFinite reports whether rv holds a NaN or ±Inf float anywhere
// encoding/json would look.
func nonFinite(rv reflect.Value, depth int) bool {
	if !rv.IsValid() || depth > maxDepth {
		return false
	}
	if rv.Type().Implements(marshalerType) {
		return false
	}
	switch rv.Kind() {
	case reflect.Float32, reflect.Float64:
		f := rv.Float()
		return math.IsNaN(f) || math.IsInf(f, 0)
	case reflect.Pointer, reflect.Interface:
		return !rv.IsNil() && nonFinite(rv.Elem(), depth+1)
	case reflect.Slice, reflect.Array:
		for i := 0; i < rv.Len(); i++ {
			if nonFinite(rv.Index(i), depth+1) {
				return true
			}
		}
	case reflect.Map:
		it := rv.MapRange()
		for it.Next() {
			if nonFinite(it.Value(), depth+1) {
				return true
			}
		}
	case reflect.Struct:
		t := rv.Type()
		for i := 0; i < t.NumField(); i++ {
			// An embedded struct's fields are promoted even when its
			// type is unexported.
			if f := t.Field(i); (f.IsExported() || f.Anonymous) && nonFinite(rv.Field(i), depth+1) {
				return true
			}
		}
	}
	return false
}

// rebuild renders rv as plain JSON values with its non-finite floats as
// strings. Only called on a value nonFinite found one in.
func rebuild(rv reflect.Value, depth int) any {
	if !rv.IsValid() || depth > maxDepth {
		return nil
	}
	if rv.Type().Implements(marshalerType) || !nonFinite(rv, depth) {
		return rv.Interface()
	}
	switch rv.Kind() {
	case reflect.Float32, reflect.Float64:
		return safeFloat(rv.Float(), rv.Interface())
	case reflect.Pointer, reflect.Interface:
		return rebuild(rv.Elem(), depth+1)
	case reflect.Slice, reflect.Array:
		out := make([]any, rv.Len())
		for i := range out {
			out[i] = rebuild(rv.Index(i), depth+1)
		}
		return out
	case reflect.Map:
		out := make(map[string]any, rv.Len())
		it := rv.MapRange()
		for it.Next() {
			out[fmt.Sprint(it.Key().Interface())] = rebuild(it.Value(), depth+1)
		}
		return out
	case reflect.Struct:
		out := map[string]any{}
		structFields(rv, depth, out)
		return out
	}
	return rv.Interface()
}

// structFields puts a struct's exported fields into out under their JSON
// names: a json tag's name, "-" skipped, omitempty honoured, an untagged
// embedded struct's fields promoted, as encoding/json does.
func structFields(rv reflect.Value, depth int, out map[string]any) {
	t := rv.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() && !f.Anonymous {
			continue
		}
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, opts, _ := strings.Cut(tag, ",")
		fv := rv.Field(i)
		if f.Anonymous && name == "" {
			if fv.Kind() == reflect.Pointer {
				if fv.IsNil() {
					continue
				}
				fv = fv.Elem()
			}
			if fv.Kind() == reflect.Struct {
				structFields(fv, depth+1, out)
				continue
			}
			if !f.IsExported() {
				continue
			}
		}
		if strings.Contains(","+opts+",", ",omitempty,") && fv.IsZero() {
			continue
		}
		if name == "" {
			name = f.Name
		}
		out[name] = rebuild(fv, depth+1)
	}
}

// Cap returns v unchanged when its JSON is at most max bytes, and
// otherwise a marker that says so: {"truncated": true, "bytes": n,
// "sha256": hex of the full JSON, "head": its first bytes}. A value a
// client sent can be as large as its request, and an entry is fsynced and
// kept in memory: Cap bounds what one costs, and the hash still lets the
// full value be matched against a copy kept elsewhere.
func Cap(v any, max int) any {
	if v == nil {
		return nil
	}
	b, err := json.Marshal(jsonSafe(v))
	if err != nil || len(b) <= max {
		return v
	}
	sum := sha256.Sum256(b)
	head := max / 4
	if head > 256 {
		head = 256
	}
	h := string(b[:head])
	for !utf8.ValidString(h) {
		h = h[:len(h)-1]
	}
	return map[string]any{"truncated": true, "bytes": len(b), "sha256": hex.EncodeToString(sum[:]), "head": h}
}
