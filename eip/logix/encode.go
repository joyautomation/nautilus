package logix

import (
	"encoding/binary"
	"fmt"
	"math"
)

// EncodeScalar renders a Go value to the little-endian wire bytes of the CIP
// elementary type code. Accepts bool for BOOL, int64/int/float64 for the
// integer and bit-pattern types (float64 is truncated), and float64/int64/int
// for REAL/LREAL — the loose acceptance matches what a tag store hands back.
func EncodeScalar(code uint16, v any) ([]byte, error) {
	ti, ok := TypeByCode(code)
	if !ok {
		return nil, fmt.Errorf("logix: unknown CIP type 0x%04x", code)
	}
	switch ti.Code {
	case 0xC1:
		b, ok := toBool(v)
		if !ok {
			return nil, fmt.Errorf("logix: BOOL write needs bool, got %T", v)
		}
		if b {
			return []byte{0xFF}, nil
		}
		return []byte{0x00}, nil
	case 0xCA:
		f, ok := toFloat(v)
		if !ok {
			return nil, fmt.Errorf("logix: REAL write needs number, got %T", v)
		}
		out := make([]byte, 4)
		binary.LittleEndian.PutUint32(out, math.Float32bits(float32(f)))
		return out, nil
	case 0xCB:
		f, ok := toFloat(v)
		if !ok {
			return nil, fmt.Errorf("logix: LREAL write needs number, got %T", v)
		}
		out := make([]byte, 8)
		binary.LittleEndian.PutUint64(out, math.Float64bits(f))
		return out, nil
	}
	// Integer / bit-pattern types.
	n, ok := toInt(v)
	if !ok {
		return nil, fmt.Errorf("logix: %s write needs integer, got %T", ti.Name, v)
	}
	out := make([]byte, ti.Size)
	switch ti.Size {
	case 1:
		out[0] = byte(n)
	case 2:
		binary.LittleEndian.PutUint16(out, uint16(n))
	case 4:
		binary.LittleEndian.PutUint32(out, uint32(n))
	case 8:
		binary.LittleEndian.PutUint64(out, uint64(n))
	}
	return out, nil
}

func toBool(v any) (bool, bool) {
	switch x := v.(type) {
	case bool:
		return x, true
	case float64:
		return x != 0, true
	case int64:
		return x != 0, true
	case int:
		return x != 0, true
	}
	return false, false
}

func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int64:
		return float64(x), true
	case int:
		return float64(x), true
	}
	return 0, false
}

func toInt(v any) (int64, bool) {
	switch x := v.(type) {
	case int64:
		return x, true
	case int:
		return int64(x), true
	case float64:
		return int64(x), true
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}
