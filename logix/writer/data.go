package writer

import (
	"strconv"
	"strings"

	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/lang/ld"
	"github.com/joyautomation/nautilus/lang/st"
)

// Data instructions: a ladder assignment `{ y := expr }` is the Logix box
// its value's shape names — one operation, one instruction — or a CPT
// carrying the expression when the shape is anything else. A compare
// whose operand is an expression is a CMP carrying the comparison.
//
// Logix spells an expression nearly as IEC does: the operators + - * /
// MOD ** AND OR XOR NOT, functions ABS ACS ASN ATN COS DEG FRD LN LOG RAD
// SIN SQR TAN TOD TRN. The names that differ are mapped; a function with
// no Logix spelling (MIN, MAX, LIMIT, SEL, MUX, the string functions, the
// bit shifts) is a rule: compute it in an ST routine, or write it as the
// arithmetic it is.

// logixFn maps IEC function names to their Logix expression spelling.
var logixFn = map[string]string{
	"ABS": "ABS", "SQRT": "SQR", "LN": "LN", "LOG": "LOG", "SIN": "SIN", "COS": "COS", "TAN": "TAN",
	"ASIN": "ASN", "ACOS": "ACS", "ATAN": "ATN", "TRUNC": "TRN",
}

// logixExpr prints an expression in Logix CPT/CMP spelling, or names
// the part it cannot spell.
func (c *rungCtx) logixExpr(e ld.Expr) (string, string) {
	switch x := e.(type) {
	case *ld.Lit:
		t := x.Text
		switch {
		case strings.EqualFold(t, "TRUE"):
			return "1", ""
		case strings.EqualFold(t, "FALSE"):
			return "0", ""
		case strings.HasPrefix(t, "'"):
			return "", "a string literal"
		case strings.Contains(t, "#"):
			if n, ok := c.lw.enumLiteral(t); ok {
				return strconv.FormatInt(n, 10), "" // Mode#Run
			}
			if ms, ok := parseTime(t); ok {
				return msText(ms), "" // TIME is DINT milliseconds
			}
			i := strings.Index(t, "#")
			switch strings.ToUpper(t[:i]) {
			case "16", "2", "8":
				return t, "" // Logix reads 16#FF, 2#1010, 8#17
			}
			if _, ok := parseInt(t[i+1:]); ok {
				return t[i+1:], "" // INT#5
			}
			return "", "the literal " + t
		}
		return t, ""
	case *ld.Ref:
		ref, ok := c.ref(x.Text)
		if !ok {
			return "", "the operand " + x.Text
		}
		return ref, ""
	case *ld.Call:
		name := strings.ToUpper(x.Name)
		if ref, ok := c.timerMillis(x); ok {
			return ref, ""
		}
		if name == "EXPT" && len(x.Args) == 2 {
			l, bad := c.logixExpr(x.Args[0])
			if bad != "" {
				return "", bad
			}
			r, bad := c.logixExpr(x.Args[1])
			if bad != "" {
				return "", bad
			}
			return "(" + l + " ** " + r + ")", ""
		}
		to, ok := logixFn[name]
		if !ok || len(x.Args) != 1 {
			return "", "the function " + x.Name
		}
		a, bad := c.logixExpr(x.Args[0])
		if bad != "" {
			return "", bad
		}
		return to + "(" + a + ")", ""
	case *ld.Unary:
		a, bad := c.logixExpr(x.X)
		if bad != "" {
			return "", bad
		}
		if x.Op == "NOT" {
			return "NOT " + a, ""
		}
		return "-" + a, ""
	case *ld.Binary:
		l, bad := c.logixExpr(x.L)
		if bad != "" {
			return "", bad
		}
		r, bad := c.logixExpr(x.R)
		if bad != "" {
			return "", bad
		}
		op := x.Op
		switch op {
		case "<", ">", "<=", ">=", "=", "<>":
			// comparisons live in CMP, never in CPT
		}
		return "(" + l + " " + op + " " + r + ")", ""
	}
	return "", "an expression"
}

func msText(n int64) string { return strconv.FormatInt(n, 10) }

