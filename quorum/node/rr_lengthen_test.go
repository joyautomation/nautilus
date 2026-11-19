package node

import (
	"testing"
	"time"

	"github.com/joyautomation/nautilus/quorum"
)

// 2s -> 4s -> 2s across two restarts; the first grant after the 4s wait
// is A's renewal at its unchanged term, so nothing is written.
func TestReviewLengthenThenShorten(t *testing.T) {
	dir := t.TempDir()
	clk := newFakeClock()
	w1, stop1 := startWitness(t, WitnessOptions{Dir: dir, Lease: 2 * time.Second, clock: clk.now})
	vote(t, w1.Handler(), 2*time.Second, "a,b", quorum.Request{From: "a", Term: 1, Nonce: 5, ID: 1})
	clk.add(3 * time.Second)
	waitFor(t, 2*time.Second, func() bool {
		_, rep := vote(t, w1.Handler(), 2*time.Second, "a,b", quorum.Request{From: "a", Term: 7, Nonce: 5, ID: 2})
		return rep.Granted
	})
	stop1()
	w2, stop2 := startWitness(t, WitnessOptions{Dir: dir, Lease: 4 * time.Second, clock: clk.now})
	// B's probe opens the pair (its wait starts); A's renewals don't reach W in the wait.
	vote(t, w2.Handler(), 4*time.Second, "a,b", quorum.Request{From: "b", Term: 0, Nonce: 9, ID: 1})
	clk.add(4*time.Second + time.Millisecond)
	waitFor(t, 2*time.Second, func() bool {
		_, rep := vote(t, w2.Handler(), 4*time.Second, "a,b", quorum.Request{From: "a", Term: 7, Renewal: true, Nonce: 5, ID: 3})
		return rep.Granted
	})
	aEnd := clk.now().Add(time.Duration(float64(4*time.Second) * (1 - DefaultRho)))
	st, _ := (&State{Path: dir + "/" + testCluster + stateSuffix}).Load()
	t.Logf("file after the 4s renewal grant: Promised %d Lease %v", st.Promised, st.Lease)
	stop2()
	w3, stop3 := startWitness(t, WitnessOptions{Dir: dir, Lease: 2 * time.Second, clock: clk.now})
	defer stop3()
	vote(t, w3.Handler(), 2*time.Second, "a,b", quorum.Request{From: "b", Term: 8, Nonce: 9, ID: 9})
	clk.add(2*time.Second + time.Millisecond)
	for i := range 20 {
		_, rep := vote(t, w3.Handler(), 2*time.Second, "a,b", quorum.Request{From: "b", Term: 8, Nonce: 9, ID: uint64(10 + i)})
		if rep.Granted && clk.now() < aEnd {
			t.Fatalf("granted B term 8 at %v inside A's 4s term-7 lease (to %v)", clk.now(), aEnd)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
