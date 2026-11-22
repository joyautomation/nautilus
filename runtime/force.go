package runtime

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/joyautomation/nautilus/lang/ir"
)

// Forcing — the PLC force table.
//
// A FORCE holds a tag (or one member of a struct tag) at a value until it is
// removed, whatever the field and the logic say. It is the store, not the
// scan loop, that enforces it, which is what makes one mechanism give every
// controller's two force behaviours at once:
//
//   - A forced INPUT: the driver's delivery each scan (setMany) is captured
//     as the tag's ACTUAL value and the forced value is stored in its place,
//     so the program's snapshot reads the forced value — Logix's input force,
//     TIA's force of an I address.
//   - A forced OUTPUT (or state/setpoint) tag: the program's commit at the end
//     of the scan is captured as the actual value and the forced value is
//     re-applied over it, so the output push hands the driver the forced
//     value whatever the logic computed. Inside the scan the program sees its
//     own write, as it would on a PLC whose output force is applied to the
//     output image after the logic.
//
// Every writer — driver delivery, program commit, operator SetPath, retained
// restore — funnels through writeLocked, so there is no path around a force.
//
// # Change detection
//
// A forced tag's STORED value is the forced value, and it goes through the
// same sameValue/generation stamp as any other write: applying a force, or
// changing a force's value, is one change (one generation); re-applying the
// same forced value every scan over a moving input is not a change at all —
// the store does not move, the SSE stream sends nothing, the output push
// stays quiet. Removing a force restores the actual value (the last thing the
// driver delivered or the logic wrote while the force held), which is a
// change again if it differs. See Tags' "Write generations".
//
// # Lifetime
//
// Forces live in this process's tag store and nowhere else: they are not
// retained (a restart comes up unforced — deliberately unlike Logix, which
// keeps forces in the project; a controller that boots holding stale forces
// is a commissioning hazard, and a nautilus restart is usually a deploy) and
// they are not replicated (a redundancy takeover drops them — see
// Runtime.takeover — because the standby never saw them, and a flapping
// leader must not resurrect forces nobody is looking at).

// Force is one entry of the force table, as the API reports it.
type Force struct {
	// Name is the forced address: a tag ("StartPB") or a member path
	// ("P101.Speed").
	Name string `json:"name"`
	// Value is the forced value, in the plain JSON form All() uses.
	Value any `json:"value"`
	// Actual is what the tag would hold without the force — the driver's
	// latest delivery for an input, the logic's latest write for an output —
	// at this address. Absent when nothing has been written since the force.
	Actual any `json:"actual,omitempty"`
	// SinceMs is when the force was applied (or last changed), epoch ms.
	SinceMs int64 `json:"sinceMs"`
}

// forceSet is every force on ONE root tag.
type forceSet struct {
	// entries in application order: a whole-tag force (path nil) first,
	// then member forces, so a member force refines a whole one.
	entries []forceEntry
	// actual is the value the unforced writers last asked to store — what
	// the tag returns to when the last force is removed.
	actual ir.Value
	// wrote is whether anything has been written since the force was
	// applied (the reported Actual is only interesting once it has).
	wrote bool
	// typ is the root tag's declared type (nil when none), so a member
	// force's [n] counts from the array's declared lower bound.
	typ *ir.Type
}

type forceEntry struct {
	name    string   // the address as given ("P101.Speed")
	path    []string // member path below the root; nil = the whole tag
	val     ir.Value // the forced value, already coerced to the member's type
	sinceMs int64
	// gen is which force this is: a number nothing else has, stamped each time
	// the entry is set. A caller that must remove only the force it put there
	// (an I/O checkout, #719) keeps it and asks UnforceIf, so a force someone
	// else set or changed since is left alone.
	gen uint64
}

// apply lays every force of the set over v and returns the result. Forces
// were validated (and coerced) when applied, so an error here means the tag
// changed shape underneath them — the force is skipped rather than the write
// lost.
func (fs *forceSet) apply(v ir.Value) ir.Value {
	for i := range fs.entries {
		e := &fs.entries[i]
		if nv, err := ir.SetFieldTyped(v, fs.typ, e.path, e.val, ""); err == nil {
			v = nv
		}
	}
	return v
}

