package runtime_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/runtime"
)

// gateChannel is a ChannelIO whose first write blocks until the test
// releases it, so a second write waits in the queue behind it.
type gateChannel struct {
	mu      sync.Mutex
	writes  []string // variables written, in order
	arrived chan string
	release chan struct{}
	held    atomic.Bool
}

func newGateChannel() *gateChannel {
	return &gateChannel{arrived: make(chan string, 8), release: make(chan struct{})}
}

func (c *gateChannel) Read(ctx context.Context, variable string, shape ir.Value, elem string) (ir.Value, error) {
	return shape, nil
}

func (c *gateChannel) Write(ctx context.Context, variable string, data ir.Value, elem string) error {
	c.mu.Lock()
	c.writes = append(c.writes, variable)
	c.mu.Unlock()
	c.arrived <- variable
	if c.held.CompareAndSwap(false, true) {
		select {
		case <-c.release:
		case <-ctx.Done():
		}
	}
	return nil
}

func (c *gateChannel) Close() error { return nil }

func (c *gateChannel) seen() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.writes...)
}

const twoWrites = `PROGRAM Main
VAR_EXTERNAL P1 : BOOL; P2 : BOOL; V : DINT; END_VAR
VAR w1 : WRITE; w2 : WRITE; END_VAR
w1(REQ := P1, ID := 'peer', VAR_1 := 'A', SD_1 := V);
w2(REQ := P2, ID := 'peer', VAR_1 := 'B', SD_1 := V);
END_PROGRAM`

// queueBehindInFlight builds a runtime on coord, posts write A (held in
// flight by the peer) and write B (queued behind it), and returns them.
func queueBehindInFlight(t *testing.T, coord runtime.Coordinator) (*runtime.Runtime, *gateChannel) {
	t.Helper()
	ch := newGateChannel()
	rt, err := runtime.New(runtime.Options{
		Program:     twoWrites,
		Scan:        time.Hour,
		Seed:        map[string]any{"P1": false, "P2": false, "V": int64(7)},
		Coordinator: coord,
		Channels:    []runtime.Channel{{Name: "peer", Channel: ch, Timeout: 10 * time.Second}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { close(ch.release) })
	rt.Scan() // the first takeover
	if err := rt.Tags().WriteGlobal("P1", ir.BoolVal(true)); err != nil {
		t.Fatal(err)
	}
	rt.Scan()
	select {
	case v := <-ch.arrived:
		if v != "A" {
			t.Fatalf("first write to reach the peer: %s, want A", v)
		}
	case <-time.After(time.Second):
		t.Fatal("write A never reached the peer")
	}
	if err := rt.Tags().WriteGlobal("P2", ir.BoolVal(true)); err != nil {
		t.Fatal(err)
	}
	rt.Scan() // B is queued behind A
	return rt, ch
}

// noSecondWrite releases the held write and checks that the queued one
// never reaches the peer.
func noSecondWrite(t *testing.T, ch *gateChannel, why string) {
	t.Helper()
	ch.release <- struct{}{}
	select {
	case v := <-ch.arrived:
		t.Fatalf("write %s reached the peer %s; seen %v", v, why, ch.seen())
	case <-time.After(300 * time.Millisecond):
	}
}

// A write queued in one term must not leave in the next, even in the
// window where the new term's gate is already open but the runtime has not
// yet run its takeover (it waits for the next scan and the inputs wait,
// #786). Found in review of #797: the leadership count moved only inside
// takeover, so this write left in term 2.
func TestChannelWriteFromOldTermNotSentBeforeTakeover(t *testing.T) {
	coord := &termFlag{}
	coord.lead.Store(true)
	coord.term.Store(1)
	_, ch := queueBehindInFlight(t, coord)

	coord.lead.Store(false) // the lease is lost
	coord.term.Store(2)     // and won again, in a new term
	coord.lead.Store(true)
	// No scan: takeover has not run yet.
	noSecondWrite(t, ch, "in term 2 before the takeover ran")
}

// A write still queued when the gate closes is not sent while it is closed
// (the runtime wires the coordinator's gate into the channels).
func TestChannelWriteNotSentWhileGateClosed(t *testing.T) {
	coord := &termFlag{}
	coord.lead.Store(true)
	coord.term.Store(1)
	_, ch := queueBehindInFlight(t, coord)

	coord.lead.Store(false)
	noSecondWrite(t, ch, "with the gate closed")
}

// Without a term (a Coordinator that is not Termed), every opening of the
// gate is a takeover: a write queued before it is not sent after it.
func TestChannelWriteFromOldLeadershipNotSentAfterTakeover(t *testing.T) {
	coord := &leadFlag{}
	coord.lead.Store(true)
	rt, ch := queueBehindInFlight(t, coord)

	coord.lead.Store(false)
	rt.Scan() // standing by
	for _, n := range []string{"P1", "P2"} {
		if err := rt.Tags().WriteGlobal(n, ir.BoolVal(false)); err != nil {
			t.Fatal(err)
		}
	}
	coord.lead.Store(true)
	rt.Scan() // the takeover: a new leadership
	noSecondWrite(t, ch, "after the takeover")
}

// The same leadership resumed (the gate closed and reopened in one term,
// nobody else led) is not a takeover: a write still queued is sent.
func TestChannelWriteSentOnSameTermResume(t *testing.T) {
	coord := &termFlag{}
	coord.lead.Store(true)
	coord.term.Store(1)
	_, ch := queueBehindInFlight(t, coord)

	coord.lead.Store(false)
	coord.lead.Store(true)
	ch.release <- struct{}{}
	select {
	case v := <-ch.arrived:
		if v != "B" {
			t.Fatalf("second write: %s, want B", v)
		}
	case <-time.After(time.Second):
		t.Fatalf("queued write never sent on a same-term resume; seen %v", ch.seen())
	}
}
