package node

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/quorum"
)

// #1029 3c-3c-2b (D1): a wiped old box back beside the box that replaced
// it. Its adoption probes carry their sequence and reach the peer as well
// as the witness, so both voters mark the name; the peer takes no
// re-adoption from it while the replacement is live; and recommission on
// it refuses.

// marked reports whether id's voter (a controller's, or the witness's)
// marks name.
func (c *cluster) marked(id, name quorum.NodeID) bool {
	var m []quorum.NodeID
	if id == WitnessID {
		c.witnessVoter(func(v *quorum.Voter, _ quorum.Instant) { m = v.Marked() })
	} else {
		e := c.elector(id)
		e.mu.Lock()
		m = e.v.Marked()
		e.mu.Unlock()
	}
	return slices.Contains(m, name)
}

// startOldBox starts a second process under id's name with no state file
// (an old box wiped while off, switched back on beside its replacement),
// with no listener and links of its own ("old").
func (c *cluster) startOldBox(id quorum.NodeID) *Elector {
	c.t.Helper()
	return c.startOldBoxOn(id, &faultRT{c: c, from: "old", base: c.transport()})
}

// startOldBoxOn is startOldBox on transport rt.
func (c *cluster) startOldBoxOn(id quorum.NodeID, rt http.RoundTripper) *Elector {
	c.t.Helper()
	peer := c.other(id)
	e, err := New(Options{Cluster: c.pair, Self: id, Peer: peer, PeerAddr: c.addr[peer], WitnessAddr: c.addr[WitnessID],
		Lease: loopLease, StatePath: c.ctl[id].state + ".old", Token: c.tok[id], ClientToken: c.ctok[id],
		client: &http.Client{Transport: rt}})
	if err != nil {
		c.t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	ran := make(chan struct{})
	go func() { e.Run(ctx); close(ran) }()
	c.t.Cleanup(func() { cancel(); <-ran; e.Close() })
	return e
}

// A controller that lost its file (no copy) and restarted: its adoption
// probes reach the peer as well as the witness, each with its own
// sequence number (adopt_probe_unseq; adopt_probe_witness_only only
// sometimes, the next test catches it), it
// re-adopts once its old boot has aged out of the peer's live boots, and
// no voter marks its name for the restart.
func TestALostStateRestartsProbeReachesThePeerUnmarked(t *testing.T) {
	c := newCluster(t)
	c.startAll()
	c.commission()
	c.settledLeader(leases(20))
	c.kill("a")
	c.wipe("a")
	a := c.start("a")
	born := c.ctl["a"].born
	c.waitFor("a to re-adopt", leases(20), func() bool { return c.status("a").Commissioned })
	var seqs []uint64
	c.mu.Lock()
	for _, v := range c.votes {
		if v.from == "a" && v.to == "b" && v.req.Uncommissioned && v.req.Nonce == a.Nonce() && v.at.After(born) {
			seqs = append(seqs, v.req.Seq)
		}
	}
	c.mu.Unlock()
	if len(seqs) == 0 {
		t.Fatal("no adoption probe of the restarted a reached b")
	}
	if slices.Contains(seqs, 0) {
		t.Fatalf("an adoption probe to b carried no sequence: %v", seqs)
	}
	c.settledLeader(leases(20))
	for _, id := range []quorum.NodeID{"b", WitnessID} {
		if c.marked(id, "a") {
			t.Fatalf("%s marks a for a lost-state restart", id)
		}
	}
}

// The wiped old box switched back on beside a running a: the witness and
// b both hear two boots of a, and mark it (adopt_probe_witness_only: b
// never hears it; adopt_probe_unseq does not turn this one red, the box's
// other frames carry a sequence here, but the lost-state test above), and
// the box never re-adopts from b while the replacement is live: both
// other members hold the replacement first, so the box makes itself a
// spare (#1029 3c-2).
func TestAWipedOldBoxBesideItsReplacementIsMarkedOnTheWire(t *testing.T) {
	c := newCluster(t)
	c.startAll()
	c.commission()
	c.settledLeader(leases(20))
	old := c.startOldBox("a")
	c.waitFor("b and the witness to mark a", leases(8), func() bool {
		return c.marked("b", "a") && c.marked(WitnessID, "a")
	})
	time.Sleep(leases(4))
	if old.Status().Commissioned {
		t.Fatal("the wiped old box re-adopted from b beside the live replacement")
	}
}

// Recommission on the wiped old box refuses while a voter hears the
// replacement (recommission_beside_live). The box is cut from b here, so
// only the witness hears both, and the box never learns it is the copy
// (no spare): an operator could run recommission on it.
func TestRecommissionRefusesBesideALiveBootOfItsName(t *testing.T) {
	c := newCluster(t)
	c.startAll()
	c.commission()
	c.settledLeader(leases(20))
	c.mu.Lock()
	c.cut[[2]quorum.NodeID{"old", "b"}], c.cut[[2]quorum.NodeID{"b", "old"}] = true, true
	c.mu.Unlock()
	old := c.startOldBox("a")
	c.waitFor("the old box to hear the witness", leases(20), func() bool { return old.Status().WitnessIncarnation != 0 })
	time.Sleep(leases(2)) // past a lease and D: the old box's start is settled everywhere
	probe, _ := old.RecommissionChecked(opCtx(t), "op", RecommissionRequest{})
	rep, err := old.RecommissionChecked(opCtx(t), "op", RecommissionRequest{Floor: probe.Suggested})
	if !errors.Is(err, ErrCommandRefused) || !strings.Contains(rep.Refused, "witness (boot") || !strings.Contains(rep.Refused, "another process named a") {
		t.Fatalf("recommission beside the replacement: %v %s", err, rep.Refused)
	}
	if old.Status().Commissioned || old.Spare() {
		t.Fatalf("a refused recommission changed the old box: %+v", old.Status())
	}
	// Its own boot is not another: with the replacement stopped and out
	// of the live window, the same command goes ahead.
	c.kill("a")
	time.Sleep(leases(2))
	done, err := old.RecommissionChecked(opCtx(t), "op", RecommissionRequest{Floor: probe.Suggested})
	if err != nil {
		t.Fatalf("recommission with the replacement stopped: %v %s", err, done.Refused)
	}
	// b is cut from the old box: what it may hear was not checked, and
	// the report says so (naut-lead's ruling).
	if len(done.LiveUnchecked) != 1 || !strings.Contains(done.LiveUnchecked[0], "b did not answer") {
		t.Fatalf("the report doesn't name the unchecked peer: %q", done.LiveUnchecked)
	}
}

// A voter that started under a lease and D ago can't say which boots of
// the name run, so recommission refuses on its word (fail closed) rather
// than reading its empty answer as "none": here the witness restarts
// while the replacement runs, and the old box asks at once.
func TestRecommissionRefusesWhileAVoterCantYetSay(t *testing.T) {
	c := newCluster(t)
	c.startAll()
	c.commission()
	c.settledLeader(leases(20))
	c.mu.Lock()
	c.cut[[2]quorum.NodeID{"old", "b"}], c.cut[[2]quorum.NodeID{"b", "old"}] = true, true
	c.mu.Unlock()
	old := c.startOldBox("a")
	c.waitFor("the old box to hear the witness", leases(20), func() bool { return old.Status().WitnessIncarnation != 0 })
	time.Sleep(leases(2))
	probe, _ := old.RecommissionChecked(opCtx(t), "op", RecommissionRequest{})
	c.stopWitness()
	c.startWitness()
	end := time.Now().Add(leases(8))
	for time.Now().Before(end) {
		rep, err := old.RecommissionChecked(opCtx(t), "op", RecommissionRequest{Floor: probe.Suggested})
		if err == nil {
			t.Fatalf("recommission went ahead beside the replacement while the witness could not yet say: %+v", rep.Live)
		}
		if strings.Contains(rep.Refused, "started less than") {
			return
		}
		time.Sleep(loopLease / 20)
	}
	t.Fatal("never refused on the restarted witness's word")
}

// A witness that answered the members' state but misses the check for
// another process of the name refuses the command: the witness must
// answer, and its silence there is no "none" (live_check_witness_skipped,
// the #1207 review). The /dup requests are, in order: the peer's state
// (cut), the witness's, the peer's check (cut), the witness's check
// (dropped).
func TestRecommissionRefusesWhenTheWitnessMissesTheLiveCheck(t *testing.T) {
	c := newCluster(t)
	c.startAll()
	c.commission()
	c.settledLeader(leases(20))
	c.mu.Lock()
	c.cut[[2]quorum.NodeID{"old", "b"}], c.cut[[2]quorum.NodeID{"b", "old"}] = true, true
	c.mu.Unlock()
	rt := &failNth{base: &faultRT{c: c, from: "old", base: c.transport()}, n: 4}
	old := c.startOldBoxOn("a", rt)
	c.waitFor("the old box to hear the witness", leases(20), func() bool { return old.Status().WitnessIncarnation != 0 })
	c.kill("a") // no replacement heard: only the witness's silence stands in the way
	time.Sleep(leases(2))
	rep, err := old.RecommissionChecked(opCtx(t), "op", RecommissionRequest{Floor: 1 << 20})
	if !errors.Is(err, ErrCommandRefused) || !strings.Contains(rep.Refused, "the witness must answer the check") {
		t.Fatalf("recommission with the witness's check dropped: %v %s", err, rep.Refused)
	}
	if old.Status().Commissioned {
		t.Fatal("a refused recommission commissioned the old box")
	}
}
