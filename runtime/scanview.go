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
// Two tasks writing the same tag resolve by commit order, last wins — the
// same rule a scan-serialised runtime had, now at commit instead of scan
// granularity; and a read-modify-write of one tag from two tasks can lose
// an update, exactly as on any PLC where tasks share globals. A program
// that needs an atomic cross-task counter owns the tag in one task.
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
	dirty map[string]struct{}
	// firstScan answers ir.ScanInfo for the scan in progress (Program.Run).
	firstScan bool
}

func newScanView(store *Tags) *scanView {
	return &scanView{store: store, vals: map[string]ir.Value{}, dirty: map[string]struct{}{}}
}

// snapshot copies the named externals in. A name the store does not hold
// yet is left out; a read of it falls through to the store (and an
// UndefinedTagError, as before).
func (v *scanView) snapshot(names []string) {
	for k := range v.vals {
		delete(v.vals, k)
	}
	v.store.mu.RLock()
	for _, name := range names {
		if tv, ok := v.store.vals[name]; ok {
			v.vals[name] = tv.v
		}
	}
	v.store.mu.RUnlock()
}

// commit writes every changed external back as one unit. Equal values are
// suppressed and generations stamped by writeLocked, exactly as a direct
// write would be.
func (v *scanView) commit() {
	if len(v.dirty) == 0 {
		return
	}
	v.store.mu.Lock()
	for name := range v.dirty {
		v.store.writeLocked(name, v.vals[name])
		delete(v.dirty, name)
	}
	v.store.mu.Unlock()
}

// ir.Host — what the VM sees during the scan.

func (v *scanView) ReadGlobal(name string) (ir.Value, error) {
	if val, ok := v.vals[name]; ok {
		return val, nil
	}
	return v.store.ReadGlobal(name)
}

func (v *scanView) WriteGlobal(name string, val ir.Value) error {
	v.vals[name] = val
	v.dirty[name] = struct{}{}
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
