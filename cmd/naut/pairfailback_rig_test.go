package main

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/internal/project"
	"github.com/joyautomation/nautilus/server"
)

// Opt-in failback end to end on the pair rig (#710 d2, pairfailback.go).

// A switchover is refused to a standby that does not reach a field driver
// the leader has connected, naming it; a driver the standby cannot see
// ("waiting") is skipped. switch_ignores_io_reach turns it red.
func TestPairSwitchoverRefusedForAStandbyThatDoesNotReach(t *testing.T) {
	r := newSwitchRig(t)
	lead := r.leader()
	stand := other(lead)
	r.switchReady(t, lead)
	r.nodes[lead].m.setIOStatus(func() []server.DriverStatus {
		return []server.DriverStatus{
			{Kind: "modbus", Name: "pump1", State: "connected"},
			{Kind: "ethernet-ip", Name: "rack", State: "connected"},
		}
	})
	r.nodes[stand].m.setIOStatus(func() []server.DriverStatus {
		return []server.DriverStatus{{Kind: "ethernet-ip", Name: "rack", State: "waiting"}}
	})
	code, ans := r.switchover(t, lead)
	if code != http.StatusConflict || ans.Outcome != "refused" || !strings.Contains(ans.Error, "does not reach modbus pump1") || strings.Contains(ans.Error, "rack") {
		t.Fatalf("switchover: %d %q %q, want 409 refused naming modbus pump1 only", code, ans.Outcome, ans.Error)
	}
	if got := r.leader(); got != lead {
		t.Fatalf("a refused switchover moved the lead to %s", got)
	}
}

// newFailbackRig is the switchover rig with redundancy.failback preferring
// a, after 5 s (ten 500 ms leases).
func newFailbackRig(t *testing.T) *pairRig {
	t.Helper()
	testPairAPIToken = "api-secret"
	t.Cleanup(func() { testPairAPIToken = "" })
	after := project.Duration(5 * time.Second)
	return newHotPairRig(t, func(_ string, p *project.Project) {
		p.Redundancy.Failback = &project.FailbackConfig{Prefer: "a", After: &after}
	})
}

// failback suspends or resumes on n.
func (r *pairRig) failback(t *testing.T, n string, suspend bool) {
	t.Helper()
	body := `{"suspend":false}`
	if suspend {
		body = `{"suspend":true,"why":"test"}`
	}
	if code, b := r.authed(n, http.MethodPost, "/api/cluster/failback", body); code != http.StatusOK {
		t.Fatalf("failback suspend=%v on %s: %d %s", suspend, n, code, b)
	}
}

// The pair fails back to a, hot, once a has been ready for after; a
// manual switchover away from a suspends it, carried to b in the final
// image, and b does not fail back until an operator resumes.
func TestPairFailbackIsHotAndAManualSwitchoverSuspends(t *testing.T) {
	r := newFailbackRig(t)
	if r.leader() == "b" {
		r.switchReady(t, "b")
		r.waitNewLeader(t, "a", 20*time.Second)
	}
	// a leads: a manual switchover away from it suspends failback.
	r.set(t, "a", "Mode", 5)
	r.switchReady(t, "a")
	if code, ans := r.readySwitchover(t, "a"); code != http.StatusOK || ans.Outcome != "done" {
		t.Fatalf("switchover: %d %+v", code, ans)
	}
	r.waitNewLeader(t, "b", 5*time.Second)
	pairWait(t, "b's failback status", 5*time.Second, func() bool {
		s := r.streamStatus("b")
		return s != nil && s.Failback != nil
	})
	if f := r.streamStatus("b").Failback; !f.Suspended || !strings.Contains(f.Why, "manual switchover") {
		t.Fatalf("b's failback after the manual switchover: %+v, want suspended (carried from a)", f)
	}
	r.switchReady(t, "b")
	time.Sleep(6 * time.Second) // past after: no failback while suspended
	if got := r.leader(); got != "b" {
		t.Fatalf("failed back to %s while suspended", got)
	}

	// Resumed (asked on the standby: forwarded to b), it fails back hot.
	r.failback(t, "a", false)
	stopped := 0
	t0 := time.Now()
	pairWait(t, "the failback to a", 20*time.Second, func() bool {
		if c := r.count("b"); c > stopped {
			stopped = c
		}
		return r.nodes["a"].m.q.IsLeader() && r.nodes["a"].rt.Leading()
	})
	t.Logf("failback %s after the resume (after 5 s)", time.Since(t0).Round(100*time.Millisecond))
	if time.Since(t0) < 5*time.Second {
		t.Fatalf("failed back %s after the resume, before after (5 s)", time.Since(t0))
	}
	pairWait(t, "a's takeover report", 5*time.Second, func() bool {
		s := r.streamStatus("a")
		return s != nil && s.Takeover != ""
	})
	if s := r.streamStatus("a"); s.Takeover != "hot" {
		t.Fatalf("failback takeover %q (%s), want hot", s.Takeover, s.TakeoverCold)
	}
	if got := r.get("a", "Mode"); got != "5" {
		t.Errorf("Mode after the failback = %s, want the operator's 5", got)
	}
	if got := r.count("a"); got < stopped {
		t.Errorf("Count after the failback = %d, below b's last %d: scans repeated", got, stopped)
	}
	if f := r.streamStatus("a").Failback; f == nil || f.Suspended {
		t.Fatalf("a's failback after the failback: %+v, want on", f)
	}
}
