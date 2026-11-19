package host

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// 3b-3b, design §3.4: a command held across a closed leadership gate (#943)
// is published only under the term of the lease it was computed in. The
// gate closes for less than leaderPoll (serve never sees it), and opens
// again under term 9: this replica lost the lease and won it back, so the
// command computed under term 5 is dropped, never published; one computed
// under term 9 is. old_term_kept turns it red.
func TestHeldCommandFromAnEarlierTermIsNotPublished(t *testing.T) {
	var term atomic.Uint64
	term.Store(5)
	d, w, leader := startWithGate(t, "h-term", func(d *Driver, g func() bool) {
		d.SetWriteGate(g)
		d.SetSendGate(func() (bool, time.Duration, uint64) { return g(), time.Second, term.Load() })
	})
	writeSpeed(t, d, 11) // under term 5: the term is seen, and 11 goes out
	waitUntil(t, "the term-5 command", func() bool { return len(w.commands(t)) == 1 })

	leader.Store(false)
	writeSpeed(t, d, 22) // computed under term 5, held by the closed gate
	time.Sleep(250 * time.Millisecond)
	term.Store(9)
	leader.Store(true) // won again, under term 9
	time.Sleep(leaderPoll + 300*time.Millisecond)
	barrier(t, d, w)
	if c := w.commands(t); len(c) != 1 {
		t.Fatalf("commands = %v; want only the term-5 11 (the held 22 was computed under term 5)", c)
	}

	writeSpeed(t, d, 33) // computed under term 9
	waitUntil(t, "the term-9 command", func() bool { return len(w.commands(t)) == 2 })
	if c := w.commands(t); c[1] != 33.0 {
		t.Fatalf("commands = %v; want [11 33]", c)
	}
}

// #1047 review (2): a term move that ANOTHER caller saw first (a scan's
// WriteOutputs, while a flush holds its work) still stops the flush: the
// work it took under the earlier term is neither published, nor put back,
// nor parked. The reviewer's probe: the flush's after-swap hook moves the
// term and runs WriteOutputs. old_term_kept turns both red.
func TestTermMoveSeenByAnotherCallerStopsTheFlush(t *testing.T) {
	for _, dark := range []bool{false, true} {
		name := "publish"
		if dark {
			name = "park"
		}
		t.Run(name, func(t *testing.T) {
			var term atomic.Uint64
			term.Store(5)
			d, w, _ := startWithGate(t, "h-gen-"+name, func(d *Driver, g func() bool) {
				d.SetWriteGate(g)
				d.SetSendGate(func() (bool, time.Duration, uint64) { return g(), time.Second, term.Load() })
			})
			writeSpeed(t, d, 11)
			waitUntil(t, "the term-5 command", func() bool { return len(w.commands(t)) == 1 })
			if dark {
				d.handleMessage("spBv1.0/G/NDEATH/W6", enc(t, ndeath(1))) // the site goes dark: commands park
			}
			fired := false
			hook := func() {
				if fired {
					return
				}
				fired = true
				term.Store(9)
				_ = d.WriteOutputs(nil) // a scan sees the move first
			}
			testHookAfterSwap.Store(&hook)
			t.Cleanup(func() { testHookAfterSwap.Store(nil) })
			writeSpeed(t, d, 22) // computed under term 5; the flush takes it, then the term moves
			time.Sleep(300 * time.Millisecond)
			if dark {
				d.handleMessage("spBv1.0/G/NBIRTH/W6", enc(t, nbirth(2))) // back: anything parked would go now
				time.Sleep(300 * time.Millisecond)
			}
			barrier(t, d, w)
			if c := w.commands(t); len(c) != 1 {
				t.Fatalf("commands = %v; want only the term-5 11 (the 22 the flush held was computed under term 5)", c)
			}
		})
	}
}

// #1047 review (2), the requeue: a publish that fails after the term
// moved (seen by another caller) does not put its earlier-term command
// back, or the next flush would publish it under the new term.
func TestFailedPublishAfterATermMoveIsNotRequeued(t *testing.T) {
	var term atomic.Uint64
	term.Store(5)
	d, w, _ := startWithGate(t, "h-gen-requeue", func(d *Driver, g func() bool) {
		d.SetWriteGate(g)
		d.SetSendGate(func() (bool, time.Duration, uint64) { return g(), time.Second, term.Load() })
	})
	writeSpeed(t, d, 11)
	waitUntil(t, "the term-5 command", func() bool { return len(w.commands(t)) == 1 })
	fail := func() error {
		if term.Load() == 5 {
			term.Store(9)
			_ = d.WriteOutputs(nil) // a scan sees the move first
		}
		return errors.New("broker lost")
	}
	testHookPublish.Store(&fail)
	t.Cleanup(func() { testHookPublish.Store(nil) })
	writeSpeed(t, d, 22) // computed under term 5; its publish fails after the move
	time.Sleep(200 * time.Millisecond)
	testHookPublish.Store(nil)
	d.kickWriter()
	time.Sleep(300 * time.Millisecond)
	barrier(t, d, w)
	if c := w.commands(t); len(c) != 1 {
		t.Fatalf("commands = %v; want only the term-5 11 (the failed 22 was put back and published under term 9)", c)
	}
}
