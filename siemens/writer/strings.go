package writer

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/joyautomation/nautilus/lang/st"
)

// STRING (#1303). A nautilus STRING has no length; TIA's String holds at
// most 254 bytes. Both count bytes, and every string function below was
// measured against nautilus's (lang/ir builtins_std.go, the oracles in
// lang/conformance/st-string-clamp and fn-conversions) on an S7-1500
// (FW 3.1, TIA V21, 2026-11-25). They agree except:
//
//   - MID, DELETE and REPLACE with a position P below 1, INSERT with P
//     below 0, and DELETE and REPLACE with a negative length L give an empty string
//     on the CPU, where nautilus clamps P and L into range. The writer
//     clamps those arguments the same way first (strArg), unless they are
//     constants already in range.
//   - A result longer than 254 bytes keeps its first 254 on the CPU and
//     all of them in nautilus: a warning (StringLengthRule).
//   - The integer conversions to STRING write a '+' before a value >= 0
//     on the CPU: a helper function drops it (naut_STR_LINT).
//   - STRING_TO_<number> of text that is not a number: a warning
//     (StringParseRule).
//
// SCL takes the string functions with formal parameters only (TIA
// refuses positional ones, LEN aside), CONCAT with up to 32 inputs, and a
// statement whose calls nest more than 7 deep not at all (#1374): the
// writer flattens a chain of CONCATs into one.

// maxString is the most bytes TIA's String holds.
const maxString = 254

// StringLengthRule tags the warning that a string function's result may
// grow past 254 bytes. Like a REAL → integer conversion it is everywhere,
// so check and the deploy summarize it per program.
const StringLengthRule = "siemens/string-length"

// StringParseRule tags the warning that a STRING_TO_<number> reads text
// that is not a number differently: nautilus faults, the CPU carries on.
const StringParseRule = "siemens/string-parse"

// stringFuncs are nautilus's string functions and TIA's formal parameter
// names for their arguments, in nautilus's order.
var stringFuncs = map[string][]string{
	"LEN":     {"IN"},
	"LEFT":    {"IN", "L"},
	"RIGHT":   {"IN", "L"},
	"MID":     {"IN", "L", "P"},
	"INSERT":  {"IN1", "IN2", "P"},
	"DELETE":  {"IN", "L", "P"},
	"REPLACE": {"IN1", "IN2", "L", "P"},
	"FIND":    {"IN1", "IN2"},
	"CONCAT":  nil, // IN1 … IN32
}

// span is the range an integer argument of a string function is clamped
// into: lo is where TIA starts to differ from nautilus (below it TIA gives
// an empty string), hi a value past the end of any String, which both read alike.
type span struct{ lo, hi int64 }

var (
	spanLen    = span{0, maxString}     // L
	spanPos    = span{1, maxString + 1} // P of MID, DELETE, REPLACE: 1-based
	spanInsert = span{0, maxString}     // P of INSERT: after P, 0 prepends
)

// stringLit prints a STRING literal, or refuses one TIA cannot hold.
func (pw *progWriter) stringLit(v string, line int) string {
	if len(v) > maxString {
		pw.diag("siemens/string-length", line, fmt.Sprintf("this STRING literal is %d bytes; TIA's String holds at most %d", len(v), maxString))
		return ""
	}
	if !ascii(v) {
		pw.gap("siemens/string", line, gapString, "a STRING literal with characters outside ASCII", "how TIA stores them is not measured yet")
		return ""
	}
	return st.QuoteString(v)
}

