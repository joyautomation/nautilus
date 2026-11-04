package optommp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync/atomic"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/runtime"
)

// The write gate under redundancy (#605, and #607's hazard here): what a
// replica accepted as leader never goes out once it has lost leadership,
// and an output another leader overwrote goes back to the program's value
// when this replica leads again, changed or not.

// gateWatch is a test write gate that counts the driver's looks at it.
type gateWatch struct {
	open  atomic.Bool
	looks atomic.Int64
}

func (g *gateWatch) gate() bool { g.looks.Add(1); return g.open.Load() }

// seen waits until the driver has looked at the gate twice since the test
// moved it: the second look started after the first finished, whatever
// that one found. A driver that never looks is given two seconds and fails
// on what it sends, not here.
func (g *gateWatch) seen() {
	n := g.looks.Load()
	for deadline := time.Now().Add(2 * time.Second); g.looks.Load() < n+2 && time.Now().Before(deadline); {
		time.Sleep(2 * time.Millisecond)
	}
}

type leaderFunc func() bool

func (f leaderFunc) IsLeader() bool { return f() }

// gateProg commands a digital output and a scratchpad integer with
// constants, so both are unchanged from the first scan on: the case an
// on-change diff skips.
const gateProg = `PROGRAM Main
VAR_EXTERNAL
  EPIC_M1_DO00 : BOOL;
  EPIC_SP_I0 : DINT;
END_VAR
EPIC_M1_DO00 := TRUE;
EPIC_SP_I0 := 42;
END_PROGRAM`

// gateRuntime runs gateProg over d; coord nil is a replica that always
// leads (its driver's gate may still close: logic not healthy).
func gateRuntime(t *testing.T, d *Driver, coord runtime.Coordinator) *runtime.Runtime {
	t.Helper()
	rt, err := runtime.New(runtime.Options{
		Program:     gateProg,
		Driver:      d,
		Coordinator: coord,
		Tags: []runtime.TagDef{
			{Name: "EPIC_M1_DO00", Role: runtime.RoleOutput, Type: "BOOL"},
			{Name: "EPIC_SP_I0", Role: runtime.RoleOutput, Type: "DINT"},
		},
	})
	if err != nil {
		t.Fatalf("runtime: %v", err)
	}
	return rt
}

func do00(dev *fakeDevice) bool { return dev.U32(addrDigitalPacked+digitalModuleStride)&1 != 0 }

// commanded reports whether the unit holds gateProg's commands.
func commanded(dev *fakeDevice) bool { return do00(dev) && dev.U32(addrScratchInts) == 42 }

// otherMaster writes over both commands, as the replica that led in
// between would.
func otherMaster(dev *fakeDevice) {
	dev.SetDigital(1, 0, false)
	dev.PutU32(addrScratchInts, 5)
}