// resolveForceAddr splits a force address into its root tag and member path,
// with SetPath's rule that a tag whose own NAME contains a dot wins. Caller
// holds t.mu.
func (t *Tags) resolveForceAddr(addr string) (string, []string, *tagVal, error) {
	if addr == "" {
		return "", nil, nil, fmt.Errorf("no tag name")
	}
	if tv, key, ok := t.lookupLocked(addr); ok {
		return key, nil, tv, nil
	}
	root, segs := ir.SplitAddress(addr)
	tv, root, ok := t.lookupLocked(root)
	if !ok || len(segs) == 0 {
		return "", nil, nil, &UndefinedTagError{root}
	}
	return root, declaredPath(tv.v, segs), tv, nil
}

// declaredPath respells a member path as the struct declares it (member
// names are case-insensitive), so a force entry is named one way however
// the operator typed it. An index segment ([n]) steps into the element. A
// segment that names nothing is left as typed — SetFieldTyped reports it.
func declaredPath(v ir.Value, path []string) []string {
	out := make([]string, len(path))
	copy(out, path)
	for i, seg := range out {
		if strings.HasPrefix(seg, "[") {
			// one spelling of an index: "[01]" and "[+1]" name element 1, kept
			// as "[1]", so the force is found (and its overlaps seen) however
			// it was typed
			if strings.HasSuffix(seg, "]") {
				if n, err := strconv.Atoi(strings.TrimSpace(seg[1 : len(seg)-1])); err == nil {
					out[i] = "[" + strconv.Itoa(n) + "]"
				}
			}
			if v.Kind != ir.TypeArray || len(v.Arr) == 0 {
				break
			}
			v = v.Arr[0] // every element has the same shape
			continue
		}
		if v.Kind != ir.TypeStruct || v.Struct == nil {
			break
		}
		j, ok := v.Struct.FieldOf(seg)
		if !ok || j >= len(v.Fld) {
			break
		}
		out[i] = v.Struct.Fields[j].Name
		v = v.Fld[j]
	}
	return out
}

// Force holds a tag — or one member of a struct tag, by dotted path — at v
// until Unforce. v is checked and converted exactly as a Write would be:
// a value the declared type cannot hold (out of an INT's range, a string
// for a REAL, NaN, a number that is no enumeration member) is refused with
// the same error, so an operator's force can never put a value in the
// store that a write could not. Forcing an address that is
// already forced changes its value.
func (t *Tags) Force(addr string, v any) error {
	return t.force(addr, v, true)
}

// ForceLenient is Force with the lenient conversion retain and given: use:
// an acceptance test's force: may hold a tag at a value the program itself
// can produce, such as an INT counted past 32767, which the VM does not
// wrap.
func (t *Tags) ForceLenient(addr string, v any) error {
	return t.force(addr, v, false)
}

func (t *Tags) force(addr string, v any, strict bool) error {
	_, _, err := t.forceGen(addr, v, strict, false)
	return err
}

// forceKeyLocked is addr as the force table keys it: the root tag in the
// store's spelling, members as the struct declares them, one "[n]" per index
// (spaces, a sign, leading zeros and "[i,j]" all reduced to it). It is the
// one place an address becomes a key: every function of the table that takes
// an address (set, remove, look up, overlap) goes through it, so two spellings
// of one element are one entry, never two and never one with two names. An
// address that resolves to no tag is its own key. The caller holds t.mu.
func (t *Tags) forceKeyLocked(addr string) string {
	if root, path, _, err := t.resolveForceAddr(addr); err == nil {
		return ir.JoinPath(root, path)
	}
	return addr
}

// ForceKey is the name the table keeps a force on addr under (forceKeyLocked).
func (t *Tags) ForceKey(addr string) string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.forceKeyLocked(addr)
}

// ErrAlreadyForced is what ForceIfFree answers for an address (or a member
// or the whole tag around it) that is forced already.
var ErrAlreadyForced = fmt.Errorf("already forced")

// ForceIfFree is Force that refuses, atomically, an address already forced
// (by itself, a member of it or the tag around it), and returns the force's
// generation for UnforceIf and the name the table keeps it under (the address
// as the table spells it: "M[1][2]" for "M[1,2]", the struct's own member
// names): ForceGen and UnforceIf take that name. Nothing about Force changes.
func (t *Tags) ForceIfFree(addr string, v any) (uint64, string, error) {
	return t.forceGen(addr, v, true, true)
}

// ForceGen is the generation of the force on exactly addr, if there is one.
func (t *Tags) ForceGen(addr string) (uint64, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	key := ir.NameKey(t.forceKeyLocked(addr))
	for _, fs := range t.forces {
		for i := range fs.entries {
			if ir.NameKey(fs.entries[i].name) == key {
				return fs.entries[i].gen, fs.entries[i].gen != 0
			}
		}
	}
	return 0, false
}

