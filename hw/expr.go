package hw

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/joyautomation/nautilus/lang/ir"
)

// Expr is the small arithmetic/boolean expression the manifests use in two
// places: a `derived:` member computed from its siblings ("AdminUp &&
// !OperUp", "100 * InBps / (SpeedMbps * 1e6)"), and the Prometheus driver's
// `expr:` over named metric selectors ("100 - 100 * idle / cpus").
//
// It is deliberately a calculator, not a language: identifiers, numbers,
// true/false, ! && || == != < <= > >= + - * / and parentheses. Anything
// that wants a conditional, a function or state belongs in the project's
// ST, where it is versioned and testable with the rest of the logic.
// Division by zero yields 0 rather than an error — a port with unknown
// speed reads 0% utilisation, not NaN on an operator screen.
type Expr struct {
	src  string
	root node
	vars []string
}

// ParseExpr compiles src. Errors name the offending token and column.
func ParseExpr(src string) (*Expr, error) {
	p := &parser{src: src}
	p.next()
	n, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	if p.tok.kind != tokEOF {
		return nil, fmt.Errorf("expr %q: unexpected %q at column %d", src, p.tok.text, p.tok.pos+1)
	}
	e := &Expr{src: src, root: n}
	seen := map[string]bool{}
	walk(n, func(m node) {
		if id, ok := m.(identNode); ok && !seen[string(id)] {
			seen[string(id)] = true
			e.vars = append(e.vars, string(id))
		}
	})
	sort.Strings(e.vars)
	return e, nil
}

// String returns the source the expression was parsed from.
func (e *Expr) String() string { return e.src }

// Vars lists the identifiers the expression reads, sorted — what a manifest
// validator checks against the sibling members or selectors that exist.
func (e *Expr) Vars() []string { return append([]string(nil), e.vars...) }

// Eval computes the expression against a resolver. An identifier the
// resolver does not know is an error; a type clash (a string in
// arithmetic, a number under !) is an error naming the operator.
func (e *Expr) Eval(lookup func(name string) (ir.Value, bool)) (ir.Value, error) {
	return e.root.eval(lookup)
}

// ── evaluation ─────────────────────────────────────────────────────────

type node interface {
	eval(lookup func(string) (ir.Value, bool)) (ir.Value, error)
}

type numNode float64
type boolNode bool
type identNode string
type unaryNode struct {
	op string
	x  node
}
type binNode struct {
	op   string
	l, r node
}

func (n numNode) eval(func(string) (ir.Value, bool)) (ir.Value, error) {
	return ir.RealVal(float64(n)), nil
}
func (n boolNode) eval(func(string) (ir.Value, bool)) (ir.Value, error) {
	return ir.BoolVal(bool(n)), nil
}
func (n identNode) eval(lookup func(string) (ir.Value, bool)) (ir.Value, error) {
	v, ok := lookup(string(n))
	if !ok {
		return ir.Value{}, fmt.Errorf("expr: unknown name %q", string(n))
	}
	return v, nil
}

func (n unaryNode) eval(lookup func(string) (ir.Value, bool)) (ir.Value, error) {
	x, err := n.x.eval(lookup)
	if err != nil {
		return ir.Value{}, err
	}
	switch n.op {
	case "!":
		if x.Kind != ir.TypeBool {
			return ir.Value{}, fmt.Errorf("expr: ! applied to a %s", x.Kind)
		}
		return ir.BoolVal(!x.B), nil
	default: // "-"
		f, ok := num(x)
		if !ok {
			return ir.Value{}, fmt.Errorf("expr: unary - applied to a %s", x.Kind)
		}
		return ir.RealVal(-f), nil
	}
}

