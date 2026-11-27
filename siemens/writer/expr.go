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

// required is the input TIA's instruction wants at every call (PT of a
// timer, PV of a counter), by the IEC block.
var required = map[string]string{"TON": "PT", "TOF": "PT", "TP": "PT"}

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
		if f, long := longReal(e, "LREAL"); long && pw.lreal {
			return lrealBits(f)
		}
		return number(e)
	case *st.BoolLit:
		if e.Value {
			return "TRUE"
		}
		return "FALSE"
	case *st.StringLit:
		return pw.stringLit(e.Value, e.Pos.Line)
	case *st.TimeLit:
		return pw.timeLit(e.Raw, e.Pos.Line)
	case *st.TypedLit:
		u := strings.ToUpper(e.TypeName)
		if u == "LTIME" {
			pw.gap("siemens/ltime", e.Pos.Line, gapTime, "LTIME", "nautilus's LTIME is milliseconds, TIA's nanoseconds")
			return ""
		}
		if !elementary[u] {
			if id, ok := e.Inner.(*st.IdentExpr); ok && pw.w.enumOf(e.TypeName) != nil {
				return pw.enumMember(e.TypeName, id.Name, e.Pos.Line)
			}
			pw.gap("siemens/enum", e.Pos.Line, gapOther, "the typed literal "+e.TypeName+"#…", "")
			return ""
		}
		switch u {
		case "LREAL": // the literal is an LREAL whatever its context (#1429)
			if f, long := longReal(e, "LREAL"); long {
				return lrealBits(f)
			}
		case "REAL":
			return pw.inLREAL(false, func() string { return u + "#" + pw.expr(e.Inner, unaryPrec) })
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
		member := e.Member
		ot := pw.typ(e.Object)
		if timers[ot] && timerOut(member) {
			// The latch, never the timer itself (timers.go).
			return pw.widen(pw.typ(e), pw.lastPath(e))
		}
		if sd := pw.w.structOf(ot); sd != nil {
			if f, ok := sd.fieldOf(member); ok {
				member = f.Name // as the TYPE spells it
			}
		} else if _, counter := counterInputs[ot]; counter {
			member = counterMember(ot, member)
		} else if p := pw.w.userFB(ot); p != nil {
			pn, ok := p.pins()[strings.ToUpper(member)]
			if !ok || pn.kind == "VAR_TEMP" {
				// A temp lives for one call only (TIA keeps none in the
				// instance), and a constant is the block's own.
				pw.gap("siemens/member", e.Pos.Line, gapOther, "reading "+exprShort(e)+", a temp or constant of "+p.what()+", from outside it", "")
				return ""
			}
			if pn.kind == "VAR_IN_OUT" {
				// nautilus keeps the in-out's last value in the
				// instance; TIA keeps a reference to the caller's
				// variable there, which the caller names directly.
				pw.gap("siemens/member", e.Pos.Line, gapOther, exprShort(e)+", an in-out of "+p.what()+", from outside it", "name the variable bound to it instead")
				return ""
			}
			member = pn.name // as the block declares it
		}
		return pw.widen(pw.typ(e), pw.expr(e.Object, unaryPrec+1)+"."+sclName(member))
	case *st.IndexExpr:
		return pw.widen(pw.typ(e), pw.index(e))
	case *st.UnaryExpr:
		return pw.unary(e, parent)
	case *st.BinaryExpr:
		return pw.binary(e, parent)
	case *st.CallExpr:
		// A call's arguments are in their own width, except where SEL,
		// MIN, MAX or LIMIT pass integer arithmetic through to a 64-bit
		// caller: then they are computed at 64 bits too, and the caller
		// wraps the result.
		wide := pw.wide
		through := wide && wideGeneric[strings.ToUpper(e.Name)] && pw.typ(e) == tWide
		pw.wide = through
		s := pw.call(e, false)
		pw.wide = wide
		if through {
			return s
		}
		return pw.widen(pw.typ(e), s)
	}
	pw.gap("siemens/expression", lineAt(e), gapOther, fmt.Sprintf("the expression %T", e), "")
	return ""
}

