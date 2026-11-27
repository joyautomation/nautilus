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
		case t == "LREAL" || t == tAnyReal:
			in[i] = args[i] // a literal takes the LReal formal's type, all its digits
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
func atan2Source(prefix, fn string) []byte {
	name := quote(prefix + fn)
	r := "#" + prefix + fn
	const (
		pi   = "3.141592653589793"
		pi2  = "1.5707963267948966"
		pi4  = "0.7853981633974483"
		pi34 = "2.356194490192345"
	)
	// sign(v, ref, in): v with ref's sign (ref is non-zero here), its
	// lines indented by in after the first.
	sign := func(v, ref, in string) string {
		return fmt.Sprintf("IF %s < 0.0 THEN\n%s\t%s := -%s;\n%sELSE\n%s\t%s := %s;\n%sEND_IF;", ref, in, r, v, in, in, r, v, in)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "FUNCTION %s : LReal\n", name)
	b.WriteString("{ S7_Optimized_Access := 'TRUE' }\nVERSION : 0.1\n")
	b.WriteString("   VAR_INPUT \n      Y : LReal;\n      X : LReal;\n   END_VAR\n")
	b.WriteString("   VAR_TEMP \n      q : LReal;\n      w : LWord;\n   END_VAR\n\nBEGIN\n")
	b.WriteString("\t// Go's math.Atan2, case by case.\n")
	fmt.Fprintf(&b, "\tIF #Y <> #Y OR #X <> #X THEN\n\t\t%s := #Y + #X; // NaN\n", r)
	b.WriteString("\tELSIF #Y = 0.0 THEN\n")
	b.WriteString("\t\t#w := LREAL_TO_LWORD(#X);\n")
	fmt.Fprintf(&b, "\t\tIF #X >= 0.0 AND NOT #w.%%X63 THEN\n\t\t\t%s := #Y; // ±0, Y's sign\n", r)
	b.WriteString("\t\tELSE\n\t\t\t#w := LREAL_TO_LWORD(#Y);\n")
	fmt.Fprintf(&b, "\t\t\tIF #w.%%X63 THEN\n\t\t\t\t%s := -%s;\n\t\t\tELSE\n\t\t\t\t%s := %s;\n\t\t\tEND_IF;\n\t\tEND_IF;\n", r, pi, r, pi)
	fmt.Fprintf(&b, "\tELSIF #X = 0.0 THEN\n\t\t%s\n", sign(pi2, "#Y", "\t\t"))
	fmt.Fprintf(&b, "\tELSIF #X > 1.7976931348623157E+308 THEN // +inf\n\t\tIF ABS(#Y) > 1.7976931348623157E+308 THEN\n\t\t\t%s\n\t\tELSIF #Y < 0.0 THEN\n\t\t\t%s := LWORD_TO_LREAL(16#8000000000000000); // -0\n\t\tELSE\n\t\t\t%s := 0.0;\n\t\tEND_IF;\n", sign(pi4, "#Y", "\t\t\t"), r, r)
	fmt.Fprintf(&b, "\tELSIF #X < -1.7976931348623157E+308 THEN // -inf\n\t\tIF ABS(#Y) > 1.7976931348623157E+308 THEN\n\t\t\t%s\n\t\tELSE\n\t\t\t%s\n\t\tEND_IF;\n", sign(pi34, "#Y", "\t\t\t"), sign(pi, "#Y", "\t\t\t"))
	fmt.Fprintf(&b, "\tELSIF ABS(#Y) > 1.7976931348623157E+308 THEN\n\t\t%s\n", sign(pi2, "#Y", "\t\t"))
	b.WriteString("\tELSE\n\t\t#q := ATAN(#Y / #X);\n")
	fmt.Fprintf(&b, "\t\tIF #X < 0.0 THEN\n\t\t\tIF #q <= 0.0 THEN\n\t\t\t\t%s := #q + %s;\n\t\t\tELSE\n\t\t\t\t%s := #q - %s;\n\t\t\tEND_IF;\n\t\tELSE\n\t\t\t%s := #q;\n\t\tEND_IF;\n", r, pi, r, pi, r)
	b.WriteString("\tEND_IF;\nEND_FUNCTION\n")
	return []byte(b.String())
}
