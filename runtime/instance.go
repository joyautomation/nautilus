package runtime

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

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
//   - A delivery lands only the from: and both: members, merged onto the
//     stored value, so memory and commands are never touched by a driver.
//   - A write from outside the programs (the API, an OPC UA client, a
//     Sparkplug command) is refused for a from: or expr: member, by the one
//     front door they all use (Tags.Write).
//   - A program's commit never lands a from: or expr: member, even if
//     something assigned one: the check refuses that at load (a later
//     slice), and the commit is the last line.
//   - Forces, substitutes and a test's given: are not outside writes: they
//     may set from: and both: members, which is what they are for.
//   - Quality is per member, and the root shows the worst of its delivered
//     members (the device library's rule).
//
// Experimental, like Image: the shape may change while struct wiring
// (#1458) lands slice by slice. What is not here yet: commands are not pushed (to: members reach no driver
// until the push rules land), memory is not retained, and expressions are not
// evaluated. An instance is a RoleState tag, so it is seeded, kept only by
// the hot takeover image, and is no driver input or output.

// MemberKind is what one member of an instance's value comes from.
// Experimental (see the package's instance notes).
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

// MemberDef is one member's wiring, as the runtime needs it.
type MemberDef struct {
	Kind MemberKind
	// Retained marks a memory member kept across a restart (not yet used).
	Retained bool
	// Readback names the member that shows what the device did with a
	// MemberTo, for the HMI.
	Readback string
	// Event marks a MemberTo that must never be sent twice.
	Event bool
	Unit  string
	Desc  string
}

// InstanceDef is what makes a tag an instance: its wiring's name and its
// members' kinds. Experimental, as Instance is.
type InstanceDef struct {
	Wiring  string
	Version string
	// Params are the instance's values for its wiring's params.
	Params map[string]string
	// Members are keyed by member path: "Speed", "Cmd.Start", "Levels[1]". A
	// path takes the kind of its own entry, else of its nearest listed
	// ancestor, else MemberMemory.
	Members map[string]MemberDef
}

// Instance declares a tag of a wired type. typeName is the project's TYPE
// (a STRUCT); the tag is seeded at its zero, or at Init. Experimental while
// struct wiring (#1458) lands: commands are not yet pushed to a driver,
// memory is not yet retained and expressions are not yet evaluated.
//
// A driver delivers an instance's from: and both: members either one by one,
// under the member's full path ("P101.Speed", which mixes freely across the
// children of an io.Multi), or as the whole struct under the instance's name,
// from which only those members are taken. A value of another type, or a
// member the instance does not have, is not delivered.
func Instance(name, typeName string, def InstanceDef, opts ...TagOpt) TagDef {
	d := newDef(name, RoleState, nil, opts)
	d.Type = typeName
	d.Instance = &def
	return d
}

// instInfo is one instance as the store uses it.
type instInfo struct {
	name string // the stored spelling
	typ  *ir.Type
	def  *InstanceDef
	// kinds by pathKey, from def.Members.
	kinds map[string]MemberKind
	// delivered marks the from: and both: leaves; readOnly the from: and
	// expr: ones. Shaped like the type, for writeMask.merge.
	delivered, readOnly writeMask
	// keys are the delivery keys a driver may use, one per delivered entry
	// ("P101.Speed"), with the path below the root.
	keys []instKey
	// roPaths are the read-only entries' pathKeys, for the front door, and
	// roNames their paths as declared, for its messages.
	roPaths []string
	roNames map[string]string
}

type instKey struct {
	key  string   // "P101.Speed" as delivered
	segs []string // the path below the root, for SetFieldTyped
	path string   // pathKey(segs)
}

// pathKey is a member path's canonical spelling: names folded like every IEC
// identifier, indexes as written.
func pathKey(segs []string) string {
	var b strings.Builder
	for i, s := range segs {
		if strings.HasPrefix(s, "[") {
			b.WriteString(strings.ReplaceAll(s, " ", ""))
			continue
		}
		if i > 0 {
			b.WriteByte('.')
		}
		b.WriteString(ir.NameKey(s))
	}
	return b.String()
}

