package runtime

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
)

// Instances: tags of a wired type (struct wiring, #1458,
// docs/design/one-struct-model.md §2). One struct tag whose members each have
// a KIND: read from a device (from), a command to one (to), a peer
// controller's own memory (both), memory the program and operators keep, or
// an expression. The store still holds whole tags; an instance's kinds decide
// who may change which member:
//
//   - A delivery lands only the from: and both: leaves, merged onto the live
//     value, so memory and commands are never touched by a driver.
//   - A write from outside the programs (the API, an OPC UA client, a
//     Sparkplug command) is refused for a from: or expr: member, or anything
//     holding one, by the one front door they all use (Tags.Write).
//   - A program's commit never lands a from: or expr: leaf, even if
//     something assigned one: the check refuses that at load (a later
//     slice), and the commit is the last line. A hot takeover's image never
//     brings in a from: or both: leaf either: like an input, the new leader
//     reads it on its first scan.
//   - Forces, substitutes and a test's given: are not outside writes: they
//     may set delivered members, which is what they are for.
//   - Quality is per member, and the root shows the worst of its delivered
//     members (the device library's rule).
//
// Every rule is decided on the leaves of one tree built from the type
// (knode), each carrying its effective kind: its own entry, else its
// nearest listed ancestor's, else memory. Values are matched to that tree by
// member name and position, never by StructDef identity, so a driver's own
// StructDef and a recompiled TYPE after an online edit are the same shape.
//
// Experimental: CONTRIBUTING.md, "Go API". What is not here yet: commands are
// not pushed (to: members reach no driver until the push rules land), memory
// is not retained, and expressions are not evaluated. An instance is a
// RoleState tag, so it is seeded, is no driver input or output, and is never
// retained.

// MemberKind is what one member of an instance's value comes from.
// Experimental.
type MemberKind uint8

const (
	// MemberMemory: kept by the program, operators and clients. A member an
	// InstanceDef does not name is memory.
	MemberMemory MemberKind = iota
	// MemberFrom: read from a device each poll; never written by anyone
	// else.
	MemberFrom
	// MemberTo: a command the program (or an operator) sets and a driver
	// sends.
	MemberTo
	// MemberBoth: a peer controller's own memory, read each poll and written
	// on change.
	MemberBoth
	// MemberExpr: computed each scan; never written.
	MemberExpr
)

func (k MemberKind) String() string {
	switch k {
	case MemberMemory:
		return "memory"
	case MemberFrom:
		return "from"
	case MemberTo:
		return "to"
	case MemberBoth:
		return "both"
	case MemberExpr:
		return "expr"
	}
	return "kind(" + strconv.Itoa(int(k)) + ")"
}

// delivered: a driver fills it.
func (k MemberKind) delivered() bool { return k == MemberFrom || k == MemberBoth }

// readOnly: nothing but its own source (a device, the expression) sets it.
func (k MemberKind) readOnly() bool { return k == MemberFrom || k == MemberExpr }

// MemberDef is one member's wiring, as the runtime needs it. Experimental.
type MemberDef struct {
	Kind MemberKind
	// Readback names the member that shows what the device did with a
	// MemberTo (a from, both or expr member).
	Readback string
	// Event marks a MemberTo that must never be sent twice.
	Event bool
}

// InstanceDef is what makes a tag an instance. Experimental.
type InstanceDef struct {
	// Members are keyed by member path: "Speed", "Cmd.Start", "Levels[1]". A
	// member takes its own entry's kind, else its nearest listed ancestor's,
	// else MemberMemory.
	Members map[string]MemberDef
}

// InstanceTag declares a tag of a wired type. typeName is the project's TYPE
// (a STRUCT); the tag is seeded at its zero, or at Init. Experimental while
// struct wiring (#1458) lands: commands are not yet pushed to a driver,
// memory is not yet retained and expressions are not yet evaluated.
//
// A driver delivers an instance's from: and both: members either one by one,
// under the member's full path ("P101.Speed", which mixes freely across the
// children of an io.Multi), or as the whole struct under the instance's name,
// from which only those members are taken. A value of another shape, or one a
// member cannot hold, is not delivered: it is counted, and the instance's
// quality is Bad until a delivery lands.
func InstanceTag(name, typeName string, def InstanceDef, opts ...TagOpt) TagDef {
	d := newDef(name, RoleState, nil, opts)
	d.Type = typeName
	m := make(map[string]MemberDef, len(def.Members))
	for k, v := range def.Members {
		m[k] = v
	}
	d.Instance = &InstanceDef{Members: m}
	return d
}

