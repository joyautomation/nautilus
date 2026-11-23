package writer

import (
	"fmt"
	"strings"

	"github.com/joyautomation/nautilus/lang/st"
)

// Expression types, as far as the writer must know them to write the
// same arithmetic as nautilus. Integer types are their IEC names; these
// are the extra classes.
const (
	tAnyInt  = "ANY_INT"  // an integer literal: takes its operand's type
	tAnyReal = "ANY_REAL" // a real literal
	// tWide is integer arithmetic. nautilus evaluates it at 64 bits and
	// wraps only when the result is stored (#800); SCL would compute at
	// the operands' width. The writer widens every operand to LINT and
	// wraps explicitly at the store, and refuses wide arithmetic anywhere
	// it cannot do that.
	tWide = "WIDE"
)

// intWidth is the width and signedness of each integer type nautilus wraps
// to on a store. LINT needs no wrap at the store (the arithmetic is 64-bit
// already); ULINT arithmetic is refused.
var intWidth = map[string]struct {
	bits   uint
	signed bool
}{
	"SINT": {8, true}, "INT": {16, true}, "DINT": {32, true}, "LINT": {64, true},
	"USINT": {8, false}, "UINT": {16, false}, "UDINT": {32, false}, "ULINT": {64, false},
}

func isInt(t string) bool { _, ok := intWidth[t]; return ok }

// bitStrings are the bit string types by width. nautilus holds one as an
// unsigned integer of its width.
var bitStrings = map[string]uint{"BYTE": 8, "WORD": 16, "DWORD": 32, "LWORD": 64}

func isBits(t string) bool { _, ok := bitStrings[t]; return ok }

// bitsFor is the bit string of an integer or bit string type's width:
// INT and UINT → WORD. SCL's bitwise operators, shifts and rotates are
// written on it, so the operation acts on exactly the type's bits.
func bitsFor(t string) string {
	if isBits(t) {
		return t
	}
	switch intWidth[t].bits {
	case 8:
		return "BYTE"
	case 16:
		return "WORD"
	case 32:
		return "DWORD"
	case 64:
		return "LWORD"
	}
	return ""
}

// unsignedOf is the unsigned integer type of a bit string's width.
func unsignedOf(t string) string {
	return map[string]string{"BYTE": "USINT", "WORD": "UINT", "DWORD": "UDINT", "LWORD": "ULINT"}[t]
}

// intOrBits is an integer or a bit string type: what nautilus's bitwise
// operators, shifts and rotates take.
func intOrBits(t string) bool { return isInt(t) || isBits(t) }

// isIntish is an integer-valued type class: a bit string is one too, an
// unsigned integer of its width in nautilus's arithmetic.
func isIntish(t string) bool { return isInt(t) || isBits(t) || t == tAnyInt || t == tWide }

// unsigned64 is a 64-bit unsigned type, whose arithmetic the writer does
// not translate: LINT, its wide type, does not hold it.
func unsigned64(t string) bool { return t == "ULINT" || t == "LWORD" }

func isReal(t string) bool { return t == "REAL" || t == "LREAL" || t == tAnyReal }

// narrow is an integer type that must be widened to LINT inside wide
// arithmetic.
func narrow(t string) bool { return isInt(t) && t != "LINT" || isBits(t) && t != "LWORD" }

// instMembers are the members of the instances the subset writes, by
// nautilus's types: a counter's PV and CV are nautilus's 64-bit int.
var instMembers = map[string]map[string]string{
	"TON":    {"IN": "BOOL", "PT": "TIME", "Q": "BOOL", "ET": "TIME"},
	"TOF":    {"IN": "BOOL", "PT": "TIME", "Q": "BOOL", "ET": "TIME"},
	"TP":     {"IN": "BOOL", "PT": "TIME", "Q": "BOOL", "ET": "TIME"},
	"CTU":    {"CU": "BOOL", "R": "BOOL", "PV": "LINT", "Q": "BOOL", "CV": "LINT"},
	"CTD":    {"CD": "BOOL", "LD": "BOOL", "PV": "LINT", "Q": "BOOL", "CV": "LINT"},
	"CTUD":   {"CU": "BOOL", "CD": "BOOL", "R": "BOOL", "LD": "BOOL", "PV": "LINT", "QU": "BOOL", "QD": "BOOL", "CV": "LINT"},
	"R_TRIG": {"CLK": "BOOL", "Q": "BOOL"},
	"F_TRIG": {"CLK": "BOOL", "Q": "BOOL"},
	// SR and RS have no SCL instruction: the writer keeps each instance
	// as a static STRUCT of its slots and writes the latch inline.
	"SR": {"S1": "BOOL", "R": "BOOL", "Q1": "BOOL"},
	"RS": {"S": "BOOL", "R1": "BOOL", "Q1": "BOOL"},
}

