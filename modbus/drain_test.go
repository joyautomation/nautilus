package modbus

import (
	"context"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/internal/fence"
)

// inflightConn is a healthy device that notes, during each request, how
// many sends a planned switchover's drain would wait for (fence.InFlight).
type inflightConn struct {
	fenceConn
	during []int
}

func (c *inflightConn) Request(ctx context.Context, unit uint8, fc byte, pdu []byte) ([]byte, error) {
	c.during = append(c.during, fence.InFlight())
	return c.fenceConn.Request(ctx, unit, fc, pdu)
}

// #710 d1b: on a pair every Modbus request (a bit's read and write, each
// register run) is counted for the drain while it is on the wire, and the
// count is back to nothing once the flush ends, so a drain neither
// misses a request nor waits forever. A done called before the request
// (or never) turns it red.
func TestModbusEachRequestIsCountedForTheDrain(t *testing.T) {
	d, s := fencedDriver(t)
	d.SetSendGate(func() (bool, time.Duration, uint64) { return true, time.Second, 1 })
	c := &inflightConn{}
	d.flushWrites(context.Background(), s, c)
	if len(c.during) != 4 {
		t.Fatalf("%d requests; want 4 (the bit's read and write, two registers)", len(c.during))
	}
	for i, n := range c.during {
		if n < 1 {
			t.Errorf("request %d ran with nothing in flight: a drain would not wait for it", i)
		}
	}
	drainSettles(t, "after the flush")
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