// gatedDriver runs a driver with one scratchpad integer against the fake,
// its write gate g, and dials that fail while down is true.
func gatedDriver(t *testing.T, dev *fakeDevice, g func() bool, down *atomic.Bool) *Driver {
	t.Helper()
	m := benchManifest(t, dev, ManifestOptions{ScratchInts: 1})
	drv, err := New(m, WithScanRate(5*time.Millisecond), WithDialer(func(context.Context) (net.Conn, error) {
		if down != nil && down.Load() {
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
	drv.SetWriteGate(g)
	drv.Start(context.Background())
	t.Cleanup(drv.Stop)
	return drv
}

// scansPast waits until the driver has scanned n more times than when it
// was called: every flush it had to make by then has run, since each scan
// flushes after it reads.
func scansPast(t *testing.T, drv *Driver, n uint64) {
	t.Helper()
	from := drv.Health().Reads
	eventually(t, fmt.Sprintf("%d more scans", n), func() bool { return drv.Health().Reads >= from+n })
}

// A replica that accepted commands as leader while the processor was away,
// and lost leadership before it came back, sends none of them once it is
// back. The queue drains, and the commands are asked for again: once the
// replica leads again its takeover push sends them.
func TestQueuedWritesGoNowhereAfterTheGateClosed(t *testing.T) {
	dev := newBenchDevice()
	g := &gateWatch{}
	g.open.Store(true)
	var down atomic.Bool
	drv := gatedDriver(t, dev, g.gate, &down)
	eventually(t, "first scan", func() bool { return drv.Health().Reads > 0 })

	down.Store(true)
	dev.Drop()
	eventually(t, "Stale after the drop", func() bool { return drv.Quality()["EPIC_M0_DI00"] == nio.Stale })
	rt := gateRuntime(t, drv, leaderFunc(g.open.Load))
	rt.Scan() // leader, processor away: the commands are held
	drv.mu.Lock()
	held := len(drv.pending)
	drv.mu.Unlock()
	if held != 2 {
		t.Fatalf("held %d commands while the processor was away, want 2", held)
	}

	g.open.Store(false) // leadership lost during the outage
	down.Store(false)   // the processor is back
	eventually(t, "Good after the reconnect", func() bool { return drv.Quality()["EPIC_M0_DI00"] == nio.Good })
	scansPast(t, drv, 2)

	if do00(dev) || dev.U32(addrScratchInts) != 0 {
		t.Errorf("DO00 %v, scratch int 0 = %d on the unit: a standby sent what it accepted as leader", do00(dev), dev.U32(addrScratchInts))
	}
	drv.mu.Lock()
	left := len(drv.pending)
	drv.mu.Unlock()
	if left != 0 {
		t.Errorf("%d commands still queued after the flush; the queue must drain", left)
	}

	// Leader again: the takeover push commands them.
	g.open.Store(true)
	rt.Scan()
	eventually(t, "the commands after the takeover", func() bool { return commanded(dev) })
}

// #605: an output this replica wrote, that another leader overwrote while
// this one stood by, goes back to the program's value when this replica
// leads again, though the program's value never changed. On a takeover the
// runtime pushes because it took over; on a gate that reopens without one
// (logic healthy again) it pushes because the driver asks (OutputsWanted).
func TestRegainedGateSendsTheUnchangedOutputsAgain(t *testing.T) {
	for _, tc := range []struct {
		name     string
		takeover bool
	}{{"takeover", true}, {"gate reopens while leading", false}} {
		t.Run(tc.name, func(t *testing.T) {
			dev := newBenchDevice()
			g := &gateWatch{}
			g.open.Store(true)
			drv := gatedDriver(t, dev, g.gate, nil)
			eventually(t, "first scan", func() bool { return drv.Health().Reads > 0 })
			var coord runtime.Coordinator
			if tc.takeover {
				coord = leaderFunc(g.open.Load)
			}
			rt := gateRuntime(t, drv, coord)
			rt.Scan()
			eventually(t, "the leader's commands", func() bool { return commanded(dev) })

			g.open.Store(false)
			g.seen()
			rt.Scan()
			otherMaster(dev)
			g.open.Store(true)
			g.seen()
			rt.Scan()
			rt.Scan()
			eventually(t, "the unchanged commands back", func() bool { return commanded(dev) })
		})
	}
}

// A gate that closes partway through a flush stops it: the write after the
// one in flight when it closed is dropped, not sent.
func TestGateClosingMidFlushStopsTheFlush(t *testing.T) {
	dev := newBenchDevice()
	// Open until the flush's first write (the digital outputs) has landed.
	drv := gatedDriver(t, dev, func() bool { return !do00(dev) }, nil)
	eventually(t, "first scan", func() bool { return drv.Health().Reads > 0 })
	_ = drv.WriteOutputs(nio.Values{"EPIC_M1_DO00": true, "EPIC_SP_I0": int64(42)})
	eventually(t, "DO00 on at the unit", func() bool { return do00(dev) })
	scansPast(t, drv, 2)
	if v := dev.U32(addrScratchInts); v != 0 {
		t.Errorf("scratch int 0 = %d: the flush went on after the gate closed", v)
	}
	if !drv.OutputsWanted() {
		t.Error("a dropped command was not asked for")
	}
}

// #606: a replica that started as a standby commands every output on its
// first takeover, with the program's value: the driver has no start-up
// baseline, so the takeover push is not taken for one.
func TestStandbyFirstTakeoverCommands(t *testing.T) {
	dev := newBenchDevice()
	otherMaster(dev) // what the old leader commanded
	g := &gateWatch{}
	drv := gatedDriver(t, dev, g.gate, nil) // gate closed: a standby
	eventually(t, "first scan", func() bool { return drv.Health().Reads > 0 })
	g.seen()
	rt := gateRuntime(t, drv, leaderFunc(g.open.Load))
	rt.Scan() // standing by: no scan
	g.open.Store(true)
	rt.Scan() // the first scan as leader: a takeover
	eventually(t, "every output commanded on the first takeover", func() bool { return commanded(dev) })
}
