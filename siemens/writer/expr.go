package writer

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/joyautomation/nautilus/lang/ir"
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
			pw.gap("siemens/ltime", e.Pos.Line, gapTime, "LTIME", "nautilus's LTIME is milliseconds, TIA's nanoseconds")
			return ""
		}
		if !elementary[u] {
			pw.gap("siemens/enum", e.Pos.Line, gapStructured, "enumerations ("+e.TypeName+"#…)", "")
			return ""
		}
		return pw.widen(u, u+"#"+pw.expr(e.Inner, unaryPrec))
	case *st.IdentExpr:
		return pw.widen(pw.typ(e), pw.ident(e.Name, e.Pos.Line))
	case *st.MemberExpr:
		if n, ok := bitIndex(e.Member); ok {
			return pw.bitAccess(e, n)
		}
		if strings.HasPrefix(e.Member, "%") {
			pw.gap("siemens/partial", e.Pos.Line, gapOther, "the partial access ."+e.Member, "bit access (.3, .%X3) is written")
			return ""
		}
		return pw.widen(pw.typ(e), pw.expr(e.Object, unaryPrec+1)+"."+sclName(e.Member))
	case *st.IndexExpr:
		pw.gap("siemens/type", e.Pos.Line, gapStructured, "arrays", "")
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
	pw.gap("siemens/expression", lineAt(e), gapOther, fmt.Sprintf("the expression %T", e), "")
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
		return toLINT(t, s)
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
			return pw.bitwiseNot(e, t)
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
	pw.gap("siemens/int-width", line, gapOther, "integer arithmetic here",
		"nautilus computes it at 64 bits and wraps only when the result is stored, which the writer reproduces in an assignment or a comparison; until then, compute it into a variable first")
}

