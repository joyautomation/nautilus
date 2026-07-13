package logix

import (
	"encoding/binary"
	"fmt"
	"math"

	"github.com/joyautomation/nautilus/eip/cip"
)

// Value is a decoded tag value: a scalar, an array, or a struct tree. Exactly
// one of Scalar / Elems / Fields is populated. Scalars normalize to Go types:
// BOOL → bool, integer and bit-pattern types → int64, REAL/LREAL → float64,
// Logix strings → string.
type Value struct {
	Type   string // elementary type name or template name
	Scalar any
	Elems  []Value
	Fields []Field
}

// Field is one named member of a struct Value, in template wire order.
type Field struct {
	Name  string
	Value Value
}

// Registry indexes uploaded templates for decoding struct reads.
type Registry struct {
	byID     map[uint16]*Template
	byHandle map[uint16]*Template
	byName   map[string]*Template
}

// NewRegistry builds a Registry over browsed templates.
func NewRegistry(templates map[uint16]*Template) *Registry {
	r := &Registry{
		byID:     make(map[uint16]*Template, len(templates)),
		byHandle: make(map[uint16]*Template, len(templates)),
		byName:   make(map[string]*Template, len(templates)),
	}
	for id, t := range templates {
		r.byID[id] = t
		r.byHandle[t.Handle] = t
		r.byName[t.Name] = t
	}
	return r
}

// ByName resolves a template by Logix type name.
func (r *Registry) ByName(name string) (*Template, bool) {
	t, ok := r.byName[name]
	return t, ok
}

// ByID resolves a template by Template-class instance ID.
func (r *Registry) ByID(id uint16) (*Template, bool) {
	t, ok := r.byID[id]
	return t, ok
}

// Decode interprets a RawTag read. Struct values resolve their template via
// the structure handle; atomic values decode by CIP type code, yielding an
// array when the payload holds multiple elements.
func (r *Registry) Decode(raw RawTag) (Value, error) {
	if raw.Type == cip.TypeStruct {
		t, ok := r.byHandle[raw.Handle]
		if !ok {
			return Value{}, fmt.Errorf("logix: no template with handle 0x%04x", raw.Handle)
		}
		return r.DecodeStructData(t, raw.Data)
	}
	ti, ok := TypeByCode(raw.Type)
	if !ok {
		return Value{}, fmt.Errorf("logix: unknown CIP type 0x%04x", raw.Type)
	}
	n := len(raw.Data) / ti.Size
	if n <= 0 {
		return Value{}, fmt.Errorf("logix: %s payload too short (%d bytes)", ti.Name, len(raw.Data))
	}
	if n == 1 {
		s, err := decodeElementary(ti, raw.Data, 0)
		if err != nil {
			return Value{}, err
		}
		return Value{Type: ti.Name, Scalar: s}, nil
	}
	elems := make([]Value, n)
	for i := 0; i < n; i++ {
		s, err := decodeElementary(ti, raw.Data, i*ti.Size)
		if err != nil {
			return Value{}, err
		}
		elems[i] = Value{Type: ti.Name, Scalar: s}
	}
	return Value{Type: ti.Name, Elems: elems}, nil
}

// DecodeStructData decodes one instance of template t from raw bytes. If the
// payload carries multiple back-to-back instances (a whole array of UDTs),
// the result is an array Value.
func (r *Registry) DecodeStructData(t *Template, raw []byte) (Value, error) {
	size := int(t.StructSize)
	if size <= 0 || len(raw) < size {
		return Value{}, fmt.Errorf("logix: %s payload %d bytes, need %d", t.Name, len(raw), size)
	}
	if len(raw) >= 2*size {
		n := len(raw) / size
		elems := make([]Value, n)
		for i := 0; i < n; i++ {
			v, err := r.decodeStructOne(t, raw[i*size:(i+1)*size])
			if err != nil {
				return Value{}, err
			}
			elems[i] = v
		}
		return Value{Type: t.Name, Elems: elems}, nil
	}
	return r.decodeStructOne(t, raw[:size])
}

func (r *Registry) decodeStructOne(t *Template, raw []byte) (Value, error) {
	if t.IsString() {
		return decodeLogixString(t, raw)
	}
	vis := t.VisibleMembers()
	fields := make([]Field, 0, len(vis))
	for _, m := range vis {
		v, err := r.decodeMember(t, m, raw)
		if err != nil {
			return Value{}, fmt.Errorf("%s.%s: %w", t.Name, m.Name, err)
		}
		fields = append(fields, Field{Name: m.Name, Value: v})
	}
	return Value{Type: t.Name, Fields: fields}, nil
}