// simple reports whether an expression is one operand: a reference or a
// literal, as a Logix instruction takes.
func (c *rungCtx) simple(e ld.Expr) (string, bool) {
	switch x := e.(type) {
	case *ld.Lit, *ld.Ref:
		s, bad := c.logixExpr(x)
		return s, bad == ""
	case *ld.Call:
		return c.timerMillis(x)
	}
	return "", false
}

// timerMillis reads TIME_TO_DINT(t.ET) / TIME_TO_DINT(t.PT) as the timer's
// ACC / PRE, which Logix holds as that DINT of milliseconds (#793).
func (c *rungCtx) timerMillis(x *ld.Call) (string, bool) {
	if !strings.EqualFold(x.Name, "TIME_TO_DINT") || len(x.Args) != 1 {
		return "", false
	}
	r, ok := x.Args[0].(*ld.Ref)
	if !ok {
		return "", false
	}
	up := strings.ToUpper(r.Text)
	if !strings.HasSuffix(up, ".ET") && !strings.HasSuffix(up, ".PT") {
		return "", false
	}
	return c.ref(r.Text)
}

// binaryInstr: the arithmetic with an instruction of its own. MOD, NEG,
// SQR and XPY exist in Logix too, but no export in the corpus carries
// them (mnemonics_test.go), so those shapes go through CPT, which does.
var binaryInstr = map[string]string{"+": "ADD", "-": "SUB", "*": "MUL", "/": "DIV"}

// assign lowers `{ y := expr; … }` to one instruction per assignment.
func (c *rungCtx) assign(e ld.Element) ([]string, bool) {
	as, err := ld.ParseAssignments(e.Text)
	if err != nil {
		return nil, false // the compiler's diagnostic
	}
	var out []string
	runs := copyRuns(as)
	for i := 0; i < len(as); i++ {
		a := as[i]
		// A whole array or structure, or a run the import marked
		// `@logix COP n`/`CPS n`, is a copy (copy.go).
		if a.Pragma("logix MSG.Flags") {
			// The import's copy of a message's status word (msg.go).
			text, ok := c.msgFlags(a)
			if !ok {
				return nil, false
			}
			out = append(out, text)
			continue
		}
		var run *copyRun
		if r, ok := runs[i]; ok {
			run = &r
		}
		if text, next, ok := c.copyText(as, i, run); ok {
			out = append(out, text)
			i = next - 1
			continue
		} else if next > i {
			return nil, false // its diagnostic is given
		}
		target, ok := c.ref(a.Target)
		if !ok {
			return nil, false
		}
		if c.boolTarget(a.Target) {
			c.lw.diag(ruleDataOp, c.r.Line, c.r.Name, "{ %s := %s }: Logix data instructions take no BOOL; drive %s with a coil — ( %s ), ( S %s ) / ( R %s )", a.Target, a.Value.IEC(), a.Target, a.Target, a.Target, a.Target)
			return nil, false
		}
		if hasBoolOp(a.Value) {
			c.lw.diag(ruleDataOp, c.r.Line, c.r.Name, "{ %s := %s }: a comparison or boolean operator in an assignment has no Logix data instruction; drive the BOOL with a coil", a.Target, a.Value.IEC())
			return nil, false
		}
		// A conversion that is the whole right-hand side, into a tag of
		// its own type, is the store a data box makes anyway (#777).
		if inner, ok := c.storeConversion(a); ok {
			a.Value = inner
		}
		if c.realQuotient(a.Target, a.Value.IEC()) {
			c.lw.diag(ruleConversion, c.r.Line, c.r.Name, "{ %s := %s }: %s", a.Target, a.Value.IEC(), realQuotientMsg)
			return nil, false
		}
		switch v := a.Value.(type) {
		case *ld.Lit, *ld.Ref:
			s, ok := c.simple(v)
			if !ok {
				return c.cpt(a, target)
			}
			out = append(out, "MOVE("+s+","+target+")")
			continue
		case *ld.Binary:
			if instr, ok := binaryInstr[v.Op]; ok {
				if l, ok := c.simple(v.L); ok {
					if r, ok := c.simple(v.R); ok {
						out = append(out, instr+"("+l+","+r+","+target+")")
						continue
					}
				}
			}
		case *ld.Call:
			if len(v.Args) == 1 && strings.EqualFold(v.Name, "ABS") {
				if x, ok := c.simple(v.Args[0]); ok {
					out = append(out, "ABS("+x+","+target+")")
					continue
				}
			}
		}
		texts, ok := c.cpt(a, target)
		if !ok {
			return nil, false
		}
		out = append(out, texts...)
	}
	return out, true
}

