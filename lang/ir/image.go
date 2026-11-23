package ir

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"math"
	"strconv"
)

// FrameFormat versions the leaf order FlattenInto writes and ApplyLeaves
// reads (a pair's state stream, #710 S1). It is part of every layout hash,
// so a change to the walk changes the hash and an image of the old order is
// never applied.
const FrameFormat = 1

// Leaf is one elementary value of a flattened frame, in a third of the
// space of a Value (32 bytes against 112), which is most of what the copy
// costs. Bits holds a BOOL as 0 or 1, an integer and a TIME as themselves,
// a REAL as its IEEE 754 bits; S holds a STRING, and an enumeration
// member's name on an integer.
type Leaf struct {
	Bits uint64
	S    string
	Kind TypeKind
}

// LeafOf is v's leaf; v must be elementary.
func LeafOf(v *Value) Leaf {
	l := Leaf{Kind: v.Kind, S: v.S}
	switch v.Kind {
	case TypeBool:
		if v.B {
			l.Bits = 1
		}
	case TypeReal:
		l.Bits = math.Float64bits(v.F)
	default:
		l.Bits = uint64(v.I)
	}
	return l
}

// Value is the leaf as a Value.
func (l Leaf) Value() Value {
	v := Value{Kind: l.Kind, S: l.S}
	switch l.Kind {
	case TypeBool:
		v.B = l.Bits != 0
	case TypeReal:
		v.F = math.Float64frombits(l.Bits)
	default:
		v.I = int64(l.Bits)
	}
	return v
}

// FlattenInto writes the frame's leaves into dst, reusing its storage, and
// returns it. A leaf is an elementary value (BOOL, an integer, REAL, TIME,
// STRING); a STRUCT, an ARRAY and a function-block instance are walked in
// order (fields, elements, the instance's slots), depth first. Two frames
// of the same layout (Program.Layout) have the same number of leaves of the
// same kinds in the same order.
//
// It copies values only: a leaf keeps no pointer into the frame (a STRING
// shares its immutable bytes), so the frame may run on while the copy is
// encoded elsewhere. After the first call with enough capacity it makes no
// allocation, which is what lets a scan take it at its boundary.
func FlattenInto(dst []Leaf, f *Frame) []Leaf {
	dst = dst[:0]
	if f == nil {
		return dst
	}
	for i := range f.Slots {
		dst = flattenValue(dst, &f.Slots[i])
	}
	return dst
}

func flattenValue(dst []Leaf, v *Value) []Leaf {
	switch v.Kind {
	case TypeArray:
		for i := range v.Arr {
			dst = flattenValue(dst, &v.Arr[i])
		}
		return dst
	case TypeStruct:
		for i := range v.Fld {
			dst = flattenValue(dst, &v.Fld[i])
		}
		return dst
	case TypeFB:
		if v.FB != nil {
			for i := range v.FB.Slots {
				dst = flattenValue(dst, &v.FB.Slots[i])
			}
		}
		return dst
	}
	return append(dst, LeafOf(v))
}

// ErrLeaves is returned by ApplyLeaves when the leaves do not fit the
// frame: another count, or a leaf of another kind.
var ErrLeaves = errors.New("ir: the image's leaves do not fit this frame")

// ApplyLeaves writes leaves, as FlattenInto wrote them, back into f. Each
// leaf must have the kind the frame holds at its place, and every leaf must
// be used: otherwise it returns ErrLeaves (wrapped, with the place), and f
// may be partly written, so apply to a frame that is not running.
func ApplyLeaves(f *Frame, leaves []Leaf) error {
	if f == nil {
		if len(leaves) == 0 {
			return nil
		}
		return fmt.Errorf("%w: %d leaves for no frame", ErrLeaves, len(leaves))
	}
	n := 0
	for i := range f.Slots {
		var err error
		if n, err = applyValue(&f.Slots[i], leaves, n); err != nil {
			return fmt.Errorf("%w (slot %d)", err, i)
		}
	}
	if n != len(leaves) {
		return fmt.Errorf("%w: %d leaves, the frame holds %d", ErrLeaves, len(leaves), n)
	}
	return nil
}

