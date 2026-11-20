package node

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/quorum"
)

// The election and the pair's operator-intent log (#710 PR 3b-4b,
// logpref.go), with a stand-in for the log's report: an unsynced leader
// yields to a synced, able peer, at most once per yieldEvery, and never to
// one that may not campaign; an unsynced standby defers to a synced peer,
// for two leases at most with no leader.

// logCluster is a commissioned pair whose members report a log (logOn),
// all synced, with leader L settled.
func logCluster(t *testing.T) (*cluster, quorum.NodeID) {
	t.Helper()
	c := newCluster(t)
	for _, id := range []quorum.NodeID{"a", "b"} {
		c.ctl[id].logOn.Store(true)
	}
	c.startAll()
	c.commission()
	return c, c.settledLeader(leases(20))
}

func (c *cluster) countEvents(kind EventKind) (n int, last string) {
	c.evMu.Lock()
	defer c.evMu.Unlock()
	for _, ev := range c.evs {
		if ev.Kind == kind {
			n++
			last = ev.Detail
		}
	}
	return n, last
}

// An unsynced leader whose catch-up failed hands the lead to its synced,
// healthy peer, which then holds it: the yielded one doesn't campaign
// back while the peer is reachable and synced.
func TestLoopbackUnsyncedLeaderYieldsToASyncedPeer(t *testing.T) {
	c, l := logCluster(t)
	o := c.other(l)
	c.ctl[l].logUnsynced.Store(true)
	c.ctl[l].logStuck.Store(true)
	c.waitFor("the synced peer to lead", leases(20), func() bool { return c.leader() == o })
	n, msg := c.countEvents(EventYielded)
	if n != 1 || !strings.Contains(msg, "yielded to "+string(o)) || !strings.Contains(msg, "own operator log unsynced") || !strings.Contains(msg, "able to sync") {
		t.Fatalf("yield events: %d, %q", n, msg)
	}
	c.steady(o, 8)
}

// A renewal round sent before the yield and granted after it moves the
// lease's end: the yield holds until the lease is seen over, not until the
// instant it was due to end.
func TestYieldHoldsUntilTheLeaseIsSeenOver(t *testing.T) {
	due := quorum.Instant(time.Hour)
	if !yieldHolds(true, true, due.Add(time.Millisecond), due) {
		t.Fatal("the yield let renewals resume while a late grant keeps the lease")
	}
	if yieldHolds(true, false, due.Add(-time.Millisecond), due) {
		t.Fatal("the yield held after the lease was over")
	}
	if yieldHolds(false, true, due, due) {
		t.Fatal("held with no yield")
	}
}

// At most one yield per yieldEvery: a flapping peer (it takes over, dies,
// comes back synced) doesn't bounce the lead a second time.
func TestLoopbackYieldIsDamped(t *testing.T) {
	c, l := logCluster(t)
	o := c.other(l)
	c.ctl[l].logUnsynced.Store(true)
	c.ctl[l].logStuck.Store(true)
	c.waitFor("the synced peer to lead", leases(20), func() bool { return c.leader() == o })
	c.kill(o) // it dies before syncing l
	c.waitFor("the unsynced survivor to lead", leases(20), func() bool { return c.leader() == l })
	c.start(o) // back, synced
	c.waitFor("the peer to answer synced", leases(20), func() bool {
		e := c.elector(l)
		e.mu.Lock()
		defer e.mu.Unlock()
		return e.peerLogFresh() && e.peerLog.synced && e.peerLog.may
	})
	c.steady(l, 8)
	if n, _ := c.countEvents(EventYielded); n != 1 {
		t.Fatalf("%d yields within yieldEvery, want 1", n)
	}
}

