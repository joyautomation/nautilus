package eip

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/eip/logixserver"
	"github.com/joyautomation/nautilus/runtime"
)

// The start-up baseline decides each output against what the controller
// holds, on a push computed from a live read of it, never on the first
// scan's push, which is computed from init values before anything was read
// (#1123, as modbus and opcua).

const incSpeedProg = "PROGRAM Main\nVAR_EXTERNAL Speed : REAL; SpeedCmd : REAL; END_VAR\nSpeedCmd := Speed + 1.0;\nEND_PROGRAM"

// baselineRig: a runtime over an eip driver, scanned by hand. up nil: the
// controller is reachable from the start; else it is down until up is set.
// noPoll makes the outputs unreadable (class none).
func tags(prog string) []runtime.TagDef {
	if prog == incStructProg {
		return []runtime.TagDef{runtime.Input("Speed", runtime.Init(0.0)),
			{Name: "TRSCmd", Role: runtime.RoleOutput, Type: "Plt_Type"}}
	}
	out := []runtime.TagDef{runtime.Output("SpeedCmd", runtime.Init(0.0))}
	if strings.Contains(prog, "Speed :") {
		out = append(out, runtime.Input("Speed", runtime.Init(0.0)))
	}
	return out
}

func baselineRig(t *testing.T, prog string, noPoll bool, down bool, setup func(store *logixserver.TagStore)) (*Driver, func(n int), *logixserver.Server, *atomic.Bool) {
	t.Helper()
	_, port, store, srv, _ := startEmulatorFull(t, false)
	setup(store)
	var up *atomic.Bool
	if down {
		port, up = switchProxy(t, fmt.Sprintf("127.0.0.1:%d", port))
	}
	opts := []Option{WithPort(port), WithScanRate(10 * time.Millisecond)}
	if noPoll {
		opts = append(opts, WithTagClass(NoPoll, "SpeedCmd", "TRSCmd"))
	}
	d, err := New("127.0.0.1", testManifest(), opts...)
	if err != nil {
		t.Fatal(err)
	}
	rt, err := runtime.New(runtime.Options{
		Program: prog, Driver: d,
		Tags: tags(prog),
	})
	if err != nil {
		t.Fatal(err)
	}
	d.Start(context.Background())
	t.Cleanup(d.Stop)
	return d, func(n int) {
		for i := 0; i < n; i++ {
			rt.Scan()
			time.Sleep(10 * time.Millisecond)
		}
	}, srv, up
}

func cmdWrites(srv *logixserver.Server, prefix string) []string {
	var out []string
	for _, p := range srv.Writes() {
		if len(p) >= len(prefix) && p[:len(prefix)] == prefix {
			out = append(out, p)
		}
	}
	return out
}

func TestStartupNeverDrivesTheInitDerivedValue(t *testing.T) {
	for _, tc := range []struct {
		name   string
		noPoll bool
		held   float64
		want   []string // writes to SpeedCmd
	}{
		{"holds the live value", false, 5.0, nil},
		{"holds another value", false, 9.0, []string{"SpeedCmd"}},
		{"cannot be read back", true, 9.0, []string{"SpeedCmd"}},
	} {
		for _, down := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/down=%v", tc.name, down), func(t *testing.T) {
				store := (*logixserver.TagStore)(nil)
				_, scan, srv, up := baselineRig(t, incSpeedProg, tc.noPoll, down, func(s *logixserver.TagStore) {
					store = s
					s.UpdateValue("Speed", 4.0)
					s.UpdateValue("SpeedCmd", tc.held)
				})
				if down {
					scan(30)
					up.Store(true)
				}
				scan(80)
				got := cmdWrites(srv, "SpeedCmd")
				if !slices.Equal(got, tc.want) {
					t.Fatalf("writes to SpeedCmd = %v, want %v (the init-derived 1.0 must never go out)", got, tc.want)
				}
				if len(tc.want) == 1 {
					if v := storeVal(store, "SpeedCmd"); v != 5.0 {
						t.Fatalf("SpeedCmd on the controller = %v, want the live-derived 5", v)
					}
				}
			})
		}
	}
}

