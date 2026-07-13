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
// The lexer skips comments, so the block is invisible to compilation,
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

// writeLayout produces the text edit that replaces (or creates, just above
// END_FBD) the @layout block. An empty entry set removes the block.
func (b *modelBuilder) writeLayout(entries map[string]layoutEntry) ([]TextEdit, error) {
	if b.layoutStart > 0 {
		if len(entries) == 0 {
			return []TextEdit{{Line: b.layoutStart, Col: 1, EndLine: b.layoutEnd + 1, EndCol: 1}}, nil
		}
		return []TextEdit{{
			Line: b.layoutStart, Col: 1, EndLine: b.layoutEnd + 1, EndCol: 1,
			NewText: renderLayoutBlock(entries),
		}}, nil
	}
	if len(entries) == 0 {
		return nil, nil
	}
	for i, line := range b.src {
		if strings.EqualFold(strings.TrimSpace(line), "END_FBD") {
			at := i + 1
			return []TextEdit{{Line: at, Col: 1, EndLine: at, EndCol: 1, NewText: renderLayoutBlock(entries)}}, nil
		}
	}
	return nil, fmt.Errorf("fbd edit: no END_FBD to anchor the layout block")
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
