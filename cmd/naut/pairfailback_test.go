package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/leader"
	"github.com/joyautomation/nautilus/quorum"
	"github.com/joyautomation/nautilus/quorum/node"
	"github.com/joyautomation/nautilus/retain"
)

// fakeFailbackPair is the failback's pair in unit tests: who leads, in
// which term, why the standby is not ready, and the switchover's outcome.
type fakeFailbackPair struct {
	term     uint64
	lead     bool
	notReady string
	outcome  string
	switches int
}

func (p *fakeFailbackPair) leadTerm() (uint64, bool)            { return p.term, p.lead }
func (p *fakeFailbackPair) switchRefusal(uint64, string) string { return p.notReady }
func (p *fakeFailbackPair) switchover(context.Context, string) (switchReport, string, string) {
	p.switches++
	if p.outcome == "" {
		return switchReport{}, leader.SwitchDone, ""
	}
	return switchReport{}, p.outcome, "test"
}

// newTestFailback is self's failback (prefer a, after 10 s, max 3) on a
// clock the test moves.
func newTestFailback(self string) (*pairFailback, *time.Time) {
	clock := time.Unix(1_000_000, 0)
	m := &pairMember{o: node.Options{Self: quorum.NodeID(self), Lease: time.Second}}
	f := &pairFailback{m: m, prefer: "a", after: 10 * time.Second, max: 3, now: func() time.Time { return clock }}
	m.failback = f
	return f, &clock
}

// run ticks f every second for d, on its clock.
func runFailback(f *pairFailback, clock *time.Time, p *fakeFailbackPair, d time.Duration) {
	for end := clock.Add(d); clock.Before(end); *clock = clock.Add(time.Second) {
		f.tick(context.Background(), p)
	}
}

// The non-preferred leader fails back once the preferred standby has been
// ready for after, not before; any break in the readiness starts the clock
// again. failback_ready_never_resets turns it red.
func TestFailbackWaitsForAfterWithoutABreak(t *testing.T) {
	f, clock := newTestFailback("b")
	p := &fakeFailbackPair{term: 7, lead: true}
	runFailback(f, clock, p, 6*time.Second)
	p.notReady = "the standby is not current"
	runFailback(f, clock, p, time.Second)
	if st := f.status(true); st.NotReady != p.notReady || st.ReadyMs != 0 {
		t.Fatalf("status after a break: %+v", st)
	}
	p.notReady = ""
	runFailback(f, clock, p, 9*time.Second)
	if p.switches != 0 {
		t.Fatalf("failed back %d time(s) before after of unbroken readiness", p.switches)
	}
	runFailback(f, clock, p, 2*time.Second)
	if p.switches != 1 {
		t.Fatalf("failed back %d time(s) once ready for after, want 1", p.switches)
	}
}

// Nothing happens on a standby, on the preferred controller leading, or
// while suspended. failback_ignores_suspend turns it red.
func TestFailbackOnlyFromTheOtherLeaderUnsuspended(t *testing.T) {
	f, clock := newTestFailback("b")
	p := &fakeFailbackPair{term: 7}
	runFailback(f, clock, p, 30*time.Second) // a standby
	f.setSuspended(true, "user:op", "maintenance on a")
	p.lead = true
	runFailback(f, clock, p, 30*time.Second)
	if p.switches != 0 {
		t.Fatalf("failed back %d time(s) while a standby or suspended", p.switches)
	}
	if c := f.condition(true); !strings.Contains(c, "suspended") || !strings.Contains(c, "user:op") {
		t.Fatalf("condition %q, want suspended by user:op", c)
	}
	f.setSuspended(false, "user:op", "")
	runFailback(f, clock, p, 12*time.Second)
	if p.switches != 1 {
		t.Fatalf("failed back %d time(s) after resume, want 1", p.switches)
	}
	fa, clock := newTestFailback("a")
	pa := &fakeFailbackPair{term: 7, lead: true}
	runFailback(fa, clock, pa, 30*time.Second)
	if pa.switches != 0 {
		t.Fatal("the preferred controller failed back to itself")
	}
}

// A failback each time followed within after by a failover back stops
// itself at max: suspended, stopped, its alarm up. A failback that holds
// resets the count. failback_never_stops and failback_count_never_resets
// turn it red.
func TestFailbackPingPongStops(t *testing.T) {
	f, clock := newTestFailback("b")
	p := &fakeFailbackPair{term: 7, lead: true}
	for i := 1; i <= 3; i++ {
		runFailback(f, clock, p, 12*time.Second)
		if p.switches != i {
			t.Fatalf("round %d: %d failbacks", i, p.switches)
		}
		p.lead = false
		runFailback(f, clock, p, 2*time.Second) // a leads, then fails
		p.term, p.lead = p.term+2, true
		runFailback(f, clock, p, time.Second) // b takes over: a failover
	}
	st := f.status(true)
	if !st.Suspended || !st.Stopped || st.Count != 3 || !strings.Contains(st.Why, "3 failbacks") {
		t.Fatalf("after 3 ping-pongs: %+v, want suspended and stopped at 3", st)
	}
	if !f.stopped() {
		t.Fatal("the ping-pong stop's alarm is not up")
	}
	runFailback(f, clock, p, 30*time.Second)
	if p.switches != 3 {
		t.Fatalf("failed back again after the stop (%d)", p.switches)
	}

	// The preferred controller holding for after resets the count.
	fa, clock := newTestFailback("a")
	fa.st.Count = 2
	pa := &fakeFailbackPair{term: 9, lead: true}
	runFailback(fa, clock, pa, 5*time.Second)
	if fa.st.Count != 2 {
		t.Fatal("the count reset before the preferred controller held for after")
	}
	runFailback(fa, clock, pa, 6*time.Second)
	if fa.st.Count != 0 {
		t.Fatalf("count %d after the preferred controller held for after, want 0", fa.st.Count)
	}
}

