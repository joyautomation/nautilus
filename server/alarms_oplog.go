package server

import (
	"context"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/joyautomation/nautilus/alarm"
	"github.com/joyautomation/nautilus/oplog"
	"github.com/joyautomation/nautilus/retain"
	"github.com/joyautomation/nautilus/runtime"
)

// Alarm acknowledgements and shelves through the replicated log of
// operator intent (#710 PR 3b-2c), as substitutes go (substitutes_oplog.go).
//
// In a redundant pair an ack, a shelve or an unshelve is validated against
// the leader's engine, then proposed to the log; every replica applies the
// committed entry (AlarmLog.Apply). The leading replica applies it to its
// live engine, which journals it; a standby, whose engine does not
// evaluate, only records it in the replicated alarm view. At a takeover,
// after the log's catch-up and before the first scan, the new leader
// restores that view (PairAlarms): its acknowledgements held for each
// alarm's first activation, its shelves until their own ends. A takeover
// whose catch-up failed carries no acknowledgement at all: those alarms
// re-annunciate (the safe direction, ISA-18.2), and the alarm journal says
// "acks not carried: takeover unsynced"; shelves still carry.
//
// # Occurrences
//
// An acknowledgement is of an occurrence. The invariant (#1033 review):
// the view holds an ack for an alarm only while the leader's engine has
// that alarm's current occurrence acknowledged (ack-active, or shelved
// over it). So:
//   - an ack that would change nothing is refused (409), never recorded;
//   - every transition out of ack-active on the leader (a return to
//     normal, an RTN to unack-rtn of a latched alarm, a new activation, a
//     suppress) proposes an ack-clear, retried until committed, batched;
//   - the leader reconciles the view against its engine at every log tick
//     (driveLog), clearing any ack its engine doesn't hold;
//   - an unsynced takeover clears the view's acks outright.
// The residual is the leader dying between such a transition and the
// ack-clear's commit: one log round trip and a scan with the peer up, up
// to the peer's outage plus a retry (2 s) with it down. Acks bound to an
// activation proper come with #715.
//
// # Takeovers reset the engine
//
// A replica that led before keeps an engine frozen at its last leadership.
// Every takeover resets its operator state (alarm.Engine.
// ResetOperatorState) before restoring the view, so an ack of an
// occurrence that ended, or a shelf undone, while it stood by never
// survives in it (#1033 review).
//
// # Live only once restored
//
// A replica wins the lease before its runtime takes over: the takeover
// waits for live inputs and the log's catch-up (#626), and only then
// restores the view into the engine. Until it has, in this term, the
// engine still holds a standby's (or an earlier leadership's) state, and
// nothing may be judged from it (#1033 CI): Reconcile would clear every
// carried ack, an ack-clear would be proposed from the restore's own
// transitions, and an HTTP ack or shelve would be checked against stale
// occurrences. So the log is live (live) only while this replica leads
// AND its takeover restored the view under the current term; alarm writes
// arriving in between are refused (503, retry), and committed entries
// only update the view, which the restore then applies. A same-term
// resume (no takeover) stays live.
//
// # Late entries lapse
//
// An ack or a shelve proposed while the log had no majority, and committed
// by a later leader more than AlarmIntentCommitBound after it was
// proposed, is lapsed (oplog.KindLapse, as a late substitute start is): it
// would act on whatever occurrence is live by then. Unshelves and
// ack-clears never lapse: late, they are safe.

// AlarmIntentCommitBound is how old an ack or a shelve may be, by the
// committing leader's clock, and still apply (the substitutes' bound).
const AlarmIntentCommitBound = runtime.SubstituteStartCommitBound

// PairNoMajorityAlarms is a pair's no-majority refusal for alarm writes.
const PairNoMajorityAlarms = "operator-intent log has no majority: neither the other controller nor the witness answers; alarm acknowledgements and shelves cannot be made until one of them returns"

