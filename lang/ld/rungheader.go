package ld

import (
	"fmt"
	"regexp"
	"strings"
)

// rungNameRe matches just a RUNG header's keyword and optional name,
// stopping right after any trailing whitespace. Whatever follows on the
// line — a `(* … *)` header comment, inline elements, or nothing — is
// read separately by parseRungHeader, which (unlike rungRe) can follow a
// header comment across more than one line.
var rungNameRe = regexp.MustCompile(`(?i)^\s*RUNG\b(?:\s+([A-Za-z_][A-Za-z0-9_]*))?\s*`)

// rungHeader is a RUNG header read from the ORIGINAL source lines: its
// name, an optional (* … *) header comment (which may span any number of
// lines, and may itself nest per IEC 61131-3 3rd edition), and whatever
// inline element text follows the comment (or the name, when there is no
// comment) on its last line.
type rungHeader struct {
	name    string
	comment string
	tail    string
	bodyCol int // 1-based column, on lines[endLine], where tail begins
	endLine int // 0-based index into lines of the header's LAST line
}

// parseRungHeader reads the RUNG header starting at lines[i]. The caller
// must already know lines[i] is a RUNG header (rungRe matched the
// comment-stripped line) — parseRungHeader itself works from the ORIGINAL
// lines so a header comment's real text (and any code that follows it)
// comes through untouched, even when the comment runs on to later lines.
func parseRungHeader(lines []string, i int) (rungHeader, error) {
	raw := lines[i]
	loc := rungNameRe.FindStringSubmatchIndex(raw)
	name := ""
	if loc[2] >= 0 {
		name = raw[loc[2]:loc[3]]
	}
	endLine, endCol := i, loc[1]
	comment := ""
	if strings.HasPrefix(raw[endCol:], "(*") {
		text, cLine, cCol, ok := scanBlockComment(lines, i, endCol)
		if !ok {
			return rungHeader{}, fmt.Errorf("ld: line %d: unterminated (* … *) comment", i+1)
		}
		comment, endLine, endCol = text, cLine, cCol
	}
	rest := lines[endLine][endCol:]
	lead := len(rest) - len(strings.TrimLeft(rest, " \t"))
	return rungHeader{
		name:    name,
		comment: comment,
		tail:    strings.TrimSpace(rest),
		bodyCol: endCol + lead + 1,
		endLine: endLine,
	}, nil
}

// scanBlockComment reads a `(* … *)` comment beginning at
// lines[startLine][startCol:] (which must start with "(*"). It supports
// nesting the same way stripComments does, and may span any number of
// lines. It returns the comment's text (delimiters removed, each line
// trimmed, blank lines dropped, the rest joined with a single space —
// which leaves a same-line comment's text exactly as written), the 0-based
// line and the column immediately after the closing "*)".
func scanBlockComment(lines []string, startLine, startCol int) (text string, endLine, endCol int, ok bool) {
	var buf strings.Builder
	depth := 0
	line, col := startLine, startCol
	for line < len(lines) {
		s := lines[line]
		for col < len(s) {
			switch {
			case col+1 < len(s) && s[col] == '(' && s[col+1] == '*':
				depth++
				if depth > 1 {
					buf.WriteString("(*")
				}
				col += 2
			case col+1 < len(s) && s[col] == '*' && s[col+1] == ')':
				depth--
				col += 2
				if depth == 0 {
					return joinCommentLines(buf.String()), line, col, true
				}
				buf.WriteString("*)")
			default:
				buf.WriteByte(s[col])
				col++
			}
		}
		buf.WriteByte('\n')
		line++
		col = 0
	}
	return "", 0, 0, false
}

// joinCommentLines trims each line of a (possibly multi-line) comment's
// raw text and joins the non-blank ones with a single space. A same-line
// comment (no embedded newline) reduces to a plain TrimSpace, so this
// changes nothing for the case that already worked.
func joinCommentLines(raw string) string {
	parts := strings.Split(raw, "\n")
	kept := parts[:0]
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, " ")
}
