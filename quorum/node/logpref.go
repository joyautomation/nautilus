package node

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/joyautomation/nautilus/quorum"
)

// The pair's operator-intent log and the election (#710 PR 3b-4b). The
// log is composed outside this package (cmd/naut, over package oplog), so
// the elector sees it only through two seams set once the log exists:
//
//   - SetLog: what the log says of this controller's copy (LogState). An
//     unsynced controller does not campaign while the other controller is
//     reachable, synced and able to campaign (a preference: it campaigns
//     after two leases with no leader, and a lone survivor always does).
//     An unsynced LEADER whose catch-up for its term failed, while the
//     other controller is all three, yields: it stops renewing, once per
//     yieldEvery, so the synced one takes over and syncs it. Otherwise it
//     leads read-only (the log refuses writes until synced), keeping the
//     plant under control.
//   - SetVouch: Recommission vouches for this controller's log first
//     (oplog.Node.Vouch), and refuses with the log's refusal.
//
// What the other controller says of its log and whether it may campaign
// travels in the hello's answer (Log, bound by InfoMAC), asked every half
// lease while this controller's log is unsynced (probeLog), so "within the
// last lease" is what the preference and the yield judge on.
//
// The seams are called outside e.mu: the log calls into the election
// under its own lock (LeaderAddr, Term), so calling it under e.mu would
// deadlock.

// LogState is what the pair's operator-intent log says of this
// controller's copy.
type LogState struct {
	// Synced: this controller's log is synced (it counts).
	Synced bool
	// Stuck: this controller leads and its catch-up for the current term
	// has failed (the log is read-only under it).
	Stuck bool
	// CanSync: synced, and it could catch up as leader: a vouch's one-shot
	// catch-up is pending, or another copy that counts was heard. A
	// controller yields only to one that can (naut-lead, #1053): a synced
	// one whose other copies are all lost would lead read-only too.
	CanSync bool
	// History and HistoryID are the log's history (#1029 3c-3c-2a): sent
	// in the hello so the other controller judges this copy at it from
	// first contact. A copy at a lower history, or the same number under
	// another id (a fork), is not one to defer or yield to.
	History, HistoryID uint64
}

// LogAccept is what a person accepts at a vouch (oplog.VouchAccept), as
// the digest of the picture they were shown.
type LogAccept struct {
	Loss    string `json:"loss,omitempty"`
	LostLog string `json:"lostLog,omitempty"`
}

// Empty: nothing is accepted.
func (a LogAccept) Empty() bool { return a.Loss == "" && a.LostLog == "" }

// yieldEvery is how seldom one controller yields (naut-lead, #710): at
// most once per this, so a flapping peer can't bounce the lead.
var yieldEvery = 10 * time.Minute

// EventYielded: this controller, leading with an unsynced log, stopped
// renewing so the other, synced, takes over.
const EventYielded EventKind = "redundancy.yielded"

// EventVouchRefused: a re-commission went ahead, but the log refused the
// vouch for this controller's copy (Detail says why), so it stays unsynced
// and read-only.
const EventVouchRefused EventKind = "redundancy.oplog.vouch-refused"

// peerLog is what the other controller's last verified hello answer said
// of its log.
type peerLog struct {
	synced, canSync, may bool
	history, historyID   uint64
	at                   time.Time
}

// SetLog sets the log's report (see the top of this file). Call it before
// Run.
func (e *Elector) SetLog(f func() LogState) {
	e.mu.Lock()
	e.logFn = f
	e.mu.Unlock()
}

// SetVouch sets what Recommission runs first, and Vouch runs: the log's
// vouch for this controller's copy (by names the person; inc is the
// witness incarnation being adopted, at which the witness's copy counts;
// acc what the person accepted). It returns the picture the vouch saw, as
// JSON (oplog.Picture), and its refusal. Call it before Run.
func (e *Elector) SetVouch(f func(ctx context.Context, by string, inc uint64, acc LogAccept) (json.RawMessage, error)) {
	e.mu.Lock()
	e.vouchFn = f
	e.mu.Unlock()
}

// logState reads the log's report, outside mu. ok is false without one.
func (e *Elector) logState() (LogState, bool) {
	e.mu.Lock()
	f := e.logFn
	e.mu.Unlock()
	if f == nil {
		return LogState{}, false
	}
	return f(), true
}

// peerLogFresh: the other controller's log report is from within the last
// lease. Under mu.
func (e *Elector) peerLogFresh() bool {
	return !e.peerLog.at.IsZero() && time.Since(e.peerLog.at) < e.o.Lease
}