// A takeover by the other controller long after its failback, or after a
// switchover that did not hand over, is not counted.
func TestFailbackCountsOnlyAFailoverWithinAfter(t *testing.T) {
	f, clock := newTestFailback("b")
	p := &fakeFailbackPair{term: 7, lead: true}
	runFailback(f, clock, p, 12*time.Second)
	p.lead = false
	runFailback(f, clock, p, 20*time.Second) // a held past after
	p.term, p.lead = 9, true
	runFailback(f, clock, p, time.Second)
	p.outcome = leader.SwitchAborted
	runFailback(f, clock, p, 11*time.Second) // a failback that aborts
	p.term = 11
	runFailback(f, clock, p, time.Second)
	if f.st.Count != 0 {
		t.Fatalf("count %d, want 0", f.st.Count)
	}
}

// A manual switchover away from the preferred controller suspends
// failback, by its operator; one that does not hand over puts it back.
// failback_manual_not_suspended turns it red.
func TestFailbackManualSwitchoverAwaySuspends(t *testing.T) {
	f, _ := newTestFailback("a")
	undo := f.switchingAway("user:op")
	if c := f.condition(true); !strings.Contains(c, "manual switchover") || !strings.Contains(c, "user:op") {
		t.Fatalf("condition %q, want suspended by the manual switchover", c)
	}
	if got := f.RetainedFailback(); got == nil || !got.Suspended {
		t.Fatalf("retained %+v: the final image must carry the suspension", got)
	}
	undo()
	if f.st.Suspended {
		t.Fatal("a switchover that did not hand over left failback suspended")
	}
	fb, _ := newTestFailback("b")
	fb.switchingAway("user:op")
	if fb.st.Suspended {
		t.Fatal("a switchover toward the preferred controller suspended failback")
	}
}

// An unknown state (the store unreadable, nothing carried) suspends
// failback rather than read as never set.
func TestFailbackUnknownStateSuspends(t *testing.T) {
	f, _ := newTestFailback("b")
	f.RestoreFailback(nil, false)
	if !f.st.Suspended || !strings.Contains(f.st.Why, "could not be read") {
		t.Fatalf("state %+v, want suspended as unreadable", f.st)
	}
	f.RestoreFailback(&retain.FailbackState{Count: 1}, true)
	if f.st.Suspended || f.st.Count != 1 {
		t.Fatalf("state %+v, want the loaded one", f.st)
	}
}

// The I/O comparison: a driver the leader has connected must be connected
// on the standby too; one the standby cannot see ("waiting") is skipped,
// and one the leader has not connected is not asked of the standby.
func TestIONotReached(t *testing.T) {
	lead := []switchDriver{
		{Kind: "modbus", Name: "pump1", State: "connected"},
		{Kind: "modbus", Name: "pump2", State: "connected"},
		{Kind: "ethernet-ip", Name: "rack", State: "connected"},
		{Kind: "modbus", Name: "spare", State: "error"},
	}
	standby := []switchDriver{
		{Kind: "modbus", Name: "pump1", State: "connected"},
		{Kind: "modbus", Name: "pump2", State: "connecting"},
		{Kind: "ethernet-ip", Name: "rack", State: "waiting"},
	}
	got := strings.Join(ioNotReached(lead, standby), ", ")
	if got != "modbus pump2" {
		t.Fatalf("not reached: %q, want modbus pump2", got)
	}
	if bad := ioNotReached(lead[:1], nil); len(bad) != 1 {
		t.Fatalf("a standby with no drivers: %v, want pump1 not reached", bad)
	}
}

// The undo of a switchover that did not hand over puts back only what it
// set: an operator's suspend that landed meanwhile stands (Fable on d2).
// failback_undo_overwrites turns it red.
func TestFailbackUndoKeepsAnOperatorsSuspend(t *testing.T) {
	f, _ := newTestFailback("a")
	undo := f.switchingAway("user:op")
	f.setSuspended(true, "user:other", "servicing a")
	undo()
	if !f.st.Suspended || f.st.By != "user:other" {
		t.Fatalf("state %+v after the undo, want user:other's suspend", f.st)
	}
}

// A failback that passed the resign but ended as a failover moved the
// lead too: a failover back within after counts (Fable on d2).
// failback_failover_not_counted turns it red.
func TestFailbackCountsAFailbackThatEndedAsAFailover(t *testing.T) {
	f, clock := newTestFailback("b")
	p := &fakeFailbackPair{term: 7, lead: true, outcome: leader.SwitchFailover}
	runFailback(f, clock, p, 12*time.Second)
	p.lead = false
	runFailback(f, clock, p, 2*time.Second)
	p.term, p.lead = 9, true
	runFailback(f, clock, p, time.Second)
	if f.st.Count != 1 {
		t.Fatalf("count %d after a failback that ended as a failover and a failover back, want 1", f.st.Count)
	}
}
