package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/retain"
)

// Substitutes through the replicated log of operator intent (#707 PR 3b).
//
// In a redundant pair, starts, stops and acknowledgements are operator
// intent: they go through the oplog as entries (substitute, unsubstitute,
// suback), and EVERY instance applies them (ApplySubstitute and its
// siblings), so a takeover keeps them exactly. A substitute's ID is its
// entry's seq, and unsubstitute and suback name entries by it, so a stale
// stop never ends a newer substitute of the same tag.
//
// The runtime does not import the oplog (the context map): a composing
// package adapts it to IntentLog and hands its committed entries to the
// Apply methods. Without one (a single controller) the runtime takes
// starts and stops directly, as before.
//
// # Takeover: every committed substitute before the first scan
//
// The log's lazy catch-up runs before the new leader's first WRITE, which
// may be much later: until then its scans would run automatic control on
// the bad reading a substitute committed while it was cut off was meant to
// hide (redundancy test plan §8.1 case 7). So the takeover edge starts
// IntentLog.SyncLeader and holds the first scan until it returns or
// takeoverIntentWait passes, like the inputs wait (#626) and alongside it,
// so the takeover waits for the longer of the two, not their sum. Then
// takeover() runs:
//
//   - synced: the log's set is the table, and the retain store's
//     substitutes are not used (the log wins);
//   - not synced (no majority, or the wait ran out): the replica keeps
//     what it has applied from the log and adds the retained entries the
//     log has not delivered to it (IDs above the highest it applied),
//     marked catch-up-failed, and __Substitute_Fault is raised. A
//     background retry reconciles once the log answers. Nothing is
//     dropped silently.
//
// # Expiry: the leader's, recorded, never waiting for quorum
//
// Only a leader scans, so only a leader expires an entry. It ends the
// entry locally at once, then proposes unsubstitute (by system, reason
// expired) in the background; without quorum the proposal is retried
// until it commits (reconciled at the heal), and the expiry has already
// happened (§8.1 case 10). Applying its own unsubstitute later is a no-op.

// IntentLog is the replicated log of operator intent, as the runtime uses
// it. server.SubstituteLog adapts an oplog node to it.
type IntentLog interface {
	// SyncLeader returns once this leader holds, and has applied through
	// the Apply methods, every committed entry; or an error.
	SyncLeader(ctx context.Context) error
	// ProposeExpiry records that the substitute entry id expired: an
	// unsubstitute by system, reason expired. It returns once committed.
	ProposeExpiry(ctx context.Context, id uint64) error
}

// SubstituteReasonLogSuperseded: a takeover ended the substitute because
// this controller's operator-intent log was superseded by another history
// (a loss a person accepted elsewhere, #1029 3c-3c-2a): it was that old
// history's.
const SubstituteReasonLogSuperseded = "log-superseded"

// StaleRetained is an IntentLog that can say the retain store's
// substitutes may be another log history's (#1029 3c-3c-2a: a person
// accepted a loss elsewhere and this copy is superseded): a takeover whose
// catch-up failed then restores none of them.
type StaleRetained interface {
	RetainedStale() bool
}

// RequestIDWindow is how long a client request ID is remembered after
// the start it carried, on the log's clock: a retry within it is
// answered with that entry (or refused if it asks for something else);
// after it, the same ID starts afresh. The longest grant (7 d) plus a day.
const RequestIDWindow = 8 * 24 * time.Hour

// ErrRequestReused refuses a request ID used again for a different start
// (another address, value or duration) than the one it first carried.
var ErrRequestReused = errors.New("requestId reused with a different request")

// reqSeen is a request ID's first applied entry.
type reqSeen struct {
	id      uint64
	fp      string // address, value, granted duration
	startMs int64  // its start, on the log's (the proposer's) clock
}

