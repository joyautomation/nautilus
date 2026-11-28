//go:build !js

package oplog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync/atomic"
	"time"
)

// The compaction decision's seam (#1455 PR 2b, docs/design/oplog-compaction.md
// §3). The log decides when and how far; the keep decision (Options.Compact,
// the server's, which knows the kinds) decides what stays. All of it is
// kind-blind here: the gate (every copy heard, synced, counting, on keep
// rules no newer than the leader's, clocks close), the compaction time T,
// Through, the trigger, and how long a wanted compaction has waited.
//
// Only the leader decides, on its own goroutine (decideLoop), one decision
// at a time, never on the log tick's. Nothing heavy runs under mu: the
// committed entries are read outside it (committed entries never change in
// place: a rewrite replaces the slice, and a replaced suffix is above the
// commit index).

// decideLimits are the decision's timings and thresholds (Options.decide:
// tests set them; zero fields take the defaults).
type decideLimits struct {
	every      time.Duration                               // between two looks (default 5 min)
	margin     time.Duration                               // the tail never dropped, by TS before T (7 d)
	minEntries int                                         // the part older than the margin compacts from this many entries (20,000)
	minBytes   int                                         // or this many bytes (8 MB)
	gap        time.Duration                               // between two compactions, by TS (24 h)
	spread     time.Duration                               // the three clocks' spread that refuses (5 min)
	heard      time.Duration                               // how recent a copy's answer must be (10 s)
	shrink     int                                         // how many times an over-size decision is retried smaller (8)
	maxK       int                                         // tests: a smaller cap on K's size than the entry cap
	paceWork   time.Duration                               // the decision's and the rewrite's chunk of work (2 ms): compact_pace.go
	paceDuty   float64                                     // their share of the time, 0 to 1 (0.25; 1: unpaced)
	paceNow    func() time.Time                            // tests: the pacer's clock
	paceSleep  func(done <-chan struct{}, d time.Duration) // tests: the pacer's rest
}

// limits is the Node's decideLimits with the defaults filled in: a copy
// is heard while its last answer is within four replica call timeouts
// (two leases in a pair: the leader's heartbeat runs every half lease).
func (n *Node) limits() decideLimits {
	d := n.o.decide
	if d.heard <= 0 {
		d.heard = 4 * n.o.Timeout
	}
	return d.withDefaults()
}

func (d decideLimits) withDefaults() decideLimits {
	def := func(v *time.Duration, x time.Duration) {
		if *v <= 0 {
			*v = x
		}
	}
	def(&d.every, 5*time.Minute)
	def(&d.margin, 7*24*time.Hour)
	def(&d.gap, 24*time.Hour)
	def(&d.spread, 5*time.Minute)
	def(&d.heard, 10*time.Second)
	if d.minEntries <= 0 {
		d.minEntries = 20000
	}
	if d.minBytes <= 0 {
		d.minBytes = 8 << 20
	}
	if d.shrink <= 0 {
		d.shrink = 8
	}
	def(&d.paceWork, 2*time.Millisecond)
	if d.paceDuty <= 0 || d.paceDuty > 1 {
		d.paceDuty = 0.25
	}
	return d
}

// decideState is the Node's state for the decision.
type decideState struct {
	// decideRefused is why the last decision's check refused (Status
	// CompactDecisionRefused, oplog-compaction-refused); "" once one
	// passes.
	decideRefused string
	// rulesMarked: the log holds a carrier naming keep rules, or a base
	// (needsRulesMark), as far as this process has seen.
	rulesMarked atomic.Bool
}

// needsRulesMark reports whether a leader that decides compactions must
// sequence a carrier naming them though nothing is left to commit: once,
// before any compaction, so blocked-since has a start (compactSince). The
// log is read outside mu, from the decision loop, never a takeover's
// catch-up (SyncLeader stays as it was: Fable, 2b-1).
func (n *Node) needsRulesMark(p *pacer) bool {
	if n.o.CompactRules == 0 || n.o.Compact == nil || n.rulesMarked.Load() || mutOplog("carrier_unmarked") {
		return false
	}
	n.mu.Lock()
	snap, base := n.entries[:n.indexAfterLocked(n.commit)], n.cut.Compact
	n.mu.Unlock()
	if base != 0 {
		n.rulesMarked.Store(true) // a K floors blocked-since: no marker is needed
		return false
	}
	if _, ok := firstRulesTerm(snap, p); ok {
		n.rulesMarked.Store(true)
		return false
	}
	return true
}

