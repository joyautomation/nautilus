package sfc

import (
	"github.com/joyautomation/nautilus/lang/st"
	"strings"
)

// TokenType enumerates the SFC body lexer's token kinds. The SFC-specific
// keywords are always reserved (recognized regardless of surrounding
// context, including inside a transition condition or action body) — the
// same simplification IEC's own ST lexer makes for its keywords. This is
// what lets the parser find a condition's or action body's end (the
// terminating ";"/END_TRANSITION, or END_ACTION) by scanning tokens
// without re-implementing an ST expression/statement parser here: conditions
// and action bodies are captured as raw text SPANS (see Span in ast.go),
// not parsed.
type TokenType int

const (
	TokenEOF TokenType = iota
	TokenIdent
	TokenString
	TokenColon
	TokenSemicolon
	TokenComma
	TokenLParen
	TokenRParen
	TokenAssign // :=
	TokenOther  // any other single byte, opaque to the parser (operators, digits, ...)

	// Reserved words. Case-insensitive; Literal preserves the source casing.
	TokenInitialStep
	TokenStep
	TokenEndStep
	TokenTransition
	TokenEndTransition
	TokenFrom
	TokenTo
	TokenAction
	TokenEndAction
)

var sfcKeywords = map[string]TokenType{
	"INITIAL_STEP":   TokenInitialStep,
	"STEP":           TokenStep,
	"END_STEP":       TokenEndStep,
	"TRANSITION":     TokenTransition,
	"END_TRANSITION": TokenEndTransition,
	"FROM":           TokenFrom,
	"TO":             TokenTo,
	"ACTION":         TokenAction,
	"END_ACTION":     TokenEndAction,
}

// Token is one lexical token from an SFC body. Line is absolute (1-based,
// already offset into the original .sfc file — see Lex). Offset is the
// 0-based byte offset within the text Lex was given (body-relative),
// used by the parser to slice verbatim condition/action-body spans.
type Token struct {
	Type    TokenType
	Literal string
	Line    int
	Col     int
	Offset  int
}

// Lex tokenizes SFC body source — the text between the "SFC" and
// "END_SFC" marker lines. startLine is added to every 1-based line the
// lexer computes internally, so every token's Line is the line number in
// the ORIGINAL .sfc file. This mirrors lang/fbd's bodyLine convention
// (splitFBD / parseNetlist) so positions line up the same way FBD's do.
func Lex(src string, startLine int) []Token {
	l := &lexer{src: src, line: 1, col: 1, startLine: startLine}
	var toks []Token
	for {
		tok := l.next()
		toks = append(toks, tok)
		if tok.Type == TokenEOF {
			break
		}
	}
	return toks
}

type lexer struct {
	src       string
	pos       int
	line, col int
	startLine int
}

func (l *lexer) peekByte() byte {
	if l.pos >= len(l.src) {
		return 0
	}
	return l.src[l.pos]
}

func (l *lexer) peekByteAt(off int) byte {
	if l.pos+off >= len(l.src) {
		return 0
	}
	return l.src[l.pos+off]
}

func (l *lexer) advanceByte() byte {
	c := l.src[l.pos]
	l.pos++
	if c == '\n' {
		l.line++
		l.col = 1
	} else {
		l.col++
	}
	return c
}

// skipTrivia consumes whitespace, "// ..." line comments, and "(* ... *)"
// block comments (non-nested — IEC comments don't nest).
func (l *lexer) skipTrivia() {
	for l.pos < len(l.src) {
		switch {
		case l.peekByte() == ' ' || l.peekByte() == '\t' || l.peekByte() == '\r' || l.peekByte() == '\n':
			l.advanceByte()
		case l.peekByte() == '/' && l.peekByteAt(1) == '/':
			for l.pos < len(l.src) && l.peekByte() != '\n' {
				l.advanceByte()
			}
		case l.peekByte() == '(' && l.peekByteAt(1) == '*':
			l.advanceByte()
			l.advanceByte()
			for l.pos < len(l.src) && !(l.peekByte() == '*' && l.peekByteAt(1) == ')') {
				l.advanceByte()
			}
			if l.pos < len(l.src) {
				l.advanceByte()
				l.advanceByte()
			}
		default:
			return
		}
	}
}

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}

func isIdentPart(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9')
}

func (l *lexer) next() Token {
	l.skipTrivia()
	line, col, offset := l.startLine+l.line, l.col, l.pos
	if l.pos >= len(l.src) {
		return Token{Type: TokenEOF, Line: line, Col: col, Offset: offset}
	}
	c := l.peekByte()
	switch {
	case isIdentStart(c):
		start := l.pos
		for l.pos < len(l.src) && isIdentPart(l.peekByte()) {
			l.advanceByte()
		}
		lit := l.src[start:l.pos]
		if tt, ok := sfcKeywords[strings.ToUpper(lit)]; ok {
			return Token{Type: tt, Literal: lit, Line: line, Col: col, Offset: offset}
		}
		return Token{Type: TokenIdent, Literal: lit, Line: line, Col: col, Offset: offset}
	case c == '\'' || c == '"':
		// '…' or "…", ending where st's lexer ends it: $' and $" are
		// quotes inside it, so an action's 'it$'s' does not open a string
		// that runs on past END_ACTION.
		start := l.pos
		end, _ := st.StringEnd(l.src, l.pos)
		for l.pos < end {
			l.advanceByte()
		}
		return Token{Type: TokenString, Literal: l.src[start:l.pos], Line: line, Col: col, Offset: offset}
	case c == ':' && l.peekByteAt(1) == '=':
		l.advanceByte()
		l.advanceByte()
		return Token{Type: TokenAssign, Literal: ":=", Line: line, Col: col, Offset: offset}
	case c == ':':
		l.advanceByte()
		return Token{Type: TokenColon, Literal: ":", Line: line, Col: col, Offset: offset}
	case c == ';':
		l.advanceByte()
		return Token{Type: TokenSemicolon, Literal: ";", Line: line, Col: col, Offset: offset}
	case c == ',':
		l.advanceByte()
		return Token{Type: TokenComma, Literal: ",", Line: line, Col: col, Offset: offset}
	case c == '(':
		l.advanceByte()
		return Token{Type: TokenLParen, Literal: "(", Line: line, Col: col, Offset: offset}
	case c == ')':
		l.advanceByte()
		return Token{Type: TokenRParen, Literal: ")", Line: line, Col: col, Offset: offset}
	default:
		l.advanceByte()
		return Token{Type: TokenOther, Literal: string(c), Line: line, Col: col, Offset: offset}
	}
}
