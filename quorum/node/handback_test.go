package node

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The hand-back's election side (#710 S2c-3, handback.go), on loopback.

// A leader hands the lead back: its gate closes at once, its peer leads
// within the hold, and the guard holds, also across a restart (it is in
// the voter state). handback_keeps_renewing and handback_guard_volatile
// turn it red.
func TestLoopbackHandBackGivesTheLeadToThePeer(t *testing.T) {
	c := newCluster(t)
	c.startAll()
	c.commission()
	who := c.settledLeader(leases(20))
	peer := c.other(who)
	e := c.elector(who)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := e.HandBack(ctx, e.Term(), c.leases(3), "test"); err != nil {
		t.Fatal(err)
	}
	if e.IsLeader() {
		t.Fatal("the gate is open after the hand-back")
	}
	c.waitFor(string(peer)+" to lead", c.leases(4), func() bool { return c.elector(peer).IsLeader() })
	if held, _ := e.HandBackHeld(); !held {
		t.Fatal("no guard after a hand-back")
	}
	c.kill(who)
	c.start(who)
	if held, _ := c.elector(who).HandBackHeld(); !held {
		t.Fatal("the guard did not survive a restart")
	}
}

// A hand-back whose write fails does not happen, and arms no guard: a
// guard for a hand-back that never was would refuse the next one, and the
// peer's question, for a minute (Fable, S3). The leader keeps the lead.
// handback_guard_kept_on_failure turns it red.
func TestLoopbackFailedHandBackArmsNoGuard(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes into a read-only directory")
	}
	c := newCluster(t)
	c.startAll()
	c.commission()
	who := c.settledLeader(leases(20))
	e := c.elector(who)
	dir := filepath.Dir(c.ctl[who].state)
	os.Chmod(dir, 0o500)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	err := e.HandBack(ctx, e.Term(), c.leases(3), "test")
	os.Chmod(dir, 0o700)
	if err == nil {
		t.Fatal("a hand-back whose write failed went on")
	}
	if held, _ := e.HandBackHeld(); held {
		t.Fatal("a failed hand-back armed the guard")
	}
	if !e.IsLeader() {
		t.Fatal("a failed hand-back closed the gate")
	}
}
