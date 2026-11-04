package modbus

import (
	"sync/atomic"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/runtime"
)

// These run the driver under the runtime's scan, so what is asserted is
// the whole path: the driver forgets a command, asks for the outputs
// (io.OutputsRequester, #601), and the runtime's next scan hands them
// over, changed or not.

// outputsProg declares the two outputs and never writes them: the test
// sets them the way an operator would, so they change only when it says.
const outputsProg = `PROGRAM Main
VAR_EXTERNAL SP : REAL; KA : REAL; END_VAR
END_PROGRAM`

func newOutputsRuntime(t *testing.T, d *Driver, coord runtime.Coordinator) *runtime.Runtime {
	t.Helper()
	rt, err := runtime.New(runtime.Options{
		Program:     outputsProg,
		Driver:      d,
		Outputs:     []string{"SP", "KA"},
		Seed:        nio.Values{"SP": 0.0, "KA": 7.0},
		Coordinator: coord,
	})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return rt
}

type leaderFunc func() bool

func (f leaderFunc) IsLeader() bool { return f() }

// A setpoint set once and never changed again, whose write a gateway
// answers 0x0B (#596), goes out again on the runtime's first scan after
// its retry backoff (RetryMin): the driver forgets it and asks for the
// outputs, so the scan pushes them although none changed. Before #601 it
// waited for the next change, which for a setpoint can be never. It goes
// out once more, not on every scan.
func TestUndeliveredSetpointGoesOutAgainAfterTheBackoff(t *testing.T) {
	srv := newSlave(t)
	u := srv.Unit(1)
	rec := &recorder{}
	m := Manifest{
		Sources: []Source{fastSource("DEV", srv.Addr())},
		Tags: []TagBinding{
			{Name: "SP", Source: "DEV", Table: TableHolding, Address: 500, Format: "uint16", Writable: true},
			{Name: "KA", Source: "DEV", Table: TableHolding, Address: 400, Format: "uint16", Writable: true},
		},
	}
	d, err := New(m, WithScanRate(20*time.Millisecond), WithDialer(recordingDialer(rec)))
	if err != nil {
		t.Fatal(err)
	}
	startDriver(t, d)
	waitFor(t, "connect", func() bool { return d.Health().Sources[0].State == "connected" })
	rt := newOutputsRuntime(t, d, nil)

	rt.Scan() // the first push is the driver's baseline: recorded, not sent
	u.InjectExceptionAt(FCWriteSingleRegister, 500, 0x0B)
	rt.Tags().SetReal("SP", 21)
	rt.Scan()
	waitFor(t, "the 0x0B answer", func() bool { return d.Health().Sources[0].Exceptions == 1 })
	u.InjectExceptionAt(FCWriteSingleRegister, 500, 0)

	waitFor(t, "the unchanged setpoint goes out again", func() bool {
		rt.Scan()
		return u.Holding(500) == 21
	})

	for range 5 {
		rt.Scan()
		time.Sleep(10 * time.Millisecond)
	}
	if n := len(rec.writes(FCWriteSingleRegister, 500)); n != 2 {
		t.Errorf("SP frames = %d, want 2: the 0x0B one and one resend", n)
	}
	if n := len(rec.writes(FCWriteSingleRegister, 400)); n != 0 {
		t.Errorf("KA was never commanded and never forgotten, yet %d frames went out", n)
	}
}

