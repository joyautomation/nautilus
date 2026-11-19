// bytes.go is the byte types (#705): ByteString and ByteArray. Both are a
// binding's wire type for octets (IO-Link process data and ISDU values);
// the tag store holds them as a STRING of lowercase hex, two digits per
// octet, so the language side stays vendor-neutral and a length that
// varies with the attached device needs no fixed array.
//
//   - ByteString is the built-in type (OPC UA Part 6, i=15).
//   - ByteArray is Byte[]: a one-dimensional array of Byte, which is how
//     the IO-Link companion specification (OPC 30120) types process data
//     and the ISDU arguments.
//
// A read accepts either shape for either type (gopcua holds both as
// []byte); a write sends the declared one.
package opcua

import (
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/gopcua/opcua/ua"
)

// The byte types a binding may declare.
const (
	ByteString = "ByteString"
	ByteArray  = "ByteArray"
)

// MaxBytes is the most octets a byte-typed value may hold: an IO-Link ISDU
// value is up to 232 octets and process data up to 32. A longer value is
// refused, never truncated.
const MaxBytes = 232

func isByteType(typ string) bool { return typ == ByteString || typ == ByteArray }

// valueOf is toValue for a binding: a byte-typed binding takes the octets
// as lowercase hex, anything else is a scalar. The error says why a value
// is refused.
func (d *Driver) valueOf(b *binding, v *ua.Variant) (any, bool, error) {
	if !isByteType(b.Type) {
		val, ok := toValue(v)
		if !ok {
			return nil, false, errors.New("value is not a scalar built-in")
		}
		return val, true, nil
	}
	s, err := bytesToHex(v)
	if err != nil {
		return nil, false, err
	}
	return s, true, nil
}

func bytesToHex(v *ua.Variant) (string, error) {
	if v == nil {
		return "", errors.New("no value")
	}
	var raw []byte
	switch x := v.Value().(type) {
	case []byte:
		raw = x
	case ua.ByteArray:
		raw = x
	default:
		return "", fmt.Errorf("value is a %T, not octets", v.Value())
	}
	if len(raw) > MaxBytes {
		return "", fmt.Errorf("%d octets do not fit: a byte value holds at most %d", len(raw), MaxBytes)
	}
	return hex.EncodeToString(raw), nil
}

// bytesVariant encodes hex text (either case, an even number of digits,
// nothing else) as the declared byte type.
func bytesVariant(typ string, v any) (*ua.Variant, error) {
	s, ok := v.(string)
	if !ok {
		return nil, fmt.Errorf("%T is not hex text", v)
	}
	raw, err := hex.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("%q is not hex text (an even number of hex digits): %w", s, err)
	}
	if len(raw) > MaxBytes {
		return nil, fmt.Errorf("%d octets do not fit: a byte value holds at most %d", len(raw), MaxBytes)
	}
	if typ == ByteArray {
		return ua.NewVariant(ua.ByteArray(raw))
	}
	return ua.NewVariant(raw)
}
