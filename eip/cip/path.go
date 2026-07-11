package cip

import (
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"
)

// AnsiExtendedSymbol (0x91) is the Extended-Symbolic ANSI symbol segment Logix
// Read Tag uses to name a tag or struct member. Layout: [0x91][u8 len][len
// ASCII bytes][optional pad byte so the segment is an even number of bytes].
const AnsiExtendedSymbol = 0x91

// EPATH segment type codes (Vol 1, App. C). EPATH is the byte-encoded
// addressing format used in CIP messages — it can describe class/instance/
// attribute, connection points, symbolic names (Logix tags), and routing.
//
// The high 3 bits of the first byte select the segment type:
//
//	0x00 Port    (port + link address — used for routing)
//	0x20 Logical (class/instance/attribute/conn-point/member)
//	0x40 Network (network-related — rarely seen at app level)
//	0x60 Symbolic (ASCII symbol — pre-Logix)
//	0x80 Data    (Logix Extended Symbolic, including ANSI symbol + element)
//	0xA0 DataType-Constructed
//	0xC0 DataType-Elementary
//
// We only emit / parse Port + Logical + Extended Symbolic here. Drives and
// most non-Logix CIP objects need nothing beyond Logical.

// Logical segment formats (low 2 bits of first byte).
const (
	logicalFmt8Bit  = 0x00
	logicalFmt16Bit = 0x01
	logicalFmt32Bit = 0x02
)

// Logical segment "what is being addressed" (middle 3 bits, after high 3).
const (
	logicalKindClass     = 0x00
	logicalKindInstance  = 0x04
	logicalKindMember    = 0x08
	logicalKindConnPoint = 0x0C
	logicalKindAttribute = 0x10
	logicalKindSpecial   = 0x14
	logicalKindServiceID = 0x18
)

// Logical segment base = 0x20 (high 3 bits = 001).
const logicalSegmentBase = 0x20

// LogicalSegment yields the 8-bit form of class/instance/attribute. 16/32-bit
// variants exist; this helper covers the common ≤255 case.
func LogicalSegment(kind, value uint8) []byte {
	return []byte{byte(logicalSegmentBase | kind | logicalFmt8Bit), value}
}

// LogicalSegment16 yields the 16-bit form. Note that 16/32 variants insert
// a pad byte after the segment byte (Vol 1 §C-1.4.2).
func LogicalSegment16(kind uint8, value uint16) []byte {
	out := make([]byte, 4)
	out[0] = byte(logicalSegmentBase | kind | logicalFmt16Bit)
	out[1] = 0x00 // pad
	binary.LittleEndian.PutUint16(out[2:4], value)
	return out
}

// BuildPath encodes a class/instance/attribute triplet into an EPATH. The
// attribute is optional — pass 0 to omit it. Returns the byte string.
// Common values fit in 8 bits; we transparently widen to 16-bit when needed.
func BuildPath(class, instance uint32, attribute uint16) []byte {
	var p []byte

	// Class
	if class > 0xFF {
		p = append(p, LogicalSegment16(logicalKindClass, uint16(class))...)
	} else {
		p = append(p, LogicalSegment(logicalKindClass, uint8(class))...)
	}

	// Instance — note: instance 0 is valid (refers to the class itself).
	if instance > 0xFF {
		p = append(p, LogicalSegment16(logicalKindInstance, uint16(instance))...)
	} else {
		p = append(p, LogicalSegment(logicalKindInstance, uint8(instance))...)
	}

	// Attribute (optional).
	if attribute > 0 {
		if attribute > 0xFF {
			p = append(p, LogicalSegment16(logicalKindAttribute, attribute)...)
		} else {
			p = append(p, LogicalSegment(logicalKindAttribute, uint8(attribute))...)
		}
	}

	return p
}

// ParsedPath is the decoded subset of EPATH we care about for the server's
// message router. Drives and most non-Logix devices only ever address
// class/instance/attribute, so this covers the practical universe.
type ParsedPath struct {
	Class     uint32
	Instance  uint32
	Attribute uint32 // 0 if absent
	Member    uint32 // 0 if absent (for assembly member access, etc.)
	HasClass  bool
	HasInst   bool
	HasAttr   bool
	HasMember bool
}

// PathSegment is one decoded EPATH segment. Multi-segment paths (e.g. a
// Forward_Open connection path that addresses configuration + O→T + T→O
// assemblies) need every segment, which ParsePath flattens away.
type PathSegment struct {
	Kind  uint8  // logicalKindClass / logicalKindInstance / logicalKindConnPoint / ...
	Value uint32 // segment value
}

