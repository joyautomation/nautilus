package node

import "testing"

// Takeover or start (#710 (b), Elector.IsTakeover): a new pair's first
// leader starts (nobody led before it); the standby that takes over when
// that leader dies, having seen its lease, takes over.
// every_acquisition_a_takeover turns it red.
func TestLoopbackAFirstLeaderStartsAStandbyTakesOver(t *testing.T) {
	c := newCluster(t)
	c.startAll()
	c.commission()
	who := c.settledLeader(leases(20))
	if c.elector(who).IsTakeover() {
		t.Fatal("a new pair's first leader counts as a takeover")
	}
	peer := c.other(who)
	c.waitFor(string(peer)+" to see the leader", c.leases(4), func() bool { return c.status(peer).Leader == string(who) })
	c.kill(who)
	c.waitFor(string(peer)+" to lead", c.leases(6), func() bool { return c.elector(peer).IsLeader() })
	if !c.elector(peer).IsTakeover() {
		t.Fatal("the standby that took over from a live leader counts as a start")
	}
}
