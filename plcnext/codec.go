package plcnext

// codec.go reads and writes bindings against the process image. Pure
// functions over byte slices: the tests pin them without a controller.

import (
	"encoding/binary"
	"fmt"
	"math"
)

func order(byteOrder string) binary.ByteOrder {
	if byteOrder == ByteOrderLittle {
		return binary.LittleEndian
	}
	return binary.BigEndian
}

// decode reads one binding out of the image. Bits come back as bool,
// everything else as float64 after scaling (scale 0 means 1).
func decode(b Binding, img []byte, byteOrder string) (any, error) {
	f := b.format()
	if b.Offset+f.Bytes() > len(img) {
		return nil, fmt.Errorf("tag %s: %s at offset %d, image is %d bytes", b.Name, f, b.Offset, len(img))
	}
	bo := order(byteOrder)
	span := img[b.Offset : b.Offset+f.Bytes()]
	var raw float64
	switch f.Kind {
	case "bit":
		return bo.Uint16(span)>>uint(f.Bit)&1 == 1, nil
	case "ch":
		i, bit := f.bitAt(byteOrder)
		return span[i]>>uint(bit)&1 == 1, nil
	case "uint8":
		raw = float64(span[0])
	case "int8":
		raw = float64(int8(span[0]))
	case "uint16":
		raw = float64(bo.Uint16(span))
	case "int16":
		raw = float64(int16(bo.Uint16(span)))
	case "uint32":
		raw = float64(bo.Uint32(span))
	case "int32":
		raw = float64(int32(bo.Uint32(span)))
	case "float32":
		raw = float64(math.Float32frombits(bo.Uint32(span)))
	}
	scale := b.Scale
	if scale == 0 {
		scale = 1
	}
	return raw*scale + b.Bias, nil
}

// encode writes one engineering value into the image in place: a bit
// merges into its word, a number is inverse-scaled and rounded into the
// format's range.
func encode(b Binding, v any, img []byte, byteOrder string) error {
	f := b.format()
	if b.Offset+f.Bytes() > len(img) {
		return fmt.Errorf("tag %s: %s at offset %d, image is %d bytes", b.Name, f, b.Offset, len(img))
	}
	bo := order(byteOrder)
	span := img[b.Offset : b.Offset+f.Bytes()]
	if f.Kind == "bit" {
		on, err := truthy(v)
		if err != nil {
			return fmt.Errorf("tag %s: %w", b.Name, err)
		}
		w := bo.Uint16(span)
		if on {
			w |= 1 << uint(f.Bit)
		} else {
			w &^= 1 << uint(f.Bit)
		}
		bo.PutUint16(span, w)
		return nil
	}
	if f.Kind == "ch" {
		on, err := truthy(v)
		if err != nil {
			return fmt.Errorf("tag %s: %w", b.Name, err)
		}
		i, bit := f.bitAt(byteOrder)
		if on {
			span[i] |= 1 << uint(bit)
		} else {
			span[i] &^= 1 << uint(bit)
		}
		return nil
	}
	x, err := number(v)
	if err != nil {
		return fmt.Errorf("tag %s: %w", b.Name, err)
	}
	scale := b.Scale
	if scale == 0 {
		scale = 1
	}
	raw := (x - b.Bias) / scale
	switch f.Kind {
	case "float32":
		bo.PutUint32(span, math.Float32bits(float32(raw)))
		return nil
	}
	r := math.Round(raw)
	switch f.Kind {
	case "uint8":
		span[0] = uint8(clamp(r, 0, math.MaxUint8))
	case "int8":
		span[0] = uint8(int8(clamp(r, math.MinInt8, math.MaxInt8)))
	case "uint16":
		bo.PutUint16(span, uint16(clamp(r, 0, math.MaxUint16)))
	case "int16":
		bo.PutUint16(span, uint16(int16(clamp(r, math.MinInt16, math.MaxInt16))))
	case "uint32":
		bo.PutUint32(span, uint32(clamp(r, 0, math.MaxUint32)))
	case "int32":
		bo.PutUint32(span, uint32(int32(clamp(r, math.MinInt32, math.MaxInt32))))
	}
	return nil
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func truthy(v any) (bool, error) {
	switch x := v.(type) {
	case bool:
		return x, nil
	case float64:
		return x != 0, nil
	case int64:
		return x != 0, nil
	case int:
		return x != 0, nil
	}
	return false, fmt.Errorf("cannot write %T to a bit", v)
}

func number(v any) (float64, error) {
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
	return 0, fmt.Errorf("cannot write %T to a number", v)
}
