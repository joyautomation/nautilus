package ld

import (
	"fmt"
	"strings"
)

// RungSpan is a RUNG's place in the .ld source, as an editor outline lists
// it. Unlike Graph it never parses the rung's elements, so a rung that is
// half typed still has its place.
type RungSpan struct {
	Name    string // as written, or Graph's "rung<line>" for an unnamed rung
	Comment string // the header's (* … *) text
	Line    int    // 1-based line of the RUNG header
	Col     int    // 1-based column of the RUNG keyword
	EndLine int    // 1-based last line holding the rung's header or elements
	// NameCol is the 1-based column of the name on the header line, 0 for
	// an unnamed rung.
	NameCol int
	POU     string // owning FUNCTION_BLOCK, "" for the program
}

// Rungs lists every RUNG of every LD … END_LD block in source order, the
// same rungs Graph draws, without parsing their elements. An LD block with
// no END_LD runs to the end of the file.
func Rungs(src string) []RungSpan {
	lines := strings.Split(src, "\n")
	stripped := strings.Split(stripComments(src), "\n")
	var out []RungSpan
	inLD := false
	pou := ""
	cur := -1 // index into out of the rung still collecting lines
	for i := 0; i < len(lines); i++ {
		s := stripped[i]
		switch {
		case !inLD && fbStartRe.MatchString(s):
			pou = fbStartRe.FindStringSubmatch(lines[i])[1]
		case !inLD && fbEndRe.MatchString(s):
			pou = ""
		case !inLD && ldStartRe.MatchString(s):
			inLD = true
		case inLD && ldEndRe.MatchString(s):
			inLD, cur = false, -1
		case inLD && rungRe.MatchString(s):
			r := RungSpan{Line: i + 1, EndLine: i + 1, POU: pou}
			r.Col = len(lines[i]) - len(strings.TrimLeft(lines[i], " \t")) + 1
			if loc := rungNameRe.FindStringSubmatchIndex(lines[i]); loc != nil && loc[2] >= 0 {
				r.Name, r.NameCol = lines[i][loc[2]:loc[3]], loc[2]+1
			}
			if hdr, err := parseRungHeader(lines, i); err == nil {
				r.Comment = hdr.comment
				r.EndLine = hdr.endLine + 1
				i = hdr.endLine
			}
			if r.Name == "" {
				r.Name = fmt.Sprintf("rung%d", r.Line)
			}
			out = append(out, r)
			cur = len(out) - 1
		case inLD && cur >= 0 && strings.TrimSpace(s) != "":
			// An element line (comment-only lines strip to blank and do not
			// stretch the rung over the next one's leading notes).
			out[cur].EndLine = i + 1
		}
	}
	return out
}
