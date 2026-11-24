package main

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/joyautomation/nautilus/alarm"
	"github.com/joyautomation/nautilus/audit"
	"github.com/joyautomation/nautilus/internal/project"
	"github.com/joyautomation/nautilus/leader"
	"github.com/joyautomation/nautilus/retain"
	"github.com/joyautomation/nautilus/runtime"
)

// Opt-in failback (#710 d2, design §4.8, redundancy.failback): when the
// preferred controller has been ready to take the lead (the switchover's
// own checks, switchRefusal) for after without a break, the other
// controller, leading, runs the planned switchover to it. A manual
// switchover away from the preferred controller suspends it, as does an
// operator; max failbacks in a row, each followed within after by a
// failover back, stop it with an alarm. Its state lives in the retain
// store (retain.FailbackState), so a takeover keeps it.

// pairFailback is a pair member's failback, nil without
// redundancy.failback. It is the runtime's FailbackKeeper.
type pairFailback struct {
	m      *pairMember
	prefer string
	after  time.Duration
	max    int
	now    func() time.Time

	mu sync.Mutex
	st retain.FailbackState
	// readySince is when the preferred standby's readiness began (zero:
	// not ready now); notReady why it is not.
	readySince time.Time
	notReady   string
	// lastBack is when this controller last failed back (a done
	// switchover), on its own clock; zero when none counts.
	lastBack time.Time
	// seenTerm and leadSince are the leadership the loop last saw this
	// controller hold, and since when.
	seenTerm  uint64
	leadSince time.Time
	// pending are journal entries made under mu, written by flush after
	// it is released: a journal write fsyncs, and mu is taken under the
	// runtime's takeover lock, the alarm engine's and the saver's (Fable
	// on d2). flushMu keeps them in order.
	pending []audit.Entry
	flushMu sync.Mutex
}

// setupFailback builds the failback from the manifest and registers it on
// the runtime as the keeper of its retained state, before Run.
func (m *pairMember) setupFailback(rt *runtime.Runtime, r *project.RedundancyConfig) {
	if r == nil || r.Failback == nil {
		return
	}
	f := r.Failback
	m.failback = &pairFailback{m: m, prefer: f.Prefer, after: f.AfterOr(), max: f.MaxOr(), now: time.Now}
	rt.SetFailback(m.failback)
}

// RetainedFailback is what the leader saves (runtime.FailbackKeeper).
func (f *pairFailback) RetainedFailback() *retain.FailbackState {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.st == (retain.FailbackState{}) {
		return nil
	}
	st := f.st
	return &st
}

// RestoreFailback takes a load's state (runtime.FailbackKeeper). An
// unknown state (an unreadable store, nothing carried) suspends failback
// rather than read it as never set: an operator's suspend must not be
// lost to a bad disk (#710 d2 Q2).
func (f *pairFailback) RestoreFailback(st *retain.FailbackState, known bool) {
	f.mu.Lock()
	defer func() {
		f.mu.Unlock()
		go f.flush() // never a journal write under the takeover's lock
	}()
	switch {
	case !known:
		f.st = retain.FailbackState{Suspended: true, By: "system", AtMs: f.now().UnixMilli(),
			Why: "the failback state could not be read (the retain store is unreadable): resume it with naut redundancy failback resume once the pair is healthy"}
		slog.Warn("redundancy: failback suspended: its state could not be read from the retain store", "prefer", f.prefer)
		f.record("suspended", "system", f.st.Why, audit.ResultError)
	case st == nil:
		f.st = retain.FailbackState{}
	default:
		f.st = *st
	}
	f.readySince = time.Time{}
}

// record queues a failback event for the journal, under f.mu; flush
// writes it.
func (f *pairFailback) record(event, user, why string, result string, extra ...any) {
	detail := map[string]any{"event": event, "prefer": f.prefer}
	if why != "" {
		detail["why"] = why
	}
	for i := 0; i+1 < len(extra); i += 2 {
		detail[extra[i].(string)] = extra[i+1]
	}
	f.pending = append(f.pending, audit.Entry{
		User: user, Source: audit.SourceSystem,
		Action: "redundancy.failback", Target: string(f.m.o.Self),
		Detail: detail, Result: result,
	})
}

// flush writes the queued events to the journal, in order. Never under
// f.mu.
func (f *pairFailback) flush() {
	f.flushMu.Lock()
	defer f.flushMu.Unlock()
	f.mu.Lock()
	p := f.pending
	f.pending = nil
	f.mu.Unlock()
	j := f.m.journal.Load()
	if j == nil {
		return
	}
	for _, e := range p {
		_, _ = (*j).Record(e)
	}
}