// typ returns an expression's type class, or "" when the subset cannot
// tell (the caller refuses where it matters).
func (pw *progWriter) typ(e st.Expression) string {
	switch e := e.(type) {
	case *st.NumberLit:
		if e.Base == 10 || e.Base == 0 {
			if strings.ContainsAny(e.Value, ".eE") {
				return tAnyReal
			}
		}
		return tAnyInt
	case *st.BoolLit:
		return "BOOL"
	case *st.TimeLit:
		return "TIME"
	case *st.StringLit:
		return "STRING"
	case *st.TypedLit:
		return strings.ToUpper(e.TypeName)
	case *st.IdentExpr:
		if l, ok := pw.locals[strings.ToLower(e.Name)]; ok {
			return l.typ
		}
		if g, ok := pw.w.globals[strings.ToLower(e.Name)]; ok {
			return strings.ToUpper(strings.TrimSpace(g.Type))
		}
	case *st.MemberExpr:
		if _, ok := bitIndex(e.Member); ok && intOrBits(pw.typ(e.Object)) {
			return "BOOL"
		}
		if id, ok := e.Object.(*st.IdentExpr); ok {
			if l, ok := pw.locals[strings.ToLower(id.Name)]; ok {
				if ms, ok := instMembers[l.typ]; ok {
					return ms[strings.ToUpper(e.Member)]
				}
			}
		}
	case *st.UnaryExpr:
		t := pw.typ(e.Operand)
		if strings.EqualFold(e.Op, "NOT") {
			if t == "BOOL" || t == "" {
				return t
			}
			// nautilus complements the 64-bit value: a result only its
			// store cuts back to a width (lang/st lowerUnary).
			return tWide
		}
		if isIntish(t) {
			return tWide
		}
		return t
	case *st.BinaryExpr:
		op := strings.ToUpper(e.Op)
		l, r := pw.typ(e.Left), pw.typ(e.Right)
		switch op {
		case "=", "<>", "<", ">", "<=", ">=":
			return "BOOL"
		case "AND", "OR", "XOR", "&":
			if l == "BOOL" && r == "BOOL" {
				return "BOOL"
			}
			if t, ok := pw.sameWidth(e); ok {
				return t
			}
			if (intOrBits(l) || isIntish(l)) && (intOrBits(r) || isIntish(r)) {
				return tWide
			}
			return ""
		}
		if l == "LREAL" || r == "LREAL" {
			return "LREAL"
		}
		if isReal(l) || isReal(r) {
			return "REAL"
		}
		if isIntish(l) && isIntish(r) {
			if l == tAnyInt && r == tAnyInt {
				return tAnyInt
			}
			return tWide
		}
		return ""
	case *st.CallExpr:
		name := strings.ToUpper(e.Name)
		args, _ := pw.positional(e)
		if _, to, ok := strings.Cut(name, "_TO_"); ok {
			return to
		}
		switch name {
		case "TRUNC":
			return "LINT"
		case "SHL", "SHR", "ROL", "ROR":
			if len(args) > 0 {
				if t := pw.typ(args[0]); intOrBits(t) {
					return t
				}
			}
			return ""
		case "MUX":
			if len(args) > 1 {
				return commonType(pw, args[1:])
			}
			return ""
		}
		if strings.HasPrefix(name, "TRUNC_") || strings.Contains(name, "_TRUNC_") {
			_, to, _ := strings.Cut(name, "TRUNC_")
			return to
		}
		if to, ok := strings.CutPrefix(name, "TO_"); ok {
			return to
		}
		if mathFuncs[name] || name == "EXPT" {
			if len(args) > 0 && pw.typ(args[0]) == "LREAL" {
				return "LREAL"
			}
			return "REAL"
		}
		if (namedFuncs[name] != nil || name == "MIN" || name == "MAX") && len(args) > 0 {
			if name == "SEL" && len(args) == 3 {
				args = args[1:]
			}
			return commonType(pw, args)
		}
	}
	return ""
}

