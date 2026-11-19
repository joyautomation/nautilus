package iodd

import (
	"bytes"
	"fmt"
	"math"
	"unicode/utf8"
)

// The process data codec (docs/design/iolink.md §5).
//
// Process data is an octet string. A record item's bitOffset counts from
// the least significant bit of the LAST octet: the image is one
// big-endian unsigned integer and an item occupies bits bitOffset ..
// bitOffset+bitLength-1 of it. (The IO-Link Community's standard
// definitions show it: V_DirectParameters_1's subindex 1, the first
// octet, is at bitOffset 120 of 128.)

// Check reports whether the codec can decode (and, for process data out,
// encode) every item of the image. Parse accepts what Check refuses,
// since an IODD is still worth reading for its parameters.
func (im *Image) Check() error {
	for _, it := range im.Items {
		switch it.Type.Kind {
		case Unsupported:
			return fmt.Errorf("%s is a %s, which nautilus does not decode yet", it.label(), it.Type.Name)
		case String, OctetString:
			if it.BitOffset%8 != 0 {
				return fmt.Errorf("%s: a %s at bitOffset %d, not on an octet boundary", it.label(), it.Type.Name, it.BitOffset)
			}
		}
	}
	return nil
}

// Decode decodes one image of process data. raw must be exactly the
// image's length: a short image is an error, never zero-padded. A value
// is a bool, uint64, int64, float32, string or []byte by the item's
// kind; a scaled integer is a float64 (raw × gradient + offset).
func (im *Image) Decode(raw []byte) ([]any, error) {
	if len(raw) != im.Octets() {
		return nil, fmt.Errorf("process data is %d octets; the IODD's is %d", len(raw), im.Octets())
	}
	if err := im.Check(); err != nil {
		return nil, err
	}
	out := make([]any, len(im.Items))
	for i, it := range im.Items {
		v, err := decodeItem(it, raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", it.label(), err)
		}
		out[i] = v
	}
	return out, nil
}

func decodeItem(it Item, raw []byte) (any, error) {
	t := it.Type
	switch t.Kind {
	case Boolean:
		return bits(raw, it.BitOffset, 1) == 1, nil
	case UInteger:
		u := bits(raw, it.BitOffset, t.BitLength)
		if it.Scaled {
			return float64(u)*it.Gradient + it.Offset, nil
		}
		return u, nil
	case Integer:
		u := bits(raw, it.BitOffset, t.BitLength)
		// Sign-extend from the item's top bit.
		s := int64(u<<(64-t.BitLength)) >> (64 - t.BitLength)
		if it.Scaled {
			return float64(s)*it.Gradient + it.Offset, nil
		}
		return s, nil
	case Float32:
		return math.Float32frombits(uint32(bits(raw, it.BitOffset, 32))), nil
	case String:
		b := bytes.TrimRight(octets(raw, it.BitOffset, t.FixedLength), "\x00")
		if t.Encoding == "US-ASCII" {
			for _, c := range b {
				if c >= 0x80 {
					return nil, fmt.Errorf("octet 0x%02X in a US-ASCII string", c)
				}
			}
		} else if !utf8.Valid(b) {
			return nil, fmt.Errorf("not valid UTF-8")
		}
		return string(b), nil
	case OctetString:
		return append([]byte(nil), octets(raw, it.BitOffset, t.FixedLength)...), nil
	}
	return nil, fmt.Errorf("a %s is not decoded", t.Name)
}

// bits returns n (≤ 64) bits of raw starting at bit off, counted from the
// least significant bit of the last octet.
func bits(raw []byte, off, n int) uint64 {
	var v uint64
	for i := n - 1; i >= 0; i-- {
		p := off + i
		b := raw[len(raw)-1-p/8] >> (p % 8) & 1
		v = v<<1 | uint64(b)
	}
	return v
}

// setBits writes the low n bits of v at bit off, as bits reads them.
func setBits(raw []byte, off, n int, v uint64) {
	for i := range n {
		p := off + i
		idx := len(raw) - 1 - p/8
		mask := byte(1) << (p % 8)
		if v>>i&1 == 1 {
			raw[idx] |= mask
		} else {
			raw[idx] &^= mask
		}
	}
}

// octets returns the n octets of an octet-aligned item at bit off: its
// first octet is the most significant.
func octets(raw []byte, off, n int) []byte {
	end := len(raw) - off/8
	return raw[end-n : end]
}

// Encode builds one image of process data out from a value per item, in
// item order. A nil value encodes as zero. A value that does not fit its
// item is refused, never wrapped or truncated. A scaled item takes its
// value in engineering units and is encoded as round((v − offset) /
// gradient).
func (im *Image) Encode(values []any) ([]byte, error) {
	if len(values) != len(im.Items) {
		return nil, fmt.Errorf("%d values for %d items", len(values), len(im.Items))
	}
	if err := im.Check(); err != nil {
		return nil, err
	}
	raw := make([]byte, im.Octets())
	for i, it := range im.Items {
		if values[i] == nil {
			continue
		}
		if err := encodeItem(raw, it, values[i]); err != nil {
			return nil, fmt.Errorf("%s: %w", it.label(), err)
		}
	}
	return raw, nil
}