func (n binNode) eval(lookup func(string) (ir.Value, bool)) (ir.Value, error) {
	l, err := n.l.eval(lookup)
	if err != nil {
		return ir.Value{}, err
	}
	// && and || short-circuit, so a derived BOOL can guard a sibling that
	// is meaningless when the first operand is false.
	if n.op == "&&" || n.op == "||" {
		if l.Kind != ir.TypeBool {
			return ir.Value{}, fmt.Errorf("expr: %s applied to a %s", n.op, l.Kind)
		}
		if (n.op == "&&" && !l.B) || (n.op == "||" && l.B) {
			return l, nil
		}
		r, err := n.r.eval(lookup)
		if err != nil {
			return ir.Value{}, err
		}
		if r.Kind != ir.TypeBool {
			return ir.Value{}, fmt.Errorf("expr: %s applied to a %s", n.op, r.Kind)
		}
		return r, nil
	}
	r, err := n.r.eval(lookup)
	if err != nil {
		return ir.Value{}, err
	}
	switch n.op {
	case "==", "!=":
		eq, err := equal(l, r)
		if err != nil {
			return ir.Value{}, err
		}
		return ir.BoolVal(eq == (n.op == "==")), nil
	}
	lf, lok := num(l)
	rf, rok := num(r)
	if !lok || !rok {
		return ir.Value{}, fmt.Errorf("expr: %s applied to %s and %s", n.op, l.Kind, r.Kind)
	}
	switch n.op {
	case "+":
		return ir.RealVal(lf + rf), nil
	case "-":
		return ir.RealVal(lf - rf), nil
	case "*":
		return ir.RealVal(lf * rf), nil
	case "/":
		if rf == 0 {
			return ir.RealVal(0), nil
		}
		return ir.RealVal(lf / rf), nil
	case "<":
		return ir.BoolVal(lf < rf), nil
	case "<=":
		return ir.BoolVal(lf <= rf), nil
	case ">":
		return ir.BoolVal(lf > rf), nil
	case ">=":
		return ir.BoolVal(lf >= rf), nil
	}
	return ir.Value{}, fmt.Errorf("expr: unknown operator %q", n.op)
}

func num(v ir.Value) (float64, bool) {
	switch v.Kind {
	case ir.TypeReal:
		return v.F, true
	case ir.TypeInt, ir.TypeTime:
		return float64(v.I), true
	}
	return 0, false
}

func equal(l, r ir.Value) (bool, error) {
	if lf, ok := num(l); ok {
		rf, ok := num(r)
		if !ok {
			return false, fmt.Errorf("expr: == compares a number with a %s", r.Kind)
		}
		return lf == rf, nil
	}
	if l.Kind != r.Kind {
		return false, fmt.Errorf("expr: == compares a %s with a %s", l.Kind, r.Kind)
	}
	switch l.Kind {
	case ir.TypeBool:
		return l.B == r.B, nil
	case ir.TypeString:
		return l.S == r.S, nil
	}
	return false, fmt.Errorf("expr: == on a %s", l.Kind)
}

func walk(n node, fn func(node)) {
	fn(n)
	switch m := n.(type) {
	case unaryNode:
		walk(m.x, fn)
	case binNode:
		walk(m.l, fn)
		walk(m.r, fn)
	}
}

// ── parsing ────────────────────────────────────────────────────────────

type tokKind int

const (
	tokEOF tokKind = iota
	tokNum
	tokIdent
	tokOp
	tokLParen
	tokRParen
)

type token struct {
	kind tokKind
	text string
	pos  int
}

type parser struct {
	src string
	pos int
	tok token
}

