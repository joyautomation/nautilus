package ld

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// renameInst: rename a function-block instance from the diagram — the
// rung's inline declaration (`t1:TON(...)`), a header declaration of it
// (`VAR t1 : TON; END_VAR`) if the POU has one, and every reference
// (`t1.Q`, `GE(t1.ET, T#2S)`) in the POU that owns the rung. Instance names
// are POU-scoped, so a FUNCTION_BLOCK defined in the same file keeps its
// own `t1`. The edits come back together, so the host applies them as one
// WorkspaceEdit — one undo step.

func opRenameInst(src string, m *Model, op EditOp) ([]TextEdit, error) {
	r, err := findRung(m, op.Rung)
	if err != nil {
		return nil, err
	}
	el, err := locate(r, op)
	if err != nil {
		return nil, err
	}
	if el.Kind != "fb" {
		return nil, fmt.Errorf("ld edit: only function blocks have an instance name")
	}
	oldName, newName := el.Inst, strings.TrimSpace(op.Name)
	if !identOnly.MatchString(newName) {
		return nil, fmt.Errorf("ld edit: %q is not a valid instance name", newName)
	}
	if newName == oldName {
		return nil, nil
	}
	if !strings.EqualFold(newName, oldName) && instTaken(m, r.POU, newName) {
		return nil, fmt.Errorf("ld edit: %q is already declared — pick another instance name", newName)
	}

	edits, renamed, err := renameInPOU(src, m, r.POU, oldName, newName)
	if err != nil {
		return nil, err
	}
	if len(edits) == 0 {
		return nil, fmt.Errorf("ld edit: instance %q not found in the text", oldName)
	}
	// The never-break-the-text gate: the renamed file must still parse.
	if _, err := Graph(strings.Join(renamed, "\n")); err != nil {
		return nil, fmt.Errorf("ld edit: refused — the result would not parse: %w", err)
	}
	return edits, nil
}

// renameInPOU rewrites every occurrence of oldName that names the variable
// (renameable) on the lines of one POU, and returns the line edits and the
// file's lines with them applied.
func renameInPOU(src string, m *Model, pou, oldName, newName string) ([]TextEdit, []string, error) {
	lines := strings.Split(src, "\n")
	stripped := strings.Split(stripComments(src), "\n")
	inScope := pouLines(m, pou)

	var edits []TextEdit
	renamed := append([]string(nil), lines...)
	depth := 0 // paren depth, carried across lines (a call may wrap)
	inString := false
	prevWord := ""
	for i := range lines {
		if !inScope(i + 1) {
			continue
		}
		code := stripped[i]
		var cuts [][2]int // byte spans of oldName to replace on this line
		for j := 0; j < len(code); {
			c := code[j]
			switch {
			case inString:
				if c == '\'' {
					inString = false
				}
				j++
			case c == '\'':
				inString = true
				j++
			case c == '(':
				depth++
				j++
			case c == ')':
				if depth > 0 {
					depth--
				}
				j++
			case isIdentStart(c):
				k := j
				for k < len(code) && isIdentPart(code[k]) {
					k++
				}
				word := code[j:k]
				if strings.EqualFold(word, oldName) && renameable(code, j, k, depth, prevWord) {
					cuts = append(cuts, [2]int{j, k})
				}
				prevWord = word
				j = k
			default:
				if c != ' ' && c != '\t' {
					prevWord = ""
				}
				j++
			}
		}
		if len(cuts) == 0 {
			continue
		}
		var b strings.Builder
		last := 0
		for _, c := range cuts {
			b.WriteString(lines[i][last:c[0]])
			b.WriteString(newName)
			last = c[1]
		}
		b.WriteString(lines[i][last:])
		renamed[i] = b.String()
		edits = append(edits, TextEdit{Line: i + 1, Col: 1, EndLine: i + 1, EndCol: len(lines[i]) + 1, NewText: renamed[i]})
	}
	return edits, renamed, nil
}

