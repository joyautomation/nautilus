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
//     other`) assigns everything beneath it.
//   - A bit of an integer (`S.Word.3 := TRUE`, `W.3 := TRUE` on a plain
//     word tag, a ladder coil on a bit) assigns that bit only, and an IEC
//     partial access (`W.%B1 := …`) only its bits: the commit lands the
//     program's assigned bits into the word the store holds then (stored
//     &^ mask | program & mask), so a write to a different bit of the same
//     word that landed mid-scan survives, exactly as a sibling member does.
//     Assigning the word itself still assigns every bit. The sign bit of a
//     signed word narrower than 64 bits (INT bit 15) arrives with every bit
//     above it in the mask, because the VM holds the word sign-extended
//     and setting the sign sets them (#611): the program's sign then
//     lands with its upper bits, never beside a writer's, so the commit is
//     in range without knowing the declared type.
//   - A function-block instance is identity, not value: an assignment
//     reaching into one commits that whole subtree, as before.
//
// Cost. Recording a member write walks the path once (no allocation once a
// tag's mask exists: masks live across scans). At commit, a tag nobody else
// has written since the snapshot — the store's generation says so — is
// stored whole, exactly as before, because the program's copy of the
// untouched members and bits is still current. Only a tag that changed
// underneath the scan (or is forced) is merged, which copies the containers
// along the assigned paths.

// writeMask is the part of one tag a scan assigned: a tree shaped like the
// value, built lazily along the paths written, and reused scan to scan.
type writeMask struct {
	all  bool        // this node was assigned as a whole
	any  bool        // something beneath was assigned: kids is valid
	bits uint64      // an integer of which only these bits were assigned
	kids []writeMask // one per struct member / array element / FB pin
}

// mark records an assignment at path inside v, the tag's new whole value.
func (m *writeMask) mark(v ir.Value, path ir.MemberPath) {
	n, cur := m, v
	for i := 0; i < path.Len(); i++ {
		if n.all {
			return
		}
		if n.bits != 0 {
			// An integer's bits, now reached into as a container: the
			// shape changed this scan, so own the whole subtree.
			n.all = true
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
	if bits := path.Bits(); bits != 0 && !n.all && !n.any && cur.Kind == ir.TypeInt {
		n.bits |= bits
		return
	}
	n.all = true
}

// marked reports whether anything at or beneath this node was assigned.
func (m *writeMask) marked() bool { return m.all || m.any || m.bits != 0 }

// reset clears the marks for the next scan, keeping the allocated tree.
func (m *writeMask) reset() {
	if m.any {
		for i := range m.kids {
			if k := &m.kids[i]; k.marked() {
				k.reset()
			}
		}
	}
	m.all, m.any, m.bits = false, false, 0
}

// merge returns base (the tag as the store holds it now) with the marked
// parts replaced by src's (the program's value). Unmarked parts are shared
// with base, not copied: stored values are immutable by convention, so a
// container is copied only on a marked path. Where base's shape differs
// from src's — a different struct type, a different array length, an
// integer that is no longer one — src wins for that subtree: the program's
// view of the shape is the one it assigned through.
func (m *writeMask) merge(base, src ir.Value) ir.Value {
	if m.all {
		return src
	}
	if !m.any {
		if m.bits == 0 {
			return base
		}
		if base.Kind != ir.TypeInt || src.Kind != ir.TypeInt {
			return src
		}
		out := base
		out.I = int64(uint64(base.I)&^m.bits | uint64(src.I)&m.bits)
		return out
	}
	switch {
	case src.Kind == ir.TypeStruct && base.Kind == ir.TypeStruct &&
		base.Struct == src.Struct && len(base.Fld) == len(src.Fld) && len(m.kids) == len(src.Fld):
		out := base
		out.Fld = make([]ir.Value, len(base.Fld))
		copy(out.Fld, base.Fld)
		for i := range m.kids {
			if k := &m.kids[i]; k.marked() {
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
			if k := &m.kids[i]; k.marked() {
				out.Arr[i] = k.merge(base.Arr[i], src.Arr[i])
			}
		}
		return out
	}
	return src
}
