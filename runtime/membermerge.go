package runtime

import "github.com/joyautomation/nautilus/lang/ir"

// Member-level commit (#578).
//
// A program's assignment to one member of a struct tag (or one element of
// an array tag) is a read-modify-write of the whole tag inside the scan,
// because the store holds whole tags. Committing that whole value at the
// end of the scan would also write back every member the program only
// COPIED in at the start, overwriting anything an operator, an OPC UA
// client, a Sparkplug NCMD or another task stored into those members while
// the scan ran. So a scan records WHAT it assigned, as a writeMask per tag,
// and the commit lands only that:
//
//   - A member, element or bit the program assigned this scan takes the
//     program's value, even when the program stored the value it already
//     had (`S.A := S.A`). That is the rule a scalar tag has always had —
//     the program's assignment is the last write — and it is why the mask
//     records assignments rather than diffing against the snapshot, which
//     could not see an assignment of an unchanged value.
//   - Every other member keeps whatever the store holds at commit.
//   - Assigning a whole member (`S.Limits := L`) or the whole tag (`S :=
//     other`) assigns everything beneath it. A bit (`S.Word.3 := TRUE`) is
//     an assignment of its word: the granularity is the member.
//   - A function-block instance is identity, not value: an assignment
//     reaching into one commits that whole subtree, as before.
//
// Cost. Recording a member write walks the path once (no allocation once a
// tag's mask exists: masks live across scans). At commit, a tag nobody else
// has written since the snapshot — the store's generation says so — is
// stored whole, exactly as before, because the program's copy of the
// untouched members is still current. Only a tag that changed underneath
// the scan (or is forced) is merged, which copies the containers along the
// assigned paths.

// writeMask is the part of one tag a scan assigned: a tree shaped like the
// value, built lazily along the paths written, and reused scan to scan.
type writeMask struct {
	all  bool        // this node was assigned as a whole
	any  bool        // something beneath was assigned: kids is valid
	kids []writeMask // one per struct member / array element / FB pin
}

// mark records an assignment at path inside v, the tag's new whole value.
func (m *writeMask) mark(v ir.Value, path ir.MemberPath) {
	n, cur := m, v
	for i := 0; i < path.Len(); i++ {
		if n.all {
			return
		}
		idx, field := path.Step(i)
		var elems []ir.Value
		switch {
		case field && cur.Kind == ir.TypeStruct:
			elems = cur.Fld
		case !field && cur.Kind == ir.TypeArray:
			elems = cur.Arr
		default:
			// A function-block pin, or a shape this walk does not model:
			// the whole subtree is the program's.
			n.all = true
			return
		}
		if idx < 0 || idx >= len(elems) {
			n.all = true
			return
		}
		if len(n.kids) != len(elems) {
			if n.any {
				// The shape changed under marks already made this scan;
				// keep them by owning the whole subtree.
				n.all = true
				return
			}
			n.kids = make([]writeMask, len(elems))
		}
		n.any = true
		n, cur = &n.kids[idx], elems[idx]
	}
	n.all = true
}

// reset clears the marks for the next scan, keeping the allocated tree.
func (m *writeMask) reset() {
	if m.any {
		for i := range m.kids {
			if k := &m.kids[i]; k.all || k.any {
				k.reset()
			}
		}
	}
	m.all, m.any = false, false
}

// merge returns base (the tag as the store holds it now) with the marked
// parts replaced by src's (the program's value). Unmarked parts are shared
// with base, not copied: stored values are immutable by convention, so a
// container is copied only on a marked path. Where base's shape differs
// from src's — a different struct type, a different array length — src
// wins for that subtree: the program's view of the shape is the one it
// assigned through.
func (m *writeMask) merge(base, src ir.Value) ir.Value {
	if m.all || !m.any {
		if m.all {
			return src
		}
		return base
	}
	switch {
	case src.Kind == ir.TypeStruct && base.Kind == ir.TypeStruct &&
		base.Struct == src.Struct && len(base.Fld) == len(src.Fld) && len(m.kids) == len(src.Fld):
		out := base
		out.Fld = make([]ir.Value, len(base.Fld))
		copy(out.Fld, base.Fld)
		for i := range m.kids {
			if k := &m.kids[i]; k.all || k.any {
				out.Fld[i] = k.merge(base.Fld[i], src.Fld[i])
			}
		}
		return out
	case src.Kind == ir.TypeArray && base.Kind == ir.TypeArray &&
		len(base.Arr) == len(src.Arr) && len(m.kids) == len(src.Arr):
		out := base
		out.Arr = make([]ir.Value, len(base.Arr))
		copy(out.Arr, base.Arr)
		for i := range m.kids {
			if k := &m.kids[i]; k.all || k.any {
				out.Arr[i] = k.merge(base.Arr[i], src.Arr[i])
			}
		}
		return out
	}
	return src
}
