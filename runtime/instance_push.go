package runtime

import (
	"fmt"
	"sort"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
)

// The push rules (struct wiring, #1458 slice 4;
// docs/design/one-struct-model.md §2.3). An instance's to: and both: leaves
// go to the drivers in the same push as the output tags, each under its full
// path ("P101.Cmd.Start"), the spelling a delivery uses. A member listed whole
// (a struct or an array) is pushed leaf by leaf, so every rule below holds
// per leaf. A leaf that a rule keeps back is absent from the push, which is
// how a held output tag is kept back too: the driver is not handed it.
//
//   - A to: leaf is written per leadership. One that nothing has written in
//     this leadership is not handed over, whatever it holds: its initial
//     value is a seed for the program, and a value a takeover image carried
//     may be stale. A program's assignment, an outside write and a force
//     count as writes, even of the value the leaf already holds.
//   - An event: leaf is never sent twice. The first value it shows after a
//     start, a takeover, a driver asking for every output (a gate that
//     reopened, a session re-established, a write it lost), a leadership
//     resumed or a driver swapped in is a baseline, and only a later change
//     goes out. On an ask, a resume or a swap the baseline is the value the
//     leaf showed at the push before, so a change made on that very scan is
//     still a change.
//   - A both: leaf is the peer's. It is not handed over before it has been
//     delivered in this leadership, and then only once something has written
//     it. The push hands what was written, even if a delivery came between
//     the write and the push (no in-flight hold yet: the store shows the
//     peer's old value until the peer answers, design question 13). Once a
//     push has handed it over, a delivery of another value means the peer
//     moved off it (another client wrote it), and the leaf is the peer's
//     again: not handed over until written again, so a copy the peer has
//     moved on from is never written back, on a reopen's full push either.
//     A program that assigns the leaf every scan re-makes the command after
//     each read that shows the peer elsewhere, so it is handed on every scan
//     until the peer shows it: a level re-assert, until an in-flight hold
//     (slice 12) waits for the peer's answer.
//     An outside write before the first delivery is refused
//     (ErrNotDelivered); a program's assignment then is not a write, and the
//     delivery replaces it.
//   - A to: leaf with a readback: of the same type that nothing has written
//     in this leadership adopts the read-back's value when the read-back is
//     first delivered. The adopted value is not a command: the leaf is still
//     unwritten. An event: leaf never adopts.
//
// All of this state lives under Tags.mu.

// ErrNotDelivered: a write to a both: member before the peer's value has been
// read in this leadership. The servers answer it as any refused write.
// Experimental, as InstanceTag is.
var ErrNotDelivered = fmt.Errorf("not read from the peer yet")

// pushLeaf is one to: or both: leaf and its push state.
type pushLeaf struct {
	key   string // "P101.Cmd.Start": the leaf's full path, as pushed
	idx   []int  // its positions below the root
	node  *knode
	event bool
	both  bool
	dkey  int // a both: leaf's delivery key (instInfo.keys), else -1
	// rbKey is the delivery key of the read-back this leaf adopts, rbIdx its
	// positions; rbKey is -1 when the leaf adopts nothing.
	rbKey int
	rbIdx []int

	written bool // in this leadership (a both: leaf: since the peer last moved off it)
	// byForce: a to: leaf written only by a force; when the force goes, so
	// does the write (the actual value under it was never commanded).
	byForce bool
	// A both: leaf's command: the value last written (cmd), each new command
	// numbered (wrev), and whether no push has handed it over yet (pend). The
	// push hands cmd, not what the store shows: a delivery between the write
	// and the push shows the peer's old value (the flicker of design question
	// 13) and must not cancel the command. fillRev is the wrev the push map
	// holds, so a new command of the value already in it is handed again.
	pend          bool
	cmd           ir.Value
	wrev, fillRev uint64
	// An event leaf's baseline (based), whether it has moved off it since
	// (live), and the value a push last handed over successfully (sent,
	// hasSent): what the device holds once a driver has forgotten it.
	based, live, hasSent bool
	base, sent           ir.Value
	// inc and last are what the last evaluation put in the push.
	inc  bool
	last ir.Value
}