// A REAL the controller holds as the same 32 bits is equal, though the
// program's float64 differs from what decodes back.
func TestStartupComparesInTheControllersEncoding(t *testing.T) {
	_, scan, srv, _ := baselineRig(t, "PROGRAM Main\nVAR_EXTERNAL SpeedCmd : REAL; END_VAR\nSpeedCmd := 0.1;\nEND_PROGRAM", false, false,
		func(s *logixserver.TagStore) { s.UpdateValue("SpeedCmd", float64(float32(0.1))) })
	scan(60)
	if got := cmdWrites(srv, "SpeedCmd"); len(got) != 0 {
		t.Fatalf("writes = %v, want none: the controller holds 0.1 already", got)
	}
}

const incStructProg = `
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
VAR_EXTERNAL Speed : REAL; TRSCmd : Plt_Type; END_VAR
VAR c : Plt_Type; END_VAR
c.Header.Displacement := 9.75;
c.Header.Valid := TRUE;
c.Count := REAL_TO_DINT(Speed) + 1;
TRSCmd := c;
END_PROGRAM`

// A struct output is decided member by member: equal sends nothing, and
// one differing member sends that member alone.
func TestStartupStructWritesOnlyTheDifferingMember(t *testing.T) {
	for _, tc := range []struct {
		name  string
		count float64
		want  []string
	}{
		{"all equal", 5, nil},
		{"count differs", 9, []string{"TRSCmd.Count"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, scan, srv, _ := baselineRig(t, incStructProg, false, false, func(s *logixserver.TagStore) {
				s.UpdateValue("Speed", 4.0)
				s.UpdateValue("TRSCmd.Header.Displacement", 9.75)
				s.UpdateValue("TRSCmd.Header.Valid", true)
				s.UpdateValue("TRSCmd.Count", tc.count)
			})
			scan(80)
			if got := cmdWrites(srv, "TRSCmd"); !slices.Equal(got, tc.want) {
				t.Fatalf("writes = %v, want %v", got, tc.want)
			}
		})
	}
}

// A driver that began as a standby has no baseline: when its gate opens,
// every output is commanded, equal or not (#606).
func TestStartupBaselineIsNotArmedForAStandby(t *testing.T) {
	_, port, store, srv, _ := startEmulatorFull(t, false)
	store.UpdateValue("SpeedCmd", 77.0) // already what the program commands
	g := &gateWatch{}
	d, err := New("127.0.0.1", testManifest(), WithPort(port), WithScanRate(10*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	d.SetWriteGate(g.gate)
	d.gateOpen() // seen shut: a standby
	d.Start(context.Background())
	t.Cleanup(d.Stop)
	rt := gateRuntime(t, d, nil)
	waitFor(t, "primed", func() bool { return d.Health().Primed })
	rt.Scan()
	g.open.Store(true)
	for i := 0; i < 40; i++ {
		rt.Scan()
		time.Sleep(10 * time.Millisecond)
	}
	waitFor(t, "every output commanded after the takeover", func() bool { return len(cmdWrites(srv, "SpeedCmd")) == 1 })
}

// A driver swapped into a running set: disarmed, its first push is a command
// (equal to the controller's or not); held, it is recorded and never sent.
func TestSwappedDriverBaseline(t *testing.T) {
	for _, tc := range []struct {
		name string
		arm  func(*Driver)
		held float64 // what the controller holds; the push is 7
		want int
	}{
		{"disarmed commands even what the controller holds", (*Driver).DisarmBaseline, 7.0, 1},
		{"held records, though the controller differs", (*Driver).HoldBaseline, 3.0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, port, store, srv, _ := startEmulatorFull(t, false)
			store.UpdateValue("SpeedCmd", tc.held)
			d, err := New("127.0.0.1", testManifest(), WithPort(port), WithScanRate(10*time.Millisecond))
			if err != nil {
				t.Fatal(err)
			}
			d.Start(context.Background())
			t.Cleanup(d.Stop)
			tc.arm(d)
			waitFor(t, "primed", func() bool { return d.Health().Primed })
			if err := d.WriteOutputs(map[string]any{"SpeedCmd": 7.0}); err != nil {
				t.Fatal(err)
			}
			pollsPast(t, d, 3)
			if got := len(cmdWrites(srv, "SpeedCmd")); got != tc.want {
				t.Fatalf("writes = %d, want %d", got, tc.want)
			}
		})
	}
}
