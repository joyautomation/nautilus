package server

import (
	"testing"

	"github.com/joyautomation/nautilus/lang/ir"
)

// An array of structs, FBs or arrays is an Object with one child per
// element; an array of scalars is one array Variable. The first element
// decides (IEC arrays are homogeneous); an empty array is not composite.
func TestCompositeArray(t *testing.T) {
	arr := func(elems ...ir.Value) ir.Value { return ir.Value{Kind: ir.TypeArray, Arr: elems} }
	st := ir.Value{Kind: ir.TypeStruct}
	fb := ir.Value{Kind: ir.TypeFB}
	in := ir.Value{Kind: ir.TypeInt}
	for _, c := range []struct {
		name string
		v    ir.Value
		want bool
	}{
		{"array of structs", arr(st, st, st), true},
		{"array of FBs", arr(fb, fb), true},
		{"array of arrays", arr(arr(in), arr(in)), true},
		{"array of ints", arr(in, in, in), false},
		{"empty array", arr(), false},
		{"a struct is not an array", st, false},
		{"an int", in, false},
	} {
		if got := compositeArray(c.v); got != c.want {
			t.Errorf("%s: compositeArray = %v, want %v", c.name, got, c.want)
		}
	}
}
