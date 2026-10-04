package ld

import (
	"fmt"
	"strings"
)

// Expr is an IEC 61131-3 expression as written in a ladder assignment
// (`{ y := a + b }`) — parsed so the ladder compiler can spell it as the
// FBD netlist's prefix calls, and so a code generator for another
// controller can pick its instruction from the shape (a sum is one
// instruction, a product another, anything else an expression box).
//
// The node kinds: *Lit (a number, TRUE/FALSE, a typed or based literal,
// a duration), *Ref (a variable with its accessor chain, verbatim),
// *Call (FN(args)), *Unary (NOT x, -x), *Binary (x op y).
type Expr interface {
	// IEC prints the expression back in infix IEC form, normalized.
	IEC() string
	// FBD prints it as the netlist's prefix calls: ADD(a, b), NOT x.
	FBD() string
}

type Lit struct{ Text string }
type Ref struct{ Text string }
type Call struct {
	Name string
	Args []Expr
}
type Unary struct {
	Op string // "-" | "NOT"
	X  Expr
}
type Binary struct {
	Op   string // ** * / MOD + - < > <= >= = <> AND XOR OR
	L, R Expr
}

func (e *Lit) IEC() string { return e.Text }
func (e *Lit) FBD() string { return e.Text }
func (e *Ref) IEC() string { return e.Text }
func (e *Ref) FBD() string { return e.Text }
func (e *Call) IEC() string {
	args := make([]string, len(e.Args))
	for i, a := range e.Args {
		args[i] = a.IEC()
	}
	return e.Name + "(" + strings.Join(args, ", ") + ")"
}
func (e *Call) FBD() string {
	args := make([]string, len(e.Args))
	for i, a := range e.Args {
		args[i] = a.FBD()
	}
	return e.Name + "(" + strings.Join(args, ", ") + ")"
}
func (e *Unary) IEC() string {
	if e.Op == "NOT" {
		return "NOT " + paren(e.X, 7)
	}
	return "-" + paren(e.X, 7)
}
func (e *Unary) FBD() string {
	if e.Op == "NOT" {
		return "NOT " + e.X.FBD()
	}
	if l, ok := e.X.(*Lit); ok {
		return "-" + l.Text
	}
	return "SUB(0, " + e.X.FBD() + ")"
}
func (e *Binary) IEC() string {
	p := precedence(e.Op)
	l := paren(e.L, p)
	r := paren(e.R, p+1) // left-associative: the right side needs a stronger bind
	if e.Op == "**" {
		l, r = paren(e.L, p+1), paren(e.R, p) // right-associative
	}
	return l + " " + e.Op + " " + r
}

var fbdFn = map[string]string{"+": "ADD", "-": "SUB", "*": "MUL", "/": "DIV", "MOD": "MOD", "**": "EXPT",
	"<": "LT", ">": "GT", "<=": "LE", ">=": "GE", "=": "EQ", "<>": "NE", "AND": "AND", "XOR": "XOR", "OR": "OR"}

func (e *Binary) FBD() string {
	return fbdFn[e.Op] + "(" + e.L.FBD() + ", " + e.R.FBD() + ")"
}

// precedence: higher binds tighter. IEC 61131-3 table 55.
func precedence(op string) int {
	switch op {
	case "OR":
		return 1
	case "XOR":
		return 2
	case "AND":
		return 3
	case "<", ">", "<=", ">=", "=", "<>":
		return 4
	case "+", "-":
		return 5
	case "*", "/", "MOD":
		return 6
	case "**":
		return 8
	}
	return 0
}

// paren prints x, parenthesized when it binds looser than min.
func paren(x Expr, min int) string {
	switch v := x.(type) {
	case *Binary:
		if precedence(v.Op) < min {
			return "(" + v.IEC() + ")"
		}
	case *Unary:
		if 7 < min {
			return "(" + v.IEC() + ")"
		}
	}
	return x.IEC()
}

// ParseExpr parses one IEC expression.
func ParseExpr(src string) (Expr, error) {
	p := &exprParser{src: src}
	e, err := p.parse(0)
	if err != nil {
		return nil, err
	}
	p.ws()
	if p.pos < len(p.src) {
		return nil, fmt.Errorf("unexpected %q", p.src[p.pos:])
	}
	return e, nil
}

type exprParser struct {
	src string
	pos int
}

func (p *exprParser) ws() {
	for p.pos < len(p.src) && (p.src[p.pos] == ' ' || p.src[p.pos] == '\t' || p.src[p.pos] == '\n' || p.src[p.pos] == '\r') {
		p.pos++
	}
}

