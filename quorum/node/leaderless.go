package node

import (
	"fmt"
	"strings"
	"time"

	"github.com/joyautomation/nautilus/quorum"
)

// What a slow or failing voter state write looks like to the operator
// (#1436). A grant leaves a voter only after its write, so writes slower
// than the lease's write budget (quorum.Config.WriteBudget) can leave a
// pair with no leader: never two, the gates shut, but silent, and a pair
// with no leader evaluates none of the manifest's alarms (it scans
// nothing). These are the reports that make it seen, and the one alarm it
// does raise (leaderlessalarm.go) is raised from them: each write's time, a grant released after its asker stopped
// waiting, and a controller that has seen no leader for a while, with the
// cause it can name.

// noteEvery is how often, at most, a standing slow-write, late-grant or
// no-leader report is logged again.
const noteEvery = time.Minute

// wordFresh is how many leases a voter's campaign word stays current for
// the cause.
const wordFresh = 4

// refusedMsg words a refusal: the promise it named only when that is
// what refused it (a term at or above the one asked). A voter in its
// restart wait, uncommissioned, or holding another's relayed promise
// refuses too, and its word names no promise.
func refusedMsg(who string, w quorum.Word) string {
	if w.Promised >= w.Term {
		return fmt.Sprintf("%s refused this controller's campaign for term %d: it had promised term %d", who, w.Term, w.Promised)
	}
	return fmt.Sprintf("%s refused this controller's campaign for term %d", who, w.Term)
}

// leaderlessAfter is how many leases with no leader seen make a report:
// a takeover wins in about one, and a slow election in two or three.
const leaderlessAfter = 4

// Leaderless is a controller's report that it has seen no leader for a
// while (Status.Leaderless, #1436).
type Leaderless struct {
	// For is how long: since a leader was last seen, or since this
	// controller's voter's restart wait ended.
	For time.Duration
	// Cause is what this controller can name of why, one clause per
	// voter that did not grant its campaigns.
	Cause string
	// Raises: this controller raises the no-leader alarm
	// (nautilus.pair-leaderless) for it now (leaderlessalarm.go).
	Raises bool
}

// writeBudget is how long a state write may take at this lease
// (quorum.Config.WriteBudget).
func writeBudget(lease time.Duration) time.Duration {
	return quorum.Config{Lease: lease}.WriteBudget()
}

// writeIsSlow reports whether a write that took took is past the warning
// line: half the budget, so the warning comes before the pair can't elect.
func writeIsSlow(took, lease time.Duration) bool {
	if mut("slow_write_at_budget") {
		return took > writeBudget(lease)
	}
	return took > writeBudget(lease)/2
}

// writeNote keeps one voter's write times and rate-limits their warnings
// (the elector's and each witness pair's). Under its owner's lock.
type writeNote struct {
	last, max time.Duration
	slow      int // slow writes since the last warning
	slowMax   time.Duration
	warnAt    quorum.Instant
	warned    bool
	late      uint64 // grants released to nobody (lateGrants)
	lateNew   int    // since the last warning
	lateAt    quorum.Instant
	lateTold  bool
}

// took records a write's time and reports whether to warn now: it is
// slow at lease, and no warning went out in the last noteEvery. n and
// worst are the slow writes since the last warning and the slowest.
func (w *writeNote) took(now quorum.Instant, d, lease time.Duration) (warn bool, n int, worst time.Duration) {
	if d < 0 {
		return false, 0, 0 // a rewrite of what is on disk: served no ask
	}
	w.last = d
	w.max = max(w.max, d)
	if !writeIsSlow(d, lease) {
		return false, 0, 0
	}
	w.slow++
	w.slowMax = max(w.slowMax, d)
	if w.warned && now.Sub(w.warnAt) < noteEvery && !mut("slow_write_unlimited") {
		return false, 0, 0
	}
	n, worst = w.slow, w.slowMax
	w.warned, w.warnAt, w.slow, w.slowMax = true, now, 0, 0
	return true, n, worst
}

// lateGrant counts a grant the write released after its asker had
// stopped waiting, and reports whether to warn now (n since the last).
func (w *writeNote) lateGrant(now quorum.Instant) (warn bool, n int) {
	w.late++
	w.lateNew++
	if w.lateTold && now.Sub(w.lateAt) < noteEvery {
		return false, 0
	}
	n = w.lateNew
	w.lateTold, w.lateAt, w.lateNew = true, now, 0
	return true, n
}

func slowWriteMsg(who string, n int, worst, lease time.Duration) string {
	return fmt.Sprintf("%s: %d voter state write(s) took up to %v from the ask to on disk; a %v lease needs them well under %v, "+
		"or the pair can be left with no leader (it fails safe: never two). Check the state directory's disk",
		who, n, worst.Round(time.Millisecond), lease, writeBudget(lease).Round(time.Millisecond))
}

