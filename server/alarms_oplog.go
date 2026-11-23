package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/joyautomation/nautilus/alarm"
	"github.com/joyautomation/nautilus/alarm/acklink"
	"github.com/joyautomation/nautilus/audit"
	"github.com/joyautomation/nautilus/lang/ir"
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
// to the peer's outage plus a retry (2 s) with it down.
//
// Since #715 an ack is bound to an occurrence (alarm.Engine.AckIDsAt): the
// entry names the occurrence each alarm was on when the ack was checked,
// and the leader applies it only to that occurrence, so an ack committed
// after its occurrence ended (and another began) acknowledges nothing.
// The view keeps the occurrence with the ack, and a takeover restores the
// ack only with that occurrence, only if the field re-asserts it
// (alarm.Engine.RestoreAlarms). The residual above is narrowed, not
// closed: if the leader dies after an acknowledged occurrence ended and a
// new one began, but before the ack-clear (which names the ended
// occurrence) committed, the new leader takes the field's condition for
// the old occurrence and carries its ack to it.
//
// # Occurrences across a takeover
//
// For the alarms an escalation policy covers (SetOccurrenceMatch), the
// leader keeps the view's occurrences current at every log tick
// (Reconcile): occurrence-start for one that began, or whose wait on a
// person moved, occurrence-end for one that ended; batched, one entry of
// each kind at most per tick. A takeover continues them, so escalation
// goes on at its level (the new leader's dispatcher resends the current
// level once) instead of starting again at level 1. One that began within
// a tick of the leader's death is not in the view: it starts afresh,
// re-paging from level 1 (the safe direction).
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
	clrOcc     map[string]string // the occurrence each queued ack-clear ends
	clrRetry   time.Duration
	clrRunning bool

	// match says which alarms' occurrences the view keeps for a takeover
	// (those an escalation policy covers); nil: none. occBusy is set
	// while an occurrence proposal is in flight.
	match   atomic.Pointer[func(alarm.Def) bool]
	occBusy atomic.Bool
	oosBusy atomic.Bool // an endOutOfService is in flight
	detBusy atomic.Bool // a syncDetect proposal is in flight

	// flood is the alarm flood the leader's engine is in (#1249,
	// KindAlarmDetect), nil when none. Under mu.
	flood *retain.AlarmFlood
	// heardMs is the latest instant this replica knows a leader was
	// live: the newest applied entry's TS (the leader's clock), or a log
	// tick while this replica led live. With the node's LeaderHeard it
	// dates the view's episodes at a takeover (vouchedMs).
	heardMs atomic.Int64

	// adopt holds, by alarm, a committed ack of an occurrence the live
	// engine is not on, until the log is caught up (Reconcile): an entry
	// after it that ends or replaces that occurrence (an ack-clear, an
	// occurrence-end or -start, a later ack) cancels it; what is left
	// adopts the standing alarm's occurrence (alarm.Engine.AckCommitted).
	// Under mu.
	adopt map[string]adoption

	// ended hands the ids of alarms that left ack-active on the live
	// leader from the engine's hook (under the engine's lock, on the scan)
	// to clearEnded, which judges them against the view: the scan never
	// waits on mu (#1053: control never waits on operator data).
	ended chan endedOcc
	// ackGen counts the acks the view has taken; each view ack keeps the
	// count it was taken at, and each hand-off the count when its event
	// fired, so a late judgement never clears an ack taken after its
	// event. The ONE place the view sets acked is Apply's KindAck, which
	// bumps it there; any new path that sets acked must bump it too
	// (TestOnlyKindAckSetsAViewAck). Every engine ack from the log comes
	// after that write: applyAck in the same Apply, AckCommitted (an
	// adoption) later, a takeover's restore later (it reads Held under
	// liveMu, which excludes Apply). The count says only that the ack is
	// not newer than the event, not that the event ended it: an event can
	// fire between the view's ack and the engine's, so clearIfAcked also
	// asks the engine, under liveMu (which waits out that Apply).
	ackGen atomic.Uint64

	// seen hears every committed alarm entry this replica proposed, once
	// applied (observe: the server's unknown-outcome resolutions).
	seen atomic.Pointer[func(oplog.Entry)]
	// journal records alarm lapses (observe: the server's journal).
	journal atomic.Pointer[func([]audit.Entry)]
	// lapsed holds the lapsed acks and shelves by seq until the KindLapse
	// naming them is applied (under mu, bounded by maxLapsedAlarms).
	lapsed map[uint64]oplog.Entry

	// spent holds the ack links (#715) the log's acks were made by, by
	// link ID, with the TS of the entry that spent each, on every replica
	// (live or not): a link is spent once, so a later ack naming one is
	// not applied. Built from the log alone (an entry's TS, never this
	// replica's clock), so every replica holds the same set, and the node
	// replays its file into Apply at start, so it survives a restart. Kept
	// when the view is dropped (a superseded history): a link refused once
	// too often costs a visit to the HMI, one honoured twice an ack nobody
	// gave. Under mu.
	spent map[string]int64

	// oos is the log's out-of-service windows (#715 PR 5), by equipment;
	// equip each equipment's alarm IDs (the definitions are fixed for the
	// engine's life). A synced takeover restores the windows on every
	// alarm of their equipment; an unsynced one carries none (a stale
	// window would hide alarms). Under mu.
	oos   map[string]oosIntent
	equip map[string][]string
	// ending holds the windows an unsynced takeover did not carry, by
	// equipment, until the in-service Reconcile proposes for each
	// commits: Held never carries them (a later synced takeover must not
	// bring back a hide this one showed and journalled as ended). Under mu.
	ending map[string]oosIntent
}

// oosIntent is one equipment's out-of-service window, as the log has it.
type oosIntent struct {
	name                        string // the equipment as the entry named it
	untilMs, grantedMs, startMs int64
	by, reason                  string
	seq                         uint64 // the entry it came from (a KindLapse may name it)
}

// linkSpentFor is how long the log remembers a spent link: past it no
// link minted when it was spent can still check (acklink.MaxTTL, and the
// clock skew acklink.Verify allows, with room).
const linkSpentFor = acklink.MaxTTL + time.Hour

// LinkSpent reports whether the log has an ack made by link id.
func (l *AlarmLog) LinkSpent(id string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, ok := l.spent[id]
	return ok
}

