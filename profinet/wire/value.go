package wire

import (
	"encoding/binary"
	"fmt"
	"math"
)

// Format is a value's wire format inside a submodule's IO data. PROFINET
// IO data is big-endian.
type Format uint8

const (
	FormatInvalid Format = iota
	FormatBool           // one bit; the binding names it with a bit offset 0–7
	FormatInt8
	FormatUint8
	FormatInt16
	FormatUint16
	FormatInt32
	FormatUint32
	FormatInt64
	FormatUint64
	FormatFloat32
	FormatFloat64
)

var formatNames = [...]string{
	FormatInvalid: "",
	FormatBool:    "bool",
	FormatInt8:    "int8",
	FormatUint8:   "uint8",
	FormatInt16:   "int16",
	FormatUint16:  "uint16",
	FormatInt32:   "int32",
	FormatUint32:  "uint32",
	FormatInt64:   "int64",
	FormatUint64:  "uint64",
	FormatFloat32: "float32",
	FormatFloat64: "float64",
}

// ParseFormat maps a manifest token to a Format.
func ParseFormat(s string) (Format, error) {
	for f, n := range formatNames {
		if n != "" && n == s {
			return Format(f), nil
		}
	}
	return FormatInvalid, fmt.Errorf("unknown format %q (bool, int8, uint8, int16, uint16, int32, uint32, int64, uint64, float32, float64)", s)
}

func (f Format) String() string {
	if int(f) < len(formatNames) {
		return formatNames[f]
	}
	return fmt.Sprintf("Format(%d)", uint8(f))
}

// Size is the bytes a value of this format occupies (a bool occupies its
// byte, shared with the other bits of that byte).
func (f Format) Size() int {
	switch f {
	case FormatBool, FormatInt8, FormatUint8:
		return 1
	case FormatInt16, FormatUint16:
		return 2
	case FormatInt32, FormatUint32, FormatFloat32:
		return 4
	case FormatInt64, FormatUint64, FormatFloat64:
		return 8
	}
	return 0
}

// IECType is the IEC 61131-3 elementary type a tag of this format has.
func (f Format) IECType() string {
	switch f {
	case FormatBool:
		return "BOOL"
	case FormatInt8:
		return "SINT"
	case FormatUint8:
		return "USINT"
	case FormatInt16:
		return "INT"
	case FormatUint16:
		return "UINT"
	case FormatInt32:
		return "DINT"
	case FormatUint32:
		return "UDINT"
	case FormatInt64:
		return "LINT"
	case FormatUint64:
		return "ULINT"
	case FormatFloat32:
		return "REAL"
	case FormatFloat64:
		return "LREAL"
	}
	return ""
}

// Decode reads one value at off (bit for FormatBool) in the shapes the
// runtime takes from a driver: bool, int64 for every integer format,
// float64 for both floats. A uint64 above math.MaxInt64 does not fit the
// tag and is an error, which the driver reports as Bad quality.
func Decode(data []byte, f Format, off int, bit uint8) (any, error) {
	if off < 0 || off+f.Size() > len(data) || f.Size() == 0 {
		return nil, fmt.Errorf("profinet: %s at byte %d is outside %d bytes of IO data", f, off, len(data))
	}
	b := data[off:]
	switch f {
	case FormatBool:
		if bit > 7 {
			return nil, fmt.Errorf("profinet: bit %d is not 0–7", bit)
		}
		return b[0]>>bit&1 == 1, nil
	case FormatInt8:
		return int64(int8(b[0])), nil
	case FormatUint8:
		return int64(b[0]), nil
	case FormatInt16:
		return int64(int16(binary.BigEndian.Uint16(b))), nil
	case FormatUint16:
		return int64(binary.BigEndian.Uint16(b)), nil
	case FormatInt32:
		return int64(int32(binary.BigEndian.Uint32(b))), nil
	case FormatUint32:
		return int64(binary.BigEndian.Uint32(b)), nil
	case FormatInt64:
		return int64(binary.BigEndian.Uint64(b)), nil
	case FormatUint64:
		u := binary.BigEndian.Uint64(b)
		if u > math.MaxInt64 {
			return nil, fmt.Errorf("profinet: uint64 %d does not fit a 64-bit signed tag", u)
		}
		return int64(u), nil
	case FormatFloat32:
		return float64(math.Float32frombits(binary.BigEndian.Uint32(b))), nil
	case FormatFloat64:
		return math.Float64frombits(binary.BigEndian.Uint64(b)), nil
	}
	return nil, fmt.Errorf("profinet: unknown format %d", f)
}