// logHold reports whether this controller, not leading, defers to the
// other for the log's sake: its own log is unsynced, and the other's,
// heard within the last lease, is synced and may campaign. Never for
// longer than two leases with no leader seen. Under mu.
func (e *Elector) logHold(now quorum.Instant, ls LogState, has bool) bool {
	switch e.logDefers(now, ls, has) {
	case deferNo:
		e.holdSince = time.Time{}
		return false
	case deferLeads:
		e.holdSince = time.Time{} // the other leads: nothing to campaign for
		return true
	}
	if e.holdSince.IsZero() {
		e.holdSince = time.Now()
	}
	return time.Since(e.holdSince) < 2*e.o.Lease
}

const (
	deferNo    = iota // nothing to defer to
	deferLeads        // the other, synced, leads
	deferWait         // the other, synced, may take over
)

// logDefers is logHold's judgement without its clock. Under mu.
func (e *Elector) logDefers(now quorum.Instant, ls LogState, has bool) int {
	if !has || ls.Synced || !e.peerLogFresh() || !e.peerLogSynced(ls) || !e.peerLog.may || mut("campaign_ignores_sync") {
		return deferNo
	}
	if from, until := e.v.LeaderSeen(); from == e.o.Peer && now < until {
		return deferLeads
	}
	return deferWait
}

// peerLogSynced: the other controller's log, by its last hello, is synced
// at a history this copy would take from: the same, or a higher number
// (which supersedes this one). A lower one is a history a person accepted
// to lose, and the same number under another id a fork: neither is a log
// to defer or yield to (#1029 3c-3c-2a). Under mu.
func (e *Elector) peerLogSynced(ls LogState) bool {
	p := e.peerLog
	if !p.synced {
		return false
	}
	if mut("lower_history_peer_synced") {
		return true
	}
	return p.history > ls.History || p.history == ls.History && p.historyID == ls.HistoryID
}

// maybeYield is a leader's look at the log: an unsynced leader whose
// catch-up failed, while the other controller is synced and may campaign
// (heard within the last lease), stops renewing until its lease has run
// out, at most once per yieldEvery. Its gate stays open to the lease's
// end (minus the fence), as a renewal that failed leaves it, so the plant
// is controlled until the other can take over. Under mu.
func (e *Elector) maybeYield(now quorum.Instant, ls LogState, has bool) {
	if !has || !e.c.Leading(now) || ls.Synced || !ls.Stuck || mut("unsynced_leader_keeps_lead") {
		return
	}
	if !e.peerLogFresh() || !e.peerLogSynced(ls) || !e.peerLog.canSync && !mut("yield_ignores_can_sync") || !e.peerLog.may && !mut("yield_ignores_may") {
		return
	}
	if e.v.Duplicated(now, e.o.Peer) && !mut("yield_to_marked_peer") {
		return // the peer's vote counts nowhere here: it may say it can campaign, but it can't win (#1029 3c)
	}
	if !e.yieldedAt.IsZero() && time.Since(e.yieldedAt) < yieldEvery && !mut("yield_undamped") {
		return
	}
	e.yieldedAt = time.Now()
	e.yieldHold, e.yieldDue = true, e.c.LeaseEnd()
	msg := fmt.Sprintf("yielded to %s: own operator log unsynced, %s synced and able to sync the others", e.o.Peer, e.o.Peer)
	e.log.Warn("redundancy: " + msg + "; this controller stops renewing so " + string(e.o.Peer) + " takes over and syncs it")
	e.event(Event{Kind: EventYielded, Detail: msg})
}

// yielding: a yield's lease has not run out yet. Under mu.
func (e *Elector) yielding(now quorum.Instant) bool {
	e.yieldHold = yieldHolds(e.yieldHold, e.c.Leading(now), now, e.yieldDue)
	return e.yieldHold
}

// yieldHolds is whether a yield still holds renewals back: until the lease
// is seen over, not until the instant it was due to end (due), since a
// renewal round sent before the yield and granted after it moves the
// lease's end (#710 3b-4b self-review).
func yieldHolds(hold, leading bool, now, due quorum.Instant) bool {
	if !hold {
		return false
	}
	if mut("yield_until_due") {
		return now < due
	}
	return leading
}

