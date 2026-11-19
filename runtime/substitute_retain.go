package runtime

import (
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/retain"
)

// Substitutes in the retain store (#707 PR 3a).
//
// A substitute is bounded, audited operator intent meant to run the plant
// through a bad sensor, so unlike a force it survives a restart: a deploy
// must not put the bad reading back into the logic mid-run. What is kept
// (retain.SubstituteState): every active entry with its wall-clock start,
// until and granted duration; the highest ID ever issued, so an ID is never
// issued twice; and, while a substitute runs, the wall clock rounded down
// to lastSeenStep.
//
// # A single controller's restart (no coordinator)
//
// The remainder is computed once, on the wall clock, in restart mode
// (substituteRemainder): until − now, so the downtime counts, and an entry
// whose time ran out while the controller was down expires at the load. So
// does every entry when the clock went backwards across the restart: now
// before an entry's start, or before lastSeen. lastSeen is written only
// while a substitute is active, at most once per lastSeenStep, so it costs
// one store write per 30 s while one runs and none otherwise; a backwards
// step smaller than that across a restart is not seen, which is within
// SubstituteSkew. From then on the remainder is timed on the monotonic
// clock, as any entry is.
//
// # A takeover from a shared store (a coordinator)
//
// The store is what the last leader held, and a standby cannot start a
// substitute, so this replica's own table (from an earlier spell as leader)
// is stale by definition: the store's set REPLACES it. An entry in both is
// kept as this process holds it; one only here ends as a stop (reason
// superseded, not journalled: the leader that ended it did); one only in
// the store is applied untimed, and takeover's retimeSubstitutes computes
// its remainder once from until (remainTakeover).
//
// # Failures
//
// An entry the project no longer allows (its rule, its input or its type
// changed in a deploy) ends at the load as an expiry, reason
// refused-at-load: audited, and __Substitute_Expired pulses. A store that
// cannot be loaded leaves the table as it is and raises __Substitute_Fault
// (retain-load-failed) until a load succeeds.

// lastSeenStep is the granularity of retain.SubstituteState.LastSeenMs.
const lastSeenStep = SubstituteSkew

// retainedSubstitutes is the substitute section of what the controller
// retains: nil until the first substitute.
func (r *Runtime) retainedSubstitutes() *retain.SubstituteState {
	r.subMu.Lock()
	defer r.subMu.Unlock()
	t := r.tags
	t.mu.RLock()
	var es []retain.SubstituteRetain
	for _, ss := range t.subs {
		for i := range ss.entries {
			e := &ss.entries[i]
			es = append(es, retain.SubstituteRetain{
				ID: e.ID, Name: e.Name, Value: e.Value, By: e.By,
				StartMs: e.StartMs, UntilMs: e.UntilMs, GrantedMs: e.GrantedMs,
				AckOnStart: e.AckOnStart, Acked: e.Acked,
			})
		}
	}
	t.mu.RUnlock()
	es = append(es, r.subPending...) // still waiting for their input: kept
	if len(es) == 0 && r.subSeq == 0 {
		return nil
	}
	sort.Slice(es, func(i, j int) bool { return es[i].ID < es[j].ID })
	st := &retain.SubstituteState{Entries: es, HighID: r.subSeq}
	if len(es) > 0 {
		st.LastSeenMs = r.wallNow().Truncate(lastSeenStep).UnixMilli()
	}
	return st
}

