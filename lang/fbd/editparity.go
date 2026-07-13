package fbd

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Editor-parity ops: comments, variable deletion, statement duplication
// (copy/paste), and ghost input/output reference chips. Shared philosophy:
// an edit is never refused because the RESULT wouldn't compile — the text
// must still parse (or the whole diagram dies), but semantic holes are left
// for compile diagnostics to flag and guide, exactly like typing in text.

// ── comments ────────────────────────────────────────────────────────────────

// commentRun is a block of consecutive full-line // comments in the FBD
// body: one diagram note. Lines are 1-based file lines, text is the joined
// content without the // prefixes.
type commentRun struct {
	start, end int
	text       string
}

// scanComments finds comment runs between the FBD and END_FBD lines.
// (* *) blocks — including @layout — stay invisible here.
func scanComments(src []string, bodyStart int) []commentRun {
	var runs []commentRun
	open := -1
	var buf []string
	flush := func(endLine int) {
		if open != -1 {
			runs = append(runs, commentRun{start: open, end: endLine, text: strings.Join(buf, "\n")})
			open, buf = -1, nil
		}
	}
	for i := bodyStart; i < len(src); i++ { // 0-based index i = file line i+1
		trimmed := strings.TrimSpace(src[i])
		if strings.EqualFold(trimmed, "END_FBD") {
			break
		}
		if strings.HasPrefix(trimmed, "//") {
			if open == -1 {
				open = i + 1
			}
			buf = append(buf, strings.TrimPrefix(strings.TrimPrefix(trimmed, "//"), " "))
			continue
		}
		flush(i)
	}
	flush(len(src))
	return runs
}

// renderComment turns note text back into indented // lines.
func renderComment(text string) string {
	var b strings.Builder
	for _, line := range strings.Split(text, "\n") {
		b.WriteString("  // " + strings.TrimSpace(line) + "\n")
	}
	return b.String()
}

func commentOrdinal(id string) (int, bool) {
	if !strings.HasPrefix(id, "cm:") {
		return 0, false
	}
	var n int
	if _, err := fmt.Sscanf(strings.TrimPrefix(id, "cm:"), "%d", &n); err != nil {
		return 0, false
	}
	return n, true
}

// opSetComment replaces a note's text (Node: cm:N, Text: the new content;
// newlines make a multi-line comment).
func (b *modelBuilder) opSetComment(op EditOp) ([]TextEdit, error) {
	n, ok := commentOrdinal(op.Node)
	if !ok || n < 0 || n >= len(b.comments) {
		return nil, fmt.Errorf("fbd edit: unknown comment %q", op.Node)
	}
	if strings.TrimSpace(op.Text) == "" {
		return b.deleteComment(n), nil
	}
	run := b.comments[n]
	return []TextEdit{{Line: run.start, Col: 1, EndLine: run.end + 1, EndCol: 1,
		NewText: renderComment(op.Text)}}, nil
}

func (b *modelBuilder) deleteComment(n int) []TextEdit {
	run := b.comments[n]
	return []TextEdit{{Line: run.start, Col: 1, EndLine: run.end + 1, EndCol: 1}}
}

// ── deleteVar ───────────────────────────────────────────────────────────────

// opDeleteVar removes a header declaration by name. References the netlist
// still holds become undeclared-variable diagnostics — deliberately allowed;
// the error chips lead the user to rewire or re-declare.
func (b *modelBuilder) opDeleteVar(op EditOp) ([]TextEdit, error) {
	name := strings.TrimSpace(op.NewName)
	for _, v := range b.m.Vars {
		if strings.EqualFold(v.Name, name) {
			return []TextEdit{{Line: v.Line, Col: 1, EndLine: v.Line + 1, EndCol: 1}}, nil
		}
	}
	return nil, fmt.Errorf("fbd edit: no declaration named %q", name)
}

// ── duplicate (copy/paste) ──────────────────────────────────────────────────