// Regaining the write gate (#600): this replica led and wrote, lost the
// gate, another master wrote the same registers, and the gate came back.
// Every output goes out again with the program's value, the one that
// never changed too, and the rewrite: keep-alive re-asserts nothing until
// the first push after the reopen has handed it the program's value: never
// the value from the previous term.
//
// With a takeover the runtime pushes because it took over; without one (a
// gate that reopens on its own, logic healthy again) it pushes because
// the driver asked for the outputs when it saw the gate reopen (#601).
func TestRegainingTheGateSendsEveryOutputAgain(t *testing.T) {
	for _, tc := range []struct {
		name     string
		takeover bool
		ka       float64 // the program's KA once the gate is back
	}{
		// While standby, the leader in between retuned the keep-alive
		// word; the retained value came back with the takeover.
		{"takeover", true, 8},
		// The gate alone closed and reopened: nothing changed.
		{"gate only", false, 7},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newSlave(t)
			u := srv.Unit(1)
			rec := &recorder{}
			m := Manifest{
				Sources: []Source{fastSource("DEV", srv.Addr())},
				Tags: []TagBinding{
					{Name: "SP", Source: "DEV", Table: TableHolding, Address: 500, Format: "uint16", Writable: true},
					{Name: "KA", Source: "DEV", Table: TableHolding, Address: 400, Format: "uint16", Writable: true, Rewrite: 30 * time.Millisecond},
				},
			}
			d, err := New(m, WithScanRate(20*time.Millisecond), WithDialer(recordingDialer(rec)))
			if err != nil {
				t.Fatal(err)
			}
			// One source: its loop is the only caller of the gate, so the
			// looks are counted in order.
			var open atomic.Bool
			var looks atomic.Int64
			open.Store(true)
			d.SetWriteGate(func() bool { looks.Add(1); return open.Load() })
			// seen waits until the driver has looked at the gate after the
			// test moved it: the second look after a move started after the
			// first finished, whatever that one found.
			seen := func(what string) {
				n := looks.Load()
				waitFor(t, what, func() bool { return looks.Load() >= n+2 })
			}
			startDriver(t, d)
			waitFor(t, "connect", func() bool { return d.Health().Sources[0].State == "connected" })
			var coord runtime.Coordinator
			if tc.takeover {
				coord = leaderFunc(open.Load)
			}
			rt := newOutputsRuntime(t, d, coord)

			// Leader: the baseline (KA is a keep-alive and is sent), then SP.
			rt.Scan()
			rt.Tags().SetReal("SP", 77)
			rt.Scan()
			waitFor(t, "the leader's writes", func() bool { return u.Holding(500) == 77 && u.Holding(400) == 7 })

			open.Store(false)
			seen("the driver to see the gate close")
			// A standby does not scan; the gate-only runtime has nothing
			// changed to push. Meanwhile another master writes both
			// registers.
			rt.Scan()
			u.SetHolding(500, 5)
			u.SetHolding(400, 3)
			if tc.ka != 7 {
				rt.Tags().SetReal("KA", tc.ka)
			}

			open.Store(true)
			seen("the driver to see the gate reopen")
			kaBefore := len(rec.writes(FCWriteSingleRegister, 400))
			spBefore := len(rec.writes(FCWriteSingleRegister, 500))
			time.Sleep(120 * time.Millisecond) // four keep-alive periods, no push yet
			if got := rec.writes(FCWriteSingleRegister, 400)[kaBefore:]; len(got) != 0 || u.Holding(400) != 3 {
				t.Errorf("the keep-alive re-asserted before the first push after the reopen: %d frames (first value %d), holding 400 = %d",
					len(got), firstValue(got), u.Holding(400))
			}

			rt.Scan()
			waitFor(t, "every output back at the program's value", func() bool {
				return u.Holding(500) == 77 && u.Holding(400) == uint16(tc.ka)
			})
			waitFor(t, "the keep-alive re-asserting again", func() bool {
				return len(rec.writes(FCWriteSingleRegister, 400)) >= kaBefore+3
			})
			for range 3 {
				rt.Scan()
			}
			time.Sleep(40 * time.Millisecond)
			for _, f := range rec.writes(FCWriteSingleRegister, 400)[kaBefore:] {
				if f.count != uint16(tc.ka) {
					t.Errorf("the keep-alive sent %d after the reopen, want only the program's %v", f.count, tc.ka)
				}
			}
			if n := len(rec.writes(FCWriteSingleRegister, 500)) - spBefore; n != 1 {
				t.Errorf("SP frames after the reopen = %d, want 1", n)
			}
		})
	}
}

func firstValue(fs []frameRec) uint16 {
	if len(fs) == 0 {
		return 0
	}
	return fs[0].count
}
