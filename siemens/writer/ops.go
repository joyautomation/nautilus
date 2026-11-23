package writer

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/joyautomation/nautilus/lang/st"
)

// nautilus's bitwise operators act on the 64-bit values of their operands
// (lang/ir vm.go), and its integer NOT is a 64-bit result its store cuts
// back (lang/st lowerUnary). The writer writes them two ways:
//
//   - same width: both operands of one integer or bit string type T (or
//     one an integer literal T holds). The operation on T's bits is then
//     the 64-bit one cut to T, and T holds the result: written on the bit
//     string of T's width (INT_TO_WORD(a) AND INT_TO_WORD(b)) and
//     converted back, so it never depends on how TIA treats AND of INTs;
//   - otherwise, 64 bits: each operand widened to LINT, as LWORD, and the
//     result back as LINT, which the store wraps like any integer
//     arithmetic (types.go).

// litInt is the value of an integer literal, signed or not.
func litInt(e st.Expression) (int64, bool) {
	switch v := e.(type) {
	case *st.NumberLit:
		if v.Base == 10 || v.Base == 0 {
			if strings.ContainsAny(v.Value, ".eE") {
				return 0, false
			}
			n, err := strconv.ParseInt(strings.ReplaceAll(v.Value, "_", ""), 10, 64)
			return n, err == nil
		}
		n, err := strconv.ParseUint(strings.ReplaceAll(v.Value, "_", ""), v.Base, 64)
		return int64(n), err == nil
	case *st.UnaryExpr:
		if v.Op == "-" {
			n, ok := litInt(v.Operand)
			return -n, ok
		}
	}
	return 0, false
}

// fits reports whether n is a value of the integer or bit string type t.
func fits(n int64, t string) bool {
	if w, ok := bitStrings[t]; ok {
		return w == 64 || n >= 0 && n < int64(1)<<w
	}
	return inRange(n, t)
}

// bitsLit prints n as a literal of bit string type bits, two's complement.
func bitsLit(n int64, bits string) string {
	w := bitStrings[bits]
	u := uint64(n)
	if w < 64 {
		u &= uint64(1)<<w - 1
	}
	return fmt.Sprintf("%s#16#%X", bits, u)
}

// sameWidth is the type T of a bitwise AND, OR or XOR written at its
// operands' width, when there is one.
func (pw *progWriter) sameWidth(e *st.BinaryExpr) (string, bool) {
	return sameWidthOf(e, pw.typ(e.Left), pw.typ(e.Right))
}

// sameWidthOf is sameWidth with the operands' types already known: typ
// calls it with the types it has, so a chain of N bitwise operators types
// each operand once, not 2^N times.
func sameWidthOf(e *st.BinaryExpr, lt, rt string) (string, bool) {
	if intOrBits(lt) && lt == rt {
		return lt, true
	}
	// A literal (-2 included, which types as arithmetic) takes the other
	// operand's type in nautilus, when that type holds it.
	if n, ok := litInt(e.Right); ok && intOrBits(lt) {
		return lt, fits(n, lt)
	}
	if n, ok := litInt(e.Left); ok && intOrBits(rt) {
		return rt, fits(n, rt)
	}
	return "", false
}

// bitwise writes AND, OR or XOR of integers or bit strings.
func (pw *progWriter) bitwise(e *st.BinaryExpr, op string, parent int) string {
	if t, ok := pw.sameWidth(e); ok {
		wide := pw.wide
		pw.wide = false
		bits := bitsFor(t)
		s := pw.asBits(e.Left, t, bits) + " " + op + " " + pw.asBits(e.Right, t, bits)
		pw.wide = wide
		if t != bits {
			return pw.widen(t, bits+"_TO_"+t+"("+s+")")
		}
		if pw.wide && narrow(t) {
			return pw.widen(t, s)
		}
		return paren(s, precedence[op], parent)
	}
	lt, rt := pw.typ(e.Left), pw.typ(e.Right)
	if !(intOrBits(lt) || isIntish(lt)) || !(intOrBits(rt) || isIntish(rt)) {
		pw.gap("siemens/operator", e.Pos.Line, gapOther, fmt.Sprintf("%s on %s and %s", op, orUnknown(lt), orUnknown(rt)), "")
		return ""
	}
	if unsigned64(lt) && lt != rt || unsigned64(rt) && lt != rt {
		pw.gap("siemens/int-width", e.Pos.Line, gapOther, op+" of "+lt+" and "+orUnknown(rt), "")
		return ""
	}
	if !pw.wide {
		pw.wideRefused(e.Pos.Line)
		return ""
	}
	return "LWORD_TO_LINT(" + pw.asLWORD(e.Left) + " " + op + " " + pw.asLWORD(e.Right) + ")"
}

