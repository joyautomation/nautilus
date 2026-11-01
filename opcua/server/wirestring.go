// wirestring.go makes an empty String in a value go on the wire as an
// empty String rather than a null one (#434, black-box F-37).
//
// Part 6 §5.2.2.4 encodes a String as an Int32 length and its bytes, with
// -1 for null, so "" (length 0) and null are different values, and a client
// that decodes them apart (asyncua, the .NET and Java stacks) read null
// where the project held "". The encoder is gopcua's ua package, not ours:
// ua.Buffer.WriteString writes -1 for every "", Go's string having no null,
// and a Variant's value goes through it with no hook to say otherwise.
// Changing WriteString would change every empty String field of every
// structure the stack encodes (BrowseNames, locales, diagnostics), where
// null is what clients expect; only a value is wrong.
//
// So a value holding "" is handed to the encoder as a copy of its Variant
// whose Go value has an Int32 0 where each "" was: the Variant's mask still
// says String, and the encoder writes an Int32 0 as the four bytes of a
// length-0 String. The copy is made only at the edge, as a Read result or a
// notification leaves, so nothing of ours ever reads it; ua.Variant keeps
// its value unexported, so the copy's value is set through reflect.
// selfTest checks the trick against the encoder once, at start: should a
// gopcua upgrade change Variant or its encoding, the wire goes back to
// null for "" rather than to anything wrong, and
// TestWireStringSurvivesTheEncoder fails.
package server

import (
	"bytes"
	"reflect"
	"sync"
	"unsafe"

	"github.com/gopcua/opcua/ua"
)

// wireValue returns dv ready for the wire: dv itself, or, when its value is
// a String or an array of Strings holding "", a copy whose value encodes
// each "" as an empty String. dv is never changed (a node of gopcua's may
// hand out the DataValue it holds).
func wireValue(dv *ua.DataValue) *ua.DataValue {
	if dv == nil || dv.Value == nil {
		return dv
	}
	v := wireVariant(dv.Value)
	if v == dv.Value {
		return dv
	}
	cp := *dv
	cp.Value = v
	return &cp
}

// wireVariant is wireValue for a Variant: v, or a copy of it whose "" are
// sent as empty Strings.
func wireVariant(v *ua.Variant) *ua.Variant {
	if v.Type() != ua.TypeIDString || !wireStringsWork() {
		return v
	}
	repl, ok := emptyAsZero(v.Value())
	if !ok {
		return v
	}
	return withValue(v, repl)
}

// emptyAsZero is val with each "" in it an Int32 0, and whether there was
// any: a string, a slice of them, or a slice of those (a multi-dimensional
// array), whose levels stay slices so the encoder walks into them.
func emptyAsZero(val any) (any, bool) {
	switch x := val.(type) {
	case string:
		if x == "" {
			return int32(0), true
		}
		return x, false
	case []string:
		out, found := make([]any, len(x)), false
		for i, s := range x {
			if s == "" {
				out[i], found = int32(0), true
			} else {
				out[i] = s
			}
		}
		return out, found
	}
	rv := reflect.ValueOf(val)
	if rv.Kind() != reflect.Slice {
		return val, false
	}
	// A level above the strings: each element converted, gathered in a
	// slice of the converted type ([][]any for a [][]string), never in a
	// []any. The encoder walks into an element only when its kind is
	// Slice, and an element of a []any is an Interface: it would write
	// nothing for it.
	if rv.Len() == 0 {
		return val, false
	}
	elems, found := make([]any, rv.Len()), false
	for i := range elems {
		e, ok := emptyAsZero(rv.Index(i).Interface())
		elems[i], found = e, found || ok
	}
	// Every level is converted, found or not, so that sibling elements
	// share a type ([]string always becomes []any above).
	typ := reflect.TypeOf(elems[0])
	out := reflect.MakeSlice(reflect.SliceOf(typ), len(elems), len(elems))
	for i, e := range elems {
		if reflect.TypeOf(e) != typ {
			return val, false // a ragged shape the encoder could not write either
		}
		out.Index(i).Set(reflect.ValueOf(e))
	}
	return out.Interface(), found
}

// withValue is a copy of v with its unexported value replaced by val; its
// mask and array lengths are v's.
func withValue(v *ua.Variant, val any) *ua.Variant {
	cp := *v
	f := reflect.ValueOf(&cp).Elem().FieldByName("value")
	if !f.IsValid() || f.Kind() != reflect.Interface {
		return v
	}
	reflect.NewAt(f.Type(), unsafe.Pointer(f.UnsafeAddr())).Elem().Set(reflect.ValueOf(val))
	return &cp
}

var (
	wireStringsOnce sync.Once
	wireStringsOK   bool
)

// wireStringsWork reports whether the trick does what it says against the
// gopcua this was built with: "" encodes as an empty String, and an array
// of Strings with one in it as the same array with an empty String there.
func wireStringsWork() bool {
	wireStringsOnce.Do(func() { wireStringsOK = selfTest() })
	return wireStringsOK
}

func selfTest() (ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	encode := func(v any) []byte {
		val, _ := emptyAsZero(v)
		b, err := withValue(ua.MustVariant(v), val).Encode()
		if err != nil {
			return nil
		}
		return b
	}
	// A two-dimensional array is what gopcua writes for it with the one
	// null length an empty one.
	grid := [][]string{{"a", "b"}, {"", "c"}}
	plain, err := ua.MustVariant(grid).Encode()
	if err != nil {
		return false
	}
	return bytes.Equal(encode(""), []byte{0x0c, 0, 0, 0, 0}) &&
		bytes.Equal(encode([]string{"", "b"}), []byte{0x8c, 2, 0, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 'b'}) &&
		bytes.Equal(encode(grid), bytes.Replace(plain, []byte{0xff, 0xff, 0xff, 0xff}, []byte{0, 0, 0, 0}, 1))
}
