package main

import (
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/alarm"
)

// #1033 review regression tests, end to end (the reviewer's probes 1-3).

// healthy installs a health flag on member n's election: false, it stops
// renewing and its gate closes, so the other member takes over without
// this one exiting (Release is for good; this hands leadership back).
func (r *pairRig) healthy(n string) *atomic.Bool {
	var ok atomic.Bool
	ok.Store(true)
	r.nodes[n].m.q.Node().SetHealth(ok.Load)
	return &ok
}

// waitLeads waits until member n leads and its runtime has taken over.
func (r *pairRig) waitLeads(t *testing.T, n string) {
	t.Helper()
	pairWait(t, n+" to lead", 15*time.Second, func() bool { return r.nodes[n].m.q.IsLeader() && r.nodes[n].rt.Leading() })
}

// Probe 1: an ack of a Trip that is normal is refused and leaves no ack in
// the standby's view; Trip's next occurrence, after a synced handover,
// annunciates.
func TestReviewAckOfANormalAlarmEndToEnd(t *testing.T) {
	r := newPairRig(t)
	lead := r.leader()
	stand := other(lead)
	if code, b, _ := r.call(lead, "POST", "/api/alarms/ack", `{"ids":["Trip"],"by":"op"}`); code != http.StatusConflict {
		t.Fatalf("ack of a normal alarm: %d %s; want 409", code, b)
	}
	if r.heldAck(stand) {
		t.Fatal("the standby's view holds an ack nobody could make")
	}
	r.setTrip(t, lead, true)
	pairWait(t, "unack on the leader", 5*time.Second, func() bool { return r.alarmState(lead) == alarm.UnackActive })
	r.handover(t, lead)
	r.setTrip(t, stand, true)
	pairWait(t, "Trip active on the new leader", 5*time.Second, func() bool { return r.alarmState(stand) != alarm.Normal })
	if s := r.alarmState(stand); s != alarm.UnackActive {
		t.Fatalf("a never-acked occurrence is %s on the new leader", s)
	}
}

// staleEngineCase: A leads and acks occurrence 1 of Trip; A loses its
// lease without exiting (its engine frozen at ack-active); on B occurrence
// 1 ends (ack-clear) and occurrence 2 starts, unacknowledged. Then A leads
// again, by handback (synced) or because B died (unsynced): occurrence 2
// must be unacknowledged on A (engine_not_reset turns it red).
func staleEngineCase(t *testing.T, bDies bool) {
	r := newPairRigWith(t, tripOnAtStart)
	a := r.leader()
	b := other(a)
	ha, hb := r.healthy(a), r.healthy(b)
	r.setTrip(t, a, true)
	pairWait(t, "unack on a", 5*time.Second, func() bool { return r.alarmState(a) == alarm.UnackActive })
	r.ackTrip(t, a)
	pairWait(t, "ack on a", 5*time.Second, func() bool { return r.alarmState(a) == alarm.AckActive })
	pairWait(t, "b holds the ack", 5*time.Second, func() bool { return r.heldAck(b) })

	ha.Store(false) // a stops renewing: b takes over, a's engine frozen
	r.waitLeads(t, b)
	r.setTrip(t, b, true)
	pairWait(t, "Trip active on b", 5*time.Second, func() bool { return r.alarmState(b) != alarm.Normal })
	r.setTrip(t, b, false)
	pairWait(t, "normal on b", 5*time.Second, func() bool { return r.alarmState(b) == alarm.Normal })
	pairWait(t, "a's view drops the ack", 5*time.Second, func() bool { return !r.heldAck(a) })
	r.setTrip(t, b, true)
	pairWait(t, "occurrence 2 unack on b", 5*time.Second, func() bool { return r.alarmState(b) == alarm.UnackActive })
	if s := r.alarmState(a); s != alarm.AckActive {
		t.Logf("a's frozen engine: %s", s)
	}

	ha.Store(true)
	if bDies {
		r.nodes[b].crash()
	} else {
		hb.Store(false) // handback, synced: b still answers the log
	}
	r.waitLeads(t, a)
	r.setTrip(t, a, true)
	pairWait(t, "Trip active on a", 5*time.Second, func() bool { return r.alarmState(a) != alarm.Normal })
	if s := r.alarmState(a); s != alarm.UnackActive {
		t.Fatalf("occurrence 2, never acknowledged, is %s on a (b died: %v)", s, bDies)
	}
}

func TestReviewStaleEngineSyncedHandback(t *testing.T) { staleEngineCase(t, false) }
func TestReviewStaleEngineUnsynced(t *testing.T)       { staleEngineCase(t, true) }