// WalkSegments decodes every logical segment in an EPATH in order. Useful for
// Forward_Open paths that list multiple class/instance/connection-point
// segments to address different ports of the same object.
func WalkSegments(b []byte) ([]PathSegment, error) {
	out := make([]PathSegment, 0, 4)
	i := 0
	for i < len(b) {
		seg := b[i]
		segType := seg & 0xE0
		if segType != logicalSegmentBase {
			return out, fmt.Errorf("cip: unsupported path segment 0x%02x at offset %d", seg, i)
		}
		kind := seg & 0x1C
		fmtBits := seg & 0x03

		var value uint32
		switch fmtBits {
		case logicalFmt8Bit:
			if i+2 > len(b) {
				return out, fmt.Errorf("cip: truncated 8-bit logical segment")
			}
			value = uint32(b[i+1])
			i += 2
		case logicalFmt16Bit:
			if i+4 > len(b) {
				return out, fmt.Errorf("cip: truncated 16-bit logical segment")
			}
			value = uint32(binary.LittleEndian.Uint16(b[i+2 : i+4]))
			i += 4
		case logicalFmt32Bit:
			if i+6 > len(b) {
				return out, fmt.Errorf("cip: truncated 32-bit logical segment")
			}
			value = binary.LittleEndian.Uint32(b[i+2 : i+6])
			i += 6
		default:
			return out, fmt.Errorf("cip: invalid logical format %d", fmtBits)
		}

		out = append(out, PathSegment{Kind: kind, Value: value})
	}
	return out, nil
}

// AssemblyEndpoints extracts the (O→T, T→O) assembly instance numbers from a
// Forward_Open connection path. PowerFlex 70 / generic CIP drives use the
// pattern: Class 0x04 + (Instance|ConnPoint) repeated 2..3 times. The last
// two non-config segments are O→T and T→O respectively. Returns (oToT, tToO)
// and true if both were found.
//
// Logical kinds we treat as endpoints: Instance (0x04) and ConnPoint (0x0C).
// PowerFlex AOP emits ConnPoint; some scanners (libplctag) emit Instance.
func AssemblyEndpoints(path []byte) (oToT, tToO uint16, ok bool) {
	segs, err := WalkSegments(path)
	if err != nil {
		return 0, 0, false
	}
	endpoints := make([]uint16, 0, 3)
	for _, s := range segs {
		if s.Kind == logicalKindInstance || s.Kind == logicalKindConnPoint {
			endpoints = append(endpoints, uint16(s.Value))
		}
	}
	if len(endpoints) < 2 {
		return 0, 0, false
	}
	// Path lists: [config], O→T, T→O. When length == 2 there's no config.
	return endpoints[len(endpoints)-2], endpoints[len(endpoints)-1], true
}

// ParsePath decodes an EPATH. The encoded length is in 16-bit words; pass the
// raw segment bytes (not the leading path-size byte). Returns ParsedPath and
// the number of bytes consumed. Unsupported segment types fail loudly because
// silently dropping them in a server leads to mysterious wrong-object reads.
func ParsePath(b []byte) (ParsedPath, int, error) {
	var p ParsedPath
	i := 0
	for i < len(b) {
		seg := b[i]
		segType := seg & 0xE0
		if segType != logicalSegmentBase {
			return p, i, fmt.Errorf("cip: unsupported path segment 0x%02x at offset %d", seg, i)
		}
		kind := seg & 0x1C
		fmtBits := seg & 0x03

		var value uint32
		switch fmtBits {
		case logicalFmt8Bit:
			if i+2 > len(b) {
				return p, i, fmt.Errorf("cip: truncated 8-bit logical segment")
			}
			value = uint32(b[i+1])
			i += 2
		case logicalFmt16Bit:
			if i+4 > len(b) {
				return p, i, fmt.Errorf("cip: truncated 16-bit logical segment")
			}
			value = uint32(binary.LittleEndian.Uint16(b[i+2 : i+4]))
			i += 4
		case logicalFmt32Bit:
			if i+6 > len(b) {
				return p, i, fmt.Errorf("cip: truncated 32-bit logical segment")
			}
			value = binary.LittleEndian.Uint32(b[i+2 : i+6])
			i += 6
		default:
			return p, i, fmt.Errorf("cip: invalid logical format %d", fmtBits)
		}

		switch kind {
		case logicalKindClass:
			p.Class, p.HasClass = value, true
		case logicalKindInstance:
			p.Instance, p.HasInst = value, true
		case logicalKindAttribute:
			p.Attribute, p.HasAttr = value, true
		case logicalKindMember:
			p.Member, p.HasMember = value, true
		default:
			return p, i, fmt.Errorf("cip: unsupported logical kind 0x%02x", kind)
		}
	}
	return p, i, nil
}

