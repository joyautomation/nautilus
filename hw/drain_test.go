package hw

import (
	"context"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/internal/fence"
	"github.com/joyautomation/nautilus/lang/ir"
)

// #710 d1b: on a pair each Redfish, SNMP or Prometheus command is counted
// for a planned switchover's drain while it runs, and nothing is left
// counted once the flush ends, nor after commands dropped as an earlier
// term's. A done called before the command (or never, on the old-term
// path) turns it red.
func TestEachCommandIsCountedForTheDrain(t *testing.T) {
	var during []int
	b, s, sent, _ := fencedBase(t, func(int) { during = append(during, fence.InFlight()) })
	b.SetSendGate(func() (bool, time.Duration, uint64) { return true, time.Second, 1 })
	b.flushWrites(context.Background(), s)
	if len(*sent) != 3 {
		t.Fatalf("sent %v; want all three", *sent)
	}
	for i, n := range during {
		if n < 1 {
			t.Errorf("command %d ran with nothing in flight: a drain would not wait for it", i)
		}
	}
	drainSettles(t, "after the flush")

	b2, s2, sent2, _ := fencedBase(t, nil)
	b2.SetSendGate(func() (bool, time.Duration, uint64) { return true, time.Second, 9 }) // queued in term 1
	b2.flushWrites(context.Background(), s2)
	if len(*sent2) != 0 {
		t.Fatalf("an earlier term's commands: sent %v; want none", *sent2)
	}
	drainSettles(t, "after an earlier term's commands")
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

// A command that hangs (a BMC that never answers) does not hold a drain to
// its send deadline: the drain cancels its fenced context, and the
// command ends at once.
func TestADrainCutsAHungCommandShort(t *testing.T) {
	b, s, _, _ := fencedBase(t, nil)
	s.pending = s.pending[:1]
	b.write = func(ctx context.Context, _ string, _ WriteDecl, _ ir.Value) error {
		<-ctx.Done()
		return ctx.Err()
	}
	b.SetSendGate(func() (bool, time.Duration, uint64) { return true, 3 * time.Second, 1 })
	flushed := make(chan struct{})
	go func() { defer close(flushed); b.flushWrites(context.Background(), s) }()
	for end := time.Now().Add(time.Second); fence.InFlight() == 0; time.Sleep(time.Millisecond) {
		if time.Now().After(end) {
			t.Fatal("the command never started")
		}
	}
	b.SetSendGate(func() (bool, time.Duration, uint64) { return false, 0, 1 })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	if err := fence.Drain(ctx, 0); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if took := time.Since(start); took > time.Second {
		t.Fatalf("the drain took %v; want the hung command cut at once", took)
	}
	<-flushed
}