// spend records e's link as spent, and reports false when it already
// was: e is then not applied. It prunes the links spent longer ago than
// any could still check, by e's TS. Under mu.
func (l *AlarmLog) spend(e oplog.Entry) bool {
	if e.Link == "" {
		return true
	}
	if _, ok := l.spent[e.Link]; ok && !mutServer("link_replay_applied") {
		return false
	}
	if l.spent == nil {
		l.spent = map[string]int64{}
	}
	for id, ts := range l.spent {
		if e.TS-ts > linkSpentFor.Milliseconds() {
			delete(l.spent, id)
		}
	}
	l.spent[e.Link] = e.TS
	return true
}

// maxLapsedAlarms bounds the lapsed entries waiting for their KindLapse.
const maxLapsedAlarms = 1024

// observe hands the log the server's hooks: seen for each committed entry
// this replica proposed, journal for an operator's lapsed ack or shelve.
func (l *AlarmLog) observe(seen func(oplog.Entry), journal func([]audit.Entry)) {
	l.seen.Store(&seen)
	l.journal.Store(&journal)
}

// tell passes e to seen when this replica proposed it. Not while the node
// replays its file at startup (Attach comes after): what this process did
// not propose is never resolved by it.
func (l *AlarmLog) tell(e oplog.Entry) {
	if f := l.seen.Load(); f != nil && l.node != nil && e.Origin == l.node.Self() {
		(*f)(e)
	}
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

	// occ, activeMs and waitMs are the occurrence the log knows the alarm
	// is on (#715): from an ack (the occurrence it acknowledged) or an
	// occurrence-start. "" when none.
	occ      string
	activeMs int64
	waitMs   int64

	ackGen uint64 // AlarmLog.ackGen when the view took its ack

	// chatSinceMs, chatActs and chatEpisodes are the chattering episode
	// the leader's engine is in (#1249, KindAlarmDetect): its start
	// names it. chatSinceMs 0: none.
	chatSinceMs            int64
	chatActs, chatEpisodes int
}

// adoption is a committed ack waiting to adopt an occurrence (AlarmLog.adopt).
type adoption struct {
	occ      string
	activeMs int64
	by       string
}

// clearOcc forgets v's occurrence.
func (v *alarmIntent) clearOcc() { v.occ, v.activeMs, v.waitMs = "", 0, 0 }

// NewAlarmLog is an AlarmLog for eng; leading reports whether this replica
// holds the lease now, and term the term it holds it under (the elector's
// fencing token). The engine is live once a takeover restored the view in
// that term (see live). It registers the engine hook that proposes
// ack-clears. Attach the node once it exists (its Apply must already
// reach Apply).
func NewAlarmLog(eng *alarm.Engine, leading func() bool, term func() uint64) *AlarmLog {
	l := &AlarmLog{eng: eng, leading: leading, term: term, view: map[string]alarmIntent{}, refusal: PairNoMajorityAlarms, clrRetry: 2 * time.Second,
		ended: make(chan endedOcc, endedQueue), oos: map[string]oosIntent{}, equip: map[string][]string{}, ending: map[string]oosIntent{}}
	for _, r := range eng.Records() {
		k := ir.NameKey(r.Equipment)
		l.equip[k] = append(l.equip[k], r.ID)
	}
	eng.OnEvent(l.engineEvent)
	go l.clearEnded()
	return l
}

// endedOcc is one alarm that left ack-active, with the occurrence it was
// on then.
type endedOcc struct {
	id, occ string
	gen     uint64 // AlarmLog.ackGen when the event fired
	term    uint64 // the leadership it fired in
}

