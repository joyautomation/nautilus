package node

import "testing"

// A standby restarts under a live leader while every state write is slow
// (10/16 of a lease, as on a slow SD card): its voter, in its restart
// wait, refuses the leader's renewal at the very term it promised the
// leader. The leader still holds its lease steadily. Before, it re-termed
// its renewal on that refusal, the re-term needed a write at every voter,
// and the witness's outlasted the lease (#1120, TestLoopbackStandbyRestarts
// on CI). Now a refusal from a voter that promised this boot the term
// re-terms only once the term stops renewing (Candidate.renewLacksMajority).
// reterm_on_own_promise and reterm_with_majority turn it red (core
// mutants: run against the code, as the loopback suite skips under them).
func TestLoopbackSlowWritesAndAStandbyRestartKeepTheLease(t *testing.T) {
	c := newCluster(t)
	c.slowWrite = c.lease * 10 / 16
	c.startAll()
	c.commission()
	who := c.settledLeader(leases(20))
	sb := c.other(who)
	// The standby has granted the led term (its renewal grants need no
	// write): on these writes the campaign won on {leader, witness} and
	// the standby's own grant of it timed out (Fable: 8 in 264 runs under
	// load killed it before the leader had heard one, so the plain
	// re-term fired; that window is named in renewLacksMajority).
	c.steady(who, 2)
	c.kill(sb)
	c.start(sb)
	c.steady(who, 4)
}
