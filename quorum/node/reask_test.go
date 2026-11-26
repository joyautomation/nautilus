package node

import (
	"context"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/quorum"
)

// Every state write slow (#710, the lost switchover campaign): each
// voter's write, the witness's and both controllers', takes longer than a
// vote's own timeout (lease/8), as on a slow eMMC or SD card. A grant
// leaves only after its write, so every campaign's first ask times out
// at the candidate though the voters granted it. The candidate asks the
// same term again (Candidate.Tick's re-ask): a voter that already
// promised it that term answers at once, with no new write. So:
//
//   - the pair elects at all (no campaign could win before: each came
//     back at a new term, which needs a new write, and timed out again);
//
//   - a planned switchover hands over inside the resigner's hold, and
//     the resigner does not take the lead back;
//
//   - a failover elects the standby.
//
// no_reask and reask_new_term turn it red.
func TestLoopbackEveryWriteSlowStillElectsAndSwitches(t *testing.T) {
	c := newCluster(t)
	c.slowWrite = c.lease/8 + c.lease/16 // 94 ms at the loopback lease, past lease/8
	c.startAll()
	c.commission()
	who := c.settledLeader(leases(20))
	peer := c.other(who)
	c.waitFor(string(peer)+" to see the leader", c.leases(4), func() bool { return c.status(peer).Leader == string(who) })
	time.Sleep(c.leases(1))

	e := c.elector(who)
	start := time.Now()
	rep, err := e.Resign(context.Background(), 41, true, func(context.Context, time.Duration) error { return nil })
	if err != nil || !rep.Resigned || !rep.Drained || !rep.Witness || !rep.Peer {
		t.Fatalf("%s's resign: %+v %v", who, rep, err)
	}
	hold := quorum.ResignHold(c.lease)
	c.waitFor(string(peer)+" to lead inside "+string(who)+"'s resign hold", hold, func() bool { return c.elector(peer).IsLeader() })
	took := time.Since(start)
	time.Sleep(c.leases(1))
	if l := c.leader(); l != peer {
		t.Fatalf("the leader a lease after %s led is %q, want %s (the resigner took it back)", peer, l, peer)
	}

	c.kill(peer)
	fail := time.Now()
	c.waitFor(string(who)+" to take over from "+string(peer), c.leases(8), func() bool { return c.elector(who).IsLeader() })
	t.Logf("every write %v: switchover led %v after the resign (hold %v); failover led %v after the kill; %d state writes in all",
		c.slowWrite, took, hold, time.Since(fail), c.writes.Load())
}
