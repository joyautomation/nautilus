package ld

import (
	"fmt"
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

	lines := strings.Split(src, "\n")
	stripped := strings.Split(stripComments(src), "\n")
	inScope := pouLines(m, r.POU)

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
	if len(edits) == 0 {
		return nil, fmt.Errorf("ld edit: instance %q not found in the text", oldName)
	}
	// The never-break-the-text gate: the renamed file must still parse.
	if _, err := Graph(strings.Join(renamed, "\n")); err != nil {
		return nil, fmt.Errorf("ld edit: refused — the result would not parse: %w", err)
	}
	return edits, nil
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
