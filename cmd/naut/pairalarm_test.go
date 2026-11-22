package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/alarm"
	"github.com/joyautomation/nautilus/internal/project"
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

// tripOnAtStart starts Trip true on every member, at every start: the
// field asserts the condition on whichever controller takes over, as a
// field input both read would. Since #715 an acknowledgement carries
// across a takeover only with its occurrence, and an occurrence carries
// only if the field re-asserts the condition at the takeover's first
// evaluation (the takeover waited for live inputs); a setpoint written
// after the takeover is a new occurrence.
func tripOnAtStart(_ string, p *project.Project) {
	for i := range p.Runtime.Tags {
		if p.Runtime.Tags[i].Name == "Trip" {
			p.Runtime.Tags[i].Init = true
		}
	}
}

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
	// A certain refusal (nothing proposed) is retried for a few seconds,
	// as startedID does: a new leader's view of the copies lags a ping.
	var code int
	var b string
	for end := time.Now().Add(10 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		var h http.Header
		code, b, h = r.call(n, "POST", "/api/alarms/ack", `{"ids":["Trip"],"by":"op"}`)
		if code == http.StatusOK || h.Get("X-Nautilus-Outcome") != "refused" && !strings.Contains(b, "no leader") || time.Now().After(end) {
			break
		}
	}
	if code != http.StatusOK {
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
	r := newPairRigWith(t, tripOnAtStart)
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
	occ := r.occurrence(lead)

	r.handover(t, lead)
	r.setTrip(t, stand, true)
	pairWait(t, "Trip active on the new leader", 5*time.Second, func() bool { return r.alarmState(stand) != alarm.Normal })
	if s := r.alarmState(stand); s != alarm.AckActive {
		t.Fatalf("Trip on the new leader is %s; want ack-active (the ack carried)", s)
	}
	if o := r.occurrence(stand); o != occ {
		t.Fatalf("Trip on the new leader is on occurrence %s; want %s, the one acknowledged", o, occ)
	}
}

// occurrence is member n's current occurrence of Trip.
func (r *pairRig) occurrence(n string) string {
	rec, _ := r.nodes[n].eng.Lookup("Trip")
	return rec.Occurrence
}

