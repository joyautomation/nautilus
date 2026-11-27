package writer

import (
	"fmt"
	"strings"

	"github.com/joyautomation/nautilus/lang/st"
)

// ATAN2 (#1305 B2). nautilus's ATAN2(Y, X) is Go's math.Atan2 at 64 bits
// (lang/ir builtins_std.go); TIA has no ATAN2. The writer calls a FUNCTION
// of its own, on LReal, that takes Go's cases in Go's order (NaN; Y = ±0
// by X's sign bit; X = ±0; X = ±inf; Y = ±inf; then ATAN(Y / X) moved by
// ±π when X < 0), with float64's π constants and signed zeros read and
// made through the bit pattern. Its result converts to a REAL target as
// any REAL function's does.

// atan2Helper is the helper's name, after the block prefix.
const atan2Helper = "ATAN2"

func (pw *progWriter) atan2(argExprs []st.Expression, args []string, line int) string {
	if len(args) != 2 {
		pw.diag("siemens/call", line, "ATAN2 takes two arguments")
		return ""
	}
	in := make([]string, 2)
	for i, a := range argExprs {
		switch t := pw.typ(a); {
		case t == "LREAL":
			in[i] = args[i]
		case t == tAnyReal: // the formal is LReal: the literal is an LREAL (#1429)
			in[i] = pw.inLREAL(true, func() string { return pw.expr(a, 0) })
		case t == "REAL":
			in[i] = "REAL_TO_LREAL(" + args[i] + ")"
		case isInt(t) && t != "ULINT":
			in[i] = t + "_TO_LREAL(" + args[i] + ")" // nautilus: the integer as a float64
		case t == tAnyInt:
			in[i] = args[i] + ".0"
		default:
			pw.gap("siemens/function", line, gapOther, "ATAN2 of an argument of type "+orUnknown(t), "")
			return ""
		}
	}
	pw.w.useGuarded(atan2Helper, pw.path)
	call := quote(pw.w.prefix+atan2Helper) + "(Y := " + in[0] + ", X := " + in[1] + ")"
	if pw.typ(&st.CallExpr{Name: "ATAN2", Args: argExprs}) == "LREAL" {
		return call
	}
	return "LREAL_TO_REAL(" + call + ")"
}

// atan2Source is the SCL of the ATAN2 helper (Go's math.Atan2).
//
// Every float64 constant is written as its bit pattern
// (LWORD_TO_LREAL(16#…)): TIA keeps 15 significant digits of an LReal
// literal (measured on the bench, #1305 B2), short of float64's 17, and
// would round π. Infinity is read from the exponent bits, never compared
// with a literal.
func atan2Source(prefix, fn string) []byte {
	name := quote(prefix + fn)
	r := "#" + prefix + fn
	c := func(bits string) string { return "LWORD_TO_LREAL(16#" + bits + ")" }
	pi, pi2, pi4, pi34 := c("400921FB54442D18"), c("3FF921FB54442D18"), c("3FE921FB54442D18"), c("4002D97C7F3321D2")
	const expo = "16#7FF0000000000000"
	// sign(v, ref, in): v with ref's sign (ref is non-zero here), its
	// lines indented by in after the first.
	sign := func(v, ref, in string) string {
		return fmt.Sprintf("IF %s < 0.0 THEN\n%s\t%s := -%s;\n%sELSE\n%s\t%s := %s;\n%sEND_IF;", ref, in, r, v, in, in, r, v, in)
	}
	inf := func(w string) string { return "(" + w + " AND " + expo + ") = " + expo }
	var b strings.Builder
	fmt.Fprintf(&b, "FUNCTION %s : LReal\n", name)
	b.WriteString("{ S7_Optimized_Access := 'TRUE' }\nVERSION : 0.1\n")
	b.WriteString("   VAR_INPUT \n      Y : LReal;\n      X : LReal;\n   END_VAR\n")
	b.WriteString("   VAR_TEMP \n      q : LReal;\n      wy : LWord;\n      wx : LWord;\n   END_VAR\n\nBEGIN\n")
	b.WriteString("\t// Go's math.Atan2, case by case, on the bit patterns.\n")
	b.WriteString("\t#wy := LREAL_TO_LWORD(#Y);\n\t#wx := LREAL_TO_LWORD(#X);\n")
	fmt.Fprintf(&b, "\tIF #Y <> #Y OR #X <> #X THEN\n\t\t%s := #Y + #X; // NaN\n", r)
	b.WriteString("\tELSIF #Y = 0.0 THEN\n")
	fmt.Fprintf(&b, "\t\tIF #X >= 0.0 AND NOT #wx.%%X63 THEN\n\t\t\t%s := #Y; // ±0, Y's sign\n", r)
	fmt.Fprintf(&b, "\t\tELSIF #wy.%%X63 THEN\n\t\t\t%s := -%s;\n\t\tELSE\n\t\t\t%s := %s;\n\t\tEND_IF;\n", r, pi, r, pi)
	fmt.Fprintf(&b, "\tELSIF #X = 0.0 THEN\n\t\t%s\n", sign(pi2, "#Y", "\t\t"))
	fmt.Fprintf(&b, "\tELSIF %s AND NOT #wx.%%X63 THEN // +inf\n\t\tIF %s THEN\n\t\t\t%s\n\t\tELSIF #Y < 0.0 THEN\n\t\t\t%s := LWORD_TO_LREAL(16#8000000000000000); // -0\n\t\tELSE\n\t\t\t%s := 0.0;\n\t\tEND_IF;\n", inf("#wx"), inf("#wy"), sign(pi4, "#Y", "\t\t\t"), r, r)
	fmt.Fprintf(&b, "\tELSIF %s THEN // -inf\n\t\tIF %s THEN\n\t\t\t%s\n\t\tELSE\n\t\t\t%s\n\t\tEND_IF;\n", inf("#wx"), inf("#wy"), sign(pi34, "#Y", "\t\t\t"), sign(pi, "#Y", "\t\t\t"))
	fmt.Fprintf(&b, "\tELSIF %s THEN\n\t\t%s\n", inf("#wy"), sign(pi2, "#Y", "\t\t"))
	b.WriteString("\tELSE\n\t\t#q := ATAN(#Y / #X);\n")
	fmt.Fprintf(&b, "\t\tIF #X < 0.0 THEN\n\t\t\tIF #q <= 0.0 THEN\n\t\t\t\t%s := #q + %s;\n\t\t\tELSE\n\t\t\t\t%s := #q - %s;\n\t\t\tEND_IF;\n\t\tELSE\n\t\t\t%s := #q;\n\t\tEND_IF;\n", r, pi, r, pi, r)
	b.WriteString("\tEND_IF;\nEND_FUNCTION\n")
	return []byte(b.String())
}
