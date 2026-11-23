package node

import (
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/quorum"
)

// leaseEvents is every EventLeaseMismatch id raised about voter in one
// direction, from its from'th event on (a restarted controller's own).
func (c *cluster) leaseEvents(id, voter quorum.NodeID, inbound bool, from int) []string {
	c.evMu.Lock()
	defer c.evMu.Unlock()
	var out []string
	for i, ev := range c.evBy[id] {
		if i >= from && ev.Kind == EventLeaseMismatch && ev.Voter == voter && ev.Inbound == inbound {
			out = append(out, ev.Detail)
		}
	}
	return out
}

// eventCount is how many events id has raised so far.
func (c *cluster) eventCount(id quorum.NodeID) int {
	c.evMu.Lock()
	defer c.evMu.Unlock()
	return len(c.evBy[id])
}

// waitLease waits for the last lease report id raised about voter (one
// direction) to contain want, or to be cleared (want "", after a raise).
func (c *cluster) waitLease(id, voter quorum.NodeID, inbound bool, want string) {
	c.t.Helper()
	c.waitFor(string(id)+"'s lease report about "+string(voter)+" = "+want, leases(20), func() bool {
		evs := c.leaseEvents(id, voter, inbound, 0)
		if len(evs) == 0 {
			return false
		}
		last := evs[len(evs)-1]
		if want == "" {
			return last == ""
		}
		return strings.Contains(last, want)
	})
}

// noLeaseReports fails if id raised any lease report from its from'th
// event on.
func (c *cluster) noLeaseReports(id quorum.NodeID, from int) {
	c.t.Helper()
	c.evMu.Lock()
	defer c.evMu.Unlock()
	for i, ev := range c.evBy[id] {
		if i >= from && ev.Kind == EventLeaseMismatch {
			c.t.Errorf("%s raised a lease report: %+v", id, ev)
		}
	}
}

// A witness given a shorter --lease than the pair's (the default fell to
// 1 s; a pair with an explicit 2 s lease upgraded its witness without
// --lease 2s) refuses every vote: both controllers raise it, naming the
// witness, once. Restarted with the pair's lease, it takes the votes and
// both clear it, once (#710).
func TestLoopbackWitnessRefusingTheLeaseIsRaisedAndCleared(t *testing.T) {
	c := newCluster(t)
	c.startAll()
	c.commission()
	who := c.settledLeader(leases(20))

	c.stopWitness()
	c.setLease(WitnessID, loopLease/2)
	c.startWitness()
	for _, id := range []quorum.NodeID{"a", "b"} {
		c.waitLease(id, WitnessID, false, "longer than this witness's")
	}
	c.steady(who, 4) // on the two controllers alone: no fault tolerance, but running
	for _, id := range []quorum.NodeID{"a", "b"} {
		evs := c.leaseEvents(id, WitnessID, false, 0)
		if len(evs) != 1 {
			t.Errorf("%s reported the witness's refusal %d times, want once: %q", id, len(evs), evs)
		}
		if len(evs) > 0 && strings.Contains(evs[0], "://") {
			t.Errorf("%s's report is the call, not what the witness said (an operator reads it): %q", id, evs[0])
		}
	}

	// Restarted still short, the witness takes the pair's lease for its
	// restart wait (the lease its file records) and refuses it after: no
	// all-clear in between (Fable review).
	c.stopWitness()
	c.startWitness()
	c.steady(who, 4)
	for _, id := range []quorum.NodeID{"a", "b"} {
		if evs := c.leaseEvents(id, WitnessID, false, 0); len(evs) != 1 {
			t.Errorf("%s across a restart of the still-short witness: %q, want the one raise", id, evs)
		}
	}

	c.stopWitness()
	c.setLease(WitnessID, loopLease)
	c.startWitness()
	for _, id := range []quorum.NodeID{"a", "b"} {
		c.waitLease(id, WitnessID, false, "")
	}
	c.steady(who, 2)
	for _, id := range []quorum.NodeID{"a", "b"} {
		if evs := c.leaseEvents(id, WitnessID, false, 0); len(evs) != 2 {
			t.Errorf("%s: %d reports about the witness, want a raise and a clear: %q", id, len(evs), evs)
		}
		if evs := c.leaseEvents(id, WitnessID, true, 0); len(evs) != 0 {
			t.Errorf("%s: the witness's refusal reported as its own voter's: %q", id, evs)
		}
	}
}

// The release note's upgrade order across the default's change (2 s to
// 1 s), with 2 and 1 loopback leases standing in for the two releases:
// standby first, then the leader, the witness last. A pair under a
// longer witness elects without a report. While the two controllers
// differ, each side reports it once, in its own direction, and never
// flaps; at the end no report stands anywhere (#710).
func TestLoopbackLeaseUpgradeOrderReportsOnceAndEndsClear(t *testing.T) {
	c := newCluster(t)
	long := 2 * loopLease
	for _, id := range []quorum.NodeID{"a", "b", WitnessID} {
		c.setLease(id, long)
	}
	c.startAll()
	c.commission()
	lead := c.settledLeader(leases(20))
	stby := c.other(lead)
	for _, id := range []quorum.NodeID{"a", "b"} {
		c.noLeaseReports(id, 0)
	}

	// 1. The standby upgrades: its voter refuses the leader's longer lease.
	c.kill(stby)
	stbyFrom := c.eventCount(stby)
	c.setLease(stby, loopLease)
	c.start(stby)
	c.waitLease(lead, stby, false, "longer than this voter's")
	c.waitLease(stby, lead, true, "longer than this voter's")
	c.steady(lead, 6) // the leader keeps leading on the witness; and a window for a flap to show
	if evs := c.leaseEvents(lead, stby, false, 0); len(evs) != 1 {
		t.Errorf("the leader reported the standby's refusal %d times, want once: %q", len(evs), evs)
	}
	if evs := c.leaseEvents(stby, lead, true, stbyFrom); len(evs) != 1 {
		t.Errorf("the standby reported refusing the leader %d times, want once: %q", len(evs), evs)
	}
	if evs := c.leaseEvents(lead, stby, true, 0); len(evs) != 0 {
		t.Errorf("the leader's own voter refused nothing, but it reported %q", evs)
	}
	if evs := c.leaseEvents(stby, lead, false, stbyFrom); len(evs) != 0 {
		t.Errorf("the leader's voter takes the standby's shorter lease, but the standby reported %q", evs)
	}

	// 2. The leader upgrades: the upgraded standby takes over, and the
	// old leader comes back on the shorter lease, which its peer takes.
	c.kill(lead)
	leadFrom := c.eventCount(lead)
	c.setLease(lead, loopLease)
	c.start(lead)
	c.waitLease(stby, lead, true, "")

	// 3. The witness, last.
	c.stopWitness()
	c.setLease(WitnessID, loopLease)
	c.startWitness()
	now := c.settledLeader(leases(20))
	c.steady(now, 4)
	c.noLeaseReports(lead, leadFrom)
	for id, from := range map[quorum.NodeID]int{lead: leadFrom, stby: stbyFrom} { // each one's running process
		for _, voter := range []quorum.NodeID{c.other(id), WitnessID} {
			for _, in := range []bool{false, true} {
				if evs := c.leaseEvents(id, voter, in, from); len(evs) > 0 && evs[len(evs)-1] != "" {
					t.Errorf("%s still reports a lease refusal about %s (inbound %v): %q", id, voter, in, evs)
				}
			}
		}
	}
}