// lateGrantMsg words a late-grant warning. The asker did not count that
// vote; its campaign may still have won on the other two, so the warning
// says what a write this slow costs only once it does.
func lateGrantMsg(who string, n int, took, lease time.Duration) string {
	msg := fmt.Sprintf("%s granted %d vote(s) only after the asker had stopped waiting (a vote waits %v): the asker did not count them", who, n, lease/8)
	if took >= 0 {
		msg += fmt.Sprintf("; the state write took %v", took.Round(time.Millisecond))
	}
	return msg + fmt.Sprintf(". A campaign is lost once a voter's writes pass %v at this lease", writeBudget(lease).Round(time.Millisecond))
}

// lateGrants counts, per asker, the grants a write released that reached
// no waiter: the asker had given up before the write landed. A campaign's
// re-asks ride one write, so the earlier asks' grants reach nobody while
// the latest's is answered; only an asker none of whose grants were
// answered counts (#1436).
func lateGrants(missed, hit map[quorum.NodeID]bool) int {
	n := 0
	for to := range missed {
		if !hit[to] || mut("late_grant_any_miss") {
			n++
		}
	}
	return n
}

// mayLeadLocked is MayLead with the health and the log's state already
// read (outside mu). Under mu.
func (e *Elector) mayLeadLocked(now quorum.Instant, healthy bool, ls LogState, hasLog bool) string {
	switch {
	case e.spare.Load():
		return "this controller is a spare"
	case !healthy:
		return "this controller is unhealthy"
	case e.released:
		return "this controller released the lead"
	case e.handingBack(now):
		return "this controller is handing the lead back itself"
	case e.boxPending || e.v.State().Incarnation == 0:
		return "this controller is not commissioned"
	case !e.mayCampaign(now):
		return "this controller's configuration differs from its peer's"
	case e.c.Marked():
		return "this controller's name is marked duplicated"
	case hasLog && e.logDefers(now, ls, hasLog) != deferNo && (e.holdSince.IsZero() || time.Since(e.holdSince) < 2*e.o.Lease):
		return "this controller's operator log defers its campaign"
	}
	return ""
}

// noteLeaderless updates the no-leader report at a look (#1436): past
// leaderlessAfter leases with no leader seen, Status.Leaderless names it
// and its cause, logged once and then every noteEvery. Under mu.
func (e *Elector) noteLeaderless(now quorum.Instant, healthy bool, ls LogState, hasLog bool) {
	from, until := e.v.LeaderSeen()
	if e.c.Leading(now) || (from == e.o.Peer && now < until) || e.spare.Load() {
		if e.ll != nil && e.llLogged {
			e.log.Info("redundancy: a leader is seen again", "after", now.Sub(e.llSince).Round(time.Millisecond))
		}
		e.ll, e.llLogged, e.leaderAt = nil, false, now
		e.endRaise()
		return
	}
	since := max(e.leaderAt, e.c.HoldUntil())
	if mut("leaderless_from_boot") {
		since = e.leaderAt
	}
	if now < since || now.Sub(since) <= leaderlessAfter*e.o.Lease {
		e.ll = nil
		e.endRaise()
		return
	}
	cause, quiet := e.leaderlessCause(now, healthy, ls, hasLog)
	start := e.ll == nil
	e.ll, e.llSince = &Leaderless{For: now.Sub(since), Cause: cause}, since
	e.noteRaise(now, start)
	if quiet && !mut("leaderless_never_quiet") {
		return // a report of its own stands for it
	}
	if e.llLogged && now.Sub(e.llLogAt) < noteEvery {
		return
	}
	e.llLogged, e.llLogAt = true, now
	e.log.Error("redundancy: this controller has seen no leader for "+now.Sub(since).Round(time.Millisecond).String()+
		" (its own voter has promised none, and the peer's renewals don't reach it). If none leads, the pair runs no outputs and evaluates no alarms but "+
		"nautilus.pair-leaderless, which a project with an alarms: section raises, paged by one controller (it fails safe: never two leaders). Cause: "+cause,
		"lease", e.o.Lease)
}