// requestPrint is what makes two starts with one request ID the same
// request.
func requestPrint(name string, value any, grantedMs int64) string {
	return fmt.Sprintf("%s|%v|%d", ir.NameKey(name), value, grantedMs)
}

// SameRequest reports whether s is the start a request for addr with v
// for d asks for: a retry with the same request ID must be the same.
func SameRequest(s Substitution, addr string, v any, d time.Duration) bool {
	return requestPrint(s.Name, s.Value, s.GrantedMs) == requestPrint(addr, v, d.Milliseconds())
}

// DefaultTakeoverIntentWait bounds the takeover's wait for the log's
// catch-up when SetIntentLog is given no wait.
const DefaultTakeoverIntentWait = 2 * time.Second

// SubstituteReasonCatchUpFailed: a takeover could not catch up the
// operator-intent log within its wait, so the entry runs from the retain
// store, unconfirmed by the log, until a later catch-up reconciles it.
const SubstituteReasonCatchUpFailed = "catch-up-failed"

// expiryRetry paces the retry of an expiry the log did not take.
const expiryRetry = 2 * time.Second

// intentState is the runtime's side of the log.
type intentState struct {
	// cfg is the log and the takeover's wait (SetIntentLog): an atomic,
	// since takeover and the event path read it under leadMu and subMu.
	cfg atomic.Pointer[intentCfg]

	// The takeover's catch-up, guarded by leadMu (gateEdge).
	syncCh     chan error
	syncCancel context.CancelFunc
	syncSince  time.Time
	synced     bool // the last takeover's catch-up succeeded

	// retrying is a background catch-up after a failed one; guarded by
	// subMu.
	retrying bool
	// applied is the highest substitute ID applied from the log; guarded
	// by subMu.
	applied uint64
	// unconfirmed are the IDs a failed catch-up took from the retain
	// store, until the log confirms them (or a catch-up ends them);
	// guarded by subMu. Kept by ID because an entry may still be waiting
	// for its input (subPending) when it is marked.
	unconfirmed map[uint64]bool
	// reqs is every client request ID the log has applied (within
	// RequestIDWindow), with the entry that carried it first; guarded by
	// subMu. Built from the replicated log alone (applied in the same
	// order everywhere, and replayed at a restart), and pruned on the
	// log's own clock (each entry's start), so every replica, and a new
	// leader, applies a request ID once, even after the entry it started
	// ended.
	reqs map[string]reqSeen

	// lapses are the recent lapsed starts (substitute_lapse.go); guarded
	// by subMu.
	lapses []SubstituteLapse

	// expiries are expired IDs not yet committed as unsubstitute entries.
	expMu      sync.Mutex
	expiries   []uint64
	expRunning bool
}

type intentCfg struct {
	log  IntentLog
	wait time.Duration
}

// SetIntentLog makes the runtime a replica of a redundant pair whose
// substitutes go through the log l (see above); wait bounds a takeover's
// catch-up (0: DefaultTakeoverIntentWait). Call it before Run, and hand
// the log's committed substitute entries to the Apply methods.
func (r *Runtime) SetIntentLog(l IntentLog, wait time.Duration) {
	if wait <= 0 {
		wait = DefaultTakeoverIntentWait
	}
	r.intent.cfg.Store(&intentCfg{log: l, wait: wait})
}

// intentLog is the log, or nil.
func (r *Runtime) intentLog() IntentLog {
	if c := r.intent.cfg.Load(); c != nil {
		return c.log
	}
	return nil
}

// ReplicatedSubstitute is a committed substitute entry, as ApplySubstitute
// takes it.
type ReplicatedSubstitute struct {
	ID         uint64 // the entry's seq
	Name       string // the address
	Value      any
	By         string
	UntilMs    int64
	GrantedMs  int64
	AckOnStart bool
	// RequestID is the client's request ID, if any: an entry naming one an
	// active entry already holds is a retry of a start whose outcome was
	// unknown, and is not applied again.
	RequestID string
}

