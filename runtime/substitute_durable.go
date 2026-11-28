package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/retain"
)

// Substitutes made durable before an API answers (#707 PR 3a, with #706's
// SaveRetained).
//
// The saver writes on a 2 s tick, so a change it has not written yet is
// lost by a hard kill, and on a redundant pair by a lease lost within the
// tick: a stopped substitute would come back at the restart or takeover,
// and the first scan would run on it. So every change made through the API
// is saved before the API answers, and the retain store holds exactly the
// state the operator was told about. That is what lets a takeover take the
// store's set as the truth.
//
// # A start (or replace): save, then apply
//
// The ID is reserved and the start is held PENDING: the save writes the
// table as it is plus the pending start (and without the entry it would
// replace), and nothing runs on it yet. Only once the store holds it is it
// applied, under subMu, and only if its address is as it was when it was
// reserved: a newer change to that address in the meantime (a stop, an
// applied start, or a later pending start) wins, and this start is refused
// (ErrSubstituteSuperseded, 409). A save that fails refuses it too
// (ErrSubstituteNotSaved, 503). Nothing was applied in either case, so
// there is nothing to undo; a best-effort save then brings the store back
// to the table. The save is never made while holding subMu: the scan takes
// it.
//
// A crash between the save and the apply leaves the start in the store,
// so a restart runs it, though its request was never answered: like any
// unanswered request, its outcome is unknown, and an operator re-reads GET
// /api/substitutes.
//
// # A stop or acknowledgement: apply, then save
//
// A stop must take effect at once, so it is applied first and then saved.
// One the store does not take still stands live; the answer says it was
// not kept, and __Substitute_Fault (retain-save-failed) stays up until
// the saver's next successful write. A leader that lost its lease during
// the save says so: the next leader loads the store, which may still hold
// the substitute.
//
// An expiry is not saved synchronously: it ends the entry by its until,
// which a restart or takeover re-derives the same way, so a store that
// still holds it ends it again.

// ErrSubstituteNotSaved wraps the retain store's error when a change could
// not be made durable.
var ErrSubstituteNotSaved = errors.New("the change could not be saved to the retain store")

// ErrSubstituteSuperseded refuses a start whose address changed while its
// save was in flight: the newer change wins.
var ErrSubstituteSuperseded = errors.New("a newer change to this address was made while this start was being saved")

// SubstituteReasonRetainSaveFailed: a stop or acknowledgement took effect
// but could not be saved; a restart before the next save may bring the
// old state back (a fault with no entry).
const SubstituteReasonRetainSaveFailed = "retain-save-failed"

// pendingStart is a start reserved and being saved, not yet applied.
type pendingStart struct {
	re     retain.SubstituteRetain // what the save writes
	value  any
	prevID uint64        // the entry at the address when reserved (0: none)
	end    time.Duration // its deadline on the monotonic clock
}

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

// SubstituteSaved starts a substitute as Substitute does, durable before
// it runs or returns (see above).
func (r *Runtime) SubstituteSaved(ctx context.Context, addr string, req SubstituteRequest) (Substitution, error) {
	p, err := r.reserveStart(addr, req)
	if err != nil {
		return Substitution{}, err
	}
	serr := r.saveSubstitutes(ctx)
	s, err := r.finishStart(p, serr)
	if err != nil {
		// The store may hold the refused start (it was in the snapshot):
		// bring it back to the table, best effort.
		go func() {
			c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = r.SaveRetained(c)
		}()
		slog.Warn("runtime: substitute start refused", "substitute", p.re.Name, "id", p.re.ID, "error", err)
		return Substitution{}, fmt.Errorf("substitute %s: not started: %w", addr, err)
	}
	return s, nil
}

// reserveStart checks a start as Substitute would, reserves its ID and
// holds it pending (in the snapshot a save writes, not in the table).
func (r *Runtime) reserveStart(addr string, req SubstituteRequest) (*pendingStart, error) {
	if req.For <= 0 {
		return nil, fmt.Errorf("substitute %s: a duration is required", addr)
	}
	if r.standby() {
		return nil, fmt.Errorf("substitute %s: this replica is not leading: start it on the leader", addr)
	}
	if err := r.ValidateStart(addr, req.Value); err != nil {
		return nil, err
	}
	r.subMu.Lock()
	defer r.subMu.Unlock()
	t := r.tags
	t.mu.RLock()
	root, path, tv, err := t.resolveForceAddr(addr)
	var sv ir.Value
	if err == nil {
		rootT := t.types[ir.NameKey(root)]
		var nv ir.Value
		if nv, err = ir.SetFieldTypedStrict(t.liveLocked(root, tv), rootT, path, req.Value, "tag "+root); err == nil {
			sv, _, err = ir.FieldAt(nv, rootT, path, root)
		}
	}
	name := ir.JoinPath(root, path)
	prev, _ := t.idAtLocked(name)
	t.mu.RUnlock()
	if err != nil {
		return nil, fmt.Errorf("substitute %s: %w", addr, err)
	}
	r.subSeq++
	wall, now := r.wallNow(), r.mono()
	p := &pendingStart{
		re: retain.SubstituteRetain{
			ID: r.subSeq, Name: name, Value: plain(sv), By: req.By,
			StartMs: wall.UnixMilli(), UntilMs: wall.Add(req.For).UnixMilli(), GrantedMs: req.For.Milliseconds(),
			AckOnStart: req.AckOnStart, RequestID: req.RequestID,
		},
		value: req.Value, prevID: prev, end: now + req.For,
	}
	r.subStarts = append(r.subStarts, p)
	return p, nil
}

