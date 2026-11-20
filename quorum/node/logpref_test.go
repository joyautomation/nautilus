package node

import (
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
	if n != 1 || !strings.Contains(msg, "yielded to "+string(o)) || !strings.Contains(msg, "own operator log unsynced") {
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