// ValidateSubstitute reports what Substitute would refuse about addr and
// v, without starting anything: a server checks it before it proposes a
// start to the log, so a start that cannot run is refused to the operator
// rather than committed.
func (r *Runtime) ValidateSubstitute(addr string, v any) error {
	root, _ := ir.SplitAddress(addr)
	if _, _, whole := r.tags.lookupName(addr); whole {
		root = addr
	}
	if !r.isInput(root) {
		return fmt.Errorf("substitute %s: only a driver-bound input can be substituted", addr)
	}
	if !r.substitutable(root) {
		return fmt.Errorf("substitute %s: %s is not substitutable (no substitutes: rule names it)", addr, root)
	}
	if err := r.CheckSubstituteValue(addr, v); err != nil {
		return fmt.Errorf("substitute %s: %w", addr, err)
	}
	return nil
}

// ApplySubstitute applies a committed substitute entry, on every instance.
// It is idempotent: an entry already held (the same ID, address and start)
// is confirmed, which clears a catch-up-failed mark. It arrives untimed; a
// leader derives its remainder at once from until (the leader that took
// the request gets about the granted duration; a replayed old entry does
// not get it again), and a standby leaves it to its takeover.
func (r *Runtime) ApplySubstitute(e ReplicatedSubstitute) error {
	r.subMu.Lock()
	defer r.subMu.Unlock()
	if e.ID > r.intent.applied {
		r.intent.applied = e.ID
	}
	if e.ID > r.subSeq {
		r.subSeq = e.ID
	}
	re := retain.SubstituteRetain{
		ID: e.ID, Name: e.Name, Value: e.Value, By: e.By,
		StartMs: e.UntilMs - e.GrantedMs, UntilMs: e.UntilMs, GrantedMs: e.GrantedMs,
		AckOnStart: e.AckOnStart, RequestID: e.RequestID,
	}
	wall, now := r.wallNow(), r.mono()
	at := wall.UnixMilli()
	t := r.tags
	t.mu.Lock()
	var evs []SubstituteEvent
	start := e.UntilMs - e.GrantedMs
	for id, seen := range r.intent.reqs { // the log's clock: the same on every replica
		if seen.startMs < start-RequestIDWindow.Milliseconds() {
			delete(r.intent.reqs, id)
		}
	}
	fp := requestPrint(e.Name, e.Value, e.GrantedMs)
	if seen, ok := r.intent.reqs[e.RequestID]; e.RequestID != "" && ok && seen.id != e.ID {
		t.mu.Unlock()
		if seen.fp != fp {
			slog.Warn("runtime: substitute start not applied: its requestId was used for a different start", "requestId", e.RequestID, "first", seen.id, "id", e.ID)
			return fmt.Errorf("substitute entry %d: %w (entry %d)", e.ID, ErrRequestReused, seen.id)
		}
		// A retry of a start whose outcome was unknown: the first entry
		// stands, this one is not applied (on every instance, since every
		// instance applies the log in the same order).
		slog.Info("runtime: substitute start retried; the first entry stands", "requestId", e.RequestID, "id", seen.id, "retry", e.ID)
		return nil
	}
	delete(r.intent.unconfirmed, e.ID) // the log delivered it: confirmed
	if held, ok := t.entryLocked(e.ID); ok {
		if sameEntry(held, re) {
			evs = t.confirmLocked(e.ID, at)
			t.syncSubLocked()
			t.mu.Unlock()
			r.publishSub(evs)
			return nil
		}
		// The same ID under another identity: the log's entry is the
		// committed one, so this replica's (from the retain store) ends.
		if s, ok := t.unsubstituteLocked(e.ID); ok {
			evs = append(evs, SubstituteEvent{Kind: SubstituteEventStop, Reason: SubstituteReasonSuperseded, Entry: s, By: "system", AtMs: at})
			evs = append(evs, faultCleared(s, at)...)
		}
	}
	for i, p := range r.subPending {
		if p.ID == e.ID {
			r.subPending = append(r.subPending[:i:i], r.subPending[i+1:]...)
			break
		}
	}
	// A newer start on an address replaces the entry there, as a start
	// does on the leader.
	if id, ok := t.idAtLocked(e.Name); ok {
		if s, ok := t.unsubstituteLocked(id); ok {
			evs = append(evs, SubstituteEvent{Kind: SubstituteEventStop, Reason: SubstituteReasonReplaced, Entry: s, By: e.By, AtMs: at, Replicated: true})
			evs = append(evs, faultCleared(s, at)...)
		}
	}
	before := len(r.subPending)
	evs = append(evs, r.installLocked([]retain.SubstituteRetain{re}, now, at)...)
	if e.RequestID != "" && (len(r.subPending) > before || t.hasIDLocked(e.ID)) {
		// Recorded only once applied (running, or waiting for its input):
		// a start refused here leaves its request ID free for a retry.
		if r.intent.reqs == nil {
			r.intent.reqs = map[string]reqSeen{}
		}
		r.intent.reqs[e.RequestID] = reqSeen{id: e.ID, fp: fp, startMs: start}
	}
	if len(r.subPending) == before && t.hasIDLocked(e.ID) {
		s, _ := t.entryLocked(e.ID)
		evs = append(evs, SubstituteEvent{Kind: SubstituteEventStart, Entry: s, By: e.By, AtMs: at, Replicated: true})
		if !r.standby() {
			evs = append(evs, t.rederiveLocked(wall, time.Time{}, now, remainTakeover)...)
		}
	}
	r.subPendingMode = remainTakeover
	t.syncSubLocked()
	t.mu.Unlock()
	r.subPendingN.Store(int32(len(r.subPending)))
	r.publishSub(evs)
	return nil
}

