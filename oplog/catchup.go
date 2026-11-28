//go:build !js

package oplog

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// Reading peers' logs: a new leader's catch-up (leaderCatchUp), a
// follower's (CatchUp), page by page (fetchAfter), meeting a compacted
// base (compact_wire.go). Not in the browser playground's build: its node
// is standalone and never reads a peer (catchup_js.go).

// leaderCatchUp is what a new leader does before its first write of a
// term: fetch from every peer whatever is after our last entry and take
// the most advanced answer, so an instance that lost its file (a
// reschedule) and then took the lease does not number its first write
// over entries its peers already committed. Raft restricts votes to the
// most advanced log; here the leader is elected elsewhere, so it catches
// up instead. Caller holds seqMu.
//
// It succeeds only when the peers that answered and this instance make a
// majority: a committed entry is stored on a majority, and any majority
// meets it, so then the most advanced answer holds every committed entry.
// Fewer answers and it returns ErrReadOnly and the term stays unsynced, so
// the next call asks again.
func (n *Node) leaderCatchUp(ctx context.Context, term uint64) error {
	n.mu.Lock()
	if n.syncedTerm == term {
		n.mu.Unlock()
		return nil
	}
	if n.stoppedLocked() {
		err := n.stopErrLocked()
		n.mu.Unlock()
		return fmt.Errorf("%w: %w", ErrReadOnly, err)
	}
	// From the COMMIT index, not the last entry: an uncommitted suffix of
	// this leader's own may be stale (stored, never replicated, and since
	// overtaken by a committed entry at the same seq), and only answers
	// from its commit on carry the entries that replace it. Its own suffix
	// is one of the candidates, when it counts.
	from := n.commit
	last := n.lastLocked()
	own := afterResp{Commit: n.commit, LastSeq: last, LastTerm: n.termAtLocked(last)}
	selfCounts := n.syncedLocked() || mutOplog("unsynced_self_counted")
	// A vouched log's first catch-up as leader (Vouch): it waits for every
	// peer, and only when EVERY peer answered, takes the most up-to-date
	// log among itself and those that count, even when that is itself
	// alone. Once. A silent peer may hold a write a majority committed
	// without this log (#1053 review: two vouched controllers, one silent
	// witness): then it fails, and the vouch is kept for when it answers,
	// unless the person accepted that peer's loss (Mark.Accept).
	alone := selfCounts && n.mark.Alone
	accept := n.mark.Accept
	hist := n.mark.Lineage
	n.mu.Unlock()
	// The most advanced log is Raft's "up to date", compared on each WHOLE
	// log's last entry: the higher term wins, then the higher seq, so an
	// older term's uncommitted leftovers never outrank a newer term's
	// committed log, even one that ends at the commit index (and so sends
	// no entries). This leader's own log wins a tie.
	better := func(a, b afterResp) bool {
		return a.LastTerm > b.LastTerm || a.LastTerm == b.LastTerm && a.LastSeq > b.LastSeq
	}
	// Only a replica that counts is a candidate, or counts toward the
	// majority: one whose log is not synced (lost, rebuilt, another
	// identity) may lack committed entries, and a majority that counted
	// it might hold them nowhere (#710 3b-4).
	var best afterResp
	var bestPeer string
	bestSelf, answered, commit := false, 0, uint64(0)
	if selfCounts {
		best, bestSelf = own, true
		answered, commit = 1, own.Commit
	}
	var errs []string
	var newer error
	var silent []string
	type ahead struct {
		p   string
		got afterResp
	}
	var uncounted []ahead // answers that don't count, at this history
	var forked []string   // copies whose log forked from this one
	type answer struct {
		got afterResp
		err error
	}
	// Every peer at once; decided once a majority that counts answered
	// (any majority meets the commit majority, so its most advanced log
	// holds every committed entry), after a short wait for the rest. A
	// straggler that has seen a newer term is not waited for: this
	// leader's own lease ends it, and its replication is refused by term.
	// Each peer's first page decides: every page names its whole log (its
	// last entry, commit, term, history). Only the chosen log's further
	// pages are read, after the choice (#1455).
	fanOut(ctx, n, n.others(), func(ctx context.Context, p string) answer {
		got, err := n.fetchFirst(ctx, p, from) // a compacted copy answers its base
		return answer{got, err}
	}, func(p string, a answer) bool {
		switch {
		case a.err != nil:
			errs = append(errs, p+": "+a.err.Error())
			silent = append(silent, p)
		case a.got.History > hist.History:
			// A newer log history (a person accepted a loss elsewhere):
			// this copy's log is superseded (fetchAfter has learnt it).
			newer = fmt.Errorf("%w: %s holds a newer log history (%d; this copy %d): %w", ErrReadOnly, n.name(p), a.got.History, hist.History, errSuperseded)
			return true
		case a.got.Term > term:
			// A peer has seen a newer term: this leadership is over.
			newer = fmt.Errorf("%w: a peer is at term %d, this leader at %d", ErrReadOnly, a.got.Term, term)
			return true
		case a.got.History < hist.History && !mutOplog("history_lower_counted"):
			// An older history's copy: its loss was accepted when this one
			// started; it neither counts nor holds anything to wait for.
			errs = append(errs, p+": its log is an older history's (superseded)")
		case a.got.Lineage.forks(hist) && !mutOplog("history_fork_counted"):
			// Another override's log at the same number: it never counts,
			// and only a person's choice (a vouch here, its catch-up
			// overriding it) ends the fork.
			errs = append(errs, fmt.Sprintf("%s: its log forked from this one at history %d (accepted by %s)", p, a.got.History, a.got.HistoryBy))
			forked = append(forked, p)
		case !n.counts(p, a.got.Unsynced, a.got.Identity, a.got.Lineage, hist):
			errs = append(errs, p+": its log is not synced")
			uncounted = append(uncounted, ahead{p, a.got})
		default:
			answered++
			commit = max(commit, a.got.Commit)
			if better(a.got, best) {
				best, bestSelf, bestPeer = a.got, false, p
			}
		}
		return !alone && answered >= n.majority()
	})
	if newer != nil {
		return newer
	}
	// What going ahead alone would override: a silent peer (it may hold a
	// write a majority committed without this log), and a copy that
	// answers but doesn't count yet with a later log (a witness in its
	// quiet period after a re-commission, #1053), whose entries would be
	// lost. Each only as the person accepted (Mark.Accept), else it waits.
	var outside []string
	for _, p := range silent {
		if !accept.covers(p, true, false, 0, 0) && !mutOplog("vouch_alone_partial") {
			outside = append(outside, n.name(p)+" does not answer")
		}
	}
	overrides := len(silent) > 0
	waitAhead := ""
	for _, u := range uncounted {
		if !better(u.got, best) {
			continue
		}
		overrides = true
		if accept.covers(u.p, false, false, u.got.LastTerm, u.got.LastSeq) || mutOplog("vouch_alone_ignores_ahead") {
			continue
		}
		if accept == nil {
			waitAhead = fmt.Sprintf("%s holds a later log (term %d, seq %d) but does not count yet", n.name(u.p), u.got.LastTerm, u.got.LastSeq)
		}
		outside = append(outside, fmt.Sprintf("%s holds a later log (term %d, seq %d) that does not count", n.name(u.p), u.got.LastTerm, u.got.LastSeq))
	}
	// A fork the person accepted at the vouch is overridden by this
	// catch-up even with a majority that counts: the only case a vouch
	// acts on a synced copy whose majority counts, since choosing one side
	// of a fork is what it is for. A fork not accepted waits.
	resolves := false
	for _, p := range forked {
		switch {
		case alone && accept.covers(p, false, true, 0, 0):
			resolves = !mutOplog("fork_resolved_without_bump")
		case alone:
			outside = append(outside, n.name(p)+"'s log forked from this one")
		}
	}
	useVouch := answered < n.majority() && alone
	if (useVouch || alone && len(forked) > 0) && len(outside) > 0 {
		if waitAhead != "" && len(outside) == 1 {
			return fmt.Errorf("%w: a vouched catch-up waits: %s", ErrReadOnly, waitAhead)
		}
		if accept == nil {
			return fmt.Errorf("%w: catch-up heard from %d of %d that count (%s); a vouched catch-up needs every copy to answer", ErrReadOnly, answered, len(n.o.Peers), strings.Join(errs, "; "))
		}
		sort.Strings(outside)
		return fmt.Errorf("%w: a vouched catch-up waits: not what was accepted (%s): naut redundancy vouch again", ErrReadOnly, strings.Join(outside, "; "))
	}
	if useVouch && overrides && len(silent) == len(n.others()) && !mutOplog("override_all_silent") {
		// Every other copy silent (naut-lead, 3c-3c-2a (i)): the new
		// history would sit on this copy alone, and the other two, back at
		// the old one, could commit there and lose it all at first contact.
		// Alone this copy commits nothing anyway.
		return fmt.Errorf("%w: a loss accepted with every other copy silent would leave the new history on this copy alone: bring one back or rebuild it (a rebuilt witness answers), then naut redundancy vouch again", ErrReadOnly)
	}
	if answered < n.majority() && !useVouch {
		why := ""
		switch {
		case !selfCounts:
			why = "; this replica's own log is not synced"
		case alone:
			why = "; a vouched catch-up needs every copy to answer"
		}
		return fmt.Errorf("%w: catch-up heard from %d of %d that count (%s)%s", ErrReadOnly, answered, len(n.o.Peers), strings.Join(errs, "; "), why)
	}
	// A counting copy's entries (the same history's) are taken first: a
	// crash before the bump below then leaves this copy at the old history
	// with them and its one-shot pending, never at the new one without
	// them (#1177 review nit). Page by page, each synced as it is taken
	// (#1455); nothing is committed until the last.
	moved := fmt.Errorf("%w: the log history moved during the catch-up", ErrReadOnly)
	deposed := func() error {
		if n.term > term && !mutOplog("leader_catchup_term_unchecked") {
			return fmt.Errorf("%w: a newer term (%d) was seen during the catch-up for term %d", ErrReadOnly, n.term, term)
		}
		return nil
	}
	take := func(p afterResp) error {
		n.mu.Lock()
		defer n.mu.Unlock()
		if !n.mark.Lineage.same(hist) || n.stoppedLocked() {
			return moved // a newer history was heard while the peers were asked
		}
		if err := deposed(); err != nil {
			return err
		}
		if n.basePage(p) {
			return n.takeBasePageLocked(bestPeer, p) // installed on its last page
		}
		if _, err := n.acceptLocked(p.Entries); err != nil {
			n.log.Warn("oplog: leader catch-up", "error", err)
			return err
		}
		return nil
	}
	if !bestSelf && (len(best.Entries) > 0 || n.basePage(best)) {
		if err := take(best); err != nil {
			return err
		}
		if after, _, done := n.nextPage(best); !done {
			// Every page from the log that was chosen, still the same and
			// still counting (Fable, #1455 review: a copy that lost its log
			// mid-read comes back empty at the same term and history, and
			// its empty page would end the read short of what was chosen).
			_, err := n.fetchAfter(ctx, bestPeer, after, func(p afterResp) error {
				same := p.Term == best.Term && p.Lineage.same(best.Lineage) && p.LastSeq == best.LastSeq && p.LastTerm == best.LastTerm && sameBase(p, best)
				if !mutOplog("leader_catchup_pages_unchecked") && (!same || !n.counts(bestPeer, p.Unsynced, p.Identity, p.Lineage, hist)) {
					return fmt.Errorf("%w: %s's log moved while it was read page by page", ErrReadOnly, n.name(bestPeer))
				}
				return take(p)
			})
			if err != nil {
				return err
			}
		}
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if !n.mark.Lineage.same(hist) || n.stoppedLocked() {
		return moved
	}
	if err := deposed(); err != nil {
		return err
	}
	if useVouch || resolves {
		// Spent before it is used: a mark that can't record that never
		// leaves a second use. An override starts a new log history in
		// the same write, durable before this copy is synced or takes or
		// sequences anything at it: the copies it overrides then count for
		// nothing, and are moved aside when they meet it.
		m := n.mark
		if !mutOplog("vouch_alone_kept") {
			m.Alone = false
		}
		m.Accept = nil
		bump := (overrides && useVouch || resolves) && !mutOplog("history_not_bumped") && !mutOplog("history_after_write")
		if bump {
			m.Lineage = newLineage(hist, accept)
		}
		if err := n.setMarkLocked(m); err != nil {
			return fmt.Errorf("oplog: recording that the vouch's catch-up is used: %w", err)
		}
		if bump {
			n.log.Warn("oplog: a vouched catch-up overrode a copy, as a person accepted: a new log history starts; the overridden copies count for nothing and are moved aside when they meet it", "history", m.History)
		}
	}
	// The best log a majority answered with holds every committed entry,
	// so any commit index one of them reported is safe here, and so is the
	// commit floor (a held K's Through, compact.go).
	if err := n.commitTo(max(commit, n.floor)); err != nil {
		return fmt.Errorf("%w: %w", ErrReadOnly, err)
	}
	n.syncedTerm = term
	n.becomeSyncedLocked("caught up from a majority as leader")
	if useVouch && overrides && mutOplog("history_after_write") {
		m := n.mark
		m.Lineage = newLineage(hist, accept)
		if err := n.setMarkLocked(m); err != nil {
			n.syncedTerm = 0
			return fmt.Errorf("oplog: recording the new log history: %w", err)
		}
	}
	if useVouch {
		n.log.Warn("oplog: caught up on a vouched log, without a majority that counts", "answered", answered, "commit", n.commit)
	}
	n.usedVouchLocked()
	return nil
}

// fetchPage reads one page of a peer's log after seq and notes what it
// says of itself (its sync, identity and history).
func (n *Node) fetchPage(ctx context.Context, peer string, after uint64, base bool) (afterResp, error) {
	var got afterResp
	q := ""
	if base {
		q = "&base=1" // a compacted base's pages (compact_wire.go)
	}
	if err := n.call(ctx, peer, fmt.Sprintf("/api/oplog?after=%d&page=1%s", after, q), nil, &got); err != nil {
		return afterResp{}, err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.notePeerLocked(peer, got.Unsynced, got.Identity, got.Lineage)
	n.notePeerWireLocked(peer, got.compactWire, got.Commit, 0)
	if err := n.learnHistoryLocked(got.Lineage); err != nil {
		return afterResp{}, err
	}
	return got, nil
}

// fetchAfter reads a peer's log after seq page by page, handing each page
// to take as it arrives: it holds one page at a time, and a log of any
// length is read (#1455: it gathered every page first, so a replacement
// held the log twice, and refused one over 16,384 pages). The pages must
// come from one log: the same term and history, each starting where the
// last ended. It returns the last page, its entries dropped.
//
// The first page may meet a compacted base (fetchFirst): a base's pages,
// then the tail after it, are read alike (nextPage).
func (n *Node) fetchAfter(ctx context.Context, peer string, after uint64, take func(afterResp) error) (afterResp, error) {
	var first afterResp
	base := false
	for page := 0; ; page++ {
		var got afterResp
		var err error
		if page == 0 {
			got, err = n.fetchFirst(ctx, peer, after)
		} else {
			got, err = n.fetchPage(ctx, peer, after, base)
		}
		if err != nil {
			return afterResp{}, err
		}
		if page == 0 {
			first = got
		} else if !n.pageFollows(first, got, after, base) {
			return afterResp{}, fmt.Errorf("oplog: %s's log moved while it was read page by page", peer)
		}
		if err := take(got); err != nil {
			return afterResp{}, err
		}
		next, b, done := n.nextPage(got)
		if done {
			got.Entries, got.More = nil, false
			return got, nil
		}
		after, base = next, b
	}
}

// CatchUp asks the leader for everything after our last entry and its
// commit index: at startup, on a gap, and periodically from a follower.
func (n *Node) CatchUp(ctx context.Context) error {
	if n.standalone() || n.isLeader() {
		return nil
	}
	leader := n.leaderAddr()
	if leader == "" {
		return ErrReadOnly
	}
	// From the commit index, not the last entry: an uncommitted entry this
	// instance holds may be stale, and only the leader's entries from the
	// commit on replace it (see acceptLocked). Each page is checked and
	// taken as it arrives, synced with it (#1455); the commit index moves
	// once, with the last page, so a lapse and the entries it names are
	// applied in one range (see applyRange, #1466).
	_, err := n.fetchAfter(ctx, leader, n.Commit(), func(got afterResp) error {
		if !got.LeaderSynced && !mutOplog("unsynced_leader_writes") {
			// Its entries past our commit index may be an earlier term's
			// leftovers that would replace one a later term committed
			// (#1050 review). Its heartbeat or a later pull brings them
			// once it has caught up.
			return errTermNotSynced
		}
		n.mu.Lock()
		defer n.mu.Unlock()
		switch {
		case n.stoppedLocked():
			return n.stopErrLocked()
		case !got.Lineage.same(n.mark.Lineage) && !mutOplog("history_lower_counted") && !mutOplog("history_fork_counted"):
			// A lower history's leader (its log is one a person accepted
			// to lose; a higher one superseded this copy in fetchPage),
			// or a forked one.
			return fmt.Errorf("%w: the leader's log is at history %d (id %x), this copy's at %d (id %x)", errConflict, got.History, got.HistoryID, n.mark.History, n.mark.HistoryID)
		}
		if got.Term < n.term {
			return fmt.Errorf("%w: leader at term %d, have seen %d", errConflict, got.Term, n.term)
		}
		if n.basePage(got) {
			return n.takeBasePageLocked(leader, got) // installed on its last page
		}
		if _, err := n.acceptLocked(got.Entries); err != nil {
			return err
		}
		switch {
		case !got.More || len(got.Entries) == 0:
			return n.commitTo(got.Commit)
		case mutOplog("catchup_commit_per_page"):
			return n.commitTo(min(got.Commit, got.Entries[len(got.Entries)-1].Seq))
		}
		return nil
	})
	return err
}