func (r *Registry) decodeMember(t *Template, m Member, raw []byte) (Value, error) {
	if m.IsStruct() {
		nested, ok := r.byID[m.NestedID()]
		if !ok {
			return Value{}, fmt.Errorf("unresolved nested template 0x%03x", m.NestedID())
		}
		stride := int(nested.StructSize)
		if m.IsArray() {
			count := int(m.Info)
			elems := make([]Value, 0, count)
			for i := 0; i < count; i++ {
				off := int(m.Offset) + i*stride
				if off+stride > len(raw) {
					return Value{}, fmt.Errorf("array element %d out of range", i)
				}
				v, err := r.decodeStructOne(nested, raw[off:off+stride])
				if err != nil {
					return Value{}, err
				}
				elems = append(elems, v)
			}
			return Value{Type: nested.Name, Elems: elems}, nil
		}
		if int(m.Offset)+stride > len(raw) {
			return Value{}, fmt.Errorf("member out of range")
		}
		return r.decodeStructOne(nested, raw[m.Offset:int(m.Offset)+stride])
	}

	code := m.ElementaryCode()
	ti, ok := TypeByCode(code)
	if !ok {
		return Value{}, fmt.Errorf("unknown member type 0x%04x", m.Type)
	}

	// BOOL members are bit-hosted: Offset addresses the host byte, Info is
	// the bit position. BOOL arrays are packed bit arrays (Info = count).
	if code == 0xC1 {
		if m.IsArray() {
			count := int(m.Info)
			elems := make([]Value, 0, count)
			for i := 0; i < count; i++ {
				off := int(m.Offset) + i/8
				if off >= len(raw) {
					return Value{}, fmt.Errorf("bit array element %d out of range", i)
				}
				b := raw[off]&(1<<(i%8)) != 0
				elems = append(elems, Value{Type: "BOOL", Scalar: b})
			}
			return Value{Type: "BOOL", Elems: elems}, nil
		}
		if int(m.Offset) >= len(raw) {
			return Value{}, fmt.Errorf("bool host byte out of range")
		}
		return Value{Type: "BOOL", Scalar: raw[m.Offset]&(1<<(m.Info%8)) != 0}, nil
	}

	if m.IsArray() {
		count := int(m.Info)
		elems := make([]Value, 0, count)
		for i := 0; i < count; i++ {
			s, err := decodeElementary(ti, raw, int(m.Offset)+i*ti.Size)
			if err != nil {
				return Value{}, err
			}
			elems = append(elems, Value{Type: ti.Name, Scalar: s})
		}
		return Value{Type: ti.Name, Elems: elems}, nil
	}
	s, err := decodeElementary(ti, raw, int(m.Offset))
	if err != nil {
		return Value{}, err
	}
	return Value{Type: ti.Name, Scalar: s}, nil
}

// decodeLogixString extracts the string from a STRING-shaped struct: DINT LEN
// then SINT DATA[].
func decodeLogixString(t *Template, raw []byte) (Value, error) {
	vis := t.VisibleMembers()
	lenM, dataM := vis[0], vis[1]
	if int(lenM.Offset)+4 > len(raw) {
		return Value{}, fmt.Errorf("string LEN out of range")
	}
	n := int(int32(binary.LittleEndian.Uint32(raw[lenM.Offset:])))
	if n < 0 {
		n = 0
	}
	if n > int(dataM.Info) {
		n = int(dataM.Info)
	}
	start := int(dataM.Offset)
	if start+n > len(raw) {
		n = len(raw) - start
		if n < 0 {
			n = 0
		}
	}
	return Value{Type: t.Name, Scalar: string(raw[start : start+n])}, nil
}

// decodeElementary reads one scalar of type ti at offset off.
func decodeElementary(ti TypeInfo, raw []byte, off int) (any, error) {
	if off < 0 || off+ti.Size > len(raw) {
		return nil, fmt.Errorf("%s at offset %d out of range (%d bytes)", ti.Name, off, len(raw))
	}
	b := raw[off:]
	switch ti.Code {
	case 0xC1:
		return b[0] != 0, nil
	case 0xC2:
		return int64(int8(b[0])), nil
	case 0xC3:
		return int64(int16(binary.LittleEndian.Uint16(b))), nil
	case 0xC4, 0xCC, 0xCE:
		return int64(int32(binary.LittleEndian.Uint32(b))), nil
	case 0xC5, 0xCF:
		return int64(binary.LittleEndian.Uint64(b)), nil
	case 0xC6, 0xD1:
		return int64(b[0]), nil
	case 0xC7, 0xCD, 0xD2:
		return int64(binary.LittleEndian.Uint16(b)), nil
	case 0xC8, 0xD3:
		return int64(binary.LittleEndian.Uint32(b)), nil
	case 0xC9, 0xD4:
		return int64(binary.LittleEndian.Uint64(b)), nil
	case 0xCA:
		return float64(math.Float32frombits(binary.LittleEndian.Uint32(b))), nil
	case 0xCB:
		return math.Float64frombits(binary.LittleEndian.Uint64(b)), nil
	}
	return nil, fmt.Errorf("undecodable type %s", ti.Name)
}