// loadSubstitutes applies the store's substitute section (see above).
// Called from loadRetained once the store has loaded.
func (r *Runtime) loadSubstitutes(st *retain.SubstituteState) {
	if st == nil && r.tags.SubstituteCount() == 0 && !r.subLoadFaulted() && r.subPendingN.Load() == 0 {
		return
	}
	r.subMu.Lock()
	defer r.subMu.Unlock()
	wall, now := r.wallNow(), r.mono()
	at := wall.UnixMilli()
	var entries []retain.SubstituteRetain
	var lastSeen time.Time
	if st != nil {
		entries = st.Entries
		if st.HighID > r.subSeq {
			r.subSeq = st.HighID
		}
		if st.LastSeenMs > 0 {
			lastSeen = time.UnixMilli(st.LastSeenMs)
		}
	}
	// A takeover times what it loads from until in takeover mode (its
	// retimeSubstitutes, right after this); a restart in restart mode,
	// with last-seen. Entries still waiting for their input are derived
	// the same way when they apply.
	r.subPending, r.subPendingLastSeen = nil, lastSeen
	r.subPendingMode = remainTakeover
	if r.coord == nil {
		r.subPendingMode = remainRestart
	}
	var evs []SubstituteEvent
	t := r.tags
	t.mu.Lock()
	if t.subLoadFault {
		t.subLoadFault = false
		evs = append(evs, SubstituteEvent{Kind: SubstituteEventFaultClear, Reason: SubstituteReasonRetainLoadFailed, By: "system", AtMs: at})
	}
	if r.coord != nil {
		inStore := make(map[uint64]bool, len(entries))
		for _, e := range entries {
			inStore[e.ID] = true
		}
		var gone []uint64
		for _, ss := range t.subs {
			for i := range ss.entries {
				if !inStore[ss.entries[i].ID] {
					gone = append(gone, ss.entries[i].ID)
				}
			}
		}
		sort.Slice(gone, func(i, j int) bool { return gone[i] < gone[j] })
		for _, id := range gone {
			if s, ok := t.unsubstituteLocked(id); ok {
				evs = append(evs, SubstituteEvent{Kind: SubstituteEventStop, Reason: SubstituteReasonSuperseded, Entry: s, By: "system", AtMs: at})
				evs = append(evs, faultCleared(s, at)...)
			}
		}
	}
	evs = append(evs, r.installLocked(entries, now, at)...)
	if r.coord == nil {
		// A restart: the remainder from until, downtime counted, and a
		// clock that went back across the restart expires the entry.
		evs = append(evs, t.rederiveLocked(wall, lastSeen, now, remainRestart)...)
	}
	t.syncSubLocked()
	t.mu.Unlock()
	r.subPendingN.Store(int32(len(r.subPending)))
	logSubEvents(evs)
	r.subEvents.push(evs)
}

// installLocked lays retained entries into the table, untimed (the caller
// derives their remainders). An entry whose input does not exist yet (an
// input with no initial value exists from its first read) is kept in
// subPending for applyPendingSubstitutes; one the project no longer allows
// ends as a refused-at-load expiry, with the pulse. Caller holds subMu and
// t.mu.
func (r *Runtime) installLocked(entries []retain.SubstituteRetain, now time.Duration, at int64) []SubstituteEvent {
	t := r.tags
	var evs []SubstituteEvent
	refused := false
	for _, re := range entries {
		if t.hasIDLocked(re.ID) {
			continue // this process holds it already, timed
		}
		s := Substitution{
			ID: re.ID, Name: re.Name, Value: re.Value, By: re.By,
			StartMs: re.StartMs, UntilMs: re.UntilMs, GrantedMs: re.GrantedMs,
			AckOnStart: re.AckOnStart, Acked: re.Acked,
		}
		if t.addressActiveLocked(re.Name) {
			// Only without a coordinator (with one, the table was just
			// cut to the store's set): a substitute started before the
			// load, by an embedder that never called LoadRetained, is
			// the newer intent, as a replace would be.
			evs = append(evs, SubstituteEvent{Kind: SubstituteEventStop, Reason: SubstituteReasonSuperseded, Entry: s, By: "system", AtMs: at})
			continue
		}
		root, err := r.loadableLocked(re)
		if err == nil {
			if _, _, exists := t.lookupLocked(root); !exists {
				r.subPending = append(r.subPending, re)
				continue
			}
			_, _, err = t.substituteLocked(re.Name, re.Value, subEntry{Substitution: s})
		}
		if err != nil {
			slog.Warn("runtime: retained substitute not restored; it ends here and the live value is back",
				"substitute", re.Name, "id", re.ID, "error", err)
			evs = append(evs, SubstituteEvent{Kind: SubstituteEventExpire, Reason: SubstituteReasonRefusedAtLoad, Entry: s, By: "system", AtMs: at})
			refused = true
		}
	}
	if refused {
		t.armPulseLocked(now)
	}
	return evs
}

