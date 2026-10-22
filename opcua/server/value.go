// value.go maps the tag store's values onto OPC UA's: an ir.Value becomes a
// Variant with the DataType the tag was declared as, an io.Quality becomes a
// StatusCode, and a dotted address below a tag resolves to the member it
// names — through struct fields, array elements and a function block's pins
// alike, the same shape runtime.Tags.All renders for the HTTP API.
package server

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/gopcua/opcua/id"
	"github.com/gopcua/opcua/ua"
	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
)

// member is one addressable point in a tag's value tree: the tag itself
// (path empty) or a struct field, array element or FB pin below it.
type member struct {
	addr string // "P101.Drive.Speed", "Levels[2]"
	val  ir.Value
	typ  *ir.Type // declared type when known; nil on an untyped walk
}

// walk resolves segs (ir.PathSegments form: member names and "[n]") below
// the root value. It mirrors ir.FieldAt for structs and arrays and adds the
// function-block case, which FieldAt leaves to the VM: an FB's pins are the
// members the HTTP API and the HMI already show, so they are the members a
// browsing client sees too.
func walk(root string, v ir.Value, t *ir.Type, segs []string) (member, bool) {
	addr := root
	for _, seg := range segs {
		switch {
		case strings.HasPrefix(seg, "["):
			if v.Kind != ir.TypeArray {
				return member{}, false
			}
			idx, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(seg, "["), "]"))
			if err != nil {
				return member{}, false
			}
			lo := 0
			var et *ir.Type
			if t != nil && t.Kind == ir.TypeArray {
				lo, et = t.ArrLoBound, t.Elem
			}
			i := idx - lo
			if i < 0 || i >= len(v.Arr) {
				return member{}, false
			}
			v, t, addr = v.Arr[i], et, addr+seg
		case v.Kind == ir.TypeStruct && v.Struct != nil:
			i, ok := v.Struct.FieldOf(seg)
			if !ok || i >= len(v.Fld) {
				return member{}, false
			}
			v, t, addr = v.Fld[i], v.Struct.Fields[i].Type, addr+"."+v.Struct.Fields[i].Name
		case v.Kind == ir.TypeFB && v.FB != nil && v.FB.Def != nil:
			slots := v.FB.Def.AllSlots()
			found := -1
			for i, s := range slots {
				if ir.SameName(s.Name, seg) && !strings.HasPrefix(s.Name, "_") {
					found = i
					break
				}
			}
			if found < 0 || found >= len(v.FB.Slots) {
				return member{}, false
			}
			v, t, addr = v.FB.Slots[found], slots[found].Type, addr+"."+slots[found].Name
		default:
			return member{}, false
		}
	}
	return member{addr: addr, val: v, typ: t}, true
}

// children lists the members directly below m, in declaration order: a
// struct's fields, an FB's visible pins, a composite array's elements. A
// scalar or an array of scalars has none — the array IS the value.
func children(m member) []member {
	v := m.val
	switch v.Kind {
	case ir.TypeStruct:
		if v.Struct == nil {
			return nil
		}
		out := make([]member, 0, len(v.Fld))
		for i, f := range v.Fld {
			if i >= len(v.Struct.Fields) {
				break
			}
			sf := v.Struct.Fields[i]
			out = append(out, member{addr: m.addr + "." + sf.Name, val: f, typ: sf.Type})
		}
		return out
	case ir.TypeFB:
		if v.FB == nil || v.FB.Def == nil {
			return nil
		}
		slots := v.FB.Def.AllSlots()
		out := make([]member, 0, len(slots))
		for i, s := range slots {
			if i >= len(v.FB.Slots) || strings.HasPrefix(s.Name, "_") {
				continue
			}
			out = append(out, member{addr: m.addr + "." + s.Name, val: v.FB.Slots[i], typ: s.Type})
		}
		return out
	case ir.TypeArray:
		if !compositeArray(v) {
			return nil
		}
		lo := 0
		var et *ir.Type
		if m.typ != nil && m.typ.Kind == ir.TypeArray {
			lo, et = m.typ.ArrLoBound, m.typ.Elem
		}
		out := make([]member, 0, len(v.Arr))
		for i, e := range v.Arr {
			out = append(out, member{addr: m.addr + "[" + strconv.Itoa(lo+i) + "]", val: e, typ: et})
		}
		return out
	}
	return nil
}