// ApplyUnsubstitute applies a committed unsubstitute entry: the entry id
// ends, if it is still active (an older or ended ID ends nothing, so a
// late stop never ends a newer substitute). Reason "expired" is the
// leader's expiry, reported as one.
func (r *Runtime) ApplyUnsubstitute(id uint64, by, reason string) {
	r.dropExpiry(id)
	r.subMu.Lock()
	defer r.subMu.Unlock()
	at := r.wallNow().UnixMilli()
	r.tags.mu.Lock()
	s, ok := r.tags.unsubstituteLocked(id)
	r.tags.mu.Unlock()
	if !ok {
		s, ok = r.pendingRestore(id)
	}
	if !ok {
		return
	}
	kind := SubstituteEventStop
	if reason == SubstituteReasonExpired {
		kind = SubstituteEventExpire
	}
	evs := []SubstituteEvent{{Kind: kind, Reason: reason, Entry: s, By: by, AtMs: at, Replicated: true}}
	r.publishSub(append(evs, faultCleared(s, at)...))
}

// ApplySuback applies a committed suback entry: AckSubstitutes, reported
// as replicated.
func (r *Runtime) ApplySuback(ids []uint64, by string) {
	r.ackSubstitutes(ids, by, true)
}

// confirmLocked clears an entry's catch-up-failed mark: the log has
// delivered it. Caller holds t.mu and calls syncSubLocked after.
func (t *Tags) confirmLocked(id uint64, at int64) []SubstituteEvent {
	for _, ss := range t.subs {
		for i := range ss.entries {
			e := &ss.entries[i]
			if e.ID == id && e.Fault == SubstituteReasonCatchUpFailed {
				e.Fault = ""
				return []SubstituteEvent{{Kind: SubstituteEventFaultClear, Reason: SubstituteReasonCatchUpFailed, Entry: t.reportLocked(ss, e), By: "system", AtMs: at}}
			}
		}
	}
	return nil
}

