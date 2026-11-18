package plcnext

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/plcnext/plcnexttest"

	nio "github.com/joyautomation/nautilus/io"
)

// logRec keeps every record the driver logs, for asserting levels.
type logRec struct {
	mu  sync.Mutex
	got []slog.Record
}

func (l *logRec) Enabled(context.Context, slog.Level) bool { return true }
func (l *logRec) Handle(_ context.Context, r slog.Record) error {
	l.mu.Lock()
	l.got = append(l.got, r)
	l.mu.Unlock()
	return nil
}
func (l *logRec) WithAttrs([]slog.Attr) slog.Handler { return l }
func (l *logRec) WithGroup(string) slog.Handler      { return l }

// level is the level of the first record whose message has sub, and
// whether there was one.
func (l *logRec) level(sub string) (slog.Level, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, r := range l.got {
		if strings.Contains(r.Message, sub) {
			return r.Level, true
		}
	}
	return 0, false
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal(what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// allOutputs is what the runtime hands WriteOutputs when the driver asks
// for every output.
func allOutputs(do00, do15 bool) nio.Values {
	return nio.Values{"DO_00": do00, "DO_15": do15, "AO_00": 0.0}
}

// With the write gate closed nothing leaves the driver over gRPC: no output
// span, and no BusConductor configure/start (commands too), while outputs
// keep changing; the hold is logged as an error and shown in the status.
// Opened, the bus is conducted. A driver the runtime never pushed to
// writes its frame as it stands; one it has pushed to writes nothing until
// the runtime answers OutputsWanted, then the whole frame (#863).
func TestWriteGateGRPC(t *testing.T) {
	f := plcnexttest.New()
	addr := SocketTarget(f.Listen(t))
	m, err := ParseManifest([]byte(benchManifest))
	if err != nil {
		t.Fatal(err)
	}
	logs := &logRec{}
	d, err := New(m, WithScanRate(5*time.Millisecond), WithLogger(slog.New(logs)),
		WithDialer(func(ctx context.Context) (dataAccess, error) { return Dial(ctx, addr) }))
	if err != nil {
		t.Fatal(err)
	}
	var open atomic.Bool
	if n := nio.SetPlainGate(d, open.Load); n != 1 {
		t.Fatalf("io.SetPlainGate installed %d gates, want the plcnext driver's", n)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.Start(ctx)
	defer d.Stop()
	spans := func() int { return strings.Count(wrote(f), DefaultOutputs) }

	// Closed from the start: the bus is only read.
	waitFor(t, "never connected", func() bool { return d.Health().Connected })
	time.Sleep(50 * time.Millisecond)
	if w := wrote(f); w != "" {
		t.Fatalf("with the gate closed the driver wrote %q", w)
	}
	if h := d.Health(); !h.GateClosed || !h.GateHolds {
		t.Fatalf("closed gate on gRPC: GateClosed=%v GateHolds=%v, want both", h.GateClosed, h.GateHolds)
	}
	if lv, ok := logs.level("outputs HOLD"); !ok || lv != slog.LevelError {
		t.Fatalf("a closed gate on gRPC must be logged as an error (found=%v level=%v)", ok, lv)
	}

	// Opened, never pushed to: conducted, then the frame as it stands.
	open.Store(true)
	waitFor(t, "gate open, but the bus was not conducted and the frame written", func() bool {
		return strings.Contains(wrote(f), "START_IO_REQ=true") && spans() > 0
	})
	if w := wrote(f); strings.Index(w, DefaultOutputs) < strings.Index(w, "START_IO_REQ=true") {
		t.Fatalf("an output went out before the bus was conducted: %q", w)
	}
	if !d.OutputsWanted() || d.OutputsWanted() {
		t.Fatal("a reopened gate must ask for every output, once")
	}

	// The runtime pushes; then the gate closes and outputs keep changing.
	if err := d.WriteOutputs(allOutputs(true, false)); err != nil {
		t.Fatal(err)
	}
	n := spans() + 1
	waitFor(t, "an open gate's push was not written", func() bool { return spans() >= n })
	open.Store(false)
	waitFor(t, "the gate never showed closed", func() bool { return d.Health().GateClosed })
	n = spans()
	for _, v := range []bool{true, false, true} {
		if err := d.WriteOutputs(nio.Values{"DO_15": v}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if spans() != n {
		t.Fatalf("with the gate closed the output span went out: %q", wrote(f))
	}

	// Reopened: nothing until the runtime's push, then the whole frame.
	open.Store(true)
	waitFor(t, "the gate never showed open", func() bool { return !d.Health().GateClosed })
	time.Sleep(50 * time.Millisecond)
	if spans() != n {
		t.Fatalf("reopened: the previous term's frame went out before the runtime's push: %q", wrote(f))
	}
	if !d.OutputsWanted() {
		t.Fatal("a reopened gate must ask for every output")
	}
	if err := d.WriteOutputs(allOutputs(false, true)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the runtime's push after a reopen was not written", func() bool { return spans() > n })
}

// The BusConductor handshake writes several requests over up to tens of
// seconds; the gate is looked at before each, so one that closes partway
// stops the rest, and the handshake is finished once it opens again (and
// again, if it closes partway through that too). The state reported
// meanwhile is read, not assumed (#863).
func TestWriteGateClosesMidHandshake(t *testing.T) {
	for _, c := range []struct {
		name, closeAfter, never string
		again                   string // closes once more after this, in the reopen's handshake
		runNone                 bool   // run-port: none: Running is what was requested
	}{
		{name: "before CONFIG_REQ", closeAfter: "CONFIG_MUST_MATCH=false", never: "CONFIG_REQ"},
		{name: "in the edge's hold", closeAfter: "CONFIG_REQ=false", never: "CONFIG_REQ=true"},
		{name: "before START_IO_REQ", closeAfter: "CONFIG_REQ=true", never: "START_IO_REQ"},
		{name: "no RUN port", closeAfter: "CONFIG_REQ=true", never: "START_IO_REQ", runNone: true},
		{name: "closes again on reopen", closeAfter: "CONFIG_REQ=false", never: "CONFIG_REQ=true", again: "CONFIG_REQ=true"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := plcnexttest.New()
			addr := SocketTarget(f.Listen(t))
			man := benchManifest
			if c.runNone {
				man = "run-port: none\n" + man
			}
			m, err := ParseManifest([]byte(man))
			if err != nil {
				t.Fatal(err)
			}
			d, err := New(m, WithScanRate(5*time.Millisecond), WithDialer(func(ctx context.Context) (dataAccess, error) { return Dial(ctx, addr) }))
			if err != nil {
				t.Fatal(err)
			}
			// 0 open until closeAfter is written, 1 closed, 2 open until
			// again is written, 3 closed, 4 open.
			var phase atomic.Int32
			d.SetWriteGate(func() bool {
				switch phase.Load() {
				case 0:
					if strings.Contains(wrote(f), c.closeAfter) {
						phase.CompareAndSwap(0, 1)
						return false
					}
					return true
				case 2:
					if c.again != "" && strings.Contains(wrote(f), c.again) {
						phase.CompareAndSwap(2, 3)
						return false
					}
					return true
				case 4:
					return true
				}
				return false
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			d.Start(ctx)
			defer d.Stop()

			waitFor(t, "never connected", func() bool { return d.Health().Connected })
			time.Sleep(100 * time.Millisecond)
			if w := wrote(f); strings.Contains(w, c.never) {
				t.Fatalf("the gate closed after %s, but %s went out: %q", c.closeAfter, c.never, w)
			}
			h := d.Health()
			if h.Running || h.Modules != 4 {
				t.Fatalf("deferred handshake: Running=%v Modules=%d, want the bus as read (stopped, 4 modules)", h.Running, h.Modules)
			}
			if q := d.Quality()["DI_00"]; c.runNone && q != nio.Bad {
				t.Fatalf("no RUN port, START_IO_REQ deferred: inputs %v, want Bad (the bus was not started)", q)
			}
			if want := c.closeAfter == "CONFIG_REQ=true"; h.Configured != want {
				t.Fatalf("deferred handshake: Configured=%v, want %v as read", h.Configured, want)
			}

			f.SetModules(5) // a module added meanwhile
			phase.Store(2)
			if c.again != "" {
				waitFor(t, "the gate never closed again", func() bool { return phase.Load() == 3 })
				time.Sleep(100 * time.Millisecond)
				if w := wrote(f); strings.Contains(w, "START_IO_REQ") {
					t.Fatalf("closed again after %s, but START_IO_REQ went out: %q", c.again, w)
				}
				phase.Store(4)
			}
			waitFor(t, "gate open, but the handshake was not finished", func() bool {
				return strings.Contains(wrote(f), "START_IO_REQ=true") && d.Health().Running
			})
			if n := d.Health().Modules; n != 5 {
				t.Fatalf("after the handshake Modules=%d, want the 5 read then", n)
			}
		})
	}
}

// recImage is a route that records writes.
type recImage struct{ writes int }

func (r *recImage) read(_ context.Context, n int) (reading, error) {
	return reading{img: make([]byte, n), q: nio.Good, running: true}, nil
}
func (r *recImage) write(context.Context, []byte) error { r.writes++; return nil }
func (*recImage) everyScan() bool                       { return false }

// While the handshake is still to finish nothing is written, even with
// the gate open: the gate can open between the session's look (conduct)
// and the poll's, and the bus is conducted first (#863).
func TestPollHoldsOutputsUntilConducted(t *testing.T) {
	m, err := ParseManifest([]byte(benchManifest))
	if err != nil {
		t.Fatal(err)
	}
	d, err := New(m)
	if err != nil {
		t.Fatal(err)
	}
	d.SetWriteGate(func() bool { return true })
	if err := d.WriteOutputs(nio.Values{"DO_00": true}); err != nil {
		t.Fatal(err)
	}
	im := &recImage{}
	if err := d.poll(context.Background(), im, true); err != nil {
		t.Fatal(err)
	}
	if im.writes != 0 {
		t.Fatal("an output went out before the BusConductor handshake finished")
	}
	if err := d.poll(context.Background(), im, false); err != nil {
		t.Fatal(err)
	}
	if im.writes != 1 {
		t.Fatalf("conducted, the changed output was written %d times, want 1", im.writes)
	}
}