// commonType is the type MIN/MAX/LIMIT/SEL return for these arguments: the
// widest integer or real among them.
func commonType(pw *progWriter, args []st.Expression) string {
	best := ""
	for _, a := range args {
		t := pw.typ(a)
		switch {
		case t == tWide:
			return tWide
		case t == "LREAL":
			best = "LREAL"
		case isReal(t) && best != "LREAL":
			best = "REAL"
		case isInt(t) && !isReal(best) && (best == "" || best == tAnyInt || intWidth[t].bits > intWidth[best].bits):
			best = t
		case t == tAnyInt && best == "":
			best = tAnyInt
		case isBits(t) && best == "":
			best = t
		case t != "" && !isIntish(t) && !isReal(t):
			best = t
		}
	}
	return best
}

// wrap returns v (a LINT expression) wrapped to the integer type t the way
// nautilus wraps a store: two's complement for a signed type, modulo for
// an unsigned one. The result is converted to t, and is always in range.
func wrap(v, t string) string {
	w := intWidth[t]
	if t == "LINT" {
		return v
	}
	m := uint64(1) << w.bits
	v = "(" + v + ")" // MOD binds tighter than + and -
	if w.signed {
		h := m / 2
		return fmt.Sprintf("LINT_TO_%s(((%s + LINT#%d) MOD LINT#%d + LINT#%d) MOD LINT#%d - LINT#%d)", t, v, h, m, m, m, h)
	}
	return fmt.Sprintf("LINT_TO_%s((%s MOD LINT#%d + LINT#%d) MOD LINT#%d)", t, v, m, m, m)
}

// zero is the SCL start value of a type, for re-initialising temps.
func zero(t string) (string, bool) {
	switch {
	case t == "BOOL":
		return "FALSE", true
	case isInt(t), t == "BYTE", t == "WORD", t == "DWORD":
		return "0", true
	case t == "REAL", t == "LREAL":
		return "0.0", true
	case t == "TIME":
		return "T#0ms", true
	}
	return "", false
}

// inRange reports whether n fits the integer type t.
func inRange(n int64, t string) bool {
	w := intWidth[t]
	if w.bits == 64 {
		return w.signed || n >= 0
	}
	if w.signed {
		h := int64(1) << (w.bits - 1)
		return n >= -h && n < h
	}
	return n >= 0 && n < int64(1)<<w.bits
}

// timeLit prints a time literal for TIA's TIME, which is a 32-bit count of
// milliseconds, or says why it cannot be written exactly: a sub-millisecond
// unit (an LTIME literal such as LT#1500us lexes the same as a TIME one,
// so this is also what refuses it) or a value beyond the 32-bit range.
func timeLit(raw string) (string, string) {
	r := strings.TrimSpace(raw)
	body := strings.TrimPrefix(r, "-")
	for i := 0; i < len(body); {
		j := i
		for j < len(body) && body[j] >= '0' && body[j] <= '9' {
			j++
		}
		k := j
		for k < len(body) && (body[k] < '0' || body[k] > '9') {
			k++
		}
		unit := strings.ToLower(strings.TrimRight(body[j:k], "_"))
		switch {
		case j == i:
			return "", gapMessage(gapTime, "the time literal "+raw, "only whole d, h, m, s and ms are exact in TIA's TIME")
		case unit == "d" || unit == "h" || unit == "m" || unit == "s" || unit == "ms":
		default:
			return "", gapMessage(gapTime, "the time literal "+raw, "only whole d, h, m, s and ms are exact in TIA's TIME, which counts milliseconds (an LTIME literal reads the same)")
		}
		i = k
	}
	if ms := int64(st.ParseTimeMs(body)); ms > 2147483647 {
		return "", gapMessage(gapTime, "the time literal "+raw, "it is beyond TIA's TIME range (about 24.8 days)")
	}
	return "T#" + r, ""
}

// holds reports whether every value of integer type from is a value of
// integer type to: then a conversion between them is exact and needs no
// wrap.
func holds(to, from string) bool {
	t, f := intWidth[to], intWidth[from]
	switch {
	case t.signed && f.signed:
		return t.bits >= f.bits
	case t.signed && !f.signed:
		return t.bits > f.bits
	case !t.signed && !f.signed:
		return t.bits >= f.bits
	}
	return false
}
