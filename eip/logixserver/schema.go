// Package logixserver presents a tag surface as an Allen-Bradley ControlLogix
// controller over EtherNet/IP: identity, tag-list upload (Symbol class),
// UDT definitions (Template class), and connected Read/Write Tag — the
// surface pycomm3's LogixDriver and nautilus's own eip/logix client speak.
// It serves two purposes: a hermetic integration-test fixture for the
// EtherNet/IP driver, and a building block for exposing a nautilus runtime's
// tags to other Logix-speaking clients.
package logixserver

import (
	"sort"
	"strings"
)

// baseTagBit mirrors pycomm3's BASE_TAG_BIT (1<<26) in the Symbol object's
// "software control" attribute. Setting it marks a tag as a real (base) tag
// rather than an alias, which is what pycomm3 expects for user tags.
const baseTagBit uint32 = 1 << 26

// symbolType bit fields (Rockwell Symbol class 0x6B, attribute 2).
const (
	symbolTypeStructBit    uint16 = 0x8000 // bit 15: 1=struct, 0=atomic
	symbolTypeDimShift            = 13     // bits 13-14: number of array dimensions
	symbolTypeTemplateMask uint16 = 0x0FFF
)

// symbolDef is one Symbol-class (0x6B) instance the controller exposes within a
// given scope. name is the wire name within that scope: controller tags use
// their bare name, program tags use their bare name (clients prepend the
// "Program:<prog>." themselves), and a program is advertised as a
// controller-scope symbol literally named "Program:<prog>".
type symbolDef struct {
	instanceID uint32
	name       string
	scope      string // "" = controller, else "Program:<prog>"
	symbolType uint16
	dims       [3]uint32
}

// templateMember is one member of a UDT template (class 0x6C).
type templateMember struct {
	name     string
	typeCode uint16 // elementary code (0xC1 BOOL, ...) or symbolTypeStructBit|childTemplateID
	typeInfo uint16 // bit position for BOOL, array length for arrays, else 0
	offset   uint32
}

// templateDef is a UDT definition clients upload via Get Structure Makeup
// (GetAttributeList on class 0x6C) then Read Template (ReadTag on class 0x6C).
type templateDef struct {
	instanceID uint32
	name       string
	structSize uint32
	handle     uint16
	members    []templateMember
}

// Schema is the static tag/UDT surface the Logix server advertises. It is
// derived once from a TagSurfaceSpec and answers tag-list-upload and template
// requests.
type Schema struct {
	symbols   []symbolDef
	templates map[uint32]*templateDef
	// instanceBase maps a controller-scope Symbol instance id to its canonical
	// base tag name, so Symbol-Instance-Addressing reads (use_instance_ids) can
	// be resolved back to the tag store.
	instanceBase map[uint32]string
	// symbolByPath maps the upper-cased canonical tag path (controller tags
	// bare, program tags "PROGRAM:X.Y") to its symbol, for struct-root reads.
	symbolByPath map[string]symbolDef
}

// templateFor returns the template backing a struct symbol.
func (s *Schema) templateFor(sym symbolDef) (*templateDef, bool) {
	if sym.symbolType&symbolTypeStructBit == 0 {
		return nil, false
	}
	t, ok := s.templates[uint32(sym.symbolType&symbolTypeTemplateMask)]
	return t, ok
}

// lookupSymbol finds a symbol by canonical path, case-insensitively.
func (s *Schema) lookupSymbol(path string) (symbolDef, bool) {
	sym, ok := s.symbolByPath[strings.ToUpper(path)]
	return sym, ok
}

