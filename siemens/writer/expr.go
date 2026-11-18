package writer

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/joyautomation/nautilus/lang/st"
)

// precedence of the binary operators, IEC 61131-3 Table 71. nautilus's
// parser has no ** operator (EXPT is the function); SCL binds ** tighter
// than unary minus, so EXPT's operands are parenthesised where it is
// written (call).
var precedence = map[string]int{
	"OR": 1, "XOR": 2, "AND": 3, "&": 3,
	"=": 4, "<>": 4,
	"<": 5, ">": 5, "<=": 5, ">=": 5,
	"+": 6, "-": 6,
	"*": 7, "/": 7, "MOD": 7,
}

const unaryPrec = 9

// mathFuncs are the one-argument REAL functions SCL spells and calls the
// same. They take REAL or LREAL arguments only: nautilus's integer forms
// (ABS of an INT) differ in width.
var mathFuncs = map[string]bool{
	"ABS": true, "SQRT": true, "LN": true, "LOG": true, "EXP": true,
	"SIN": true, "COS": true, "TAN": true, "ASIN": true, "ACOS": true, "ATAN": true,
}

// namedFuncs are the functions SCL calls with named parameters, with the
// IEC positional order (verify in S2b whether TIA takes them positional).
var namedFuncs = map[string][]string{
	"MIN":   {"IN1", "IN2"},
	"MAX":   {"IN1", "IN2"},
	"LIMIT": {"MN", "IN", "MX"},
	"SEL":   {"G", "IN0", "IN1"},
}

// expr prints an expression; parent is the precedence of the enclosing
// operator, so parentheses appear only where they are needed. In wide
// mode (pw.wide) every narrow integer operand is widened to LINT.
func (pw *progWriter) expr(e st.Expression, parent int) string {
	switch e := e.(type) {
	case *st.NumberLit:
		return number(e)
	case *st.BoolLit:
		if e.Value {
			return "TRUE"
		}
		return "FALSE"
	case *st.StringLit:
		return st.QuoteString(e.Value)
	case *st.TimeLit:
		return pw.timeLit(e.Raw, e.Pos.Line)
	case *st.TypedLit:
		u := strings.ToUpper(e.TypeName)
		if u == "LTIME" {
			pw.diag("siemens/ltime", e.Pos.Line, "LTIME is not in the first subset (nautilus's LTIME is milliseconds, TIA's nanoseconds)")
			return ""
		}
		if !elementary[u] {
			pw.diag("siemens/enum", e.Pos.Line, fmt.Sprintf("%s#…: enumerations are not in the first subset", e.TypeName))
			return ""
		}
		return pw.widen(u, u+"#"+pw.expr(e.Inner, unaryPrec))
	case *st.IdentExpr:
		return pw.widen(pw.typ(e), pw.ident(e.Name, e.Pos.Line))
	case *st.MemberExpr:
		return pw.widen(pw.typ(e), pw.expr(e.Object, unaryPrec+1)+"."+e.Member)
	case *st.IndexExpr:
		pw.diag("siemens/type", e.Pos.Line, "arrays are not in the first subset")
		return ""
	case *st.UnaryExpr:
		return pw.unary(e, parent)
	case *st.BinaryExpr:
		return pw.binary(e, parent)
	case *st.CallExpr:
		wide := pw.wide
		pw.wide = false
		s := pw.call(e, false)
		pw.wide = wide
		return pw.widen(pw.typ(e), s)
	}
	pw.diag("siemens/expression", lineAt(e), fmt.Sprintf("expression %T is not in the first subset", e))
	return ""
}

func (pw *progWriter) timeLit(raw string, line int) string {
	s, why := timeLit(raw)
	if why != "" {
		pw.diag("siemens/time", line, why)
	}
	return s
}

// widen wraps a narrow integer operand in a conversion to LINT, in wide
// mode only.
func (pw *progWriter) widen(t, s string) string {
	if pw.wide && narrow(t) {
		return t + "_TO_LINT(" + s + ")"
	}
	return s
}

// number prints a numeric literal in its own base.
func number(n *st.NumberLit) string {
	if n.Base == 10 || n.Base == 0 {
		return n.Value
	}
	return strconv.Itoa(n.Base) + "#" + n.Value
}

func (pw *progWriter) unary(e *st.UnaryExpr, parent int) string {
	t := pw.typ(e.Operand)
	if strings.EqualFold(e.Op, "NOT") {
		if t != "BOOL" {
			pw.diag("siemens/operator", e.Pos.Line, "NOT on a non-BOOL (a bitwise NOT) is not in the first subset")
			return ""
		}
		s := "NOT " + pw.expr(e.Operand, unaryPrec)
		if parent > unaryPrec {
			return "(" + s + ")"
		}
		return s
	}
	if isIntish(t) && !pw.wide {
		pw.wideRefused(e.Pos.Line)
		return ""
	}
	s := e.Op + pw.expr(e.Operand, unaryPrec)
	if parent > unaryPrec {
		return "(" + s + ")"
	}
	return s
}

