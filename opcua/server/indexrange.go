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

// parseNumericRange reads Part 4 §7.22's NumericRange: one range per
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

// indexRange is a Read's IndexRange applied to an attribute's value: the
// selected slice of an array, Bad_IndexRangeNoData when the value is not
// an array, has fewer dimensions than the range names, or does not reach
// every index named (Part 4 §7.22, as open62541 reads it). A value that
// is already a bad status is returned as it is: the range has nothing to
// select from. The server's arrays are one-dimensional.
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
	v := reflect.ValueOf(dv.Value.Value())
	if v.Kind() != reflect.Slice || len(spans) != 1 {
		return status(ua.StatusBadIndexRangeNoData)
	}
	sp := spans[0]
	if sp.hi >= v.Len() {
		return status(ua.StatusBadIndexRangeNoData)
	}
	nv, err := ua.NewVariant(v.Slice(sp.lo, sp.hi+1).Interface())
	if err != nil {
		return status(ua.StatusBadIndexRangeNoData)
	}
	cp := *dv
	cp.Value = nv
	return &cp
}
