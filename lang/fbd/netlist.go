package fbd

import (
	"fmt"
	"strings"

	"github.com/joyautomation/nautilus/lang/st"
)

// netlist is a parsed FBD body: wire definitions, FB instance declarations,
// FB invocations, and coils, in source order.
type netlist struct {
	wires   map[string]expr // wire name -> defining block expression
	wireSrc []string        // wire names in source order (for stable errors)
	fbDecls []fbDecl        // FB instances: inst : TYPE
	nodes   []node          // FB calls and coils, in source order
}

type fbDecl struct {
	name, typ string
	line      int // 1-based source line of the declaration
}

// node is an ordered netlist statement that becomes an ST statement: an FB
// call or a coil.
type node struct {
	isCall bool
	line   int // 1-based source line of the statement, for diagnostics
	// call: inst(pin := expr, ...)
	inst string
	args []namedArg
	// coil: target := expr
	target string
	source expr
}

type namedArg struct {
	pin string
	val expr
}

// expr is the FBD expression tree (blocks, refs, literals, negation). Every
// node records the 1-based file position of its first token, so diagram
// tooling can map graphical gestures back to text edits.
type expr interface {
	isExpr()
	pos() (line, col int)
}

type exprPos struct{ line, col int }

func (p exprPos) pos() (int, int) { return p.line, p.col }

type refExpr struct { // a variable or wire name
	exprPos
	name string
}
type pinExpr struct { // FB output pin: inst.pin
	exprPos
	inst, pin string
}
type litExpr struct { // literal, emitted verbatim
	exprPos
	text string
}
type notExpr struct { // inline pin negation
	exprPos
	inner expr
}
type callExpr struct { // operator/function block
	exprPos
	fn   string
	args []expr
}

func (refExpr) isExpr()  {}
func (pinExpr) isExpr()  {}
func (litExpr) isExpr()  {}
func (notExpr) isExpr()  {}
func (callExpr) isExpr() {}

// ── parser (over the ST lexer) ─────────────────────────────────────────────

type netParser struct {
	toks []st.Token
	pos  int
	// lineOffset maps token lines (relative to the FBD body) back to lines in
	// the whole .fbd file, so parse errors point at the real source line.
	lineOffset int
}

func parseNetlist(body string, lineOffset int) (*netlist, error) {
	p := &netParser{toks: st.Lex(body), lineOffset: lineOffset}
	nl := &netlist{wires: map[string]expr{}}
	for !p.at(st.TokenEOF) {
		if err := p.item(nl); err != nil {
			return nil, err
		}
	}
	return nl, nil
}

func (p *netParser) peek() st.Token         { return p.toks[p.pos] }
func (p *netParser) at(t st.TokenType) bool { return p.toks[p.pos].Type == t }
func (p *netParser) next() st.Token         { t := p.toks[p.pos]; p.pos++; return t }

// here is the current token's file-absolute position, for expr nodes.
func (p *netParser) here() exprPos {
	t := p.peek()
	return exprPos{line: t.Line + p.lineOffset, col: t.Col}
}

func (p *netParser) posErr(msg string) error {
	t := p.peek()
	return &ParseError{Line: t.Line + p.lineOffset, Col: t.Col, Msg: msg}
}

// ParseError is a netlist parse error with a position in the .fbd file, so
// tooling (LSP diagnostics, the preview) can point at the offending line.
type ParseError struct {
	Line, Col int
	Msg       string
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("fbd: line %d col %d: %s", e.Line, e.Col, e.Msg)
}

// item parses one netlist statement. Statements are terminated by an optional
// ';' but newlines also delimit (the lexer discards newlines, so we key off
// the leading form: IDENT '=' | IDENT ':=' | IDENT ':' | IDENT '(').
func (p *netParser) item(nl *netlist) error {
	if !p.at(st.TokenIdent) {
		return p.posErr(fmt.Sprintf("expected a wire, coil, or block, got %q", p.peek().Literal))
	}
	line := p.peek().Line + p.lineOffset
	name := p.next().Literal
	switch p.peek().Type {
	case st.TokenEqual: // wire: name = <block>
		p.next()
		e, err := p.expr()
		if err != nil {
			return err
		}
		if _, dup := nl.wires[name]; dup {
			return p.posErr(fmt.Sprintf("wire %q defined twice", name))
		}
		nl.wires[name] = e
		nl.wireSrc = append(nl.wireSrc, name)
	case st.TokenAssign: // coil: target := <expr>
		p.next()
		e, err := p.expr()
		if err != nil {
			return err
		}
		nl.nodes = append(nl.nodes, node{target: name, source: e, line: line})
	case st.TokenColon: // FB instance decl: inst : TYPE  (optionally with a call)
		p.next()
		if !p.at(st.TokenIdent) {
			return p.posErr("expected a function-block type after ':'")
		}
		typ := p.next().Literal
		nl.fbDecls = append(nl.fbDecls, fbDecl{name: name, typ: typ, line: line})
		if p.at(st.TokenLParen) { // inline call: inst : TON(IN := ..., ...)
			args, err := p.namedArgs()
			if err != nil {
				return err
			}
			nl.nodes = append(nl.nodes, node{isCall: true, inst: name, args: args, line: line})
		}
	case st.TokenLParen: // FB call: inst(pin := ..., ...)
		args, err := p.namedArgs()
		if err != nil {
			return err
		}
		nl.nodes = append(nl.nodes, node{isCall: true, inst: name, args: args, line: line})
	default:
		return p.posErr(fmt.Sprintf("expected '=', ':=', ':', or '(' after %q", name))
	}
	if p.at(st.TokenSemicolon) {
		p.next()
	}
	return nil
}

