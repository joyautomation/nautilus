package writer

import (
	"fmt"
	"strings"

	"github.com/joyautomation/nautilus/lang/st"
)

// precedence of the binary operators, IEC 61131-3 Table 71 (SCL agrees).
var precedence = map[string]int{
	"OR": 1, "XOR": 2, "AND": 3, "&": 3,
	"=": 4, "<>": 4,
	"<": 5, ">": 5, "<=": 5, ">=": 5,
	"+": 6, "-": 6,
	"*": 7, "/": 7, "MOD": 7,
	"**": 8,
}

const unaryPrec = 9

// mathFuncs are the one-argument functions SCL spells and calls the same.
var mathFuncs = map[string]bool{
	"ABS": true, "SQRT": true, "LN": true, "LOG": true, "EXP": true,
	"SIN": true, "COS": true, "TAN": true, "ASIN": true, "ACOS": true, "ATAN": true,
	"TRUNC": true,
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
// operator, so parentheses appear only where they are needed.
func (pw *progWriter) expr(e st.Expression, parent int) string {
	switch e := e.(type) {
	case *st.NumberLit:
		return e.Value
	case *st.BoolLit:
		if e.Value {
			return "TRUE"
		}
		return "FALSE"
	case *st.StringLit:
		return "'" + strings.NewReplacer("$", "$$", "'", "$'").Replace(e.Value) + "'"
	case *st.TimeLit:
		return "T#" + e.Raw
	case *st.TypedLit:
		u := strings.ToUpper(e.TypeName)
		if !elementary[u] {
			pw.diag("siemens/enum", e.Pos.Line, fmt.Sprintf("%s#…: enumerations are not in the first subset", e.TypeName))
			return ""
		}
		return u + "#" + pw.expr(e.Inner, unaryPrec)
	case *st.IdentExpr:
		return pw.ident(e.Name, e.Pos.Line)
	case *st.MemberExpr:
		return pw.expr(e.Object, unaryPrec+1) + "." + e.Member
	case *st.IndexExpr:
		pw.diag("siemens/type", e.Pos.Line, "arrays are not in the first subset")
		return ""
	case *st.UnaryExpr:
		op := e.Op
		if op == "NOT" {
			op = "NOT "
		}
		s := op + pw.expr(e.Operand, unaryPrec)
		if parent > unaryPrec {
			return "(" + s + ")"
		}
		return s
	case *st.BinaryExpr:
		op := strings.ToUpper(e.Op)
		p, ok := precedence[op]
		if !ok {
			pw.diag("siemens/operator", e.Pos.Line, fmt.Sprintf("operator %s is not in the first subset", e.Op))
			return ""
		}
		if op == "/" || op == "MOD" {
			pw.checkDivisor(e)
		}
		if op == "&" {
			op = "AND"
		}
		s := pw.expr(e.Left, p) + " " + op + " " + pw.expr(e.Right, p+1)
		if p < parent {
			return "(" + s + ")"
		}
		return s
	case *st.CallExpr:
		return pw.call(e, false)
	}
	pw.diag("siemens/expression", lineAt(e), fmt.Sprintf("expression %T is not in the first subset", e))
	return ""
}

// checkDivisor refuses a division whose divisor is not a non-zero
// constant: nautilus answers x/0 with 0 and counts it, and the CPU's own
// answer is not pinned down yet, so the writer cannot promise the same
// result.
func (pw *progWriter) checkDivisor(e *st.BinaryExpr) {
	if n, ok := e.Right.(*st.NumberLit); ok && strings.Trim(n.Value, "0.") != "" {
		return
	}
	if t, ok := e.Right.(*st.TypedLit); ok {
		if n, ok := t.Inner.(*st.NumberLit); ok && strings.Trim(n.Value, "0.") != "" {
			return
		}
	}
	pw.diag("siemens/division", e.Pos.Line,
		fmt.Sprintf("%s by a value that is not a non-zero constant: nautilus gives 0 for a zero divisor and the CPU's result is not yet pinned down; guard it with an IF", e.Op))
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
		if _, isInst := instances[l.typ]; !isInst {
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
			parts = append(parts, fmt.Sprintf("%s := %s", strings.ToUpper(a.Name), pw.expr(a.Value, 0)))
		}
		for _, o := range c.OutputBindings {
			parts = append(parts, fmt.Sprintf("%s => %s", strings.ToUpper(o.Name), pw.expr(o.Target, 0)))
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
	args := make([]string, len(c.Args))
	for i, a := range c.Args {
		args[i] = pw.expr(a, 0)
	}
	switch {
	case mathFuncs[name]:
		if len(args) != 1 {
			pw.diag("siemens/call", line, fmt.Sprintf("%s takes one argument", name))
			return ""
		}
		return name + "(" + args[0] + ")"
	case name == "EXPT":
		if len(args) != 2 {
			pw.diag("siemens/call", line, "EXPT takes two arguments")
			return ""
		}
		return "(" + pw.expr(c.Args[0], precedence["**"]) + " ** " + pw.expr(c.Args[1], precedence["**"]+1) + ")"
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
		if elementary[from] && elementary[to] {
			if len(args) != 1 {
				pw.diag("siemens/call", line, fmt.Sprintf("%s takes one argument", name))
				return ""
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