// No yield to a peer that may not campaign (its scans are not live): the
// unsynced leader keeps the plant under control, read-only.
func TestLoopbackNoYieldToAnUnhealthyPeer(t *testing.T) {
	c, l := logCluster(t)
	o := c.other(l)
	c.ctl[o].sick.Store(true)
	c.ctl[l].logUnsynced.Store(true)
	c.ctl[l].logStuck.Store(true)
	c.waitFor("the leader to hear the peer's report", leases(20), func() bool {
		e := c.elector(l)
		e.mu.Lock()
		defer e.mu.Unlock()
		return e.peerLogFresh() && e.peerLog.synced && !e.peerLog.may
	})
	c.steady(l, 8)
	if n, _ := c.countEvents(EventYielded); n != 0 {
		t.Fatalf("yielded to an unhealthy peer (%d)", n)
	}
}

// No yield to a peer that is synced but can't sync the others (its other
// copies all lost, no vouch pending): it would lead read-only too, so the
// lead isn't handed over for nothing (naut-lead, #1053).
func TestLoopbackNoYieldToAPeerThatCannotSync(t *testing.T) {
	c, l := logCluster(t)
	o := c.other(l)
	c.ctl[o].logCannotSync.Store(true)
	c.ctl[l].logUnsynced.Store(true)
	c.ctl[l].logStuck.Store(true)
	c.waitFor("the leader to hear the peer's report", leases(20), func() bool {
		e := c.elector(l)
		e.mu.Lock()
		defer e.mu.Unlock()
		return e.peerLogFresh() && e.peerLog.synced && !e.peerLog.canSync && e.peerLog.may
	})
	c.steady(l, 8)
	if n, _ := c.countEvents(EventYielded); n != 0 {
		t.Fatalf("yielded to a peer that can't sync (%d)", n)
	}
}

// An unsynced standby defers to a synced peer that may campaign but has
// lost its lease (its vote requests are cut): for two leases with no
// leader, then it campaigns as usual.
func TestLoopbackUnsyncedStandbyDefersTwoLeasesAtMost(t *testing.T) {
	c, l := logCluster(t)
	o := c.other(l)
	c.ctl[o].logUnsynced.Store(true)
	c.waitFor("the standby to hear the leader's report", leases(20), func() bool {
		e := c.elector(o)
		e.mu.Lock()
		defer e.mu.Unlock()
		return e.peerLogFresh() && e.peerLog.synced && e.peerLog.may
	})
	// l's vote requests are lost; o still reaches l's hello.
	c.mu.Lock()
	c.mute = map[quorum.NodeID]bool{l: true}
	c.mu.Unlock()
	c.waitFor("the leader's lease to lapse", leases(10), func() bool { return c.leader() == "" })
	gone := time.Now()
	c.waitFor("the unsynced standby to lead in the end", leases(20), func() bool { return c.leader() == o })
	if d := time.Since(gone); d < leases(2) {
		t.Fatalf("the unsynced standby led %v after the synced leader's lease lapsed, want two leases of deferring at least", d)
	}
}

// The witness's seams for the log (Bound, Grant, SetLogInfo): a cluster is
// bound by a vote only, Grant names whom the voter granted, and Pairs
// reports the log the composer says it holds.
func TestWitnessLogSeams(t *testing.T) {
	c, l := logCluster(t)
	w := c.wit.w
	if m, ok := w.Bound(c.pair); !ok || m != "a,b" {
		t.Fatalf("Bound(pair) = %q, %v", m, ok)
	}
	if _, ok := w.Bound(NewClusterID()); ok {
		t.Fatal("a cluster with no vote is bound")
	}
	if _, ok := w.Bound("../x"); ok {
		t.Fatal("an invalid cluster name is bound")
	}
	g, promised, inc, ok := w.Grant(c.pair)
	if !ok || g != l || promised == 0 || inc != c.status("a").Incarnation {
		t.Fatalf("Grant = %q, %d, %d, %v; leader %s", g, promised, inc, ok, l)
	}
	if _, _, _, ok := w.Grant(NewClusterID()); ok {
		t.Fatal("Grant for a cluster with no voter")
	}
	w.SetLogInfo(func(cluster string) (uint64, uint64, bool) { return 7, 5, cluster == c.pair })
	if ps := w.Pairs(); len(ps) != 1 || !ps[0].HasLog || ps[0].LogLast != 7 || ps[0].LogCommit != 5 {
		t.Fatalf("Pairs = %+v", ps)
	}
}

