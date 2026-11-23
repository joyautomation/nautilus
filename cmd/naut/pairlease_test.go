package main

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/internal/project"
	"github.com/joyautomation/nautilus/quorum/node"
	"github.com/joyautomation/nautilus/server"
)

// The default lease is 1 s for the pair and the witness alike (#710): a
// pair with no lease: and a witness with no --lease agree.
func TestDefaultLeaseIsOneSecondForPairAndWitness(t *testing.T) {
	if node.DefaultLease != time.Second || project.DefaultPairLease != time.Second {
		t.Fatalf("defaults: witness/node %v, pair %v; want 1s both", node.DefaultLease, project.DefaultPairLease)
	}
	if r := (&project.RedundancyConfig{Peers: map[string]string{"a": "x:1", "b": "y:1"}}); r.LeaseTime() != node.DefaultLease {
		t.Fatalf("a pair without lease: runs %v, its witness's default is %v", r.LeaseTime(), node.DefaultLease)
	}
	if !strings.Contains(witnessUsage, "(default "+node.DefaultLease.String()+")") {
		t.Fatalf("naut witness --help does not give the default lease %v:\n%s", node.DefaultLease, witnessUsage)
	}
}

// naut check warns of an explicit pair lease under 1 s, the shortest
// measured on ARM controllers, and of nothing at or above it, unset, or in
// the Kubernetes mode (whose lease: is a name).
func TestCheckWarnsOfALeaseUnderOneSecond(t *testing.T) {
	for _, c := range []struct {
		lease string
		peers bool
		warn  bool
	}{
		{"999ms", true, true},
		{"500ms", true, true},
		{"1s", true, false},
		{"2s", true, false},
		{"", true, false},
		{"", false, false},
		{"plant-lease", false, false},
	} {
		r := &project.RedundancyConfig{Lease: c.lease}
		if c.peers {
			r.Peers = map[string]string{"a": "127.0.0.1:9450", "b": "127.0.0.2:9450"}
		}
		w := leaseWarnings(&project.Project{Redundancy: r})
		got := len(w) > 0
		if got != c.warn {
			t.Errorf("lease %q (pair %v): warnings %q, want a warning: %v", c.lease, c.peers, w, c.warn)
		}
		if got && c.warn && (!strings.Contains(w[0], "under 1s") || !strings.Contains(w[0], "--lease")) {
			t.Errorf("lease %q: the warning names neither the 1 s floor nor the witness's --lease: %q", c.lease, w[0])
		}
	}
	// The stall figure is derived from the code's own timings: at 1 s,
	// 990 ms of lease, a 70 ms fence, a renewal every 250 ms.
	if got := stallTolerance(time.Second, 70*time.Millisecond); got != 670*time.Millisecond {
		t.Errorf("stall tolerance at 1 s: %v, want 670ms", got)
	}
}

// witnessLeaseAlarmOf is the pair member's witness-lease system alarm, as
// naut run builds it.
func witnessLeaseAlarmOf(t *testing.T, m *pairMember) project.SystemAlarm {
	t.Helper()
	var srv atomic.Pointer[server.Server]
	proj := loadPair(t, pairProject("127.0.0.1:9450", "127.0.0.2:9450", "127.0.0.3:9460"))
	for _, a := range systemAlarms(proj, m, &srv) {
		if a.Def.ID == WitnessLeaseAlarmID {
			return a
		}
	}
	t.Fatal("a pair member's system alarms have no witness-lease alarm")
	return project.SystemAlarm{}
}