// An acknowledgement the leader and the witness committed while the
// standby was down carries across the takeover after the leader died
// (#710 3b-4b): the standby's catch-up finds it on the witness, so Trip
// comes up acknowledged on the new leader.
func TestPairAckCarriesAcrossATakeoverAfterTheLeaderDied(t *testing.T) {
	r := newPairRigWith(t, tripOnAtStart)
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
	r := newPairRigWith(t, tripOnAtStart)
	lead := r.leader()
	stand := other(lead)
	r.setTrip(t, lead, true)
	pairWait(t, "Trip to annunciate", 5*time.Second, func() bool { return r.alarmState(lead) == alarm.UnackActive })
	r.ackTrip(t, lead)
	pairWait(t, "the standby to hold the ack", 5*time.Second, func() bool { return r.heldAck(stand) })
	old := r.occurrence(lead)
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
	// The committed ack names the occurrence it was for (#715). The
	// unsynced takeover could not continue it, but Trip has stood since:
	// once the log proves the ack, the alarm takes that occurrence and is
	// acknowledged (TestPairFieldDropsAtTheTakeover is the field that did
	// not stand).
	pairWait(t, "the committed ack to apply once synced", 5*time.Second, func() bool { return r.alarmState(stand) == alarm.AckActive })
	if o := r.occurrence(stand); o != old {
		t.Fatalf("Trip after the sync is on occurrence %s; want %s, the one acknowledged", o, old)
	}
	r.startedID(stand)
	pairWait(t, "the log conditions to clear after the sync", 5*time.Second, func() bool {
		for _, w := range r.nodes[stand].m.q.Status().Warnings {
			if strings.Contains(w, "operator-intent log") {
				return false
			}
		}
		return true
	})
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

// The field drops at the takeover (#715): Trip was acknowledged on the old
// leader, but reads false at the new leader's first evaluation and asserts
// only later. That is a new occurrence: it annunciates unacknowledged, on
// a new ID, and the new leader's journal says the carried ack was for an
// occurrence that ended. (The tests above hold the field: the ack carries.)
func TestPairFieldDropsAtTheTakeover(t *testing.T) {
	r := newPairRig(t) // Trip starts false on both
	lead := r.leader()
	stand := other(lead)
	r.setTrip(t, lead, true)
	pairWait(t, "Trip to annunciate on the leader", 5*time.Second, func() bool { return r.alarmState(lead) == alarm.UnackActive })
	r.ackTrip(t, lead)
	pairWait(t, "the standby to hold the ack", 5*time.Second, func() bool { return r.heldAck(stand) })
	old := r.occurrence(lead)

	r.handover(t, lead)
	pairWait(t, "the new leader's journal to say the ack was not carried", 5*time.Second, func() bool {
		evs, _ := r.nodes[stand].eng.Journal(time.Time{}, time.Time{}, alarm.Filter{})
		for _, e := range evs {
			if strings.Contains(e.Name, "ack of Trip by op not carried") && e.Occurrence == old {
				return true
			}
		}
		return false
	})
	r.setTrip(t, stand, true)
	pairWait(t, "Trip active on the new leader", 5*time.Second, func() bool { return r.alarmState(stand) != alarm.Normal })
	if s, o := r.alarmState(stand), r.occurrence(stand); s != alarm.UnackActive || o == old || o == "" {
		t.Fatalf("Trip on the new leader is %s on %q; want unack-active on a new occurrence (not %s)", s, o, old)
	}
}

// The leader dies mid-flood with Trip chattering (#1249 slice 1b): the
// standby's view holds both episodes from the log, and the takeover
// continues them under their start and the flood's id, so the new leader
// ends what the old one began instead of starting afresh.
func TestPairEpisodesCarryAcrossTheLeadersDeath(t *testing.T) {
	r := newPairRig(t)
	lead := r.leader()
	stand := other(lead)
	for i := range 11 { // more than ten activations: a flood, and chatter
		r.setTrip(t, lead, true)
		pairWait(t, fmt.Sprintf("activation %d", i+1), 5*time.Second, func() bool { return r.alarmState(lead) == alarm.UnackActive })
		r.setTrip(t, lead, false)
		pairWait(t, fmt.Sprintf("return %d", i+1), 5*time.Second, func() bool { return r.alarmState(lead) == alarm.UnackRTN })
	}
	f := r.nodes[lead].eng.Flood()
	trip, _ := r.nodes[lead].eng.Lookup("Trip")
	if f == nil || trip.Chatter == nil {
		t.Fatalf("no episodes on the leader: flood %+v chatter %+v", f, trip.Chatter)
	}
	pairWait(t, "the standby's view to hold both episodes", 5*time.Second, func() bool {
		hf := r.nodes[stand].al.HeldFlood(true)
		return hf != nil && hf.ID == f.ID && r.nodes[stand].al.Held(true, nil)["Trip"].ChatterSinceMs == trip.Chatter.SinceMs
	})
	r.nodes[lead].crash()
	if r.leader() != stand {
		t.Fatal("the standby did not take over")
	}
	nf := r.nodes[stand].eng.Flood()
	nt, _ := r.nodes[stand].eng.Lookup("Trip")
	if nf == nil || nf.ID != f.ID || nf.SinceMs != f.SinceMs || nf.CarriedMs == 0 {
		t.Fatalf("flood on the new leader %+v; want %s carried", nf, f.ID)
	}
	if nt.Chatter == nil || nt.Chatter.SinceMs != trip.Chatter.SinceMs || nt.Chatter.CarriedMs == 0 {
		t.Fatalf("chatter on the new leader %+v; want the episode since %d carried", nt.Chatter, trip.Chatter.SinceMs)
	}
	if rows, _ := r.nodes[stand].eng.Journal(time.Time{}, time.Time{}, alarm.Filter{Kinds: []string{alarm.KindFlood, alarm.KindChatter}}); len(rows) != 0 {
		t.Fatalf("the new leader started the carried episodes again: %+v", rows)
	}
}
