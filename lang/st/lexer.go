package st

import (
	"fmt"
	"strconv"
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
	errs   []LexError
}

// LexError is a lexical-level error found while scanning input: an
// unterminated (possibly nested) block comment, or a string literal that is
// unterminated or holds an unknown $ escape. The lexer is best-effort: it
// keeps producing whatever tokens it can and records errors on the side
// rather than aborting, so Lex's
// token-only signature (relied on by lang/sfc, lang/fbd, and
// internal/project) never has to change shape.
type LexError struct {
	Pos Pos
	Msg string
	// InString marks an error inside a string literal (unterminated, or
	// an unknown $ escape). Parse reports these; see Parse.
	InString bool
}

func (e LexError) Error() string {
	if e.Pos.Line > 0 {
		return fmt.Sprintf("%s at line %d:%d", e.Msg, e.Pos.Line, e.Pos.Col)
	}
	return e.Msg
}

// Lex tokenizes the entire input and returns a token slice. Lexical errors
// (see LexError) are dropped for backward compatibility with the many
// existing callers of this exact signature; use LexErrors to observe them.
func Lex(input string) []Token {
	l := &Lexer{input: input, line: 1, col: 1}
	l.lex()
	return l.tokens
}

// LexErrors tokenizes input exactly like Lex, additionally returning any
// lexical errors encountered. An unterminated block comment is reported at
// the position of its outermost "(*" — the point IEC 61131-3 3rd edition
// nesting considers still open at EOF — not the position of whichever
// nested "(*" happened to be scanned last.
func LexErrors(input string) ([]Token, []LexError) {
	l := &Lexer{input: input, line: 1, col: 1}
	l.lex()
	return l.tokens, l.errs
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
		case ch == '^':
			l.emit(TokenCaret, "^", 1)
		case ch == '%' && isPartialPrefix(l.peek(1)) && isDigit(l.peek(2)):
			l.lexPartial()
		case ch == '\'' || ch == '"':
			l.lexString(ch)
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
			l.skipBlockComment()
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

// skipBlockComment consumes a (possibly nested) "(* ... *)" block comment,
// with l.pos positioned at its opening "(". IEC 61131-3 3rd edition allows
// block comments to nest, so every "(*" seen while already inside a
// comment bumps a depth counter and only its matching "*)" pops it — a
// stray "*)" that closes an inner comment no longer spills the rest of the
// outer comment's text out as code (the bug this fixes). Scanning here is
// raw byte matching: it never special-cases string quotes, matching how
// real ST tooling treats comment bodies (a "(*" or "*)" inside a string
// literal is never reached from here in the first place — string tokens
// are only ever scanned from the top-level dispatch in lex(), not from
// inside a comment).
//
// If EOF is reached before depth returns to zero, a LexError is recorded
// pointing at the outermost "(*" — the position that IEC nesting still
// considers open — not whatever nested "(*" was scanned last.
func (l *Lexer) skipBlockComment() {
	openLine, openCol := l.line, l.col
	depth := 1
	l.advance(2) // consume the outermost "(*"
	for l.pos < len(l.input) {
		switch {
		case l.input[l.pos] == '(' && l.peek(1) == '*':
			depth++
			l.advance(2)
		case l.input[l.pos] == '*' && l.peek(1) == ')':
			depth--
			l.advance(2)
			if depth == 0 {
				return
			}
		case l.input[l.pos] == '\n':
			l.line++
			l.col = 1
			l.pos++
		default:
			l.advance(1)
		}
	}
	l.errs = append(l.errs, LexError{
		Pos: Pos{Line: openLine, Col: openCol},
		Msg: "unterminated block comment opened",
	})
}

// lexString scans a character string literal, l.pos at its opening quote:
// '…' is a STRING (IEC 61131-3 single-byte) and "…" a WSTRING (double-byte),
// which nautilus holds as STRING. Neither form's text is ever scanned as
// code, so `"a&b"` is one token, not a, &, b (nautilus#591). The token's
// Literal is the raw source between the quotes, escapes undecoded, so its
// source width stays len(Literal)+2; Quote says which quote opened it, and
// UnquoteString gives the value. Line/Col are the opening quote's.
func (l *Lexer) lexString(quote byte) {
	line, col := l.line, l.col
	lit := l.scanQuoted(quote)
	l.tokens = append(l.tokens, Token{Type: TokenString, Literal: lit, Line: line, Col: col, Quote: quote})
}

// scanQuoted consumes a quoted string from its opening quote to its closing
// one and returns the raw text between them. A `$` takes the next character
// with it, so `$'` and `$"` never close the string. A string has to close on
// its own line (IEC writes a line break as $N): one that reaches the end of
// the line or the input first is reported, at its opening quote, and ends
// there so the next line still lexes. A `$` not followed by one of the IEC
// escapes is reported where it stands.
func (l *Lexer) scanQuoted(quote byte) string {
	line, col := l.line, l.col
	l.advance(1) // opening quote
	start := l.pos
	for {
		if l.pos >= len(l.input) || l.input[l.pos] == '\n' {
			l.errs = append(l.errs, LexError{
				Pos:      Pos{Line: line, Col: col},
				Msg:      fmt.Sprintf("unterminated string: close it with %c on the same line (write a line break as $N)", quote),
				InString: true,
			})
			return l.input[start:l.pos]
		}
		ch := l.input[l.pos]
		if ch == quote {
			lit := l.input[start:l.pos]
			l.advance(1)
			return lit
		}
		if ch == '$' {
			if n := escapeLen(l.input[l.pos:], quote); n > 0 {
				l.advance(n)
				continue
			}
			l.errs = append(l.errs, LexError{
				Pos:      Pos{Line: l.line, Col: l.col},
				Msg:      badEscapeMsg(quote),
				InString: true,
			})
			if l.pos+1 < len(l.input) && l.input[l.pos+1] != '\n' {
				l.advance(2) // a bad escape still never closes the string
				continue
			}
		}
		l.advance(1)
	}
}

// escapeLen is the source length of the IEC 61131-3 escape at the start of
// s (which begins with '$') in a string opened by quote, or 0 when it is not
// one: $$, $', $", $L, $N, $P, $R, $T (any case), and a character code, two
// hex digits in a '…' STRING and four in a "…" WSTRING. The standard pairs
// $' with '…' and $" with "…"; either is accepted in both, since neither is
// ambiguous there.
func escapeLen(s string, quote byte) int {
	if len(s) < 2 {
		return 0
	}
	switch s[1] {
	case '$', '\'', '"', 'L', 'l', 'N', 'n', 'P', 'p', 'R', 'r', 'T', 't':
		return 2
	}
	digits := 2
	if quote == '"' {
		digits = 4
	}
	if len(s) < 1+digits {
		return 0
	}
	for i := 1; i <= digits; i++ {
		if !isHexDigit(s[i]) {
			return 0
		}
	}
	return 1 + digits
}

func badEscapeMsg(quote byte) string {
	code := "$hh (two hex digits)"
	if quote == '"' {
		code = "$hhhh (four hex digits)"
	}
	return "unknown string escape: $ starts $$, $', $\", $L, $N, $P, $R, $T or " + code + "; write a dollar sign as $$"
}

// UnquoteString is the value of a string literal whose raw source text
// (between the quotes, as a TokenString's Literal holds it) is raw, opened
// by quote: each IEC 61131-3 escape becomes its character. $N is a line
// feed, as on Codesys. A character code is a Unicode code point — $hh in a
// '…' STRING is the Latin-1 character, $hhhh in a "…" WSTRING the UCS-2
// one — held as UTF-8. Anything the lexer reported as a bad escape is kept
// as written.
func UnquoteString(raw string, quote byte) string {
	if !strings.Contains(raw, "$") {
		return raw
	}
	var b strings.Builder
	for i := 0; i < len(raw); {
		if raw[i] != '$' {
			b.WriteByte(raw[i])
			i++
			continue
		}
		n := escapeLen(raw[i:], quote)
		if n == 0 {
			b.WriteByte('$')
			i++
			continue
		}
		switch c := raw[i+1]; c {
		case '$', '\'', '"':
			b.WriteByte(c)
		case 'L', 'l', 'N', 'n':
			b.WriteByte('\n')
		case 'P', 'p':
			b.WriteByte('\f')
		case 'R', 'r':
			b.WriteByte('\r')
		case 'T', 't':
			b.WriteByte('\t')
		default:
			code, _ := strconv.ParseUint(raw[i+1:i+n], 16, 32)
			b.WriteRune(rune(code))
		}
		i += n
	}
	return b.String()
}

// QuoteString writes s as a '…' STRING literal that UnquoteString reads
// back as s: $, ' and the control characters escaped, the rest as is.
func QuoteString(s string) string {
	var b strings.Builder
	b.WriteByte('\'')
	for _, r := range s {
		switch r {
		case '$':
			b.WriteString("$$")
		case '\'':
			b.WriteString("$'")
		case '\n':
			b.WriteString("$N")
		case '\f':
			b.WriteString("$P")
		case '\r':
			b.WriteString("$R")
		case '\t':
			b.WriteString("$T")
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, "$%02X", r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('\'')
	return b.String()
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
			if q := l.peek(0); q == '\'' || q == '"' {
				pstart := l.pos
				l.scanQuoted(q)
				l.tokens = append(l.tokens, Token{Type: TokenTypedLiteral, Literal: upper + "#" + l.input[pstart:l.pos], Line: line, Col: col})
				return
			}
		}
		// Numeric / boolean typed literal: capture the payload until a
		// delimiter. A ".." ends it, so a CASE range of typed or enumeration
		// literals (INT#1..INT#5, Mode#Idle..Mode#Run) lexes as a range.
		pstart := l.pos
		for l.pos < len(l.input) && isTypedLitChar(l.input[l.pos]) {
			if l.input[l.pos] == '.' && l.peek(1) == '.' {
				break
			}
			l.advance(1)
		}
		// An elementary type prefix is upper-cased (INT#5); any other is a
		// user TYPE — an enumeration, Mode#Run — and keeps its spelling.
		prefix := upper
		if !IsScalarTypeName(upper) {
			prefix = lit
		}
		l.tokens = append(l.tokens, Token{Type: TokenTypedLiteral, Literal: prefix + "#" + l.input[pstart:l.pos], Line: line, Col: col})
		return
	}

	if tt, ok := keywords[upper]; ok {
		l.tokens = append(l.tokens, Token{Type: tt, Literal: lit, Line: line, Col: col})
	} else {
		l.tokens = append(l.tokens, Token{Type: TokenIdent, Literal: lit, Line: line, Col: col})
	}
}

// lexPartial scans an IEC partial-access suffix: %X3 (a bit), %B1 (a
// byte), %W0 (a word), %D0 (a double word), %L0. The size letter is
// upper-cased in the literal.
func (l *Lexer) lexPartial() {
	line, col := l.line, l.col
	start := l.pos
	l.advance(2) // % and the size letter
	for l.pos < len(l.input) && isDigit(l.input[l.pos]) {
		l.advance(1)
	}
	lit := "%" + strings.ToUpper(l.input[start+1:start+2]) + l.input[start+2:l.pos]
	l.tokens = append(l.tokens, Token{Type: TokenPartial, Literal: lit, Line: line, Col: col})
}

func isPartialPrefix(ch byte) bool {
	switch ch {
	case 'X', 'x', 'B', 'b', 'W', 'w', 'D', 'd', 'L', 'l':
		return true
	}
	return false
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

func isDigit(ch byte) bool { return ch >= '0' && ch <= '9' }
func isHexDigit(ch byte) bool {
	return isDigit(ch) || (ch >= 'a' && ch <= 'f') || (ch >= 'A' && ch <= 'F')
}
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
func isIdentPart(ch byte) bool {
	return unicode.IsLetter(rune(ch)) || unicode.IsDigit(rune(ch)) || ch == '_'
}
