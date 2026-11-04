package runtime

import (
	"time"

	"github.com/joyautomation/nautilus/lang/ir"
)

// scanView is one program's private window onto the shared tag store for
// the duration of a scan: its VAR_EXTERNAL set is copied in under one read
// lock (snapshot), the VM runs against the copy with no locking at all,
// and the values it changed are written back under one write lock
// (commit). Two things follow:
//
//   - A scan still sees one consistent store — every external as of one
//     instant — and lands its results as one unit, which is what the old
//     global scan lock bought. But two tasks now run CONCURRENTLY, and a
//     fast task never waits behind a slow one; the only shared sections
//     are the two copies, microseconds each.
//   - The store only ever holds committed scans. A reader (observer,
//     server, Sparkplug) can never see a scan half-done, though it can see
//     task A's commit and then task B's between two reads of its own.
//
// A commit lands what the program ASSIGNED, at member and bit granularity:
// a tag the program wrote one member (or one bit of a word) of keeps every
// other member (or bit) as the store holds it at commit, so an operator's or
// a client's write to a sibling member or another bit, or another task's
// commit, that lands mid-scan survives (#578; see membermerge.go). Two tasks assigning the same tag or the same member
// resolve by commit order, last wins — the same rule a scan-serialised
// runtime had, now at commit instead of scan granularity; and a
// read-modify-write of one member from two tasks can lose an update,
// exactly as on any PLC where tasks share globals. A program that needs an
// atomic cross-task counter owns the tag in one task.
//
// Stored values are immutable by convention (the VM copies before it
// mutates an aggregate, see ir.CopyValue), so the snapshot is a shallow
// copy with no allocation per scalar. Function-block instances are
// identity, not value (ir.FBInstance), so an FB bound as a global and
// stepped from two tasks is shared state those two tasks race on — New
// warns about such a binding.
type scanView struct {
	store *Tags
	vals  map[string]ir.Value
	// dirty is every tag the scan assigned: nil for a whole-tag assignment,
	// else the tag's writeMask (the members it assigned). masks keeps each
	// tag's mask from scan to scan, so marking allocates only the first
	// time a tag's member is written.
	dirty map[string]*writeMask
	masks map[string]*writeMask
	// snapGen is the store generation at the snapshot: a tag whose
	// generation is still at or below it has not changed since, so the
	// program's copy of its untouched members is current and it commits
	// whole, with no merge.
	snapGen uint64
	// alias maps a program's spelling of a tag to the store's when the two
	// differ only in case (#197: `VAR_EXTERNAL level` binds the manifest's
	// Level). vals and dirty are keyed by the store's spelling, so two POUs
	// that case one tag differently share one value within the scan. Empty
	// — and never consulted — while every program uses the declared
	// spelling, which keeps the scan path to the one map read it was.
	alias map[string]string
	// firstScan answers ir.ScanInfo for the scan in progress (Program.Run).
	firstScan bool
}

func newScanView(store *Tags) *scanView {
	return &scanView{store: store, vals: map[string]ir.Value{}, dirty: map[string]*writeMask{}, masks: map[string]*writeMask{}, alias: map[string]string{}}
}

// snapshot copies the named externals in. A name the store does not hold
// yet is left out; a read of it falls through to the store (and an
// UndefinedTagError, as before).
func (v *scanView) snapshot(names []string) {
	for k := range v.vals {
		delete(v.vals, k)
	}
	v.store.mu.RLock()
	v.snapGen = v.store.gen
	for _, name := range names {
		if tv, ok := v.store.vals[name]; ok {
			v.vals[name] = tv.v
			continue
		}
		key, ok := v.alias[name]
		if !ok {
			// Resolved once, then remembered: a tag never changes the
			// spelling it is stored under.
			if key, ok = v.store.fold[ir.NameKey(name)]; ok {
				v.alias[name] = key
			}
		}
		if ok {
			v.vals[key] = v.store.vals[key].v
		}
	}
	v.store.mu.RUnlock()
}

// commit writes every assigned external back as one unit. Equal values are
// suppressed and generations stamped by writeLocked, exactly as a direct
// write would be. A tag the program assigned only members of is merged
// onto what the store holds now when it changed since the snapshot, or is
// forced (merged onto its actual value, so the forced members do not leak
// into what the tag returns to when the force is removed); see
// membermerge.go.
func (v *scanView) commit() {
	if len(v.dirty) == 0 {
		return
	}
	v.store.mu.Lock()
	for name, m := range v.dirty {
		val := v.vals[name]
		if m != nil {
			if !m.all {
				if cur, key, ok := v.store.lookupLocked(name); ok && (cur.gen > v.snapGen || cur.forced) {
					val = m.merge(v.store.writeBase(key, cur), val)
				}
			}
			m.reset()
		}
		v.store.writeLocked(name, val)
		delete(v.dirty, name)
	}
	v.store.mu.Unlock()
}

// ir.Host — what the VM sees during the scan.

func (v *scanView) ReadGlobal(name string) (ir.Value, error) {
	if len(v.alias) > 0 {
		if key, ok := v.alias[name]; ok {
			name = key
		}
	}
	if val, ok := v.vals[name]; ok {
		return val, nil
	}
	return v.store.ReadGlobal(name)
}

func (v *scanView) WriteGlobal(name string, val ir.Value) error {
	if len(v.alias) > 0 {
		if key, ok := v.alias[name]; ok {
			name = key
		}
	}
	v.vals[name] = val
	if m := v.dirty[name]; m != nil {
		m.all = true // keep the mask: commit resets what it marked
	} else {
		v.dirty[name] = nil
	}
	return nil
}

// WriteGlobalMember is the VM's optional ir.MemberWriter: an assignment
// into part of a global — a member, an element, or bits of an integer
// (path.Bits). The view holds the new whole value, as for WriteGlobal, and
// records the path, so commit lands only what the program assigned.
func (v *scanView) WriteGlobalMember(name string, val ir.Value, path ir.MemberPath) error {
	if len(v.alias) > 0 {
		if key, ok := v.alias[name]; ok {
			name = key
		}
	}
	v.vals[name] = val
	m, ok := v.dirty[name]
	if ok && m == nil {
		return nil // the whole tag is already the program's this scan
	}
	if !ok {
		if m = v.masks[name]; m == nil {
			m = &writeMask{}
			v.masks[name] = m
		}
		v.dirty[name] = m
	}
	m.mark(val, path)
	return nil
}

func (v *scanView) NowMs() int64 { return v.store.NowMs() }

// DivZero forwards the VM's optional ir.DivZeroCounter to the store, so a
// division by zero inside a task still shows in ScanStats.DivZero. Any
// optional Host extension the VM looks for must be forwarded here: the
// view is the Host now, and a type assertion on it finds only what it
// implements.
func (v *scanView) DivZero() { v.store.DivZero() }

// Now forwards the VM's optional ir.WallClock: LOCAL_TIME.
func (v *scanView) Now() time.Time { return v.store.Now() }

// FirstScan is the VM's optional ir.ScanInfo: FIRST_SCAN().
func (v *scanView) FirstScan() bool { return v.firstScan }