func (pw *progWriter) binary(e *st.BinaryExpr, parent int) string {
	op := strings.ToUpper(e.Op)
	p, ok := precedence[op]
	if !ok {
		pw.gap("siemens/operator", e.Pos.Line, gapOther, "the operator "+e.Op, "")
		return ""
	}
	lt, rt := pw.typ(e.Left), pw.typ(e.Right)
	if op == "&" {
		op = "AND"
	}
	switch op {
	case "AND", "OR", "XOR":
		if lt != "BOOL" || rt != "BOOL" {
			return pw.bitwise(e, op, parent)
		}
	case "=", "<>", "<", ">", "<=", ">=":
		// A comparison of integer arithmetic compares 64-bit values in
		// nautilus: widen both sides. So does a bit string against an
		// integer (the bit string by its unsigned value), which TIA
		// would not compare as they are.
		mixed := isBits(lt) && isInt(rt) || isInt(lt) && isBits(rt)
		if mixed && (unsigned64(lt) || unsigned64(rt)) {
			pw.gap("siemens/int-width", e.Pos.Line, gapOther, fmt.Sprintf("comparing %s with %s", lt, rt), "")
			return ""
		}
		if lt == tWide || rt == tWide || mixed {
			wide := pw.wide
			pw.wide = true
			s := pw.expr(e.Left, p) + " " + op + " " + pw.expr(e.Right, p+1)
			pw.wide = wide
			return paren(s, p, parent)
		}
	default: // arithmetic
		if lt == "TIME" || rt == "TIME" || lt == "LTIME" || rt == "LTIME" {
			pw.gap("siemens/time", e.Pos.Line, gapTime, "TIME arithmetic", "nautilus's TIME is 64-bit milliseconds, TIA's 32-bit")
			return ""
		}
		if unsigned64(lt) || unsigned64(rt) {
			pw.gap("siemens/int-width", e.Pos.Line, gapOther, "ULINT and LWORD arithmetic", "")
			return ""
		}
		if num := func(t string) bool { return isIntish(t) || isReal(t) }; !num(lt) || !num(rt) {
			pw.gap("siemens/operator", e.Pos.Line, gapOther, fmt.Sprintf("%s on %s and %s", op, orUnknown(lt), orUnknown(rt)), "")
			return ""
		}
		if pw.typ(e) == tWide && !pw.wide {
			pw.wideRefused(e.Pos.Line)
			return ""
		}
		if (op == "/" || op == "MOD") && !nonZeroConstant(e.Right) {
			return pw.guardedDivision(e, op)
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
		pw.gap("siemens/type", line, gapStructured, "calls through an array of instances", "")
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
			pw.diag("siemens/call", line, l.name+": "+gapMessage(gapOther, "positional instance arguments", "name the inputs (IN := …)"))
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
				pw.gap("siemens/int-width", line, gapOther, fmt.Sprintf("%s => %s", pin, exprShort(o.Target)), fmt.Sprintf("the counter's %s is nautilus's 64-bit int; until then, bind it to a LINT, or assign it in a statement so it wraps as nautilus stores it", pin))
				return ""
			}
			parts = append(parts, fmt.Sprintf("%s => %s", pin, pw.expr(o.Target, 0)))
		}
		return "#" + l.name + "(" + strings.Join(parts, ", ") + ")"
	}

	if f, ok := pw.w.lib.funcs[strings.ToLower(c.Name)]; ok {
		pw.gap("siemens/library", line, gapPOUs, "the project's function "+f, "")
		return ""
	}
	if stmt {
		pw.gap("siemens/call", line, gapOther, c.Name+"(…) as a statement", "only function block instances are called that way so far")
		return ""
	}
	if len(c.OutputBindings) > 0 {
		pw.gap("siemens/call", line, gapOther, c.Name+" with an output binding (=>)", "")
		return ""
	}
	argExprs, ok := pw.positional(c)
	if !ok {
		pw.gap("siemens/call", line, gapOther, c.Name+" with named arguments other than its IEC inputs ("+strings.Join(ir.FormalNames(name, len(c.NamedArgs)), ", ")+")", "")
		return ""
	}
	for _, a := range argExprs {
		if pw.typ(a) == tWide {
			pw.wideRefused(line)
			return ""
		}
	}
	args := make([]string, len(argExprs))
	for i, a := range argExprs {
		args[i] = pw.expr(a, 0)
	}
	switch {
	case name == "TRUNC":
		// nautilus truncates toward zero into its 64-bit int.
		if len(args) != 1 || !isReal(pw.typ(argExprs[0])) {
			pw.gap("siemens/function", line, gapOther, "TRUNC of anything but one REAL or LREAL", "")
			return ""
		}
		if !realConstFits(argExprs[0], "LINT", math.Trunc) {
			pw.divergesAs(ConversionRangeRule, line, "TRUNC", "the value exceeds the 64-bit range (or is NaN)", "TRUNC_LINT sets ENO FALSE")
		}
		return "TRUNC_LINT(" + args[0] + ")"
	case strings.HasPrefix(name, "TRUNC_") || strings.HasPrefix(name, "REAL_TRUNC_") || strings.HasPrefix(name, "LREAL_TRUNC_"):
		_, to, _ := strings.Cut(name, "TRUNC_")
		if len(args) != 1 || !isReal(pw.typ(argExprs[0])) || !isInt(to) || to == "ULINT" {
			pw.gap("siemens/function", line, gapOther, name+" of this argument", "")
			return ""
		}
		if !realConstFits(argExprs[0], to, math.Trunc) {
			pw.divergesAs(ConversionRangeRule, line, name, "the value exceeds "+to+"'s range (or is NaN)", "TRUNC_"+to+" sets ENO FALSE")
		}
		return "TRUNC_" + to + "(" + args[0] + ")"
	case mathFuncs[name]:
		if len(args) != 1 {
			pw.diag("siemens/call", line, fmt.Sprintf("%s takes one argument", name))
			return ""
		}
		if !isReal(pw.typ(argExprs[0])) {
			pw.gap("siemens/function", line, gapOther, name+" of a non-REAL argument", "")
			return ""
		}
		return name + "(" + args[0] + ")"
	case name == "EXPT":
		if len(args) != 2 {
			pw.diag("siemens/call", line, "EXPT takes two arguments")
			return ""
		}
		if !isReal(pw.typ(argExprs[0])) {
			pw.gap("siemens/function", line, gapOther, "EXPT of a non-REAL base", "")
			return ""
		}
		return "(" + pw.expr(argExprs[0], unaryPrec+1) + " ** " + pw.expr(argExprs[1], unaryPrec+1) + ")"
	case (name == "MIN" || name == "MAX" || name == "LIMIT") && slices.ContainsFunc(argExprs, func(a st.Expression) bool { return isBits(pw.typ(a)) }):
		// nautilus compares bit strings as unsigned numbers; TIA's
		// MIN, MAX and LIMIT take numbers only.
		pw.gap("siemens/function", line, gapOther, name+" of bit strings", "")
		return ""
	case name == "MIN" || name == "MAX":
		// TIA's MIN and MAX take IN1 … IN32.
		if len(args) < 2 || len(args) > 32 {
			pw.gap("siemens/call", line, gapOther, fmt.Sprintf("%s of %d arguments", name, len(args)), "TIA's takes 2 to 32")
			return ""
		}
		return name + "(" + formal(ir.FormalNames(name, len(args)), args) + ")"
	case name == "MUX":
		// K, then IN0 … IN31 in TIA. A K out of range faults the scan in
		// nautilus; the CPU's MUX sets ENO and carries on (a documented
		// divergence, like an out-of-range REAL conversion).
		if len(args) < 3 || len(args) > 33 {
			pw.gap("siemens/call", line, gapOther, fmt.Sprintf("MUX of %d inputs", len(args)-1), "TIA's takes 2 to 32")
			return ""
		}
		if k := pw.typ(argExprs[0]); !intOrBits(k) && k != tAnyInt {
			pw.gap("siemens/call", line, gapOther, "MUX with a selector of type "+orUnknown(k), "")
			return ""
		}
		if k, ok := litInt(argExprs[0]); !ok || k < 0 || k >= int64(len(args)-1) {
			pw.diverges(line, "MUX", fmt.Sprintf("the selector K is outside 0..%d", len(args)-2), "MUX sets ENO FALSE")
		}
		return "MUX(" + formal(ir.FormalNames(name, len(args)), args) + ")"
	case name == "SHL" || name == "SHR" || name == "ROL" || name == "ROR":
		return pw.shift(name, argExprs, args, line)
	case namedFuncs[name] != nil:
		params := namedFuncs[name]
		if len(args) != len(params) {
			pw.diag("siemens/call", line, fmt.Sprintf("%s takes %d arguments", name, len(params)))
			return ""
		}
		return name + "(" + formal(params, args) + ")"
	}
	if to, ok := strings.CutPrefix(name, "TO_"); ok {
		// The overloaded TO_<type> is the explicit conversion from its
		// argument's type (lang/ir conversions.go).
		if len(args) != 1 {
			pw.diag("siemens/call", line, fmt.Sprintf("%s takes one argument", name))
			return ""
		}
		from := pw.typ(argExprs[0])
		if from == to {
			return args[0]
		}
		if !elementary[from] {
			if from == "STRING" || to == "STRING" {
				pw.gap("siemens/function", line, gapString, name, "")
				return ""
			}
			pw.gap("siemens/conversion", line, gapOther, name+" of a value of no declared type", "name the source type (…_TO_"+to+")")
			return ""
		}
		return pw.conversion(from+"_TO_"+to, from, to, argExprs[0], args, line)
	}
	if from, to, ok := strings.Cut(name, "_TO_"); ok && from != "" {
		return pw.conversion(name, from, to, first(argExprs), args, line)
	}
	pw.gap("siemens/function", line, functionGap(name), name, "")
	return ""
}

