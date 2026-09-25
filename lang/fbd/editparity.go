package fbd

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/joyautomation/nautilus/lang/internal/hdrvars"
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
			// Close any run that touches END_FBD here — falling through to
			// the post-loop flush would stamp it with end-of-file and a
			// later delete/rewrite of that note would take END_FBD (and
			// everything after) with it.
			flush(i)
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
			return deleteDeclEdit(b.src, v.Line, v.Name)
		}
	}
	return nil, fmt.Errorf("fbd edit: no declaration named %q", name)
}

// deleteDeclEdit removes one declaration from its line: the whole line when
// it stands alone, else just its `name : TYPE;` — a compact header
// (`VAR A : BOOL; B : BOOL; END_VAR`) keeps its neighbours.
func deleteDeclEdit(lines []string, line int, name string) ([]TextEdit, error) {
	if line < 1 || line > len(lines) {
		return nil, fmt.Errorf("fbd edit: declaration line %d out of range", line)
	}
	col, end, whole, ok := hdrvars.DeleteSpan(lines[line-1], name)
	if !ok {
		return nil, fmt.Errorf("fbd edit: can't locate the declaration of %q", name)
	}
	if whole {
		return []TextEdit{{Line: line, Col: 1, EndLine: line + 1, EndCol: 1}}, nil
	}
	return []TextEdit{{Line: line, Col: col, EndLine: line, EndCol: end}}, nil
}

// ── duplicate (copy/paste) ──────────────────────────────────────────────────

// opDuplicate copies the statements behind the given node ids, renaming
// every name they OWN (wires, instances, coil targets) to a fresh _copy
// name. References BETWEEN copied statements follow the renames (a copied
// seal-in latch stays a self-consistent loop); references to anything
// outside the selection sever to `_` open pins — a paste is the element and
// its intra-selection wiring, never silent taps into the originals' tags
// (two alarms quietly watching one sensor). Literals stay: they're the
// element's configuration, not wiring. The copies land right after the
// originals; the `_`/undeclared diagnostics are the intended breadcrumbs.
func (b *modelBuilder) opDuplicate(op EditOp) ([]TextEdit, error) {
	if strings.TrimSpace(op.Text) != "" {
		return b.opPasteFrom(op)
	}
	text, last, err := b.duplicateText(op.Nodes, b.nameTaken, false)
	if err != nil {
		return nil, err
	}
	return []TextEdit{{Line: last + 1, Col: 1, EndLine: last + 1, EndCol: 1, NewText: text}}, nil
}

// opPasteFrom is duplicate against a SNAPSHOT: op.Text is the source the
// node ids were copied from (this file earlier — before a cut deleted
// them — or another .fbd entirely). The statements come from the snapshot;
// names are freshened only where they collide with THIS file (a cut then
// paste keeps its names), and the copies land just above END_FBD. With
// KeepRefs (a cut) references outside the selection stay wired — the
// originals are gone, so nothing is silently tapped; a plain copy severs
// them exactly like duplicate.
func (b *modelBuilder) opPasteFrom(op EditOp) ([]TextEdit, error) {
	snap, err := buildModel(op.Text)
	if err != nil {
		return nil, fmt.Errorf("fbd edit: the copied source no longer parses: %v", err)
	}
	text, _, err := snap.duplicateText(op.Nodes, b.nameTaken, op.KeepRefs)
	if err != nil {
		return nil, err
	}
	endFBD := -1
	for i, line := range b.srcStripped {
		if strings.EqualFold(strings.TrimSpace(line), "END_FBD") {
			endFBD = i + 1
		}
	}
	if endFBD == -1 {
		return nil, fmt.Errorf("fbd edit: no END_FBD to paste before")
	}
	return []TextEdit{{Line: endFBD, Col: 1, EndLine: endFBD, EndCol: 1, NewText: text}}, nil
}

