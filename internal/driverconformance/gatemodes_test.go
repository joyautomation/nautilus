package driverconformance

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
)

// The C5/C8 gate flow checked against in-memory drivers, one per design
// and one per way to get it wrong: a driver that keeps its gate design
// passes, and each broken one fails with the reason.
func TestHarnessGateModes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mode    gateMode
		drv     stubBehaviour
		sampled bool   // the rig has no sent hook, and polls every 10 ms
		wantErr string // "" passes
	}{
		{"drop and ask", dropAndAsk, dropsAndAsks, false, ""},
		{"drop and ask, sends while closed", dropAndAsk, ignoresGate, false, "while the write gate was closed"},
		{"drop and ask, never asks", dropAndAsk, dropsSilently, false, "OutputsWanted after the gate reopened"},
		{"hold and deliver", holdAndDeliver, holdsLatest, false, ""},
		{"hold and deliver, sends while closed", holdAndDeliver, ignoresGate, false, "while the write gate was closed"},
		{"hold and deliver, drops", holdAndDeliver, dropsAndAsks, false, "held while the gate was closed"},
		{"hold and deliver, keeps a superseded command", holdAndDeliver, holdsOldest, false, "must never be sent"},
		{"hold and deliver, replays the backlog", holdAndDeliver, replaysBacklog, false, "must never be sent"},
		{"hold and deliver, sampled", holdAndDeliver, holdsLatest, true, ""},
		{"hold and deliver, replays the backlog, sampled", holdAndDeliver, replaysBacklog, true, "must never be sent"},
		{"hold and deliver, resends while closed", holdAndDeliver, resendsWhileClosed, false, "while the write gate was closed"},
		{"a rebaselining driver declared holdAndDeliver", holdAndDeliver, rebaselines, false, "held while the gate was closed"},
		{"drop and rebaseline", dropAndRebaseline, rebaselines, false, ""},
		{"drop and rebaseline, sampled", dropAndRebaseline, rebaselines, true, ""},
		{"drop and rebaseline, sends while closed", dropAndRebaseline, ignoresGate, false, "while the write gate was closed"},
		{"drop and rebaseline, replays the held command", dropAndRebaseline, holdsLatest, false, "no replay"},
		{"drop and rebaseline, replays the held command, sampled", dropAndRebaseline, holdsLatest, true, "no replay"},
		{"drop and rebaseline, commands on reopen", dropAndRebaseline, commandsOnReopen, false, "no replay"},
		{"a holding driver declared dropAndAsk", dropAndAsk, holdsLatest, false, "OutputsWanted after the gate reopened"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := subject{name: "stub", gate: tc.mode, start: func(t tb, o startOpts) *rig { return startStub(t, o, tc.drv, tc.sampled) }}
			failed, msgs := record(t, func(r tb) { c8(r, s) })
			got := strings.Join(msgs, "\n")
			switch {
			case tc.wantErr == "" && failed:
				t.Fatalf("failed: %s", got)
			case tc.wantErr != "" && !strings.Contains(got, tc.wantErr):
				t.Fatalf("failed=%v with %q, want it to fail saying %q", failed, got, tc.wantErr)
			}
		})
	}
}

type stubBehaviour int

const (
	dropsAndAsks       stubBehaviour = iota // dropAndAsk done right
	dropsSilently                           // drops, never asks
	holdsLatest                             // holdAndDeliver done right
	holdsOldest                             // keeps the first command pushed while closed
	replaysBacklog                          // sends every command pushed while closed, in order
	ignoresGate                             // sends whatever the gate says
	resendsWhileClosed                      // holdsLatest, but sends the device's value again while closed
	rebaselines                             // dropAndRebaseline done right
	commandsOnReopen                        // drops while closed, then sends the first push after reopening
)

// stubDriver has one output whose device value is device. A poll loop
// sends what is owed, one command per poll, when the gate lets it.
type stubDriver struct {
	b      stubBehaviour
	mu     sync.Mutex
	gate   func() bool
	device any
	last   any   // the last value pushed
	queue  []any // commands owed, oldest first
	closed bool  // the poll loop last found the gate closed
	rebase bool  // the next push is a baseline
	wanted bool
	polls  uint64
	sent   []any // every value the device received
}

func (d *stubDriver) SetWriteGate(g func() bool) { d.mu.Lock(); d.gate = g; d.mu.Unlock() }

func (d *stubDriver) gateOpen() bool { return d.gate == nil || d.gate() || d.b == ignoresGate }

func (d *stubDriver) ReadInputs() (nio.Values, error) { return nio.Values{"In": 1.0}, nil }

func (d *stubDriver) WriteOutputs(v nio.Values) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	x, ok := v["Out"]
	if !ok {
		return nil
	}
	open := d.gateOpen()
	if !open && d.b == rebaselines {
		d.rebase = true
		return nil
	}
	if !open && d.b == commandsOnReopen {
		return nil
	}
	if d.rebase {
		d.rebase, d.last = false, x
		return nil
	}
	if x == d.last {
		return nil
	}
	d.last = x
	switch {
	case open:
		d.queue = []any{x}
	case d.b == holdsLatest, d.b == resendsWhileClosed:
		d.queue = []any{x}
	case d.b == holdsOldest:
		if len(d.queue) == 0 {
			d.queue = []any{x}
		}
	case d.b == replaysBacklog:
		d.queue = append(d.queue, x)
	}
	return nil
}

func (d *stubDriver) OutputsWanted() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	w := d.wanted
	d.wanted = false
	return w
}

func (d *stubDriver) poll() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.polls++
	open := d.gateOpen()
	if !open {
		d.closed = true
		if d.b == resendsWhileClosed {
			d.sent = append(d.sent, d.device)
		}
		if d.b == dropsAndAsks || d.b == dropsSilently {
			d.queue = nil
		}
		return
	}
	if d.closed {
		d.closed = false
		if d.b == dropsAndAsks {
			d.wanted = true
			d.last = nil // forget: the next push is a command
		}
		if d.b == dropsSilently {
			d.last = nil
		}
	}
	if len(d.queue) > 0 {
		d.device, d.queue = d.queue[0], d.queue[1:]
		d.sent = append(d.sent, d.device)
	}
}

func startStub(t tb, o startOpts, b stubBehaviour, sampled bool) *rig {
	period := time.Millisecond
	if sampled {
		period = 10 * time.Millisecond
	}
	d := &stubDriver{b: b, device: false}
	if o.install != nil {
		o.install(d)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ctx.Err() == nil {
			d.poll()
			time.Sleep(period)
		}
	}()
	stopOnCleanup(t, cancel, func() { <-done })
	r := &rig{
		drv: d, in: "In", out: "Out",
		outVals: [2]any{false, true},
		field:   func() any { d.mu.Lock(); defer d.mu.Unlock(); return d.device },
		polls:   func() uint64 { d.mu.Lock(); defer d.mu.Unlock(); return d.polls },
	}
	if !sampled {
		r.sent = func() []any { d.mu.Lock(); defer d.mu.Unlock(); return append([]any(nil), d.sent...) }
	}
	return r
}

var _ interface{ SetWriteGate(func() bool) } = (*stubDriver)(nil)