// PairStateRule tags the warning that a program reads a nautilus pair's
// state (#710 (b)), PairStateWarning its text.
const (
	PairStateRule    = "siemens/pair-state"
	PairStateWarning = "TAKEOVER_SCAN/HOT_TAKEOVER/PEER_READY/TAKEOVERS/TAKEOVER_HOLD read a nautilus pair; on this target they are constant (FALSE/0)"
)

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
	if ctx := pw.realContext(pw.typ(e.Left), pw.typ(e.Right)); ctx != pw.lreal {
		return pw.inLREAL(ctx, func() string { return pw.binary(e, parent) })
	}
	op := strings.ToUpper(e.Op)
	p, ok := precedence[op]
	if !ok {
		pw.gap("siemens/operator", e.Pos.Line, gapOther, "the operator "+e.Op, "")
		return ""
	}
	if h := pw.sideHint(e.Left, e.Right); h != "" {
		defer pw.hint(h)()
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
		pw.gap("siemens/type", line, gapOther, "calls through an array of instances", "")
		return ""
	}
	name := strings.ToUpper(c.Name)
	// The nautilus system functions read a nautilus pair's state (#710
	// (b)). A CPU this writes for runs no nautilus pair: never a takeover,
	// no peer, no hold. (An S7-1500R/H's own redundancy status is in its
	// system diagnostics, which nautilus does not map.)
	if ir.IsSystemFunction(name) && len(c.Args) == 0 && len(c.NamedArgs) == 0 {
		// Logic gated on one is dead code on the CPU: say so, once per
		// project (naut-lead).
		if !pw.w.pairWarned {
			pw.w.pairWarned = true
			pw.w.warnings = append(pw.w.warnings, Diag{Rule: PairStateRule, Path: pw.path, Line: line, Message: PairStateWarning})
		}
		if name == "TAKEOVERS" {
			return "0"
		}
		return "FALSE"
	}

	if l, ok := pw.locals[strings.ToLower(c.Name)]; ok {
		if pw.w.userFB(l.typ) != nil {
			pw.diag("siemens/call", line, fmt.Sprintf("%s: an instance call is a statement, not an expression", l.name))
			return ""
		}
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
		if ins, counter := counterInputs[l.typ]; counter {
			return pw.counterCall(c, l, ins)
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
			if timers[l.typ] {
				// Assignments from the latch after the call (CallStmt).
				break
			}
			if pw.refusedTimerWrite(o.Target, line) {
				return ""
			}
			pin := strings.ToUpper(o.Name)
			if src, dst := members[pin], pw.typ(o.Target); src == "LINT" && narrow(dst) {
				pw.gap("siemens/int-width", line, gapOther, fmt.Sprintf("%s => %s", pin, exprShort(o.Target)), fmt.Sprintf("the counter's %s is nautilus's 64-bit int; until then, bind it to a LINT, or assign it in a statement so it wraps as nautilus stores it", pin))
				return ""
			}
			parts = append(parts, fmt.Sprintf("%s => %s", pin, pw.expr(o.Target, 0)))
		}
		if pin, ok := required[l.typ]; ok && !slices.ContainsFunc(c.NamedArgs, func(a st.NamedArg) bool { return strings.EqualFold(a.Name, pin) }) {
			// TIA wants PT at every call of a timer and PV at every call
			// of a counter (measured on V21: "Parameter 'PT' has to be
			// used"); nautilus keeps the value the instance holds, which
			// is what this passes.
			parts = append(parts, pin+" := #"+sclName(l.name)+"."+pin)
		}
		if timers[l.typ] {
			// The call itself fills the latch: Q and ET as this call
			// computed them, read nowhere else (timers.go).
			last := "#" + sclName(pw.last[strings.ToLower(l.name)])
			parts = append(parts, "Q => "+last+".Q", "ET => "+last+".ET")
		}
		return "#" + sclName(l.name) + "(" + strings.Join(parts, ", ") + ")"
	}

	if p := pw.w.userFC(c.Name); p != nil {
		return pw.fcCall(c, p, stmt)
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
	if _, ok := stringFuncs[name]; ok {
		// Before the wide check: a length or position may be integer
		// arithmetic, which strArg computes at 64 bits.
		return pw.stringCall(c, name, argExprs)
	}
	// A conversion of integer arithmetic converts its 64-bit value, as
	// nautilus does (lang/ir conversions.go: from LINT, an int-to-int
	// conversion wraps).
	if len(argExprs) == 1 && pw.typ(argExprs[0]) == tWide {
		if out, ok := pw.wideConversion(name, argExprs[0], line); ok {
			return out
		}
	}
	// SEL, MIN, MAX and LIMIT return their 64-bit value unwrapped in
	// nautilus; where the caller computes at 64 bits (a store, a
	// comparison), the arguments do too, and the caller wraps.
	wideOK := pw.wide && wideGeneric[name] && pw.intValueArgs(name, argExprs)
	for _, a := range argExprs {
		if pw.typ(a) == tWide && !wideOK {
			pw.wideRefused(line)
			return ""
		}
	}
	// A call that returns an LREAL takes LREAL arguments (a literal there is
	// an LREAL); one that returns a REAL takes REALs.
	argCtx := pw.lreal
	switch pw.typ(c) {
	case "LREAL":
		argCtx = true
	case "REAL":
		argCtx = false
	}
	if from, _, ok := strings.Cut(name, "_TO_"); ok && from != "" {
		argCtx = from == "LREAL" // a conversion's argument is of its source type
	}
	args := make([]string, len(argExprs))
	for i, a := range argExprs {
		args[i] = pw.inLREAL(argCtx, func() string { return pw.expr(a, 0) })
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
	case name == "ABS" && len(args) == 1 && pw.wide && pw.typ(c) == tWide:
		// Integer ABS at 64 bits; the caller wraps. A constant is folded
		// (TIA would type a lone literal by its value, -128 as SINT, and
		// overflow). TIA's ABS of a LINT: see the B2 bench row for
		// LINT's least value.
		if n, ok := pw.fold(argExprs[0]); ok {
			if n == -1<<63 {
				return "(LINT#-9223372036854775807 - 1)" // Go: -min is min
			}
			if n < 0 {
				n = -n
			}
			return "LINT#" + strconv.FormatInt(n, 10)
		}
		return "ABS(" + args[0] + ")"
	case name == "ATAN2":
		return pw.atan2(argExprs, args, line)
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
		if en := pw.w.enumOf(to); en != nil {
			return pw.toEnum(en, argExprs[0], args[0], name, line)
		}
		if en := pw.w.enumOf(pw.rawTyp(argExprs[0])); en != nil && !isInt(to) {
			// nautilus converts an enumeration as its integer; TO_STRING
			// and the rest are not checked against the runtime yet.
			pw.gap("siemens/conversion", line, gapOther, name+" of the enumeration "+en.Name, "TO_<integer> is written")
			return ""
		}
		from := pw.typ(argExprs[0])
		if from == to {
			return args[0]
		}
		if (from == "STRING" || to == "STRING") && from != "" {
			return pw.conversion(from+"_TO_"+to, from, to, argExprs[0], args, line)
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

// wideGeneric are the functions whose integer arithmetic argument a
// 64-bit caller may pass through.
var wideGeneric = map[string]bool{"SEL": true, "MIN": true, "MAX": true, "LIMIT": true, "ABS": true}

// intValueArgs reports whether every value argument of the call (SEL's
// selector aside) is an integer or integer arithmetic.
func (pw *progWriter) intValueArgs(name string, args []st.Expression) bool {
	if name == "SEL" && len(args) == 3 {
		args = args[1:]
	}
	for _, a := range args {
		if t := pw.typ(a); t != tWide && (!isInt(t) || t == "ULINT") && t != tAnyInt {
			return false // ULINT: refused at 64 bits everywhere else too
		}
	}
	return len(args) > 0
}

// wideConversion writes a conversion of integer arithmetic: nautilus
// converts its 64-bit value (a LINT). To an integer or bit string it wraps
// exactly as a store does; A_TO_B wraps to A first, then converts A to B;
// to anything else it is LINT_TO_<type>. ok is false for a name that is
// not a conversion the writer knows.
func (pw *progWriter) wideConversion(name string, arg st.Expression, line int) (string, bool) {
	if to, ok := strings.CutPrefix(name, "TO_"); ok && pw.w.enumOf(to) == nil {
		if isInt(to) || isBits(to) {
			return pw.store(arg, to, line), true
		}
		if !elementary[to] {
			return "", false
		}
		if to == "BOOL" { // nautilus: the 64-bit value is not zero
			wide := pw.wide
			pw.wide = true
			v := pw.expr(arg, 0)
			pw.wide = wide
			return "(" + v + ") <> 0", true
		}
		if to == "TIME" || to == "LTIME" || strings.HasPrefix(to, "DATE") || to == "TOD" || to == "DT" || strings.HasPrefix(to, "TIME_OF") {
			pw.gap("siemens/time", line, gapTime, name+" of integer arithmetic", "nautilus's TIME is 64-bit milliseconds, TIA's 32-bit")
			return "", true
		}
		wide := pw.wide
		pw.wide = true
		v := pw.expr(arg, 0)
		pw.wide = wide
		return pw.conversion("LINT_TO_"+to, "LINT", to, arg, []string{v}, line), true
	}
	if from, to, ok := strings.Cut(name, "_TO_"); ok && from != "" && (isInt(from) || isBits(from)) {
		return pw.conversion(name, from, to, arg, []string{pw.store(arg, from, line)}, line), true
	}
	return "", false
}

// conversion writes the explicit conversion name, from one elementary
// type to another.
func (pw *progWriter) conversion(name, from, to string, arg st.Expression, args []string, line int) string {
	if from == "LTIME" || to == "LTIME" {
		pw.gap("siemens/ltime", line, gapTime, "LTIME conversions", "nautilus's LTIME is milliseconds, TIA's nanoseconds")
		return ""
	}
	if (from == "STRING" || to == "STRING") && (elementary[from] || elementary[to] || from == to) {
		if len(args) != 1 {
			pw.diag("siemens/call", line, fmt.Sprintf("%s takes one argument", name))
			return ""
		}
		return pw.stringConversion(name, from, to, args, line)
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
	// TIA V21 has no REAL or LREAL conversion to or from TIME (compiled
	// 2026-11-25: "Tag REAL_TO_TIME not defined"; every integer and bit
	// string one compiles). Its TIME is a DINT of milliseconds, so the
	// conversion goes through DINT. REAL_TO_DINT rounds half to even, as
	// nautilus's REAL_TO_TIME does (TIA's documentation; the rounding is
	// not measured on the CPU). The REAL side is TIA's 32-bit REAL, as for
	// every REAL the writer declares. Beyond 32-bit milliseconds nautilus
	// still has the right TIME and the CPU cannot hold it: a constant is
	// refused like a time literal that does not fit, anything else warned.
	if isReal(from) && to == "TIME" {
		if isNumberConst(arg) {
			if !realConstFits(arg, "DINT", math.RoundToEven) {
				pw.gap("siemens/time", line, gapTime, name+" of a value beyond the CPU's TIME", "TIA's TIME holds 32-bit milliseconds, about 24.8 days either way")
				return ""
			}
		} else {
			pw.w.warnings = append(pw.w.warnings, Diag{Rule: ConversionRangeRule, Path: pw.path, Line: line,
				Message: fmt.Sprintf("%s can differ if the value is beyond the CPU's TIME (32-bit milliseconds, about 24.8 days either way): nautilus stores up to 64-bit milliseconds (and faults the scan on NaN or beyond); on Siemens %s_TO_DINT sets ENO FALSE, the scan carries on, and what it stores is not measured", name, from)})
		}
		return "DINT_TO_TIME(" + from + "_TO_DINT(" + args[0] + "))"
	}
	if from == "TIME" && isReal(to) {
		return "DINT_TO_" + to + "(TIME_TO_DINT(" + args[0] + "))"
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

// isNumberConst reports a decimal number literal, signed or not: what
// realConstFits can judge.
func isNumberConst(e st.Expression) bool {
	if u, ok := e.(*st.UnaryExpr); ok && u.Op == "-" {
		e = u.Operand
	}
	n, ok := e.(*st.NumberLit)
	return ok && (n.Base == 10 || n.Base == 0)
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

// counterInputs are each IEC counter's inputs, in the order its
// IEC_LCOUNTER method takes them (#1338, #1371).
var counterInputs = map[string][]string{
	"CTU":  {"CU", "R", "PV"},
	"CTD":  {"CD", "LD", "PV"},
	"CTUD": {"CU", "CD", "R", "LD", "PV"},
}

// counterMember is the IEC_LCOUNTER member that holds a counter's pin:
// the counter object has one output per direction, QU and QD, so CTU's Q
// is QU and CTD's Q is QD; every other pin keeps its name.
func counterMember(typ, pin string) string {
	switch p := strings.ToUpper(pin); {
	case p == "Q" && typ == "CTU":
		return "QU"
	case p == "Q" && typ == "CTD":
		return "QD"
	default:
		return p
	}
}

// counterCall prints a counter's call as its IEC_LCOUNTER method call,
// #c.CTU(CU := …, R := …, PV := …): every input, in the method's order,
// an input the call leaves out as the value the counter holds (nautilus
// keeps an input's last value, and TIA's method takes each one). The
// output bindings are assignments after the call (CallStmt).
func (pw *progWriter) counterCall(c *st.CallExpr, l local, ins []string) string {
	given := map[string]string{}
	for _, a := range c.NamedArgs {
		pin := strings.ToUpper(a.Name)
		if !slices.Contains(ins, pin) {
			// nautilus refuses an unknown pin first.
			pw.diag("siemens/call", a.Pos.Line, fmt.Sprintf("%s has no input %s", l.typ, a.Name))
			return ""
		}
		if pw.typ(a.Value) == tWide {
			pw.wideRefused(a.Pos.Line)
			return ""
		}
		given[pin] = pw.expr(a.Value, 0)
	}
	inst := "#" + sclName(l.name)
	parts := make([]string, 0, len(ins))
	for _, pin := range ins {
		v, ok := given[pin]
		if !ok {
			v = inst + "." + pin
		}
		parts = append(parts, pin+" := "+v)
	}
	return inst + "." + l.typ + "(" + strings.Join(parts, ", ") + ")"
}
