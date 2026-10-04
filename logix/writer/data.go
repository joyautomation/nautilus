package writer

import (
	"strconv"
	"strings"

	"github.com/joyautomation/nautilus/lang/ld"
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
	}
	return "", false
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
	for _, a := range as {
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
			c.lw.diag(ruleOperand, c.r.Line, c.r.Name, "%s(%s): %s has no Logix expression form", e.Fn, e.Args, bad)
			return "", false
		}
		sides = append(sides, stripOuter(s))
	}
	return "CMP(" + sides[0] + " " + cmpOp[fn] + " " + sides[1] + ")", true
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