// endedQueue is the hand-off from the engine's hook to clearEnded: deep
// enough for an alarm flood; past it, the next log tick's Reconcile finds
// the acks left behind.
const endedQueue = 4096

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
	l.mu.Lock()
	l.adopt = nil // an earlier leadership's pending adoptions are not this one's
	l.mu.Unlock()
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
	l.heard(e.TS)
	live := l.live()
	if e.Lapsed && (e.Kind == oplog.KindAck || e.Kind == oplog.KindShelve || e.Kind == oplog.KindOutOfService) {
		slog.Warn("alarm: an operator's ack or shelve lapsed: it was committed too late to apply to the occurrence it was for", "kind", e.Kind, "ids", e.IDs, "id", e.ID, "by", e.By)
		l.mu.Lock()
		if l.lapsed == nil {
			l.lapsed = map[uint64]oplog.Entry{}
		}
		l.lapsed[e.Seq] = e
		if len(l.lapsed) > maxLapsedAlarms {
			delete(l.lapsed, slices.Min(slices.Collect(maps.Keys(l.lapsed))))
		}
		l.mu.Unlock()
		l.tell(e)
		return true, nil
	}
	var notes []string // cancelled adoptions, journalled once mu is released
	defer func() {
		if live { // a standby's engine journals nothing of the leader's
			for _, n := range notes {
				l.eng.Note("system", n)
			}
		}
		switch e.Kind {
		case oplog.KindAck, oplog.KindShelve, oplog.KindUnshelve, oplog.KindOutOfService, oplog.KindInService:
			l.tell(e)
		}
	}()
	switch e.Kind {
	case oplog.KindAck:
		occ := occsByID(e.Occs)
		l.mu.Lock()
		if !l.spend(e) {
			// The link was used already (two people pressing at once, or
			// the same press on both controllers): the first ack stands.
			l.mu.Unlock()
			notes = append(notes, "ack of "+strings.Join(e.IDs, ",")+" by "+e.By+" not applied: its link was already used")
			return true, nil
		}
		for _, id := range e.IDs {
			delete(l.adopt, id) // a later ack decides for itself
			v := l.view[id]
			v.acked, v.ackBy, v.ackMs, v.ackSeq = true, e.By, e.TS, e.Seq
			v.ackGen = l.ackGen.Add(1)
			if o, ok := occ[id]; ok {
				v.occ, v.activeMs, v.waitMs = o.Occ, o.ActiveMs, 0
			}
			l.view[id] = v
		}
		l.mu.Unlock()
		if live {
			l.applyAck(e, occ)
		}
	case oplog.KindShelve:
		l.mu.Lock()
		v := l.view[e.ID]
		v.shelfUntilMs, v.shelfBy, v.shelfSet, v.shelfSeq = e.Until, e.By, true, e.Seq
		l.view[e.ID] = v
		l.mu.Unlock()
		if live {
			if err := l.eng.ShelveWith(e.ID, time.UnixMilli(e.Until), e.By, e.Reason); err != nil {
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
	case oplog.KindOutOfService:
		l.mu.Lock()
		k := ir.NameKey(e.ID)
		l.oos[k] = oosIntent{name: e.ID, untilMs: e.Until, grantedMs: e.Until - e.TS, startMs: e.TS, by: e.By, reason: e.Reason, seq: e.Seq}
		delete(l.ending, k) // a new window: what ends now is this one
		l.mu.Unlock()
		if live {
			if err := l.eng.TakeOutOfService(e.ID, time.UnixMilli(e.Until), e.By, e.Reason); err != nil {
				slog.Warn("alarm: a committed out of service did not apply", "equipment", e.ID, "err", err)
			}
		}
	case oplog.KindInService:
		// Ref names the window a system in-service (Reconcile) ends: one
		// an operator replaced since is not ended by it. A person's names
		// none and ends whatever window stands.
		l.mu.Lock()
		k := ir.NameKey(e.ID)
		if o, ok := l.ending[k]; ok && (e.Ref == 0 || o.seq == e.Ref) {
			delete(l.ending, k)
		}
		o, ok := l.oos[k]
		stands := ok && (e.Ref == 0 || o.seq == e.Ref)
		if stands {
			delete(l.oos, k)
		}
		l.mu.Unlock()
		if live && stands {
			if err := l.eng.ReturnToService(e.ID, e.By); err != nil {
				slog.Warn("alarm: a committed return to service did not apply", "equipment", e.ID, "err", err)
			}
		}
	case oplog.KindAckClear:
		occ := occsByID(e.Occs)
		l.mu.Lock()
		for _, id := range append(slices.Clone(e.IDs), e.ID) {
			if a, ok := l.adopt[id]; ok && (occ[id].Occ == "" || occ[id].Occ == a.occ) && !mutServer("adopt_ignores_log") {
				notes = append(notes, l.cancelAdopt(id, "its ack was cleared: the occurrence left ack-active before this takeover"))
			}
			if v, ok := l.view[id]; ok && id != "" {
				if o, named := occ[id]; named && o.Occ != "" && v.occ != "" && o.Occ != v.occ && !mutServer("ack_clear_any_occurrence") {
					continue // it ends another occurrence's ack: this one is newer
				}
				v.acked, v.ackBy, v.ackMs, v.ackSeq = false, "", 0, 0
				// The acknowledged occurrence left ack-active: it ended, or
				// it is in unack-rtn, which no takeover continues. Its
				// occurrence goes with its ack, unless a newer one is in
				// the view already.
				if o, ok := occ[id]; ok && o.Occ == v.occ && !mutServer("ack_clear_keeps_occurrence") {
					v.clearOcc()
				}
				l.view[id] = v
			}
		}
		l.mu.Unlock()
	case oplog.KindOccurrenceStart:
		l.mu.Lock()
		for _, o := range e.Occs {
			if a, ok := l.adopt[o.ID]; ok && a.occ != o.Occ && !mutServer("adopt_ignores_log") {
				notes = append(notes, l.cancelAdopt(o.ID, "a later occurrence began before this takeover"))
			}
			v := l.view[o.ID]
			if v.acked && v.occ != "" && v.occ != o.Occ {
				// An ack of an occurrence that has ended (its ack-clear may
				// still be on its way): never carried to this one.
				v.acked, v.ackBy, v.ackMs, v.ackSeq = false, "", 0, 0
			}
			v.occ, v.activeMs, v.waitMs = o.Occ, o.ActiveMs, o.WaitMs
			l.view[o.ID] = v
		}
		l.mu.Unlock()
	case oplog.KindOccurrenceEnd:
		l.mu.Lock()
		for _, o := range e.Occs {
			if a, ok := l.adopt[o.ID]; ok && a.occ == o.Occ && !mutServer("adopt_ignores_log") {
				notes = append(notes, l.cancelAdopt(o.ID, "the occurrence ended before this takeover"))
			}
			if v, ok := l.view[o.ID]; ok && v.occ == o.Occ {
				v.clearOcc()
				if v.acked {
					v.acked, v.ackBy, v.ackMs, v.ackSeq = false, "", 0, 0
				}
				l.view[o.ID] = v
			}
		}
		l.mu.Unlock()
	case oplog.KindAlarmDetect:
		l.applyDetect(e)
	case oplog.KindLapse:
		// A replica that applied a named ack or shelve before the lapse
		// reached it (a 3-replica case) drops it from its view; the leader
		// that wrote the lapse never applied it.
		l.journalLapse(e)
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
		for eq, o := range l.oos {
			if slices.Contains(e.Refs, o.seq) {
				delete(l.oos, eq)
			}
		}
		for eq, o := range l.ending {
			if slices.Contains(e.Refs, o.seq) {
				delete(l.ending, eq)
			}
		}
		l.mu.Unlock()
		return false, nil // the substitutes' Apply sees it too
	default:
		return false, nil
	}
	return true, nil
}

// journalLapse records, once, the operators' acks and shelves the lapse e
// names: alarm.lapse, one entry per alarm, as the operator who made it
// (Principal, else By), so the journal says the action never took
// effect. Only the leader that wrote the lapse journals it, and not while
// the node replays its file (Attach comes after): a standby and a restart
// stay quiet. Recorded off the apply path (the journal fsyncs).
func (l *AlarmLog) journalLapse(e oplog.Entry) {
	l.mu.Lock()
	var named []oplog.Entry
	for _, seq := range e.Refs {
		if le, ok := l.lapsed[seq]; ok {
			named = append(named, le)
			delete(l.lapsed, seq)
		}
	}
	l.mu.Unlock()
	f := l.journal.Load()
	if f == nil || l.node == nil || e.Origin != l.node.Self() || len(named) == 0 {
		return
	}
	var out []audit.Entry
	for _, le := range named {
		ids := le.IDs
		if le.Kind == oplog.KindShelve || le.Kind == oplog.KindOutOfService {
			ids = []string{le.ID}
		}
		action := "alarm.ack"
		switch le.Kind {
		case oplog.KindShelve:
			action = "alarm.shelve"
		case oplog.KindOutOfService:
			action = "alarm.out-of-service"
		}
		for _, id := range ids {
			d := map[string]any{"action": action, "seq": le.Seq, "by": le.By, "ts": le.TS,
				"note": "committed too late to apply to the occurrence it was for: it never took effect"}
			if le.Kind == oplog.KindShelve || le.Kind == oplog.KindOutOfService {
				d["untilMs"] = le.Until
			}
			out = append(out, audit.Entry{User: principalOfEntryIn(le, d), Source: audit.SourceSystem, Action: "alarm.lapse",
				Target: id, Result: audit.ResultOK, Detail: d})
		}
	}
	go (*f)(out)
}

// engineEvent hears the leader's engine, under its lock, on the scan: it
// must neither block nor call the engine, so it takes no lock at all
// (live reads atomics) and hands the alarm to clearEnded. Any transition
// that leaves an alarm not ack-active ends the acknowledged occurrence:
// its ack is cleared in the log. A shelve keeps it (shelved over
// ack-active still holds the ack); its unshelve lands on the state the
// shelf interrupted, judged like any other. A full hand-off drops the id:
// Reconcile, at the next log tick, clears an ack the engine doesn't hold.
func (l *AlarmLog) engineEvent(ev alarm.Event) {
	if ev.State == "ack-active" || ev.Kind == alarm.KindShelve || ev.State == "shelved" || mutServer("ack_clear_off") {
		return
	}
	if !l.live() {
		return
	}
	if mutServer("engine_event_waits") {
		l.clearIfAcked(endedOcc{ev.ID, ev.Occurrence, l.ackGen.Load(), l.term()})
		return
	}
	if mutServer("engine_event_takes_livemu") {
		l.liveMu.Lock()
		l.liveMu.Unlock() //nolint:staticcheck // the mutation: the hook waits on liveMu
	}
	if mutServer("engine_event_blocks") {
		l.ended <- endedOcc{ev.ID, ev.Occurrence, l.ackGen.Load(), l.term()}
		return
	}
	select {
	case l.ended <- endedOcc{ev.ID, ev.Occurrence, l.ackGen.Load(), l.term()}:
	default:
	}
}

// clearEnded clears the view's ack of each alarm the engine's hook handed
// over, off the scan.
func (l *AlarmLog) clearEnded() {
	for e := range l.ended {
		l.clearIfAcked(e)
	}
}

// clearIfAcked queues an ack-clear for e's alarm when the view's ack
// existed when the event fired, and is for the occurrence that left
// ack-active (or names none, from an older
// replica, or waits to adopt one: its alarm did not stand), naming the
// occurrence the view's ack is for. An ack the view took since, for a
// later occurrence, is not cleared.
func (l *AlarmLog) clearIfAcked(e endedOcc) {
	if !l.live() || l.term() != e.term {
		return // fired in another leadership; Reconcile judges this one's
	}
	// Under liveMu: an Apply that wrote the view's ack but has not acked
	// the engine yet finishes first, so the engine is asked after it.
	if !mutServer("clear_without_livemu") {
		l.liveMu.Lock()
	}
	l.mu.Lock()
	v := l.view[e.id]
	_, adopting := l.adopt[e.id]
	l.mu.Unlock()
	clear := v.acked && (v.ackGen <= e.gen || mutServer("clear_ack_taken_after")) &&
		(v.occ == "" || v.occ == e.occ || adopting || mutServer("clear_any_later_ack"))
	if clear && !mutServer("clear_while_engine_holds") {
		// The event fired between the view's ack and the engine's (an
		// unack-rtn alarm re-activating on the same occurrence, a shelf
		// expiring): the engine holds the ack now, and it stands.
		if occ, held := l.eng.AckHeld(e.id); held && (v.occ == "" || occ == v.occ) {
			clear = false
		}
	}
	if !mutServer("clear_without_livemu") {
		l.liveMu.Unlock()
	}
	if clear {
		l.queueClear(e.id, v.occ)
	}
}

// applyAckGap, when set (tests), runs inside Apply between the view's ack
// and the engine's.
var applyAckGap atomic.Pointer[func()]

// reconcileGap, when set (tests), runs inside Reconcile between its read
// of the engine's acks and its read of the view.
var reconcileGap atomic.Pointer[func()]

// Reconcile clears from the log any ack the leader's engine doesn't hold
// (an occurrence that ended by a path no event caught, or a view left
// stale by an unsynced takeover). naut run calls it at every log tick.
// Before this term's takeover restored the view, the engine is not the
// leader's yet, and nothing is judged (see live).
func (l *AlarmLog) Reconcile() {
	if !l.live() || mutServer("ack_clear_off") {
		return
	}
	l.heard(time.Now().UnixMilli()) // leading live: its engine's episodes are current
	l.adoptPending()                // first: an ack it applies is held below
	// The engine's acks and the view's, read together under liveMu: an
	// Apply between the two reads would make its fresh ack look stale.
	if !mutServer("reconcile_without_livemu") {
		l.liveMu.Lock()
	}
	held := l.eng.AckHeldIDs()
	oosHeld := map[string]bool{}
	for _, w := range l.eng.OutOfServiceList() {
		oosHeld[ir.NameKey(w.Equipment)] = true
	}
	if f := reconcileGap.Load(); f != nil {
		(*f)()
	}
	l.mu.Lock()
	var stale []string
	for id, v := range l.view {
		if _, waits := l.adopt[id]; waits {
			continue // its ack waits for the log to catch up, not cleared
		}
		if v.acked && !held[id] {
			stale = append(stale, id)
		}
	}
	// A window the log holds and the engine doesn't (an unsynced takeover
	// did not carry it; it expired on this clock; the engine refused it)
	// is ended in the log too, so a later takeover, on a clock behind
	// this one, never brings back a hide nobody sees now.
	var notHeld []oplog.Entry
	for eq, o := range l.oos {
		if !oosHeld[eq] {
			notHeld = append(notHeld, oplog.Entry{Kind: oplog.KindInService, ID: o.name, Ref: o.seq, By: "system", Reason: "ended on the leader"})
		}
	}
	for _, o := range l.ending {
		notHeld = append(notHeld, oplog.Entry{Kind: oplog.KindInService, ID: o.name, Ref: o.seq, By: "system", Reason: "not carried by a takeover"})
	}
	l.mu.Unlock()
	if !mutServer("reconcile_without_livemu") {
		l.liveMu.Unlock()
	}
	for _, id := range stale {
		l.queueClear(id, "")
	}
	l.endOutOfService(notHeld)
	l.syncOccurrences()
	l.syncDetect()
}

// endOutOfService proposes, in the background, a return to service for
// each equipment the log holds out of service and the live engine does
// not; what does not commit is found again at the next log tick.
// Each entry names its window (Ref, the window's seq), so one an operator
// took out again meanwhile is not ended by it.
func (l *AlarmLog) endOutOfService(ends []oplog.Entry) {
	if len(ends) == 0 || l.node == nil || !l.oosBusy.CompareAndSwap(false, true) {
		return
	}
	slices.SortFunc(ends, func(a, b oplog.Entry) int { return strings.Compare(a.ID, b.ID) })
	go func() {
		defer l.oosBusy.Store(false)
		for _, e := range ends {
			ctx, cancel := context.WithTimeout(context.Background(), substituteProposeWait)
			_, err := l.node.Propose(ctx, e)
			cancel()
			if err != nil {
				slog.Warn("alarm: return to service not committed yet; the next log tick tries again", "equipment", e.ID, "err", err)
				return
			}
		}
	}()
}

// endUncarried moves the log's windows to ending: an unsynced takeover
// did not carry them, and no later takeover may.
func (l *AlarmLog) endUncarried() {
	l.mu.Lock()
	defer l.mu.Unlock()
	for k, o := range l.oos {
		l.ending[k] = o
	}
	clear(l.oos)
}

// dropAcks clears every ack from this replica's view (an unsynced
// takeover: the log could not say which are current).
// superseded: this copy's log was superseded by another history (#1029
// 3c-3c-2a, oplog.Node.RetainedStale): the view, and what the retain
// store holds of acks and shelves, are that history's.
func (l *AlarmLog) superseded() bool {
	return l.node != nil && l.node.RetainedStale() && !mutServer("superseded_alarms_kept")
}

// dropView forgets every ack and shelf the view holds.
func (l *AlarmLog) dropView() {
	l.mu.Lock()
	defer l.mu.Unlock()
	clear(l.view)
	clear(l.oos)
	clear(l.ending)
	l.adopt = nil
	l.flood = nil
}

// EndSuperseded ends what this controller holds of the old history's
// alarm intent once its log is superseded (#1029 3c-3c-2a): the view's
// acks and shelves go, and a live engine (it leads) loses its operator
// state, so a shelf the person accepted to lose hides no alarm a scan
// longer. Everything re-annunciates. It reports whether it changed
// anything.
func (l *AlarmLog) EndSuperseded() bool {
	if !l.superseded() {
		return false
	}
	l.liveMu.Lock()
	defer l.liveMu.Unlock()
	l.mu.Lock()
	had := len(l.view) > 0 || len(l.oos) > 0
	l.mu.Unlock()
	l.dropView()
	if l.live() && had {
		l.eng.Note("system", "acks and shelves ended: this controller's operator-intent log was superseded by another history")
		l.eng.ResetOperatorState()
		return true
	}
	return had
}

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
	switch e.Kind {
	case oplog.KindAck, oplog.KindShelve, oplog.KindOutOfService:
		return now.Sub(time.UnixMilli(e.TS)) > AlarmIntentCommitBound
	}
	return false
}

