package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
)

// Substitutes made durable before an API answers (#707 PR 3a, with #706's
// SaveRetained).
//
// The saver writes on a 2 s tick, so a change it has not written yet is
// lost by a hard kill, and on a redundant pair by a lease lost within the
// tick: a stopped substitute would come back at the restart or takeover,
// and the first scan would run on it. So a start, stop, replace or
// acknowledgement made through the API is saved before the API answers
// (SaveRetained), and the retain store holds exactly the state the
// operator was told about. That is what lets a takeover take the store's
// set as the truth.
//
//   - A start that cannot be saved is refused and undone: the new entry
//     ends (stop, reason not-saved) and an entry it replaced is reinstated
//     as it was (start, reason restored), so the store and the table agree
//     again. The operator is told it did not start.
//   - A stop or acknowledgement that cannot be saved still takes effect
//     live (undoing a stop would put the substitute back on purpose); the
//     answer says it was not kept, __Substitute_Fault is raised
//     (retain-save-failed), and the saver's next successful write clears
//     it.
//
// An expiry is not saved synchronously: it ends the entry by its until,
// which a restart or takeover re-derives the same way, so a store that
// still holds it ends it again.

// ErrSubstituteNotSaved wraps the retain store's error when a change could
// not be made durable.
var ErrSubstituteNotSaved = errors.New("the change could not be saved to the retain store")

// Substitute event reasons for the durability rules above.
const (
	// SubstituteReasonNotSaved: a start that could not be saved, undone.
	SubstituteReasonNotSaved = "not-saved"
	// SubstituteReasonRestored: an entry a refused start had replaced,
	// reinstated as it was.
	SubstituteReasonRestored = "restored"
	// SubstituteReasonRetainSaveFailed: a stop or acknowledgement took
	// effect but could not be saved; a restart before the next save may
	// bring the old state back (a fault with no entry).
	SubstituteReasonRetainSaveFailed = "retain-save-failed"
)

// saveSubstitutes makes the current substitute table durable: nil once the
// store holds it, or when there is no retain store (nothing outlives the
// process to disagree with).
func (r *Runtime) saveSubstitutes(ctx context.Context) error {
	err := r.SaveRetained(ctx)
	if err == nil || errors.Is(err, ErrNoRetainStore) {
		return nil
	}
	return fmt.Errorf("%w: %w", ErrSubstituteNotSaved, err)
}

// SubstituteSaved is Substitute, durable before it returns (see above). A
// start the store does not take is undone and refused.
func (r *Runtime) SubstituteSaved(ctx context.Context, addr string, req SubstituteRequest) (Substitution, error) {
	s, replaced, err := r.substitute(addr, req)
	if err != nil {
		return s, err
	}
	if err := r.saveSubstitutes(ctx); err != nil {
		r.undoStart(s, replaced)
		slog.Warn("runtime: substitute start refused: it could not be saved", "substitute", s.Name, "id", s.ID, "error", err)
		return Substitution{}, fmt.Errorf("substitute %s: not started: %w", addr, err)
	}
	return s, nil
}

// undoStart ends a start that could not be saved and reinstates the entry
// it replaced, timing included.
func (r *Runtime) undoStart(s Substitution, replaced *subEntry) {
	r.subMu.Lock()
	defer r.subMu.Unlock()
	at := r.wallNow().UnixMilli()
	var evs []SubstituteEvent
	t := r.tags
	t.mu.Lock()
	if ended, ok := t.unsubstituteLocked(s.ID); ok {
		evs = append(evs, SubstituteEvent{Kind: SubstituteEventStop, Reason: SubstituteReasonNotSaved, Entry: ended, By: "system", AtMs: at})
	}
	if replaced != nil {
		if back, _, err := t.substituteLocked(replaced.Name, replaced.Value, *replaced); err == nil {
			evs = append(evs, SubstituteEvent{Kind: SubstituteEventStart, Reason: SubstituteReasonRestored, Entry: back, By: "system", AtMs: at})
		} else {
			slog.Error("runtime: the substitute a refused start replaced could not be reinstated", "substitute", replaced.Name, "id", replaced.ID, "error", err)
		}
	}
	t.mu.Unlock()
	logSubEvents(evs)
	r.subEvents.push(evs)
}

// RestoreSaved is Restore, saved before it returns. The stop takes effect
// whether or not the save succeeds; kept says whether it did (see above).
func (r *Runtime) RestoreSaved(ctx context.Context, id uint64, by string) (s Substitution, ok, kept bool, err error) {
	s, ok = r.Restore(id, by)
	if !ok {
		return s, false, false, nil
	}
	if err := r.saveSubstitutes(ctx); err != nil {
		r.substituteSaveFailed()
		return s, true, false, err
	}
	return s, true, true, nil
}

// AckSubstitutesSaved is AckSubstitutes, saved before it returns; as
// RestoreSaved, the acknowledgement stands whether or not it is kept.
func (r *Runtime) AckSubstitutesSaved(ctx context.Context, ids []uint64, by string) (acked []Substitution, kept bool, err error) {
	acked = r.AckSubstitutes(ids, by)
	if len(acked) == 0 {
		return nil, false, nil
	}
	if err := r.saveSubstitutes(ctx); err != nil {
		r.substituteSaveFailed()
		return acked, false, err
	}
	return acked, true, nil
}

// substituteSaveFailed raises __Substitute_Fault (retain-save-failed).
func (r *Runtime) substituteSaveFailed() {
	r.subMu.Lock()
	defer r.subMu.Unlock()
	t := r.tags
	t.mu.Lock()
	was := t.subSaveFault
	t.subSaveFault = true
	t.syncSubLocked()
	t.mu.Unlock()
	if was {
		return
	}
	evs := []SubstituteEvent{{Kind: SubstituteEventFault, Reason: SubstituteReasonRetainSaveFailed, By: "system", AtMs: r.wallNow().UnixMilli()}}
	logSubEvents(evs)
	r.subEvents.push(evs)
}

// substitutesSaved clears a retain-save-failed fault: the store has taken
// a save since. Called by the saver after each successful write.
func (r *Runtime) substitutesSaved() {
	t := r.tags
	t.mu.RLock()
	set := t.subSaveFault
	t.mu.RUnlock()
	if !set {
		return
	}
	r.subMu.Lock()
	defer r.subMu.Unlock()
	t.mu.Lock()
	t.subSaveFault = false
	t.syncSubLocked()
	t.mu.Unlock()
	evs := []SubstituteEvent{{Kind: SubstituteEventFaultClear, Reason: SubstituteReasonRetainSaveFailed, By: "system", AtMs: r.wallNow().UnixMilli()}}
	logSubEvents(evs)
	r.subEvents.push(evs)
}