// binary operators by precedence level (precedence() above); level 7 is
// unary, 8 is **.
var binOps = [][]string{
	{}, {"OR"}, {"XOR"}, {"AND", "&"}, {"<=", ">=", "<>", "<", ">", "="}, {"+", "-"}, {"*", "/", "MOD"},
}

func (p *exprParser) parse(level int) (Expr, error) {
	if level == 7 {
		return p.unary()
	}
	left, err := p.parse(level + 1)
	if err != nil {
		return nil, err
	}
	for {
		p.ws()
		op := p.peekOp(binOps[level])
		if op == "" {
			return left, nil
		}
		p.pos += len(op)
		if op == "&" {
			op = "AND"
		}
		right, err := p.parse(level + 1)
		if err != nil {
			return nil, err
		}
		left = &Binary{Op: op, L: left, R: right}
	}
}

// peekOp matches one of ops at the cursor; a word operator needs a word
// boundary after it.
func (p *exprParser) peekOp(ops []string) string {
	rest := p.src[p.pos:]
	up := strings.ToUpper(rest)
	for _, op := range ops {
		if !strings.HasPrefix(up, op) {
			continue
		}
		if isIdentStart(op[0]) {
			if len(rest) > len(op) && isIdentPart(rest[len(op)]) {
				continue
			}
		}
		return op
	}
	return ""
}

func (p *exprParser) unary() (Expr, error) {
	p.ws()
	if p.peekOp([]string{"NOT"}) == "NOT" {
		p.pos += 3
		x, err := p.unary()
		if err != nil {
			return nil, err
		}
		return &Unary{Op: "NOT", X: x}, nil
	}
	if p.pos < len(p.src) && p.src[p.pos] == '-' {
		p.pos++
		x, err := p.unary()
		if err != nil {
			return nil, err
		}
		if l, ok := x.(*Lit); ok && !strings.HasPrefix(l.Text, "-") {
			return &Lit{Text: "-" + l.Text}, nil
		}
		return &Unary{Op: "-", X: x}, nil
	}
	if p.pos < len(p.src) && p.src[p.pos] == '+' {
		p.pos++
		return p.unary()
	}
	return p.power()
}

func (p *exprParser) power() (Expr, error) {
	base, err := p.primary()
	if err != nil {
		return nil, err
	}
	p.ws()
	if strings.HasPrefix(p.src[p.pos:], "**") {
		p.pos += 2
		exp, err := p.unary() // right-associative, binds the unary too
		if err != nil {
			return nil, err
		}
		return &Binary{Op: "**", L: base, R: exp}, nil
	}
	return base, nil
}

func (p *exprParser) primary() (Expr, error) {
	p.ws()
	if p.pos >= len(p.src) {
		return nil, fmt.Errorf("expected an operand")
	}
	c := p.src[p.pos]
	switch {
	case c == '(':
		p.pos++
		e, err := p.parse(0)
		if err != nil {
			return nil, err
		}
		p.ws()
		if p.pos >= len(p.src) || p.src[p.pos] != ')' {
			return nil, fmt.Errorf("expected ')'")
		}
		p.pos++
		return e, nil
	case c >= '0' && c <= '9':
		return p.number()
	case c == '\'':
		// a string literal, verbatim
		end := strings.IndexByte(p.src[p.pos+1:], '\'')
		if end < 0 {
			return nil, fmt.Errorf("unclosed string")
		}
		lit := p.src[p.pos : p.pos+end+2]
		p.pos += end + 2
		return &Lit{Text: lit}, nil
	case isIdentStart(c):
		start := p.pos
		for p.pos < len(p.src) && isIdentPart(p.src[p.pos]) {
			p.pos++
		}
		word := p.src[start:p.pos]
		up := strings.ToUpper(word)
		// A typed or based literal: INT#5, T#5S, 16#FF, 2#1010, D#…
		if p.pos < len(p.src) && p.src[p.pos] == '#' {
			p.pos++
			for p.pos < len(p.src) && (isIdentPart(p.src[p.pos]) || p.src[p.pos] == '.' || p.src[p.pos] == '-' || p.src[p.pos] == '+') {
				p.pos++
			}
			return &Lit{Text: p.src[start:p.pos]}, nil
		}
		if up == "TRUE" || up == "FALSE" {
			return &Lit{Text: up}, nil
		}
		// A call: NAME( … )
		if p.pos < len(p.src) && p.src[p.pos] == '(' {
			p.pos++
			call := &Call{Name: up}
			p.ws()
			if p.pos < len(p.src) && p.src[p.pos] == ')' {
				p.pos++
				return call, nil
			}
			for {
				a, err := p.parse(0)
				if err != nil {
					return nil, err
				}
				call.Args = append(call.Args, a)
				p.ws()
				if p.pos < len(p.src) && p.src[p.pos] == ',' {
					p.pos++
					continue
				}
				if p.pos < len(p.src) && p.src[p.pos] == ')' {
					p.pos++
					return call, nil
				}
				return nil, fmt.Errorf("expected ',' or ')' in %s(…)", word)
			}
		}
		// A reference with its accessor chain, verbatim.
		for p.pos < len(p.src) {
			switch p.src[p.pos] {
			case '[':
				depth := 0
				for p.pos < len(p.src) {
					if p.src[p.pos] == '[' {
						depth++
					}
					if p.src[p.pos] == ']' {
						depth--
					}
					p.pos++
					if depth == 0 {
						break
					}
				}
				if depth != 0 {
					return nil, fmt.Errorf("unclosed '[' in %s", word)
				}
				continue
			case '.':
				if p.pos+1 < len(p.src) && isIdentStart(p.src[p.pos+1]) {
					p.pos++
					for p.pos < len(p.src) && isIdentPart(p.src[p.pos]) {
						p.pos++
					}
					continue
				}
			}
			break
		}
		return &Ref{Text: p.src[start:p.pos]}, nil
	}
	return nil, fmt.Errorf("unexpected %q", string(c))
}

