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
//   - it leads within lease/4, not after the old leader's lease;
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
	for i := 0; i < 4; i++ {
		switchOnce(t, c, who, peer)
		who, peer = peer, who
		time.Sleep(c.lease / 2) // the new leader's renewals reach both voters
	}
	time.Sleep(c.leases(1))
	if c.leader() != who {
		t.Fatalf("the leader a lease after the last switchover is %q, want %s", c.leader(), who)
	}
}

func switchOnce(t *testing.T, c *cluster, who, peer quorum.NodeID) {
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
	if took := time.Since(start); took > c.lease/4 {
		t.Fatalf("%s led %v after %s resigned, want under lease/4 = %v\n%s", peer, took, who, c.lease/4, c.voteTrail(peer, c.leases(1)))
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
			at := mark + toPeer + i
			c.waitFor("the campaign's answer", c.leases(1), func() bool {
				c.mu.Lock()
				defer c.mu.Unlock()
				v = c.votes[at]
				return v.done
			})
			if v.verdict != "granted" {
				t.Fatalf("the witness refused %s's first campaign after the RESIGN: %s", peer, v.verdict)
			}
			// At once: within lease/16 of its voter taking the RESIGN,
			// where the look grid is lease/4 (so the look-grid mutant passes
			// this at most a quarter of the time, each of four switches).
			// Measured: under 2 ms; 11-14 ms with -race at GOMAXPROCS=2.
			if d := v.at.Sub(p.at.Add(p.took)); d > c.lease/16 {
				t.Fatalf("%s campaigned %v after its voter took the RESIGN, want at once (under %v)", peer, d, c.lease/16)
			}
			return
		}
	}
	t.Fatalf("%s sent the witness no campaign after the RESIGN", peer)
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