// markRules sequences the noop naming this build's keep rules, when the
// log holds none (needsRulesMark), for a leader caught up for its term.
// Best effort: a failure is retried at the next look.
func (n *Node) markRules(ctx context.Context, p *pacer) {
	if !n.needsRulesMark(p) {
		return
	}
	n.seqMu.Lock()
	defer n.seqMu.Unlock()
	if !n.isLeader() || !n.TermSynced() {
		return
	}
	// The term's carrier, as SyncLeader sequences it: entries an earlier
	// term left past the commit index are committed by it, so the ones
	// that must lapse are named (a bare noop would carry them out: Fable,
	// 2b-1). It names the rules (carrierLocked).
	n.mu.Lock()
	e := n.carrierLocked(n.currentTerm())
	n.mu.Unlock()
	if mutOplog("marker_skips_lapses") {
		e = Entry{Kind: KindNoop, By: "system", Origin: n.o.Self, Value: e.Value}
	}
	if _, err := n.sequenceLocked(ctx, e); err != nil {
		n.log.Warn("oplog: could not mark this term with the keep rules; the next look tries again", "error", err)
		return
	}
	n.rulesMarked.Store(true)
}

// rulesMarkedNow records that a carrier naming the rules is committed.
func (n *Node) rulesMarkedNow() { n.rulesMarked.Store(true) }

// decision is one compaction the leader may propose.
type decision struct {
	term, prev, through, commit uint64
	t                           time.Time
	base                        baseState
	snap                        []stored
}

// decideOutcome is what one look found, for tests and the log line.
type decideOutcome int

const (
	decideNotLeader decideOutcome = iota // not the leader: nothing wanted here
	decideNotWanted                      // too little to compact yet, or too soon after the last
	decideBlocked                        // wanted, but the gate does not hold (Status says why)
	decideRefused                        // the keep decision's check refused
	decideWaiting                        // the keep decision failed this time
	decideDiscarded                      // the log moved under the decision: thrown away
	decideProposed                       // a compaction was proposed and committed
)

// startDecide starts the decision loop, once New is done, when this copy
// may decide. Caller holds mu.
func (n *Node) startDecide() {
	if n.o.CompactRules == 0 || n.o.Compact == nil || n.standalone() || mutOplog("decide_off") {
		return
	}
	n.worker.Add(1)
	go n.decideLoop()
}

// decideLoop looks for a compaction every decideLimits.every.
func (n *Node) decideLoop() {
	defer n.worker.Done()
	t := time.NewTicker(n.limits().every)
	defer t.Stop()
	for {
		select {
		case <-n.done:
			return
		case <-t.C:
		}
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			select {
			case <-n.done:
				cancel()
			case <-ctx.Done():
			}
		}()
		n.decideOnce(ctx)
		cancel()
	}
}