// A voter's refusal of the pair's lease is loud where an operator looks
// (#710). (1) The witness given a shorter --lease (the default fell to
// 1 s; a pair with its own lease upgraded its witness without --lease):
// both members raise a condition naming the fix, the leader's ServiceLevel
// drops to 200, and the system alarm is up. Restarted with the pair's
// lease, all of it clears. (2) A standby on a shorter lease than the
// leader's (mid-upgrade, standby first): the leader names the refusal,
// the standby names its own, and both clear once the standby runs the
// leader's lease again.
func TestPairLeaseRefusalsAreRaisedAndCleared(t *testing.T) {
	var mu sync.Mutex
	lease := map[string]string{}
	r := newPairRigWith(t, func(n string, p *project.Project) {
		mu.Lock()
		defer mu.Unlock()
		if l, ok := lease[n]; ok {
			p.Redundancy.Lease = l
		}
	})
	lead := r.leader()
	alarms := map[string]project.SystemAlarm{}
	for _, n := range []string{"a", "b"} {
		if ws := r.nodes[n].m.q.Status().Warnings; warned(ws, "refuses") {
			t.Fatalf("%s warns of a refusal with every lease equal: %q", n, ws)
		}
		alarms[n] = witnessLeaseAlarmOf(t, r.nodes[n].m)
		if alarms[n].Active() {
			t.Fatalf("%s's witness-lease alarm is up with every lease equal", n)
		}
	}

	// (1) The witness refuses.
	addr := r.wit
	r.stopWitness()
	r.witArgs = []string{"--lease", "250ms"}
	r.startWitness(addr)
	for _, n := range []string{"a", "b"} {
		pairWait(t, n+" to raise the witness's refusal", 10*time.Second, func() bool {
			return warned(r.nodes[n].m.q.Status().Warnings, "no fault tolerance: the witness refuses this controller's votes")
		})
		ws := r.nodes[n].m.q.Status().Warnings
		if !warned(ws, "Start the witness with --lease 500ms") {
			t.Errorf("%s's condition does not name the fix (--lease 500ms): %q", n, ws)
		}
		if !alarms[n].Active() {
			t.Errorf("%s's witness-lease alarm is not up", n)
		}
	}
	if r.leader() != lead {
		t.Errorf("the lead moved from %s while the witness refused the lease", lead)
	}
	if lvl := pairServiceLevel(r.nodes[lead].m.q.Status()); lvl != 200 {
		t.Errorf("the leader's ServiceLevel with no fault tolerance: %d, want 200", lvl)
	}
	r.stopWitness()
	r.witArgs = nil
	r.startWitness(addr)
	for _, n := range []string{"a", "b"} {
		pairWait(t, n+" to clear the witness's refusal", 10*time.Second, func() bool {
			return !warned(r.nodes[n].m.q.Status().Warnings, "refuses") && !alarms[n].Active()
		})
	}

	// (2) The standby runs a shorter lease than the leader's.
	lead = r.leader()
	stby := map[string]string{"a": "b", "b": "a"}[lead]
	mu.Lock()
	lease[stby] = "250ms"
	mu.Unlock()
	r.nodes[stby].stop()
	r.restart(stby)
	pairWait(t, "the leader to name the standby's refusal", 10*time.Second, func() bool {
		return warned(r.nodes[lead].m.q.Status().Warnings, "controller "+stby+" refuses this controller's votes")
	})
	// The refuser shows it too: mid-upgrade, the refused side may run a
	// release that shows nothing (Fable review).
	pairWait(t, "the standby to name its refusal of the leader", 10*time.Second, func() bool {
		return warned(r.nodes[stby].m.q.Status().Warnings, "this controller refuses controller "+lead+"'s votes")
	})
	if ws := r.nodes[stby].m.q.Status().Warnings; warned(ws, "refuses this controller's votes") {
		t.Errorf("the standby, whose lease every voter takes, says it is refused: %q", ws)
	}
	if alarms[lead].Active() {
		t.Errorf("the witness-lease alarm is up for a refusal by the peer")
	}
	mu.Lock()
	delete(lease, stby)
	mu.Unlock()
	r.nodes[stby].stop()
	r.restart(stby)
	pairWait(t, "both to clear the refusal", 10*time.Second, func() bool {
		return !warned(r.nodes[lead].m.q.Status().Warnings, "refuses") && !warned(r.nodes[stby].m.q.Status().Warnings, "refuses")
	})
}
