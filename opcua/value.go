// value.go is the codec between OPC UA Variants and nautilus scalars. Reads
// coerce whatever the server sends into the four kinds the tag store holds
// (bool, int64, float64, string); writes encode a nautilus value as the Go
// type the binding's declared OPC UA type demands, range-checked, so a
// setpoint that does not fit an Int16 is refused rather than wrapped.
package opcua

import (
	"fmt"
	"math"
	"time"

	"github.com/gopcua/opcua/id"
	"github.com/gopcua/opcua/ua"
)

// builtinIDs maps the ns=0 DataType node id to the type name a binding
// declares — the inverse of what `naut opcua import` reads off the server.
var builtinIDs = map[uint32]string{
	id.Boolean: "Boolean", id.SByte: "SByte", id.Byte: "Byte",
	id.Int16: "Int16", id.UInt16: "UInt16", id.Int32: "Int32", id.UInt32: "UInt32",
	id.Int64: "Int64", id.UInt64: "UInt64", id.Float: "Float", id.Double: "Double",
	id.String: "String", id.DateTime: "DateTime",
}

// TypeName resolves a DataType attribute to a binding type name; ok is
// false for anything that is not one of the thirteen scalar built-ins
// (enumerations, structures, ByteString, GUID, LocalizedText …).
func TypeName(dataType *ua.NodeID) (string, bool) {
	if dataType == nil || dataType.Namespace() != 0 || dataType.Type() != ua.NodeIDTypeNumeric && dataType.Type() != ua.NodeIDTypeTwoByte && dataType.Type() != ua.NodeIDTypeFourByte {
		return "", false
	}
	name, ok := builtinIDs[dataType.IntID()]
	return name, ok
}

// toValue coerces a read Variant into a nautilus scalar. Arrays and
// compound values report ok=false: the binding is marked Bad rather than
// delivering a value the tag store cannot hold.
func toValue(v *ua.Variant) (any, bool) {
	if v == nil || v.Has(ua.VariantArrayValues) {
		return nil, false
	}
	switch x := v.Value().(type) {
	case bool:
		return x, true
	case int8:
		return int64(x), true
	case uint8:
		return int64(x), true
	case int16:
		return int64(x), true
	case uint16:
		return int64(x), true
	case int32:
		return int64(x), true
	case uint32:
		return int64(x), true
	case int64:
		return x, true
	case uint64:
		return int64(x), true
	case float32:
		return float64(x), true
	case float64:
		return x, true
	case string:
		return x, true
	case time.Time:
		return x.UnixMilli(), true
	}
	return nil, false
}

// toVariant encodes a nautilus value as the binding's declared type.
func toVariant(typ string, v any) (*ua.Variant, error) {
	switch typ {
	case "Boolean":
		b, err := toBool(v)
		if err != nil {
			return nil, err
		}
		return ua.NewVariant(b)
	case "String":
		switch x := v.(type) {
		case string:
			return ua.NewVariant(x)
		default:
			return ua.NewVariant(fmt.Sprint(v))
		}
	case "Float":
		f, err := toFloat(v)
		if err != nil {
			return nil, err
		}
		if math.Abs(f) > math.MaxFloat32 && !math.IsInf(f, 0) {
			return nil, fmt.Errorf("%v does not fit a Float", v)
		}
		return ua.NewVariant(float32(f))
	case "Double":
		f, err := toFloat(v)
		if err != nil {
			return nil, err
		}
		return ua.NewVariant(f)
	case "DateTime":
		n, err := toInt(v)
		if err != nil {
			return nil, err
		}
		return ua.NewVariant(time.UnixMilli(n).UTC())
	}
	n, err := toInt(v)
	if err != nil {
		return nil, err
	}
	var lo, hi int64
	var mk func(int64) any
	switch typ {
	case "SByte":
		lo, hi, mk = math.MinInt8, math.MaxInt8, func(n int64) any { return int8(n) }
	case "Byte":
		lo, hi, mk = 0, math.MaxUint8, func(n int64) any { return uint8(n) }
	case "Int16":
		lo, hi, mk = math.MinInt16, math.MaxInt16, func(n int64) any { return int16(n) }
	case "UInt16":
		lo, hi, mk = 0, math.MaxUint16, func(n int64) any { return uint16(n) }
	case "Int32":
		lo, hi, mk = math.MinInt32, math.MaxInt32, func(n int64) any { return int32(n) }
	case "UInt32":
		lo, hi, mk = 0, math.MaxUint32, func(n int64) any { return uint32(n) }
	case "Int64":
		lo, hi, mk = math.MinInt64, math.MaxInt64, func(n int64) any { return n }
	case "UInt64":
		lo, hi, mk = 0, math.MaxInt64, func(n int64) any { return uint64(n) }
	default:
		return nil, fmt.Errorf("unknown type %q", typ)
	}
	if n < lo || n > hi {
		return nil, fmt.Errorf("%d does not fit %s", n, typ)
	}
	return ua.NewVariant(mk(n))
}

func toBool(v any) (bool, error) {
	switch x := v.(type) {
	case bool:
		return x, nil
	case int64:
		return x != 0, nil
	case int:
		return x != 0, nil
	case float64:
		return x != 0, nil
	}
	return false, fmt.Errorf("%v (%T) is not a bool", v, v)
}

func toFloat(v any) (float64, error) {
	switch x := v.(type) {
	case float64:
		return x, nil
	case float32:
		return float64(x), nil
	case int64:
		return float64(x), nil
	case int:
		return float64(x), nil
	case bool:
		if x {
			return 1, nil
		}
		return 0, nil
	}
	return 0, fmt.Errorf("%v (%T) is not a number", v, v)
}

func toInt(v any) (int64, error) {
	switch x := v.(type) {
	case int64:
		return x, nil
	case int:
		return int64(x), nil
	case int32:
		return int64(x), nil
	case float64:
		if x != math.Trunc(x) {
			return 0, fmt.Errorf("%v is not an integer", x)
		}
		if x < math.MinInt64 || x > math.MaxInt64 {
			return 0, fmt.Errorf("%v does not fit an integer", x)
		}
		return int64(x), nil
	case bool:
		if x {
			return 1, nil
		}
		return 0, nil
	}
	return 0, fmt.Errorf("%v (%T) is not an integer", v, v)
}

// good reports whether a StatusCode's severity is Good (neither the Bad
// nor the Uncertain bit set). Uncertain is treated as not-good on purpose:
// an operator screen that shows an "Uncertain_LastUsableValue" as a live
// reading is the heuristic the Quality enum exists to replace.
func good(s ua.StatusCode) bool { return s&(ua.StatusBad|ua.StatusUncertain) == 0 }