// conversion writes the explicit conversion name, from one elementary
// type to another.
func (pw *progWriter) conversion(name, from, to string, arg st.Expression, args []string, line int) string {
	if from == "LTIME" || to == "LTIME" {
		pw.gap("siemens/ltime", line, gapTime, "LTIME conversions", "nautilus's LTIME is milliseconds, TIA's nanoseconds")
		return ""
	}
	if !elementary[from] || !elementary[to] {
		pw.gap("siemens/function", line, functionGap(name), name, "")
		return ""
	}
	if len(args) != 1 {
		pw.diag("siemens/call", line, fmt.Sprintf("%s takes one argument", name))
		return ""
	}
	// Between integer types nautilus wraps (#800); the CPU's answer for
	// an out-of-range INT_TO_UINT is not pinned down, so the writer wraps
	// explicitly instead.
	if isInt(from) && isInt(to) {
		if from == "ULINT" || to == "ULINT" {
			pw.gap("siemens/int-width", line, gapOther, name, "")
			return ""
		}
		if holds(to, from) {
			return name + "(" + args[0] + ")"
		}
		return wrap(toLINT(from, args[0]), to)
	}
	// Both round half to even (lang/ir conversions.go; TIA's CONVERT and
	// ROUND by its documentation): only the out-of-range case differs.
	if isReal(from) && isInt(to) && !realConstFits(arg, to, math.RoundToEven) {
		pw.divergesAs(ConversionRangeRule, line, name, "the value exceeds "+to+"'s range (or is NaN)", name+" sets ENO FALSE")
	}
	return name + "(" + args[0] + ")"
}

// first is the first argument, or nil.
func first(args []st.Expression) st.Expression {
	if len(args) == 0 {
		return nil
	}
	return args[0]
}

