package fbd

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Manual-layout metadata. Auto-layout from topology is the default — no
// coordinates pollute the logic — but a user may drag nodes in the diagram
// editor, and those positions persist as a structured comment keyed by the
// render model's stable node ids:
//
//	(* @layout
//	  b:c.PumpRun 320,64
//	  v:TempC#2 24,510
//	*)
//
// The block lives right after END_FBD (see layoutLine). The lexer skips
// comments, so the block is invisible to compilation,
// transpilation, and the controller; it versions and diffs like any other
// text. Only dragged nodes appear — everything else keeps auto-layout — and
// rename/delete ops keep the entries consistent with their ids.

var layoutBlockRe = regexp.MustCompile(`\(\*\s*@layout\b`)

// layoutEntry is one pinned node position.
type layoutEntry struct{ x, y int }

// parseLayout extracts the @layout block from source lines: the entry map,
// plus the 1-based line span [start, end] of the whole comment (0,0 when
// there is no block).
func parseLayout(lines []string) (map[string]layoutEntry, int, int) {
	start := -1
	for i, l := range lines {
		if layoutBlockRe.MatchString(l) {
			start = i
			break
		}
	}
	if start == -1 {
		return nil, 0, 0
	}
	entries := map[string]layoutEntry{}
	end := start
	for i := start; i < len(lines); i++ {
		end = i
		line := lines[i]
		if i == start {
			line = layoutBlockRe.ReplaceAllString(line, "")
		}
		closed := false
		if idx := strings.Index(line, "*)"); idx >= 0 {
			line = line[:idx]
			closed = true
		}
		// Fields come in pairs: <id> <x>,<y> — ids contain no spaces.
		parts := strings.Fields(line)
		for j := 0; j+1 < len(parts); j += 2 {
			xy := strings.SplitN(parts[j+1], ",", 2)
			if len(xy) != 2 {
				continue
			}
			x, errX := strconv.Atoi(strings.TrimSpace(xy[0]))
			y, errY := strconv.Atoi(strings.TrimSpace(xy[1]))
			if errX == nil && errY == nil {
				entries[parts[j]] = layoutEntry{x: x, y: y}
			}
		}
		if closed {
			break
		}
	}
	return entries, start + 1, end + 1
}

