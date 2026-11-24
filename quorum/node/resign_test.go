package node

import (
	"context"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/quorum"
)

// A planned switchover's RESIGN on real elections over HTTP (#710 d1a),
// four times, back and forth. Each time:
//   - the witness takes the RESIGN before the peer's voter is sent it, so
//     the standby's first campaign after it is granted by the witness;
//   - the standby campaigns at once when its voter takes the RESIGN, not
//     at its next look;
//   - it leads within lease/4, not after the old leader's lease;
//   - a switchover back is not held up by the new standby's own resign
//     hold.
//
// resign_peer_first, resign_waits_for_look and resign_keeps_own_hold
// turn it red.
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
	rep := c.elector(who).Resign(context.Background(), 41, true)
	if !rep.Resigned || !rep.Witness || !rep.Peer {
		t.Fatalf("%s's resign: %+v\n%s", who, rep, c.voteTrail(who, c.leases(1)))
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