// Encode writes v at off (bit for FormatBool, leaving the byte's other
// bits alone). Integer formats round floats to nearest and refuse values
// outside the format's range rather than wrap: a wrapped setpoint
// silently commands something else (the Modbus driver's rule). On error
// data is unchanged.
func Encode(data []byte, f Format, off int, bit uint8, v any) error {
	if off < 0 || off+f.Size() > len(data) || f.Size() == 0 {
		return fmt.Errorf("profinet: %s at byte %d is outside %d bytes of IO data", f, off, len(data))
	}
	b := data[off:]
	switch f {
	case FormatBool:
		if bit > 7 {
			return fmt.Errorf("profinet: bit %d is not 0–7", bit)
		}
		on, err := toBool(v)
		if err != nil {
			return err
		}
		if on {
			b[0] |= 1 << bit
		} else {
			b[0] &^= 1 << bit
		}
		return nil
	case FormatFloat32:
		x, err := toFloat(v)
		if err != nil {
			return err
		}
		binary.BigEndian.PutUint32(b, math.Float32bits(float32(x)))
		return nil
	case FormatFloat64:
		x, err := toFloat(v)
		if err != nil {
			return err
		}
		binary.BigEndian.PutUint64(b, math.Float64bits(x))
		return nil
	}
	if f == FormatUint64 {
		u, err := toUint64(v)
		if err != nil {
			return err
		}
		binary.BigEndian.PutUint64(b, u)
		return nil
	}
	n, err := toInt64(v)
	if err != nil {
		return err
	}
	lo, hi := intRange(f)
	if n < lo || n > hi {
		return fmt.Errorf("profinet: value %v is out of %s range", v, f)
	}
	switch f.Size() {
	case 1:
		b[0] = byte(n)
	case 2:
		binary.BigEndian.PutUint16(b, uint16(n))
	case 4:
		binary.BigEndian.PutUint32(b, uint32(n))
	case 8:
		binary.BigEndian.PutUint64(b, uint64(n))
	}
	return nil
}

func intRange(f Format) (lo, hi int64) {
	switch f {
	case FormatInt8:
		return math.MinInt8, math.MaxInt8
	case FormatUint8:
		return 0, math.MaxUint8
	case FormatInt16:
		return math.MinInt16, math.MaxInt16
	case FormatUint16:
		return 0, math.MaxUint16
	case FormatInt32:
		return math.MinInt32, math.MaxInt32
	case FormatUint32:
		return 0, math.MaxUint32
	}
	return math.MinInt64, math.MaxInt64
}

// toInt64 accepts the shapes the runtime hands a driver. Integers convert
// exactly; floats round to nearest and must be finite and in int64 range.
func toInt64(v any) (int64, error) {
	switch x := v.(type) {
	case int64:
		return x, nil
	case int:
		return int64(x), nil
	case int32:
		return int64(x), nil
	case int16:
		return int64(x), nil
	case int8:
		return int64(x), nil
	case uint8:
		return int64(x), nil
	case uint16:
		return int64(x), nil
	case uint32:
		return int64(x), nil
	case uint64:
		if x > math.MaxInt64 {
			return 0, fmt.Errorf("profinet: value %d is out of int64 range", x)
		}
		return int64(x), nil
	case bool:
		if x {
			return 1, nil
		}
		return 0, nil
	case float32:
		return floatToInt64(float64(x))
	case float64:
		return floatToInt64(x)
	}
	return 0, fmt.Errorf("profinet: cannot encode %T as a number", v)
}

func floatToInt64(x float64) (int64, error) {
	r := math.Round(x)
	// 2^63 is exactly representable; anything at or above it overflows.
	if math.IsNaN(r) || r < math.MinInt64 || r >= math.MaxInt64 {
		return 0, fmt.Errorf("profinet: value %v is out of integer range", x)
	}
	return int64(r), nil
}

func toUint64(v any) (uint64, error) {
	switch x := v.(type) {
	case uint64:
		return x, nil
	case float64, float32:
		f, _ := toFloat(x)
		r := math.Round(f)
		if math.IsNaN(r) || r < 0 || r >= math.MaxUint64 {
			return 0, fmt.Errorf("profinet: value %v is out of uint64 range", v)
		}
		return uint64(r), nil
	}
	n, err := toInt64(v)
	if err != nil {
		return 0, err
	}
	if n < 0 {
		return 0, fmt.Errorf("profinet: value %v is out of uint64 range", v)
	}
	return uint64(n), nil
}

func toFloat(v any) (float64, error) {
	switch x := v.(type) {
	case float64:
		return x, nil
	case float32:
		return float64(x), nil
	}
	n, err := toInt64(v)
	if err != nil {
		if u, ok := v.(uint64); ok {
			return float64(u), nil
		}
		return 0, err
	}
	return float64(n), nil
}

// toBool accepts bool or any number (nonzero is true), the tolerance the
// tag store shows when ST assigns an INT expression to a BOOL output.
func toBool(v any) (bool, error) {
	if b, ok := v.(bool); ok {
		return b, nil
	}
	f, err := toFloat(v)
	if err != nil {
		return false, fmt.Errorf("profinet: cannot encode %T as a bool", v)
	}
	return f != 0, nil
}