// renderLayoutBlock emits the canonical block text (sorted ids, one entry
// per line) — stable output keeps diffs to the lines that actually moved.
func renderLayoutBlock(entries map[string]layoutEntry) string {
	ids := make([]string, 0, len(entries))
	for id := range entries {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var b strings.Builder
	b.WriteString("  (* @layout\n")
	for _, id := range ids {
		e := entries[id]
		fmt.Fprintf(&b, "    %s %d,%d\n", id, e.x, e.y)
	}
	b.WriteString("  *)\n")
	return b.String()
}

// layoutLine is where the @layout block belongs: the line right after the
// (last) END_FBD — after the logic, before END_PROGRAM/END_FUNCTION_BLOCK —
// so it never sits between statements, never reads as part of a network,
// and a statement appended just above END_FBD can never land after it
// (#208). Found on comment-stripped text, like every structural scan.
func (b *modelBuilder) layoutLine() (int, error) {
	end := -1
	for i, line := range b.srcStripped {
		if strings.EqualFold(strings.TrimSpace(line), "END_FBD") {
			end = i + 1
		}
	}
	if end == -1 {
		return 0, fmt.Errorf("fbd edit: no END_FBD to anchor the layout block")
	}
	return end + 1, nil
}

// writeLayout produces the text edit that replaces (or creates) the @layout
// block at its fixed place (layoutLine). A block found anywhere else — a
// file written before #208, where it landed wherever the body ended at the
// first layout write — moves there on this write. An empty entry set
// removes the block.
func (b *modelBuilder) writeLayout(entries map[string]layoutEntry) ([]TextEdit, error) {
	at, err := b.layoutLine()
	if err != nil {
		return nil, err
	}
	insert := func(line int) TextEdit {
		if line > len(b.src) {
			// END_FBD is the file's last line: open a line after it.
			last := len(b.src)
			text := strings.TrimSuffix(renderLayoutBlock(entries), "\n")
			return TextEdit{Line: last, Col: len(b.src[last-1]) + 1, EndLine: last, EndCol: len(b.src[last-1]) + 1, NewText: "\n" + text}
		}
		return TextEdit{Line: line, Col: 1, EndLine: line, EndCol: 1, NewText: renderLayoutBlock(entries)}
	}
	if b.layoutStart > 0 {
		old := TextEdit{Line: b.layoutStart, Col: 1, EndLine: b.layoutEnd + 1, EndCol: 1}
		if b.layoutStart == at || len(entries) == 0 {
			if len(entries) > 0 {
				old.NewText = renderLayoutBlock(entries)
			}
			return []TextEdit{old}, nil
		}
		// Migrate: the old block goes, the new one lands after END_FBD.
		return []TextEdit{old, insert(at)}, nil
	}
	if len(entries) == 0 {
		return nil, nil
	}
	return []TextEdit{insert(at)}, nil
}

// opSetLayout pins node positions (a drag in the diagram editor). A batch
// (op.Entries) pins every dragged node atomically; the single-node form
// (Node + X/Y) remains for one-node drags.
func (b *modelBuilder) opSetLayout(op EditOp) ([]TextEdit, error) {
	pins := op.Entries
	if len(pins) == 0 {
		if op.X == nil || op.Y == nil {
			return nil, fmt.Errorf("fbd edit: setLayout needs x and y")
		}
		pins = []LayoutOpEntry{{Node: op.Node, X: *op.X, Y: *op.Y}}
	}
	entries := map[string]layoutEntry{}
	for id, e := range b.layout {
		entries[id] = e
	}
	pinned := 0
	for _, p := range pins {
		if _, ok := b.nodes[p.Node]; !ok {
			// New ghost ids are CREATED by pinning them (a bare input/output
			// reference dropped on the canvas).
			if name, _, isGhost := ghostName(p.Node); isGhost && identRe.MatchString(name) {
				entries[p.Node] = layoutEntry{x: p.X, y: p.Y}
				pinned++
				continue
			}
			// Selection drags can carry phantom group entries alongside real
			// nodes — in a batch, skip them and pin the rest; only a
			// single-node op is strict.
			if len(pins) > 1 {
				continue
			}
			return nil, fmt.Errorf("fbd edit: unknown node %q", p.Node)
		}
		entries[p.Node] = layoutEntry{x: p.X, y: p.Y}
		pinned++
	}
	if pinned == 0 {
		return nil, nil
	}
	return b.writeLayout(entries)
}

// opClearLayout removes pinned positions: one node's when Node is set, or
// the whole block — back to full auto-layout — when it isn't.
func (b *modelBuilder) opClearLayout(op EditOp) ([]TextEdit, error) {
	if len(b.layout) == 0 {
		return nil, nil
	}
	if op.Node == "" {
		return b.writeLayout(nil)
	}
	entries := map[string]layoutEntry{}
	for id, e := range b.layout {
		if id != op.Node {
			entries[id] = e
		}
	}
	return b.writeLayout(entries)
}

// remapLayout rewrites pinned ids after a rename (prefix moves) or removes
// them after a delete, returning edits only when entries changed.
func (b *modelBuilder) remapLayout(rewrite func(id string) (string, bool)) []TextEdit {
	if len(b.layout) == 0 {
		return nil
	}
	changed := false
	entries := map[string]layoutEntry{}
	for id, e := range b.layout {
		nid, keep := rewrite(id)
		if !keep {
			changed = true
			continue
		}
		if nid != id {
			changed = true
		}
		entries[nid] = e
	}
	if !changed {
		return nil
	}
	edits, err := b.writeLayout(entries)
	if err != nil {
		return nil
	}
	return edits
}