// applyPendingSubstitutes lays the retained entries that waited for their
// input, once the main read has created it: in the scan's substitute step,
// after the read and before the logic, so the first scan that has the
// input runs on its substitute. Their remainder is derived as the load
// would have (restart or takeover mode). Caller holds subMu.
func (r *Runtime) applyPendingSubstitutes() []SubstituteEvent {
	pending := r.subPending
	r.subPending = nil
	wall, now := r.wallNow(), r.mono()
	t := r.tags
	t.mu.Lock()
	evs := r.installLocked(pending, now, wall.UnixMilli())
	if len(r.subPending) < len(pending) {
		evs = append(evs, t.rederiveLocked(wall, r.subPendingLastSeen, now, r.subPendingMode)...)
	}
	t.syncSubLocked()
	t.mu.Unlock()
	r.subPendingN.Store(int32(len(r.subPending)))
	return evs
}

// pendingRestore ends a retained entry still waiting for its input, for
// Restore. Caller holds subMu.
func (r *Runtime) pendingRestore(id uint64) (Substitution, bool) {
	for i, re := range r.subPending {
		if re.ID != id {
			continue
		}
		r.subPending = append(r.subPending[:i:i], r.subPending[i+1:]...)
		r.subPendingN.Store(int32(len(r.subPending)))
		return Substitution{
			ID: re.ID, Name: re.Name, Value: re.Value, By: re.By,
			StartMs: re.StartMs, UntilMs: re.UntilMs, GrantedMs: re.GrantedMs,
			AckOnStart: re.AckOnStart, Acked: re.Acked,
		}, true
	}
	return Substitution{}, false
}

// subLoadFaulted reports whether the last retain load failed.
func (r *Runtime) subLoadFaulted() bool {
	r.tags.mu.RLock()
	defer r.tags.mu.RUnlock()
	return r.tags.subLoadFault
}

// substituteLoadFailed raises __Substitute_Fault for a retain store that
// could not be loaded: substitutes it held may be missing, and the table
// runs on as it is. A project with no substitutable input has nothing to
// flag.
func (r *Runtime) substituteLoadFailed() {
	if len(r.subRoots) == 0 {
		return
	}
	r.subMu.Lock()
	defer r.subMu.Unlock()
	t := r.tags
	t.mu.Lock()
	was := t.subLoadFault
	t.subLoadFault = true
	t.syncSubLocked()
	t.mu.Unlock()
	if was {
		return
	}
	evs := []SubstituteEvent{{Kind: SubstituteEventFault, Reason: SubstituteReasonRetainLoadFailed, By: "system", AtMs: r.wallNow().UnixMilli()}}
	logSubEvents(evs)
	r.subEvents.push(evs)
}

// loadableLocked reports whether a retained entry may run here (its root
// is a substitutable input), and its root. The value's type check is substituteLocked's.
// Caller holds t.mu.
func (r *Runtime) loadableLocked(re retain.SubstituteRetain) (string, error) {
	root, _ := ir.SplitAddress(re.Name)
	if _, key, whole := r.tags.lookupLocked(re.Name); whole {
		root = key
	}
	if !r.isInput(root) {
		return root, fmt.Errorf("%s is no longer a driver-bound input", root)
	}
	if !r.substitutable(root) {
		return root, fmt.Errorf("%s is no longer substitutable (no substitutes: rule names it)", root)
	}
	return root, nil
}

// hasIDLocked reports whether an entry with this ID is active. Caller
// holds t.mu.
func (t *Tags) hasIDLocked(id uint64) bool {
	for _, ss := range t.subs {
		for i := range ss.entries {
			if ss.entries[i].ID == id {
				return true
			}
		}
	}
	return false
}

// addressActiveLocked reports whether an entry holds exactly this
// address. Caller holds t.mu.
func (t *Tags) addressActiveLocked(name string) bool {
	for _, ss := range t.subs {
		for i := range ss.entries {
			if ir.SameName(ss.entries[i].Name, name) {
				return true
			}
		}
	}
	return false
}