// intentReady answers, on a pending takeover, whether the log's catch-up
// is done (see above): it starts it on the first ask, and is true once
// SyncLeader returned or the wait ran out. Called under leadMu, every
// time inputsReady is, so the two waits overlap.
func (r *Runtime) intentReady() bool {
	in := &r.intent
	c := in.cfg.Load()
	if c == nil {
		return true
	}
	started := false
	if in.syncCh == nil {
		ctx, cancel := context.WithCancel(context.Background())
		ch := make(chan error, 1)
		l := c.log
		go func() { ch <- l.SyncLeader(ctx) }()
		in.syncCh, in.syncCancel, in.syncSince = ch, cancel, time.Now()
		started = true
	}
	var err error
	got := false
	select {
	case err = <-in.syncCh:
		got = true
	default:
		if started && !mutRuntime("catchup_next_tick") {
			err, got = r.waitErr(in.syncCh, min(sameTickWait, c.wait))
		}
	}
	if got {
		in.syncCancel()
		in.syncCh = nil
		in.synced = err == nil
		if err != nil {
			slog.Warn("runtime: operator-intent log catch-up failed; taking over on the retained substitutes", "error", err)
		}
		return true
	}
	if time.Since(in.syncSince) < c.wait {
		return false
	}
	// Out of time: the catch-up goes on in the background (startSyncRetry
	// after the takeover), and the takeover runs on what it holds.
	in.syncCancel()
	in.syncCh = nil
	in.synced = false
	slog.Warn("runtime: operator-intent log catch-up still running; taking over on the retained substitutes", "waited", c.wait)
	return true
}

// resetIntentSync abandons a catch-up in flight: this replica is no
// longer taking over. Called under leadMu.
func (r *Runtime) resetIntentSync() {
	if r.intent.syncCh != nil {
		r.intent.syncCancel()
		r.intent.syncCh = nil
	}
}

// loadIntentFallback is the retain load's substitute step with a log: the
// log wins when the takeover's catch-up succeeded; otherwise the retained
// entries the log has not delivered (IDs above the highest applied) are
// added, marked catch-up-failed, __Substitute_Fault is raised, and a
// background catch-up reconciles later. Caller holds subMu; returns the
// events.
func (r *Runtime) loadIntentFallback(entries []retain.SubstituteRetain, now time.Duration, at int64) []SubstituteEvent {
	t := r.tags
	if r.intent.synced {
		return nil
	}
	if s, ok := r.intentLog().(StaleRetained); ok && len(entries) > 0 && s.RetainedStale() {
		// Another log history's substitutes (#1029 3c-3c-2a): their IDs
		// are that history's seqs, which the new one numbers again. None
		// is restored; the fault below still says the catch-up failed.
		slog.Warn("runtime: the retain store's substitutes are from a superseded operator-intent log; restoring none of them", "entries", len(entries))
		entries = nil
	}
	var fresh []retain.SubstituteRetain
	for _, re := range entries {
		if re.ID > r.intent.applied && !t.hasIDLocked(re.ID) {
			fresh = append(fresh, re)
		}
	}
	evs := r.installLocked(fresh, now, at)
	if r.intent.unconfirmed == nil {
		r.intent.unconfirmed = map[uint64]bool{}
	}
	for _, re := range fresh {
		r.intent.unconfirmed[re.ID] = true
	}
	return append(evs, r.markUnconfirmedLocked(at)...)
}

// markUnconfirmedLocked marks every active entry the log has not
// confirmed catch-up-failed (once), so a waiting entry is marked when it
// applies. Caller holds subMu and t.mu.
func (r *Runtime) markUnconfirmedLocked(at int64) []SubstituteEvent {
	if len(r.intent.unconfirmed) == 0 {
		return nil
	}
	t := r.tags
	var evs []SubstituteEvent
	for _, ss := range t.subs {
		for i := range ss.entries {
			if e := &ss.entries[i]; r.intent.unconfirmed[e.ID] && e.Fault == "" {
				e.Fault = SubstituteReasonCatchUpFailed
				evs = append(evs, SubstituteEvent{Kind: SubstituteEventFault, Reason: SubstituteReasonCatchUpFailed, Entry: t.reportLocked(ss, e), By: "system", AtMs: at})
			}
		}
	}
	return evs
}