// opDuplicate copies the statements behind the given node ids, renaming
// every name they OWN (wires, instances, coil targets) to a fresh _copy
// name — references between copied statements follow the renames, references
// to everything else stay. The copies land right after the originals. A
// renamed coil target is usually undeclared at first: that's the intended
// breadcrumb (declare it, or retarget the coil), not a blocked edit.
func (b *modelBuilder) opDuplicate(op EditOp) ([]TextEdit, error) {
	type stmt struct {
		span exprPos
		own  []string // names this statement introduces
	}
	var stmts []stmt
	seen := map[exprPos]bool{}
	addSpan := func(span exprPos, own ...string) {
		if seen[span] {
			return
		}
		seen[span] = true
		stmts = append(stmts, stmt{span: span, own: own})
	}
	for _, id := range op.Nodes {
		switch {
		case strings.HasPrefix(id, "b:w."):
			name := strings.TrimPrefix(id, "b:w.")
			if span, ok := b.nl.wireSpan[name]; ok {
				addSpan(span, name)
			}
		case strings.HasPrefix(id, "c:"), strings.HasPrefix(id, "b:c."):
			target := strings.TrimPrefix(strings.TrimPrefix(id, "b:c."), "c:")
			target = strings.SplitN(target, ".", 2)[0]
			target = strings.SplitN(target, "#", 2)[0]
			for _, n := range b.nl.nodes {
				if !n.isCall && n.target == target {
					addSpan(n.span, target)
				}
			}
		case strings.HasPrefix(id, "f:"):
			inst := strings.TrimPrefix(id, "f:")
			for _, d := range b.nl.fbDecls {
				if d.name == inst {
					addSpan(d.span, inst)
				}
			}
			for _, n := range b.nl.nodes {
				if n.isCall && n.inst == inst {
					addSpan(n.span, inst)
				}
			}
		}
		// v:/k:/cm:/g: ids have no owned statement — chips ride along with
		// whatever references them; skipping them keeps the gesture forgiving.
	}
	if len(stmts) == 0 {
		return nil, fmt.Errorf("fbd edit: nothing copyable selected (blocks, coils, and instances copy; input chips ride their consumers)")
	}
	sort.Slice(stmts, func(i, j int) bool {
		a, c := stmts[i].span, stmts[j].span
		if a.line != c.line {
			return a.line < c.line
		}
		return a.col < c.col
	})

	// Fresh names for everything owned by the copied set.
	renames := map[string]string{}
	taken := func(name string) bool {
		if b.nameTaken(name) {
			return true
		}
		for _, nn := range renames {
			if nn == name {
				return true
			}
		}
		return false
	}
	for _, s := range stmts {
		for _, own := range s.own {
			if _, done := renames[own]; done {
				continue
			}
			fresh := own + "_copy"
			for i := 2; taken(fresh); i++ {
				fresh = fmt.Sprintf("%s_copy%d", own, i)
			}
			renames[own] = fresh
		}
	}

	last := 0
	var out strings.Builder
	for _, s := range stmts {
		text := b.stmtLines(s.span)
		for old, fresh := range renames {
			text = regexp.MustCompile(`\b`+regexp.QuoteMeta(old)+`\b`).ReplaceAllString(text, fresh)
		}
		out.WriteString(text)
		if s.span.endLine > last {
			last = s.span.endLine
		}
	}
	return []TextEdit{{Line: last + 1, Col: 1, EndLine: last + 1, EndCol: 1, NewText: out.String()}}, nil
}

// stmtLines is the statement's full source lines (newline-terminated) — the
// copy keeps the author's formatting.
func (b *modelBuilder) stmtLines(span exprPos) string {
	var out strings.Builder
	for i := span.line; i <= span.endLine && i-1 < len(b.src); i++ {
		out.WriteString(b.src[i-1] + "\n")
	}
	return out.String()
}

// ── ghost reference chips ───────────────────────────────────────────────────

// A ghost is a bare input/output reference placed on the canvas before any
// logic touches it: `g:in.Name` / `g:out.Name` entries in the @layout block
// (lexer-invisible, like pinned positions). The model renders them as dashed
// chips; wiring one turns it into real netlist text and the entry converts
// to an ordinary pin on the real node's id.
func ghostName(id string) (name string, out bool, ok bool) {
	switch {
	case strings.HasPrefix(id, "g:in."):
		return strings.TrimPrefix(id, "g:in."), false, true
	case strings.HasPrefix(id, "g:out."):
		return strings.TrimPrefix(id, "g:out."), true, true
	}
	return "", false, false
}

// buildGhosts adds a node per ghost layout entry whose name isn't already a
// real chip/coil (a stale entry after the reference became real is dropped
// on the next layout write; rendering just ignores it).
func (b *modelBuilder) buildGhosts() {
	ids := make([]string, 0, len(b.layout))
	for id := range b.layout {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		name, isOut, ok := ghostName(id)
		if !ok {
			continue
		}
		if isOut {
			if _, exists := b.coils[name]; exists {
				continue
			}
		} else if _, exists := b.inputs[name]; exists {
			continue
		}
		kind := "input"
		if isOut {
			kind = "coil"
		}
		b.add(&Node{ID: id, Kind: kind, Label: name, Ghost: true})
	}
}

// ghostConsumed converts a used ghost's layout entry onto the real node id
// it just became (so the chip keeps its position), returning layout edits.
func (b *modelBuilder) ghostConsumed(id string) []TextEdit {
	name, isOut, ok := ghostName(id)
	if !ok {
		return nil
	}
	real := "v:" + name
	if isOut {
		real = "c:" + name
	}
	return b.remapLayout(func(entryID string) (string, bool) {
		if entryID == id {
			return real, true
		}
		return entryID, true
	})
}

// wireGhostCoil realizes a ghost output reference: dropping a wire on it
// writes the coil statement `Name := ref` before END_FBD.
func (b *modelBuilder) wireGhostCoil(op EditOp) ([]TextEdit, error) {
	name, isOut, ok := ghostName(op.To)
	if !ok || !isOut {
		return nil, fmt.Errorf("fbd edit: %q is not a ghost output", op.To)
	}
	ref, err := b.refText(op.Source, op.SourcePin)
	if err != nil {
		return nil, err
	}
	endFBD := -1
	for i, line := range b.src {
		if strings.EqualFold(strings.TrimSpace(line), "END_FBD") {
			endFBD = i + 1
		}
	}
	if endFBD == -1 {
		return nil, fmt.Errorf("fbd edit: no END_FBD to insert before")
	}
	edits := []TextEdit{{Line: endFBD, Col: 1, EndLine: endFBD, EndCol: 1,
		NewText: "  " + name + " := " + ref + "\n"}}
	return append(edits, b.ghostConsumed(op.To)...), nil
}
