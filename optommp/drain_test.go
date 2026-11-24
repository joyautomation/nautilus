package optommp

import (
	"context"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/internal/fence"
)

// #710 d1b: on a pair an OptoMMP output write is counted for a planned
// switchover's drain while it waits for the unit's answer, and a drain
// resets it there (the unit never answers; its budget is long), so the
// drain ends at once with nothing left counted instead of waiting the
// budget out. A done called before the write turns it red (nothing is
// counted during it); one never called hangs the drain.
func TestOptoMMPWriteIsCountedAndCutShortByTheDrain(t *testing.T) {
	c, unit, _ := silentUnit(t)
	d := fencedOpto(t)
	d.SetSendGate(func() (bool, time.Duration, uint64) { return true, 3 * time.Second, 1 })
	flushed := make(chan error, 1)
	go func() { _, err := d.flushCount(c); flushed <- err }()
	time.Sleep(150 * time.Millisecond) // the write is out, its answer awaited
	if n := fence.InFlight(); n < 1 {
		t.Fatal("nothing in flight while the write waits for its answer; want it counted")
	}
	d.SetSendGate(func() (bool, time.Duration, uint64) { return false, 0, 1 })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	if err := fence.Drain(ctx, 0); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if took := time.Since(start); took > time.Second {
		t.Fatalf("the drain took %v; want the waiting write reset at once", took)
	}
	if err := <-flushed; err == nil {
		t.Fatal("the reset write reported success")
	}
	c.conn.Close()
	<-unit
	drainSettles(t, "after the drain")
}

// drainSettles waits for nothing to be counted in flight: exact counts
// would catch a send of a driver an earlier test left running.
func drainSettles(t *testing.T, what string) {
	t.Helper()
	for end := time.Now().Add(2 * time.Second); fence.InFlight() != 0; time.Sleep(time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("%s: %d still counted in flight; a drain would never end", what, fence.InFlight())
		}
	}
}
