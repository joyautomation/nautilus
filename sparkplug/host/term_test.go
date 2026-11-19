package host

import (
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