func (pw *progWriter) wideRefused(line int) {
	pw.diag("siemens/int-width", line,
		"integer arithmetic here: nautilus computes it at 64 bits and wraps only when the result is stored, which the writer reproduces only in an assignment or a comparison; compute it into a variable first")
}

func (pw *progWriter) binary(e *st.BinaryExpr, parent int) string {
	op := strings.ToUpper(e.Op)
	p, ok := precedence[op]
	if !ok {
		pw.diag("siemens/operator", e.Pos.Line, fmt.Sprintf("operator %s is not in the first subset", e.Op))
		return ""
	}
	lt, rt := pw.typ(e.Left), pw.typ(e.Right)
	if op == "&" {
		op = "AND"
	}
	switch op {
	case "AND", "OR", "XOR":
		if lt != "BOOL" || rt != "BOOL" {
			pw.diag("siemens/operator", e.Pos.Line, op+" on integers or bit strings (bitwise) is not in the first subset")
			return ""
		}
	case "=", "<>", "<", ">", "<=", ">=":
		// A comparison of integer arithmetic compares 64-bit values in
		// nautilus: widen both sides.
		if lt == tWide || rt == tWide {
			wide := pw.wide
			pw.wide = true
			s := pw.expr(e.Left, p) + " " + op + " " + pw.expr(e.Right, p+1)
			pw.wide = wide
			return paren(s, p, parent)
		}
	default: // arithmetic
		if lt == "TIME" || rt == "TIME" || lt == "LTIME" || rt == "LTIME" {
			pw.diag("siemens/time", e.Pos.Line, "TIME arithmetic is not in the first subset (nautilus's TIME is 64-bit milliseconds, TIA's 32-bit)")
			return ""
		}
		if lt == "ULINT" || rt == "ULINT" {
			pw.diag("siemens/int-width", e.Pos.Line, "ULINT arithmetic is not in the first subset")
			return ""
		}
		if num := func(t string) bool { return isIntish(t) || isReal(t) }; !num(lt) || !num(rt) {
			pw.diag("siemens/operator", e.Pos.Line, fmt.Sprintf("%s on %s and %s is not in the first subset", op, orUnknown(lt), orUnknown(rt)))
			return ""
		}
		if op == "/" || op == "MOD" {
			pw.checkDivisor(e)
		}
		if pw.typ(e) == tWide && !pw.wide {
			pw.wideRefused(e.Pos.Line)
			return ""
		}
	}
	return paren(pw.expr(e.Left, p)+" "+op+" "+pw.expr(e.Right, p+1), p, parent)
}

func orUnknown(t string) string {
	if t == "" {
		return "an operand of unknown type"
	}
	return t
}

func paren(s string, p, parent int) string {
	if p < parent {
		return "(" + s + ")"
	}
	return s
}

// checkDivisor refuses a division whose divisor is not a non-zero
// constant: nautilus answers x/0 with 0 and counts it, and the CPU's own
// answer is not pinned down yet, so the writer cannot promise the same
// result.
func (pw *progWriter) checkDivisor(e *st.BinaryExpr) {
	if nonZeroConstant(e.Right) {
		return
	}
	pw.diag("siemens/division", e.Pos.Line,
		fmt.Sprintf("%s by a value that is not a non-zero constant: nautilus gives 0 for a zero divisor and the CPU's result is not yet pinned down; guard it with an IF", e.Op))
}

func nonZeroConstant(e st.Expression) bool {
	switch v := e.(type) {
	case *st.TypedLit:
		return nonZeroConstant(v.Inner)
	case *st.NumberLit:
		s := strings.ReplaceAll(v.Value, "_", "")
		if v.Base == 10 || v.Base == 0 {
			f, err := strconv.ParseFloat(s, 64)
			return err == nil && f != 0
		}
		n, err := strconv.ParseUint(s, v.Base, 64)
		return err == nil && n != 0
	}
	return false
}