// ForceHolds reports whether the force with that generation is still in the
// table (by generation alone: unique store-wide, and not dependent on how a
// name resolves).
func (t *Tags) ForceHolds(gen uint64) bool {
	if gen == 0 {
		return false
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	for _, fs := range t.forces {
		for i := range fs.entries {
			if fs.entries[i].gen == gen {
				return true
			}
		}
	}
	return false
}

// UnforceIf removes the force on addr only if it is still the one with that
// generation, atomically, and reports whether it did. A force set or changed
// meanwhile has another generation and is left alone.
func (t *Tags) UnforceIf(addr string, gen uint64) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	// by generation: unique store-wide, and not dependent on how the name
	// resolves (a tag literally named like an element must not hide the force)
	_ = addr
	for root, fs := range t.forces {
		for i := range fs.entries {
			if gen == 0 || fs.entries[i].gen != gen {
				continue
			}
			fs.entries = append(fs.entries[:i], fs.entries[i+1:]...)
			t.releaseLocked(root, fs)
			t.forceRev++
			return true
		}
	}
	return false
}

func (t *Tags) forceGen(addr string, v any, strict, ifFree bool) (uint64, string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	root, path, tv, err := t.resolveForceAddr(addr)
	if err != nil {
		return 0, "", err
	}
	if ifFree {
		// by the name the table keeps the force under, not as typed
		if f := t.overlapUnderLocked(root, ir.JoinPath(root, path)); f != "" {
			return 0, "", fmt.Errorf("%s: %w (as %s)", addr, ErrAlreadyForced, f)
		}
	}
	if tv.v.Kind == ir.TypeFB {
		return 0, "", fmt.Errorf("tag %s is a function-block instance — force one of its pins' tags instead", root)
	}
	if tv.owned {
		// The runtime stores a companion past the force layer: a force on
		// one would show in the table and hold nothing.
		return 0, "", fmt.Errorf("tag %s is set by the runtime from the substitute table and cannot be forced: %w", root, ErrReadOnly)
	}
	fs := t.forces[root]
	base := tv.v
	if fs != nil {
		base = fs.actual
	}
	// Validate and coerce through the very routine every write uses, then
	// read the coerced leaf back so the entry stores a typed value.
	rootT := t.types[ir.NameKey(root)]
	set := ir.SetFieldTyped
	if strict {
		set = ir.SetFieldTypedStrict
	}
	nv, err := set(base, rootT, path, v, "tag "+root)
	if err != nil {
		return 0, "", err
	}
	fv, _, err := ir.FieldAt(nv, rootT, path, root)
	if err != nil {
		return 0, "", err
	}
	if fv.Kind == ir.TypeFB {
		return 0, "", fmt.Errorf("%s is a function-block instance — it has no value to force", addr)
	}
	if fs == nil {
		fs = &forceSet{actual: tv.v, typ: rootT}
		if t.forces == nil {
			t.forces = map[string]*forceSet{}
		}
		t.forces[root] = fs
		tv.forced = true
	}
	name := ir.JoinPath(root, path)
	t.forceSeq++
	e := forceEntry{name: name, path: path, val: ir.CopyValue(fv), sinceMs: t.NowMs(), gen: t.forceSeq}
	replaced := false
	for i := range fs.entries {
		if fs.entries[i].name == name {
			fs.entries[i], replaced = e, true
		}
	}
	if !replaced {
		if path == nil {
			// The whole-tag force goes first; member forces already in the
			// table keep refining it.
			fs.entries = append([]forceEntry{e}, fs.entries...)
		} else {
			fs.entries = append(fs.entries, e)
		}
	}
	t.storeLocked(root, tv, fs.apply(fs.actual))
	t.forceRev++
	return e.gen, name, nil
}

// Unforce removes the force on one address and reports whether there was
// one. When it was the tag's last force the tag goes back to its actual
// value at once — the driver's last delivery, the logic's last write — rather
// than holding the forced value until something next writes it.
func (t *Tags) Unforce(addr string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	key := ir.NameKey(t.forceKeyLocked(addr))
	for root, fs := range t.forces {
		for i := range fs.entries {
			if ir.NameKey(fs.entries[i].name) != key {
				continue
			}
			fs.entries = append(fs.entries[:i], fs.entries[i+1:]...)
			t.releaseLocked(root, fs)
			t.forceRev++
			return true
		}
	}
	return false
}

// UnforceAll removes every force and returns how many there were.
func (t *Tags) UnforceAll() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := 0
	for root, fs := range t.forces {
		n += len(fs.entries)
		fs.entries = nil
		t.releaseLocked(root, fs)
	}
	if n > 0 {
		t.forceRev++
	}
	return n
}

