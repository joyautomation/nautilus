package server

import (
	"context"
	"log/slog"
	"maps"
	"net/http"
	"sync"
	"time"

	"github.com/joyautomation/nautilus/alarm"
	"github.com/joyautomation/nautilus/oplog"
	"github.com/joyautomation/nautilus/retain"
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
// An acknowledgement is of an occurrence. When an acknowledged alarm
// returns to normal on the leader, the leader proposes an ack-clear for it
// (retried until committed, as an expiry is), and the view drops the ack:
// a takeover then never carries it to a new occurrence. The residual is
// the leader dying between that return to normal and the ack-clear's
// commit: one log round trip (well under lease/2) with the peer up. Acks
// bound to an activation proper come with #715.

// PairNoMajorityAlarms is a pair's no-majority refusal for alarm writes.
const PairNoMajorityAlarms = "operator-intent log has no majority: the peer is down; alarm acknowledgements and shelves cannot be made until it returns"

// AlarmLog adapts an oplog node for an alarm engine.
type AlarmLog struct {
	eng     *alarm.Engine
	leading func() bool
	node    *oplog.Node
	refusal string

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
	shelfUntilMs int64
	shelfBy      string
	shelfSet     bool // the log shelved or unshelved it: its shelf is the log's
}

// NewAlarmLog is an AlarmLog for eng; leading reports whether this replica
// leads now (its engine is the live one). It registers the engine hook
// that proposes ack-clears. Attach the node once it exists (its Apply must
// already reach Apply).
func NewAlarmLog(eng *alarm.Engine, leading func() bool) *AlarmLog {
	l := &AlarmLog{eng: eng, leading: leading, view: map[string]alarmIntent{}, refusal: PairNoMajorityAlarms, clrRetry: 2 * time.Second}
	eng.OnEvent(l.engineEvent)
	return l
}

// Attach sets the oplog node proposals go to.
func (l *AlarmLog) Attach(n *oplog.Node) { l.node = n }

// Apply applies a committed entry if it is an alarm kind, and reports
// whether it was one.
func (l *AlarmLog) Apply(e oplog.Entry) (bool, error) {
	live := l.leading()
	switch e.Kind {
	case oplog.KindAck:
		l.mu.Lock()
		for _, id := range e.IDs {
			v := l.view[id]
			v.acked, v.ackBy, v.ackMs = true, e.By, e.TS
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
		v.shelfUntilMs, v.shelfBy, v.shelfSet = e.Until, e.By, true
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
		v := l.view[e.ID]
		v.acked, v.ackBy, v.ackMs = false, "", 0
		l.view[e.ID] = v
		l.mu.Unlock()
	default:
		return false, nil
	}
	return true, nil
}

// engineEvent hears the leader's engine (under its lock: no blocking, no
// engine calls). An acknowledged alarm back to normal is an occurrence
// over: its ack is cleared in the log.
func (l *AlarmLog) engineEvent(ev alarm.Event) {
	if ev.State != "normal" || (ev.Kind != alarm.KindRTN && ev.Kind != alarm.KindAck) || mutServer("ack_clear_off") {
		return
	}
	l.mu.Lock()
	acked := l.view[ev.ID].acked
	l.mu.Unlock()
	if acked {
		l.queueClear(ev.ID)
	}
}

// queueClear proposes an ack-clear in the background, retrying what the
// log does not take (no majority) until it does.
func (l *AlarmLog) queueClear(id string) {
	l.clrMu.Lock()
	defer l.clrMu.Unlock()
	l.clr = append(l.clr, id)
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
		id := l.clr[0]
		l.clrMu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), substituteProposeWait)
		_, err := l.node.Propose(ctx, oplog.Entry{Kind: oplog.KindAckClear, ID: id, By: "system"})
		cancel()
		if err != nil {
			slog.Warn("alarm: ack-clear not committed yet; retrying", "id", id, "err", err)
			time.Sleep(l.clrRetry)
			continue
		}
		l.clrMu.Lock()
		l.clr = l.clr[1:]
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
	if !synced && p.Log.hasAcks(m) {
		p.Engine.Note("system", "acks not carried: takeover unsynced (the operator-intent log could not be caught up, so acknowledged alarms re-annunciate)")
		slog.Warn("alarm: acknowledgements not carried: the takeover's log catch-up failed; acknowledged alarms re-annunciate")
	}
	p.Engine.RestoreAlarms(p.Log.Held(synced, m))
}

// refuseAlarmWithoutMajority answers the no-majority refusal before an
// alarm write is proposed (the substitutes' rule, substitutes_oplog.go).
func (s *Server) refuseAlarmWithoutMajority(w http.ResponseWriter) bool {
	st := s.alarmLog.node.Status()
	if st.Writable {
		return false
	}
	msg := s.alarmLog.refusal
	if st.Reason != "" {
		msg += " (" + st.Reason + ")"
	}
	refuse(w, msg, http.StatusServiceUnavailable)
	return true
}

// alarmAckThroughLog proposes an acknowledgement: "all" becomes the leader's
// unacknowledged alarms by id, since a standby can't know them.
func (s *Server) alarmAckThroughLog(w http.ResponseWriter, r *http.Request, ids []string, by string) {
	if ids == nil {
		ids = s.alarms.UnackedIDs()
		if len(ids) == 0 {
			writeJSON(w, http.StatusOK, map[string]int{"acked": 0})
			return
		}
	}
	n, err := s.alarms.CheckAck(ids)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
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