// AlarmLog adapts an oplog node for an alarm engine.
type AlarmLog struct {
	eng     *alarm.Engine
	leading func() bool
	term    func() uint64
	node    *oplog.Node
	refusal string

	// restored is the term whose takeover restored the view into the
	// engine (live). liveMu orders a committed entry's apply against the
	// restore: an entry lands either in the view the restore reads, or
	// on the engine after it, never in neither.
	restored atomic.Uint64
	liveMu   sync.Mutex

	mu   sync.Mutex
	view map[string]alarmIntent

	clrMu      sync.Mutex
	clr        []string
	clrRunning bool
	clrRetry   time.Duration
}

// alarmIntent is the view's state for one alarm.
type alarmIntent struct {
	acked        bool
	ackBy        string
	ackMs        int64
	ackSeq       uint64 // the log entry the ack came from (a KindLapse may name it)
	shelfUntilMs int64
	shelfBy      string
	shelfSeq     uint64
	shelfSet     bool // the log shelved or unshelved it: its shelf is the log's
}

// NewAlarmLog is an AlarmLog for eng; leading reports whether this replica
// holds the lease now, and term the term it holds it under (the elector's
// fencing token). The engine is live once a takeover restored the view in
// that term (see live). It registers the engine hook that proposes
// ack-clears. Attach the node once it exists (its Apply must already
// reach Apply).
func NewAlarmLog(eng *alarm.Engine, leading func() bool, term func() uint64) *AlarmLog {
	l := &AlarmLog{eng: eng, leading: leading, term: term, view: map[string]alarmIntent{}, refusal: PairNoMajorityAlarms, clrRetry: 2 * time.Second}
	eng.OnEvent(l.engineEvent)
	return l
}

// live reports whether this replica's engine is the live one: it leads,
// and its takeover restored the view under the term it leads in.
func (l *AlarmLog) live() bool {
	if mutServer("reconcile_before_restore") {
		return l.leading()
	}
	return l.leading() && l.restored.Load() == l.term()
}

// restoring runs a takeover's restore under liveMu and marks the log live
// for the term held when it began.
func (l *AlarmLog) restoring(restore func()) {
	term := l.term()
	l.liveMu.Lock()
	defer l.liveMu.Unlock()
	restore()
	l.restored.Store(term)
}

// Attach sets the oplog node proposals go to.
func (l *AlarmLog) Attach(n *oplog.Node) { l.node = n }

// Apply applies a committed entry if it is an alarm kind, and reports
// whether it was one.
func (l *AlarmLog) Apply(e oplog.Entry) (bool, error) {
	l.liveMu.Lock()
	defer l.liveMu.Unlock()
	live := l.live()
	if e.Lapsed && (e.Kind == oplog.KindAck || e.Kind == oplog.KindShelve) {
		slog.Warn("alarm: an operator's ack or shelve lapsed: it was committed too late to apply to the occurrence it was for", "kind", e.Kind, "ids", e.IDs, "id", e.ID, "by", e.By)
		return true, nil
	}
	switch e.Kind {
	case oplog.KindAck:
		l.mu.Lock()
		for _, id := range e.IDs {
			v := l.view[id]
			v.acked, v.ackBy, v.ackMs, v.ackSeq = true, e.By, e.TS, e.Seq
			l.view[id] = v
		}
		l.mu.Unlock()
		if live {
			if _, err := l.eng.Ack(e.IDs, e.By); err != nil {
				slog.Warn("alarm: a committed acknowledgement did not apply", "ids", e.IDs, "err", err)
			}
		}
	case oplog.KindShelve:
		l.mu.Lock()
		v := l.view[e.ID]
		v.shelfUntilMs, v.shelfBy, v.shelfSet, v.shelfSeq = e.Until, e.By, true, e.Seq
		l.view[e.ID] = v
		l.mu.Unlock()
		if live {
			if err := l.eng.Shelve(e.ID, time.UnixMilli(e.Until), e.By); err != nil {
				slog.Warn("alarm: a committed shelve did not apply", "id", e.ID, "err", err)
			}
		}
	case oplog.KindUnshelve:
		l.mu.Lock()
		v := l.view[e.ID]
		v.shelfUntilMs, v.shelfBy, v.shelfSet = 0, "", true
		l.view[e.ID] = v
		l.mu.Unlock()
		if live {
			if err := l.eng.Unshelve(e.ID, e.By); err != nil {
				slog.Warn("alarm: a committed unshelve did not apply", "id", e.ID, "err", err)
			}
		}
	case oplog.KindAckClear:
		l.mu.Lock()
		for _, id := range append(slices.Clone(e.IDs), e.ID) {
			if v, ok := l.view[id]; ok && id != "" {
				v.acked, v.ackBy, v.ackMs, v.ackSeq = false, "", 0, 0
				l.view[id] = v
			}
		}
		l.mu.Unlock()
	case oplog.KindLapse:
		// A replica that applied a named ack or shelve before the lapse
		// reached it (a 3-replica case) drops it from its view; the leader
		// that wrote the lapse never applied it.
		l.mu.Lock()
		for id, v := range l.view {
			if v.acked && slices.Contains(e.Refs, v.ackSeq) {
				v.acked, v.ackBy, v.ackMs, v.ackSeq = false, "", 0, 0
			}
			if v.shelfSet && v.shelfUntilMs > 0 && slices.Contains(e.Refs, v.shelfSeq) {
				v.shelfUntilMs, v.shelfBy, v.shelfSeq = 0, "", 0
			}
			l.view[id] = v
		}
		l.mu.Unlock()
		return false, nil // the substitutes' Apply sees it too
	default:
		return false, nil
	}
	return true, nil
}