// buildPush lists an instance's to: and both: leaves. Caller is
// newInstInfo, after the keys are built.
func (in *instInfo) buildPush(d TagDef) {
	defs := map[*knode]MemberDef{}
	for p, m := range d.Instance.Members {
		if chain, ok := in.walk(ir.PathSegments(p)); ok {
			defs[chain[len(chain)-1]] = m
		}
	}
	keyOf := map[*knode]int{}
	for i, k := range in.keys {
		keyOf[k.node] = i
	}
	var walk func(n *knode, idx []int, entry *knode)
	walk = func(n *knode, idx []int, entry *knode) {
		if n.own != nil {
			entry = n
		}
		if !n.leaf {
			for i := range n.kids {
				walk(&n.kids[i], append(append([]int(nil), idx...), i), entry)
			}
			return
		}
		if n.kind != MemberTo && n.kind != MemberBoth {
			return
		}
		def := defs[entry]
		l := pushLeaf{key: d.Name + addrSuffix(n.path), idx: idx, node: n, dkey: -1, rbKey: -1}
		switch n.kind {
		case MemberBoth:
			l.both = true
			l.dkey = keyOf[entry]
		case MemberTo:
			l.event = def.Event
			if def.Readback != "" && entry == n && !l.event {
				in.adoptFrom(&l, def.Readback)
			}
		}
		in.push = append(in.push, l)
	}
	walk(&in.root, nil, nil)
}

// adoptFrom sets up l's read-back adoption: only from a delivered leaf of the
// same type (an expr: read-back is not delivered, and a read-back of another
// type is what WRITTEN() is for, §10).
func (in *instInfo) adoptFrom(l *pushLeaf, readback string) {
	segs := ir.PathSegments(readback)
	chain, ok := in.walk(segs)
	if !ok {
		return
	}
	rb := chain[len(chain)-1]
	if !rb.leaf || !rb.kind.delivered() {
		return
	}
	lt, err1 := memberType(in.typ, ir.PathSegments(l.node.path))
	rt, err2 := memberType(in.typ, segs)
	if err1 != nil || err2 != nil || !lt.Equal(rt) {
		return
	}
	var entry *knode
	idx := make([]int, 0, len(segs))
	n := &in.root
	for _, s := range segs {
		i, _ := n.child(s)
		idx = append(idx, i)
		n = &n.kids[i]
		if n.own != nil {
			entry = n
		}
	}
	for i, k := range in.keys {
		if k.node == entry {
			l.rbKey, l.rbIdx = i, idx
			return
		}
	}
}

// markWrittenLocked records a write of every push leaf under picks, after
// the store took it. A both: leaf counts only once its peer value has been
// delivered in this leadership; its command is what the write left under any
// force, and a write that repeats the command it holds is no new command.
// Caller holds t.mu.
func (t *Tags) markWrittenLocked(in *instInfo, under func(*pushLeaf) bool) {
	var root ir.Value
	looked := false
	for i := range in.push {
		l := &in.push[i]
		if !under(l) {
			continue
		}
		if !l.both {
			if !l.written || l.byForce {
				l.written, l.byForce = true, false
				t.pushRev++
			}
			continue
		}
		if !in.lead[l.dkey] {
			continue
		}
		if !looked {
			tv, name, ok := t.lookupLocked(in.name)
			if !ok {
				return
			}
			root, looked = t.liveLocked(name, tv), true
		}
		c, ok := valueAt(root, l.idx)
		if !ok {
			continue
		}
		if !l.written || !sameValue(&c, &l.cmd) {
			l.written, l.pend, l.cmd = true, true, c
			l.wrev++
			t.pushRev++
		}
	}
}

// markForcedLocked records a force at path of value: a to: leaf it names
// goes out with the forced value, as a forced output does, for as long as
// the force holds (forceReleasedLocked). An object names only its members:
// the ones it leaves out are held at what they were, not commanded. A both:
// leaf is the peer's memory, and a force of it is the logic's view, as a
// force of an input is: never a command. Caller holds t.mu.
func (t *Tags) markForcedLocked(path string, value any) {
	root, segs := ir.SplitAddress(path)
	in := t.instanceOf(root)
	if in == nil || len(in.push) == 0 {
		return
	}
	var targets []string
	for _, p := range writtenPaths(value) {
		if chain, ok := in.walk(append(append([]string(nil), segs...), ir.PathSegments(p)...)); ok {
			targets = append(targets, chain[len(chain)-1].canon)
		}
	}
	for i := range in.push {
		l := &in.push[i]
		if l.both || l.written {
			continue
		}
		for _, c := range targets {
			if c == "" || pathUnder(l.node.canon, c) {
				l.written, l.byForce = true, true
				t.pushRev++
				break
			}
		}
	}
}

// forceReleasedLocked: forces on root went away and fs holds what remains.
// A to: leaf that only a force had written, and no remaining force covers,
// is unwritten again. Caller holds t.mu.
func (t *Tags) forceReleasedLocked(root string, fs *forceSet) {
	in := t.instanceOf(root)
	if in == nil || len(in.push) == 0 {
		return
	}
	var held []string
	for _, e := range fs.entries {
		if len(e.path) == 0 {
			return // the whole tag is still forced
		}
		if ec, ok := in.walk(e.path); ok {
			held = append(held, ec[len(ec)-1].canon)
		}
	}
	for i := range in.push {
		l := &in.push[i]
		if !l.byForce {
			continue
		}
		covered := false
		for _, c := range held {
			if pathUnder(l.node.canon, c) {
				covered = true
			}
		}
		if !covered {
			l.written, l.byForce = false, false
			t.pushRev++
		}
	}
}