// duplicateText renders copies of the statements behind ids (resolved in
// b), renaming each owned name that taken reports in use to a fresh _copy
// name; unless keepRefs, references outside the selection sever to `_`.
// It returns the text and the last source line the originals occupy.
func (b *modelBuilder) duplicateText(ids []string, inUse func(string) bool, keepRefs bool) (string, int, error) {
	type stmt struct {
		span  exprPos
		own   []string // names this statement introduces
		exprs []expr   // argument/source trees — out-of-selection leaves sever
		from  map[string]bool
	}
	var stmts []*stmt
	seen := map[exprPos]*stmt{}
	// addSpan registers a statement once per span. from names the part of
	// the statement the caller resolved (its "decl" head or its "node"
	// body): one statement is reachable through several selected ids —
	// c:X and its inline block b:c.X, an instance's declaration and its
	// call — and each part's expressions must be collected exactly once,
	// or every out-of-selection leaf is cut twice and the overlapping
	// splices garble the copy (MUL(Ki, e, ScanDtS) → MUL(_, _0.0)).
	addSpan := func(span exprPos, from string, exprs []expr, own ...string) {
		s, dup := seen[span]
		if !dup {
			s = &stmt{span: span, from: map[string]bool{}}
			seen[span] = s
			stmts = append(stmts, s)
		}
		if s.from[from] {
			return
		}
		s.from[from] = true
		s.exprs = append(s.exprs, exprs...)
		for _, o := range own {
			if !slices.Contains(s.own, o) {
				s.own = append(s.own, o)
			}
		}
	}
	for _, id := range ids {
		switch {
		case strings.HasPrefix(id, "b:w."):
			name := strings.TrimPrefix(id, "b:w.")
			if span, ok := b.nl.wireSpan[name]; ok {
				addSpan(span, "node", []expr{b.nl.wires[name]}, name)
			}
		case strings.HasPrefix(id, "c:"), strings.HasPrefix(id, "b:c."):
			target := strings.TrimPrefix(strings.TrimPrefix(id, "b:c."), "c:")
			if !strings.Contains(target, "[") {
				// Plain targets only: dots here separate the inline-block
				// argpath, not a member accessor.
				target = strings.SplitN(target, ".", 2)[0]
			}
			target = strings.SplitN(target, "#", 2)[0]
			// An accessor target (Levels[2], M.Cmd) can't take a _copy
			// rename and stay parseable — those coils don't duplicate;
			// retarget the element on the copy's source instead.
			if !identRe.MatchString(target) {
				continue
			}
			for _, n := range b.nl.nodes {
				if !n.isCall && n.target == target {
					addSpan(n.span, "node", []expr{n.source}, target)
				}
			}
		case strings.HasPrefix(id, "f:"):
			inst := strings.TrimPrefix(id, "f:")
			for _, d := range b.nl.fbDecls {
				if d.name == inst {
					addSpan(d.span, "decl", nil, inst)
				}
			}
			for _, n := range b.nl.nodes {
				if n.isCall && n.inst == inst {
					var args []expr
					for _, a := range n.args {
						args = append(args, a.val)
					}
					addSpan(n.span, "node", args, inst)
				}
			}
		}
		// v:/k:/cm:/g: ids have no owned statement — chips ride along with
		// whatever references them; skipping them keeps the gesture forgiving.
	}
	if len(stmts) == 0 {
		return "", 0, fmt.Errorf("fbd edit: nothing copyable selected (blocks, coils, and instances copy; input chips ride their consumers)")
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
		if inUse(name) {
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
			if !taken(own) {
				renames[own] = own // free here (a cut, or another file): keep it
				continue
			}
			fresh := own + "_copy"
			for i := 2; taken(fresh); i++ {
				fresh = fmt.Sprintf("%s_copy%d", own, i)
			}
			renames[own] = fresh
		}
	}

	owned := map[string]bool{}
	for name := range renames {
		owned[name] = true
	}
	last := 0
	var out strings.Builder
	for _, s := range stmts {
		var cut []exprPos
		if !keepRefs {
			for _, e := range s.exprs {
				severRefs(e, owned, &cut)
			}
		}
		text := b.stmtLines(s.span, cut)
		for old, fresh := range renames {
			if old == fresh {
				continue
			}
			text = regexp.MustCompile(`\b`+regexp.QuoteMeta(old)+`\b`).ReplaceAllString(text, fresh)
		}
		out.WriteString(text)
		if s.span.endLine > last {
			last = s.span.endLine
		}
	}
	return out.String(), last, nil
}