// failbackPair is what the failback asks of its pair (pairStream; a fake
// in unit tests).
type failbackPair interface {
	// leadTerm is this controller's leadership: the term, and whether it
	// leads in it (runtime and elector both).
	leadTerm() (uint64, bool)
	switchRefusal(term uint64, prefer string) string
	switchover(ctx context.Context, by string) (switchReport, string, string)
}

// run is the failback's loop, on every member, until ctx ends: it acts
// only while this controller leads.
func (f *pairFailback) run(ctx context.Context, s failbackPair) {
	every := f.m.o.Lease / 4
	if every <= 0 {
		every = 250 * time.Millisecond
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		f.tick(ctx, s)
	}
}

// tick is one look: count a failover after a failback, reset the count
// once the preferred controller has held, or time the preferred
// standby's readiness and fail back to it.
func (f *pairFailback) tick(ctx context.Context, s failbackPair) {
	defer f.flush()
	now := f.now()
	term, lead := s.leadTerm()
	if !lead {
		f.mu.Lock()
		f.seenTerm, f.readySince, f.notReady = 0, time.Time{}, ""
		f.mu.Unlock()
		return
	}
	self := string(f.m.o.Self)
	f.mu.Lock()
	if term != f.seenTerm {
		f.seenTerm, f.leadSince = term, now
		if self != f.prefer {
			f.countFailover(now)
		}
	}
	if self == f.prefer {
		// The preferred controller has held for after since it took the
		// lead: the failbacks so far were not a ping-pong.
		if f.st.Count > 0 && now.Sub(f.leadSince) >= f.after && !mutNaut("failback_count_never_resets") {
			f.record("count-reset", "system", "the preferred controller held the lead for after", audit.ResultOK, "count", f.st.Count)
			f.st.Count = 0
		}
		f.readySince, f.notReady = time.Time{}, ""
		f.mu.Unlock()
		return
	}
	if f.st.Suspended && !mutNaut("failback_ignores_suspend") {
		f.readySince, f.notReady = time.Time{}, "suspended"
		f.mu.Unlock()
		return
	}
	f.mu.Unlock()

	// The switchover's own checks, asked of the standby (outside the lock:
	// it is a request).
	why := s.switchRefusal(term, f.prefer)
	f.mu.Lock()
	if why != "" {
		if !mutNaut("failback_ready_never_resets") {
			f.readySince = time.Time{} // any break starts the clock again
		}
		f.notReady = why
		f.mu.Unlock()
		return
	}
	if f.readySince.IsZero() {
		f.readySince = now
	}
	f.notReady = ""
	readyFor := now.Sub(f.readySince)
	if readyFor < f.after {
		f.mu.Unlock()
		return
	}
	f.readySince = time.Time{}
	f.mu.Unlock()

	slog.Info("redundancy: failback: the preferred controller has been ready for "+readyFor.Round(time.Second).String()+"; switching over to it", "prefer", f.prefer)
	rep, outcome, reason := s.switchover(ctx, failbackBy)
	result := audit.ResultOK
	if outcome != leader.SwitchDone {
		// Any other outcome is the field's evidence (Q5): the switchover
		// journals its own entry; this one says the failback asked for it.
		result = audit.ResultError
		slog.Warn("redundancy: failback: the switchover did not hand over", "outcome", outcome, "why", reason)
	}
	f.mu.Lock()
	f.record("switchover", "system", reason, result, "outcome", outcome, "readyMs", readyFor.Milliseconds(), "report", rep)
	// The lead moved (done, or past the resign as a failover): a failover
	// back within after counts (Fable on d2: a failback ending as a
	// failover moved the lead too).
	if outcome == leader.SwitchDone || outcome == leader.SwitchFailover && !mutNaut("failback_failover_not_counted") {
		f.lastBack = f.now()
	}
	f.mu.Unlock()
}

// stopped: the ping-pong stop holds failback suspended (its alarm).
func (f *pairFailback) stopped() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.st.Suspended && f.st.Stopped
}

// failbackBy is the switchover's principal when failback runs it.
const failbackBy = "failback"

// suspended reports whether failback is suspended now (switchover's last
// look before it stops the scans).
func (f *pairFailback) suspended() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.st.Suspended
}