// knode is one node of an instance's type, with its effective kind and what
// its subtree holds.
type knode struct {
	kind   MemberKind
	leaf   bool
	path   string   // as the type spells it ("Cmd.Start", "Levels[1]"), "" at the root
	names  []string // a struct's member names, folded
	lo     int      // an array's lower bound
	kids   []knode
	hasRO  bool // some leaf beneath is read-only
	hasDel bool // some leaf beneath is delivered
	allDel bool // every leaf beneath is delivered
}

// pick chooses the leaves a merge takes from its source.
type pick uint8

const (
	pickDelivered    pick = iota // a delivery: the device's leaves
	pickReadOnly                 // a commit: the store keeps these
	pickNotDelivered             // a hot install: the image brings these
)

func (n *knode) has(p pick) bool {
	switch p {
	case pickDelivered:
		return n.hasDel
	case pickReadOnly:
		return n.hasRO
	}
	return !n.allDel
}

func (n *knode) takes(p pick) bool {
	switch p {
	case pickDelivered:
		return n.kind.delivered()
	case pickReadOnly:
		return n.kind.readOnly()
	}
	return !n.kind.delivered()
}

// instInfo is one instance as the store uses it.
type instInfo struct {
	name string // the stored spelling
	typ  *ir.Type
	root knode
	// keys are the delivery keys a driver may use, one per delivered entry
	// ("P101.Speed"); got says whether each has been delivered yet.
	keys []instKey
	got  []atomic.Bool
	// dropped: the last delivery held a value this instance could not take;
	// drops counts them.
	dropped atomic.Bool
	drops   atomic.Uint64
}

type instKey struct {
	key  string   // "P101.Speed", as delivered
	segs []string // the path below the root, for SetFieldTyped
	node *knode
}

// canonSeg is one path segment's canonical form: a name folded like every
// IEC identifier, an index as its number ("[01]" and "[+1]" are "[1]",
// which is how ir resolves them).
func canonSeg(s string) (string, bool) {
	if !strings.HasPrefix(s, "[") {
		return ir.NameKey(s), true
	}
	n, err := strconv.Atoi(strings.TrimSpace(strings.Trim(s, "[]")))
	if err != nil {
		return "", false
	}
	return "[" + strconv.Itoa(n) + "]", true
}

func canonPath(segs []string) (string, bool) {
	var b strings.Builder
	for i, s := range segs {
		c, ok := canonSeg(s)
		if !ok {
			return "", false
		}
		if i > 0 && !strings.HasPrefix(c, "[") {
			b.WriteByte('.')
		}
		b.WriteString(c)
	}
	return b.String(), true
}

// walk resolves segs in the tree; ok is false for a path the type does not
// have. Every node passed on the way is in the returned chain, the target
// last.
func (in *instInfo) walk(segs []string) ([]*knode, bool) {
	chain := []*knode{&in.root}
	n := &in.root
	for _, s := range segs {
		c, ok := canonSeg(s)
		if !ok || n.leaf {
			return chain, false
		}
		if strings.HasPrefix(c, "[") {
			i, _ := strconv.Atoi(strings.Trim(c, "[]"))
			i -= n.lo
			if n.names != nil || i < 0 || i >= len(n.kids) {
				return chain, false
			}
			n = &n.kids[i]
		} else {
			i := slicesIndex(n.names, c)
			if i < 0 {
				return chain, false
			}
			n = &n.kids[i]
		}
		chain = append(chain, n)
	}
	return chain, true
}

func slicesIndex(names []string, s string) int {
	for i, n := range names {
		if n == s {
			return i
		}
	}
	return -1
}

// kindAt is the effective kind of the member at segs (memory for a path the
// type does not have).
func (in *instInfo) kindAt(segs []string) MemberKind {
	chain, ok := in.walk(segs)
	if !ok {
		return MemberMemory
	}
	return chain[len(chain)-1].kind
}

