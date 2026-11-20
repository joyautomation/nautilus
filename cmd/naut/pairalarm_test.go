package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/alarm"
	"github.com/joyautomation/nautilus/server"
)

// Alarm acknowledgements through the pair's operator-intent log (#710 PR
// 3b-2c), end to end on loopback. The rig's manifest has one alarm, Trip,
// on a BOOL setpoint. Tag writes are not replicated (no state stream
// until #710 step 4), so after a takeover the test sets the condition
// through the new leader's API.

// alarmState is Trip's state on member n's engine.
func (r *pairRig) alarmState(n string) alarm.State {
	for _, rec := range r.nodes[n].eng.Records() {
		if rec.ID == "Trip" {
			return rec.State
		}
	}
	return alarm.Normal
}

// heldAck reports whether member n's replicated alarm view holds an ack for
// Trip (what a takeover there would carry).
func (r *pairRig) heldAck(n string) bool { return r.nodes[n].al.Held(true, nil)["Trip"].Acked }

func (r *pairRig) setTrip(t *testing.T, n string, v bool) {
	t.Helper()
	body := `{"name":"Trip","value":false}`
	if v {
		body = `{"name":"Trip","value":true}`
	}
	if code, b, _ := r.call(n, "POST", "/api/tags", body); code/100 != 2 {
		t.Fatalf("set Trip=%v on %s: %d %s", v, n, code, b)
	}
}

func (r *pairRig) ackTrip(t *testing.T, n string) {
	t.Helper()
	if code, b, _ := r.call(n, "POST", "/api/alarms/ack", `{"ids":["Trip"],"by":"op"}`); code != http.StatusOK {
		t.Fatalf("ack on %s: %d %s", n, code, b)
	}
}

// handover is a clean failover: the leader releases, and the standby takes
// over SYNCED (its peer still answers the log's catch-up).
func (r *pairRig) handover(t *testing.T, from string) {
	t.Helper()
	r.nodes[from].m.q.Release()
	if r.leader() != other(from) {
		t.Fatal("the standby did not take over")
	}
}

// An acknowledged alarm whose occurrence is still on keeps its ack across
// a synced takeover: the ack goes through the log, the standby holds it,
// and the new leader's first activation of Trip comes up acknowledged
// instead of re-annunciating.
func TestPairAckCarriesAcrossASyncedTakeover(t *testing.T) {
	r := newPairRig(t)
	lead := r.leader()
	stand := other(lead)
	r.setTrip(t, lead, true)
	pairWait(t, "Trip to annunciate on the leader", 5*time.Second, func() bool { return r.alarmState(lead) == alarm.UnackActive })
	r.ackTrip(t, stand) // through the standby: proxied, then the log
	pairWait(t, "the ack on the leader's engine", 5*time.Second, func() bool { return r.alarmState(lead) == alarm.AckActive })
	pairWait(t, "the standby to hold the ack", 5*time.Second, func() bool { return r.heldAck(stand) })
	if s := r.alarmState(stand); s != alarm.Normal {
		t.Fatalf("the standby's engine moved (%s): a standby only records, it doesn't journal", s)
	}

	r.handover(t, lead)
	r.setTrip(t, stand, true)
	pairWait(t, "Trip active on the new leader", 5*time.Second, func() bool { return r.alarmState(stand) != alarm.Normal })
	if s := r.alarmState(stand); s != alarm.AckActive {
		t.Fatalf("Trip on the new leader is %s; want ack-active (the ack carried)", s)
	}
}

// An acknowledgement the leader and the witness committed while the
// standby was down carries across the takeover after the leader died
// (#710 3b-4b): the standby's catch-up finds it on the witness, so Trip
// comes up acknowledged on the new leader.
func TestPairAckCarriesAcrossATakeoverAfterTheLeaderDied(t *testing.T) {
	r := newPairRig(t)
	lead := r.leader()
	stand := other(lead)
	r.setTrip(t, lead, true)
	pairWait(t, "Trip to annunciate", 5*time.Second, func() bool { return r.alarmState(lead) == alarm.UnackActive })
	r.nodes[stand].crash()
	r.ackTrip(t, lead) // on the leader and the witness only
	r.nodes[lead].crash()
	r.restart(stand)
	if r.leader() != stand {
		t.Fatal("the standby did not take over")
	}
	r.setTrip(t, stand, true)
	pairWait(t, "Trip active on the new leader", 5*time.Second, func() bool { return r.alarmState(stand) != alarm.Normal })
	if s := r.alarmState(stand); s != alarm.AckActive {
		t.Fatalf("Trip on the new leader is %s; want ack-active (the ack carried from the witness)", s)
	}
}