// intentAfterTakeover is takeover's last substitute step with a log: when
// the catch-up did not succeed, __Substitute_Fault (catch-up-failed) is
// raised, whatever the retain store held (or whether there is one), and a
// background catch-up reconciles later. Caller holds leadMu.
func (r *Runtime) intentAfterTakeover() {
	if r.intentLog() == nil {
		return
	}
	if r.intent.synced {
		// The log is caught up: whatever an earlier failed catch-up took
		// from the retain store and the log never confirmed ends now, and
		// the fault clears (a lease lost before the retry succeeded leaves
		// such entries behind).
		r.reconcileAfterSync()
		return
	}
	r.subMu.Lock()
	defer r.subMu.Unlock()
	t := r.tags
	t.mu.Lock()
	var ended []SubstituteEvent
	if s, ok := r.intentLog().(StaleRetained); ok && s.RetainedStale() {
		// This copy's log was superseded by another history (#1029
		// 3c-3c-2a): every substitute it holds, applied from its old log
		// or the retain store, is that history's, which a person accepted
		// to lose. None runs.
		ended = r.endAllSubstitutesLocked(SubstituteReasonLogSuperseded)
	}
	raised := !t.subSyncFault
	t.subSyncFault = true
	t.syncSubLocked()
	t.mu.Unlock()
	if len(ended) > 0 {
		slog.Warn("runtime: the operator-intent log was superseded by another history: ending every substitute it held", "ended", len(ended))
		r.publishSub(ended)
	}
	if raised {
		evs := []SubstituteEvent{{Kind: SubstituteEventFault, Reason: SubstituteReasonCatchUpFailed, By: "system", AtMs: r.wallNow().UnixMilli()}}
		logSubEvents(evs)
		r.subEvents.push(evs)
	}
	if !r.intent.retrying {
		r.intent.retrying = true
		go r.syncRetry()
	}
}

// SubstituteByRequest is the active entry a client's request ID started,
// if any: a start retried after an unknown outcome is answered with it.
func (r *Runtime) SubstituteByRequest(id string) (Substitution, bool) {
	if id == "" {
		return Substitution{}, false
	}
	r.tags.mu.RLock()
	defer r.tags.mu.RUnlock()
	return r.tags.byRequestLocked(id)
}

// byRequestLocked is the active entry with this request ID. Caller holds
// t.mu.
func (t *Tags) byRequestLocked(id string) (Substitution, bool) {
	for _, ss := range t.subs {
		for i := range ss.entries {
			if ss.entries[i].RequestID == id {
				return t.reportLocked(ss, &ss.entries[i]), true
			}
		}
	}
	return Substitution{}, false
}

// syncRetry catches up in the background after a takeover's catch-up
// failed, every expiryRetry, while this replica leads. On success the log
// has applied every committed entry (confirming the retained entries it
// holds), so what is still marked catch-up-failed was never committed and
// ends; the fault clears.
func (r *Runtime) syncRetry() {
	l := r.intentLog()
	for {
		// The lease, not standby(): right after a takeover the replica
		// reads as standby until its first read lands.
		if r.coord != nil && !r.coord.IsLeader() {
			r.subMu.Lock()
			r.intent.retrying = false
			r.subMu.Unlock()
			return // the next takeover catches up again
		}
		ctx, cancel := context.WithTimeout(context.Background(), expiryRetry)
		err := l.SyncLeader(ctx)
		cancel()
		if err == nil {
			r.reconcileAfterSync()
			return
		}
		time.Sleep(expiryRetry)
	}
}