// namedArgs parses "(pin := expr, ...)".
func (p *netParser) namedArgs() ([]namedArg, error) {
	if !p.at(st.TokenLParen) {
		return nil, p.posErr("expected '('")
	}
	p.next()
	var args []namedArg
	for !p.at(st.TokenRParen) {
		if !p.at(st.TokenIdent) {
			return nil, p.posErr("expected a pin name")
		}
		pin := p.next().Literal
		if !p.at(st.TokenAssign) {
			return nil, p.posErr(fmt.Sprintf("FB input %q must use ':=' (named pins)", pin))
		}
		p.next()
		e, err := p.expr()
		if err != nil {
			return nil, err
		}
		args = append(args, namedArg{pin: pin, val: e})
		if p.at(st.TokenComma) {
			p.next()
		}
	}
	p.next() // ')'
	return args, nil
}

// expr := 'NOT' expr | primary
func (p *netParser) expr() (expr, error) {
	if p.at(st.TokenNot) {
		at := p.here()
		p.next()
		inner, err := p.expr()
		if err != nil {
			return nil, err
		}
		return notExpr{exprPos: at, inner: inner}, nil
	}
	return p.primary()
}

// primary := literal | IDENT ('.' IDENT)? | IDENT '(' args ')' | '(' expr ')'
func (p *netParser) primary() (expr, error) {
	at := p.here()
	// The boolean/bit/mod operators lex as keyword tokens, not identifiers,
	// but in FBD they're block-function names (AND(a,b), MOD(a,b)). Accept
	// them as function heads when followed by '('.
	if op := opKeyword(p.peek()); op != "" {
		p.next()
		args, err := p.posArgs()
		if err != nil {
			return nil, err
		}
		return callExpr{exprPos: at, fn: op, args: args}, nil
	}
	switch t := p.peek(); t.Type {
	case st.TokenNumber, st.TokenString, st.TokenTimeLiteral, st.TokenTypedLiteral:
		p.next()
		return litExpr{exprPos: at, text: literalText(t)}, nil
	case st.TokenLParen:
		p.next()
		e, err := p.expr()
		if err != nil {
			return nil, err
		}
		if !p.at(st.TokenRParen) {
			return nil, p.posErr("expected ')'")
		}
		p.next()
		return e, nil
	case st.TokenIdent:
		name := p.next().Literal
		switch p.peek().Type {
		case st.TokenLParen: // function/operator block call
			args, err := p.posArgs()
			if err != nil {
				return nil, err
			}
			return callExpr{exprPos: at, fn: strings.ToUpper(name), args: args}, nil
		case st.TokenDot: // FB output pin
			p.next()
			if !p.at(st.TokenIdent) {
				return nil, p.posErr("expected a pin name after '.'")
			}
			pin := p.next().Literal
			return pinExpr{exprPos: at, inst: name, pin: pin}, nil
		default:
			// TRUE/FALSE lex as idents in some paths — treat boolean words as
			// literals so they emit correctly.
			if u := strings.ToUpper(name); u == "TRUE" || u == "FALSE" {
				return litExpr{exprPos: at, text: u}, nil
			}
			return refExpr{exprPos: at, name: name}, nil
		}
	}
	return nil, p.posErr(fmt.Sprintf("unexpected %q in expression", p.peek().Literal))
}

// posArgs parses positional block args "(a, b, ...)".
func (p *netParser) posArgs() ([]expr, error) {
	p.next() // '('
	var args []expr
	for !p.at(st.TokenRParen) {
		e, err := p.expr()
		if err != nil {
			return nil, err
		}
		args = append(args, e)
		if p.at(st.TokenComma) {
			p.next()
		}
	}
	p.next() // ')'
	return args, nil
}

// opKeyword returns the FBD block-function name for an operator that the ST
// lexer emits as a keyword token (AND/OR/XOR/MOD), else "". These appear in
// FBD only in function form followed by '('.
func opKeyword(t st.Token) string {
	switch t.Type {
	case st.TokenAnd:
		return "AND"
	case st.TokenOr:
		return "OR"
	case st.TokenXor:
		return "XOR"
	case st.TokenMod:
		return "MOD"
	}
	return ""
}

// literalText reconstructs a literal's source text from its token.
func literalText(t st.Token) string {
	switch t.Type {
	case st.TokenString:
		return "'" + t.Literal + "'"
	case st.TokenTimeLiteral:
		return "T#" + strings.TrimPrefix(strings.TrimPrefix(t.Literal, "T#"), "t#")
	default:
		return t.Literal
	}
}
