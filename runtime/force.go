package runtime

import (
	"fmt"
	"sort"
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
}

type forceEntry struct {
	name    string   // the address as given ("P101.Speed")
	path    []string // member path below the root; nil = the whole tag
	val     ir.Value // the forced value, already coerced to the member's type
	sinceMs int64
}

// apply lays every force of the set over v and returns the result. Forces
// were validated (and coerced) when applied, so an error here means the tag
// changed shape underneath them — the force is skipped rather than the write
// lost.
func (fs *forceSet) apply(v ir.Value) ir.Value {
	for i := range fs.entries {
		e := &fs.entries[i]
		if nv, err := ir.SetField(v, e.path, e.val, ""); err == nil {
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
	root, rest, dotted := strings.Cut(addr, ".")
	tv, root, ok := t.lookupLocked(root)
	if !ok || !dotted {
		return "", nil, nil, &UndefinedTagError{root}
	}
	return root, declaredPath(tv.v, strings.Split(rest, ".")), tv, nil
}

// declaredPath respells a member path as the struct declares it (member
// names are case-insensitive), so a force entry is named one way however
// the operator typed it. A segment that names nothing is left as typed —
// SetField reports it.
func declaredPath(v ir.Value, path []string) []string {
	out := make([]string, len(path))
	copy(out, path)
	for i, seg := range out {
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
// until Unforce. v is coerced to the type already there exactly as a SetPath
// write would be, and an address or value SetPath would refuse is refused
// here with the same message. Forcing an address that is already forced
// changes its value.
func (t *Tags) Force(addr string, v any) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	root, path, tv, err := t.resolveForceAddr(addr)
	if err != nil {
		return err
	}
	if tv.v.Kind == ir.TypeFB {
		return fmt.Errorf("tag %s is a function-block instance — force one of its pins' tags instead", root)
	}
	fs := t.forces[root]
	base := tv.v
	if fs != nil {
		base = fs.actual
	}
	// Validate and coerce through the very routine every write uses, then
	// read the coerced leaf back so the entry stores a typed value.
	nv, err := ir.SetField(base, path, v, "tag "+root)
	if err != nil {
		return err
	}
	fv := nv
	for _, seg := range path {
		i := fv.Struct.FieldIndex[seg]
		fv = fv.Fld[i]
	}
	if fv.Kind == ir.TypeFB {
		return fmt.Errorf("%s is a function-block instance — it has no value to force", addr)
	}
	if fs == nil {
		fs = &forceSet{actual: tv.v}
		if t.forces == nil {
			t.forces = map[string]*forceSet{}
		}
		t.forces[root] = fs
		tv.forced = true
	}
	name := root
	if len(path) > 0 {
		name = root + "." + strings.Join(path, ".")
	}
	e := forceEntry{name: name, path: path, val: ir.CopyValue(fv), sinceMs: t.NowMs()}
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
	return nil
}

// Unforce removes the force on one address and reports whether there was
// one. When it was the tag's last force the tag goes back to its actual
// value at once — the driver's last delivery, the logic's last write — rather
// than holding the forced value until something next writes it.
func (t *Tags) Unforce(addr string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	for root, fs := range t.forces {
		for i := range fs.entries {
			if !strings.EqualFold(fs.entries[i].name, addr) {
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
	if len(t.forces) == 0 {
		return ""
	}
	root := addr
	if _, _, ok := t.lookupLocked(addr); !ok {
		root, _, _ = strings.Cut(addr, ".")
	}
	_, root, _ = t.lookupLocked(root)
	fs := t.forces[root]
	if fs == nil {
		return ""
	}
	a := ir.NameKey(addr)
	for _, e := range fs.entries {
		n := ir.NameKey(e.name)
		if n == a || strings.HasPrefix(n, a+".") || strings.HasPrefix(a, n+".") {
			return e.name
		}
	}
	return ""
}

// readActual is ReadGlobal for what the tag holds WITHOUT its forces — the
// value its writers last stored. Unforced tags read as usual.
func (t *Tags) readActual(name string) (ir.Value, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	tv, name, ok := t.lookupLocked(name)
	if !ok {
		return ir.Value{}, &UndefinedTagError{name}
	}
	if tv.forced {
		return t.forces[name].actual, nil
	}
	return tv.v, nil
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