// call prints a function call, or an FB instance call when stmt is set.
func (pw *progWriter) call(c *st.CallExpr, stmt bool) string {
	line := c.Pos.Line
	if c.Callee != nil {
		pw.diag("siemens/type", line, "calls through an array of instances are not in the first subset")
		return ""
	}
	name := strings.ToUpper(c.Name)

	if l, ok := pw.locals[strings.ToLower(c.Name)]; ok {
		members, isInst := instMembers[l.typ]
		if !isInst {
			pw.diag("siemens/call", line, fmt.Sprintf("%s is a %s, not a function block instance", l.name, l.typ))
			return ""
		}
		if !stmt {
			pw.diag("siemens/call", line, fmt.Sprintf("%s: an instance call is a statement, not an expression", l.name))
			return ""
		}
		if len(c.Args) > 0 {
			pw.diag("siemens/call", line, fmt.Sprintf("%s: name the inputs (IN := …); positional instance arguments are not written", l.name))
			return ""
		}
		var parts []string
		for _, a := range c.NamedArgs {
			pin := strings.ToUpper(a.Name)
			if pw.typ(a.Value) == tWide {
				pw.wideRefused(a.Pos.Line)
				return ""
			}
			parts = append(parts, fmt.Sprintf("%s := %s", pin, pw.expr(a.Value, 0)))
		}
		for _, o := range c.OutputBindings {
			pin := strings.ToUpper(o.Name)
			if src, dst := members[pin], pw.typ(o.Target); src == "LINT" && narrow(dst) {
				pw.diag("siemens/int-width", line, fmt.Sprintf("%s => %s: the counter's %s is nautilus's 64-bit int; bind it to a LINT, or assign it in a statement so it wraps as nautilus stores it", pin, exprShort(o.Target), pin))
				return ""
			}
			parts = append(parts, fmt.Sprintf("%s => %s", pin, pw.expr(o.Target, 0)))
		}
		return "#" + l.name + "(" + strings.Join(parts, ", ") + ")"
	}

	if stmt {
		pw.diag("siemens/call", line, fmt.Sprintf("%s(…) as a statement: only function block instances are called that way in the first subset", c.Name))
		return ""
	}
	if len(c.NamedArgs) > 0 || len(c.OutputBindings) > 0 {
		pw.diag("siemens/call", line, fmt.Sprintf("%s: named arguments to a function are not written; give them in order", c.Name))
		return ""
	}
	for _, a := range c.Args {
		if pw.typ(a) == tWide {
			pw.wideRefused(line)
			return ""
		}
	}
	args := make([]string, len(c.Args))
	for i, a := range c.Args {
		args[i] = pw.expr(a, 0)
	}
	switch {
	case name == "TRUNC":
		pw.diag("siemens/function", line, "TRUNC is not in the first subset (nautilus's result is LINT, TIA's DINT); use REAL_TO_LINT")
		return ""
	case mathFuncs[name]:
		if len(args) != 1 {
			pw.diag("siemens/call", line, fmt.Sprintf("%s takes one argument", name))
			return ""
		}
		if !isReal(pw.typ(c.Args[0])) {
			pw.diag("siemens/function", line, fmt.Sprintf("%s of a non-REAL argument is not in the first subset", name))
			return ""
		}
		return name + "(" + args[0] + ")"
	case name == "EXPT":
		if len(args) != 2 {
			pw.diag("siemens/call", line, "EXPT takes two arguments")
			return ""
		}
		if !isReal(pw.typ(c.Args[0])) {
			pw.diag("siemens/function", line, "EXPT of a non-REAL base is not in the first subset")
			return ""
		}
		return "(" + pw.expr(c.Args[0], unaryPrec+1) + " ** " + pw.expr(c.Args[1], unaryPrec+1) + ")"
	case namedFuncs[name] != nil:
		params := namedFuncs[name]
		if len(args) != len(params) {
			pw.diag("siemens/call", line, fmt.Sprintf("%s takes %d arguments", name, len(params)))
			return ""
		}
		parts := make([]string, len(args))
		for i := range args {
			parts[i] = params[i] + " := " + args[i]
		}
		return name + "(" + strings.Join(parts, ", ") + ")"
	}
	if from, to, ok := strings.Cut(name, "_TO_"); ok {
		if from == "" {
			pw.diag("siemens/conversion", line, fmt.Sprintf("%s: the overloaded TO_* conversions are not written; name the source type (INT_TO_REAL)", name))
			return ""
		}
		if from == "LTIME" || to == "LTIME" {
			pw.diag("siemens/ltime", line, "LTIME conversions are not in the first subset (nautilus's LTIME is milliseconds, TIA's nanoseconds)")
			return ""
		}
		if elementary[from] && elementary[to] {
			if len(args) != 1 {
				pw.diag("siemens/call", line, fmt.Sprintf("%s takes one argument", name))
				return ""
			}
			// Between integer types nautilus wraps (#800); the CPU's
			// answer for an out-of-range INT_TO_UINT is not pinned down,
			// so the writer wraps explicitly instead.
			if isInt(from) && isInt(to) {
				if from == "ULINT" || to == "ULINT" {
					pw.diag("siemens/int-width", line, name+" is not in the first subset")
					return ""
				}
				if holds(to, from) {
					return name + "(" + args[0] + ")"
				}
				return wrap(toLINT(from, args[0]), to)
			}
			return name + "(" + args[0] + ")"
		}
	}
	if strings.HasPrefix(name, "TO_") {
		pw.diag("siemens/conversion", line, fmt.Sprintf("%s: the overloaded TO_* conversions are not written; name the source type (INT_TO_REAL)", name))
		return ""
	}
	pw.diag("siemens/function", line, fmt.Sprintf("%s is not in the first subset's function table", c.Name))
	return ""
}

// exprShort names an expression in a message.
func exprShort(e st.Expression) string {
	switch e := e.(type) {
	case *st.IdentExpr:
		return e.Name
	case *st.MemberExpr:
		return exprShort(e.Object) + "." + e.Member
	}
	return "the target"
}