// newInstInfo validates an instance's definition against its type and
// builds its tree.
func newInstInfo(d TagDef, types map[string]*ir.Type) (*instInfo, error) {
	if d.Type == "" {
		return nil, fmt.Errorf("instance %s: an instance needs its TYPE", d.Name)
	}
	t, err := ResolveTagType(d.Type, types)
	if err != nil {
		return nil, fmt.Errorf("instance %s: %w", d.Name, err)
	}
	if t.Kind != ir.TypeStruct || t.Struct == nil {
		return nil, fmt.Errorf("instance %s: type %s is not a STRUCT: an instance is a struct whose members are wired", d.Name, d.Type)
	}
	kinds := map[string]MemberKind{}
	declared := map[string]string{}
	paths := make([]string, 0, len(d.Instance.Members))
	for p := range d.Instance.Members {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		m := d.Instance.Members[p]
		segs := ir.PathSegments(p)
		if len(segs) == 0 {
			return nil, fmt.Errorf("instance %s: an empty member path", d.Name)
		}
		if _, err := memberType(t, segs); err != nil {
			return nil, fmt.Errorf("instance %s: member %s: %w", d.Name, p, err)
		}
		if m.Kind > MemberExpr {
			return nil, fmt.Errorf("instance %s: member %s: %v is not a member kind", d.Name, p, m.Kind)
		}
		c, _ := canonPath(segs)
		if prev, dup := declared[c]; dup {
			return nil, fmt.Errorf("instance %s: member %s is listed twice (also as %s)", d.Name, p, prev)
		}
		declared[c] = p
		kinds[c] = m.Kind
		if m.Event && m.Kind != MemberTo {
			return nil, fmt.Errorf("instance %s: member %s: Event is for a MemberTo, and this one is %s", d.Name, p, m.Kind)
		}
	}
	in := &instInfo{name: d.Name, typ: t}
	in.root = buildTree(t, nil, "", MemberMemory, kinds)
	for _, p := range paths {
		m := d.Instance.Members[p]
		segs := ir.PathSegments(p)
		chain, _ := in.walk(segs)
		node := chain[len(chain)-1]
		if m.Readback != "" {
			if m.Kind != MemberTo {
				return nil, fmt.Errorf("instance %s: member %s: Readback is for a MemberTo, and this one is %s", d.Name, p, m.Kind)
			}
			rs := ir.PathSegments(m.Readback)
			if _, err := memberType(t, rs); err != nil {
				return nil, fmt.Errorf("instance %s: member %s: readback %s: %w", d.Name, p, m.Readback, err)
			}
			if k := in.kindAt(rs); k != MemberFrom && k != MemberBoth && k != MemberExpr {
				return nil, fmt.Errorf("instance %s: member %s: readback %s is %s: a read-back shows the device, so it is from, both or expr", d.Name, p, m.Readback, k)
			}
		}
		if m.Kind.delivered() {
			in.keys = append(in.keys, instKey{key: d.Name + addrSuffix(node.path), segs: segs, node: node})
		}
	}
	in.got = make([]atomic.Bool, len(in.keys))
	return in, nil
}

// buildTree makes the node for type t at segs, with its effective kind.
func buildTree(t *ir.Type, segs []string, path string, inherited MemberKind, kinds map[string]MemberKind) knode {
	n := knode{kind: inherited, path: path}
	if c, ok := canonPath(segs); ok && len(segs) > 0 {
		if k, listed := kinds[c]; listed {
			n.kind = k
		}
	}
	child := func(seg, p string, et *ir.Type) {
		s := append(append([]string(nil), segs...), seg)
		n.kids = append(n.kids, buildTree(et, s, p, n.kind, kinds))
	}
	switch {
	case t.Kind == ir.TypeStruct && t.Struct != nil:
		for _, f := range t.Struct.Fields {
			n.names = append(n.names, ir.NameKey(f.Name))
			p := f.Name
			if path != "" {
				p = path + "." + f.Name
			}
			child(f.Name, p, f.Type)
		}
	case t.Kind == ir.TypeArray && t.Elem != nil:
		n.lo = t.ArrLoBound
		for i := 0; i < t.ArrLen; i++ {
			idx := "[" + strconv.Itoa(t.ArrLoBound+i) + "]"
			child(idx, path+idx, t.Elem)
		}
	}
	if len(n.kids) == 0 {
		n.leaf = true
		n.hasRO = n.kind.readOnly()
		n.hasDel = n.kind.delivered()
		n.allDel = n.hasDel
		return n
	}
	n.allDel = true
	for i := range n.kids {
		k := &n.kids[i]
		n.hasRO = n.hasRO || k.hasRO
		n.hasDel = n.hasDel || k.hasDel
		n.allDel = n.allDel && k.allDel
	}
	return n
}