// realConstFits reports a REAL constant that round takes into integer
// type t: a conversion of it cannot fault, so it gets no warning.
func realConstFits(e st.Expression, t string, round func(float64) float64) bool {
	neg := false
	if u, ok := e.(*st.UnaryExpr); ok && u.Op == "-" {
		neg, e = true, u.Operand
	}
	n, ok := e.(*st.NumberLit)
	if !ok || n.Base != 10 && n.Base != 0 {
		return false
	}
	f, err := strconv.ParseFloat(strings.ReplaceAll(n.Value, "_", ""), 64)
	if err != nil {
		return false
	}
	if neg {
		f = -f
	}
	r := round(f)
	w := intWidth[t]
	if w.bits == 64 {
		return r >= -9.2e18 && r <= 9.2e18 && (w.signed || r >= 0)
	}
	return r >= -9e15 && r <= 9e15 && inRange(int64(r), t)
}

// functionGap is the gap a builtin the writer has no translation for
// belongs to.
func functionGap(name string) gap {
	if strings.Contains(name, "STRING") {
		return gapString
	}
	switch name {
	case "LEN", "LEFT", "RIGHT", "MID", "CONCAT", "INSERT", "DELETE", "REPLACE", "FIND":
		return gapString
	}
	if strings.Contains(name, "TIME") || strings.Contains(name, "DATE") || strings.HasPrefix(name, "DT_") || strings.HasSuffix(name, "_DT") || strings.HasPrefix(name, "TOD") || strings.HasSuffix(name, "_TOD") {
		return gapTime
	}
	return gapOther
}

// formal names each argument with its parameter: SCL calls TIA's
// instructions with formal parameters.
func formal(params, args []string) string {
	parts := make([]string, len(args))
	for i := range args {
		parts[i] = params[i] + " := " + args[i]
	}
	return strings.Join(parts, ", ")
}

// positional is a function call's arguments in their IEC order: as given,
// or a formal call's named arguments put in the order ir.FormalNames
// gives them (the lowering's own table). ok is false for a call mixing
// both, or naming an input the function does not have.
func (pw *progWriter) positional(c *st.CallExpr) ([]st.Expression, bool) {
	if len(c.NamedArgs) == 0 {
		return c.Args, true
	}
	if len(c.Args) > 0 {
		return nil, false
	}
	names := ir.FormalNames(strings.ToUpper(c.Name), len(c.NamedArgs))
	out := make([]st.Expression, len(names))
	for _, a := range c.NamedArgs {
		i := slices.IndexFunc(names, func(n string) bool { return strings.EqualFold(n, a.Name) })
		if i < 0 || out[i] != nil {
			return nil, false
		}
		out[i] = a.Value
	}
	return out, true
}

// shift writes SHL, SHR, ROL or ROR. nautilus shifts and rotates over the
// first operand's declared width, and a signed result takes its sign from
// the top bit of that width (lang/ir builtins_std.go): exactly the
// instruction on the bit string of that width, converted back.
func (pw *progWriter) shift(name string, argExprs []st.Expression, args []string, line int) string {
	if len(args) != 2 {
		pw.diag("siemens/call", line, name+" takes two arguments")
		return ""
	}
	t := pw.typ(argExprs[0])
	if !intOrBits(t) {
		pw.gap("siemens/function", line, gapOther, name+" of a value of no declared width", "")
		return ""
	}
	if t == "ULINT" {
		pw.gap("siemens/int-width", line, gapOther, name+" of a ULINT", "")
		return ""
	}
	// TIA's N is an unsigned count. A negative N faults the scan in
	// nautilus; converted, it is a count beyond the width on the CPU (a
	// documented divergence).
	n := args[1]
	switch nt := pw.typ(argExprs[1]); {
	case isBits(nt):
		n = nt + "_TO_" + unsignedOf(nt) + "(" + n + ")"
	case isInt(nt) && intWidth[nt].signed:
		n = nt + "_TO_" + unsignedOf(bitsFor(nt)) + "(" + n + ")"
		pw.diverges(line, name, "the count N is negative", "it becomes an unsigned count beyond the width")
	case isInt(nt), nt == tAnyInt:
	default:
		pw.gap("siemens/function", line, gapOther, name+" by a count of type "+orUnknown(nt), "")
		return ""
	}
	bits := bitsFor(t)
	in := args[0]
	if t != bits {
		in = t + "_TO_" + bits + "(" + in + ")"
	}
	s := name + "(IN := " + in + ", N := " + n + ")"
	if t != bits {
		s = bits + "_TO_" + t + "(" + s + ")"
	}
	return s
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