// reconcileAfterSync ends what the log did not confirm and clears the
// catch-up fault.
func (r *Runtime) reconcileAfterSync() {
	r.subMu.Lock()
	defer r.subMu.Unlock()
	r.intent.retrying = false
	at := r.wallNow().UnixMilli()
	t := r.tags
	t.mu.Lock()
	var stale []uint64
	for _, ss := range t.subs {
		for i := range ss.entries {
			if ss.entries[i].Fault == SubstituteReasonCatchUpFailed || r.intent.unconfirmed[ss.entries[i].ID] {
				stale = append(stale, ss.entries[i].ID)
			}
		}
	}
	for id := range r.intent.unconfirmed {
		r.pendingRestore(id) // still waiting for its input: it never applies
	}
	r.intent.unconfirmed = nil
	var evs []SubstituteEvent
	for _, id := range stale {
		if s, ok := t.unsubstituteLocked(id); ok {
			evs = append(evs, SubstituteEvent{Kind: SubstituteEventStop, Reason: SubstituteReasonSuperseded, Entry: s, By: "system", AtMs: at})
			evs = append(evs, faultCleared(s, at)...)
		}
	}
	if t.subSyncFault {
		t.subSyncFault = false
		evs = append(evs, SubstituteEvent{Kind: SubstituteEventFaultClear, Reason: SubstituteReasonCatchUpFailed, By: "system", AtMs: at})
	}
	t.syncSubLocked()
	t.mu.Unlock()
	r.publishSub(evs)
}

// publishSub logs and hands out events, and, with a log, proposes every
// expiry this instance decided as an unsubstitute entry (see above).
func (r *Runtime) publishSub(evs []SubstituteEvent) {
	if len(evs) == 0 {
		return
	}
	logSubEvents(evs)
	r.subEvents.push(evs)
	if r.intentLog() == nil {
		return
	}
	var ids []uint64
	for _, e := range evs {
		if e.Kind == SubstituteEventExpire && !e.Replicated &&
			(e.Reason == SubstituteReasonExpired || e.Reason == SubstituteReasonExpiredWhileDown) {
			ids = append(ids, e.Entry.ID)
		}
	}
	if len(ids) > 0 {
		r.queueExpiries(ids)
	}
}

// queueExpiries proposes the expired IDs in the background, retrying what
// the log does not take (no quorum) until it does.
func (r *Runtime) queueExpiries(ids []uint64) {
	in := &r.intent
	in.expMu.Lock()
	defer in.expMu.Unlock()
	in.expiries = append(in.expiries, ids...)
	if !in.expRunning {
		in.expRunning = true
		go r.proposeExpiries()
	}
}

// dropExpiry forgets a queued expiry whose unsubstitute was applied (it
// committed, or another instance recorded the end).
func (r *Runtime) dropExpiry(id uint64) {
	in := &r.intent
	in.expMu.Lock()
	defer in.expMu.Unlock()
	for i, x := range in.expiries {
		if x == id {
			in.expiries = append(in.expiries[:i:i], in.expiries[i+1:]...)
			return
		}
	}
}

func (r *Runtime) proposeExpiries() {
	in := &r.intent
	l := r.intentLog()
	for {
		in.expMu.Lock()
		if r.coord != nil && !r.coord.IsLeader() && len(in.expiries) > 0 {
			// The lease is gone: the new leader owns expiry, and derives
			// it from until (its takeover) and records its own.
			slog.Info("runtime: lease lost; leaving the unrecorded expiries to the new leader", "ids", in.expiries)
			in.expiries = nil
		}
		if len(in.expiries) == 0 {
			in.expRunning = false
			in.expMu.Unlock()
			return
		}
		id := in.expiries[0]
		in.expMu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), expiryRetry)
		err := l.ProposeExpiry(ctx, id)
		cancel()
		if err != nil {
			slog.Warn("runtime: substitute expiry not yet recorded in the operator-intent log; it has ended here and is retried", "id", id, "error", err)
			time.Sleep(expiryRetry)
			continue
		}
		r.dropExpiry(id)
	}
}