// decideOnce is one look (decideLoop): it finds whether a compaction is
// wanted, whether the gate holds, asks the keep decision, and proposes.
func (n *Node) decideOnce(ctx context.Context) decideOutcome {
	lim := n.limits()
	if !n.isLeader() {
		// A follower's word would linger: what it says of compaction is the
		// leader's to say.
		n.setCompactBlocked("", time.Time{})
		n.mu.Lock()
		n.decideRefused = ""
		n.mu.Unlock()
		return decideNotLeader
	}
	// One pacer for the whole look: every loop over the log, here and in
	// the keep decision (CompactInput.Pace), shares its budget.
	p := newPacer(ctx.Done(), lim)
	n.markRules(ctx, p)
	var limit uint64 // a smaller Through after an over-size decision
	for try := 0; ; try++ {
		d, wanted, since, why := n.plan(lim, limit, p)
		switch {
		case !wanted:
			n.setCompactBlocked("", time.Time{})
			return decideNotWanted
		case transient(why):
			return decideWaiting
		case why != "":
			n.setCompactBlocked(why, since)
			return decideBlocked
		}
		keep, err := n.o.Compact(ctx, d.input(p))
		switch {
		case errors.Is(err, ErrCompactRefused):
			n.mu.Lock()
			n.decideRefused = err.Error()
			n.mu.Unlock()
			n.log.Error("oplog: the compaction's keep decision refused: nothing is proposed (a bug: the evidence is in this message)", "through", d.through, "error", err)
			return decideRefused
		case err != nil:
			n.log.Warn("oplog: the compaction's keep decision failed; the next look tries again", "through", d.through, "error", err)
			return decideWaiting
		}
		n.mu.Lock()
		n.decideRefused = ""
		n.mu.Unlock()
		_, err = n.proposeDecided(ctx, d, keep, p)
		switch {
		case errors.Is(err, ErrTooLarge) && try < lim.shrink:
			// Over the entry cap (a year's first compaction): compact a
			// smaller range, then the rest next time.
			limit = d.midpoint(p)
			if limit == 0 || mutOplog("decide_no_shrink") {
				n.log.Warn("oplog: the compaction's decision is over the entry cap and can't be made smaller", "through", d.through, "error", err)
				return decideWaiting
			}
			n.log.Info("oplog: the compaction's decision is over the entry cap: compacting a smaller range", "through", d.through, "smaller", limit)
			continue
		case errors.Is(err, errDecisionStale):
			n.log.Info("oplog: the log moved under the compaction's decision: discarded", "through", d.through, "why", err)
			return decideDiscarded
		case err != nil:
			n.log.Warn("oplog: the compaction was not committed", "through", d.through, "error", err)
			return decideWaiting
		}
		n.setCompactBlocked("", time.Time{})
		n.log.Info("oplog: compacted the log", "through", d.through, "kept", len(keep), "base", d.prev)
		return decideProposed
	}
}

// The gate's transient reasons: the next look finds them gone by itself
// (a takeover catching up, a leadership moving), so they never start or
// clear the blocked time.
const (
	whyNotLeader   = "not the leader"
	whyNotCaughtUp = "this copy has not caught up for its term"
)

func transient(why string) bool {
	return (why == whyNotLeader || why == whyNotCaughtUp) && !mutOplog("decide_transient_blocks")
}

// errDecisionStale: the log moved under a decision (another term, another
// base, the gate no longer holds): it is thrown away.
var errDecisionStale = errors.New("oplog: the decision is stale")

// input is d as the keep decision is given it, paced by p.
func (d decision) input(p *pacer) CompactInput {
	kept := map[uint64]bool{}
	for _, s := range d.base.Kept {
		kept[s] = true
	}
	return CompactInput{T: d.t, Through: d.through, Base: d.prev, Commit: d.commit, Pace: p.Pace, Entries: func(f func(Entry) bool) {
		// Lapsed as applyRange marks it, over the whole committed log (as
		// at open).
		lapsed := map[uint64]bool{}
		live := func(s stored) bool { return s.Seq > d.base.Through || kept[s.Seq] }
		for _, s := range d.snap {
			p.tick()
			if !live(s) || !s.mayLapse() {
				continue
			}
			if e := s.entry(); e.Kind == KindLapse {
				for _, r := range e.Refs {
					lapsed[r] = true
				}
			}
		}
		for _, s := range d.snap {
			p.tick()
			if !live(s) {
				continue
			}
			e := s.entry()
			if e.Kind == KindCompact || e.Kind == KindNoop {
				continue
			}
			e.Lapsed = lapsed[e.Seq] && e.Kind != KindLapse
			if !f(e) {
				return
			}
		}
	}}
}

// midpoint is a smaller Through for an over-size decision: the seq half
// way through the entries after the base, 0 when there is none.
func (d decision) midpoint(p *pacer) uint64 {
	var after []uint64
	for _, s := range d.snap {
		p.tick()
		if s.Seq > d.prev && s.Seq <= d.through {
			after = append(after, s.Seq)
		}
	}
	if len(after) < 2 {
		return 0
	}
	return after[len(after)/2-1]
}