// queueClear proposes an ack-clear in the background, retrying what the
// log does not take (no majority) until it does; the ids queued meanwhile
// go in one entry.
func (l *AlarmLog) queueClear(id, occ string) {
	l.clrMu.Lock()
	defer l.clrMu.Unlock()
	if !slices.Contains(l.clr, id) {
		l.clr = append(l.clr, id)
	}
	if occ != "" {
		if l.clrOcc == nil {
			l.clrOcc = map[string]string{}
		}
		l.clrOcc[id] = occ
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
		var occs []oplog.Occ
		for _, id := range ids {
			if o := l.clrOcc[id]; o != "" {
				occs = append(occs, oplog.Occ{ID: id, Occ: o})
			}
		}
		l.clrMu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), substituteProposeWait)
		_, err := l.node.Propose(ctx, oplog.Entry{Kind: oplog.KindAckClear, IDs: ids, Occs: occs, By: "system"})
		cancel()
		if err != nil {
			slog.Warn("alarm: ack-clear not committed yet; retrying", "ids", ids, "err", err)
			time.Sleep(l.clrRetry)
			continue
		}
		l.clrMu.Lock()
		l.clr = slices.DeleteFunc(l.clr, func(id string) bool { return slices.Contains(ids, id) })
		for _, o := range occs {
			if l.clrOcc[o.ID] == o.Occ {
				delete(l.clrOcc, o.ID)
			}
		}
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
		if synced && v.occ != "" && !mutServer("takeover_drops_occurrences") {
			// Continued only if the field re-asserts it; never from an
			// unsynced view (it can't say which is current), nor from
			// this replica's own retain store.
			r.Occurrence, r.ActiveMs, r.WaitMs = v.occ, v.activeMs, v.waitMs
		}
		if v.shelfSet {
			r.ShelfUntilMs, r.ShelfBy = v.shelfUntilMs, v.shelfBy // the log's shelf, or none
		}
		if synced && v.chatSinceMs != 0 && !mutServer("takeover_drops_episodes") {
			// Going as of the last time a live leader was heard: an
			// episode that ended on it would have been ended in the log
			// (one log tick's residual).
			r.ChatterSinceMs, r.ChatterLastMs = v.chatSinceMs, l.vouchedMsLocked()
			r.ChatterActs, r.ChatterEpisodes = v.chatActs, v.chatEpisodes
		}
		if r == (retain.AlarmRetain{}) {
			delete(out, id)
			continue
		}
		out[id] = r
	}
	if synced {
		// The log's windows, on every alarm of their equipment. Never
		// unsynced: the view can't say a window is still current, and a
		// stale one would hide alarms (fails toward showing them). What
		// is left is the least of what was granted and what this clock
		// says remains: one behind the leader's lengthens a window by its
		// lag at most, within the grant (Reconcile ends expired ones in
		// the log, so a replica far behind finds none to restore).
		now := time.Now().UnixMilli()
		for eq, o := range l.oos {
			left := min(o.grantedMs, o.untilMs-now)
			if left <= 0 {
				continue
			}
			for _, id := range l.equip[eq] {
				r := out[id]
				r.OOSEquipment, r.OOSUntilMs, r.OOSLeftMs = o.name, o.untilMs, left
				r.OOSStartMs, r.OOSBy, r.OOSReason = o.startMs, o.by, o.reason
				out[id] = r
			}
		}
	}
	return out
}