// asBits prints an operand of type t (or an integer literal t holds) as
// the bit string bits of t's width.
func (pw *progWriter) asBits(e st.Expression, t, bits string) string {
	if n, ok := litInt(e); ok {
		return bitsLit(n, bits)
	}
	s := pw.expr(e, precedence["AND"]+1)
	if t == bits {
		return s
	}
	return t + "_TO_" + bits + "(" + s + ")"
}

// asLWORD prints an operand as the LWORD of its 64-bit value, in wide
// mode: an integer operand prints widened to LINT already.
func (pw *progWriter) asLWORD(e st.Expression) string {
	if n, ok := litInt(e); ok {
		return bitsLit(n, "LWORD")
	}
	// In wide mode every narrower operand prints widened to LINT
	// already (a bit string zero-extended, as nautilus holds it).
	s := pw.expr(e, 0)
	if pw.typ(e) == "LWORD" {
		return s
	}
	return "LINT_TO_LWORD(" + s + ")"
}

// bitwiseNot writes NOT of an integer or a bit string: the complement of
// the 64-bit value, as nautilus has it. Stored straight into the
// operand's own type, the store writes it at that width instead (store).
func (pw *progWriter) bitwiseNot(e *st.UnaryExpr, t string) string {
	if !intOrBits(t) && !isIntish(t) {
		pw.gap("siemens/operator", e.Pos.Line, gapOther, "NOT of "+orUnknown(t), "")
		return ""
	}
	if unsigned64(t) {
		pw.gap("siemens/int-width", e.Pos.Line, gapOther, "NOT of a "+t+" beyond a store into its own type", "")
		return ""
	}
	if !pw.wide {
		pw.wideRefused(e.Pos.Line)
		return ""
	}
	return "LWORD_TO_LINT(NOT " + pw.asLWORD(e.Operand) + ")"
}

// narrowNot writes v := NOT x, x of v's own type t, at t's width: the
// 64-bit complement cut to t is the complement of t's bits.
func (pw *progWriter) narrowNot(v st.Expression, tt string) (string, bool) {
	u, ok := v.(*st.UnaryExpr)
	if !ok || !strings.EqualFold(u.Op, "NOT") || !intOrBits(tt) || pw.typ(u.Operand) != tt {
		return "", false
	}
	bits := bitsFor(tt)
	s := pw.expr(u.Operand, unaryPrec)
	if tt == bits {
		return "NOT " + s, true
	}
	return bits + "_TO_" + tt + "(NOT " + tt + "_TO_" + bits + "(" + s + "))", true
}

// The guarded divisions: nautilus gives 0 for a zero divisor (lang/ir
// vm.go). The writer calls a function that does the same, one per type,
// generated only when a program uses it. Integer division is at 64 bits
// already (types.go), so one LINT function covers every integer type.
var guardedFuncs = map[string]struct{ typ, op string }{
	"DIV_LINT":  {"LINT", "/"},
	"MOD_LINT":  {"LINT", "MOD"},
	"DIV_REAL":  {"REAL", "/"},
	"DIV_LREAL": {"LREAL", "/"},
}

// guardedDivision writes l / r or l MOD r, r not a non-zero constant.
func (pw *progWriter) guardedDivision(e *st.BinaryExpr, op string) string {
	t := pw.typ(e)
	var fn string
	switch {
	case t == tWide || t == tAnyInt:
		fn = "DIV_LINT"
		if op == "MOD" {
			fn = "MOD_LINT"
		}
	case op == "MOD":
		pw.gap("siemens/division", e.Pos.Line, gapOther, "MOD of "+t, "")
		return ""
	case t == "REAL" || t == tAnyReal:
		fn = "DIV_REAL"
	case t == "LREAL":
		fn = "DIV_LREAL"
	default:
		pw.gap("siemens/operator", e.Pos.Line, gapOther, fmt.Sprintf("%s of %s", op, orUnknown(t)), "")
		return ""
	}
	pw.w.useGuarded(fn, pw.path)
	ft := guardedFuncs[fn].typ
	return quote(pw.w.prefix+fn) + "(A := " + pw.divOperand(e.Left, ft) + ", B := " + pw.divOperand(e.Right, ft) + ")"
}

// divOperand prints an operand of a guarded division of type ft. An
// integer operand of a REAL division is converted explicitly, as
// nautilus converts it.
func (pw *progWriter) divOperand(e st.Expression, ft string) string {
	if ft == "LINT" {
		return pw.expr(e, 0) // wide mode: an integer operand prints as LINT
	}
	// A REAL division's operands are not integer arithmetic: printed at
	// their own type even inside a wide comparison, then converted.
	t := pw.typ(e)
	wide := pw.wide
	pw.wide = false
	s := pw.expr(e, 0)
	pw.wide = wide
	if isInt(t) && t != ft {
		return t + "_TO_" + ft + "(" + s + ")"
	}
	return s
}

