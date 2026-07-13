package logixserver

import (
	"encoding/binary"
	"math"
	"strings"
	"sync"

	"github.com/joyautomation/nautilus/eip/cip"
)

// tagEntry is the stored state for one canonical tag path.
type tagEntry struct {
	leafType uint16
	value    any
}

// TagStore holds Logix tags keyed by canonical path. Lookups are
// case-insensitive because Logix tag names are themselves case-insensitive.
type TagStore struct {
	mu    sync.RWMutex
	tags  map[string]*tagEntry // upper(canonical path) -> entry
	paths map[string]string    // upper -> original-case path (for source mapping)
}

// NewTagStore returns an empty store.
func NewTagStore() *TagStore {
	return &TagStore{
		tags:  make(map[string]*tagEntry),
		paths: make(map[string]string),
	}
}

// Set registers (or replaces) a tag with its leaf type and initial value.
func (s *TagStore) Set(path string, leafType uint16, value any) {
	key := strings.ToUpper(path)
	s.mu.Lock()
	s.tags[key] = &tagEntry{leafType: leafType, value: value}
	s.paths[key] = path
	s.mu.Unlock()
}

// UpdateValue sets the current value for an existing tag. Returns false if the
// tag is unknown.
func (s *TagStore) UpdateValue(path string, value any) bool {
	key := strings.ToUpper(path)
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.tags[key]
	if !ok {
		return false
	}
	e.value = value
	return true
}

// Resolve returns the leaf type and current value for a canonical path.
func (s *TagStore) Resolve(path string) (leafType uint16, value any, ok bool) {
	key := strings.ToUpper(path)
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.tags[key]
	if !ok {
		return 0, nil, false
	}
	return e.leafType, e.value, true
}

// EncodeLeaf serializes a value into the wire bytes for its CIP elementary
// type. The store's universal numeric form is float64; we down-convert per
// type. Booleans accept bool or any nonzero number.
func EncodeLeaf(leafType uint16, v any) []byte {
	switch leafType {
	case cip.TypeBOOL:
		if truthy(v) {
			return []byte{1}
		}
		return []byte{0}
	case cip.TypeSINT:
		return []byte{byte(int8(toFloat(v)))}
	case cip.TypeUSINT:
		return []byte{byte(uint8(toFloat(v)))}
	case cip.TypeINT:
		return u16(uint16(int16(toFloat(v))))
	case cip.TypeUINT, cip.TypeWORD:
		return u16(uint16(toFloat(v)))
	case cip.TypeDINT:
		return u32(uint32(int32(toFloat(v))))
	case cip.TypeUDINT, cip.TypeDWORD:
		return u32(uint32(toFloat(v)))
	case cip.TypeREAL:
		return u32(math.Float32bits(float32(toFloat(v))))
	case cip.TypeLINT:
		return u64(uint64(int64(toFloat(v))))
	case cip.TypeULINT, cip.TypeLWORD:
		return u64(uint64(toFloat(v)))
	case cip.TypeLREAL:
		return u64(math.Float64bits(toFloat(v)))
	default:
		return u32(math.Float32bits(float32(toFloat(v)))) // fall back to REAL
	}
}

// DecodeLeaf parses wire bytes for an elementary type back into the store's
// value form (bool or float64). Used by the Write Tag service.
func DecodeLeaf(leafType uint16, b []byte) (any, bool) {
	need := int(sizeForCIPType(leafType))
	if len(b) < need {
		return nil, false
	}
	switch leafType {
	case cip.TypeBOOL:
		return b[0] != 0, true
	case cip.TypeSINT:
		return float64(int8(b[0])), true
	case cip.TypeUSINT:
		return float64(b[0]), true
	case cip.TypeINT:
		return float64(int16(binary.LittleEndian.Uint16(b))), true
	case cip.TypeUINT, cip.TypeWORD:
		return float64(binary.LittleEndian.Uint16(b)), true
	case cip.TypeDINT:
		return float64(int32(binary.LittleEndian.Uint32(b))), true
	case cip.TypeUDINT, cip.TypeDWORD:
		return float64(binary.LittleEndian.Uint32(b)), true
	case cip.TypeREAL:
		return float64(math.Float32frombits(binary.LittleEndian.Uint32(b))), true
	case cip.TypeLINT:
		return float64(int64(binary.LittleEndian.Uint64(b))), true
	case cip.TypeULINT, cip.TypeLWORD:
		return float64(binary.LittleEndian.Uint64(b)), true
	case cip.TypeLREAL:
		return math.Float64frombits(binary.LittleEndian.Uint64(b)), true
	}
	return nil, false
}

func truthy(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case nil:
		return false
	default:
		return toFloat(v) != 0
	}
}

func toFloat(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case float32:
		return float64(t)
	case int:
		return float64(t)
	case int32:
		return float64(t)
	case int64:
		return float64(t)
	case uint:
		return float64(t)
	case uint32:
		return float64(t)
	case uint64:
		return float64(t)
	case bool:
		if t {
			return 1
		}
		return 0
	default:
		return 0
	}
}

func u16(v uint16) []byte {
	b := make([]byte, 2)
	binary.LittleEndian.PutUint16(b, v)
	return b
}

func u32(v uint32) []byte {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, v)
	return b
}

func u64(v uint64) []byte {
	b := make([]byte, 8)
	binary.LittleEndian.PutUint64(b, v)
	return b
}