// engineEvent hears the leader's engine (under its lock: no blocking, no
// engine calls). Any transition that leaves an alarm not ack-active ends
// the acknowledged occurrence: its ack is cleared in the log. A shelve
// keeps it (shelved over ack-active still holds the ack); its unshelve
// lands on the state the shelf interrupted, judged like any other.
func (l *AlarmLog) engineEvent(ev alarm.Event) {
	if ev.State == "ack-active" || ev.Kind == alarm.KindShelve || ev.State == "shelved" || mutServer("ack_clear_off") {
		return
	}
	if !l.live() {
		return
	}
	l.mu.Lock()
	acked := l.view[ev.ID].acked
	l.mu.Unlock()
	if acked {
		l.queueClear(ev.ID)
	}
}

// Reconcile clears from the log any ack the leader's engine doesn't hold
// (an occurrence that ended by a path no event caught, or a view left
// stale by an unsynced takeover). naut run calls it at every log tick.
// Before this term's takeover restored the view, the engine is not the
// leader's yet, and nothing is judged (see live).
func (l *AlarmLog) Reconcile() {
	if !l.live() || mutServer("ack_clear_off") {
		return
	}
	held := l.eng.AckHeldIDs()
	l.mu.Lock()
	var stale []string
	for id, v := range l.view {
		if v.acked && !held[id] {
			stale = append(stale, id)
		}
	}
	l.mu.Unlock()
	for _, id := range stale {
		l.queueClear(id)
	}
}

// dropAcks clears every ack from this replica's view (an unsynced
// takeover: the log could not say which are current).
func (l *AlarmLog) dropAcks() {
	l.mu.Lock()
	defer l.mu.Unlock()
	for id, v := range l.view {
		v.acked, v.ackBy, v.ackMs, v.ackSeq = false, "", 0, 0
		l.view[id] = v
	}
}

// Lapse is the log's Options.Lapse for alarm intent: an ack or a shelve
// a new leader commits from an earlier term lapses when it was proposed
// more than AlarmIntentCommitBound before now, on that leader's clock.
func (l *AlarmLog) Lapse(e oplog.Entry, now time.Time) bool {
	return (e.Kind == oplog.KindAck || e.Kind == oplog.KindShelve) && now.Sub(time.UnixMilli(e.TS)) > AlarmIntentCommitBound
}

