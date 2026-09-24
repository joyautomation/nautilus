// Package seed lets the diagram editors start from a 0-byte file.
//
// A brand-new .fbd/.ld/.sfc is empty, and every structural op resolves
// against a parse of the current source — so without help the first
// gesture in an empty diagram has nothing to resolve against. The language
// packages instead treat blank source as "the skeleton this POU would
// have": the op is applied to that skeleton, and the result comes back as
// ONE edit replacing the (blank) file with the skeleton-plus-op. The host
// forwards ops exactly as it always does; nothing outside the language
// package knows the file was empty.
package seed

import (
	"regexp"
	"sort"
	"strings"
)

// Edit mirrors the language packages' TextEdit (1-based, end-exclusive);
// their TextEdit types share this exact shape, so each converts directly.
type Edit struct {
	Line    int    `json:"line"`
	Col     int    `json:"col"`
	EndLine int    `json:"endLine"`
	EndCol  int    `json:"endCol"`
	NewText string `json:"newText"`
}

// Blank reports whether src holds nothing but whitespace — a file the
// editor should seed rather than parse.
func Blank(src string) bool { return strings.TrimSpace(src) == "" }

var identRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// PouName is the PROGRAM name for a seeded skeleton: the caller's hint
// (the webview derives one from the file name) when it's a valid
// identifier, else "Main".
func PouName(hint string) string {
	hint = strings.TrimSpace(hint)
	if identRe.MatchString(hint) {
		return hint
	}
	return "Main"
}

// Seed resolves an op against a blank file. skeleton is the POU text the
// file starts as; apply runs the op against it (nil apply, or an "init"
// op, writes just the skeleton). The single returned edit replaces the
// whole blank file.
func Seed(src, skeleton string, apply func(string) ([]Edit, error)) ([]Edit, error) {
	text := skeleton
	if apply != nil {
		edits, err := apply(skeleton)
		if err != nil {
			return nil, err
		}
		text = Apply(skeleton, edits)
	}
	return []Edit{ReplaceAll(src, text)}, nil
}

// ReplaceAll is the edit that replaces all of src with text.
func ReplaceAll(src, text string) Edit {
	lines := strings.Split(src, "\n")
	last := lines[len(lines)-1]
	return Edit{Line: 1, Col: 1, EndLine: len(lines), EndCol: len(last) + 1, NewText: text}
}

// Apply applies non-overlapping 1-based, end-exclusive edits to src,
// back-to-front so earlier offsets stay valid.
func Apply(src string, edits []Edit) string {
	starts := []int{0}
	for i, c := range src {
		if c == '\n' {
			starts = append(starts, i+1)
		}
	}
	offset := func(line, col int) int {
		idx := min(max(line-1, 0), len(starts)-1)
		return min(max(starts[idx]+col-1, 0), len(src))
	}
	type span struct {
		lo, hi int
		text   string
	}
	spans := make([]span, len(edits))
	for i, e := range edits {
		spans[i] = span{offset(e.Line, e.Col), offset(e.EndLine, e.EndCol), e.NewText}
	}
	sort.SliceStable(spans, func(i, j int) bool { return spans[i].lo > spans[j].lo })
	out := src
	for _, s := range spans {
		lo, hi := s.lo, max(s.hi, s.lo)
		hi = min(hi, len(out))
		out = out[:lo] + s.text + out[hi:]
	}
	return out
}