// markMaskWrittenLocked records a program's commit: every push leaf its
// write mask covers (a nil mask is the whole tag). Caller holds t.mu, after
// the commit's write.
func (t *Tags) markMaskWrittenLocked(in *instInfo, m *writeMask) {
	if len(in.push) == 0 {
		return
	}
	t.markWrittenLocked(in, func(l *pushLeaf) bool { return m == nil || m.covers(l.idx) })
}

// covers reports whether the mask assigned the leaf at idx.
func (m *writeMask) covers(idx []int) bool {
	n := m
	for _, i := range idx {
		if n.all || n.bits != 0 {
			return true
		}
		if !n.any || i >= len(n.kids) {
			return false
		}
		n = &n.kids[i]
	}
	return n.marked()
}

// markPathWrittenLocked records a write at path (a SetPath, the front door,
// a force), value being what was written there: an object names its members.
// Caller holds t.mu.
func (t *Tags) markPathWrittenLocked(path string, value any) {
	root, segs := ir.SplitAddress(path)
	in := t.instanceOf(root)
	if in == nil || len(in.push) == 0 {
		return
	}
	var targets []string
	for _, p := range writtenPaths(value) {
		chain, ok := in.walk(append(append([]string(nil), segs...), ir.PathSegments(p)...))
		if ok {
			targets = append(targets, chain[len(chain)-1].canon)
		}
	}
	t.markWrittenLocked(in, func(l *pushLeaf) bool {
		for _, c := range targets {
			if c == "" || pathUnder(l.node.canon, c) {
				return true
			}
		}
		return false
	})
}

// writtenPaths lists the member paths a write sets, relative to where it is
// written: "" for a value that is not an object, each leaf an object names
// (an empty object names nothing).
func writtenPaths(value any) []string {
	m, ok := value.(map[string]any)
	if !ok {
		return []string{""}
	}
	var out []string
	var walk func(prefix string, m map[string]any)
	walk = func(prefix string, m map[string]any) {
		for k, v := range m {
			p := k
			if prefix != "" {
				p = prefix + "." + k
			}
			if sub, ok := v.(map[string]any); ok {
				walk(p, sub)
				continue
			}
			out = append(out, p)
		}
	}
	walk("", m)
	sort.Strings(out)
	return out
}

// notDeliveredErr refuses an outside write that would set a both: leaf whose
// peer value has not been read in this leadership. Caller holds t.mu.
func (in *instInfo) notDeliveredErr(segs []string, value any) error {
	for _, p := range writtenPaths(value) {
		chain, ok := in.walk(append(append([]string(nil), segs...), ir.PathSegments(p)...))
		if !ok {
			continue
		}
		c := chain[len(chain)-1].canon
		for i := range in.push {
			l := &in.push[i]
			if l.both && !in.lead[l.dkey] && (c == "" || pathUnder(l.node.canon, c)) {
				return fmt.Errorf("%s has not been read from the peer yet: a both: member takes a write once its driver has delivered it: %w", l.key, ErrNotDelivered)
			}
		}
	}
	return nil
}

// deliveredLocked is what a delivery did to the push state: the keys it
// delivered (now) and the value it leaves (nv). A both: leaf whose command
// was handed over and which the peer now shows otherwise is the peer's
// again, so it is unwritten; a read-back delivered for the first time in this
// leadership is adopted. Returns the value with any adoption, and whether
// one was made. Caller holds t.mu.
func (t *Tags) deliveredLocked(in *instInfo, now []bool, nv ir.Value) (ir.Value, bool) {
	adopted := false
	first := in.firstNow[:0]
	for i, d := range now {
		if d && !in.lead[i] {
			in.lead[i] = true
			first = append(first, i)
		}
	}
	in.firstNow = first
	for i := range in.push {
		l := &in.push[i]
		switch {
		case l.both && now[l.dkey] && l.written && !l.pend:
			if a, ok := valueAt(nv, l.idx); ok && !sameValue(&a, &l.cmd) {
				l.written = false
				t.pushRev++
			}
		case l.rbKey >= 0 && !l.written && containsInt(first, l.rbKey):
			rv, ok := valueAt(nv, l.rbIdx)
			if !ok {
				continue
			}
			if m, ok := setAt(nv, l.idx, rv); ok {
				nv, adopted = m, true
			}
		}
	}
	return nv, adopted
}

