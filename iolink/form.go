// Package iolink is the IO-Link layer above a master's transport
// (docs/design/iolink.md): it turns the points a master profile's iolink:
// block names into a port's process data image, and back, and (from #705
// PR 4) derives and adapts the device on each port.
package iolink

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/joyautomation/nautilus/devprofile"
)

// Image turns the value a master's pd-in point holds into the port's
// process data image of octets octets (the IODD's length).
//
//   - u32: the integer, written out as four big-endian octets; an image
//     shorter than four is the LAST octets, and the leading ones must be
//     zero (docs/design/iolink.md §3.1). Which end a short image sits at
//     is not in the P+F manual: if the master put it at the other end,
//     the leading octets are not zero and this is an error, so a wrong
//     guess reads Bad rather than plausible wrong values. An image longer
//     than four octets cannot be carried.
//   - hex: two hex digits per octet, nothing else (no 0x, no spaces); the
//     text must be exactly octets octets long.
func Image(form string, v any, octets int) ([]byte, error) {
	switch form {
	case devprofile.FormU32:
		if octets < 1 || octets > 4 {
			return nil, fmt.Errorf("u32: a %d-octet image does not fit the four octets of a U32", octets)
		}
		u, err := u32Of(v)
		if err != nil {
			return nil, err
		}
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], u)
		for _, x := range b[:4-octets] {
			if x != 0 {
				return nil, fmt.Errorf("u32: 0x%08X does not hold a %d-octet image in its last octets (its leading octets are not zero)", u, octets)
			}
		}
		return append([]byte(nil), b[4-octets:]...), nil
	case devprofile.FormHex:
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("hex: %v (%T) is not text", v, v)
		}
		if len(s) != 2*octets {
			return nil, fmt.Errorf("hex: %q is %d digits; a %d-octet image is %d", s, len(s), octets, 2*octets)
		}
		b, err := hex.DecodeString(s)
		if err != nil {
			return nil, fmt.Errorf("hex: %q is not hex digits", s)
		}
		return b, nil
	}
	return nil, fmt.Errorf("form %q is not %s or %s", form, devprofile.FormU32, devprofile.FormHex)
}

// Carrier turns a port's process data out image into the value its
// master's pd-out point is written with: the inverse of Image.
func Carrier(form string, image []byte) (any, error) {
	switch form {
	case devprofile.FormU32:
		if len(image) < 1 || len(image) > 4 {
			return nil, fmt.Errorf("u32: a %d-octet image does not fit the four octets of a U32", len(image))
		}
		var b [4]byte
		copy(b[4-len(image):], image)
		return binary.BigEndian.Uint32(b[:]), nil
	case devprofile.FormHex:
		if len(image) == 0 {
			return nil, fmt.Errorf("hex: an empty image")
		}
		return strings.ToUpper(hex.EncodeToString(image)), nil
	}
	return nil, fmt.Errorf("form %q is not %s or %s", form, devprofile.FormU32, devprofile.FormHex)
}

// u32Of takes the integer a UDINT or DWORD point delivers.
func u32Of(v any) (uint32, error) {
	switch x := v.(type) {
	case uint32:
		return x, nil
	case uint64:
		if x <= 0xFFFFFFFF {
			return uint32(x), nil
		}
	case int64:
		if x >= 0 && x <= 0xFFFFFFFF {
			return uint32(x), nil
		}
	case int:
		if x >= 0 && int64(x) <= 0xFFFFFFFF {
			return uint32(x), nil
		}
	default:
		return 0, fmt.Errorf("u32: %v (%T) is not an integer", v, v)
	}
	return 0, fmt.Errorf("u32: %v is not a 32-bit unsigned value", v)
}