func applyValue(v *Value, leaves []Leaf, n int) (int, error) {
	var err error
	switch v.Kind {
	case TypeArray:
		for i := range v.Arr {
			if n, err = applyValue(&v.Arr[i], leaves, n); err != nil {
				return n, err
			}
		}
		return n, nil
	case TypeStruct:
		for i := range v.Fld {
			if n, err = applyValue(&v.Fld[i], leaves, n); err != nil {
				return n, err
			}
		}
		return n, nil
	case TypeFB:
		if v.FB != nil {
			for i := range v.FB.Slots {
				if n, err = applyValue(&v.FB.Slots[i], leaves, n); err != nil {
					return n, err
				}
			}
		}
		return n, nil
	}
	if n >= len(leaves) {
		return n, fmt.Errorf("%w: ran out at leaf %d", ErrLeaves, n)
	}
	l := &leaves[n]
	if l.Kind != v.Kind {
		return n, fmt.Errorf("%w: leaf %d is %s, the frame holds %s", ErrLeaves, n, l.Kind, v.Kind)
	}
	w := l.Value()
	v.I, v.F, v.B, v.S = w.I, w.F, w.B, w.S
	return n + 1, nil
}

// Layout identifies the shape of the program's frame: every slot's name,
// kind and type, walked into every STRUCT, ARRAY and function-block type
// (a built-in's internals too), plus FrameFormat. Two programs with the
// same Layout flatten to leaves that fit each other's frames, slot for
// slot. Twelve bytes of SHA-256 in hex, like the source hashes.
//
// It is computed from the compiled program, so an online edit that changes
// a declaration changes it, and one that changes only the body does not.
// Declared initial values are not part of it (`K : REAL := 2.0` and `:= 3.0`
// have one layout), nor is the body: a layout match says the leaves fit,
// not that the two programs are the same. A hot takeover (S2) also needs the
// same build and the same running sources.
func (prog *Program) Layout() string {
	if prog == nil {
		return ""
	}
	h := sha256.New()
	w := layoutWriter{h: h}
	w.int(FrameFormat)
	w.int(int64(len(prog.Slots)))
	for _, s := range prog.Slots {
		w.str(s.Name)
		w.int(int64(s.Kind))
		w.str(s.Global)
		w.flags(s.Retained, s.Constant, s.Temp)
		w.typ(s.Type, 0)
	}
	sum := h.Sum(nil)
	return hex.EncodeToString(sum[:12])
}

// maxLayoutDepth bounds the type walk; no declared type nests this deep.
const maxLayoutDepth = 64

type layoutWriter struct {
	h   hash.Hash
	buf [binary.MaxVarintLen64]byte
}

func (w *layoutWriter) int(v int64) {
	n := binary.PutVarint(w.buf[:], v)
	w.h.Write(w.buf[:n])
}

func (w *layoutWriter) flags(fs ...bool) {
	var b int64
	for i, f := range fs {
		if f {
			b |= 1 << i
		}
	}
	w.int(b)
}

func (w *layoutWriter) str(s string) {
	w.int(int64(len(s)))
	w.h.Write([]byte(s))
}

func (w *layoutWriter) typ(t *Type, depth int) {
	if t == nil {
		w.int(-1)
		return
	}
	if depth > maxLayoutDepth {
		w.str("depth:" + strconv.Itoa(depth))
		return
	}
	w.int(int64(t.Kind))
	w.str(t.Name) // LREAL against REAL, DINT against INT
	switch t.Kind {
	case TypeInt:
		if t.Enum != nil {
			w.str(t.Enum.Name)
			w.int(int64(len(t.Enum.Members)))
			for _, m := range t.Enum.Members {
				w.str(m.Name)
				w.int(m.Value)
			}
		} else {
			w.int(-1)
		}
	case TypeArray:
		w.int(int64(t.ArrLen))
		w.int(int64(t.ArrLoBound))
		w.typ(t.Elem, depth+1)
	case TypeStruct:
		if t.Struct == nil {
			w.int(-1)
			return
		}
		w.str(t.Struct.Name)
		w.int(int64(len(t.Struct.Fields)))
		for _, f := range t.Struct.Fields {
			w.str(f.Name)
			w.typ(f.Type, depth+1)
		}
	case TypeFB:
		if t.FB == nil {
			w.int(-1)
			return
		}
		w.str(t.FB.Name)
		all := t.FB.AllSlots()
		w.int(int64(len(all)))
		for _, s := range all {
			w.str(s.Name)
			w.flags(s.Constant)
			w.typ(s.Type, depth+1)
		}
	}
}