// addrSuffix is how a member path follows its root in an address.
func addrSuffix(p string) string {
	if strings.HasPrefix(p, "[") {
		return p
	}
	return "." + p
}

// memberType walks segs into t.
func memberType(t *ir.Type, segs []string) (*ir.Type, error) {
	cur := t
	for _, s := range segs {
		switch {
		case strings.HasPrefix(s, "["):
			if cur.Kind != ir.TypeArray {
				return nil, fmt.Errorf("%s indexes something that is not an array", s)
			}
			n, err := strconv.Atoi(strings.TrimSpace(strings.Trim(s, "[]")))
			if err != nil {
				return nil, fmt.Errorf("%s is not an index", s)
			}
			if n < cur.ArrLoBound || n >= cur.ArrLoBound+cur.ArrLen {
				return nil, fmt.Errorf("index %d is outside [%d..%d]", n, cur.ArrLoBound, cur.ArrLoBound+cur.ArrLen-1)
			}
			cur = cur.Elem
		default:
			if cur.Kind != ir.TypeStruct || cur.Struct == nil {
				return nil, fmt.Errorf("%s is not a member: its parent is not a STRUCT", s)
			}
			i, ok := fieldIndex(cur.Struct, s)
			if !ok {
				return nil, fmt.Errorf("type %s has no member %s", cur.Struct.Name, s)
			}
			cur = cur.Struct.Fields[i].Type
		}
	}
	return cur, nil
}

func fieldIndex(sd *ir.StructDef, name string) (int, bool) {
	if i, ok := sd.FieldIndex[name]; ok {
		return i, true
	}
	for i, f := range sd.Fields {
		if ir.SameName(f.Name, name) {
			return i, true
		}
	}
	return 0, false
}

// mergeLeaves returns base with every leaf p picks taken from src, matched
// by member name and position, never by StructDef identity; the result
// keeps base's shapes. ok is false when src's shape differs from the tree
// anywhere a picked leaf lies (another member list, another array length, a
// leaf of another kind), and the result is then base unchanged.
func mergeLeaves(n *knode, base, src ir.Value, p pick) (ir.Value, bool) {
	if !n.has(p) {
		return base, true
	}
	if n.leaf {
		if !n.takes(p) {
			return base, true
		}
		if src.Kind != base.Kind && base.Kind != ir.TypeVoid {
			return base, false
		}
		return src, true
	}
	var elems, from []ir.Value
	switch {
	case n.names != nil:
		if base.Kind != ir.TypeStruct || src.Kind != ir.TypeStruct || len(src.Fld) != len(n.kids) || len(base.Fld) != len(n.kids) {
			return base, false
		}
		if src.Struct != nil && src.Struct != base.Struct {
			for i, f := range src.Struct.Fields {
				if i >= len(n.names) || ir.NameKey(f.Name) != n.names[i] {
					return base, false
				}
			}
		}
		elems, from = base.Fld, src.Fld
	default:
		if base.Kind != ir.TypeArray || src.Kind != ir.TypeArray || len(src.Arr) != len(n.kids) || len(base.Arr) != len(n.kids) {
			return base, false
		}
		elems, from = base.Arr, src.Arr
	}
	out := make([]ir.Value, len(elems))
	copy(out, elems)
	for i := range n.kids {
		v, ok := mergeLeaves(&n.kids[i], elems[i], from[i], p)
		if !ok {
			return base, false
		}
		out[i] = v
	}
	res := base
	if n.names != nil {
		res.Fld = out
	} else {
		res.Arr = out
	}
	return res, true
}