// A leader whose own voter marked its peer a confirmed duplicate doesn't
// yield to it, whatever the peer's report says: that peer's vote counts
// nowhere here, and the voter refuses it every new grant, so it can't
// take the lead the yield hands it (#1029 3c, Fable on #1094). The
// peer's report can still say "may campaign" when no voter told the peer
// itself (its replies lost). yield_to_marked_peer turns it red.
func TestLoopbackNoYieldToAPeerItsVoterMarked(t *testing.T) {
	c, l := logCluster(t)
	o := c.other(l)
	e := c.elector(l)
	e.mu.Lock()
	// A second boot of o, heard three times (a mark needs two new frames
	// past the delay from each boot); o's own probes, still arriving a
	// quarter lease and more later, confirm it.
	e.v.Handle(e.now(), quorum.Request{From: o, Nonce: 0xd0d0, ID: quorum.ProbeID, Seq: 1})
	e.mu.Unlock()
	time.Sleep(leases(1) / 2)
	e.mu.Lock()
	e.v.Handle(e.now(), quorum.Request{From: o, Nonce: 0xd0d0, ID: quorum.ProbeID, Seq: 2})
	e.v.Handle(e.now(), quorum.Request{From: o, Nonce: 0xd0d0, ID: quorum.ProbeID, Seq: 3})
	e.mu.Unlock()
	c.waitFor("the leader's voter to mark its peer", leases(10), func() bool {
		e.mu.Lock()
		defer e.mu.Unlock()
		return slices.Contains(e.v.Marked(), o)
	})
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.now()
	if !e.c.Leading(now) {
		t.Skip("the leader lapsed meanwhile")
	}
	e.peerLog = peerLog{synced: true, canSync: true, may: true, at: time.Now()}
	e.yieldedAt = time.Time{}
	e.maybeYield(now, LogState{Stuck: true}, true)
	if e.yieldHold {
		t.Fatal("yielded to a peer this controller's voter marked a confirmed duplicate")
	}
}

// A controller a voter marks a confirmed duplicate reports "may not
// campaign" in its hello: it campaigns no more (quorum.Candidate.NoteMark),
// so an unsynced leader must not yield to it and wait for a takeover that
// never comes (#1029 3c, Fable on #1094). may_ignores_mark turns it red.
func TestLoopbackAMarkedControllerSaysItMayNotCampaign(t *testing.T) {
	c, l := logCluster(t)
	o := c.other(l)
	e := c.elector(o)
	if r := e.selfLog(); len(r) != 3 || r[2] != 'm' {
		t.Fatalf("report %q before the mark, want one that may campaign", r)
	}
	e.mu.Lock()
	e.noteMark(quorum.Reply{From: o, To: o, Nonce: e.nonce, ID: quorum.ProbeID, Marked: true})
	e.mu.Unlock()
	if r := e.selfLog(); len(r) != 3 || r[2] != 'n' {
		t.Fatalf("report %q while marked, want one that may not campaign", r)
	}
	c.waitFor("the operator to be told", leases(4), func() bool {
		n, last := c.countEvents(EventDuplicateMarked)
		return n == 1 && strings.Contains(last, "campaigns no more")
	})
}

