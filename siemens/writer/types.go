package writer

import (
	"fmt"
	"strconv"
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

func isIntish(t string) bool { return isInt(t) || t == tAnyInt || t == tWide }

func isReal(t string) bool { return t == "REAL" || t == "LREAL" || t == tAnyReal }

// narrow is an integer type that must be widened to LINT inside wide
// arithmetic.
func narrow(t string) bool { return isInt(t) && t != "LINT" }

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
			return t
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
		if _, to, ok := strings.Cut(name, "_TO_"); ok {
			return to
		}
		if mathFuncs[name] || name == "EXPT" {
			if len(e.Args) > 0 && pw.typ(e.Args[0]) == "LREAL" {
				return "LREAL"
			}
			return "REAL"
		}
		if namedFuncs[name] != nil && len(e.Args) > 0 {
			args := e.Args
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

// intConstant evaluates an integer literal, or the negation of one.
func intConstant(e st.Expression) (int64, bool) {
	neg := false
	if u, ok := e.(*st.UnaryExpr); ok && u.Op == "-" {
		neg, e = true, u.Operand
	}
	n, ok := e.(*st.NumberLit)
	if !ok {
		return 0, false
	}
	base := n.Base
	if base == 0 {
		base = 10
	}
	v, err := strconv.ParseInt(strings.ReplaceAll(n.Value, "_", ""), base, 64)
	if err != nil {
		return 0, false
	}
	if neg {
		v = -v
	}
	return v, true
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
			return "", fmt.Sprintf("time literal %s: only whole d, h, m, s and ms are written exactly", raw)
		case unit == "d" || unit == "h" || unit == "m" || unit == "s" || unit == "ms":
		default:
			return "", fmt.Sprintf("time literal %s: only whole d, h, m, s and ms are written exactly (TIA's TIME counts milliseconds; an LTIME literal is not in the first subset)", raw)
		}
		i = k
	}
	if ms := int64(st.ParseTimeMs(body)); ms > 2147483647 {
		return "", fmt.Sprintf("time literal %s is beyond TIA's TIME range (about 24.8 days)", raw)
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