// releaseLocked re-stores a root after one of its forces went away: the
// remaining forces over the actual value, or — with none left — the actual
// value itself, and the tag leaves the table. Caller holds t.mu.
func (t *Tags) releaseLocked(root string, fs *forceSet) {
	tv := t.vals[root]
	if len(fs.entries) == 0 {
		delete(t.forces, root)
		if tv != nil {
			tv.forced = false
			t.storeLocked(root, tv, fs.actual)
		}
		return
	}
	if tv != nil {
		t.storeLocked(root, tv, fs.apply(fs.actual))
	}
}

// Forces returns the force table, sorted by address.
func (t *Tags) Forces() []Force {
	t.mu.RLock()
	defer t.mu.RUnlock()
	var out []Force
	for _, fs := range t.forces {
		for _, e := range fs.entries {
			f := Force{Name: e.name, Value: plain(e.val), SinceMs: e.sinceMs}
			if fs.wrote {
				if a, ok := leafAt(fs.actual, e.path); ok {
					f.Actual = plain(a)
				}
			}
			out = append(out, f)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ForcedValues returns address → forced value (plain JSON form) for every
// active force, or nil when there are none — what a stream frame carries so
// an editor can badge forced values. Costs one map-length check when nothing
// is forced.
func (t *Tags) ForcedValues() map[string]any {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if len(t.forces) == 0 {
		return nil
	}
	out := make(map[string]any, len(t.forces))
	for _, fs := range t.forces {
		for _, e := range fs.entries {
			out[e.name] = plain(e.val)
		}
	}
	return out
}

// ForceCount is the number of active forces.
func (t *Tags) ForceCount() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	n := 0
	for _, fs := range t.forces {
		n += len(fs.entries)
	}
	return n
}

// ForceRevision changes whenever the force table does (a force applied,
// changed, or removed) and at no other time.
func (t *Tags) ForceRevision() uint64 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.forceRev
}

// ForcedOverlap reports the forced address a write to addr would collide
// with — addr itself, a forced member under it (writing the whole struct),
// or a whole-tag force over it (writing one member) — or "" when the write
// touches nothing forced.
func (t *Tags) ForcedOverlap(addr string) string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.forcedOverlapLocked(addr)
}

// forcedOverlapLocked is ForcedOverlap under a lock the caller holds.
func (t *Tags) forcedOverlapLocked(addr string) string {
	if len(t.forces) == 0 {
		return ""
	}
	if root, path, _, err := t.resolveForceAddr(addr); err == nil {
		return t.overlapUnderLocked(root, ir.JoinPath(root, path))
	}
	// an address that names no tag: as before, by the root as typed
	root := addr
	if _, _, ok := t.lookupLocked(addr); !ok {
		root, _ = ir.SplitAddress(addr)
	}
	_, root, _ = t.lookupLocked(root)
	return t.overlapUnderLocked(root, addr)
}

// overlapUnderLocked is the forced address under tag root that name is, is in
// or contains. The root is the one the address resolved to: a name is never
// resolved a second time (a tag literally named "A[1]" beside an array "A"
// would send it to the wrong root).
func (t *Tags) overlapUnderLocked(root, name string) string {
	fs := t.forces[root]
	if fs == nil {
		return ""
	}
	a := ir.NameKey(name)
	for _, e := range fs.entries {
		n := ir.NameKey(e.name)
		if n == a || under(n, a) || under(a, n) {
			return e.name
		}
	}
	return ""
}

// under reports whether address a lies inside address p: a member
// (p.X) or an element (p[2]) of it, at any depth.
func under(a, p string) bool {
	return strings.HasPrefix(a, p+".") || strings.HasPrefix(a, p+"[")
}

// readActual is ReadGlobal for what the tag holds WITHOUT its forces and
// substitutes — the value its writers last stored. Other tags read as usual.
func (t *Tags) readActual(name string) (ir.Value, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	tv, name, ok := t.lookupLocked(name)
	if !ok {
		return ir.Value{}, &UndefinedTagError{name}
	}
	return t.liveLocked(name, tv), nil
}

// leafAt walks a member path into a value.
func leafAt(v ir.Value, path []string) (ir.Value, bool) {
	for _, seg := range path {
		if v.Kind != ir.TypeStruct || v.Struct == nil {
			return ir.Value{}, false
		}
		i, ok := v.Struct.FieldOf(seg)
		if !ok || i >= len(v.Fld) {
			return ir.Value{}, false
		}
		v = v.Fld[i]
	}
	return v, true
}