// leaderlessCause names why this controller has seen no leader, from what
// it knows (#1436): first anything that keeps it from leading (MayLead),
// then, per voter that did not grant its campaigns, that voter's last
// word. quiet: a report of its own already covers it (a fenced state, a
// refused lease or version, an uncommissioned controller), so no error is
// logged for it. A slow disk is named only where it was measured: this
// controller's own, when its own grant is the one a majority lacks.
func (e *Elector) leaderlessCause(now quorum.Instant, healthy bool, ls LogState, hasLog bool) (cause string, quiet bool) {
	if why := e.mayLeadLocked(now, healthy, ls, hasLog); why != "" {
		return why, e.boxPending || e.v.State().Incarnation == 0
	}
	if e.c.FencedHold() {
		return "a voter says this controller's state is fenced (see redundancy.superseded)", true
	}
	for _, msg := range e.mismatch {
		if msg != "" {
			return "a vote is refused: " + msg, true
		}
	}
	// A word stays current across one back-off: an unheard campaign waits
	// two leases from its last re-ask (three quarters of a lease in)
	// before the next asks again.
	recent := wordFresh * e.o.Lease
	if mut("words_stale_at_backoff") {
		recent = 2 * e.o.Lease
	}
	var why []string
	granted := 0
	for _, v := range []quorum.NodeID{e.o.Peer, e.o.Witness} {
		w, ok := e.word(v)
		switch {
		case ok && now.Sub(w.At) < recent && w.Counted && w.Granted:
			granted++
		case ok && now.Sub(w.At) < recent && !w.Counted:
			why = append(why, fmt.Sprintf("voter %s's answer is not counted: %s", v, w.Why))
		case ok && now.Sub(w.At) < recent:
			why = append(why, refusedMsg("voter "+string(v), w))
		case e.answeredWithin(v, now, recent) || mut("cause_cut_as_slow"):
			why = append(why, fmt.Sprintf("voter %s has not confirmed its write (slow or failing); see its log", v))
		default:
			why = append(why, fmt.Sprintf("voter %s does not answer (its link is down, or it is stopped)", v))
		}
	}
	own, ok := e.c.Word(e.o.Self)
	ownGranted := ok && now.Sub(own.At) < recent && own.Counted && own.Granted
	if !ownGranted && (granted == 1 || mut("own_disk_blamed_always")) {
		// Its own grant is the one the majority lacks.
		budget := writeBudget(e.o.Lease)
		switch waited := max(e.writes.last, e.w.waiting(now)); {
		case e.writeErr != "":
			why = append(why, "this controller's own voter state write fails: "+e.writeErr)
		case waited > budget:
			why = append(why, fmt.Sprintf("this controller's own voter has not confirmed its write: its state disk is slow (%v; a %v lease needs under %v)",
				waited.Round(time.Millisecond), e.o.Lease, budget.Round(time.Millisecond)))
		case ok && now.Sub(own.At) < recent && !own.Granted:
			why = append(why, refusedMsg("this controller's own voter", own))
		}
	}
	if len(why) == 0 {
		return "its campaigns are granted but win no lease (the two controllers' campaigns may be colliding)", false
	}
	return strings.Join(why, "; "), false
}

// word is voter v's latest word on this boot's campaigns: the candidate's
// (quorum.Candidate.Word), or a reply the driver dropped before the
// candidate saw it (noteDrop), whichever is later. Under mu.
func (e *Elector) word(v quorum.NodeID) (quorum.Word, bool) {
	w, ok := e.c.Word(v)
	if d, dok := e.drops[v]; dok && (!ok || d.At > w.At) && !mut("driver_drops_unseen") {
		return d, true
	}
	return w, ok
}

// noteDrop records a campaign reply from v the driver drops before the
// candidate sees it (a witness certificate or incarnation it does not
// count, a fenced state): an answer, not counted, and why.
func (e *Elector) noteDrop(v quorum.NodeID, r quorum.Reply, why string) {
	if r.Term == 0 {
		return // a probe's: says nothing about a campaign
	}
	e.mu.Lock()
	e.noteDropLocked(v, r, why)
	e.mu.Unlock()
}

func (e *Elector) noteDropLocked(v quorum.NodeID, r quorum.Reply, why string) {
	if r.Term == 0 {
		return
	}
	if e.drops == nil {
		e.drops = map[quorum.NodeID]quorum.Word{}
	}
	e.drops[v] = quorum.Word{At: e.now(), Term: r.Term, Granted: r.Granted, Why: why, Promised: r.Promised}
}

// answered notes that voter v answered a call (any answer: a reply, a
// refusal, no content), so it is reachable.
func (e *Elector) answered(v quorum.NodeID) {
	e.mu.Lock()
	if e.answers == nil {
		e.answers = map[quorum.NodeID]quorum.Instant{}
	}
	e.answers[v] = e.now()
	e.mu.Unlock()
}

// answeredWithin reports whether voter v answered any call of this
// controller's in the last d: its calls in, the peer's, prove nothing of
// the link out. Under mu.
func (e *Elector) answeredWithin(v quorum.NodeID, now quorum.Instant, d time.Duration) bool {
	if at, ok := e.answers[v]; ok && now.Sub(at) < d {
		return true
	}
	return false
}