// pathUnder reports whether path is p or lies beneath it.
func pathUnder(path, p string) bool {
	return path == p || strings.HasPrefix(path, p+".") || strings.HasPrefix(path, p+"[")
}

// kindAt is the kind of the member at segs.
func (in *instInfo) kindAt(segs []string) MemberKind {
	for n := len(segs); n > 0; n-- {
		if k, ok := in.kinds[pathKey(segs[:n])]; ok {
			return k
		}
	}
	return MemberMemory
}

// newInstInfo validates an instance's definition against its type and
// builds what the store needs.
func newInstInfo(d TagDef, types map[string]*ir.Type) (*instInfo, error) {
	if d.Role != RoleState {
		return nil, fmt.Errorf("instance %s: an instance has no role of its own (declare it with runtime.Instance)", d.Name)
	}
	if d.Type == "" {
		return nil, fmt.Errorf("instance %s: an instance needs its TYPE", d.Name)
	}
	t, err := ResolveTagType(d.Type, types)
	if err != nil {
		return nil, fmt.Errorf("instance %s: %w", d.Name, err)
	}
	if t.Kind != ir.TypeStruct {
		return nil, fmt.Errorf("instance %s: type %s is not a STRUCT: an instance is a struct whose members are wired", d.Name, d.Type)
	}
	in := &instInfo{name: d.Name, typ: t, def: d.Instance, kinds: map[string]MemberKind{}, roNames: map[string]string{}}
	paths := make([]string, 0, len(d.Instance.Members))
	for p := range d.Instance.Members {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		m := d.Instance.Members[p]
		segs := ir.PathSegments(p)
		if _, err := memberType(t, segs); err != nil {
			return nil, fmt.Errorf("instance %s: member %s: %w", d.Name, p, err)
		}
		if m.Kind > MemberExpr {
			return nil, fmt.Errorf("instance %s: member %s: %v is not a member kind", d.Name, p, m.Kind)
		}
		k := pathKey(segs)
		if _, dup := in.kinds[k]; dup {
			return nil, fmt.Errorf("instance %s: member %s is listed twice", d.Name, p)
		}
		in.kinds[k] = m.Kind
		if m.Event && m.Kind != MemberTo {
			return nil, fmt.Errorf("instance %s: member %s: Event is for a MemberTo, and this one is %s", d.Name, p, m.Kind)
		}
	}
	for _, p := range paths {
		m := d.Instance.Members[p]
		if m.Readback == "" {
			continue
		}
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
	for _, p := range paths {
		segs := ir.PathSegments(p)
		switch k := in.kinds[pathKey(segs)]; {
		case k.delivered():
			in.keys = append(in.keys, instKey{key: d.Name + addrSuffix(p), segs: segs, path: pathKey(segs)})
		}
		if in.kinds[pathKey(segs)].readOnly() {
			in.roPaths = append(in.roPaths, pathKey(segs))
			in.roNames[pathKey(segs)] = p
		}
	}
	in.delivered = buildMask(t, nil, func(s []string) bool { return in.kindAt(s).delivered() })
	in.readOnly = buildMask(t, nil, func(s []string) bool { return in.kindAt(s).readOnly() })
	return in, nil
}

// addrSuffix is how a member path follows its root in an address: ".Speed",
// or "[2].Mode" for a path that starts at an index.
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
			n, err := strconv.Atoi(strings.Trim(s, "[] "))
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

// buildMask marks every leaf of t (below prefix) for which pick is true. A
// node all of whose leaves are picked is marked whole.
func buildMask(t *ir.Type, prefix []string, pick func([]string) bool) writeMask {
	var kids []writeMask
	step := func(seg string, et *ir.Type) {
		kids = append(kids, buildMask(et, append(append([]string(nil), prefix...), seg), pick))
	}
	switch {
	case t.Kind == ir.TypeStruct && t.Struct != nil:
		for _, f := range t.Struct.Fields {
			step(f.Name, f.Type)
		}
	case t.Kind == ir.TypeArray && t.Elem != nil:
		for i := 0; i < t.ArrLen; i++ {
			step("["+strconv.Itoa(t.ArrLoBound+i)+"]", t.Elem)
		}
	default:
		return writeMask{all: pick(prefix)}
	}
	all, none := true, true
	for i := range kids {
		all = all && kids[i].all
		none = none && !kids[i].marked()
	}
	switch {
	case len(kids) == 0:
		return writeMask{all: pick(prefix)}
	case all:
		return writeMask{all: true}
	case none:
		return writeMask{}
	}
	return writeMask{any: true, kids: kids}
}

// sameShape reports whether v is a value of the instance's own type, so a
// mask built from that type applies to it.
func (in *instInfo) sameShape(v ir.Value) bool {
	return v.Kind == ir.TypeStruct && in.typ.Struct != nil && v.Struct == in.typ.Struct && len(v.Fld) == len(in.typ.Struct.Fields)
}

// deliverLocked lands one driver delivery's members of this instance:
// the whole struct under its name (only its delivered members are taken),
// then each member delivered under its own path. It merges onto the live
// value (writeBase), so a forced or substituted member keeps its force over
// what the device says, as a whole input does. Caller holds t.mu.
func (t *Tags) deliverLocked(in *instInfo, vals map[string]any) {
	cur, name, ok := t.lookupLocked(in.name)
	if !ok {
		return
	}
	base := t.writeBase(name, cur)
	nv, changed := base, false
	if raw, ok := vals[in.name]; ok {
		if v, isIR := raw.(ir.Value); isIR && in.sameShape(v) && in.sameShape(nv) {
			nv, changed = in.delivered.merge(nv, v), true
		}
	}
	for _, k := range in.keys {
		raw, ok := vals[k.key]
		if !ok {
			continue
		}
		v, err := ir.SetFieldTyped(nv, in.typ, k.segs, deliveryValue(raw), "instance "+in.name)
		if err != nil {
			continue // a value the member cannot hold is not delivered
		}
		nv, changed = v, true
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
// naming the member and why. A whole-tag write to an instance is a merge of
// the members it names, so each named member is checked; a whole value that
// names none (a struct, not a mapping) is refused, since it would set them
// all.
func (t *Tags) instanceWritable(path string, value any) error {
	root, segs := ir.SplitAddress(path)
	in := t.instanceOf(root)
	if in == nil {
		return nil
	}
	if len(segs) == 0 {
		m, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("instance %s: write its members by name (an object), not a whole value: some are read-only: %w", in.name, ErrReadOnly)
		}
		for _, p := range flattenWrite(m) {
			if err := in.writable(ir.PathSegments(p)); err != nil {
				return err
			}
		}
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
	return in.writable(segs)
}

// writable refuses a path that is, holds or lies under a read-only member.
func (in *instInfo) writable(segs []string) error {
	p := pathKey(segs)
	for _, ro := range in.roPaths {
		if pathUnder(p, ro) || pathUnder(ro, p) {
			what := "read from the device (from:): its driver sets it each poll"
			if in.kinds[ro] == MemberExpr {
				what = "computed each scan (expr:)"
			}
			return fmt.Errorf("%s%s is %s, so nothing outside writes it: %w", in.name, addrSuffix(in.roNames[ro]), what, ErrReadOnly)
		}
	}
	return nil
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

// stripReadOnly puts back the read-only members of an instance a program
// committed: whatever it assigned, from: and expr: members keep what the
// store holds. Caller holds t.mu.
func (in *instInfo) stripReadOnly(committed, stored ir.Value) ir.Value {
	switch {
	case !in.readOnly.marked():
		return committed
	case !in.sameShape(committed) || !in.sameShape(stored):
		// A value of another shape would replace every member, the
		// read-only ones included; the store keeps what it holds.
		return stored
	}
	return in.readOnly.merge(committed, stored)
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

// registerInstances builds the store's view of every instance in o.Tags and
// returns their names. Caller is New, before any scan.
func (t *Tags) registerInstances(o Options, types map[string]*ir.Type) ([]string, error) {
	var names []string
	flat := map[string]bool{}
	for _, n := range append(append([]string(nil), o.Inputs...), o.Outputs...) {
		flat[ir.NameKey(n)] = true
	}
	for _, d := range o.Tags {
		if d.Instance == nil || d.Name == "" {
			continue
		}
		if d.Retain {
			return nil, fmt.Errorf("instance %s: an instance keeps its memory members by their own Retained flag, not the tag's Retain", d.Name)
		}
		if d.Role != RoleState {
			return nil, fmt.Errorf("instance %s: an instance has no role of its own (declare it with runtime.Instance)", d.Name)
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

// instanceQualities adds every instance's member and root quality to out,
// over what the driver said (out on entry): a delivered member the driver
// said nothing about is Stale while the last read failed, and the root is
// the roll-up of its delivered members and of anything said about the root
// itself. Returns out, allocated if it had to be.
func (r *Runtime) instanceQualities(out map[string]nio.Quality, standby bool) map[string]nio.Quality {
	readOK := r.readOK.Load()
	set := func(k string, q nio.Quality) {
		if out == nil {
			out = make(map[string]nio.Quality)
		}
		out[k] = q
	}
	for _, in := range r.tags.instList {
		qs := make([]nio.Quality, 0, len(in.keys)+1)
		for _, k := range in.keys {
			q, said := out[k.key]
			if !said && (!readOK || standby) {
				q = nio.Stale
				set(k.key, q)
			}
			qs = append(qs, q)
		}
		if q, said := out[in.name]; said {
			qs = append(qs, q)
		} else if len(in.keys) == 0 && (!readOK || standby) {
			qs = append(qs, nio.Stale) // filled whole, under its own name
		}
		if q, notGood := instanceQuality(qs); notGood {
			set(in.name, q)
		}
	}
	return out
}

// instanceTagQuality is TagQuality for an instance or one of its members:
// a member that is not delivered (memory, a command, an expression) is
// Good; a delivered one is what the driver says of it, else of the member
// that holds it, else of the root, else Stale while reads fail or on a
// standby. The root is the roll-up.
func (r *Runtime) instanceTagQuality(in *instInfo, segs []string, standby bool) nio.Quality {
	var said map[string]nio.Quality
	if qr, ok := r.driver.(nio.QualityReporter); ok {
		said = qr.Quality()
	}
	if standby {
		// A driver's Good and Substituted do not stand on a standby.
		for k, q := range said {
			if q == nio.Good || q == nio.Substituted {
				delete(said, k)
			}
		}
	}
	if len(segs) == 0 {
		qs := r.instanceQualities(copyQualities(said), standby)
		if q, ok := qs[in.name]; ok {
			return q
		}
		return nio.Good
	}
	if !in.kindAt(segs).delivered() {
		return nio.Good
	}
	p := pathKey(segs)
	for _, k := range in.keys {
		if pathUnder(p, k.path) {
			if q, ok := said[k.key]; ok {
				return q
			}
		}
	}
	if q, ok := said[in.name]; ok {
		return q
	}
	if standby || !r.readOK.Load() {
		return nio.Stale
	}
	return nio.Good
}

func copyQualities(m map[string]nio.Quality) map[string]nio.Quality {
	if m == nil {
		return nil
	}
	out := make(map[string]nio.Quality, len(m))
	for k, q := range m {
		out[k] = q
	}
	return out
}

// substitutableAt reports whether addr, under root, may take a substitute:
// a driver-bound input, or a delivered member of an instance (a from: or
// both: member, or a member inside one). Every gate that admits a
// substitute (start, validate, retain load, the durable and intent paths)
// asks this one question, so an instance member's substitute survives a
// restart and a takeover the way an input's does.
func (r *Runtime) substitutableAt(addr, root string) bool {
	if r.isInput(root) {
		return true
	}
	in := r.tags.instanceOf(root)
	if in == nil {
		return false
	}
	_, segs := ir.SplitAddress(addr)
	return len(segs) > 0 && in.kindAt(segs).delivered()
}

// substitutableRoot reports whether a substitutes: rule may name root: an
// input, or an instance with a delivered member.
func (r *Runtime) substitutableRoot(name string) (string, bool) {
	for _, in := range r.inputs {
		if ir.SameName(in, name) {
			return in, true
		}
	}
	if in := r.tags.instanceOf(name); in != nil && len(in.keys) > 0 {
		return in.name, true
	}
	return "", false
}
