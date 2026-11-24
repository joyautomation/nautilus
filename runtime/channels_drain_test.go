package runtime_test

import (
	"context"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/internal/fence"
)

// #710 d1b: on a pair a channel WRITE is counted for a planned
// switchover's drain while the channel carries it, and nothing is left
// counted once it has returned, nor after a write dropped for a gate read
// in another term. A done called before the write (or never) turns it red.
func TestPairChannelWriteIsCountedForTheDrain(t *testing.T) {
	coord := &pairCoord{}
	coord.open.Store(true)
	coord.term.Store(7)
	during := make(chan int, 1)
	pairWrite(t, coord, func(context.Context) { during <- fence.InFlight() })
	select {
	case n := <-during:
		if n < 1 {
			t.Fatal("the channel write ran with nothing in flight: a drain would not wait for it")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the write never reached the channel")
	}
	waitInFlight(t, 0)

	coord2 := &pairCoord{}
	coord2.open.Store(true)
	coord2.term.Store(7)
	coord2.sendTerm.Store(8)
	pairWrite(t, coord2, nil)
	time.Sleep(100 * time.Millisecond)
	waitInFlight(t, 0)
}

func waitInFlight(t *testing.T, want int) {
	t.Helper()
	for end := time.Now().Add(2 * time.Second); fence.InFlight() != want; time.Sleep(time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("%d in flight; want %d", fence.InFlight(), want)
		}
	}
}