// severRefs collects the spans of reference leaves (variables, wires,
// inst.pin reads) that point OUTSIDE the copied set — each becomes a `_`
// open pin in the copy. Literals and intra-selection references stay.
func severRefs(e expr, owned map[string]bool, out *[]exprPos) {
	switch v := e.(type) {
	case refExpr:
		if !owned[v.name] {
			*out = append(*out, v.exprPos)
		}
	case accExpr:
		// Element/member reads always point at variables outside the copied
		// set (accessors can't name wires or instances).
		*out = append(*out, v.exprPos)
	case pinExpr:
		if !owned[v.inst] {
			*out = append(*out, v.exprPos)
		}
	case notExpr:
		severRefs(v.inner, owned, out)
	case callExpr:
		for _, a := range v.args {
			severRefs(a, owned, out)
		}
	}
}

// stmtLines is the statement's full source lines (newline-terminated) — the
// copy keeps the author's formatting — with the given leaf spans replaced
// by `_` (applied right-to-left so earlier positions stay valid).
func (b *modelBuilder) stmtLines(span exprPos, cut []exprPos) string {
	lines := make([]string, 0, span.endLine-span.line+1)
	for i := span.line; i <= span.endLine && i-1 < len(b.src); i++ {
		lines = append(lines, b.src[i-1])
	}
	sort.Slice(cut, func(i, j int) bool {
		if cut[i].line != cut[j].line {
			return cut[i].line > cut[j].line
		}
		return cut[i].col > cut[j].col
	})
	// Cuts are replaced right-to-left; one that overlaps the previous
	// (already-applied) cut would splice into the `_` just written, so
	// duplicates and nested spans are skipped defensively.
	prevLine, prevCol := -1, 0
	for _, p := range cut {
		if p.line == prevLine && p.endCol > prevCol {
			continue
		}
		li := p.line - span.line
		if li < 0 || li >= len(lines) || p.line != p.endLine {
			continue
		}
		l := lines[li]
		if p.col-1 > len(l) || p.endCol-1 > len(l) {
			continue
		}
		lines[li] = l[:p.col-1] + "_" + l[p.endCol-1:]
		prevLine, prevCol = p.line, p.col
	}
	var out strings.Builder
	for _, l := range lines {
		out.WriteString(l + "\n")
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

// ghostRevert is the inverse of ghostConsumed: a disconnected coil (and,
// when the wire came from a bare variable chip, that chip too) becomes a
// ghost layout entry again, so both ends stay on the canvas as floating
// references instead of leaving placeholder diagnostics. Positions come
// from existing pinned entries, falling back to the live positions the
// webview rides on the op (a never-dragged endpoint has no pin); an
// endpoint with neither simply drops — it was only ever a canvas affordance.
func (b *modelBuilder) ghostRevert(e *Edge, target string, live []LayoutOpEntry) []TextEdit {
	ghosts := map[string]string{"c:" + target: "g:out." + target}
	if strings.HasPrefix(e.From, "v:") {
		// v:Name#2 fan-out copies all read the same variable.
		name := strings.SplitN(strings.TrimPrefix(e.From, "v:"), "#", 2)[0]
		if identRe.MatchString(name) {
			ghosts[e.From] = "g:in." + name
		}
	}
	entries := map[string]layoutEntry{}
	inline := "b:c." + target
	for id, pos := range b.layout {
		if _, isEndpoint := ghosts[id]; isEndpoint {
			continue // re-added under its ghost id below
		}
		if id == inline || strings.HasPrefix(id, inline+".") {
			continue // inline blocks die with the statement
		}
		entries[id] = pos
	}
	for old, gid := range ghosts {
		if pos, ok := b.layout[old]; ok {
			entries[gid] = pos // the remapped pin wins over any stale ghost entry
			continue
		}
		for _, p := range live {
			if p.Node == old {
				entries[gid] = layoutEntry{x: p.X, y: p.Y}
				break
			}
		}
	}
	edits, err := b.writeLayout(entries)
	if err != nil {
		return nil
	}
	return edits
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