// A takeover whose log catch-up fails carries no acknowledgement at all
// (naut-lead's rule): the alarm re-annunciates (the safe direction). With
// the witness a replica, that takes a survivor whose own log is lost while
// the other controller is down: it leads (control comes first), drives
// its outputs, says its log was lost, and refuses every operator-intent
// write as not synced, for certain. Once the other controller is back,
// both other copies count, its log syncs, the committed ack applies, and
// writes go through.
func TestPairUnsyncedTakeoverCarriesNoAcks(t *testing.T) {
	r := newPairRig(t)
	lead := r.leader()
	stand := other(lead)
	r.setTrip(t, lead, true)
	pairWait(t, "Trip to annunciate", 5*time.Second, func() bool { return r.alarmState(lead) == alarm.UnackActive })
	r.ackTrip(t, lead)
	pairWait(t, "the standby to hold the ack", 5*time.Second, func() bool { return r.heldAck(stand) })
	r.nodes[stand].crash()
	if err := os.Remove(filepath.Join(r.env[stand]["STATE_DIRECTORY"], pairLogFile)); err != nil {
		t.Fatal(err)
	}
	r.nodes[lead].crash()
	r.restart(stand)
	if r.leader() != stand {
		t.Fatal("the survivor with a lost log did not take over")
	}
	pairWait(t, "the survivor to scan", 10*time.Second, func() bool { return r.nodes[stand].rt.Stats().Count > 0 })
	r.setTrip(t, stand, true)
	pairWait(t, "Trip active on the new leader", 5*time.Second, func() bool { return r.alarmState(stand) != alarm.Normal })
	if s := r.alarmState(stand); s != alarm.UnackActive {
		t.Fatalf("after an unsynced takeover Trip is %s; want unack-active (no ack carried)", s)
	}
	// No "acks not carried" journal note here: the lost log left no view
	// holding an ack to drop. The lost-log condition says it instead.
	for _, c := range []struct{ verb, path, body string }{
		{"substitute", "/api/substitutes", `{"name":"Level","seconds":60}`},
		{"alarm ack", "/api/alarms/ack", `{"ids":["Trip"],"by":"op"}`},
	} {
		code, body, h := r.call(stand, "POST", c.path, c.body)
		if code != http.StatusServiceUnavailable || h.Get("X-Nautilus-Outcome") != "refused" || !strings.Contains(body, server.LogNotSynced) {
			t.Errorf("%s on the unsynced leader: %d %q %s; want 503, refused, %q", c.verb, code, h.Get("X-Nautilus-Outcome"), body, server.LogNotSynced)
		}
	}
	pairWait(t, "the lost-log condition on /api/cluster", 5*time.Second, func() bool {
		for _, w := range r.nodes[stand].m.q.Status().Warnings {
			if strings.Contains(w, "operator-intent log was lost") {
				return true
			}
		}
		return false
	})

	r.restart(lead)
	pairWait(t, "the survivor's log to sync once both others count", 15*time.Second, func() bool { return r.nodes[stand].m.log.Synced() })
	r.logReady(stand)
	pairWait(t, "the committed ack to apply once synced", 5*time.Second, func() bool { return r.alarmState(stand) == alarm.AckActive })
	r.startedID(stand)
	for _, w := range r.nodes[stand].m.q.Status().Warnings {
		if strings.Contains(w, "operator-intent log") {
			t.Errorf("a log condition stayed after the sync: %s", w)
		}
	}
}

// An ack is of an occurrence: acked, back to normal, active again, then a
// synced takeover (an unsynced one carries no ack anyway). The leader's ack-clear (proposed when the acked alarm went
// back to normal) means the new leader's first activation is UNacked: the
// new occurrence annunciates (ack_clear_off turns this red). The test also
// measures the residual window: from the return to normal until the
// standby's view drops the ack.
func TestPairAckDoesNotCarryToANewOccurrence(t *testing.T) {
	r := newPairRig(t)
	lead := r.leader()
	stand := other(lead)
	r.setTrip(t, lead, true)
	pairWait(t, "Trip to annunciate", 5*time.Second, func() bool { return r.alarmState(lead) == alarm.UnackActive })
	r.ackTrip(t, lead)
	pairWait(t, "the standby to hold the ack", 5*time.Second, func() bool { return r.heldAck(stand) })

	r.setTrip(t, lead, false) // back to normal: the occurrence is over
	t0 := time.Now()
	pairWait(t, "Trip normal on the leader", 5*time.Second, func() bool { return r.alarmState(lead) == alarm.Normal })
	pairWait(t, "the standby's view to drop the ack", 5*time.Second, func() bool { return !r.heldAck(stand) })
	window := time.Since(t0)
	t.Logf("residual window (return to normal on the leader → ack cleared on the standby, incl. one scan): %v", window)
	if window > 2*time.Second {
		t.Errorf("the ack-clear took %v to reach the standby; the documented bound is one log round trip plus a scan", window)
	}

	r.setTrip(t, lead, true) // a new occurrence
	pairWait(t, "the new occurrence on the leader", 5*time.Second, func() bool { return r.alarmState(lead) == alarm.UnackActive })
	r.handover(t, lead) // synced: the view decides what carries
	r.setTrip(t, stand, true)
	pairWait(t, "Trip active on the new leader", 5*time.Second, func() bool { return r.alarmState(stand) != alarm.Normal })
	if s := r.alarmState(stand); s != alarm.UnackActive {
		t.Fatalf("the new occurrence is %s on the new leader; want unack-active (it must annunciate)", s)
	}
}