// The confirmed duplicate alarm follows the voter's mark count, not the
// cause of its last duplicate: a frame that completes a mark can also be
// a campaign a relayed promise refuses, which counts again, as DupNonce,
// in the same Handle (#1113 review). The alarm must still say the name is
// marked, and how to clear it; and it is never paced away by the alarm
// before it. mark_alarm_from_cause turns it red.
func TestTheConfirmedAlarmSurvivesARelayCountInTheSameHandle(t *testing.T) {
	c, l := logCluster(t)
	e := c.elector(l)
	e.mu.Lock()
	now := e.now()
	e.dupAt = time.Now() // an alarm a moment ago: the pacing would swallow the next
	e.v.Handle(now, quorum.Request{From: "z", Nonce: 0xA, ID: quorum.ProbeID, Seq: 1})
	e.v.Handle(now.Add(10*time.Millisecond), quorum.Request{From: "z", Nonce: 0xC, ID: quorum.ProbeID, Seq: 1})
	d := time.Duration(float64(e.o.Lease/4)*(1+quorum.MaxRho)) + 50*time.Millisecond
	e.v.Handle(now.Add(d), quorum.Request{From: "z", Nonce: 0xA, ID: quorum.ProbeID, Seq: 2})
	e.v.Handle(now.Add(d+10*time.Millisecond), quorum.Request{From: "z", Nonce: 0xC, ID: quorum.ProbeID, Seq: 2})
	e.v.Handle(now.Add(d+20*time.Millisecond), quorum.Request{From: "z", Nonce: 0xA, ID: quorum.ProbeID, Seq: 3})
	// A relayed promise to z's first boot, then the second boot's campaign
	// that completes the mark AND is refused by that relay.
	e.v.Hear(now.Add(d+25*time.Millisecond), "w", 0x5, quorum.Relay{To: "z", Nonce: 0xA, Left: e.o.Lease})
	e.v.Handle(now.Add(d+30*time.Millisecond), quorum.Request{From: "z", Term: 1 << 40, Nonce: 0xC, ID: 1, Seq: 3})
	if !slices.Contains(e.v.Marked(), "z") {
		e.mu.Unlock()
		t.Fatalf("the sequence didn't mark z: %v", e.v.Marked())
	}
	e.noteDuplicates()
	e.mu.Unlock()
	c.waitFor("the confirmed alarm", leases(4), func() bool {
		n, _ := c.countEventsWith(EventDuplicateMember, "two processes claim to be z: this voter heard both running at once")
		return n == 1
	})
}

// countEventsWith counts the events of kind whose detail contains s.
func (c *cluster) countEventsWith(kind EventKind, s string) (n int, last string) {
	c.evMu.Lock()
	defer c.evMu.Unlock()
	for _, ev := range c.evs {
		if ev.Kind == kind && strings.Contains(ev.Detail, s) {
			n++
			last = ev.Detail
		}
	}
	return n, last
}

// Witness.ClearDuplicate clears the witness's mark on a name in a pair it
// serves, and reports whether there was one; an unknown pair or name
// reports false (#1029 3c, the Go call 3c-3's command will make).
func TestWitnessClearDuplicate(t *testing.T) {
	c, _ := logCluster(t)
	w := c.wit.w
	if w.ClearDuplicate("no-such-pair", "z") {
		t.Fatal("cleared a mark in a pair the witness doesn't serve")
	}
	w.mu.Lock()
	p := w.pairs[c.pair]
	w.mu.Unlock()
	if p == nil {
		t.Fatal("the witness serves no pair named for the cluster")
	}
	p.mu.Lock()
	now := w.now()
	d := time.Duration(float64(loopLease/4)*(1+quorum.MaxRho)) + 50*time.Millisecond
	for i, at := range []time.Duration{0, 10 * time.Millisecond, d, d + 10*time.Millisecond, d + 20*time.Millisecond, d + 30*time.Millisecond} {
		nonce := []uint64{0xA, 0xC}[i%2]
		p.v.Handle(now.Add(at), quorum.Request{From: "z", Nonce: nonce, ID: quorum.ProbeID, Seq: uint64(1 + i/2)})
	}
	marked := slices.Contains(p.v.Marked(), "z")
	p.mu.Unlock()
	if !marked {
		t.Fatal("the sequence didn't mark z at the witness")
	}
	if w.ClearDuplicate(c.pair, "y") {
		t.Fatal("cleared a name that wasn't marked")
	}
	if !w.ClearDuplicate(c.pair, "z") {
		t.Fatal("ClearDuplicate reported z unmarked")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.v.Marked()) != 0 {
		t.Fatalf("still marked after the clear: %v", p.v.Marked())
	}
}