// countFailover runs when this (non-preferred) controller takes the lead:
// within after of its own failback, and not by a switchover an operator
// made (which suspends), the preferred controller failed over back to it.
// At max in a row failback stops itself. Under f.mu.
func (f *pairFailback) countFailover(now time.Time) {
	back := f.lastBack
	f.lastBack = time.Time{}
	if back.IsZero() || now.Sub(back) >= f.after || f.st.Suspended {
		return
	}
	f.st.Count++
	slog.Warn("redundancy: failback: the preferred controller failed over back within after", "count", f.st.Count, "max", f.max)
	if f.st.Count < f.max || mutNaut("failback_never_stops") {
		f.record("failover-after-failback", "system", "", audit.ResultError, "count", f.st.Count)
		return
	}
	why := fmt.Sprintf("failback stopped: %d failbacks each followed by a failover within %s", f.st.Count, f.after)
	f.st.Suspended, f.st.Stopped, f.st.By, f.st.Why, f.st.AtMs = true, true, "system", why, now.UnixMilli()
	slog.Error("redundancy: "+why+"; resume it with naut redundancy failback resume once the cause is fixed", "prefer", f.prefer)
	f.record("stopped", "system", why, audit.ResultError, "count", f.st.Count)
}

// switchingAway runs as a switchover starts, before its final image: one
// from the preferred controller (an operator's, not the failback's)
// suspends failback, carried in the image to the controller taking over.
// undo puts the state back when the switchover does not hand over.
func (f *pairFailback) switchingAway(by string) (undo func()) {
	if string(f.m.o.Self) != f.prefer || mutNaut("failback_manual_not_suspended") {
		return func() {}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.st.Suspended {
		return func() {}
	}
	was := f.st
	f.st.Suspended, f.st.Stopped, f.st.By, f.st.AtMs = true, false, by, f.now().UnixMilli()
	f.st.Why = "manual switchover away from the preferred controller"
	set := f.st
	f.record("suspended", by, f.st.Why, audit.ResultOK)
	return func() {
		f.mu.Lock()
		// Only what this switchover set: an operator's suspend or resume
		// that landed meanwhile stands (Fable on d2).
		if f.st == set || mutNaut("failback_undo_overwrites") {
			f.st = was
			f.record("suspend-undone", by, "the switchover did not hand over", audit.ResultOK)
		}
		f.mu.Unlock()
		f.flush()
	}
}

// setSuspended is an operator's suspend or resume (POST
// /api/cluster/failback, on the leader). A resume ends the ping-pong
// stop and its count.
func (f *pairFailback) setSuspended(suspend bool, by, why string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if suspend {
		if why == "" {
			why = "suspended by an operator"
		}
		f.st.Suspended, f.st.Stopped, f.st.By, f.st.Why, f.st.AtMs = true, false, by, why, f.now().UnixMilli()
	} else {
		f.st = retain.FailbackState{}
		f.lastBack = time.Time{}
	}
	f.readySince = time.Time{}
}

// status is the failback's block of /api/cluster: nil without one, and on
// a standby (the leader's state decides).
func (f *pairFailback) status(leading bool) *leader.FailbackStatus {
	if f == nil || !leading {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := &leader.FailbackStatus{
		Prefer: f.prefer, AfterMs: f.after.Milliseconds(), Max: f.max,
		Suspended: f.st.Suspended, Stopped: f.st.Stopped, By: f.st.By, Why: f.st.Why, AtMs: f.st.AtMs,
		Count: f.st.Count, NotReady: f.notReady,
	}
	if !f.readySince.IsZero() {
		out.ReadyMs = f.now().Sub(f.readySince).Milliseconds()
	}
	return out
}

// condition is the failback's line in /api/cluster's warnings: suspended
// (by whom, why), "" otherwise.
func (f *pairFailback) condition(leading bool) string {
	if f == nil || !leading {
		return ""
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.st.Suspended {
		return ""
	}
	at := time.UnixMilli(f.st.AtMs).UTC().Format(time.RFC3339)
	return fmt.Sprintf("failback to %s suspended (%s, by %s at %s): naut redundancy failback resume", f.prefer, f.st.Why, f.st.By, at)
}

// FailbackStopAlarmID is the system alarm raised while the ping-pong stop
// holds failback suspended (#710 d2 Q4).
const FailbackStopAlarmID = "nautilus.failback-stopped"

// failbackAlarms are the failback's system alarms, nil without one.
func failbackAlarms(m *pairMember) []project.SystemAlarm {
	if m == nil || m.failback == nil {
		return nil
	}
	f := m.failback
	leading := m.q.IsLeader // lock-free (as holdAlarms)
	return []project.SystemAlarm{{
		Def: alarm.Def{
			ID: FailbackStopAlarmID, Tag: "__nautilus.failback-stopped",
			Name:     "Failback stopped: the preferred controller failed over back after each failback (naut redundancy failback resume once the cause is fixed)",
			Priority: alarm.High, Class: "system",
			AckRequired: true, AutoClear: true,
		},
		Active: func() bool { return leading() && f.stopped() },
	}}
}
