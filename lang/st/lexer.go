
package st

import (
	"strings"
	"unicode"
)

// Lexer tokenizes IEC 61131-3 Structured Text source.
type Lexer struct {
	input  string
	pos    int
	line   int
	col    int
	tokens []Token
}

// Lex tokenizes the entire input and returns a token slice.
func Lex(input string) []Token {
	l := &Lexer{input: input, line: 1, col: 1}
	l.lex()
	return l.tokens
}

func (l *Lexer) lex() {
	for l.pos < len(l.input) {
		l.skipWhitespaceAndComments()
		if l.pos >= len(l.input) {
			break
		}
		ch := l.input[l.pos]

		switch {
		case ch == ':' && l.peek(1) == '=':
			l.emit(TokenAssign, ":=", 2)
		case ch == '=' && l.peek(1) == '>':
			l.emit(TokenOutputAssign, "=>", 2)
		case ch == '<' && l.peek(1) == '>':
			l.emit(TokenNotEqual, "<>", 2)
		case ch == '<' && l.peek(1) == '=':
			l.emit(TokenLessEq, "<=", 2)
		case ch == '>' && l.peek(1) == '=':
			l.emit(TokenGreaterEq, ">=", 2)
		case ch == '.' && l.peek(1) == '.':
			l.emit(TokenDotDot, "..", 2)
		case ch == '<':
			l.emit(TokenLess, "<", 1)
		case ch == '>':
			l.emit(TokenGreater, ">", 1)
		case ch == '=':
			l.emit(TokenEqual, "=", 1)
		case ch == '+':
			l.emit(TokenPlus, "+", 1)
		case ch == '-':
			l.emit(TokenMinus, "-", 1)
		case ch == '*':
			l.emit(TokenStar, "*", 1)
		case ch == '/':
			l.emit(TokenSlash, "/", 1)
		case ch == '(':
			l.emit(TokenLParen, "(", 1)
		case ch == ')':
			l.emit(TokenRParen, ")", 1)
		case ch == '[':
			l.emit(TokenLBracket, "[", 1)
		case ch == ']':
			l.emit(TokenRBracket, "]", 1)
		case ch == ';':
			l.emit(TokenSemicolon, ";", 1)
		case ch == ':':
			l.emit(TokenColon, ":", 1)
		case ch == ',':
			l.emit(TokenComma, ",", 1)
		case ch == '.':
			l.emit(TokenDot, ".", 1)
		case ch == '#':
			l.emit(TokenHash, "#", 1)
		case ch == '\'':
			l.lexString()
		case isDigit(ch):
			l.lexNumber()
		case isIdentStart(ch):
			l.lexIdentOrKeyword()
		default:
			// Skip unknown characters.
			l.advance(1)
		}
	}
	l.tokens = append(l.tokens, Token{Type: TokenEOF, Line: l.line, Col: l.col})
}

func (l *Lexer) skipWhitespaceAndComments() {
	for l.pos < len(l.input) {
		ch := l.input[l.pos]
		if ch == ' ' || ch == '\t' || ch == '\r' {
			l.advance(1)
		} else if ch == '\n' {
			l.pos++
			l.line++
			l.col = 1
		} else if ch == '(' && l.peek(1) == '*' {
			// Block comment (* ... *)
			l.advance(2)
			for l.pos < len(l.input)-1 {
				if l.input[l.pos] == '*' && l.input[l.pos+1] == ')' {
					l.advance(2)
					break
				}
				if l.input[l.pos] == '\n' {
					l.line++
					l.col = 1
					l.pos++
				} else {
					l.advance(1)
				}
			}
		} else if ch == '/' && l.peek(1) == '/' {
			// Line comment // ...
			for l.pos < len(l.input) && l.input[l.pos] != '\n' {
				l.advance(1)
			}
		} else {
			break
		}
	}
}

func (l *Lexer) lexString() {
	l.advance(1) // skip opening '
	start := l.pos
	for l.pos < len(l.input) && l.input[l.pos] != '\'' {
		l.advance(1)
	}
	lit := l.input[start:l.pos]
	if l.pos < len(l.input) {
		l.advance(1) // skip closing '
	}
	l.tokens = append(l.tokens, Token{Type: TokenString, Literal: lit, Line: l.line, Col: l.col})
}

