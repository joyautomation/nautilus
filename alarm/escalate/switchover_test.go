package escalate

import (
	"strings"
	"testing"
	"time"
)

// Across a planned switchover a page may be sent twice, never lost (#710
// d1c, redundancy design §3.4 and §4.7): the notifiers are outside the
// outputs' drain, so a page already leaving when the old leader's gate
// shuts still goes, the old leader sends nothing more, and the new leader,
// taking over hot from the old one's state, resends the alarm's level.
func TestSwitchoverPageSentTwiceNeverLost(t *testing.T) {
	r := newRig(t, true)
	h := &holdChannel{rec: r.email, started: make(chan struct{}), release: make(chan struct{})}
	r.d.channels["email"] = h
	r.set("TT1", true)
	r.advance(time.Second)
	r.eng.Evaluate()
	r.d.Tick(r.now) // queues level 1's emails
	<-h.started     // the first is leaving, the second queued
	r.gate.Store(false)
	n := restartRig(t, r, 200*time.Millisecond, true) // the standby takes over hot within the lease
	n.run(2 * time.Second)
	close(h.release)
	r.d.Drain()
	got := map[string]int{}
	for _, s := range append(r.sent(), n.sent()...) {
		got[s]++
	}
	var level1 []string
	for s := range got {
		if strings.HasPrefix(s, "TT1:1:") {
			level1 = append(level1, s)
		}
	}
	if len(level1) == 0 {
		t.Fatalf("sent %v: level 1 was lost across the switchover", got)
	}
	twice := false
	for _, s := range level1 {
		if got[s] > 2 {
			t.Errorf("%s sent %d times, want at most twice", s, got[s])
		}
		twice = twice || got[s] == 2
	}
	if !twice {
		t.Errorf("sent %v: the page that was leaving at the switchover should go, and the new leader's resend too", got)
	}
	t.Logf("sent %v", got)
	if len(level1) != 2 {
		t.Errorf("sent %v: want level 1 to reach both people on call", got)
	}
}