// valueAt walks v along a key's node path, for the unchanged-leaf check.
func valueAt(in *instInfo, v ir.Value, segs []string) (ir.Value, bool) {
	n, cur := &in.root, v
	for _, s := range segs {
		c, ok := canonSeg(s)
		if !ok {
			return ir.Value{}, false
		}
		var i int
		if strings.HasPrefix(c, "[") {
			i, _ = strconv.Atoi(strings.Trim(c, "[]"))
			i -= n.lo
			if i < 0 || i >= len(cur.Arr) || i >= len(n.kids) {
				return ir.Value{}, false
			}
			cur = cur.Arr[i]
		} else {
			if i = slicesIndex(n.names, c); i < 0 || i >= len(cur.Fld) {
				return ir.Value{}, false
			}
			cur = cur.Fld[i]
		}
		n = &n.kids[i]
	}
	return cur, true
}

// sameLeaf reports whether a delivered scalar equals what the leaf holds,
// so an unchanged reading costs no copy. Conservative: only an exact kind
// match counts.
func sameLeaf(cur ir.Value, raw any) bool {
	switch x := raw.(type) {
	case bool:
		return cur.Kind == ir.TypeBool && cur.B == x
	case float64:
		return cur.Kind == ir.TypeReal && cur.F == x
	case int64:
		return cur.Kind == ir.TypeInt && cur.I == x
	case string:
		return cur.Kind == ir.TypeString && cur.S == x
	}
	return false
}

// deliverLocked lands one driver delivery's members of this instance: the
// whole struct under its name, then each member delivered under its own
// path. Only delivered leaves are taken, also below a key whose subtree
// holds a member of another kind. It merges onto the live value
// (writeBase), so a forced or substituted member keeps its force over what
// the device says, as a whole input does. Caller holds t.mu.
func (t *Tags) deliverLocked(in *instInfo, vals map[string]any) {
	cur, name, ok := t.lookupLocked(in.name)
	if !ok {
		return
	}
	base := t.writeBase(name, cur)
	nv, changed, seen, dropped := base, false, false, false
	if raw, ok := vals[in.name]; ok {
		seen, dropped = true, true
		if v, isIR := raw.(ir.Value); isIR {
			if m, ok := mergeLeaves(&in.root, nv, v, pickDelivered); ok {
				nv, changed, dropped = m, true, false
				for i := range in.got {
					in.got[i].Store(true)
				}
			}
		}
	}
	for i, k := range in.keys {
		raw, ok := vals[k.key]
		if !ok {
			continue
		}
		seen = true
		if k.node.leaf {
			if leaf, ok := valueAt(in, nv, k.segs); ok && sameLeaf(leaf, raw) {
				in.got[i].Store(true)
				continue
			}
		}
		cand, err := ir.SetFieldTyped(nv, in.typ, k.segs, deliveryValue(raw), "instance "+in.name)
		if err != nil {
			dropped = true
			continue
		}
		if !k.node.allDel {
			// A member of another kind lies below this key: take only the
			// delivered leaves.
			m, ok := mergeLeaves(&in.root, nv, cand, pickDelivered)
			if !ok {
				dropped = true
				continue
			}
			cand = m
		}
		nv, changed = cand, true
		in.got[i].Store(true)
	}
	if seen {
		in.dropped.Store(dropped)
		if dropped {
			in.drops.Add(1)
		}
	}
	if changed {
		t.writeLocked(name, nv)
	}
}

// deliveryValue unwraps a scalar ir.Value for SetFieldTyped, which takes
// the plain kinds a driver delivers.
func deliveryValue(raw any) any {
	if v, ok := raw.(ir.Value); ok && v.Kind != ir.TypeStruct && v.Kind != ir.TypeArray {
		return plain(v)
	}
	return raw
}

// instanceOf returns the instance a root name is, if it is one.
func (t *Tags) instanceOf(root string) *instInfo {
	if len(t.inst) == 0 {
		return nil
	}
	return t.inst[ir.NameKey(root)]
}