// finishStart ends a pending start once its save returned (serr): it
// applies it if the store took it and its address is as it was, and
// refuses it otherwise.
func (r *Runtime) finishStart(p *pendingStart, serr error) (Substitution, error) {
	r.subMu.Lock()
	defer r.subMu.Unlock()
	newer := false
	for i, q := range r.subStarts {
		if q == p {
			r.subStarts = append(r.subStarts[:i:i], r.subStarts[i+1:]...)
			break
		}
	}
	for _, q := range r.subStarts {
		if q.re.ID > p.re.ID && ir.SameName(q.re.Name, p.re.Name) {
			newer = true
		}
	}
	t := r.tags
	t.mu.Lock()
	defer t.mu.Unlock()
	cur, _ := t.idAtLocked(p.re.Name)
	switch {
	case serr != nil:
		return Substitution{}, serr
	case newer || cur != p.prevID:
		return Substitution{}, ErrSubstituteSuperseded
	case r.standby():
		return Substitution{}, fmt.Errorf("%w: this replica lost the lease during the save", ErrSubstituteNotSaved)
	}
	e := subEntry{
		Substitution: Substitution{
			ID: p.re.ID, By: p.re.By, AckOnStart: p.re.AckOnStart, RequestID: p.re.RequestID,
			StartMs: p.re.StartMs, UntilMs: p.re.UntilMs, GrantedMs: p.re.GrantedMs,
		},
		deadline: p.end, timed: true,
	}
	s, replaced, err := t.substituteLocked(p.re.Name, p.value, e)
	if err != nil {
		return Substitution{}, err
	}
	evs := make([]SubstituteEvent, 0, 2)
	if replaced != nil {
		evs = append(evs, SubstituteEvent{Kind: SubstituteEventStop, Reason: SubstituteReasonReplaced, Entry: replaced.Substitution, By: p.re.By, AtMs: s.StartMs})
		evs = append(evs, faultCleared(replaced.Substitution, s.StartMs)...)
	}
	r.subEvents.push(append(evs, SubstituteEvent{Kind: SubstituteEventStart, Entry: s, By: p.re.By, AtMs: s.StartMs}))
	return s, nil
}

// ValidateStart reports what a start of addr with v would refuse for its
// address and value, without starting anything.
func (r *Runtime) ValidateStart(addr string, v any) error {
	root, _ := ir.SplitAddress(addr)
	if _, _, whole := r.tags.lookupName(addr); whole {
		root = addr
	}
	if !r.substitutableAt(addr, root) {
		return fmt.Errorf("substitute %s: only a driver-bound input, or a member an instance reads from a device, can be substituted", addr)
	}
	if !r.substitutable(root) {
		return fmt.Errorf("substitute %s: %s is not substitutable (no substitutes: rule names it), so it has no %s flag for the logic to interlock on", addr, root, SubstitutedSuffix)
	}
	return nil
}

// pendingInSnapshot lays the pending starts into a retained entry list: the
// newest pending start on an address replaces what is there. Caller holds
// subMu.
func (r *Runtime) pendingInSnapshot(es []retain.SubstituteRetain, wall time.Time, now time.Duration) []retain.SubstituteRetain {
	newest := map[string]*pendingStart{}
	for _, p := range r.subStarts {
		k := ir.NameKey(p.re.Name)
		if q := newest[k]; q == nil || p.re.ID > q.re.ID {
			newest[k] = p
		}
	}
	if len(newest) == 0 {
		return es
	}
	out := es[:0:0]
	for _, e := range es {
		if newest[ir.NameKey(e.Name)] == nil {
			out = append(out, e)
		}
	}
	for _, p := range newest {
		re := p.re
		re.DeadlineMs = wall.Add(p.end - now).Truncate(time.Second).UnixMilli()
		out = append(out, re)
	}
	return out
}

// idAtLocked is the ID of the entry holding exactly this address. Caller
// holds t.mu.
func (t *Tags) idAtLocked(name string) (uint64, bool) {
	for _, ss := range t.subs {
		for i := range ss.entries {
			if ir.SameName(ss.entries[i].Name, name) {
				return ss.entries[i].ID, true
			}
		}
	}
	return 0, false
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
