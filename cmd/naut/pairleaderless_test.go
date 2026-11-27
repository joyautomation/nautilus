package main

import (
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/joyautomation/nautilus/alarm"
)

// leaderlessFiles is the rig's project with an escalation policy that
// pages system alarms through the log channel, so each send leaves a
// notify row in the journal.
func leaderlessFiles(a, b, w string) fstest.MapFS {
	files := pairProject(a, b, w)
	y := string(files["nautilus.yaml"].Data)
	y = strings.Replace(y, "alarms:\n  defs:\n    - { id: Trip, tag: Trip }\n", `alarms:
  defs:
    - { id: Trip, tag: Trip }
  oncall:
    rosters:
      - { id: ops, members: [alice] }
  escalation:
    - id: system
      match: { class: system }
      levels:
        - { after: 0s, notify: [roster:ops], via: [log] }
`, 1)
	files["nautilus.yaml"] = &fstest.MapFile{Data: []byte(y)}
	return files
}

// pages counts the notify rows member n's journals hold for id: its
// no-leader alarm engine's (pair) or its main engine's.
func (r *pairRig) pages(t *testing.T, n, id string, pair bool) int {
	t.Helper()
	var eng *alarm.Engine
	if pair {
		pa := r.nodes[n].m.pairAlarms.Load()
		if pa == nil {
			t.Fatalf("%s has no no-leader alarm engine", n)
		}
		eng = pa.Engine
	} else {
		eng = r.nodes[n].eng
	}
	evs, err := eng.Journal(time.Time{}, time.Time{}, alarm.Filter{Kinds: []string{alarm.KindNotify}})
	if err != nil {
		t.Fatal(err)
	}
	c := 0
	for _, e := range evs {
		if id == "" || e.ID == id {
			c++
		}
	}
	return c
}

// raised reports whether member n's no-leader alarm is up.
func (r *pairRig) raised(n string) bool {
	pa := r.nodes[n].m.pairAlarms.Load()
	if pa == nil {
		return false
	}
	rec, ok := pa.Engine.Lookup(PairLeaderlessAlarmID)
	return ok && rec.Cond
}

// The no-leader alarm through naut run's wiring (#1436), on one pair:
//
//  1. A leader whose hello says nothing of the alarm (as a build before it
//     says) leads: the standby sees the peer lead, raises nothing and
//     pages nobody.
//  2. Both members unhealthy (scans not completing): up and talking, no
//     leader. a, first, raises and pages; b
//     holds a's word and pages nobody. The scan's alarm engines page
//     nothing (they evaluate nothing).
//  3. The link between them cut: b loses a's word and pages too (a split
//     pages twice, never zero).
//  4. The link back, both healthy: a leader, the alarms end, and the
//     pages stop.
//
// pair_alarm_none (nobody pages) and leaderless_alarm_second_never_defers
// (b pages in 2) turn it red.
func TestPairLeaderlessAlarmPagesOnce(t *testing.T) {
	testPairFiles = leaderlessFiles
	t.Cleanup(func() { testPairFiles = nil })
	l := newLinks()
	r := newPairRigFull(t, nil, func(n string, d *pairDeps) { d.dial = l.dialer(n) })
	lease := r.nodes["a"].m.o.Lease
	quiet := 4*lease + 4*max(lease, time.Second) + 2*time.Second // the report (4 leases), the second's grace, margin

	// (1) An old build leads.
	lead := r.leader()
	stand := other(lead)
	own := r.nodes[lead].m.pairAlarms.Swap(nil) // its hello says nothing, as a build before it
	time.Sleep(quiet)
	if ll := r.nodes[stand].m.q.Node().Status().Leaderless; ll != nil {
		t.Fatalf("%s reports no leader while %s (a build without the alarm) leads: %+v", stand, lead, ll)
	}
	if r.raised(stand) || r.pages(t, stand, PairLeaderlessAlarmID, true) != 0 {
		t.Fatalf("%s raised or paged the no-leader alarm while %s leads", stand, lead)
	}
	r.nodes[lead].m.pairAlarms.Store(own) // this build again

	// (2) Both unhealthy: the leader stops renewing and neither
	// campaigns. Up, talking, and no leader.
	r.leader()
	var sick atomic.Bool
	sick.Store(true)
	for _, n := range []string{"a", "b"} {
		r.nodes[n].m.q.Node().SetHealth(func() bool { return !sick.Load() })
	}
	end := time.Now().Add(30 * time.Second)
	for r.pages(t, "a", PairLeaderlessAlarmID, true) == 0 {
		if time.Now().After(end) {
			for _, n := range []string{"a", "b"} {
				st := r.nodes[n].m.q.Node().Status()
				ra, ep := r.nodes[n].m.q.Node().LeaderlessAlarm()
				t.Logf("%s: leading %v leader %q ll %+v raise %v ep %d raised %v", n, st.Leading, st.Leader, st.Leaderless, ra, ep, r.raised(n))
			}
			t.Fatal("a paged nothing")
		}
		time.Sleep(200 * time.Millisecond)
	}
	time.Sleep(quiet)
	for _, n := range []string{"a", "b"} {
		if ll := r.nodes[n].m.q.Node().Status().Leaderless; ll == nil {
			t.Fatalf("%s reports a leader with both unhealthy", n)
		}
		if p := r.pages(t, n, "", false); p != 0 {
			t.Errorf("%s's scan alarm engine sent %d pages with no leader", n, p)
		}
	}
	if p := r.pages(t, "b", PairLeaderlessAlarmID, true); p != 0 || r.raised("b") {
		t.Fatalf("b paged the no-leader alarm %d times beside a's (up and talking): paged twice", p)
	}

	// (3) The link cut: b no longer hears a.
	l.cutLink("a", r.peer["b"])
	l.cutLink("b", r.peer["a"])
	pairWait(t, "b to page the no-leader alarm, cut off from a", 30*time.Second, func() bool {
		return r.pages(t, "b", PairLeaderlessAlarmID, true) > 0
	})

	// (4) Healed: the link back, both healthy: a leader, and the alarm
	// ends.
	l.mu.Lock()
	clear(l.cut)
	l.mu.Unlock()
	sick.Store(false)
	r.leader()
	pairWait(t, "both no-leader alarms to end", 30*time.Second, func() bool { return !r.raised("a") && !r.raised("b") })
	sent := map[string]int{"a": r.pages(t, "a", PairLeaderlessAlarmID, true), "b": r.pages(t, "b", PairLeaderlessAlarmID, true)}
	time.Sleep(3 * time.Second)
	for n, c := range sent {
		if got := r.pages(t, n, PairLeaderlessAlarmID, true); got != c {
			t.Errorf("%s paged %d more times with a leader", n, got-c)
		}
	}
	t.Logf("pages: %v", sent)
}