// queueClear proposes an ack-clear in the background, retrying what the
// log does not take (no majority) until it does; the ids queued meanwhile
// go in one entry.
func (l *AlarmLog) queueClear(id string) {
	l.clrMu.Lock()
	defer l.clrMu.Unlock()
	if !slices.Contains(l.clr, id) {
		l.clr = append(l.clr, id)
	}
	if !l.clrRunning && l.node != nil {
		l.clrRunning = true
		go l.proposeClears()
	}
}

func (l *AlarmLog) proposeClears() {
	for {
		l.clrMu.Lock()
		if len(l.clr) == 0 {
			l.clrRunning = false
			l.clrMu.Unlock()
			return
		}
		ids := slices.Clone(l.clr)
		l.clrMu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), substituteProposeWait)
		_, err := l.node.Propose(ctx, oplog.Entry{Kind: oplog.KindAckClear, IDs: ids, By: "system"})
		cancel()
		if err != nil {
			slog.Warn("alarm: ack-clear not committed yet; retrying", "ids", ids, "err", err)
			time.Sleep(l.clrRetry)
			continue
		}
		l.clrMu.Lock()
		l.clr = slices.DeleteFunc(l.clr, func(id string) bool { return slices.Contains(ids, id) })
		l.clrMu.Unlock()
	}
}

// Held is the alarm state a takeover restores: from the view when the
// log's catch-up succeeded (the log is the truth); when it failed, only
// the shelves (the view's, else the retained ones), never an ack.
func (l *AlarmLog) Held(synced bool, retained map[string]retain.AlarmRetain) map[string]retain.AlarmRetain {
	if synced && mutServer("alarm_view_ignored") {
		return maps.Clone(retained) // the replica's own retain, as before the log
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	out := map[string]retain.AlarmRetain{}
	if !synced {
		for id, r := range retained {
			if r.ShelfUntilMs > 0 {
				out[id] = retain.AlarmRetain{ShelfUntilMs: r.ShelfUntilMs, ShelfBy: r.ShelfBy}
			}
		}
	}
	for id, v := range l.view {
		r := out[id]
		if synced && v.acked {
			r.Acked, r.AckBy, r.AckMs = true, v.ackBy, v.ackMs
		}
		if v.shelfSet {
			r.ShelfUntilMs, r.ShelfBy = v.shelfUntilMs, v.shelfBy // the log's shelf, or none
		}
		if r == (retain.AlarmRetain{}) {
			delete(out, id)
			continue
		}
		out[id] = r
	}
	return out
}

// hasAcks reports whether a restore without the log would have carried an
// acknowledgement (for the "acks not carried" note).
func (l *AlarmLog) hasAcks(retained map[string]retain.AlarmRetain) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, v := range l.view {
		if v.acked {
			return true
		}
	}
	for _, r := range retained {
		if r.Acked {
			return true
		}
	}
	return false
}

// PairAlarms is a pair's alarm retainer for the runtime (SetAlarms): it
// saves what the engine holds, and restores from the log's view at a
// takeover (runtime calls RestoreAlarmsSynced with the catch-up's result).
type PairAlarms struct {
	Engine *alarm.Engine
	Log    *AlarmLog
}

func (p PairAlarms) RetainedAlarms() map[string]retain.AlarmRetain { return p.Engine.RetainedAlarms() }

// RestoreAlarms is a restore with no log catch-up to go by: as unsynced.
func (p PairAlarms) RestoreAlarms(m map[string]retain.AlarmRetain) { p.RestoreAlarmsSynced(m, false) }

// RestoreAlarmsSynced restores the view (Held) into the engine.
func (p PairAlarms) RestoreAlarmsSynced(m map[string]retain.AlarmRetain, synced bool) {
	p.Log.restoring(func() { p.restore(m, synced) })
}