// storeConversion reads `{ y := X_TO_Y(v) }` as `{ y := v }` when the
// conversion is the one a Logix data box makes as it stores into y, so the
// box (MOV, MUL, CPT, …) computes nautilus's value with no conversion of
// its own (#777):
//
//	REAL → SINT/INT/DINT   the store rounds halves to even, as nautilus
//	                       does (verified on a v38 controller; convert.go)
//	DINT/INT → INT/SINT    the store keeps the low bits, which is
//	                       nautilus's wrap (Logix's documented integer
//	                       conversion; not yet run on a controller)
//	TIME → SINT/INT/DINT   a timer's ET or PT is a DINT of milliseconds
//	                       on Logix (ACC, PRE), narrowed as above
//
// Only from a source of the conversion's own type: an LREAL, or a source
// the writer cannot type, keeps the conversion, which is then refused.
func (c *rungCtx) storeConversion(a ld.Assignment) (ld.Expr, bool) {
	call, ok := a.Value.(*ld.Call)
	if !ok || len(call.Args) != 1 {
		return nil, false
	}
	from, to, trunc, ok := splitConversion(call.Name)
	if !ok || trunc || from == "" || !ir.IsConversion(call.Name) {
		return nil, false
	}
	from, to = strings.ToUpper(from), strings.ToUpper(to)
	w := &stWriter{lw: c.lw}
	t := parseSTExpr(a.Target)
	arg := parseSTExpr(call.Args[0].IEC())
	if t == nil || arg == nil || w.typeOf(t) != to {
		return nil, false
	}
	at := w.typeOf(arg)
	switch {
	case from == "REAL" && at == "REAL" && (to == "SINT" || to == "INT" || to == "DINT"):
		return call.Args[0], true
	case isConvInt(from) && isConvInt(to) && from != "TIME" && to != "TIME" &&
		convWidth[from] > convWidth[to] && (at == from || at == anyInt):
		return call.Args[0], true
	case from == "TIME" && at == "TIME" && (to == "SINT" || to == "INT" || to == "DINT"):
		// A timer's ET or PT is the DINT of milliseconds the box moves.
		return call.Args[0], true
	case to == "TIME" && isConvInt(from) && from != "TIME" && (at == from || at == anyInt):
		// Into a timer's ET or PT: the box moves milliseconds into ACC or
		// PRE (#793).
		return call.Args[0], true
	case (to == "REAL" || to == "LREAL") && isConvInt(from) && from != "TIME" && (at == from || at == anyInt):
		// An integer into a REAL: the box converts as it stores (#782;
		// a REAL is 32 bits on the controller, as ever).
		return call.Args[0], true
	}
	return nil, false
}

// cpt lowers an assignment whose value is a whole expression.
func (c *rungCtx) cpt(a ld.Assignment, target string) ([]string, bool) {
	expr, bad := c.logixExpr(a.Value)
	if bad != "" {
		c.lw.diag(ruleDataOp, c.r.Line, c.r.Name, "{ %s := %s }: %s has no Logix expression form; compute it in an ST routine or write the arithmetic", a.Target, a.Value.IEC(), bad)
		return nil, false
	}
	return []string{"CPT(" + target + "," + stripOuter(expr) + ")"}, true
}

// stripOuter drops the parentheses logixExpr wraps every binary in, at
// the top level only.
func stripOuter(s string) string {
	if strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")") {
		depth := 0
		for i := 0; i < len(s)-1; i++ {
			switch s[i] {
			case '(':
				depth++
			case ')':
				depth--
				if depth == 0 {
					return s
				}
			}
		}
		return s[1 : len(s)-1]
	}
	return s
}

func hasBoolOp(e ld.Expr) bool {
	switch x := e.(type) {
	case *ld.Binary:
		switch x.Op {
		case "<", ">", "<=", ">=", "=", "<>", "AND", "OR", "XOR":
			return true
		}
		return hasBoolOp(x.L) || hasBoolOp(x.R)
	case *ld.Unary:
		return x.Op == "NOT" || hasBoolOp(x.X)
	case *ld.Call:
		for _, a := range x.Args {
			if hasBoolOp(a) {
				return true
			}
		}
	}
	return false
}

