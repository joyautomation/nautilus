package writer

import (
	"fmt"
	"strconv"

	"github.com/joyautomation/nautilus/lang/st"
)

// Partial access (#1305 B3). nautilus (lang/st lowerPartial, lang/ir
// vm.go BitRef) numbers parts from the least significant bit: w.%B1 is
// bits 8..15, w.%W1 bits 16..31, w.%D1 bits 32..63, w.%L0 all 64; a part
// reads as the unsigned BYTE/WORD/DWORD/LWORD (w >> bit) & mask, and a
// write replaces exactly those bits (on a signed word the top part's top
// bit is the sign, as in two's complement). TIA's slice access numbers
// the same way (.%X bench-proven; .%B/.%W/.%D on the B3 bench row). A
// part of a part is flattened to one part of the variable (w.%W1.%B0 is
// w.%B2), exact by construction (naut-lead). TIA has no slice as wide as
// its variable (Dx.%D0, Bx.%B0) and no .%L at all: such a part of a
// variable of another type reads as its bit pattern (DINT_TO_DWORD,
// LINT_TO_LWORD) and writing it is refused; of its own type (Bx.%B0,
// lw.%L0) it is the variable.

// partMember is one member's part: its width in bits and its index.
func partMember(member string) (width, index int, ok bool) {
	if n, ok := bitIndex(member); ok {
		return 1, n, true
	}
	if len(member) < 3 || member[0] != '%' {
		return 0, 0, false
	}
	w := map[byte]int{'B': 8, 'W': 16, 'D': 32, 'L': 64}[member[1]&^0x20]
	n, err := strconv.Atoi(member[2:])
	if w == 0 || err != nil || n < 0 || member[2] == '+' {
		return 0, 0, false
	}
	return w, n, true
}

// partOf flattens a partial access, a part of a part included, to one
// part of the variable it reads: that variable, the part's width in bits
// and its offset in bits. ok is false when e is no partial access.
func partOf(e *st.MemberExpr) (obj st.Expression, width, offset int, ok bool) {
	width, idx, ok := partMember(e.Member)
	if !ok {
		return nil, 0, 0, false
	}
	obj, offset = e.Object, idx*width
	for {
		m, isMember := obj.(*st.MemberExpr)
		if !isMember {
			break
		}
		w, i, isPart := partMember(m.Member)
		if !isPart {
			break
		}
		offset += i * w
		obj = m.Object
	}
	return obj, width, offset, true
}

// partType is the type a part of width bits reads as.
func partType(width int) string {
	return map[int]string{1: "BOOL", 8: "BYTE", 16: "WORD", 32: "DWORD", 64: "LWORD"}[width]
}

// sliceOf is TIA's slice access of a part of width bits at offset.
func sliceOf(width, offset int) string {
	letter := map[int]string{1: "X", 8: "B", 16: "W", 32: "D"}[width]
	return ".%" + letter + strconv.Itoa(offset/width)
}

// partAccess writes a part of an integer or bit string, read or written.
func (pw *progWriter) partAccess(e *st.MemberExpr) string {
	obj, width, offset, _ := partOf(e)
	t := pw.typ(obj)
	if !intOrBits(t) {
		pw.gap("siemens/partial", e.Pos.Line, gapOther, "partial access on "+orUnknown(t), "")
		return ""
	}
	wide := pw.wide
	pw.wide = false
	s := pw.expr(obj, unaryPrec+1)
	pw.wide = wide
	part := partType(width)
	if fullPart(t, width, offset) {
		// TIA has no slice as wide as its variable (Dx.%D0, Bx.%B0: "Invalid
		// access", bench 2026-11-27) and none at all of 64 bits: the part is
		// the variable, or its bit pattern (DINT_TO_DWORD, LINT_TO_LWORD).
		if t != part {
			s = t + "_TO_" + part + "(" + s + ")"
		}
		return pw.widen(part, s)
	}
	if width == 64 {
		pw.gap("siemens/partial", e.Pos.Line, gapOther, ".%L0 of "+orUnknown(t), "")
		return ""
	}
	// In 64-bit integer arithmetic a part is widened like any BYTE/WORD/
	// DWORD variable (nautilus computes with its value at 64 bits).
	return pw.widen(part, s+sliceOf(width, offset))
}

// fullPart reports whether a part of width bits at offset is the whole
// of a variable of type t.
func fullPart(t string, width, offset int) bool {
	return offset == 0 && bitsFor(t) != "" && bitsFor(t) == partType(width)
}

// refusedPartWrite refuses a write TIA has no slice for: a part as wide
// as a variable of another type (Dx.%D0 of a DINT, .%L0 of a LINT), which
// reads as the variable's bit pattern. It reports whether it did.
func (pw *progWriter) refusedPartWrite(target st.Expression, line int) bool {
	m, ok := target.(*st.MemberExpr)
	if !ok {
		return false
	}
	obj, width, offset, ok := partOf(m)
	if !ok {
		return false
	}
	t := pw.typ(obj)
	if !fullPart(t, width, offset) || t == partType(width) {
		return false
	}
	pw.gap("siemens/partial", line, gapOther, "writing ."+m.Member+" of "+orUnknown(t), "TIA has no slice as wide as the variable; assign the whole variable ("+partType(width)+"_TO_"+t+"(…))")
	return true
}

// allOnes is the typed literal of a part of width bits, all set.
func allOnes(width int) string {
	return fmt.Sprintf("%s#16#%X", partType(width), uint64(1)<<width-1)
}

// partObject is the variable a partial access reads (w in w.%B1, w.%W1.%X3), or nil.
func partObject(e st.Expression) st.Expression {
	if m, ok := e.(*st.MemberExpr); ok {
		if obj, _, _, ok := partOf(m); ok {
			return obj
		}
	}
	return nil
}