// renameVar: rename a header declaration from the variables panel — the
// declaration and every reference in the POU that declares it (Block, or
// the PROGRAM). A FUNCTION_BLOCK's pin is also its callers' named binding
// (`m1:MotorStarter(Stop := …)`), so a pin rename follows into every call
// of that block in this file; callers in other files are left to the
// diagnostics, as a deleted declaration's references are.
func opRenameVar(src string, m *Model, op EditOp) ([]TextEdit, error) {
	v, err := findVar(m, strings.TrimSpace(op.Name), op.Block)
	if err != nil {
		return nil, err
	}
	oldName, newName := v.Name, strings.TrimSpace(op.NewName)
	if !identOnly.MatchString(newName) {
		return nil, fmt.Errorf("ld edit: %q is not a valid identifier", newName)
	}
	if newName == oldName {
		return nil, nil
	}
	if !strings.EqualFold(newName, oldName) && instTaken(m, v.POU, newName) {
		return nil, fmt.Errorf("ld edit: %q is already declared — pick another name", newName)
	}
	edits, renamed, err := renameInPOU(src, m, v.POU, oldName, newName)
	if err != nil {
		return nil, err
	}
	if v.POU != "" && (v.Section == "VAR_INPUT" || v.Section == "VAR_OUTPUT" || v.Section == "VAR_IN_OUT") {
		calls := callerBindingEdits(renamed, m, v.POU, oldName, newName)
		for _, e := range calls {
			renamed[e.Line-1] = e.NewText
		}
		edits = mergeLineEdits(edits, calls)
		sort.Slice(edits, func(i, j int) bool { return edits[i].Line < edits[j].Line })
	}
	if len(edits) == 0 {
		return nil, fmt.Errorf("ld edit: %q not found in the text", oldName)
	}
	if _, err := Graph(strings.Join(renamed, "\n")); err != nil {
		return nil, fmt.Errorf("ld edit: refused — the result would not parse: %w", err)
	}
	return edits, nil
}

// callerBindingEdits renames a block's pin where calls of that block bind
// it by name — `inst:Block(Old := x)`, `Old => y` — on the lines outside
// the block itself (its own rungs were renamed with its header).
func callerBindingEdits(lines []string, m *Model, block, oldName, newName string) []TextEdit {
	inBlock := pouLines(m, block)
	call := regexp.MustCompile(`(?i)\b[A-Za-z_][A-Za-z0-9_]*(\[[^\]]*\])?\s*:\s*` + regexp.QuoteMeta(block) + `\s*\(`)
	pin := regexp.MustCompile(`(?i)(^|[(,]\s*)` + regexp.QuoteMeta(oldName) + `(\s*(:=|=>))`)
	var out []TextEdit
	for i, line := range lines {
		if inBlock(i + 1) {
			continue
		}
		locs := call.FindAllStringIndex(line, -1)
		if locs == nil {
			continue
		}
		var b strings.Builder
		last := 0
		for _, loc := range locs {
			open := loc[1] // just past "("
			end := matchParen(line, open-1)
			if end < 0 {
				continue
			}
			args := line[open:end]
			args = pin.ReplaceAllString(args, "${1}"+newName+"${2}")
			b.WriteString(line[last:open])
			b.WriteString(args)
			last = end
		}
		b.WriteString(line[last:])
		if nl := b.String(); nl != line {
			out = append(out, TextEdit{Line: i + 1, Col: 1, EndLine: i + 1, EndCol: len(line) + 1, NewText: nl})
		}
	}
	return out
}

// matchParen returns the index of the ")" closing the "(" at i, or -1.
func matchParen(s string, i int) int {
	depth := 0
	for j := i; j < len(s); j++ {
		switch s[j] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return j
			}
		}
	}
	return -1
}

// mergeLineEdits joins two sets of whole-line edits; on a line both touch,
// the later set (built on the earlier one's result) wins.
func mergeLineEdits(a, b []TextEdit) []TextEdit {
	byLine := map[int]int{}
	out := append([]TextEdit(nil), a...)
	for i, e := range out {
		byLine[e.Line] = i
	}
	for _, e := range b {
		if i, ok := byLine[e.Line]; ok {
			out[i] = e
			continue
		}
		out = append(out, e)
	}
	return out
}

// renameable decides whether one occurrence of the old name is the
// instance: not a member (`x.t1`), not a typed literal's tail (`T#t1`), not
// a pin name inside a call (`t1 := …` / `t1 => …` within parentheses), and
// not a rung's own name (`RUNG t1`).
func renameable(code string, j, k, depth int, prevWord string) bool {
	if j > 0 && (code[j-1] == '.' || code[j-1] == '#') {
		return false
	}
	if strings.EqualFold(prevWord, "RUNG") {
		return false
	}
	if depth > 0 {
		rest := strings.TrimLeft(code[k:], " \t")
		if strings.HasPrefix(rest, ":=") || strings.HasPrefix(rest, "=>") {
			return false
		}
	}
	return true
}

// pouLines reports whether a 1-based line belongs to the named POU: a
// FUNCTION_BLOCK's own span, or — for the PROGRAM ("") — every line outside
// the file's blocks.
func pouLines(m *Model, pou string) func(int) bool {
	if pou != "" {
		for _, b := range m.Blocks {
			if b.Name == pou {
				return func(l int) bool { return l >= b.Line && l <= b.EndLine }
			}
		}
	}
	return func(l int) bool {
		for _, b := range m.Blocks {
			if l >= b.Line && l <= b.EndLine {
				return false
			}
		}
		return true
	}
}
