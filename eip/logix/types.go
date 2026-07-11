// Package logix is a pure-Go client for the Allen-Bradley Logix tag surface
// (ControlLogix / CompactLogix) over EtherNet/IP: connected Class 3 explicit
// messaging, symbolic Read/Write Tag, batched Multiple Service Packet reads,
// tag-list upload via the Symbol class, and UDT definition upload via the
// Template class. It builds on eip/cip for framing and carries no cgo or
// third-party dependencies.
package logix

import "strings"

// CIP elementary data-type codes (Vol 1 App. C) with their Logix names and
// on-wire sizes in bytes.
type TypeInfo struct {
	Code uint16
	Name string
	Size int
}

var typesByCode = map[uint16]TypeInfo{
	0xC1: {0xC1, "BOOL", 1},
	0xC2: {0xC2, "SINT", 1},
	0xC3: {0xC3, "INT", 2},
	0xC4: {0xC4, "DINT", 4},
	0xC5: {0xC5, "LINT", 8},
	0xC6: {0xC6, "USINT", 1},
	0xC7: {0xC7, "UINT", 2},
	0xC8: {0xC8, "UDINT", 4},
	0xC9: {0xC9, "ULINT", 8},
	0xCA: {0xCA, "REAL", 4},
	0xCB: {0xCB, "LREAL", 8},
	0xCC: {0xCC, "STIME", 4},
	0xCD: {0xCD, "DATE", 2},
	0xCE: {0xCE, "TIME_OF_DAY", 4},
	0xCF: {0xCF, "DATE_AND_TIME", 8},
	0xD1: {0xD1, "BYTE", 1},
	0xD2: {0xD2, "WORD", 2},
	0xD3: {0xD3, "DWORD", 4},
	0xD4: {0xD4, "LWORD", 8},
}

// TypeByCode resolves an elementary CIP type code (low byte of a symbol or
// member type). Returns ok=false for struct/unknown codes.
func TypeByCode(code uint16) (TypeInfo, bool) {
	t, ok := typesByCode[code&0x00FF]
	return t, ok
}

// TypeByName resolves a Logix elementary type name ("DINT", "REAL", ...).
func TypeByName(name string) (TypeInfo, bool) {
	for _, t := range typesByCode {
		if t.Name == name {
			return t, true
		}
	}
	return TypeInfo{}, false
}

// Symbol type-word bit fields (Symbol class 0x6B attribute 2 — same layout as
// the @tags symbolType word).
const (
	symbolStructBit    uint16 = 0x8000 // bit 15: 1 = struct (template), 0 = atomic
	symbolSystemBit    uint16 = 0x1000 // bit 12: system-internal tag
	symbolDimShift            = 13     // bits 13-14: number of array dimensions
	symbolTemplateMask uint16 = 0x0FFF // low 12 bits: template instance ID
)

// Symbol is one tag from the controller's tag-list upload. Name is canonical:
// controller-scope tags are bare ("MotorSpeed"), program-scope tags carry
// their program prefix ("Program:MainProgram.Counter").
type Symbol struct {
	InstanceID uint32
	Name       string
	Type       uint16 // raw symbol type word
	Dims       [3]uint32
}

func (s Symbol) IsStruct() bool         { return s.Type&symbolStructBit != 0 }
func (s Symbol) IsSystem() bool         { return s.Type&symbolSystemBit != 0 }
func (s Symbol) TemplateID() uint16     { return s.Type & symbolTemplateMask }
func (s Symbol) DimCount() int          { return int(s.Type>>symbolDimShift) & 0x3 }
func (s Symbol) ElementaryCode() uint16 { return s.Type & 0x00FF }

// ElemCount returns the total number of array elements (1 for scalars).
func (s Symbol) ElemCount() int {
	n := 1
	for d := 0; d < s.DimCount(); d++ {
		if s.Dims[d] > 0 {
			n *= int(s.Dims[d])
		}
	}
	return n
}

// Template is a UDT definition uploaded from the Template class (0x6C). The
// same object describes user UDTs, AOI backing structs, predefined types
// (TIMER, COUNTER, STRING), and module-defined I/O types.
type Template struct {
	ID         uint16
	Name       string
	Handle     uint16 // structure handle: the u16 that follows 0x02A0 in reads
	StructSize uint32 // instance size in bytes
	ObjDefSize uint32 // object definition size in 32-bit words (sizing the body read)
	Members    []Member
}

// Member is one member of a template, in wire order (offsets ascending is not
// guaranteed). Hidden members (bit hosts, __ prefixed) are retained so struct
// decoding can use their offsets; consumers exporting types should skip them.
type Member struct {
	Name   string
	Info   uint16 // BOOL: bit position within host byte; array: element count; else 0
	Type   uint16 // raw type code: 0x8000|templateID for structs, 0x2000 = array bit
	Offset uint32 // byte offset within the template instance
}

func (m Member) IsStruct() bool         { return m.Type&0x8000 != 0 }
func (m Member) IsArray() bool          { return m.Type&0x2000 != 0 }
func (m Member) NestedID() uint16       { return m.Type & 0x0FFF }
func (m Member) ElementaryCode() uint16 { return m.Type & 0x00FF }

// Hidden reports whether the member is controller-internal: bit-host bytes
// (ZZZZZZZZZZ...), compiler-generated members (__ prefix).
func (m Member) Hidden() bool {
	return strings.HasPrefix(m.Name, "ZZZZZ") || strings.HasPrefix(m.Name, "__")
}

// VisibleMembers returns the members consumers should expose, in wire order.
func (t *Template) VisibleMembers() []Member {
	out := make([]Member, 0, len(t.Members))
	for _, m := range t.Members {
		if !m.Hidden() {
			out = append(out, m)
		}
	}
	return out
}

// IsString reports whether the template has the Logix string shape: a DINT
// LEN followed by a SINT[] DATA. Covers the predefined STRING (82 chars) and
// user-sized STRINGnn types; such values decode to a Go string.
func (t *Template) IsString() bool {
	vis := t.VisibleMembers()
	if len(vis) != 2 {
		return false
	}
	lenM, dataM := vis[0], vis[1]
	if lenM.Name != "LEN" || dataM.Name != "DATA" {
		return false
	}
	return lenM.ElementaryCode() == 0xC4 && !lenM.IsStruct() &&
		dataM.ElementaryCode() == 0xC2 && dataM.IsArray() && !dataM.IsStruct()
}

// BrowseResult is a full snapshot of the controller's user-visible tag
// database: symbols in canonical naming plus every template reachable from
// them (including nested templates).
type BrowseResult struct {
	Symbols   []Symbol
	Templates map[uint16]*Template
	Programs  []string
}

// TemplateByName finds a template by its Logix type name.
func (b *BrowseResult) TemplateByName(name string) (*Template, bool) {
	for _, t := range b.Templates {
		if t.Name == name {
			return t, true
		}
	}
	return nil, false
}