// lexNumber handles decimal integers, reals, exponents, and based integers (16#FF, 2#1010, 8#777).
func (l *Lexer) lexNumber() {
	start := l.pos
	line, col := l.line, l.col
	for l.pos < len(l.input) && isDigit(l.input[l.pos]) {
		l.advance(1)
	}
	// Based integer: <base>#<digits>
	if l.pos < len(l.input) && l.input[l.pos] == '#' {
		baseStr := l.input[start:l.pos]
		l.advance(1) // skip #
		digStart := l.pos
		for l.pos < len(l.input) && isBaseDigit(l.input[l.pos]) {
			l.advance(1)
		}
		lit := baseStr + "#" + l.input[digStart:l.pos]
		l.tokens = append(l.tokens, Token{Type: TokenBasedNumber, Literal: lit, Line: line, Col: col})
		return
	}
	// Decimal fractional part
	if l.pos < len(l.input) && l.input[l.pos] == '.' && l.peek(1) != '.' {
		l.advance(1)
		for l.pos < len(l.input) && isDigit(l.input[l.pos]) {
			l.advance(1)
		}
	}
	// Exponent
	if l.pos < len(l.input) && (l.input[l.pos] == 'e' || l.input[l.pos] == 'E') {
		l.advance(1)
		if l.pos < len(l.input) && (l.input[l.pos] == '+' || l.input[l.pos] == '-') {
			l.advance(1)
		}
		for l.pos < len(l.input) && isDigit(l.input[l.pos]) {
			l.advance(1)
		}
	}
	l.tokens = append(l.tokens, Token{Type: TokenNumber, Literal: l.input[start:l.pos], Line: line, Col: col})
}

func (l *Lexer) lexIdentOrKeyword() {
	start := l.pos
	line, col := l.line, l.col
	for l.pos < len(l.input) && isIdentPart(l.input[l.pos]) {
		l.advance(1)
	}
	lit := l.input[start:l.pos]
	upper := strings.ToUpper(lit)

	// Typed-prefix literal: IDENT#payload.
	// Covers T#5s (legacy TimeLiteral shape), TIME#5s, INT#42, REAL#3.14,
	// BOOL#TRUE, STRING#'x', and hex/bin/oct ints when written with a type prefix.
	if l.pos < len(l.input) && l.input[l.pos] == '#' {
		l.advance(1) // skip #
		// Time literal keeps its own token for backward compatibility.
		if upper == "T" || upper == "TIME" || upper == "LT" || upper == "LTIME" {
			tstart := l.pos
			for l.pos < len(l.input) && isTimeChar(l.input[l.pos]) {
				l.advance(1)
			}
			l.tokens = append(l.tokens, Token{Type: TokenTimeLiteral, Literal: l.input[tstart:l.pos], Line: line, Col: col})
			return
		}
		// STRING#'...' — wrap the string payload including quotes in the literal.
		if upper == "STRING" || upper == "WSTRING" {
			if l.pos < len(l.input) && l.input[l.pos] == '\'' {
				pstart := l.pos
				l.advance(1)
				for l.pos < len(l.input) && l.input[l.pos] != '\'' {
					l.advance(1)
				}
				if l.pos < len(l.input) {
					l.advance(1) // closing quote
				}
				l.tokens = append(l.tokens, Token{Type: TokenTypedLiteral, Literal: upper + "#" + l.input[pstart:l.pos], Line: line, Col: col})
				return
			}
		}
		// Numeric / boolean typed literal: capture the payload until a delimiter.
		pstart := l.pos
		for l.pos < len(l.input) && isTypedLitChar(l.input[l.pos]) {
			l.advance(1)
		}
		l.tokens = append(l.tokens, Token{Type: TokenTypedLiteral, Literal: upper + "#" + l.input[pstart:l.pos], Line: line, Col: col})
		return
	}

	if tt, ok := keywords[upper]; ok {
		l.tokens = append(l.tokens, Token{Type: tt, Literal: lit, Line: line, Col: col})
	} else {
		l.tokens = append(l.tokens, Token{Type: TokenIdent, Literal: lit, Line: line, Col: col})
	}
}

func (l *Lexer) emit(tt TokenType, lit string, width int) {
	l.tokens = append(l.tokens, Token{Type: tt, Literal: lit, Line: l.line, Col: l.col})
	l.advance(width)
}

func (l *Lexer) advance(n int) {
	l.pos += n
	l.col += n
}

func (l *Lexer) peek(offset int) byte {
	idx := l.pos + offset
	if idx >= len(l.input) {
		return 0
	}
	return l.input[idx]
}

func isDigit(ch byte) bool     { return ch >= '0' && ch <= '9' }
func isHexDigit(ch byte) bool  { return isDigit(ch) || (ch >= 'a' && ch <= 'f') || (ch >= 'A' && ch <= 'F') }
func isBaseDigit(ch byte) bool { return isHexDigit(ch) || ch == '_' }
func isTimeChar(ch byte) bool {
	// Digits and unit letters: s, m, h, d, ms (letters consumed as identifier chars).
	return isDigit(ch) || (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || ch == '_' || ch == '.'
}
func isTypedLitChar(ch byte) bool {
	return isDigit(ch) || ch == '.' || ch == '+' || ch == '-' ||
		(ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || ch == '_' || ch == '#'
}
func isIdentStart(ch byte) bool { return unicode.IsLetter(rune(ch)) || ch == '_' }
func isIdentPart(ch byte) bool  { return unicode.IsLetter(rune(ch)) || unicode.IsDigit(rune(ch)) || ch == '_' }
