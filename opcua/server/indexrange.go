package server

import (
	"reflect"
	"strconv"
	"strings"

	"github.com/gopcua/opcua/ua"
)

// span is one dimension of a NumericRange: the first and last index
// selected, inclusive.
type span struct{ lo, hi int }

// parseNumericRange reads Part 4 §7.27's NumericRange: one range per
// dimension, comma-separated, each "n" or "n:m" with m > n, indexes
// counted from 0. Anything else is Bad_IndexRangeInvalid.
func parseNumericRange(s string) ([]span, ua.StatusCode) {
	var out []span
	for _, dim := range strings.Split(s, ",") {
		lo, hi, two := strings.Cut(dim, ":")
		a, err := strconv.ParseUint(lo, 10, 32)
		if err != nil {
			return nil, ua.StatusBadIndexRangeInvalid
		}
		b := a
		if two {
			if b, err = strconv.ParseUint(hi, 10, 32); err != nil || b <= a {
				return nil, ua.StatusBadIndexRangeInvalid
			}
		}
		out = append(out, span{int(a), int(b)})
	}
	return out, ua.StatusGood
}

// indexRange is a ReadValueId's IndexRange applied to an attribute's
// value, for a Read or a monitored item: the selected slice of an array,
// the selected characters of a String, or, for an array of Strings, the
// selected elements cut to the characters a second dimension names
// (Part 4 §7.27: a String is an array of characters, a ByteString an
// array of bytes, and a scalar has no dimension to index).
// Bad_IndexRangeNoData when the value has fewer dimensions than the range
// names or a lower bound lies past the end; an upper bound past the end
// is the partial result, everything from the lower bound on. In an array
// of Strings an element too short for its substring's lower bound is the
// empty string, not a failed read. A value that is already a bad status
// is returned as it is: the range has nothing to select from. The
// server's own arrays are one-dimensional.
func indexRange(dv *ua.DataValue, rng string) *ua.DataValue {
	if dv == nil || dv.Status&0x80000000 != 0 { // a bad status: nothing to select from
		return dv
	}
	spans, code := parseNumericRange(rng)
	if code != ua.StatusGood {
		return status(code)
	}
	if dv.Value == nil {
		return status(ua.StatusBadIndexRangeNoData)
	}
	out, ok := cut(dv.Value.Value(), spans)
	if !ok {
		return status(ua.StatusBadIndexRangeNoData)
	}
	nv, err := ua.NewVariant(out)
	if err != nil {
		return status(ua.StatusBadIndexRangeNoData)
	}
	cp := *dv
	cp.Value = nv
	return &cp
}

// cut is val reduced to the spans: one over an array or a String, two
// over an array of Strings. Not ok when val has fewer dimensions than
// there are spans or a lower bound is past the end; an upper bound past
// the end stops at the end.
func cut(val any, spans []span) (any, bool) {
	if s, ok := val.(string); ok {
		if len(spans) != 1 {
			return nil, false
		}
		return substring(s, spans[0])
	}
	v := reflect.ValueOf(val)
	if v.Kind() != reflect.Slice || len(spans) > 2 || spans[0].lo >= v.Len() {
		return nil, false
	}
	v = v.Slice(spans[0].lo, min(spans[0].hi, v.Len()-1)+1)
	if len(spans) == 1 {
		return v.Interface(), true
	}
	strs, ok := v.Interface().([]string)
	if !ok {
		return nil, false
	}
	out := make([]string, len(strs))
	for i, s := range strs {
		out[i], _ = substring(s, spans[1]) // too short: "" (§7.27: a null or empty value)
	}
	return out, true
}

// substring is the characters of s the span names, counted in characters
// rather than bytes so that a range never cuts a multi-byte character.
// Not ok when s does not reach the span's first character.
func substring(s string, sp span) (string, bool) {
	r := []rune(s)
	if sp.lo >= len(r) {
		return "", false
	}
	return string(r[sp.lo : min(sp.hi, len(r)-1)+1]), true
}