func encodeItem(raw []byte, it Item, v any) error {
	t := it.Type
	switch t.Kind {
	case Boolean:
		b, ok := v.(bool)
		if !ok {
			return fmt.Errorf("%v (%T) is not a BOOL", v, v)
		}
		setBits(raw, it.BitOffset, 1, map[bool]uint64{false: 0, true: 1}[b])
		return nil
	case UInteger, Integer:
		n, err := integerOf(it, v)
		if err != nil {
			return err
		}
		if t.Kind == UInteger {
			if n.neg || (t.BitLength < 64 && n.u>>t.BitLength != 0) {
				return fmt.Errorf("%s does not fit a %d-bit unsigned integer", n, t.BitLength)
			}
			setBits(raw, it.BitOffset, t.BitLength, n.u)
			return nil
		}
		lo, hi := -(int64(1) << (t.BitLength - 1)), int64(1)<<(t.BitLength-1)-1
		if t.BitLength == 64 {
			lo, hi = math.MinInt64, math.MaxInt64
		}
		s, ok := n.int64()
		if !ok || s < lo || s > hi {
			return fmt.Errorf("%s does not fit a %d-bit integer", n, t.BitLength)
		}
		setBits(raw, it.BitOffset, t.BitLength, uint64(s))
		return nil
	case Float32:
		var f float32
		switch x := v.(type) {
		case float32:
			f = x
		case float64:
			if !math.IsInf(x, 0) && !math.IsNaN(x) && math.Abs(x) > math.MaxFloat32 {
				return fmt.Errorf("%g does not fit a Float32", x)
			}
			f = float32(x)
		default:
			return fmt.Errorf("%v (%T) is not a REAL", v, v)
		}
		setBits(raw, it.BitOffset, 32, uint64(math.Float32bits(f)))
		return nil
	case String:
		s, ok := v.(string)
		if !ok {
			return fmt.Errorf("%v (%T) is not a STRING", v, v)
		}
		if len(s) > t.FixedLength {
			return fmt.Errorf("%q is %d octets; the item holds %d", s, len(s), t.FixedLength)
		}
		if t.Encoding == "US-ASCII" {
			for i := 0; i < len(s); i++ {
				if s[i] >= 0x80 {
					return fmt.Errorf("%q is not US-ASCII", s)
				}
			}
		}
		dst := octets(raw, it.BitOffset, t.FixedLength)
		copy(dst, s)
		clear(dst[len(s):])
		return nil
	case OctetString:
		b, ok := v.([]byte)
		if !ok {
			return fmt.Errorf("%v (%T) is not an array of BYTE", v, v)
		}
		if len(b) > t.FixedLength {
			return fmt.Errorf("%d octets; the item holds %d", len(b), t.FixedLength)
		}
		dst := octets(raw, it.BitOffset, t.FixedLength)
		copy(dst, b)
		clear(dst[len(b):])
		return nil
	}
	return fmt.Errorf("a %s is not encoded", t.Name)
}

// integer is a value on its way into an integer item: a magnitude and a
// sign, so the whole uint64 and int64 ranges both fit.
type integer struct {
	u   uint64
	neg bool
}

func (n integer) String() string {
	if n.neg {
		return fmt.Sprintf("-%d", n.u)
	}
	return fmt.Sprint(n.u)
}

func (n integer) int64() (int64, bool) {
	if n.neg {
		if n.u > 1<<63 {
			return 0, false
		}
		return -int64(n.u-1) - 1, true
	}
	if n.u > math.MaxInt64 {
		return 0, false
	}
	return int64(n.u), true
}

func integerOf(it Item, v any) (integer, error) {
	if it.Scaled {
		var f float64
		switch x := v.(type) {
		case float64:
			f = x
		case float32:
			f = float64(x)
		case int64:
			f = float64(x)
		case uint64:
			f = float64(x)
		case int:
			f = float64(x)
		default:
			return integer{}, fmt.Errorf("%v (%T) is not a number", v, v)
		}
		r := math.Round((f - it.Offset) / it.Gradient)
		if math.IsNaN(r) || math.IsInf(r, 0) || math.Abs(r) >= 1<<63 {
			return integer{}, fmt.Errorf("%g is out of range", f)
		}
		if r < 0 {
			return integer{u: uint64(-r), neg: true}, nil
		}
		return integer{u: uint64(r)}, nil
	}
	switch x := v.(type) {
	case uint64:
		return integer{u: x}, nil
	case int64:
		if x < 0 {
			return integer{u: uint64(-(x + 1)) + 1, neg: true}, nil
		}
		return integer{u: uint64(x)}, nil
	case int:
		return integerOf(it, int64(x))
	}
	return integer{}, fmt.Errorf("%v (%T) is not an integer", v, v)
}
