package node

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/quorum"
)

// A rolling upgrade of a running pair: the three members restarted one at
// a time onto a new release. Within one release only the leader's restart
// stops control (the planned handover). Across a vote protocol version
// (quorum.WireVersion) two members on different releases can't vote with
// each other, so the old leader's majority and the new one share a member,
// which must restart: control stops once whatever the order. The guide's
// order (the standby, the witness, then the former leader) makes that
// member the witness, the quickest to start, and the old leader's restart
// then costs nothing. These run on loopback with a modeled start time per
// restart, and check the election alone: a takeover on a controller adds
// the log catch-up, the inputs wait, a scan and the output push.

// upgradeDown is a restarted box's modeled start time: long enough that a
// stop which waits for the restarted box shows, against one that doesn't.
const upgradeDown = 2 * loopLease

// upgrade restarts id onto release rel after down: a controller as a power
// cut and a start on its own state file, the witness as a stop and a start
// on its state directory.
func (c *cluster) upgrade(id quorum.NodeID, rel int, down time.Duration) {
	c.t.Helper()
	if id == WitnessID {
		c.stopWitness()
	} else {
		c.kill(id)
	}
	time.Sleep(down)
	c.mu.Lock()
	if c.release == nil {
		c.release = map[quorum.NodeID]int{}
	}
	c.release[id] = rel
	c.mu.Unlock()
	if id == WitnessID {
		c.startWitness()
	} else {
		c.start(id)
	}
}

// stop is an interval in which no controller held the gate.
type stop struct {
	took time.Duration
	// takeover: the gate opened again for another controller, or another
	// term. Not one: a late renewal under -race, the same leader and term
	// again (#1120).
	takeover bool
}

// stopRecorder samples the gates every millisecond and keeps each stop
// under the step that was running when it began.
type stopRecorder struct {
	mu    sync.Mutex
	step  string
	stops map[string][]stop
	end   chan struct{}
	done  chan struct{}
}

func (c *cluster) recordStops() *stopRecorder {
	g := &stopRecorder{stops: map[string][]stop{}, end: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(g.done)
		var since time.Time
		var during string
		var who quorum.NodeID
		var term uint64
		tk := time.NewTicker(time.Millisecond)
		defer tk.Stop()
		for {
			select {
			case <-g.end:
				return
			case <-tk.C:
			}
			g.mu.Lock()
			step := g.step
			g.mu.Unlock()
			now, t := c.leader(), uint64(0)
			if now != "" {
				if e := c.elector(now); e != nil {
					t = e.Term()
				}
			}
			switch {
			case now == "" && since.IsZero():
				since, during = time.Now(), step
			case now != "" && !since.IsZero():
				g.mu.Lock()
				g.stops[during] = append(g.stops[during], stop{took: time.Since(since), takeover: now != who || t != term})
				g.mu.Unlock()
				since = time.Time{}
			}
			if now != "" {
				who, term = now, t
			}
		}
	}()
	return g
}

func (g *stopRecorder) at(step string) { g.mu.Lock(); g.step = step; g.mu.Unlock() }

func (g *stopRecorder) close() map[string][]stop {
	close(g.end)
	<-g.done
	return g.stops
}

// takeovers is the stops in a step that handed control on.
func takeovers(ss []stop) (n int, longest time.Duration) {
	for _, s := range ss {
		if s.takeover {
			n++
			longest = max(longest, s.took)
		}
	}
	return n, longest
}

// roll restarts a settled pair's members in order (by role: "standby",
// "witness", "leader", as they stand at the start) onto release rel, each
// after down, and returns the stops per step.
func roll(t *testing.T, lease time.Duration, rel int, down time.Duration, order ...string) map[string][]stop {
	c := newCluster(t)
	c.lease = lease
	c.startAll()
	c.commission()
	old := c.settledLeader(c.leases(20))
	time.Sleep(c.leases(3)) // past any restarted voter's wait
	id := map[string]quorum.NodeID{"leader": old, "standby": c.other(old), "witness": WitnessID}
	g := c.recordStops()
	for _, role := range order {
		g.at(role)
		c.upgrade(id[role], rel, down)
		// Settled: a leader, then past every restarted voter's wait. Not
		// the witness's backing: while the releases differ a leader may
		// rest on the two controllers alone.
		c.waitFor("a leader after restarting the "+role, c.leases(40), func() bool { return c.leader() != "" })
		time.Sleep(c.leases(6))
		c.waitFor("a leader, settled, after restarting the "+role, c.leases(40), func() bool { return c.leader() != "" })
	}
	stops := g.close()
	var b strings.Builder
	for _, role := range order {
		n, longest := takeovers(stops[role])
		fmt.Fprintf(&b, " %s: %d takeover(s), longest stop %v (%d stop(s) in all);", role, n, longest.Round(time.Millisecond), len(stops[role]))
	}
	t.Logf("release %d, order %v, lease %v, start time %v:%s", rel, order, lease, down, b.String())
	return stops
}