func (p *exprParser) number() (Expr, error) {
	start := p.pos
	for p.pos < len(p.src) && (p.src[p.pos] >= '0' && p.src[p.pos] <= '9' || p.src[p.pos] == '_') {
		p.pos++
	}
	if p.pos < len(p.src) && p.src[p.pos] == '#' {
		// based literal: 16#FF, 2#1010
		p.pos++
		for p.pos < len(p.src) && (isIdentPart(p.src[p.pos])) {
			p.pos++
		}
		return &Lit{Text: p.src[start:p.pos]}, nil
	}
	if p.pos < len(p.src) && p.src[p.pos] == '.' && p.pos+1 < len(p.src) && p.src[p.pos+1] >= '0' && p.src[p.pos+1] <= '9' {
		p.pos++
		for p.pos < len(p.src) && (p.src[p.pos] >= '0' && p.src[p.pos] <= '9' || p.src[p.pos] == '_') {
			p.pos++
		}
	}
	if p.pos < len(p.src) && (p.src[p.pos] == 'e' || p.src[p.pos] == 'E') {
		q := p.pos + 1
		if q < len(p.src) && (p.src[q] == '+' || p.src[q] == '-') {
			q++
		}
		if q < len(p.src) && p.src[q] >= '0' && p.src[q] <= '9' {
			p.pos = q
			for p.pos < len(p.src) && p.src[p.pos] >= '0' && p.src[p.pos] <= '9' {
				p.pos++
			}
		}
	}
	return &Lit{Text: p.src[start:p.pos]}, nil
}

// Assignment is one `target := expr` of a ladder assignment element.
type Assignment struct {
	Target string
	Value  Expr
}

// ParseAssignments parses the body of `{ … }`: assignments separated by
// semicolons.
func ParseAssignments(body string) ([]Assignment, error) {
	var out []Assignment
	for _, stmt := range splitStatements(body) {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		i := strings.Index(stmt, ":=")
		if i < 0 {
			return nil, fmt.Errorf("%q: an assignment needs ':='", stmt)
		}
		target := strings.TrimSpace(stmt[:i])
		tok := &rungTok{src: target, line: 1}
		got, err := tok.ident()
		if err != nil || got != target {
			return nil, fmt.Errorf("%q: the target must be a variable, a member or an element", target)
		}
		value, err := ParseExpr(stmt[i+2:])
		if err != nil {
			return nil, fmt.Errorf("%q: %v", stmt, err)
		}
		out = append(out, Assignment{Target: target, Value: value})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("an empty assignment")
	}
	return out, nil
}

// splitStatements splits on ';' outside parentheses, brackets and strings.
func splitStatements(s string) []string { return splitStatementsOn(s, ';') }

// PrintAssignments renders assignments as the element body, normalized.
func PrintAssignments(as []Assignment) string {
	parts := make([]string, len(as))
	for i, a := range as {
		parts[i] = a.Target + " := " + a.Value.IEC()
	}
	return strings.Join(parts, "; ")
}