var cmpOp = map[string]string{"GT": ">", "GE": ">=", "LT": "<", "LE": "<=", "EQ": "=", "NE": "<>"}

// cmpExpr lowers a compare whose operand is an expression: CMP(l op r).
func (c *rungCtx) cmpExpr(fn string, args []string, e ld.Element) (string, bool) {
	var sides []string
	for _, a := range args {
		x, err := ld.ParseExpr(a)
		if err != nil {
			c.lw.diag(ruleOperand, c.r.Line, c.r.Name, "%s(%s): %q: %v", e.Fn, e.Args, a, err)
			return "", false
		}
		s, bad := c.logixExpr(x)
		if bad != "" {
			c.lw.diag(ruleOperand, c.r.Line, c.r.Name, "%s(%s): %s has no Logix expression form; compute it into a tag first", e.Fn, e.Args, bad)
			return "", false
		}
		sides = append(sides, stripOuter(s))
	}
	if c.realQuotient("", "("+args[0]+") < ("+args[1]+")") {
		c.lw.diag(ruleConversion, c.r.Line, c.r.Name, "%s(%s): %s", e.Fn, e.Args, realQuotientMsg)
		return "", false
	}
	return "CMP(" + sides[0] + " " + cmpOp[fn] + " " + sides[1] + ")", true
}

// realQuotientMsg is the refusal of an integer / on a rung whose quotient
// meets a REAL (#368).
const realQuotientMsg = "integer division (/): Logix divides integers in REAL once the quotient meets a REAL (DIV or CPT into a REAL destination, a REAL operand beside it, a CMP against a REAL), so 7 / 2 gives 3.5 where nautilus gives 3; compute the quotient into a DINT tag of its own first ({ q := a / b }) and use the tag, or divide by a REAL (a / 2.0) if a REAL quotient is meant"

// realQuotient reports an integer / in a rung's expression that Logix
// divides in REAL (#368): a DIV or CPT into a REAL destination (target),
// a CPT with a REAL operand, a CMP against a REAL. Verified on a v38
// controller: DIV(a,b,r) and CPT(r,(a / b) + c) with DINT a = 7, b = 2
// store 3.5 and 6.5, CMP(a / b > x) with x = 3.25 is true; nautilus
// truncates first (3, 6, false). Into an integer, and against an integer,
// Logix truncates as nautilus does. The expression is typed as ST, which
// spells it the same way.
func (c *rungCtx) realQuotient(target, value string) bool {
	e := parseSTExpr(value)
	if e == nil {
		return false
	}
	w := &stWriter{lw: c.lw}
	inReal := w.knownReal(e)
	if target != "" {
		if t := parseSTExpr(target); t != nil && isRealType(w.typeOf(t)) {
			inReal = true
		}
	}
	return w.realDivision(e, inReal)
}

// parseSTExpr reads an IEC expression with the ST parser, or nil.
func parseSTExpr(src string) st.Expression {
	p, err := st.Parse("PROGRAM nautilus_expr\nnautilus_x := " + src + ";\nEND_PROGRAM\n")
	if err != nil || len(p.Statements) != 1 {
		return nil
	}
	a, ok := p.Statements[0].(*st.AssignStmt)
	if !ok {
		return nil
	}
	return a.Value
}

// boolTarget reports an assignment target that is a BOOL: a bit of an
// integer, or a variable declared BOOL. A Logix MOVE has no BOOL operand
// (RxCMP_E_AUDIT_INVALIDOPTYPE at build), so the rule fires at check time.
func (c *rungCtx) boolTarget(target string) bool {
	if _, bit := splitBit(target); bit != "" {
		return true
	}
	base, rest := splitRef(target)
	v, ok := c.lw.vars[strings.ToLower(base)]
	if !ok {
		return false
	}
	typ := strings.ToUpper(strings.TrimSpace(v.Type))
	if i := strings.LastIndex(typ, " OF "); strings.HasPrefix(typ, "ARRAY") && i >= 0 && !strings.Contains(rest, ".") {
		typ = strings.TrimSpace(typ[i+4:])
	}
	return typ == "BOOL" && !strings.Contains(rest, ".")
}