func (p *parser) next() {
	for p.pos < len(p.src) && p.src[p.pos] == ' ' {
		p.pos++
	}
	start := p.pos
	if p.pos >= len(p.src) {
		p.tok = token{tokEOF, "", start}
		return
	}
	c := p.src[p.pos]
	switch {
	case c == '(':
		p.pos++
		p.tok = token{tokLParen, "(", start}
	case c == ')':
		p.pos++
		p.tok = token{tokRParen, ")", start}
	case unicode.IsDigit(rune(c)) || (c == '.' && p.pos+1 < len(p.src) && unicode.IsDigit(rune(p.src[p.pos+1]))):
		for p.pos < len(p.src) && (unicode.IsDigit(rune(p.src[p.pos])) || p.src[p.pos] == '.' ||
			p.src[p.pos] == 'e' || p.src[p.pos] == 'E' ||
			((p.src[p.pos] == '-' || p.src[p.pos] == '+') && (p.src[p.pos-1] == 'e' || p.src[p.pos-1] == 'E'))) {
			p.pos++
		}
		p.tok = token{tokNum, p.src[start:p.pos], start}
	case unicode.IsLetter(rune(c)) || c == '_':
		for p.pos < len(p.src) && (unicode.IsLetter(rune(p.src[p.pos])) || unicode.IsDigit(rune(p.src[p.pos])) || p.src[p.pos] == '_' || p.src[p.pos] == '.') {
			p.pos++
		}
		p.tok = token{tokIdent, p.src[start:p.pos], start}
	default:
		for _, op := range []string{"&&", "||", "==", "!=", "<=", ">=", "<", ">", "!", "+", "-", "*", "/"} {
			if strings.HasPrefix(p.src[p.pos:], op) {
				p.pos += len(op)
				p.tok = token{tokOp, op, start}
				return
			}
		}
		p.tok = token{tokOp, string(c), start}
		p.pos++
	}
}

func (p *parser) errorf(format string, args ...any) error {
	return fmt.Errorf("expr %q: %s at column %d", p.src, fmt.Sprintf(format, args...), p.tok.pos+1)
}

func (p *parser) accept(ops ...string) (string, bool) {
	if p.tok.kind != tokOp {
		return "", false
	}
	for _, op := range ops {
		if p.tok.text == op {
			p.next()
			return op, true
		}
	}
	return "", false
}

func (p *parser) parseOr() (node, error) {
	l, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for {
		op, ok := p.accept("||")
		if !ok {
			return l, nil
		}
		r, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		l = binNode{op, l, r}
	}
}

func (p *parser) parseAnd() (node, error) {
	l, err := p.parseCmp()
	if err != nil {
		return nil, err
	}
	for {
		op, ok := p.accept("&&")
		if !ok {
			return l, nil
		}
		r, err := p.parseCmp()
		if err != nil {
			return nil, err
		}
		l = binNode{op, l, r}
	}
}

func (p *parser) parseCmp() (node, error) {
	l, err := p.parseAdd()
	if err != nil {
		return nil, err
	}
	op, ok := p.accept("==", "!=", "<=", ">=", "<", ">")
	if !ok {
		return l, nil
	}
	r, err := p.parseAdd()
	if err != nil {
		return nil, err
	}
	return binNode{op, l, r}, nil
}

func (p *parser) parseAdd() (node, error) {
	l, err := p.parseMul()
	if err != nil {
		return nil, err
	}
	for {
		op, ok := p.accept("+", "-")
		if !ok {
			return l, nil
		}
		r, err := p.parseMul()
		if err != nil {
			return nil, err
		}
		l = binNode{op, l, r}
	}
}

func (p *parser) parseMul() (node, error) {
	l, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	for {
		op, ok := p.accept("*", "/")
		if !ok {
			return l, nil
		}
		r, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		l = binNode{op, l, r}
	}
}

func (p *parser) parseUnary() (node, error) {
	if op, ok := p.accept("!", "-"); ok {
		x, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		return unaryNode{op, x}, nil
	}
	return p.parsePrimary()
}

func (p *parser) parsePrimary() (node, error) {
	switch p.tok.kind {
	case tokNum:
		f, err := strconv.ParseFloat(p.tok.text, 64)
		if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
			return nil, p.errorf("bad number %q", p.tok.text)
		}
		p.next()
		return numNode(f), nil
	case tokIdent:
		text := p.tok.text
		p.next()
		switch text {
		case "true":
			return boolNode(true), nil
		case "false":
			return boolNode(false), nil
		}
		return identNode(text), nil
	case tokLParen:
		p.next()
		n, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		if p.tok.kind != tokRParen {
			return nil, p.errorf("expected )")
		}
		p.next()
		return n, nil
	case tokEOF:
		return nil, p.errorf("unexpected end of expression")
	}
	return nil, p.errorf("unexpected %q", p.tok.text)
}