// selfLog is this controller's log report for the hello: synced, and
// whether it may campaign now (healthy, not released, nothing holding
// it). "" without a log. Takes mu; call it outside.
func (e *Elector) selfLog() string {
	ls, has := e.logState()
	if !has {
		return ""
	}
	e.mu.Lock()
	h := e.health
	e.mu.Unlock()
	healthy := h == nil || h()
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.now()
	hold := e.logDefers(now, ls, has) != deferNo &&
		(e.holdSince.IsZero() || time.Since(e.holdSince) < 2*e.o.Lease)
	may := healthy && !e.released && !e.boxPending && e.v.State().Incarnation != 0 &&
		e.mayCampaign(now) && !hold && (!e.c.Marked() || mut("may_ignores_mark"))
	return logReport(ls.Synced, ls.Synced && ls.CanSync, may, ls.History, ls.HistoryID)
}

// logReport encodes a log report: "s" or "u" (synced or not), "c" or "x"
// (can sync the others or not), then "m" or "n" (may campaign or not),
// then, at a history above 0, "<history>.<id in hex>" (#1029 3c-3c-2a;
// only then, so a pair at history 0 sends what an older build reads).
func logReport(synced, canSync, may bool, history, id uint64) string {
	r := []byte("uxn")
	if synced {
		r[0] = 's'
	}
	if canSync {
		r[1] = 'c'
	}
	if may {
		r[2] = 'm'
	}
	if history > 0 {
		return string(r) + strconv.FormatUint(history, 10) + "." + strconv.FormatUint(id, 16)
	}
	return string(r)
}

// parseLogHistory reads a log report's history suffix; ok is false when
// it is malformed.
func parseLogHistory(suffix string) (history, id uint64, ok bool) {
	if suffix == "" {
		return 0, 0, true
	}
	h, i, found := strings.Cut(suffix, ".")
	if !found {
		return 0, 0, false
	}
	var err1, err2 error
	history, err1 = strconv.ParseUint(h, 10, 64)
	id, err2 = strconv.ParseUint(i, 16, 64)
	return history, id, err1 == nil && err2 == nil
}

// probeLog asks the other controller's hello for its log report every
// half lease while this controller's own log is unsynced (the only time
// the preference or a yield needs it).
func (e *Elector) probeLog(ctx context.Context) {
	every := max(e.o.Lease/2, 50*time.Millisecond)
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
		if ls, has := e.logState(); has && !ls.Synced {
			e.askLog(ctx)
		}
	}
}

// askLog is one hello asked for the peer's log report only; a verified
// answer is recorded.
func (e *Elector) askLog(ctx context.Context) {
	if e.mute() {
		return
	}
	c := newChallenge()
	q := url.Values{"c": {c}, "from": {string(e.o.Self)}, "log": {"1"}}
	e.selfInfo().query(q)
	body, err := e.call.call(ctx, max(e.o.Lease/2, 100*time.Millisecond), http.MethodGet, e.peer+Prefix+e.o.Cluster+"/hello?"+q.Encode(), nil)
	if err != nil {
		return
	}
	var h helloAnswer
	if json.Unmarshal(body, &h) != nil || h.Node != string(e.o.Peer) {
		return
	}
	if pi, ok := e.answerVerified(c, h); ok {
		e.heard(pi)
	}
}

// heardLog records the peer's verified log report. Under mu.
func (e *Elector) heardLog(r string) {
	if len(r) < 3 {
		return
	}
	h, id, ok := parseLogHistory(r[3:])
	if !ok {
		return
	}
	e.peerLog = peerLog{synced: r[0] == 's', canSync: r[1] == 'c', may: r[2] == 'm', history: h, historyID: id, at: time.Now()}
}

// PeerLog is what the other controller's last hello said of its log
// (#1029 3c-3c-2a), for the caller that drives the log: whether it is
// synced, its history, and whether it leads now. ok is false when it was
// not heard within the last lease.
type PeerLog struct {
	Synced             bool
	History, HistoryID uint64
	Leads              bool
}

// PeerLog reports the other controller's log, as its last hello said.
func (e *Elector) PeerLog() (PeerLog, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.peerLogFresh() {
		return PeerLog{}, false
	}
	from, until := e.v.LeaderSeen()
	return PeerLog{Synced: e.peerLog.synced, History: e.peerLog.history, HistoryID: e.peerLog.historyID, Leads: from == e.o.Peer && e.now() < until}, true
}

// vouch runs the log's vouch, outside mu: the picture it saw (JSON) and
// its refusal.
func (e *Elector) vouch(ctx context.Context, by string, inc uint64, acc LogAccept) (json.RawMessage, error) {
	e.mu.Lock()
	f := e.vouchFn
	e.mu.Unlock()
	if f == nil {
		return nil, nil
	}
	return f(ctx, by, inc, acc)
}
