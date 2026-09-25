// Package hdrvars scans the ST declaration header that precedes a
// graphical body (.fbd, .sfc) for the diagram's variables panel, and
// locates one declaration's text so the panel can delete it.
//
// Line-based and deliberately forgiving: it reads the header's raw text
// (so Init is the verbatim initializer an editor can round-trip) rather
// than a parsed AST. It understands the compact forms people actually
// write — several declarations on one line, and a whole section on one
// line (`VAR A : BOOL; B : BOOL; END_VAR`) — which a one-match-per-line
// scan silently truncated to the first declaration.
package hdrvars

import (
	"regexp"
	"strings"
)

// Decl is one `name : TYPE [:= init];` declaration.
type Decl struct {
	Name    string
	Type    string
	Init    string
	Section string // VAR, VAR_EXTERNAL, VAR_INPUT, …
	Line    int    // 1-based line of the declaration
}

// sectionRe matches a section opener at the start of a line — the keyword
// plus optional qualifiers — capturing the keyword; the rest of the line
// may carry declarations (a compact section).
var sectionRe = regexp.MustCompile(`(?i)^\s*(VAR(?:_[A-Z_]+)?)\b((?:\s+(?:RETAIN|NON_RETAIN|CONSTANT|PERSISTENT)\b)*)`)
var endVarRe = regexp.MustCompile(`(?i)\bEND_VAR\b`)
var identRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Scan returns every declaration in header, in source order. Comments are
// blanked first (positions preserved), so a commented-out declaration or
// section keyword is never read as real.
func Scan(header string) []Decl {
	var out []Decl
	section := ""
	for i, line := range strings.Split(StripComments(header), "\n") {
		rest := line
		for {
			if section == "" {
				m := sectionRe.FindStringSubmatchIndex(rest)
				if m == nil {
					break
				}
				section = strings.ToUpper(rest[m[2]:m[3]])
				rest = rest[m[1]:]
			}
			// Declarations up to END_VAR (if it closes on this line).
			body := rest
			closed := false
			if loc := endVarRe.FindStringIndex(rest); loc != nil {
				body = rest[:loc[0]]
				rest = rest[loc[1]:]
				closed = true
			}
			for _, d := range splitDecls(body) {
				if dec, ok := parseDecl(d.text); ok {
					dec.Section = section
					dec.Line = i + 1
					out = append(out, dec)
				}
			}
			if !closed {
				break
			}
			section = ""
		}
	}
	return out
}

type declText struct {
	text       string
	start, end int // byte offsets within the scanned text; end is past the ';'
}

// splitDecls splits text on `;` outside string literals. A trailing
// fragment with no `;` is dropped (it isn't a complete declaration).
func splitDecls(text string) []declText {
	var out []declText
	start := 0
	inStr := byte(0)
	for i := 0; i < len(text); i++ {
		c := text[i]
		switch {
		case inStr != 0:
			if c == inStr {
				inStr = 0
			}
		case c == '\'' || c == '"':
			inStr = c
		case c == ';':
			out = append(out, declText{text: text[start:i], start: start, end: i + 1})
			start = i + 1
		}
	}
	return out
}

func parseDecl(s string) (Decl, bool) {
	s = strings.TrimSpace(s)
	colon := strings.Index(s, ":")
	if colon < 0 || strings.HasPrefix(s[colon:], ":=") {
		return Decl{}, false
	}
	name := strings.TrimSpace(s[:colon])
	rest := strings.TrimSpace(s[colon+1:])
	init := ""
	if idx := strings.Index(rest, ":="); idx >= 0 {
		init = strings.TrimSpace(rest[idx+2:])
		rest = strings.TrimSpace(rest[:idx])
	}
	if !identRe.MatchString(name) || rest == "" {
		return Decl{}, false
	}
	return Decl{Name: name, Type: rest, Init: init}, true
}

// DeleteSpan locates the declaration of name on line (the raw text of the
// declaration's line) and returns the 1-based, end-exclusive column range
// to remove: the whole line's content when the declaration is alone on it
// (whole is then true — the caller should drop the line), else just
// `name : TYPE;` plus the whitespace after it, so its neighbours and any
// section keywords sharing the line survive.
func DeleteSpan(line, name string) (col, endCol int, whole, ok bool) {
	stripped := StripComments(line)
	// Mask section keywords so their text isn't read as part of a decl.
	scan := stripped
	if m := sectionRe.FindStringIndex(scan); m != nil {
		scan = strings.Repeat(" ", m[1]) + scan[m[1]:]
	}
	if loc := endVarRe.FindStringIndex(scan); loc != nil {
		scan = scan[:loc[0]] + strings.Repeat(" ", len(scan)-loc[0])
	}
	for _, d := range splitDecls(scan) {
		dec, good := parseDecl(d.text)
		if !good || !strings.EqualFold(dec.Name, name) {
			continue
		}
		// Start at the name itself, not the leading whitespace.
		start := d.start + (len(d.text) - len(strings.TrimLeft(d.text, " \t")))
		end := d.end
		// Alone on the line? (Nothing but whitespace/comments elsewhere.)
		if strings.TrimSpace(stripped[:start]) == "" && strings.TrimSpace(stripped[end:]) == "" {
			return 1, len(line) + 1, true, true
		}
		for end < len(line) && (line[end] == ' ' || line[end] == '\t') {
			end++
		}
		if end == len(line) { // last on the line: take the gap before it instead
			for start > 0 && (line[start-1] == ' ' || line[start-1] == '\t') {
				start--
			}
		}
		return start + 1, end + 1, false, true
	}
	return 0, 0, false, false
}

// StripComments blanks every comment body to spaces — nested `(* … *)`
// and `//` — preserving line and column positions; string literals are
// skipped whole. (Same algorithm as lang/fbd's and lang/ld's local copies.)
func StripComments(src string) string {
	out := []byte(src)
	inString := false
	for i := 0; i < len(out); {
		switch {
		case inString:
			if out[i] == '\'' {
				inString = false
			}
			i++
		case out[i] == '\'':
			inString = true
			i++
		case out[i] == '/' && i+1 < len(out) && out[i+1] == '/':
			for i < len(out) && out[i] != '\n' {
				out[i] = ' '
				i++
			}
		case out[i] == '(' && i+1 < len(out) && out[i+1] == '*':
			depth := 1
			out[i], out[i+1] = ' ', ' '
			i += 2
			for i < len(out) && depth > 0 {
				switch {
				case out[i] == '(' && i+1 < len(out) && out[i+1] == '*':
					depth++
					out[i], out[i+1] = ' ', ' '
					i += 2
				case out[i] == '*' && i+1 < len(out) && out[i+1] == ')':
					depth--
					out[i], out[i+1] = ' ', ' '
					i += 2
				case out[i] == '\n':
					i++
				default:
					out[i] = ' '
					i++
				}
			}
		default:
			i++
		}
	}
	return string(out)
}