// compositeArray says whether an array's elements are themselves structs,
// FBs or arrays — which OPC UA cannot carry as one array Variant, so the
// array is exposed as an Object with one child per element instead.
func compositeArray(v ir.Value) bool {
	if v.Kind != ir.TypeArray {
		return false
	}
	for _, e := range v.Arr {
		switch e.Kind {
		case ir.TypeStruct, ir.TypeFB, ir.TypeArray:
			return true
		}
		return false
	}
	return false
}

// isObject says whether a member browses as an Object (it has children)
// rather than a Variable (it has a value).
func isObject(m member) bool {
	switch m.val.Kind {
	case ir.TypeStruct, ir.TypeFB:
		return true
	case ir.TypeArray:
		return compositeArray(m.val)
	}
	return false
}

// dataType is the ns=0 DataType node a member's Value is encoded as. The
// store holds every integer as int64 and every real as float64, but a
// DINT is still a DINT: a declared width maps to its own OPC UA type, an
// enumeration to Int32 (OPC UA enumerations are Int32-valued), TIME to
// Duration (a Double of milliseconds). Everything else is the canonical
// 64-bit type, which is also what Sparkplug publishes.
func dataType(m member) uint32 {
	v, t := m.val, m.typ
	if v.Kind == ir.TypeArray {
		if len(v.Arr) > 0 {
			var et *ir.Type
			if t != nil && t.Kind == ir.TypeArray {
				et = t.Elem
			}
			return dataType(member{val: v.Arr[0], typ: et})
		}
		if t != nil && t.Kind == ir.TypeArray && t.Elem != nil {
			return dataType(member{val: ir.Zero(t.Elem), typ: t.Elem})
		}
		return id.BaseDataType
	}
	switch v.Kind {
	case ir.TypeBool:
		return id.Boolean
	case ir.TypeReal:
		return id.Double
	case ir.TypeTime:
		return id.Duration
	case ir.TypeString:
		return id.String
	case ir.TypeInt:
		if t != nil && t.Enum != nil {
			return id.Int32
		}
		return intType(t)
	}
	return id.BaseDataType
}

// intType picks the OPC UA integer type for a declared IEC width.
func intType(t *ir.Type) uint32 {
	if t == nil {
		return id.Int64
	}
	switch strings.ToUpper(t.Name) {
	case "SINT":
		return id.SByte
	case "USINT", "BYTE":
		return id.Byte
	case "INT":
		return id.Int16
	case "UINT", "WORD":
		return id.UInt16
	case "DINT":
		return id.Int32
	case "UDINT", "DWORD":
		return id.UInt32
	case "ULINT", "LWORD":
		return id.UInt64
	}
	return id.Int64
}

// scalar converts one scalar ir.Value to the Go value gopcua encodes as the
// Variant of type dt. An integer that does not fit its declared width is
// sent as Int64 rather than wrapped: the DataType attribute then disagrees
// with one reading, which a client can see, where a silent wrap could not
// be seen by anyone.
func scalar(v ir.Value, dt uint32) any {
	switch v.Kind {
	case ir.TypeBool:
		return v.B
	case ir.TypeReal:
		return v.F
	case ir.TypeTime:
		return float64(v.I) // Duration: milliseconds as a Double
	case ir.TypeString:
		return v.S
	case ir.TypeInt:
		n := v.I
		switch dt {
		case id.SByte:
			if n >= math.MinInt8 && n <= math.MaxInt8 {
				return int8(n)
			}
		case id.Byte:
			if n >= 0 && n <= math.MaxUint8 {
				return uint8(n)
			}
		case id.Int16:
			if n >= math.MinInt16 && n <= math.MaxInt16 {
				return int16(n)
			}
		case id.UInt16:
			if n >= 0 && n <= math.MaxUint16 {
				return uint16(n)
			}
		case id.Int32:
			if n >= math.MinInt32 && n <= math.MaxInt32 {
				return int32(n)
			}
		case id.UInt32:
			if n >= 0 && n <= math.MaxUint32 {
				return uint32(n)
			}
		case id.UInt64:
			if n >= 0 {
				return uint64(n)
			}
		}
		return n
	}
	return nil
}

