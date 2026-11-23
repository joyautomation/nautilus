package node

import (
	"context"
	"net/http"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/quorum"
)

// A member whose state writes are slow, though well inside a couple of
// seconds, still completes an operator's clear: the wait for the write
// is the command's, not a share of the lease (#1329). On a CI runner a
// witness's fsync outlasted its quarter-lease wait and from-spare ended
// incomplete; at the 1 s default lease that wait is 250 ms on a real
// witness, shorter than an fsync on a slow SD card. Here the witness
// takes 600 ms over the clear's write: past its 125 ms share of the
// loopback lease, past the controller's 500 ms call to it, and under the
// floor. dup_wait_lease_scaled turns it red.
func TestAClearWaitsOutASlowDisk(t *testing.T) {
	c := newCluster(t)
	c.startAll()
	c.commission()
	c.settledLeader(leases(20))

	// The witness again, on a disk whose next write once slow is set
	// takes 600 ms: the clear's (or a grant's just before it, which the
	// clear's then waits behind). One write only: the witness's replies
	// wait for its writes too, and the re-check a lease later needs them.
	var slow atomic.Bool
	c.stopWitness()
	w, err := NewWitness(WitnessOptions{Dir: c.wit.dir, Token: c.tok[WitnessID], Lease: c.lease,
		write: func(f *os.File, b []byte) (int, error) {
			if slow.CompareAndSwap(true, false) {
				time.Sleep(600 * time.Millisecond)
			}
			return f.Write(b)
		}})
	if err != nil {
		t.Fatal(err)
	}
	h, _ := Guard(c.tok[WitnessID], w.Handler())
	ctx, cancel := context.WithCancel(context.Background())
	c.wit.w, c.wit.cancel, c.wit.ran = w, cancel, make(chan struct{})
	go func() { w.Run(ctx); close(c.wit.ran) }()
	c.wit.srv = &http.Server{Handler: h}
	go c.wit.srv.Serve(c.listen(WitnessID))
	c.settledLeader(leases(20))

	c.markAt("b", "a")
	c.witnessVoter(func(v *quorum.Voter, now quorum.Instant) { markOld(t, v, now, "a") })
	c.waitFor("a to be held by the marks", leases(10), func() bool { return c.status("a").Held })
	time.Sleep(leases(2)) // every member's voter up a lease and D (Voter.Settled)

	slow.Store(true)
	cctx, ccancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer ccancel()
	rep, err := c.elector("b").ClearDuplicateAll(cctx, "a", "user:op")
	if err != nil {
		t.Fatalf("clear on a slow disk: %v (report %+v)", err, rep)
	}
	for _, a := range rep.Clear {
		if a.Err != "" {
			t.Errorf("%s: %s", a.Member, a.Err)
		}
	}
	c.witnessVoter(func(v *quorum.Voter, now quorum.Instant) {
		if m := v.Marked(); len(m) != 0 {
			t.Errorf("the witness still marks %v", m)
		}
	})
}

// The floor holds at every lease: never shorter than 2 s, and a long
// lease's own share where that is longer.
func TestDupWriteWaitHasAFloor(t *testing.T) {
	for _, c := range []struct{ share, want time.Duration }{
		{125 * time.Millisecond, 2 * time.Second},
		{250 * time.Millisecond, 2 * time.Second},
		{5 * time.Second, 5 * time.Second},
	} {
		if got := dupWriteWait(c.share); got != c.want {
			t.Errorf("dupWriteWait(%v) = %v, want %v", c.share, got, c.want)
		}
	}
}