func containsInt(s []int, v int) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// setAt returns v with the leaf at idx replaced, copying the containers on
// the way; ok is false when v has no such leaf.
func setAt(v ir.Value, idx []int, leaf ir.Value) (ir.Value, bool) {
	if len(idx) == 0 {
		return leaf, true
	}
	i := idx[0]
	switch v.Kind {
	case ir.TypeStruct:
		if i >= len(v.Fld) {
			return v, false
		}
		k, ok := setAt(v.Fld[i], idx[1:], leaf)
		if !ok {
			return v, false
		}
		out := v
		out.Fld = append([]ir.Value(nil), v.Fld...)
		out.Fld[i] = k
		return out, true
	case ir.TypeArray:
		if i >= len(v.Arr) {
			return v, false
		}
		k, ok := setAt(v.Arr[i], idx[1:], leaf)
		if !ok {
			return v, false
		}
		out := v
		out.Arr = append([]ir.Value(nil), v.Arr...)
		out.Arr[i] = k
		return out, true
	}
	return v, false
}

// newPushLeadership forgets every leaf's push state: a takeover's new
// leadership has written nothing and been delivered nothing.
func (t *Tags) newPushLeadership() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.instPush == 0 {
		return
	}
	for _, in := range t.instList {
		for i := range in.lead {
			in.lead[i] = false
		}
		for i := range in.push {
			l := &in.push[i]
			l.written, l.byForce, l.pend = false, false, false
			l.based, l.live, l.hasSent = false, false, false
		}
	}
	t.pushRev++
}

// rebaseEvents starts a new baseline for every event leaf (a driver asked for
// every output, a leadership resumed, a driver swapped in): what the device
// holds, the value last handed over successfully, or the baseline it had if
// none was. A change from there, on this very scan too, still goes out; a
// push that failed is no hand-over, so its change goes out again.
func (t *Tags) rebaseEvents() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.instPush == 0 {
		return
	}
	for _, in := range t.instList {
		for i := range in.push {
			if l := &in.push[i]; l.event && l.based {
				if l.hasSent {
					l.base = l.sent
				}
				l.live = false
			}
		}
	}
	t.pushRev++
}

// instancePush brings the instance part of out (the push's map, which keeps
// what the last call put there) up to date and reports whether it changed.
// An instance is looked at again only when its value or the push state moved.
func (t *Tags) instancePush(out nio.Values) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.instPush == 0 {
		return false
	}
	all := t.pushRev != t.pushSeen
	t.pushSeen = t.pushRev
	changed := false
	for _, in := range t.instList {
		if len(in.push) == 0 {
			continue
		}
		tv, _, ok := t.lookupLocked(in.name)
		if !ok {
			continue
		}
		if !all && in.pushLooked && tv.gen == in.pushGen {
			continue
		}
		in.pushLooked, in.pushGen = true, tv.gen
		for i := range in.push {
			l := &in.push[i]
			cur, ok := valueAt(tv.v, l.idx)
			inc := ok && l.written // a both: leaf is written only once delivered
			if l.both {
				cur = l.cmd
			}
			if l.event && ok {
				switch {
				case !l.based:
					l.based, l.base, l.live = true, cur, false
				case !l.live && !sameValue(&cur, &l.base):
					l.live = true
				}
				inc = inc && l.live
			}
			switch {
			case !inc:
				if l.inc {
					delete(out, l.key)
					changed = true
				}
			case !l.inc || !sameValue(&cur, &l.last) || (l.both && l.fillRev != l.wrev):
				if cur.Kind == ir.TypeStruct || cur.Kind == ir.TypeArray {
					out[l.key] = cur
				} else {
					out[l.key] = plain(cur)
				}
				changed = true
			}
			l.inc, l.last = inc, cur
			if inc && l.both {
				l.fillRev = l.wrev
			}
		}
	}
	return changed
}

// instancePushed: the push instancePush filled was handed over. A both:
// command in it is no longer pending, unless a newer one came while the
// driver had it; an event leaf in it is what the device now holds. taken,
// when not nil, says which keys were handed over: a push some children of
// an io.Multi failed was still taken by the others (#1497), and only the
// failing children's leaves stay unhanded.
func (t *Tags) instancePushed(taken func(key string) bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, in := range t.instList {
		for i := range in.push {
			l := &in.push[i]
			if !l.inc || taken != nil && !taken(l.key) {
				continue
			}
			if l.both && l.fillRev == l.wrev {
				l.pend = false
			}
			if l.event {
				l.sent, l.hasSent = l.last, true
			}
		}
	}
}