// encodeInstanceList renders a Get_Instance_Attribute_List (0x55) reply body for
// the symbols in scope whose instance id is >= start, sorted by instance id.
// The attribute layout mirrors exactly what pycomm3 requests and decodes:
// instance(UDINT), name(STRING), symbol_type(UINT), symbol_address(UDINT),
// symbol_object_address(UDINT), software_control(UDINT), dims[3](UDINT),
// external_access(USINT).
func (s *Schema) encodeInstanceList(scope string, start uint32) []byte {
	var matches []symbolDef
	for _, sym := range s.symbols {
		if sym.scope == scope && sym.instanceID >= start {
			matches = append(matches, sym)
		}
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].instanceID < matches[j].instanceID })

	out := make([]byte, 0, 64)
	for _, sym := range matches {
		out = append(out, u32(sym.instanceID)...)
		out = append(out, u16(uint16(len(sym.name)))...) // STRING length prefix
		out = append(out, []byte(sym.name)...)
		out = append(out, u16(sym.symbolType)...)
		out = append(out, u32(0)...)           // symbol_address
		out = append(out, u32(0)...)           // symbol_object_address
		out = append(out, u32(baseTagBit)...)  // software_control: base tag, not alias
		out = append(out, u32(sym.dims[0])...) // attr 8: array dimensions
		out = append(out, u32(sym.dims[1])...)
		out = append(out, u32(sym.dims[2])...)
		out = append(out, 1) // external_access: read/write
	}
	return out
}

// attributesReply renders a Get Structure Makeup (GetAttributeList 0x03) reply
// for a template, in the exact attribute order pycomm3 requests: object
// definition size (4), structure size (5), member count (2), structure handle
// (1). Each entry is [UINT attr_num][UINT status][value].
func (t *templateDef) attributesReply() []byte {
	objDefSize := t.objectDefinitionSize()
	out := make([]byte, 0, 32)
	out = append(out, u16(4)...) // attribute count
	out = appendAttr32(out, 4, objDefSize)
	out = appendAttr32(out, 5, t.structSize)
	out = appendAttr16(out, 2, uint16(len(t.members)))
	out = appendAttr16(out, 1, t.handle)
	return out
}

// appendAttr32 appends a [UINT attr_num][UINT status=0][UDINT value] entry.
func appendAttr32(out []byte, attr uint16, value uint32) []byte {
	out = append(out, u16(attr)...)
	out = append(out, u16(0)...)
	out = append(out, u32(value)...)
	return out
}

// appendAttr16 appends a [UINT attr_num][UINT status=0][UINT value] entry.
func appendAttr16(out []byte, attr, value uint16) []byte {
	out = append(out, u16(attr)...)
	out = append(out, u16(0)...)
	out = append(out, u16(value)...)
	return out
}

// rawData renders the Read Template payload clients parse: a member-info array
// (UINT type_info, UINT type_code, UDINT offset per member) followed by a
// null-delimited name section beginning with "<TemplateName>;<n>" and then each
// member name in order. It is padded to objectDefinitionSize*4-21 bytes so the
// single read pycomm3 issues returns exactly the size it computes.
func (t *templateDef) rawData() []byte {
	out := make([]byte, 0, 64)
	for _, m := range t.members {
		out = append(out, u16(m.typeInfo)...)
		out = append(out, u16(m.typeCode)...)
		out = append(out, u32(m.offset)...)
	}
	var names strings.Builder
	names.WriteString(t.name)
	names.WriteString(";0\x00")
	for _, m := range t.members {
		names.WriteString(m.name)
		names.WriteByte(0)
	}
	out = append(out, []byte(names.String())...)

	target := int(t.objectDefinitionSize())*4 - 21
	for len(out) < target {
		out = append(out, 0)
	}
	return out
}

// objectDefinitionSize is the value pycomm3 multiplies by 4 (minus 21) to learn
// how many template bytes to read. It must cover the full raw template payload.
func (t *templateDef) objectDefinitionSize() uint32 {
	rawLen := 0
	for range t.members {
		rawLen += 8
	}
	rawLen += len(t.name) + len(";0") + 1
	for _, m := range t.members {
		rawLen += len(m.name) + 1
	}
	return uint32((rawLen + 21 + 3) / 4)
}
