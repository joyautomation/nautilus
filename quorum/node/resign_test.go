package node

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/quorum"
)

// A planned switchover's RESIGN on real elections over HTTP (#710 d1a),
// four times, back and forth. Each time:
//
//   - the witness takes the RESIGN before the peer's voter is sent it, so
//     the standby's first campaign after it is granted by the witness;
//
//   - the standby campaigns at once when its voter takes the RESIGN, not
//     at its next look;
//
//   - it leads within lease/4, not after the old leader's lease. A grant
//     waits for its voter's state write, so a stalled disk delays it; the
//     bound the design promises then is a voter's write within
//     7*lease/8 (redundancy-test-plan F24): the campaign's last re-ask, at
//     3*lease/4, granted within a vote's wait, so it leads within
//     7*lease/8 of its first campaign, and never past the lease. A
//     handover past lease/4 is logged (#1120: one CI disk stall held both
//     voters' writes ~360 ms and b led at 372 ms);
//
//   - a switchover back is not held up by the new standby's own resign
//     hold.
//
//   - each RESIGN leaves with the resigner's gate already shut, and after
//     the outputs' drain, which starts with the gate shut and is handed
//     the fence margin to settle (#710 d1b).
//
// resign_peer_first, resign_waits_for_look, resign_keeps_own_hold,
// resign_publish_after_send and resign_before_drain turn it red.
func TestLoopbackAResignHandsTheLeadOverAtOnce(t *testing.T) {
	c := newCluster(t)
	c.startAll()
	c.commission()
	who := c.settledLeader(leases(20))
	peer := c.other(who)
	c.waitFor(string(peer)+" to see the leader", c.leases(4), func() bool { return c.status(peer).Leader == string(who) })
	time.Sleep(c.leases(1)) // the standby's voter has granted renewals of this leadership
	heard := 0              // switchovers whose witness answered the first campaign
	for i := 0; i < 4; i++ {
		if switchOnce(t, c, who, peer) {
			heard++
		}
		who, peer = peer, who
		time.Sleep(c.lease / 2) // the new leader's renewals reach both voters
	}
	if heard == 0 {
		t.Fatal("in no switchover did the witness answer the standby's first campaign")
	}
	time.Sleep(c.leases(1))
	if c.leader() != who {
		t.Fatalf("the leader a lease after the last switchover is %q, want %s", c.leader(), who)
	}
}