// Within one release the standby's and the witness's restarts never stop
// control, and the leader's costs one takeover that doesn't wait for the
// restarted box: the standby takes over alone.
//
// A measurement guard, the baseline the upgrade order is judged against,
// not a safety gate: of every protocol mutation, only no_reterm turns it
// red, and only now and then (1 run in 5).
func TestRollingRestartStopsOnlyAtTheLeader(t *testing.T) {
	stops := roll(t, loopLease, 0, upgradeDown, "standby", "witness", "leader")
	for _, role := range []string{"standby", "witness"} {
		if n, _ := takeovers(stops[role]); n != 0 {
			t.Errorf("restarting the %s: %d takeover(s), want none: %+v", role, n, stops[role])
		}
	}
	if n, longest := takeovers(stops["leader"]); n != 1 || longest >= upgradeDown {
		t.Errorf("restarting the leader: %d takeover(s), longest %v; want one, shorter than the old leader's start time %v (the standby takes over alone)", n, longest, upgradeDown)
	}
}

// Across a vote protocol version, in the guide's order: the standby's
// restart costs nothing (the leader keeps the witness's vote), the
// witness's costs the one takeover (the old leader loses its majority, and
// the upgraded standby leads once the witness is back), and the former
// leader's restart then costs nothing.
func TestRollingUpgradeAcrossAWireVersion(t *testing.T) {
	stops := roll(t, loopLease, 1, upgradeDown, "standby", "witness", "leader")
	for _, role := range []string{"standby", "leader"} {
		if n, _ := takeovers(stops[role]); n != 0 {
			t.Errorf("upgrading the %s: %d takeover(s), want none: %+v", role, n, stops[role])
		}
	}
	// The stop runs from the witness's stop until it is back and past its
	// restart wait. The wait (a lease) outlasts what the old leader had
	// left of the lease the witness last granted, so the stop is at least
	// the witness's start time: the guide's number.
	if n, longest := takeovers(stops["witness"]); n != 1 || longest < upgradeDown || longest > upgradeDown+4*loopLease {
		t.Errorf("upgrading the witness: %d takeover(s), longest %v; want one, from the witness's start time %v to 4 leases more", n, longest, upgradeDown)
	}
}

// TestMeasureRollingUpgrade measures every order across a vote protocol
// version, and one release's for comparison. Skipped unless
// QUORUM_MEASURE_UPGRADE=<runs per order>; UPGRADE_LEASE and UPGRADE_DOWN
// set the lease (default 1 s) and the modeled start time (default 1 s).
func TestMeasureRollingUpgrade(t *testing.T) {
	n, _ := strconv.Atoi(os.Getenv("QUORUM_MEASURE_UPGRADE"))
	if n <= 0 {
		t.Skip("set QUORUM_MEASURE_UPGRADE=<runs per order>")
	}
	lease, down := time.Second, time.Second
	if d, err := time.ParseDuration(os.Getenv("UPGRADE_LEASE")); err == nil {
		lease = d
	}
	if d, err := time.ParseDuration(os.Getenv("UPGRADE_DOWN")); err == nil {
		down = d
	}
	for _, c := range []struct {
		rel   int
		order []string
	}{
		{0, []string{"standby", "witness", "leader"}},
		{1, []string{"standby", "witness", "leader"}}, // the guide's order
		{1, []string{"witness", "standby", "leader"}},
		{1, []string{"standby", "leader", "witness"}},
		{1, []string{"leader", "standby", "witness"}},
		{1, []string{"leader", "witness", "standby"}},
		{1, []string{"witness", "leader", "standby"}},
	} {
		for i := range n {
			t.Run(fmt.Sprintf("release%d/%s/%d", c.rel, strings.Join(c.order, ","), i), func(t *testing.T) {
				roll(t, lease, c.rel, down, c.order...)
			})
		}
	}
}