// variant encodes a member's value: a scalar as itself, an array of scalars
// as a one-dimensional OPC UA array of the element type. Composite values
// have no Variant — they are Objects, read through their children.
func variant(m member) (*ua.Variant, error) {
	v := m.val
	dt := dataType(m)
	if v.Kind != ir.TypeArray {
		if isObject(m) {
			return nil, fmt.Errorf("%s is an object, not a variable", m.addr)
		}
		return ua.NewVariant(scalar(v, dt))
	}
	if compositeArray(v) {
		return nil, fmt.Errorf("%s is an array of objects, not a variable", m.addr)
	}
	switch dt {
	case id.Boolean:
		out := make([]bool, len(v.Arr))
		for i, e := range v.Arr {
			out[i] = e.B
		}
		return ua.NewVariant(out)
	case id.Double, id.Duration:
		out := make([]float64, len(v.Arr))
		for i, e := range v.Arr {
			out[i], _ = scalar(e, dt).(float64)
		}
		return ua.NewVariant(out)
	case id.String:
		out := make([]string, len(v.Arr))
		for i, e := range v.Arr {
			out[i] = e.S
		}
		return ua.NewVariant(out)
	}
	// Integers: one width for the whole array. If any element would not
	// fit the declared width the array falls back to Int64, so it stays
	// one well-typed array rather than a mixed one.
	for _, e := range v.Arr {
		if _, wide := scalar(e, dt).(int64); wide && dt != id.Int64 {
			dt = id.Int64
			break
		}
	}
	switch dt {
	case id.SByte:
		return ua.NewVariant(mapInts(v.Arr, func(n int64) int8 { return int8(n) }))
	case id.Byte:
		return ua.NewVariant(mapInts(v.Arr, func(n int64) uint8 { return uint8(n) }))
	case id.Int16:
		return ua.NewVariant(mapInts(v.Arr, func(n int64) int16 { return int16(n) }))
	case id.UInt16:
		return ua.NewVariant(mapInts(v.Arr, func(n int64) uint16 { return uint16(n) }))
	case id.Int32:
		return ua.NewVariant(mapInts(v.Arr, func(n int64) int32 { return int32(n) }))
	case id.UInt32:
		return ua.NewVariant(mapInts(v.Arr, func(n int64) uint32 { return uint32(n) }))
	case id.UInt64:
		return ua.NewVariant(mapInts(v.Arr, func(n int64) uint64 { return uint64(n) }))
	}
	return ua.NewVariant(mapInts(v.Arr, func(n int64) int64 { return n }))
}

func mapInts[T any](arr []ir.Value, f func(int64) T) []T {
	out := make([]T, len(arr))
	for i, e := range arr {
		out[i] = f(e.I)
	}
	return out
}

// statusOf maps a tag's quality onto the OPC UA status a client reads
// beside the value. Stale is Uncertain — the value is the last good one and
// still worth showing, which is exactly what Uncertain_LastUsableValue
// means — and the two Bad qualities are Bad, so a client that only checks
// the severity bit treats them the way an HMI treats a bad binding.
func statusOf(q nio.Quality) ua.StatusCode {
	switch q {
	case nio.Stale:
		return ua.StatusUncertainLastUsableValue
	case nio.Bad:
		return ua.StatusBadDeviceFailure
	case nio.NotConnected:
		return ua.StatusBadNotConnected
	}
	return ua.StatusGood
}