// guardedSource is the SCL of one guarded division function.
func guardedSource(prefix, fn string) []byte {
	g := guardedFuncs[fn]
	name := prefix + fn
	zero := "0"
	if g.typ != "LINT" {
		zero = "0.0"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "FUNCTION %s : %s\n", quote(name), g.typ)
	b.WriteString("{ S7_Optimized_Access := 'TRUE' }\n")
	b.WriteString("VERSION : 0.1\n")
	fmt.Fprintf(&b, "   VAR_INPUT \n      A : %s;\n      B : %s;\n   END_VAR\n", g.typ, g.typ)
	b.WriteString("\nBEGIN\n")
	fmt.Fprintf(&b, "\tIF #B = %s THEN\n\t\t#%s := %s;\n\tELSE\n\t\t#%s := #A %s #B;\n\tEND_IF;\n", zero, name, zero, name, g.op)
	b.WriteString("END_FUNCTION\n")
	return []byte(b.String())
}

// A latch is IEC's SR or RS, which TIA has only as a LAD/FBD box. The
// writer keeps an instance as a static STRUCT of its slots, so its inputs
// persist across calls as an instance's do and #f.Q1 reads like any
// instance member, and writes the latch inline (lang/ir builtins_fb.go):
// SR is set-dominant, Q1 := S1 OR (Q1 AND NOT R); RS reset-dominant,
// Q1 := NOT R1 AND (S OR Q1).
type latchDef struct {
	set, reset string
	setWins    bool
}

var latches = map[string]latchDef{
	"SR": {set: "S1", reset: "R", setWins: true},
	"RS": {set: "S", reset: "R1"},
}

func (l latchDef) members() string {
	return fmt.Sprintf("         %s : Bool;\n         %s : Bool;\n         Q1 : Bool;\n", l.set, l.reset)
}

// latch writes a call of an SR or RS instance: its inputs into the
// struct, the latch, then its output bindings.
func (pw *progWriter) latch(b *strings.Builder, c *st.CallExpr, l local, def latchDef, in string, at int) {
	line := c.Pos.Line
	if len(c.Args) > 0 {
		pw.diag("siemens/call", line, l.name+": "+gapMessage(gapOther, "positional instance arguments", "name the inputs ("+def.set+" := …)"))
		return
	}
	self := "#" + sclName(l.name)
	for _, a := range c.NamedArgs {
		pin := strings.ToUpper(a.Name)
		if pin != def.set && pin != def.reset {
			pw.diag("siemens/call", line, fmt.Sprintf("%s has no input %s", l.typ, a.Name))
			return
		}
		pw.emit(b, at, "%s%s.%s := %s;\n", in, self, pin, pw.store(a.Value, "BOOL", line))
	}
	q, s, r := self+".Q1", self+"."+def.set, self+"."+def.reset
	if def.setWins {
		pw.emit(b, at, "%s%s := %s OR (%s AND NOT %s);\n", in, q, s, q, r)
	} else {
		pw.emit(b, at, "%s%s := NOT %s AND (%s OR %s);\n", in, q, r, s, q)
	}
	for _, o := range c.OutputBindings {
		if !strings.EqualFold(o.Name, "Q1") {
			pw.diag("siemens/call", line, fmt.Sprintf("%s has no output %s", l.typ, o.Name))
			return
		}
		pw.emit(b, at, "%s%s := %s;\n", in, pw.expr(o.Target, 0), q)
		pw.assigned(b, in, at, o.Target)
	}
}

// bitIndex is the bit of a bit access member: .3, or IEC's .%X3.
func bitIndex(member string) (int, bool) {
	m := member
	if len(m) > 2 && (m[:2] == "%X" || m[:2] == "%x") {
		m = m[2:]
	}
	if m == "" {
		return 0, false
	}
	n, err := strconv.Atoi(m)
	return n, err == nil && n >= 0 && m[0] != '+'
}

// bitAccess writes a bit of an integer or bit string, read or written,
// as TIA's slice access .%Xn: bit 0 is the least significant, as in
// nautilus (whose lowering already checked n against the width).
func (pw *progWriter) bitAccess(e *st.MemberExpr, n int) string {
	t := pw.typ(e.Object)
	if !intOrBits(t) {
		pw.gap("siemens/partial", e.Pos.Line, gapOther, "bit access on "+orUnknown(t), "")
		return ""
	}
	wide := pw.wide
	pw.wide = false
	s := pw.expr(e.Object, unaryPrec+1)
	pw.wide = wide
	return s + ".%X" + strconv.Itoa(n)
}
