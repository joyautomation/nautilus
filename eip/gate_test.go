package eip

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/eip/logixserver"
	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/runtime"
)

// The write gate under redundancy (#607, #605): what a replica queued as
// leader never goes out once it has lost leadership, and an output another
// leader overwrote goes back to the program's value when this replica
// leads again, changed or not.

// gateWatch is a test write gate that counts the driver's looks at it.
type gateWatch struct {
	open  atomic.Bool
	looks atomic.Int64
}

func (g *gateWatch) gate() bool { g.looks.Add(1); return g.open.Load() }

// seen waits until the driver has looked at the gate twice since the test
// moved it. While no command is handed over, the serve loop is the only
// caller, so the second look started after the first finished, whatever
// that one found. A driver that never looks is given two seconds and fails
// on what it sends, not here.
func (g *gateWatch) seen() {
	n := g.looks.Load()
	for deadline := time.Now().Add(2 * time.Second); g.looks.Load() < n+2 && time.Now().Before(deadline); {
		time.Sleep(5 * time.Millisecond)
	}
}

type leaderFunc func() bool

func (f leaderFunc) IsLeader() bool { return f() }

// gateProg commands both writable bindings with constants, so every output
// is unchanged from its first scan on: the case an on-change diff skips.
const gateProg = `
TYPE
  Header_Type : STRUCT
    Displacement : REAL;
    Valid : BOOL;
  END_STRUCT;
  Plt_Type : STRUCT
    Header : Header_Type;
    Count : DINT;
  END_STRUCT;
END_TYPE

PROGRAM Main
VAR_EXTERNAL
  SpeedCmd : REAL;
  TRSCmd : Plt_Type;
END_VAR
VAR
  c : Plt_Type;
END_VAR
c.Header.Displacement := 9.75;
c.Header.Valid := TRUE;
c.Count := 99;
SpeedCmd := 77.0;
TRSCmd := c;
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
			{Name: "SpeedCmd", Role: runtime.RoleOutput, Type: "REAL"},
			{Name: "TRSCmd", Role: runtime.RoleOutput, Type: "Plt_Type"},
		},
	})
	if err != nil {
		t.Fatalf("runtime: %v", err)
	}
	return rt
}

func storeVal(store *logixserver.TagStore, path string) any {
	_, v, _ := store.Resolve(path)
	return v
}

// commanded reports whether the controller holds gateProg's commands.
func commanded(store *logixserver.TagStore) bool {
	return storeVal(store, "SpeedCmd") == 77.0 &&
		storeVal(store, "TRSCmd.Header.Displacement") == 9.75 &&
		storeVal(store, "TRSCmd.Header.Valid") == true &&
		storeVal(store, "TRSCmd.Count") == 99.0
}

// otherMaster writes over every commanded value, as the replica that led in
// between would.
func otherMaster(store *logixserver.TagStore) {
	store.UpdateValue("SpeedCmd", 5.0)
	store.UpdateValue("TRSCmd.Header.Displacement", 1.5)
	store.UpdateValue("TRSCmd.Header.Valid", false)
	store.UpdateValue("TRSCmd.Count", 5.0)
}

func startGated(t *testing.T, port int, g *gateWatch) *Driver {
	t.Helper()
	d, err := New("127.0.0.1", testManifest(), WithPort(port), WithScanRate(20*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	d.SetWriteGate(g.gate)
	d.Start(context.Background())
	t.Cleanup(d.Stop)
	return d
}

// pollsPast waits until the serve loop has polled n more times than when
// it was called: every flush the loop had to make by then has run, since
// the loop flushes before it polls.
func pollsPast(t *testing.T, d *Driver, n uint64) {
	t.Helper()
	from := d.Health().Polls
	waitFor(t, fmt.Sprintf("%d more polls", n), func() bool { return d.Health().Polls >= from+n })
}

// switchProxy forwards to target while up is true; while it is false it
// closes every connection it accepts, a controller that is down.
func switchProxy(t *testing.T, target string) (port int, up *atomic.Bool) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	up = &atomic.Bool{}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			if !up.Load() {
				_ = c.Close()
				continue
			}
			s, err := net.Dial("tcp", target)
			if err != nil {
				_ = c.Close()
				continue
			}
			go func() { _, _ = io.Copy(c, s); _ = c.Close() }()
			go func() { _, _ = io.Copy(s, c); _ = s.Close() }()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port, up
}

// #607: a replica that queued commands as leader while the controller was
// down, and lost leadership before it came back, sends none of them once
// it is back. The queue drains, and the commands are asked for again: once
// the replica leads again its takeover push sends them.
func TestQueuedWritesGoNowhereAfterTheGateClosed(t *testing.T) {
	host, emPort, store := startEmulator(t)
	port, up := switchProxy(t, fmt.Sprintf("%s:%d", host, emPort))
	g := &gateWatch{}
	g.open.Store(true)
	d := startGated(t, port, g)
	waitFor(t, "a failed connect", func() bool { return d.Health().LastError != "" })

	rt := gateRuntime(t, d, leaderFunc(g.open.Load))
	rt.Scan() // leader, controller down: the commands are queued
	d.wmu.Lock()
	queued := len(d.pending)
	d.wmu.Unlock()
	if queued != 2 {
		t.Fatalf("queued %d commands while the controller was down, want 2", queued)
	}

	g.open.Store(false) // leadership lost during the outage
	up.Store(true)      // the controller is back
	waitFor(t, "primed", func() bool { return d.Health().Primed })
	pollsPast(t, d, 2)

	if v := storeVal(store, "SpeedCmd"); v != 0.0 {
		t.Errorf("SpeedCmd = %v on the controller: a standby sent what it queued as leader", v)
	}
	if v := storeVal(store, "TRSCmd.Count"); v != 0.0 {
		t.Errorf("TRSCmd.Count = %v on the controller: a standby sent what it queued as leader", v)
	}
	d.wmu.Lock()
	left := len(d.pending)
	d.wmu.Unlock()
	if left != 0 {
		t.Errorf("%d commands still queued after the flush; the queue must drain", left)
	}

	// Leader again: the takeover push commands them, struct members too.
	g.open.Store(true)
	rt.Scan()
	waitFor(t, "the commands after the takeover", func() bool { return commanded(store) })
}

// #605: an output this replica wrote, that another leader overwrote while
// this one stood by, goes back to the program's value when this replica
// leads again, though the program's value never changed. On a takeover the
// runtime pushes because it took over; on a gate that reopens without one
// (logic healthy again) it pushes because the driver asks (OutputsWanted).
// The struct goes out in full, not only the members that changed.
func TestRegainedGateSendsTheUnchangedOutputsAgain(t *testing.T) {
	for _, tc := range []struct {
		name     string
		takeover bool
	}{{"takeover", true}, {"gate reopens while leading", false}} {
		t.Run(tc.name, func(t *testing.T) {
			_, port, store := startEmulator(t)
			g := &gateWatch{}
			g.open.Store(true)
			d := startGated(t, port, g)
			waitFor(t, "primed", func() bool { return d.Health().Primed })
			var coord runtime.Coordinator
			if tc.takeover {
				coord = leaderFunc(g.open.Load)
			}
			rt := gateRuntime(t, d, coord)
			rt.Scan()
			waitFor(t, "the leader's commands", func() bool { return commanded(store) })

			g.open.Store(false)
			g.seen()
			rt.Scan()
			otherMaster(store)
			g.open.Store(true)
			g.seen()
			rt.Scan()
			rt.Scan()
			waitFor(t, "the unchanged commands back", func() bool { return commanded(store) })
		})
	}
}

// #605: a gate that closes partway through a flush stops it. The tag being
// written when the gate closed and every one after it are dropped, not
// sent, and so is the rest of a struct whose first member went out.
func TestGateClosingMidFlushStopsTheFlush(t *testing.T) {
	cmd := ir.Value{Kind: ir.TypeStruct, Fld: []ir.Value{
		{Kind: ir.TypeStruct, Fld: []ir.Value{ir.RealVal(9.75), ir.BoolVal(true)}},
		ir.IntVal(99),
	}}
	t.Run("between tags", func(t *testing.T) {
		_, port, store := startEmulator(t)
		// Open until the first write (SpeedCmd, in name order) has landed.
		g := func() bool { return storeVal(store, "SpeedCmd") != 7.25 }
		d, err := New("127.0.0.1", testManifest(), WithPort(port), WithScanRate(20*time.Millisecond))
		if err != nil {
			t.Fatal(err)
		}
		d.SetWriteGate(g)
		d.Start(context.Background())
		t.Cleanup(d.Stop)
		waitFor(t, "primed", func() bool { return d.Health().Primed })
		_ = d.WriteOutputs(nio.Values{"SpeedCmd": 7.25, "TRSCmd": cmd})
		waitFor(t, "SpeedCmd", func() bool { return storeVal(store, "SpeedCmd") == 7.25 })
		pollsPast(t, d, 2)
		if v := storeVal(store, "TRSCmd.Count"); v != 0.0 {
			t.Errorf("TRSCmd.Count = %v: the flush went on after the gate closed", v)
		}
		if !d.OutputsWanted() {
			t.Error("a dropped command was not asked for")
		}
	})
	t.Run("between struct members", func(t *testing.T) {
		_, port, store := startEmulator(t)
		// Open until the struct's first member has landed.
		g := func() bool { return storeVal(store, "TRSCmd.Header.Displacement") != 9.75 }
		d, err := New("127.0.0.1", testManifest(), WithPort(port), WithScanRate(20*time.Millisecond))
		if err != nil {
			t.Fatal(err)
		}
		d.SetWriteGate(g)
		d.Start(context.Background())
		t.Cleanup(d.Stop)
		waitFor(t, "primed", func() bool { return d.Health().Primed })
		_ = d.WriteOutputs(nio.Values{"TRSCmd": cmd})
		waitFor(t, "the first member", func() bool { return storeVal(store, "TRSCmd.Header.Displacement") == 9.75 })
		pollsPast(t, d, 2)
		if v, c := storeVal(store, "TRSCmd.Header.Valid"), storeVal(store, "TRSCmd.Count"); v != false || c != 0.0 {
			t.Errorf("TRSCmd.Header.Valid = %v, TRSCmd.Count = %v: the struct write went on after the gate closed", v, c)
		}
	})
}

// #606: a replica that started as a standby commands every output on its
// first takeover, with the program's value: the driver has no start-up
// baseline, so the takeover push is not taken for one.
func TestStandbyFirstTakeoverCommands(t *testing.T) {
	_, port, store := startEmulator(t)
	otherMaster(store) // what the old leader commanded
	g := &gateWatch{}
	d := startGated(t, port, g) // gate closed: a standby
	waitFor(t, "primed", func() bool { return d.Health().Primed })
	g.seen()
	rt := gateRuntime(t, d, leaderFunc(g.open.Load))
	rt.Scan() // standing by: no scan
	g.open.Store(true)
	rt.Scan() // the first scan as leader: a takeover
	waitFor(t, "every output commanded on the first takeover", func() bool { return commanded(store) })
}