// instanceWritable is the front door's kind check: nil, or an ErrReadOnly
// naming the member and why. A write that names members (an object) is
// checked member by member; any other value at a path is checked for every
// leaf it would set.
func (t *Tags) instanceWritable(path string, value any) error {
	root, segs := ir.SplitAddress(path)
	in := t.instanceOf(root)
	if in == nil {
		return nil
	}
	if m, ok := value.(map[string]any); ok {
		for _, p := range flattenWrite(m) {
			if err := in.writable(append(append([]string(nil), segs...), ir.PathSegments(p)...)); err != nil {
				return err
			}
		}
		return nil
	}
	if len(segs) == 0 {
		return fmt.Errorf("instance %s: write its members by name (an object), not a whole value: some are read-only: %w", in.name, ErrReadOnly)
	}
	return in.writable(segs)
}

// writable refuses a path that is, holds or lies under a read-only member.
// A path the type does not have is left to the write's own resolution,
// which refuses it.
func (in *instInfo) writable(segs []string) error {
	chain, ok := in.walk(segs)
	for _, n := range chain[1:] {
		if n.leaf && n.kind.readOnly() {
			return in.readOnlyErr(n)
		}
	}
	if !ok {
		return nil
	}
	if n := chain[len(chain)-1]; n.hasRO {
		return in.readOnlyErr(firstRO(n))
	}
	return nil
}

func firstRO(n *knode) *knode {
	if n.leaf {
		return n
	}
	for i := range n.kids {
		if n.kids[i].hasRO {
			return firstRO(&n.kids[i])
		}
	}
	return n
}

func (in *instInfo) readOnlyErr(n *knode) error {
	what := "read from the device (from:): its driver sets it each poll"
	if n.kind == MemberExpr {
		what = "computed each scan (expr:)"
	}
	return fmt.Errorf("%s%s is %s, so nothing outside writes it: %w", in.name, addrSuffix(n.path), what, ErrReadOnly)
}

