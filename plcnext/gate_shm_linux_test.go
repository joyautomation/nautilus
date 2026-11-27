package plcnext

import (
	"context"
	"encoding/binary"
	"log/slog"
	"math/bits"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
)

// With the write gate closed nothing is written to the page, the
// every-scan heartbeat included, so the shim's output watchdog drives the
// frame to zero: the shim's own safe state, logged as the gate's doing and
// not as a driver stall. Reopened, the frame stays at zero until the
// runtime's push, then it is the whole of it (#863).
func TestWriteGateShm(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nautilus-plcnext-io")
	shim := startFakeShim(t, path)
	noBC := strings.Replace(benchManifest, "bus-conductor: BcComponent1", `bus-conductor: ""`, 1) // never dials
	m, err := ParseManifest([]byte("transport: shm\nshm: " + path + "\noutput-watchdog: 100ms\n" + noBC))
	if err != nil {
		t.Fatal(err)
	}
	logs := &logRec{}
	d, err := New(m, WithScanRate(5*time.Millisecond), WithLogger(slog.New(logs)))
	if err != nil {
		t.Fatal(err)
	}
	var open atomic.Bool
	open.Store(true)
	d.SetWriteGate(open.Load)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.Start(ctx)
	defer d.Stop()

	set := func() int { return bits.OnesCount8(shim.out(0)) + bits.OnesCount8(shim.out(1)) } // DO_00..DO_15
	gen := func() uint64 { return binary.LittleEndian.Uint64(shim.mem[shmOffOutGen:]) }
	if err := d.WriteOutputs(allOutputs(true, false)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the output never reached the frame", func() bool { return set() == 1 })

	open.Store(false)
	eventually(t, "the gate never showed closed", func() bool { return d.Health().GateClosed })
	g := gen()
	if err := d.WriteOutputs(nio.Values{"DO_15": true}); err != nil {
		t.Fatal(err)
	}
	eventuallyWithin(t, 2*time.Second, "the shim's watchdog never zeroed the frame of a gated driver", func() bool { return set() == 0 })
	if h := d.Health(); h.GateHolds {
		t.Error("shm with a watchdog: the closed gate must not be shown as holding the outputs")
	}
	if gen() != g {
		t.Fatalf("the page's output generation went %d → %d with the gate closed: the driver kept writing", g, gen())
	}

	open.Store(true)
	eventually(t, "the gate never showed open", func() bool { return !d.Health().GateClosed })
	time.Sleep(50 * time.Millisecond)
	if gen() != g || set() != 0 {
		t.Fatalf("reopened: the previous term's frame went out before the runtime's push (gen %d → %d, %d bits set)", g, gen(), set())
	}
	if !d.OutputsWanted() {
		t.Fatal("a reopened gate must ask for every output")
	}
	if err := d.WriteOutputs(allOutputs(true, true)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the runtime's push after a reopen never reached the frame", func() bool { return set() == 2 })

	if n := d.Health().WatchdogTrips; n != 0 {
		t.Errorf("__WatchdogTrips = %d: a zeroing the gate chose is not a trip", n)
	}
	if lv, ok := logs.level("the write gate is closed, so the shim drove"); !ok || lv != slog.LevelInfo {
		t.Errorf("a gate-caused watchdog trip must be logged as the gate's doing, at info (found=%v level=%v)", ok, lv)
	}
	for _, stall := range []string{"outputs were older than its watchdog", "went longer than the shim's watchdog"} {
		if _, ok := logs.level(stall); ok {
			t.Errorf("a gate-caused trip was logged as a driver stall (%q)", stall)
		}
	}
}

// output-watchdog: 0 turns the shim's watchdog off, so a closed gate holds
// the outputs as on gRPC: an error in the log and "outputs held" (#863).
func TestWriteGateShmWithoutWatchdog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nautilus-plcnext-io")
	shim := startFakeShim(t, path)
	noBC := strings.Replace(benchManifest, "bus-conductor: BcComponent1", `bus-conductor: ""`, 1)
	m, err := ParseManifest([]byte("transport: shm\nshm: " + path + "\noutput-watchdog: 0s\n" + noBC))
	if err != nil {
		t.Fatal(err)
	}
	logs := &logRec{}
	d, err := New(m, WithScanRate(5*time.Millisecond), WithLogger(slog.New(logs)))
	if err != nil {
		t.Fatal(err)
	}
	var open atomic.Bool
	open.Store(true)
	d.SetWriteGate(open.Load)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.Start(ctx)
	defer d.Stop()

	if err := d.WriteOutputs(allOutputs(true, false)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the output never reached the frame", func() bool { return shim.out(0)|shim.out(1) != 0 })
	open.Store(false)
	eventually(t, "the gate never showed closed", func() bool { return d.Health().GateClosed })
	if !d.Health().GateHolds {
		t.Fatal("shm with output-watchdog 0: the closed gate must be shown as holding the outputs")
	}
	// The driver marks the gate shut before it logs why, so the line can
	// trail what Health shows (#1120): wait for it.
	eventually(t, "the closed gate was never logged", func() bool { _, ok := logs.level("outputs HOLD"); return ok })
	if lv, ok := logs.level("outputs HOLD"); !ok || lv != slog.LevelError {
		t.Fatalf("shm with output-watchdog 0: a closed gate must be logged as an error (found=%v level=%v)", ok, lv)
	}
	time.Sleep(300 * time.Millisecond)
	if shim.out(0)|shim.out(1) == 0 {
		t.Fatal("with no watchdog the outputs hold; the frame went to zero")
	}
}