// ParseLogicalPrefix decodes the leading run of logical class/instance/
// attribute/member segments and stops (without error) at the first non-logical
// segment — e.g. an ANSI symbol (0x91). It returns the decoded ParsedPath and
// the number of bytes consumed. Logix mixes addressing forms in one EPATH:
// a Symbol-class read-by-instance-id path is [class 0x6B][instance N] followed
// by ANSI member segments, which ParsePath rejects wholesale.
func ParseLogicalPrefix(b []byte) (ParsedPath, int) {
	var p ParsedPath
	i := 0
	for i < len(b) {
		seg := b[i]
		if seg&0xE0 != logicalSegmentBase {
			break
		}
		kind := seg & 0x1C
		var value uint32
		switch seg & 0x03 {
		case logicalFmt8Bit:
			if i+2 > len(b) {
				return p, i
			}
			value = uint32(b[i+1])
			i += 2
		case logicalFmt16Bit:
			if i+4 > len(b) {
				return p, i
			}
			value = uint32(binary.LittleEndian.Uint16(b[i+2 : i+4]))
			i += 4
		case logicalFmt32Bit:
			if i+6 > len(b) {
				return p, i
			}
			value = binary.LittleEndian.Uint32(b[i+2 : i+6])
			i += 6
		default:
			return p, i
		}
		switch kind {
		case logicalKindClass:
			p.Class, p.HasClass = value, true
		case logicalKindInstance:
			p.Instance, p.HasInst = value, true
		case logicalKindAttribute:
			p.Attribute, p.HasAttr = value, true
		case logicalKindMember:
			p.Member, p.HasMember = value, true
		default:
			return p, i
		}
	}
	return p, i
}

// TagSegment is one element of a parsed symbolic (Logix Read Tag) path: either
// a symbol name (e.g. "Plt", "Program:MBD_3To1_Merge") or an array index.
type TagSegment struct {
	Symbol  string
	Index   uint32
	IsIndex bool
}

// ParseSymbolicTag decodes the EPATH a Logix Read Tag carries: chained ANSI
// extended-symbol segments (0x91) interleaved with array/member index segments
// (logical "Member" segments 0x28 8-bit / 0x29 16-bit / 0x2A 32-bit). ParsePath
// deliberately rejects these; this is the symbolic-tag counterpart and leaves
// ParsePath untouched.
func ParseSymbolicTag(b []byte) ([]TagSegment, error) {
	var segs []TagSegment
	i := 0
	for i < len(b) {
		seg := b[i]
		switch {
		case seg == AnsiExtendedSymbol:
			if i+2 > len(b) {
				return nil, fmt.Errorf("cip: truncated symbol segment at offset %d", i)
			}
			n := int(b[i+1])
			start := i + 2
			if start+n > len(b) {
				return nil, fmt.Errorf("cip: symbol name length %d exceeds buffer at offset %d", n, i)
			}
			segs = append(segs, TagSegment{Symbol: string(b[start : start+n])})
			adv := 2 + n
			if n%2 == 1 {
				adv++ // pad byte so the segment is word-aligned
			}
			i += adv
		case seg&0xE0 == logicalSegmentBase && seg&0x1C == logicalKindMember:
			switch seg & 0x03 {
			case logicalFmt8Bit:
				if i+2 > len(b) {
					return nil, fmt.Errorf("cip: truncated 8-bit index segment at offset %d", i)
				}
				segs = append(segs, TagSegment{IsIndex: true, Index: uint32(b[i+1])})
				i += 2
			case logicalFmt16Bit:
				if i+4 > len(b) {
					return nil, fmt.Errorf("cip: truncated 16-bit index segment at offset %d", i)
				}
				segs = append(segs, TagSegment{IsIndex: true, Index: uint32(binary.LittleEndian.Uint16(b[i+2 : i+4]))})
				i += 4
			case logicalFmt32Bit:
				if i+6 > len(b) {
					return nil, fmt.Errorf("cip: truncated 32-bit index segment at offset %d", i)
				}
				segs = append(segs, TagSegment{IsIndex: true, Index: binary.LittleEndian.Uint32(b[i+2 : i+6])})
				i += 6
			}
		default:
			return nil, fmt.Errorf("cip: unsupported symbolic segment 0x%02x at offset %d", seg, i)
		}
	}
	if len(segs) == 0 || segs[0].IsIndex {
		return nil, fmt.Errorf("cip: symbolic path must begin with a symbol segment")
	}
	return segs, nil
}

// CanonicalTagPath renders parsed segments back into the dotted, bracketed form
// the tag store keys on, e.g. "Program:MBD_3To1_Merge.Plt[4].Header.Displacement".
func CanonicalTagPath(segs []TagSegment) string {
	var sb strings.Builder
	for i, s := range segs {
		if s.IsIndex {
			sb.WriteByte('[')
			sb.WriteString(strconv.FormatUint(uint64(s.Index), 10))
			sb.WriteByte(']')
			continue
		}
		if i > 0 {
			sb.WriteByte('.')
		}
		sb.WriteString(s.Symbol)
	}
	return sb.String()
}