// hasOutOfService reports whether an unsynced restore leaves out an
// out-of-service window the log or the retain store held (for its note).
func (l *AlarmLog) hasOutOfService(retained map[string]retain.AlarmRetain) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.oos) > 0 {
		return true
	}
	for _, r := range retained {
		if r.OOSEquipment != "" {
			return true
		}
	}
	return false
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
	if p.Log.superseded() {
		// This copy's log was superseded by another history (#1029
		// 3c-3c-2a): its acks and shelves, in the view or the retain
		// store, are that history's, which a person accepted to lose. A
		// stale shelf would hide an alarm: none is carried, everything
		// re-annunciates.
		p.Log.dropView()
		p.Engine.Note("system", "acks and shelves not carried: this controller's operator-intent log was superseded by another history")
		slog.Warn("alarm: acknowledgements and shelves not carried: the operator-intent log was superseded by another history")
		p.Engine.ResetOperatorState()
		p.Engine.RestoreAlarms(map[string]retain.AlarmRetain{})
		return
	}
	if !synced {
		if p.Log.hasAcks(m) {
			p.Engine.Note("system", "acks not carried: takeover unsynced (the operator-intent log could not be caught up, so acknowledged alarms re-annunciate)")
			slog.Warn("alarm: acknowledgements not carried: the takeover's log catch-up failed; acknowledged alarms re-annunciate")
		}
		p.Log.dropAcks() // the view can't say which acks are current: none are
		if p.Log.hasOutOfService(m) {
			p.Engine.Note("system", "out of service not carried: takeover unsynced (the operator-intent log could not be caught up, so equipment taken out of service shows its alarms again)")
			slog.Warn("alarm: out of service not carried: the takeover's log catch-up failed; its alarms show")
		}
		p.Log.endUncarried()
	}
	// A frozen engine from an earlier leadership forgets what it held;
	// the view (or, unsynced, the shelves alone) is what applies now.
	if !mutServer("engine_not_reset") {
		p.Engine.ResetOperatorState()
	}
	p.Engine.RestoreAlarms(p.Log.Held(synced, m))
	p.Engine.RestoreFlood(p.Log.HeldFlood(synced))
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
	switch {
	case st.NotSynced:
		msg = LogNotSynced
	case st.Uncounted:
		msg = LogCopiesNotSynced
	}
	if st.Reason != "" {
		msg += " (" + st.Reason + ")"
	}
	refuse(w, msg, http.StatusServiceUnavailable)
	return true
}