// flattenWrite lists the member paths a nested write object names.
func flattenWrite(m map[string]any) []string {
	var out []string
	var walk func(prefix string, m map[string]any)
	walk = func(prefix string, m map[string]any) {
		for k, v := range m {
			p := k
			if prefix != "" {
				p = prefix + "." + k
			}
			if sub, ok := v.(map[string]any); ok && len(sub) > 0 {
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

// stripReadOnly puts back the read-only leaves of an instance a program
// committed: whatever it assigned, from: and expr: members keep what the
// store holds. A committed value of another shape lands nothing.
func (in *instInfo) stripReadOnly(committed, stored ir.Value) ir.Value {
	if !in.root.hasRO {
		return committed
	}
	if v, ok := mergeLeaves(&in.root, committed, stored, pickReadOnly); ok {
		return v
	}
	return stored
}

// installCarried writes a value a pair's image carried (a hot takeover).
// An instance takes from it every leaf but the delivered ones: like an
// input, a from: or both: member is the new leader's to read on its first
// scan, never the old leader's last reading. Caller holds no lock.
func (t *Tags) installCarried(name string, v ir.Value) {
	in := t.instanceOf(name)
	if in == nil {
		_ = t.WriteGlobal(name, v)
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	cur, key, ok := t.lookupLocked(name)
	if !ok {
		return
	}
	if m, ok := mergeLeaves(&in.root, t.writeBase(key, cur), v, pickNotDelivered); ok {
		t.writeLocked(key, m)
	}
}

// memberSubstituted reports whether a substitute covers the member at segs
// of root (the whole tag, the member, or something inside it).
func (t *Tags) memberSubstituted(in *instInfo, segs []string) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	_, key, ok := t.lookupLocked(in.name)
	if !ok {
		return false
	}
	ss := t.subs[key]
	if ss == nil {
		return false
	}
	if len(segs) == 0 {
		return len(ss.entries) > 0 // the root: any of its members
	}
	p, ok := canonPath(segs)
	if !ok {
		return false
	}
	for _, e := range ss.entries {
		if len(e.path) == 0 {
			return true
		}
		if ep, ok := canonPath(e.path); ok && (pathUnder(p, ep) || pathUnder(ep, p)) {
			return true
		}
	}
	return false
}

// pathUnder reports whether path is p or lies beneath it.
func pathUnder(path, p string) bool {
	return path == p || strings.HasPrefix(path, p+".") || strings.HasPrefix(path, p+"[")
}

// instanceQuality rolls an instance's delivered members up into its root
// (the device library's rule): NotConnected while none has arrived, Bad if
// any is Bad or some have not arrived, else Stale, else Substituted.
func instanceQuality(qs []nio.Quality) (nio.Quality, bool) {
	if len(qs) == 0 {
		return nio.Good, false
	}
	notConn, stale, bad, subst := 0, false, false, false
	for _, q := range qs {
		switch q {
		case nio.Good:
		case nio.NotConnected:
			notConn++
		case nio.Stale:
			stale = true
		case nio.Substituted:
			subst = true
		default:
			bad = true
		}
	}
	switch {
	case notConn == len(qs):
		return nio.NotConnected, true
	case bad || notConn > 0:
		return nio.Bad, true
	case stale:
		return nio.Stale, true
	case subst:
		return nio.Substituted, true
	}
	return nio.Good, false
}

// instRoll is one instance's qualities: each delivered key's, and the
// root's.
type instRoll struct {
	members []nio.Quality // by key
	root    nio.Quality
}

// rollInstance computes one instance's qualities from what the driver said
// (said may be nil): a key the driver said nothing about takes the
// driver's verdict on the root, else Stale while reads fail or on a
// standby, else NotConnected until it has been delivered once.
func (r *Runtime) rollInstance(in *instInfo, said map[string]nio.Quality, standby, readOK bool) instRoll {
	rootQ, rootSaid := said[in.name]
	roll := instRoll{members: make([]nio.Quality, len(in.keys))}
	qs := make([]nio.Quality, 0, len(in.keys)+2)
	for i, k := range in.keys {
		q, ok := said[k.key]
		switch {
		case ok:
		case rootSaid:
			q = rootQ
		case standby || !readOK:
			q = nio.Stale
		case !in.got[i].Load():
			q = nio.NotConnected
		default:
			q = nio.Good
		}
		roll.members[i] = q
		qs = append(qs, q)
	}
	if rootSaid {
		qs = append(qs, rootQ)
	} else if len(in.keys) == 0 && (standby || !readOK) {
		qs = append(qs, nio.Stale)
	}
	if in.dropped.Load() {
		qs = append(qs, nio.Bad)
	}
	roll.root, _ = instanceQuality(qs)
	return roll
}

// driverSaid is what the driver reports, with its Good and Substituted
// dropped on a standby (they do not stand there; see Quality).
func (r *Runtime) driverSaid(standby bool) map[string]nio.Quality {
	qr, ok := r.driver.(nio.QualityReporter)
	if !ok {
		return nil
	}
	said := qr.Quality()
	if standby {
		for k, q := range said {
			if q == nio.Good || q == nio.Substituted {
				delete(said, k)
			}
		}
	}
	return said
}

// instanceQualities adds every instance's member and root qualities to
// out, which holds what the driver said on entry. Returns out, allocated
// if it had to be.
func (r *Runtime) instanceQualities(out map[string]nio.Quality, standby bool) map[string]nio.Quality {
	readOK := r.readOK.Load()
	set := func(k string, q nio.Quality) {
		if out == nil {
			out = make(map[string]nio.Quality)
		}
		out[k] = q
	}
	for _, in := range r.tags.instList {
		roll := r.rollInstance(in, out, standby, readOK)
		for i, k := range in.keys {
			if q := roll.members[i]; q != nio.Good {
				set(k.key, q)
			}
		}
		if roll.root != nio.Good {
			set(in.name, roll.root)
		}
	}
	return out
}

// instanceTagQuality is TagQuality for an instance or one of its members.
// A member that is not delivered is Good (Stale on a standby for a command
// or an expression, which the scan refreshes); a delivered one is its key's
// quality, or the roll-up of the keys beneath it; the root is the roll-up.
// A substitute covering the address makes a Good member Substituted.
func (r *Runtime) instanceTagQuality(in *instInfo, segs []string, standby bool) nio.Quality {
	roll := r.rollInstance(in, r.driverSaid(standby), standby, r.readOK.Load())
	q := roll.root
	if len(segs) > 0 {
		chain, ok := in.walk(segs)
		n := chain[len(chain)-1]
		switch {
		case !ok:
			return nio.Good
		case !n.hasDel:
			q = nio.Good
			if standby && (n.kind == MemberTo || n.kind == MemberExpr) {
				q = nio.Stale
			}
		default:
			p, _ := canonPath(segs)
			var qs []nio.Quality
			for i, k := range in.keys {
				kp, _ := canonPath(k.segs)
				if pathUnder(p, kp) || pathUnder(kp, p) {
					qs = append(qs, roll.members[i])
				}
			}
			q, _ = instanceQuality(qs)
		}
	}
	if !standby && q == nio.Good && r.tags.memberSubstituted(in, segs) {
		q = nio.Substituted
	}
	return q
}

// substitutableAt reports whether addr, under root, may take a substitute:
// a driver-bound input, or a member of an instance every leaf of which is
// delivered. Every gate that admits a substitute (start, validate, the
// retain load, the durable and intent paths) asks this one question, so an
// instance member's substitute survives a restart and a takeover the way an
// input's does.
func (r *Runtime) substitutableAt(addr, root string) bool {
	if r.isInput(root) {
		return true
	}
	in := r.tags.instanceOf(root)
	if in == nil {
		return false
	}
	_, segs := ir.SplitAddress(addr)
	if len(segs) == 0 {
		return false
	}
	chain, ok := in.walk(segs)
	return ok && chain[len(chain)-1].allDel
}

// substitutableRoot reports whether a substitutes: rule may name root: an
// input, or an instance with a delivered member.
func (r *Runtime) substitutableRoot(name string) (string, bool) {
	for _, in := range r.inputs {
		if ir.SameName(in, name) {
			return in, true
		}
	}
	if in := r.tags.instanceOf(name); in != nil && in.root.hasDel {
		return in.name, true
	}
	return "", false
}

// registerInstances builds the store's view of every instance in o.Tags and
// returns their names. Caller is New, before any scan.
func (t *Tags) registerInstances(o Options, types map[string]*ir.Type) ([]string, error) {
	var names []string
	flat := map[string]bool{}
	for _, n := range append(append([]string(nil), o.Inputs...), o.Outputs...) {
		flat[ir.NameKey(n)] = true
	}
	retained := map[string]bool{}
	for _, n := range o.RetainTags {
		retained[ir.NameKey(n)] = true
	}
	for _, d := range o.Tags {
		if d.Instance == nil || d.Name == "" {
			continue
		}
		if d.Role != RoleState {
			return nil, fmt.Errorf("instance %s: an instance has no role of its own (declare it with runtime.InstanceTag)", d.Name)
		}
		if d.Retain || retained[ir.NameKey(d.Name)] {
			return nil, fmt.Errorf("instance %s: an instance is never retained whole: a restart must not bring back a command or an old reading (memory members get their own retention in a later slice)", d.Name)
		}
		if flat[ir.NameKey(d.Name)] {
			return nil, fmt.Errorf("instance %s is also a driver input or output: an instance is delivered member by member, never whole", d.Name)
		}
		in, err := newInstInfo(d, types)
		if err != nil {
			return nil, err
		}
		if t.inst == nil {
			t.inst = map[string]*instInfo{}
		}
		if _, dup := t.inst[ir.NameKey(d.Name)]; dup {
			return nil, fmt.Errorf("instance %s is declared twice", d.Name)
		}
		t.inst[ir.NameKey(d.Name)] = in
		t.instList = append(t.instList, in)
		names = append(names, d.Name)
	}
	return names, nil
}

// instanceSubstitutedNames lists the instance member addresses a substitute
// stands in for, as each was started ("P101.Speed").
func (t *Tags) instanceSubstitutedNames() []string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	var out []string
	for _, in := range t.instList {
		_, key, ok := t.lookupLocked(in.name)
		if !ok {
			continue
		}
		if ss := t.subs[key]; ss != nil {
			for _, e := range ss.entries {
				if len(e.path) > 0 {
					out = append(out, e.Name)
				}
			}
		}
	}
	return out
}