// PendingExpiries is the expired IDs not yet recorded in the log.
func (r *Runtime) PendingExpiries() []uint64 {
	in := &r.intent
	in.expMu.Lock()
	defer in.expMu.Unlock()
	return append([]uint64(nil), in.expiries...)
}

// endAllSubstitutesLocked ends every substitute, active or still waiting
// for its input, for reason, and returns the events. Caller holds subMu
// and t.mu.
func (r *Runtime) endAllSubstitutesLocked(reason string) []SubstituteEvent {
	t := r.tags
	at := r.wallNow().UnixMilli()
	var ended []SubstituteEvent
	var ids []uint64
	for _, ss := range t.subs {
		for i := range ss.entries {
			ids = append(ids, ss.entries[i].ID)
		}
	}
	for _, id := range ids {
		if s, ok := t.unsubstituteLocked(id); ok {
			ended = append(ended, SubstituteEvent{Kind: SubstituteEventStop, Reason: reason, Entry: s, By: "system", AtMs: at})
			ended = append(ended, faultCleared(s, at)...)
		}
	}
	var waiting []uint64
	for _, re := range r.subPending {
		waiting = append(waiting, re.ID)
	}
	for _, id := range waiting {
		if s, ok := r.pendingRestore(id); ok {
			ended = append(ended, SubstituteEvent{Kind: SubstituteEventStop, Reason: reason, Entry: s, By: "system", AtMs: at})
		}
	}
	r.subPendingN.Store(int32(len(r.subPending)))
	return ended
}

// EndSupersededSubstitutes ends every substitute this runtime holds,
// active or waiting, because its operator-intent log was superseded by
// another history (#1029 3c-3c-2a): they are the old history's. Leading or
// standing by, at once: a leader that learns it is superseded must not
// run them a scan longer. It returns how many it ended.
func (r *Runtime) EndSupersededSubstitutes() int {
	r.subMu.Lock()
	t := r.tags
	t.mu.Lock()
	ended := r.endAllSubstitutesLocked(SubstituteReasonLogSuperseded)
	t.syncSubLocked()
	t.mu.Unlock()
	r.subMu.Unlock()
	if len(ended) > 0 {
		slog.Warn("runtime: the operator-intent log was superseded by another history: ending every substitute it held", "ended", len(ended))
		r.publishSub(ended)
	}
	n := 0
	for _, e := range ended {
		if e.Kind == SubstituteEventStop {
			n++
		}
	}
	return n
}

// DropRetainedIntent removes the operator intent the retain store keeps of
// the log (the active substitutes, and the alarms' acknowledgements and
// shelves), keeping its other state and the substitutes' HighID, and says
// how many substitutes and alarms it removed (#1029 3c-3c-2a): a
// controller whose operator-intent log was superseded by another history
// moves its log aside at its start, and what it retained of the old
// history (substitutes numbered by that history's seqs, shelves a person
// accepted to lose) must never be restored. Call it at the start, before
// Run: nothing else reads or writes the store then.
func (r *Runtime) DropRetainedIntent() (int, error) {
	if r.retainStore == nil {
		return 0, nil
	}
	st, err := r.retainStore.Load()
	if err != nil {
		return 0, err
	}
	n := len(st.Alarms)
	if mutRuntime("stale_retained_alarms_kept") {
		n = 0
	}
	if st.Substitutes != nil {
		n += len(st.Substitutes.Entries)
	}
	if n == 0 {
		return 0, nil
	}
	if st.Substitutes != nil {
		st.Substitutes.Entries, st.Substitutes.LastSeenMs = nil, 0
	}
	if !mutRuntime("stale_retained_alarms_kept") {
		st.Alarms, st.AlarmFlood = nil, nil
	}
	return n, r.retainStore.Save(st)
}
