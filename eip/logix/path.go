package logix

import (
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"

	"github.com/joyautomation/nautilus/eip/cip"
)

// EncodeTagPath builds the EPATH for a symbolic tag reference like
// "Motor1.Cmd.Speed", "Program:MainProgram.Counts[4]" or "Grid[2,3].Val".
// Each dotted component becomes an ANSI extended-symbol segment (0x91) and
// each array index a logical Member segment sized to the index value. The
// "Program:Name" prefix stays a single symbol segment — the colon is part of
// the Logix scope name, not a separator.
func EncodeTagPath(tag string) ([]byte, error) {
	if tag == "" {
		return nil, fmt.Errorf("logix: empty tag name")
	}
	var out []byte
	for _, part := range strings.Split(tag, ".") {
		name := part
		var idx []uint32
		if i := strings.IndexByte(part, '['); i >= 0 {
			if !strings.HasSuffix(part, "]") {
				return nil, fmt.Errorf("logix: malformed index in %q", tag)
			}
			name = part[:i]
			// Accept both "[2,3]" and "[2][3]" for multi-dimensional access.
			for _, group := range strings.Split(part[i+1:len(part)-1], "][") {
				for _, s := range strings.Split(group, ",") {
					n, err := strconv.ParseUint(strings.TrimSpace(s), 10, 32)
					if err != nil {
						return nil, fmt.Errorf("logix: bad array index in %q: %w", tag, err)
					}
					idx = append(idx, uint32(n))
				}
			}
		}
		if name == "" {
			return nil, fmt.Errorf("logix: empty path component in %q", tag)
		}
		if len(name) > 255 {
			return nil, fmt.Errorf("logix: path component too long in %q", tag)
		}
		out = append(out, cip.AnsiExtendedSymbol, byte(len(name)))
		out = append(out, name...)
		if len(name)%2 == 1 {
			out = append(out, 0) // pad to word alignment
		}
		for _, n := range idx {
			out = append(out, encodeIndexSegment(n)...)
		}
	}
	return out, nil
}

// encodeIndexSegment emits a logical Member segment (array element access)
// in the narrowest of the 8/16/32-bit forms.
func encodeIndexSegment(n uint32) []byte {
	switch {
	case n <= 0xFF:
		return []byte{0x28, byte(n)}
	case n <= 0xFFFF:
		b := []byte{0x29, 0x00, 0, 0}
		binary.LittleEndian.PutUint16(b[2:], uint16(n))
		return b
	default:
		b := []byte{0x2A, 0x00, 0, 0, 0, 0}
		binary.LittleEndian.PutUint32(b[2:], n)
		return b
	}
}

// scopePrefixPath emits the ANSI segment addressing a program scope
// ("Program:MainProgram") for prefixing class-addressed requests like the
// Symbol-class tag-list walk.
func scopePrefixPath(scope string) []byte {
	out := []byte{cip.AnsiExtendedSymbol, byte(len(scope))}
	out = append(out, scope...)
	if len(scope)%2 == 1 {
		out = append(out, 0)
	}
	return out
}