func (p PairAlarms) restore(m map[string]retain.AlarmRetain, synced bool) {
	if !synced {
		if p.Log.hasAcks(m) {
			p.Engine.Note("system", "acks not carried: takeover unsynced (the operator-intent log could not be caught up, so acknowledged alarms re-annunciate)")
			slog.Warn("alarm: acknowledgements not carried: the takeover's log catch-up failed; acknowledged alarms re-annunciate")
		}
		p.Log.dropAcks() // the view can't say which acks are current: none are
	}
	// A frozen engine from an earlier leadership forgets what it held;
	// the view (or, unsynced, the shelves alone) is what applies now.
	if !mutServer("engine_not_reset") {
		p.Engine.ResetOperatorState()
	}
	p.Engine.RestoreAlarms(p.Log.Held(synced, m))
}

// PairAlarmsTakingOver refuses an alarm write that reaches a leader whose
// takeover has not restored the alarm state yet (see AlarmLog.live).
const PairAlarmsTakingOver = "this controller is taking over: its alarm state is not restored yet, so an acknowledgement or a shelve can't be checked; retry in a moment"

// refuseAlarmBeforeRestore answers PairAlarmsTakingOver before anything
// is read from the engine: in that window it holds a standby's state.
func (s *Server) refuseAlarmBeforeRestore(w http.ResponseWriter) bool {
	if s.alarmLog.live() {
		return false
	}
	w.Header().Set("Retry-After", "1")
	refuse(w, PairAlarmsTakingOver, http.StatusServiceUnavailable)
	return true
}

// refuseAlarmWithoutMajority answers the no-majority refusal before an
// alarm write is proposed (the substitutes' rule, substitutes_oplog.go).
func (s *Server) refuseAlarmWithoutMajority(w http.ResponseWriter) bool {
	st := s.alarmLog.node.Status()
	if st.Writable {
		return false
	}
	msg := s.alarmLog.refusal
	if st.NotSynced {
		msg = LogNotSynced
	}
	if st.Reason != "" {
		msg += " (" + st.Reason + ")"
	}
	refuse(w, msg, http.StatusServiceUnavailable)
	return true
}

// alarmAckThroughLog proposes an acknowledgement: "all" becomes the leader's
// unacknowledged alarms by id, since a standby can't know them.
func (s *Server) alarmAckThroughLog(w http.ResponseWriter, r *http.Request, ids []string, by string) {
	if s.refuseAlarmBeforeRestore(w) {
		return
	}
	if ids == nil {
		ids = s.alarms.UnackedIDs()
		if len(ids) == 0 {
			writeJSON(w, http.StatusOK, map[string]int{"acked": 0})
			return
		}
	}
	ids, err := s.alarms.AckableIDs(ids)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if len(ids) == 0 {
		// Nothing to acknowledge: recorded in the log, it would sit in the
		// view as an ack no occurrence holds (#1033 review).
		refuse(w, "nothing to acknowledge: those alarms are not active-unacknowledged or returned-unacknowledged", http.StatusConflict)
		return
	}
	n := len(ids)
	if s.refuseAlarmWithoutMajority(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), substituteProposeWait)
	defer cancel()
	if _, err := s.alarmLog.node.Propose(ctx, oplog.Entry{Kind: oplog.KindAck, IDs: ids, By: by}); err != nil {
		unknownOutcome(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"acked": n})
}

// shelveThroughLog proposes a shelve, or an unshelve when until is zero.
func (s *Server) shelveThroughLog(w http.ResponseWriter, r *http.Request, id string, until time.Time, by string) {
	if s.refuseAlarmBeforeRestore(w) {
		return
	}
	e := oplog.Entry{Kind: oplog.KindUnshelve, ID: id, By: by}
	err := error(nil)
	if until.IsZero() {
		err = s.alarms.CheckUnshelve(id)
	} else {
		err = s.alarms.CheckShelve(id, until)
		e.Kind, e.Until = oplog.KindShelve, until.UnixMilli()
	}
	if err != nil {
		http.Error(w, err.Error(), alarmErrStatus(err))
		return
	}
	if s.refuseAlarmWithoutMajority(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), substituteProposeWait)
	defer cancel()
	if _, err := s.alarmLog.node.Propose(ctx, e); err != nil {
		unknownOutcome(w, err)
		return
	}
	s.writeAlarmRecord(w, id)
}
