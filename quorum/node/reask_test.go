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

// Every voter write at 0.8 of the lease (#1436): past three quarters, where
// a campaign stopped being asked again and no write that slow could win,
// and under seven eighths, where the last ask's wait now ends (the write
// budget). At a 1 s lease (75 ms of margin either side), the pair:
//
//   - elects at all;
//
//   - keeps that leader for 20 leases: its renewals need no write, so the
//     slow disk costs no lease once won (one acquisition each, none lost);
//
//   - hands the lead over on a switchover, inside the resigner's hold,
//     and keeps it.
//
// A re-ask window of three quarters of a lease turns it red: no leader in
// 20 leases (by hand: the mutation runner skips the loopback tests for a
// core mutation; reask_window_3q's rows catch it in package quorum).
func TestLoopbackWritesNearTheBudgetElectSwitchAndHold(t *testing.T) {
	c := newCluster(t)
	c.lease = time.Second
	c.slowWrite = c.lease * 8 / 10
	if b := writeBudget(c.lease); c.slowWrite <= c.lease*3/4 || c.slowWrite >= b {
		t.Fatalf("write %v is not between 3/4 lease and the budget %v", c.slowWrite, b)
	}
	c.startAll()
	c.commission()
	who := c.settledLeader(c.leases(20))
	peer := c.other(who)
	hold := func(leader quorum.NodeID, n int) {
		t.Helper()
		acq := [2]int{c.status(leader).Acquisitions, c.status(c.other(leader)).Acquisitions}
		end := time.Now().Add(c.leases(n))
		for time.Now().Before(end) {
			if l := c.leader(); l != leader || c.elector(c.other(leader)).IsLeader() {
				t.Fatalf("the leader is %q, want %s alone throughout %d leases", l, leader, n)
			}
			time.Sleep(10 * time.Millisecond)
		}
		if got := [2]int{c.status(leader).Acquisitions, c.status(c.other(leader)).Acquisitions}; got != acq {
			t.Fatalf("acquisitions %s/%s went %v -> %v over %d leases: the lease lapsed and was won again", leader, c.other(leader), acq, got, n)
		}
	}
	hold(who, 20)

	start := time.Now()
	rep, err := c.elector(who).Resign(context.Background(), 41, true, func(context.Context, time.Duration) error { return nil })
	if err != nil || !rep.Resigned {
		t.Fatalf("%s's resign: %+v %v", who, rep, err)
	}
	c.waitFor(string(peer)+" to lead inside "+string(who)+"'s resign hold", quorum.ResignHold(c.lease), func() bool { return c.elector(peer).IsLeader() })
	took := time.Since(start)
	hold(peer, 4)
	t.Logf("every write %v at a %v lease: held 20 leases; switchover led %v after the resign; %d state writes", c.slowWrite, c.lease, took, c.writes.Load())
}

// One slow voter costs nothing (#1436): the witness's writes take 450 ms,
// past the write budget at this lease, while both controllers' own are
// fast. A campaign needs any two grants, and the two controllers' voters
// give them at once, so the pair elects and a switchover hands over
// within a quarter lease, as with every disk fast: neither the re-asks
// nor the writer's de-dup make a fast majority wait on the slow third.
func TestLoopbackOneSlowVoterCostsNothing(t *testing.T) {
	c := newCluster(t)
	c.slowWrite, c.slowOnly = 450*time.Millisecond, WitnessID
	if c.slowWrite <= writeBudget(c.lease) {
		t.Fatalf("the witness's write %v is within the budget %v: the case proves nothing", c.slowWrite, writeBudget(c.lease))
	}
	c.startAll()
	c.commission()
	who := c.waitLeader(c.leases(20))
	peer := c.other(who)
	c.waitFor(string(peer)+" to see the leader", c.leases(8), func() bool { return c.status(peer).Leader == string(who) })
	time.Sleep(c.leases(1))
	start := time.Now()
	rep, err := c.elector(who).Resign(context.Background(), 41, true, func(context.Context, time.Duration) error { return nil })
	if err != nil || !rep.Resigned {
		t.Fatalf("%s's resign: %+v %v", who, rep, err)
	}
	c.waitFor(string(peer)+" to lead within a quarter lease", c.lease/4+c.lease/32, func() bool { return c.elector(peer).IsLeader() })
	t.Logf("the witness's writes %v: switchover led %v after the resign", c.slowWrite, time.Since(start))
}