// switchOnce switches the lead from who to peer, and reports whether the
// witness answered peer's first campaign (granted: a refusal fails). An
// ask that timed out says nothing: peer leads on its own voter's grant.
func switchOnce(t *testing.T, c *cluster, who, peer quorum.NodeID) bool {
	t.Helper()
	c.mu.Lock()
	mark := len(c.votes)
	c.mu.Unlock()
	start := time.Now()
	e := c.elector(who)
	var drainEnd time.Time
	var gateAtDrain bool
	var settle time.Duration
	rep, err := e.Resign(context.Background(), 41, true, func(_ context.Context, s time.Duration) error {
		gateAtDrain, settle = e.IsLeader(), s
		time.Sleep(c.lease / 64) // the drain takes a while
		drainEnd = time.Now()
		return nil
	})
	if err != nil || !rep.Resigned || !rep.Drained || !rep.Witness || !rep.Peer {
		t.Fatalf("%s's resign: %+v %v\n%s", who, rep, err, c.voteTrail(who, c.leases(1)))
	}
	if gateAtDrain {
		t.Fatalf("%s's drain began with its gate open", who)
	}
	if settle != e.Fence() || settle <= 0 {
		t.Fatalf("the drain was handed %v to settle, want the fence margin %v", settle, e.Fence())
	}
	if c.elector(who).IsLeader() {
		t.Fatalf("the resigned leader %s's gate is open", who)
	}
	c.waitFor(string(peer)+" to lead", c.leases(4), func() bool { return c.elector(peer).IsLeader() })
	led := time.Now()
	switch took := led.Sub(start); {
	case took > c.lease:
		t.Fatalf("%s led %v after %s resigned, past the lease %v: no faster than a failover\n%s", peer, took, who, c.lease, c.voteTrail(peer, c.leases(1)))
	case took > c.lease/4:
		t.Logf("%s led %v after %s resigned, past lease/4 = %v: a voter's write was slow (F24)\n%s", peer, took, who, c.lease/4, c.voteTrail(peer, c.leases(1)))
	}

	c.mu.Lock()
	votes := append([]sentVote(nil), c.votes[mark:]...)
	c.mu.Unlock()
	toWit, toPeer := -1, -1
	for i, v := range votes {
		if !v.req.Resign || v.from != who {
			continue
		}
		switch v.to {
		case "w":
			toWit = i
		case peer:
			toPeer = i
		}
	}
	if toWit < 0 || toPeer < 0 {
		t.Fatalf("%s's RESIGN sent to the witness at %d, to %s at %d", who, toWit, peer, toPeer)
	}
	w, p := votes[toWit], votes[toPeer]
	if drainEnd.IsZero() || !w.at.After(drainEnd) {
		t.Fatalf("a RESIGN left before the outputs' drain ended (drain ended %v, RESIGN sent %v)", drainEnd.Sub(start), w.at.Sub(start))
	}
	if w.gateOpen || p.gateOpen {
		t.Fatalf("a RESIGN left with %s's gate open (to the witness: %v, to %s: %v)", who, w.gateOpen, peer, p.gateOpen)
	}
	if !(w.done && w.at.Add(w.took).Before(p.at)) {
		t.Fatalf("the RESIGN went to %s before the witness had answered its own (witness sent %v, answered after %v; peer sent %v)",
			peer, w.at.Sub(start), w.took, p.at.Sub(start))
	}
	for i, v := range votes[toPeer:] {
		if v.from == peer && v.to == "w" && v.req.Term != 0 && !v.req.Renewal && !v.req.Resign {
			// At once: within lease/16 of its voter taking the RESIGN,
			// where the look grid is lease/4 (so the look-grid mutant passes
			// this at most a quarter of the time, each of four switches).
			// Measured: under 2 ms; 11-14 ms with -race at GOMAXPROCS=2.
			if d := v.at.Sub(p.at.Add(p.took)); d > c.lease/16 {
				t.Fatalf("%s campaigned %v after its voter took the RESIGN, want at once (under %v)", peer, d, c.lease/16)
			}
			// The design's bound on a slow voter write (F24): the campaign
			// is asked again every lease/8, the last at 3*lease/4 after its
			// first send, and the last ask's grant may come a vote's wait
			// (lease/8) after that, so peer leads within 7*lease/8 of its
			// first campaign; lease/32 more for waitFor's 5 ms poll.
			if d, max := led.Sub(v.at), c.lease*7/8+c.lease/32; d > max {
				t.Fatalf("%s led %v after its first campaign, want under 7*lease/8 (+poll) = %v (F24)\n%s", peer, d, max, c.voteTrail(peer, c.leases(1)))
			}
			return witnessGranted(t, c, peer, mark+toPeer+i, v.req.Term)
		}
	}
	t.Fatalf("%s sent the witness no campaign after the RESIGN", peer)
	return false
}

// witnessGranted waits for the answers to peer's campaign for term at the
// witness, from c.votes[at] on: the first ask and each re-ask of the same
// term. A refusal fails; granted is true. An ask that failed at the
// transport (a vote's deadline, lease/8) is no answer: a stalled witness
// (#1120: 63 ms without an answer under -race, while the peer's grant took
// 2 ms) says nothing of the RESIGN's order, and peer leads on its own
// voter's grant without asking again, so false.
func witnessGranted(t *testing.T, c *cluster, peer quorum.NodeID, at int, term uint64) bool {
	t.Helper()
	var asks []sentVote
	c.waitFor("the campaign's answers", c.leases(1), func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		asks = asks[:0]
		for _, v := range c.votes[at:] {
			if v.from == peer && v.to == "w" && v.req.Term == term && !v.req.Renewal && !v.req.Resign {
				if !v.done {
					return false
				}
				asks = append(asks, v)
			}
		}
		return true
	})
	granted := false
	for _, v := range asks {
		switch {
		case v.err != nil:
		case v.verdict == "granted":
			granted = true
		default:
			t.Fatalf("the witness refused %s's campaign after the RESIGN: %s", peer, v.verdict)
		}
	}
	if !granted {
		t.Logf("the witness answered none of %s's %d campaign asks in time (a stalled witness): no verdict this switchover", peer, len(asks))
	}
	return granted
}