// tsOf is an entry's TS, read without decoding the rest.
func tsOf(s stored) int64 {
	var v struct {
		TS int64 `json:"ts"`
	}
	_ = json.Unmarshal(s.raw, &v)
	return v.TS
}

// plan is the decision as the log stands: whether a compaction is wanted
// (enough older than the margin, and the last one long enough ago), since
// when (from the log alone, compactSince), why the gate does not hold
// ("" when it does), and what it would be. cap, when set, bounds Through
// (a smaller range after an over-size one).
func (n *Node) plan(lim decideLimits, cap uint64, p *pacer) (d decision, wanted bool, since time.Time, why string) {
	term, leader := n.currentTerm(), n.isLeader()
	n.mu.Lock()
	now := n.o.Now()
	d = decision{term: term, commit: n.commit, base: n.cut, prev: n.cut.Through}
	d.snap = n.entries[:n.indexAfterLocked(n.commit)]
	clocks, why := n.clocksLocked(lim, now)
	switch {
	case !leader:
		why = whyNotLeader // the transient reasons first: they say nothing of the others
	case n.syncedTerm != term:
		why = whyNotCaughtUp
	}
	prevK := n.compactTSLocked()
	n.mu.Unlock()

	// Outside mu: the committed entries read for their TS.
	var newest int64
	if len(d.snap) > 0 {
		newest = tsOf(d.snap[len(d.snap)-1])
	}
	t := now
	for _, c := range clocks {
		t = minTime(t, c)
	}
	if newest > 0 {
		t = minTime(t, time.UnixMilli(newest))
	}
	d.t = t
	cut := t.Add(-lim.margin).UnixMilli()
	count, size := 0, 0
	for _, s := range d.snap {
		p.tick()
		if s.Seq <= d.prev {
			continue
		}
		if cap > 0 && s.Seq > cap {
			break
		}
		if tsOf(s) <= cut {
			d.through = s.Seq
		}
	}
	for _, s := range d.snap {
		p.tick()
		if s.Seq > d.prev && s.Seq <= d.through {
			count++
			size += len(s.raw)
		}
	}
	since = compactSince(d.snap, d.prev, prevK, lim, p)
	wanted = (count >= lim.minEntries || size >= lim.minBytes || cap > 0) &&
		(prevK == 0 || t.Sub(time.UnixMilli(prevK)) >= lim.gap) && !mutOplog("decide_never_wanted")
	if !wanted || why != "" {
		return d, wanted, since, why
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if w := n.gateLocked(leader, term, d.term, lim, now); w != "" {
		return d, wanted, since, w
	}
	d.through = n.throughLocked(d.through)
	if d.through <= d.prev {
		return d, wanted, since, "waiting for every copy to commit past the base, and for no lapse to span the part to compact"
	}
	return d, wanted, since, ""
}

// compactSince is since when a compaction has been wanted, from the log
// alone (so a restart, or the other controller leading, finds the same
// time): when the part after the base first held enough to compact (the
// minEntries-th entry after the base, or the one that brought it to
// minBytes) passed the margin; never before the last compaction's TS plus
// the gap; and, before the first compaction, never before the margin has
// passed since the first term a leader on compaction's rules began (its
// carrier names its rules: carrierLocked). So a log grown long before
// compaction was switched on waits the margin from then, and a rolling
// upgrade raises nothing. TS follow the sequencing leaders' clocks, so a
// stepped clock moves it, never the process's own start.
func compactSince(snap []stored, prev uint64, prevK int64, lim decideLimits, p *pacer) time.Time {
	if mutOplog("blocked_since_now") {
		return time.Now()
	}
	count, size := 0, 0
	var at int64
	for _, s := range snap {
		p.tick()
		if s.Seq <= prev {
			continue
		}
		count++
		size += len(s.raw)
		if count >= lim.minEntries || size >= lim.minBytes {
			at = tsOf(s) + lim.margin.Milliseconds()
			break
		}
	}
	if at == 0 && len(snap) > 0 {
		at = tsOf(snap[len(snap)-1]) + lim.margin.Milliseconds()
	}
	switch {
	case prevK > 0:
		at = max(at, prevK+lim.gap.Milliseconds())
	case !mutOplog("blocked_since_unfloored"):
		if first, ok := firstRulesTerm(snap, p); ok {
			at = max(at, first+lim.margin.Milliseconds())
		} else if len(snap) > 0 {
			at = max(at, tsOf(snap[len(snap)-1])+lim.margin.Milliseconds())
		}
	}
	return time.UnixMilli(at)
}

// firstRulesTerm is the earliest TS of a committed carrier (a term's
// first entry) that names keep rules: when the first term a leader on
// compaction's rules began. Two leaders in a row may each write one before
// either sees the other's, so the least TS of them all, never the first
// found. snap is committed entries only: an uncommitted tail may be
// replaced.
func firstRulesTerm(snap []stored, p *pacer) (int64, bool) {
	var first int64
	found := false
	for _, s := range snap {
		p.tick()
		if !bytes.Contains(s.raw, []byte(`"rules"`)) {
			continue
		}
		e := s.entry()
		if e.Kind != KindNoop && e.Kind != KindLapse || e.By != "system" {
			continue
		}
		var v struct {
			Rules int `json:"rules"`
		}
		if json.Unmarshal(e.Value, &v) != nil || v.Rules < 1 || found && mutOplog("rules_mark_first_found") {
			continue
		}
		if !found || e.TS < first {
			first, found = e.TS, true
		}
	}
	return first, found
}

// compactTSLocked is the TS of the compaction that made the base (0:
// none). Caller holds mu.
func (n *Node) compactTSLocked() int64 {
	c := n.cut
	if c.Compact == 0 {
		return 0
	}
	if c.K != nil {
		return c.K.TS
	}
	if i := n.indexAfterLocked(c.Compact - 1); i < len(n.entries) && n.entries[i].Seq == c.Compact {
		return tsOf(n.entries[i])
	}
	return 0
}

// clocksLocked is the other copies' clocks as of now (each answer's clock,
// moved on by the time since it came), or why they can't be used: a copy
// not heard recently, or one that does not compact. Caller holds mu.
func (n *Node) clocksLocked(lim decideLimits, now time.Time) ([]time.Time, string) {
	var out []time.Time
	for _, p := range n.others() {
		pw, ok := n.peerWire[p]
		switch {
		case !ok || now.Sub(pw.at) > lim.heard:
			return nil, "waiting for " + n.name(p) + ": not heard from"
		case pw.rules < 1:
			return nil, "waiting for " + n.name(p) + ": it runs a build from before log compaction"
		case pw.now == 0:
			return nil, "waiting for " + n.name(p) + ": its clock is not known"
		}
		out = append(out, time.UnixMilli(pw.now).Add(now.Sub(pw.at)))
	}
	return out, ""
}

// gateLocked is why the gate of §3 does not hold for a decision made at
// term, "" when it does; leader and cur are this copy's leadership and term
// now, read before mu (the election's own lock may wait on the node).
// Caller holds mu.
func (n *Node) gateLocked(leader bool, cur, term uint64, lim decideLimits, now time.Time) string {
	switch {
	case !leader || cur != term:
		return whyNotLeader
	case n.syncedTerm != term:
		return whyNotCaughtUp
	case !n.syncedLocked():
		return "this copy's log is not synced"
	case n.supersedingLocked():
		return "this copy's log was superseded"
	case n.mark.Diverged != "":
		return "this copy's log diverged"
	case n.mark.Lost != "":
		return "this copy's log was lost and is not synced again"
	case n.mark.Alone || n.mark.Accept != nil || n.spendOwed:
		return "a vouch's catch-up is not spent yet"
	}
	// Entries stored and not committed are compactEntryLocked's to refuse,
	// under the sequencing lock: a look made meanwhile sees each write in
	// flight.
	clocks, why := n.clocksLocked(lim, now)
	if why != "" {
		return why
	}
	lo, hi := now, now
	for _, c := range clocks {
		lo, hi = minTime(lo, c), maxTime(hi, c)
	}
	if hi.Sub(lo) > lim.spread && !mutOplog("decide_clock_spread_ignored") {
		return fmt.Sprintf("clocks disagree: the copies' clocks spread %s, more than %s", hi.Sub(lo).Round(time.Second), lim.spread)
	}
	for _, p := range n.others() {
		pw := n.peerWire[p]
		switch {
		case !n.countsLocked(p) && !mutOplog("decide_uncounted_peer"):
			return "waiting for " + n.name(p) + ": its log is not synced, or does not count"
		case pw.rules > n.o.CompactRules:
			return fmt.Sprintf("waiting for this controller's update: %s runs newer keep rules (%d) than this one (%d)", n.name(p), pw.rules, n.o.CompactRules)
		case pw.base.Rules > n.o.CompactRules:
			return fmt.Sprintf("waiting for this controller's update: %s's log was compacted under newer keep rules (%d)", n.name(p), pw.base.Rules)
		}
	}
	if n.cut.Rules > n.o.CompactRules {
		return fmt.Sprintf("waiting for this controller's update: the log was compacted under newer keep rules (%d) than this build's (%d)", n.cut.Rules, n.o.CompactRules)
	}
	return ""
}

// throughLocked is the highest seq up to want that every copy has
// committed and that no KindLapse above it splits (it names nothing at or
// below it). Caller holds mu.
func (n *Node) throughLocked(want uint64) uint64 {
	through := want
	for _, p := range n.others() {
		if c := n.peerWire[p].commit; c < through && !mutOplog("decide_ignores_peer_commit") {
			through = c
		}
	}
	for changed := true; changed; {
		changed = false
		for _, s := range n.entries[n.indexAfterLocked(through):] {
			if !s.mayLapse() {
				continue
			}
			if l := s.entry(); l.Kind == KindLapse {
				for _, r := range l.Refs {
					if r <= through && r > 0 {
						through, changed = r-1, true
					}
				}
			}
		}
	}
	return through
}

// proposeDecided proposes d's compaction with keep, re-checked under the
// sequencing lock: the same term, the same base, the gate still holding,
// nothing uncommitted (proposeCompact's own checks). A decision the log
// moved under is errDecisionStale.
func (n *Node) proposeDecided(ctx context.Context, d decision, keep []uint64, p *pacer) (Entry, error) {
	lim := n.limits()
	// K's body, built here over the committed entries read outside mu
	// (compactBodyOf: the keep list checked, the lapses its kept entries
	// need, the digest): under the locks only the tail is scanned (a
	// year's first compaction holds about 876k entries up to Through).
	pre, err := compactBodyOf(d.base, d.snap, d.through, keep, n.o.CompactRules, d.termAt, p)
	if err != nil {
		return Entry{}, err
	}
	return n.proposeCompactChecked(ctx, d.through, keep, &pre, func(leader bool, cur uint64) error {
		if (n.cut.Through != d.prev || n.cut.Compact != d.base.Compact) && !mutOplog("decide_base_unchecked") {
			return fmt.Errorf("%w: the base moved from %d to %d", errDecisionStale, d.prev, n.cut.Through)
		}
		if w := n.gateLocked(leader, cur, d.term, lim, n.o.Now()); w != "" && !mutOplog("decide_gate_unchecked") {
			return fmt.Errorf("%w: %s", errDecisionStale, w)
		}
		return nil
	})
}

// termAt is the term at seq in d's committed entries (0: not held), the
// base's Through at its term though it was dropped.
func (d decision) termAt(seq uint64) uint64 {
	i := sort.Search(len(d.snap), func(i int) bool { return d.snap[i].Seq >= seq })
	if seq == 0 || i >= len(d.snap) || d.snap[i].Seq != seq {
		if seq != 0 && seq == d.base.Through {
			return d.base.ThroughTerm
		}
		return 0
	}
	return d.snap[i].Term
}

func minTime(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

func maxTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// decideForTest is one look, for tests.
func (n *Node) decideForTest(ctx context.Context) decideOutcome { return n.decideOnce(ctx) }