// alarmAckThroughLog proposes an acknowledgement: "all" becomes the leader's
// unacknowledged alarms by id, since a standby can't know them.
//
// link is the ack link's ID when the ack is made by one (#715, acklink.go):
// the entry carries it, and every replica refuses a second ack naming it.
func (s *Server) alarmAckThroughLog(w http.ResponseWriter, r *http.Request, ids []string, occ map[string]string, by, link string) {
	s.alarmAckThroughLogAs(w, r, ids, occ, by, link, ids == nil, nil, "")
}

// alarmAckThroughLogAs is alarmAckThroughLog where all says the request was
// an acknowledge-all (so nothing to acknowledge is not a refusal), also when
// ids lists only the alarms the person may acknowledge; skipped and why are
// what an area-limited acknowledge-all left alone, for the answer.
func (s *Server) alarmAckThroughLogAs(w http.ResponseWriter, r *http.Request, ids []string, occ map[string]string, by, link string, all bool, skipped []string, why string) {
	if s.refuseAlarmBeforeRestore(w) {
		return
	}
	// One read of the leader's engine: which alarms the ack changes, and
	// the occurrence each is on, so the entry binds the ack to it.
	targets, err := s.alarms.AckTargets(ids, occ)
	if err != nil {
		http.Error(w, err.Error(), alarmErrStatus(err))
		return
	}
	ids = make([]string, 0, len(targets))
	occs := make([]oplog.Occ, 0, len(targets))
	for _, t := range targets {
		ids = append(ids, t.ID)
		if t.Occurrence != "" {
			occs = append(occs, oplog.Occ{ID: t.ID, Occ: t.Occurrence, ActiveMs: t.ActiveMs})
		}
	}
	if all && len(ids) == 0 {
		writeJSON(w, http.StatusOK, ackAnswer{IDs: []string{}, Skipped: skipped, SkippedWhy: why})
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
	if mutServer("ack_entry_unbound") {
		occs = nil
	}
	propose := s.alarmLog.node.Propose
	if f := alarmProposeSeam.Load(); f != nil {
		propose = *f // tests: a proposal the log never confirms
	}
	if _, err := s.proposeFor(ctx, r, oplog.Entry{Kind: oplog.KindAck, IDs: ids, Occs: occs, By: by, Link: link}, true, propose); err != nil {
		unknownOutcome(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ackAnswer{Acked: n, IDs: ids, Skipped: skipped, SkippedWhy: why})
}

// shelveThroughLog proposes a shelve, or an unshelve when until is zero.
func (s *Server) shelveThroughLog(w http.ResponseWriter, r *http.Request, id string, until time.Time, by, reason string) {
	if s.refuseAlarmBeforeRestore(w) {
		return
	}
	e := oplog.Entry{Kind: oplog.KindUnshelve, ID: id, By: by}
	err := error(nil)
	if until.IsZero() {
		err = s.alarms.CheckUnshelve(id)
	} else {
		err = s.alarms.CheckShelve(id, until)
		e.Kind, e.Until, e.Reason = oplog.KindShelve, until.UnixMilli(), reason
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
	if _, err := s.proposeFor(ctx, r, e, true, s.alarmLog.node.Propose); err != nil {
		unknownOutcome(w, err)
		return
	}
	s.writeAlarmRecord(w, id)
}

// alarmProposeSeam, set only by tests, replaces the oplog node's Propose
// for an acknowledgement (an outcome the log never confirms).
var alarmProposeSeam atomic.Pointer[func(context.Context, oplog.Entry) (oplog.Entry, error)]

// occsByID indexes an entry's occurrences by alarm.
func occsByID(occs []oplog.Occ) map[string]oplog.Occ {
	if len(occs) == 0 {
		return nil
	}
	out := make(map[string]oplog.Occ, len(occs))
	for _, o := range occs {
		out[o.ID] = o
	}
	return out
}

// applyAck applies a committed ack to the live engine: each alarm on the
// occurrence the entry names for it (an entry from a replica that predates
// occurrences names none, and acks whatever occurrence is on). One whose
// occurrence ended since the ack was checked is not acknowledged: the
// person saw another one.
func (l *AlarmLog) applyAck(e oplog.Entry, occ map[string]oplog.Occ) {
	if f := applyAckGap.Load(); f != nil {
		(*f)()
	}
	for _, id := range e.IDs {
		o, named := occ[id]
		if !named || mutServer("apply_ack_any_occurrence") {
			if _, err := l.eng.AckIDsAt([]string{id}, nil, e.By); err != nil {
				slog.Warn("alarm: a committed acknowledgement did not apply", "id", id, "err", err)
			}
			continue
		}
		_, err := l.eng.AckIDsAt([]string{id}, map[string]string{id: o.Occ}, e.By)
		switch {
		case errors.Is(err, alarm.ErrOtherOccurrence):
			// Not the occurrence on now. It may be the one that stood
			// through a takeover that could not continue it; only the
			// rest of the log can say (Reconcile decides once caught up).
			l.mu.Lock()
			if l.adopt == nil {
				l.adopt = map[string]adoption{}
			}
			l.adopt[id] = adoption{occ: o.Occ, activeMs: o.ActiveMs, by: e.By}
			l.mu.Unlock()
		case err != nil:
			slog.Warn("alarm: a committed acknowledgement did not apply", "id", id, "err", err)
		}
	}
}

// cancelAdopt drops id's pending adoption and returns the journal note
// saying why, for the caller to write once mu is released (the engine's
// hook takes mu under the engine's lock). Under mu.
func (l *AlarmLog) cancelAdopt(id, why string) string {
	a := l.adopt[id]
	delete(l.adopt, id)
	return "ack of " + id + " by " + a.by + " not applied: " + why
}

// adoptPending decides the pending adoptions once the log holds and has
// applied every committed entry (live leader, at its log tick): each ack
// left adopts its alarm's occurrence if the alarm has stood since this
// replica's restore, else it is refused and journalled.
func (l *AlarmLog) adoptPending() {
	// The node is asked before mu is taken, never under it: the node
	// applies commits (Apply, which takes mu) under its own lock (#1198
	// review: the inverse order froze the log, then the scan).
	caughtUp := func() bool { return l.node == nil || (l.node.Synced() && l.node.Commit() >= l.node.LastSeq()) }
	var up bool
	if mutServer("adopt_under_mu") {
		l.mu.Lock()
		up = caughtUp()
		l.mu.Unlock()
	} else {
		up = caughtUp()
	}
	l.mu.Lock()
	if len(l.adopt) == 0 {
		l.mu.Unlock()
		return
	}
	if !up {
		l.mu.Unlock()
		return // entries may still come that end the acked occurrence
	}
	pending := l.adopt
	l.adopt = nil
	l.mu.Unlock()
	for id, a := range pending {
		if _, err := l.eng.AckCommitted(id, a.occ, a.activeMs, a.by); err != nil {
			why := "committed after the occurrence it was for ended"
			if !errors.Is(err, alarm.ErrOtherOccurrence) {
				why = err.Error()
			}
			slog.Warn("alarm: a committed acknowledgement was for an occurrence that has ended; not applied", "id", id, "by", a.by)
			l.eng.Note("system", "ack of "+id+" by "+a.by+" not applied: "+why)
		}
	}
}

// SetOccurrenceMatch names the alarms whose occurrences the view keeps for
// a takeover: those an escalation policy covers (#715). Call it before the
// runtime runs; nil keeps none (acks still carry their own).
func (l *AlarmLog) SetOccurrenceMatch(match func(alarm.Def) bool) {
	if match == nil {
		l.match.Store(nil)
		return
	}
	l.match.Store(&match)
}

// syncOccurrences brings the view's occurrences in line with the live
// engine (the leader, at each log tick): occurrence-start for each covered
// alarm waiting on a person whose occurrence or wait the view does not
// hold, occurrence-end for each occurrence the view holds that the engine
// is no longer on. At most one entry of each kind, in the background; a
// tick that finds the last proposal still in flight does nothing (the
// next one finds what it left).
func (l *AlarmLog) syncOccurrences() {
	if l.node == nil || !l.live() || l.occBusy.Load() {
		return
	}
	var match func(alarm.Def) bool
	if m := l.match.Load(); m != nil {
		match = *m
	}
	cur := l.eng.Occurrences()
	var starts, ends []oplog.Occ
	l.mu.Lock()
	for id, o := range cur {
		if _, waits := l.adopt[id]; waits || match == nil || !o.Waiting || !match(o.Def) {
			continue // a pending adoption decides its occurrence first
		}
		v := l.view[id]
		if v.occ != o.Occurrence.Occurrence || v.waitMs != o.WaitMs || v.activeMs != o.ActiveMs {
			starts = append(starts, oplog.Occ{ID: id, Occ: o.Occurrence.Occurrence, ActiveMs: o.ActiveMs, WaitMs: o.WaitMs})
		}
	}
	for id, v := range l.view {
		if _, waits := l.adopt[id]; waits || v.occ == "" {
			continue
		}
		if o, ok := cur[id]; !ok || o.Occurrence.Occurrence != v.occ {
			ends = append(ends, oplog.Occ{ID: id, Occ: v.occ})
		}
	}
	l.mu.Unlock()
	if mutServer("occurrences_not_synced") || (len(starts) == 0 && len(ends) == 0) {
		return
	}
	slices.SortFunc(starts, func(a, b oplog.Occ) int { return strings.Compare(a.ID, b.ID) })
	slices.SortFunc(ends, func(a, b oplog.Occ) int { return strings.Compare(a.ID, b.ID) })
	l.occBusy.Store(true)
	go func() {
		defer l.occBusy.Store(false)
		for _, e := range []oplog.Entry{{Kind: oplog.KindOccurrenceEnd, Occs: ends, By: "system"},
			{Kind: oplog.KindOccurrenceStart, Occs: starts, By: "system"}} {
			if len(e.Occs) == 0 {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), substituteProposeWait)
			_, err := l.node.Propose(ctx, e)
			cancel()
			if err != nil {
				slog.Warn("alarm: occurrences not committed yet; the next log tick tries again", "kind", e.Kind, "alarms", len(e.Occs), "err", err)
				return
			}
		}
	}()
}

// Alarm floods and chattering across a takeover (#1249 slice 1b).
//
// The leader keeps the view's episodes current at every log tick
// (syncDetect): one KindAlarmDetect entry names the flood and the
// chattering episodes that began, and those that ended, since the view
// last heard. Only an episode beginning or ending is proposed, never its
// numbers moving: the log is never compacted. A synced takeover continues
// them (Held, HeldFlood) as going at the last instant this replica knew a
// leader was live (vouchedMsLocked: the newest entry, a replication from
// the leader, its own last tick leading), so the new leader ends each one
// window after that unless its own activations keep it going (and at once
// after a cold start hours later), and
// writes the end row under the episode's start and the flood's id. An
// unsynced takeover carries none (the view can't say which are current):
// the new leader starts with none, and the next tick ends the view's.
// Applying an entry never touches the engine: detection is the leader's
// own.

// detectEntryVersion is detectEntry's version. A replica ignores a body
// of a later version (its takeover then carries no episode).
const detectEntryVersion = 1

// detectEntry is a KindAlarmDetect entry's Value. Ends apply before
// starts.
type detectEntry struct {
	V          int                `json:"v"`
	Flood      *retain.AlarmFlood `json:"flood,omitempty"`    // the flood begun
	FloodEnd   string             `json:"floodEnd,omitempty"` // the flood ended, by id
	Chatter    []chatterEpisode   `json:"chatter,omitempty"`  // episodes begun
	ChatterEnd []chatterEpisode   `json:"chatterEnd,omitempty"`
}

// chatterEpisode is one alarm's chattering episode in a detectEntry: its
// start names it.
type chatterEpisode struct {
	ID       string `json:"id"`
	SinceMs  int64  `json:"sinceMs"`
	Acts     int    `json:"acts,omitempty"`
	Episodes int    `json:"episodes,omitempty"`
}

// applyDetect applies a KindAlarmDetect entry to the view.
func (l *AlarmLog) applyDetect(e oplog.Entry) {
	var b detectEntry
	if err := json.Unmarshal(e.Value, &b); err != nil || b.V != detectEntryVersion {
		slog.Warn("alarm: an alarm-detect entry this build does not read; a takeover carries no flood or chattering episode from it", "seq", e.Seq, "v", b.V, "err", err)
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if b.FloodEnd != "" && l.flood != nil && (l.flood.ID == b.FloodEnd || mutServer("flood_end_any")) {
		l.flood = nil
	}
	for _, c := range b.ChatterEnd {
		if v, ok := l.view[c.ID]; ok && v.chatSinceMs == c.SinceMs {
			v.chatSinceMs, v.chatActs, v.chatEpisodes = 0, 0, 0
			l.view[c.ID] = v
		}
	}
	if b.Flood != nil && b.Flood.ID != "" {
		f := *b.Flood
		f.FirstOut = slices.Clone(f.FirstOut)
		l.flood = &f
	}
	for _, c := range b.Chatter {
		if c.ID == "" || c.SinceMs == 0 {
			continue
		}
		v := l.view[c.ID]
		v.chatSinceMs, v.chatActs, v.chatEpisodes = c.SinceMs, c.Acts, c.Episodes
		l.view[c.ID] = v
	}
}

// HeldFlood is the flood a takeover continues: the view's, from a synced
// catch-up only, as going at the last instant a live leader was heard.
func (l *AlarmLog) HeldFlood(synced bool) *retain.AlarmFlood {
	if !synced || mutServer("takeover_drops_episodes") {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.flood == nil {
		return nil
	}
	f := *l.flood
	f.FirstOut = slices.Clone(f.FirstOut)
	f.OverMs = l.vouchedMsLocked()
	return &f
}

// heard records that a leader was live at ms (epoch ms).
func (l *AlarmLog) heard(ms int64) {
	for {
		cur := l.heardMs.Load()
		if ms <= cur || l.heardMs.CompareAndSwap(cur, ms) {
			return
		}
	}
}

// vouchedMsLocked is the latest instant this replica knows a live leader
// stood behind the view's episodes: the newest applied entry, its own
// last log tick as live leader, or the last replication a leader sent.
// After a cold start (both controllers down for hours) it is the newest
// entry's time, so an episode that ended while nobody ran ends on the
// new leader's first scan instead of reading as live for a window. A
// leader is "heard" while it lives; one whose build predates the
// alarm-detect kind never ends the view's episodes, so after an
// interlude under it they read as going until it was last heard.
func (l *AlarmLog) vouchedMsLocked() int64 {
	if mutServer("episodes_stamped_now") {
		return time.Now().UnixMilli()
	}
	ms := l.heardMs.Load()
	if l.node != nil {
		if t := l.node.LeaderHeard(); !t.IsZero() {
			ms = max(ms, t.UnixMilli())
		}
	}
	return ms
}

// syncDetect brings the view's episodes in line with the live engine (the
// leader, at each log tick): one KindAlarmDetect naming the flood and the
// chattering episodes begun or ended since. In the background; a tick
// that finds the last proposal in flight does nothing (the next one finds
// what it left).
func (l *AlarmLog) syncDetect() {
	if l.node == nil || !l.live() || l.detBusy.Load() || mutServer("episodes_not_synced") {
		return
	}
	chat, flood := l.eng.Episodes()
	b := detectEntry{V: detectEntryVersion}
	l.mu.Lock()
	for id, r := range chat {
		if l.view[id].chatSinceMs != r.ChatterSinceMs {
			b.Chatter = append(b.Chatter, chatterEpisode{ID: id, SinceMs: r.ChatterSinceMs, Acts: r.ChatterActs, Episodes: r.ChatterEpisodes})
		}
	}
	for id, v := range l.view {
		if v.chatSinceMs != 0 && chat[id].ChatterSinceMs != v.chatSinceMs {
			b.ChatterEnd = append(b.ChatterEnd, chatterEpisode{ID: id, SinceMs: v.chatSinceMs})
		}
	}
	if flood != nil && (l.flood == nil || l.flood.ID != flood.ID) {
		b.Flood = flood
	}
	if l.flood != nil && (flood == nil || flood.ID != l.flood.ID) {
		b.FloodEnd = l.flood.ID
	}
	l.mu.Unlock()
	if b.Flood == nil && b.FloodEnd == "" && len(b.Chatter) == 0 && len(b.ChatterEnd) == 0 {
		return
	}
	byID := func(a, b chatterEpisode) int { return strings.Compare(a.ID, b.ID) }
	slices.SortFunc(b.Chatter, byID)
	slices.SortFunc(b.ChatterEnd, byID)
	val, err := json.Marshal(b)
	if err != nil {
		return
	}
	l.detBusy.Store(true)
	go func() {
		defer l.detBusy.Store(false)
		ctx, cancel := context.WithTimeout(context.Background(), substituteProposeWait)
		defer cancel()
		if _, err := l.node.Propose(ctx, oplog.Entry{Kind: oplog.KindAlarmDetect, Value: val, By: "system"}); err != nil {
			slog.Warn("alarm: flood and chatter episodes not committed yet; the next log tick tries again", "err", err)
		}
	}()
}