// A drain that does not finish sends no RESIGN (#710 d1b): the resigner
// has stopped leading, and its standby takes over only once the lease has
// run out at the voters, as after a failover. A drain that does not end is
// cut at half a lease. resign_despite_drain_error turns it red.
func TestLoopbackAnUnfinishedDrainSendsNoResign(t *testing.T) {
	c := newCluster(t)
	c.startAll()
	c.commission()
	who := c.settledLeader(leases(20))
	peer := c.other(who)
	c.waitFor(string(peer)+" to see the leader", c.leases(4), func() bool { return c.status(peer).Leader == string(who) })
	time.Sleep(c.leases(1))
	c.mu.Lock()
	mark := len(c.votes)
	c.mu.Unlock()
	start := time.Now()
	rep, err := c.elector(who).Resign(context.Background(), 41, true, func(ctx context.Context, _ time.Duration) error {
		<-ctx.Done() // a send that never ends
		return ctx.Err()
	})
	took := time.Since(start)
	if !errors.Is(err, ErrDrainUnproven) || !rep.Resigned || rep.Drained || rep.Witness || rep.Peer {
		t.Fatalf("resign with an unfinished drain: %+v %v; want resigned, not drained, no RESIGN taken", rep, err)
	}
	if took < c.lease/2-c.lease/16 || took > c.lease {
		t.Fatalf("the unfinished drain was cut after %v, want half a lease (%v)", took, c.lease/2)
	}
	c.mu.Lock()
	for _, v := range c.votes[mark:] {
		if v.req.Resign {
			c.mu.Unlock()
			t.Fatalf("a RESIGN left after an unfinished drain (%s to %s)", v.from, v.to)
		}
	}
	c.mu.Unlock()
	if c.elector(who).IsLeader() {
		t.Fatalf("%s's gate is open after its resign", who)
	}
	c.waitFor(string(peer)+" to lead", c.leases(4), func() bool { return c.elector(peer).IsLeader() })
}

// A drain that finishes, but only after the lease the resign cut short
// would have run out (the process stalled), frees nothing and sends no
// RESIGN: its own voter's promise may already back a new campaign, so the
// lease running out, as in a failover, is all that is left (#710 d1b, the
// node's late-drain path). resign_despite_late_drain turns it red.
func TestLoopbackALateDrainSendsNoResign(t *testing.T) {
	c := newCluster(t)
	c.startAll()
	c.commission()
	who := c.settledLeader(leases(20))
	peer := c.other(who)
	c.waitFor(string(peer)+" to see the leader", c.leases(4), func() bool { return c.status(peer).Leader == string(who) })
	time.Sleep(c.leases(1))
	c.mu.Lock()
	mark := len(c.votes)
	c.mu.Unlock()
	rep, err := c.elector(who).Resign(context.Background(), 41, true, func(context.Context, time.Duration) error {
		time.Sleep(c.lease + c.lease/4) // a stall: done, but after the resigned lease
		return nil
	})
	if !errors.Is(err, ErrDrainUnproven) || !rep.Resigned || rep.Drained || rep.Witness || rep.Peer {
		t.Fatalf("resign with a late drain: %+v %v; want resigned, not drained, no RESIGN taken", rep, err)
	}
	c.mu.Lock()
	for _, v := range c.votes[mark:] {
		if v.req.Resign {
			c.mu.Unlock()
			t.Fatalf("a RESIGN left after a drain that ended past the resigned lease (%s to %s)", v.from, v.to)
		}
	}
	c.mu.Unlock()
	c.waitFor(string(peer)+" to lead", c.leases(4), func() bool { return c.elector(peer).IsLeader() })
}
