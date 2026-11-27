package optommp

import (
	"context"
	"errors"
	"math"
	"net"
	"sync/atomic"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/runtime"
)

// The start-up baseline decides each output against what the unit holds, on
// a push computed from a scan that read it live, never on the first scan's
// push, which is computed from init values before anything was read
// (#1123, as modbus, opcua and eip).

const incIntProg = "PROGRAM Main\nVAR_EXTERNAL EPIC_SP_I1 : DINT; EPIC_SP_I0 : DINT; END_VAR\nEPIC_SP_I0 := EPIC_SP_I1 + 1;\nEND_PROGRAM"

// baselineRig: a runtime over an optommp driver, scanned by hand. down: the
// processor is unreachable until up is set.
func baselineRig(t *testing.T, dev *fakeDevice, down bool, opts ManifestOptions, prog string, tags []runtime.TagDef) (*Driver, *runtime.Runtime, func(n int), *atomic.Bool) {
	t.Helper()
	m := benchManifest(t, dev, opts)
	up := &atomic.Bool{}
	up.Store(!down)
	drv, err := New(m, WithScanRate(5*time.Millisecond), WithDialer(func(context.Context) (net.Conn, error) {
		if !up.Load() {
			return nil, errors.New("unreachable")
		}
		a, b := net.Pipe()
		go dev.Serve(b)
		return a, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	drv.retryMin = 10 * time.Millisecond
	drv.chassis = 20 * time.Millisecond
	rt, err := runtime.New(runtime.Options{Program: prog, Driver: drv, Tags: tags})
	if err != nil {
		t.Fatal(err)
	}
	drv.Start(context.Background())
	t.Cleanup(drv.Stop)
	return drv, rt, func(n int) {
		for i := 0; i < n; i++ {
			rt.Scan()
			time.Sleep(6 * time.Millisecond)
		}
	}, up
}

func incTags() []runtime.TagDef {
	return []runtime.TagDef{runtime.Input("EPIC_SP_I1", runtime.Init(int64(0))), runtime.Output("EPIC_SP_I0", runtime.Init(int64(0)))}
}

func TestStartupNeverDrivesTheInitDerivedValue(t *testing.T) {
	for _, tc := range []struct {
		name string
		held uint32
		want int
	}{
		{"holds the live value", 5, 0},
		{"holds another value", 9, 1},
	} {
		for _, down := range []bool{false, true} {
			name := tc.name + "/up at start"
			if down {
				name = tc.name + "/down 30 scans then up"
			}
			t.Run(name, func(t *testing.T) {
				dev := newBenchDevice()
				dev.PutU32(addrScratchInts+4, 4) // the input
				dev.PutU32(addrScratchInts, tc.held)
				_, _, scan, up := baselineRig(t, dev, down, ManifestOptions{ScratchInts: 1, ScratchIntsIn: 1}, incIntProg, incTags())
				if down {
					scan(30)
					up.Store(true)
				}
				scan(150)
				if got := dev.WritesTo(addrScratchInts); got != tc.want {
					t.Fatalf("write requests to the output = %d, want %d (the init-derived 1 must never go out)", got, tc.want)
				}
				if tc.want == 1 && dev.U32(addrScratchInts) != 5 {
					t.Fatalf("the unit holds %d, want the live-derived 5", dev.U32(addrScratchInts))
				}
			})
		}
	}
}

// A REAL the unit holds as the same 32 bits is equal.
func TestStartupComparesInTheUnitsEncoding(t *testing.T) {
	dev := newBenchDevice()
	dev.PutU32(addrScratchFloats, math.Float32bits(0.1))
	_, _, scan, _ := baselineRig(t, dev, false, ManifestOptions{ScratchFloats: 1},
		"PROGRAM Main\nVAR_EXTERNAL EPIC_SP_F0 : REAL; END_VAR\nEPIC_SP_F0 := 0.1;\nEND_PROGRAM",
		[]runtime.TagDef{runtime.Output("EPIC_SP_F0", runtime.Init(0.0))})
	scan(80)
	if got := dev.WritesTo(addrScratchFloats); got != 0 {
		t.Fatalf("write requests = %d, want none: the unit holds 0.1 already", got)
	}
}

// A driver that began as a standby has no baseline: when its gate opens,
// every output is commanded, equal or not (#606).
func TestStartupBaselineIsNotArmedForAStandby(t *testing.T) {
	dev := newBenchDevice()
	dev.PutU32(addrScratchInts, 42) // already what the program commands
	g := &gateWatch{}
	m := benchManifest(t, dev, ManifestOptions{ScratchInts: 1})
	drv, err := New(m, WithScanRate(5*time.Millisecond), WithDialer(func(context.Context) (net.Conn, error) {
		a, b := net.Pipe()
		go dev.Serve(b)
		return a, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	drv.retryMin = 10 * time.Millisecond
	drv.chassis = 20 * time.Millisecond
	drv.SetWriteGate(g.gate)
	drv.gateOpen() // seen shut: a standby
	drv.Start(context.Background())
	t.Cleanup(drv.Stop)
	rt := gateRuntime(t, drv, nil)
	eventually(t, "first scan", func() bool { return drv.Health().Reads > 0 })
	rt.Scan()
	g.open.Store(true)
	for i := 0; i < 40; i++ {
		rt.Scan()
		time.Sleep(6 * time.Millisecond)
	}
	eventually(t, "the takeover commands the output", func() bool { return dev.WritesTo(addrScratchInts) >= 1 })
}

// A driver swapped into a running set: disarmed, its first push is a
// command; held, it is recorded and never sent.
func TestSwappedDriverBaseline(t *testing.T) {
	for _, tc := range []struct {
		name string
		arm  func(*Driver)
		held uint32 // what the unit holds; the push is 7
		want int
	}{
		{"disarmed commands even what the unit holds", (*Driver).DisarmBaseline, 7, 1},
		{"held records, though the unit differs", (*Driver).HoldBaseline, 3, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dev := newBenchDevice()
			dev.PutU32(addrScratchInts, tc.held)
			m := benchManifest(t, dev, ManifestOptions{ScratchInts: 1})
			drv := startDriver(t, dev, m)
			tc.arm(drv)
			eventually(t, "first scan", func() bool { return drv.Health().Reads > 0 })
			if err := drv.WriteOutputs(map[string]any{"EPIC_SP_I0": int64(7)}); err != nil {
				t.Fatal(err)
			}
			scansPast(t, drv, 4)
			if got := dev.WritesTo(addrScratchInts); got != tc.want {
				t.Fatalf("writes = %d, want %d", got, tc.want)
			}
		})
	}
}

// Start-up with the processor away and the gate closing before it is back:
// the first push waits undecided, nothing is sent while the gate is closed,
// and the takeover commands everything (the gate test's case at start-up).
func TestStartupAwayThenGateClosesSendsNothing(t *testing.T) {
	dev := newBenchDevice()
	g := &gateWatch{}
	g.open.Store(true)
	var down atomic.Bool
	down.Store(true)
	drv := gatedDriver(t, dev, g.gate, &down)
	rt := gateRuntime(t, drv, leaderFunc(g.open.Load))
	// The first scan waits for the unit's first attempt (#786): the
	// processor is away, so that is its first failed connect.
	eventually(t, "the first attempt to fail", drv.InputsReady)
	rt.Scan() // armed baseline, processor away: waits undecided
	drv.mu.Lock()
	undecided, pending := len(drv.undecided), len(drv.pending)
	drv.mu.Unlock()
	if undecided != 2 || pending != 0 {
		t.Fatalf("undecided=%d pending=%d, want 2 and 0", undecided, pending)
	}
	g.open.Store(false)
	down.Store(false)
	eventually(t, "Good after connect", func() bool { return drv.Quality()["EPIC_M0_DI00"] == nio.Good })
	scansPast(t, drv, 3)
	for i := 0; i < 5; i++ {
		rt.Scan()
	}
	scansPast(t, drv, 2)
	if do00(dev) || dev.U32(addrScratchInts) != 0 {
		t.Fatalf("a standby sent a start-up command")
	}
	g.open.Store(true)
	rt.Scan()
	eventually(t, "the commands after the takeover", func() bool { return commanded(dev) })
}