func ascii(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

// stringCall writes one of nautilus's string functions.
func (pw *progWriter) stringCall(c *st.CallExpr, name string, args []st.Expression) string {
	line := c.Pos.Line
	params := stringFuncs[name]
	if name == "CONCAT" {
		return pw.concat(c, args)
	}
	if len(args) != len(params) {
		pw.diag("siemens/call", line, fmt.Sprintf("%s takes %d arguments", name, len(params)))
		return ""
	}
	if name == "LEN" {
		return "LEN(" + pw.expr(args[0], 0) + ")"
	}
	parts := make([]string, len(args))
	for i, a := range args {
		var s string
		switch {
		case params[i] == "L" && (name == "LEFT" || name == "RIGHT" || name == "MID"):
			// A negative length gives an empty string in both: no lower clamp.
			s = pw.strArg(a, spanLen, false, line)
		case params[i] == "L":
			s = pw.strArg(a, spanLen, true, line)
		case params[i] == "P" && name == "INSERT":
			s = pw.strArg(a, spanInsert, true, line)
		case params[i] == "P":
			s = pw.strArg(a, spanPos, true, line)
		default:
			s = pw.expr(a, 0)
		}
		if s == "" {
			return ""
		}
		parts[i] = params[i] + " := " + s
	}
	if name == "INSERT" || name == "REPLACE" {
		pw.mayGrow(line, name, args[:2])
	}
	return name + "(" + strings.Join(parts, ", ") + ")"
}

// strArg prints a length or position argument as the INT TIA takes,
// clamped into sp where TIA differs from nautilus (low) or where the value
// may not fit an INT. A constant is clamped here; an INT is clamped below
// only; any other integer is computed at 64 bits (as nautilus does) and
// clamped before it narrows to INT, so it cannot wrap.
func (pw *progWriter) strArg(a st.Expression, sp span, low bool, line int) string {
	if n, ok := litInt(a); ok {
		// Exact either way: below lo both give an empty string where low is false.
		return strconv.FormatInt(min(max(n, sp.lo), sp.hi), 10)
	}
	t := pw.typ(a)
	if t == "INT" {
		s := pw.expr(a, 0)
		if !low || s == "" {
			return s
		}
		return fmt.Sprintf("MAX(IN1 := %s, IN2 := %d)", s, sp.lo)
	}
	if !isIntish(t) || unsigned64(t) {
		pw.gap("siemens/string", line, gapString, "a string function's length or position of type "+orUnknown(t), "")
		return ""
	}
	wide := pw.wide
	pw.wide = true
	s := pw.expr(a, 0)
	pw.wide = wide
	if s == "" {
		return ""
	}
	return fmt.Sprintf("LINT_TO_INT(LIMIT(MN := LINT#%d, IN := %s, MX := LINT#%d))", sp.lo, s, sp.hi)
}

// concat writes CONCAT as one call of all its operands, a nested CONCAT's
// included (TIA takes IN1 … IN32, and refuses calls nested 8 deep).
func (pw *progWriter) concat(c *st.CallExpr, args []st.Expression) string {
	ops := pw.concatOperands(args)
	if len(ops) < 2 {
		pw.diag("siemens/call", c.Pos.Line, "CONCAT takes at least 2 arguments")
		return ""
	}
	pw.mayGrow(c.Pos.Line, "CONCAT", ops)
	parts := make([]string, len(ops))
	for i, o := range ops {
		if parts[i] = pw.expr(o, 0); parts[i] == "" {
			return ""
		}
	}
	return concatOf(parts)
}

// concatOperands is args with every nested CONCAT replaced by its own
// operands, in order.
func (pw *progWriter) concatOperands(args []st.Expression) []st.Expression {
	var out []st.Expression
	for _, a := range args {
		if c, ok := a.(*st.CallExpr); ok && strings.EqualFold(c.Name, "CONCAT") && c.Callee == nil && len(c.OutputBindings) == 0 {
			if inner, ok := pw.positional(c); ok && len(inner) >= 2 {
				out = append(out, pw.concatOperands(inner)...)
				continue
			}
		}
		out = append(out, a)
	}
	return out
}

// concatOf is CONCAT of the printed operands, in calls of at most 32.
func concatOf(parts []string) string {
	if len(parts) > 32 {
		var groups []string
		for len(parts) > 0 {
			n := min(32, len(parts))
			if n == 1 {
				groups = append(groups, parts[0])
			} else {
				groups = append(groups, concatOf(parts[:n]))
			}
			parts = parts[n:]
		}
		return concatOf(groups)
	}
	named := make([]string, len(parts))
	for i, p := range parts {
		named[i] = fmt.Sprintf("IN%d := %s", i+1, p)
	}
	return "CONCAT(" + strings.Join(named, ", ") + ")"
}

// mayGrow warns that a function's result may pass 254 bytes, unless its
// operands are literals that add up to no more.
func (pw *progWriter) mayGrow(line int, name string, ops []st.Expression) {
	n := 0
	for _, o := range ops {
		l, ok := o.(*st.StringLit)
		if !ok {
			n = maxString + 1
			break
		}
		n += len(l.Value)
	}
	if n <= maxString {
		return
	}
	pw.w.warnings = append(pw.w.warnings, Diag{Rule: StringLengthRule, Path: pw.path, Line: line,
		Message: fmt.Sprintf("%s here can differ if its result grows past %d bytes: TIA keeps the first %d bytes; nautilus keeps them all", name, maxString, maxString)})
}

// stringConversion writes a conversion to or from STRING.
func (pw *progWriter) stringConversion(name, from, to string, args []string, line int) string {
	if to == "STRING" {
		switch {
		case from == "STRING":
			return args[0]
		case isInt(from):
			// TIA writes '+' before a value >= 0; nautilus does not.
			fn := "STR_LINT"
			arg := toLINT(from, args[0])
			if from == "ULINT" {
				fn, arg = "STR_ULINT", args[0]
			}
			pw.w.useGuarded(fn, pw.path)
			return quote(pw.w.prefix+fn) + "(A := " + arg + ")"
		case from == "BOOL":
			return "SEL(G := " + args[0] + ", IN0 := 'FALSE', IN1 := 'TRUE')"
		}
		pw.gap("siemens/string-conversion", line, gapStringConv, name, stringConvWhy(from))
		return ""
	}
	if isInt(to) || to == "REAL" || to == "LREAL" {
		pw.w.warnings = append(pw.w.warnings, Diag{Rule: StringParseRule, Path: pw.path, Line: line,
			Message: fmt.Sprintf("%s can differ if the text is not a plain decimal number in %s's range: nautilus stops the program (the scan faults); the PLC silently uses 0 or a partial number ('4x' reads as 4) and carries on", name, to)})
		return name + "(" + args[0] + ")"
	}
	pw.gap("siemens/string-conversion", line, gapStringConv, name, stringConvWhy(to))
	return ""
}

// stringConvWhy says why a conversion between STRING and t is not
// written yet.
func stringConvWhy(t string) string {
	switch {
	case isReal(t):
		return "TIA writes +2.150000E+1 where nautilus writes 21.5"
	case t == "TIME":
		return "TIA has no such conversion in SCL; nautilus writes T#5000ms"
	}
	return "TIA has no such conversion in SCL"
}

// stringHelpers are the helper functions the conversions to STRING call.
var stringHelpers = map[string]string{"STR_LINT": "LINT", "STR_ULINT": "ULINT"}

// stringHelperSource is the SCL of one: TIA's conversion, without the '+'
// it writes before a value >= 0.
func stringHelperSource(prefix, fn string) []byte {
	name := prefix + fn
	t := stringHelpers[fn]
	var b strings.Builder
	fmt.Fprintf(&b, "FUNCTION %s : String\n", quote(name))
	b.WriteString("{ S7_Optimized_Access := 'TRUE' }\n")
	b.WriteString("VERSION : 0.1\n")
	fmt.Fprintf(&b, "   VAR_INPUT \n      A : %s;\n   END_VAR\n", t)
	b.WriteString("   VAR_TEMP \n      s : String;\n   END_VAR\n")
	b.WriteString("\nBEGIN\n")
	fmt.Fprintf(&b, "\t#s := %s_TO_STRING(#A);\n", t)
	b.WriteString("\tIF LEFT(IN := #s, L := 1) = '+' THEN\n\t\t#s := DELETE(IN := #s, L := 1, P := 1);\n\tEND_IF;\n")
	fmt.Fprintf(&b, "\t#%s := #s;\n", name)
	b.WriteString("END_FUNCTION\n")
	return []byte(b.String())
}
