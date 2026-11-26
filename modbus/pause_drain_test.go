//go:build quorumtest

package modbus

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/internal/fence"
)

// A planned switchover's drain on Modbus over a real socket (#710 d1b): a
// write that read the gate open stalls after the read (fence.AfterCheck)
// or right before its system call (fence.RightBeforeSend); the gate
// closes behind it and fence.Drain runs. Nothing reaches the device after
// Drain returns: the drain waits for the stalled write (it was counted
// before the read, and its done comes after the request returns), and the
// write does not go out during it (not started, or its socket reset).
// A done called before the request ends lets Drain return while the
// write is still to go out, and the device then receives it.
// (go test -tags quorumtest -run Pause ./modbus/)
func TestModbusPauseAcrossCloseAndDrain(t *testing.T) {
	for _, pt := range []struct {
		name  string
		point fence.Point
	}{{"after the check", fence.AfterCheck}, {"right before the send", fence.RightBeforeSend}} {
		t.Run(pt.name, func(t *testing.T) {
			dev := newArrivals(t)
			d, s := fencedDriver(t)
			s.pending = map[string]any{"SP1": float64(1)}
			gate, opened := closableGate()
			d.SetSendGate(gate.read)
			tc, err := dialTCP(context.Background(), dev.addr(), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer tc.Close()

			const stall = 300 * time.Millisecond
			fence.Pause(pt.point, stall)
			defer fence.Pause(pt.point, 0)
			flushed := make(chan struct{})
			go func() {
				defer close(flushed)
				d.flushWrites(context.Background(), s, tc)
			}()
			drainAfterClose(t, gate, opened, dev, flushed)
		})
	}
}

// closableGate is a pair's gate with a long budget that a test closes;
// opened is closed at the first read that finds it open.
type testGate struct {
	shut atomic.Bool
	once sync.Once
	open chan struct{}
}

func closableGate() (*testGate, <-chan struct{}) {
	g := &testGate{open: make(chan struct{})}
	return g, g.open
}

func (g *testGate) read() (bool, time.Duration, uint64) {
	if g.shut.Load() {
		return false, 0, 1
	}
	g.once.Do(func() { close(g.open) })
	return true, 2 * time.Second, 1
}

// arrivals is a device that reads whatever comes and notes when bytes
// arrived; it never answers.
type arrivals struct {
	ln   net.Listener
	mu   sync.Mutex
	last time.Time
	n    int
}

func newArrivals(t *testing.T) *arrivals {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	a := &arrivals{ln: ln}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				buf := make([]byte, 512)
				for {
					n, err := c.Read(buf)
					if n > 0 {
						a.mu.Lock()
						a.n += n
						a.last = time.Now()
						a.mu.Unlock()
					}
					if err != nil {
						return
					}
				}
			}()
		}
	}()
	return a
}

func (a *arrivals) addr() string { return a.ln.Addr().String() }

func (a *arrivals) after(t0 time.Time) (int, time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.last.After(t0) {
		return a.n, a.last
	}
	return 0, time.Time{}
}

// drainAfterClose waits for the stalled send to have read the gate open,
// closes the gate, drains, and checks that nothing reaches the device
// after the drain returned, and that the flush then ends.
func drainAfterClose(t *testing.T, g *testGate, opened <-chan struct{}, dev *arrivals, flushed <-chan struct{}) {
	t.Helper()
	select {
	case <-opened:
	case <-time.After(3 * time.Second):
		t.Fatal("the write never read the gate")
	}
	time.Sleep(30 * time.Millisecond) // inside the stall
	g.shut.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	start := time.Now()
	if err := fence.Drain(ctx, 0); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	// Bounded by the stall, not the 2 s budget: what waits on the wire is cut.
	if took := time.Since(start); took > time.Second {
		t.Fatalf("the drain took %v; want it within the stall, not the send budget", took)
	}
	drained := time.Now()
	select {
	case <-flushed:
	case <-time.After(3 * time.Second):
		t.Fatal("the flush did not end")
	}
	time.Sleep(100 * time.Millisecond)
	if n, at := dev.after(drained); n > 0 {
		t.Fatalf("the device received bytes %v after the drain returned", at.Sub(drained))
	}
}
