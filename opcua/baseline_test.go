package opcua

import (
	"fmt"
	"testing"
	"time"

	"github.com/gopcua/opcua/ua"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/runtime"
)

// The start-up baseline decides each output against what the SERVER holds,
// at the first read of its node (#1121): a program whose first computed
// value differs from the server's commands it; an equal one sends nothing.

func quiet(t *testing.T, d *Driver) {
	t.Helper()
	time.Sleep(300 * time.Millisecond) // several scans
	if w := d.Health().Writes; w != 0 {
		t.Fatalf("writes = %d, want 0", w)
	}
}

// The issue: the controller's first value (already true there; 7.0 here)
// differs from what the server was read as, and it was never sent.
func TestFirstOutputDifferingFromTheServerIsWritten(t *testing.T) {
	ts := startServer(t, freePort(t))
	d := startDriver(t, ts.url, manifest(2))
	waitFor(t, "first poll", func() bool { _, ok := read(t, d)["Setpoint"]; return ok })
	_ = d.WriteOutputs(nio.Values{"Setpoint": 7.0}) // the first push since start
	waitFor(t, "the first value to land", func() bool { return ts.get(t, "Setpoint") == 7.0 })
	if w := d.Health().Writes; w != 1 {
		t.Errorf("writes = %d, want 1", w)
	}
}

func TestFirstOutputEqualToTheServerWritesNothing(t *testing.T) {
	ts := startServer(t, freePort(t))
	d := startDriver(t, ts.url, manifest(2))
	waitFor(t, "first poll", func() bool { _, ok := read(t, d)["Setpoint"]; return ok })
	_ = d.WriteOutputs(nio.Values{"Setpoint": 0.0})
	quiet(t, d)
}

// The other direction: an operator left the server at a value and the
// program starts at its default. The controller owns its outputs, so it
// writes its value (a value the operator sets belongs in an input).
func TestFirstOutputOverwritesWhatTheServerHolds(t *testing.T) {
	ts := startServer(t, freePort(t))
	ts.set(t, "Setpoint", 5.0)
	d := startDriver(t, ts.url, manifest(2))
	waitFor(t, "first poll", func() bool { return read(t, d)["Setpoint"] == 5.0 })
	_ = d.WriteOutputs(nio.Values{"Setpoint": 0.0})
	waitFor(t, "the program's value", func() bool { return ts.get(t, "Setpoint") == 0.0 })
}

// A program INT and a server Double holding the same number are equal.
func TestFirstOutputOfAnotherNumericKindIsComparedByValue(t *testing.T) {
	ts := startServer(t, freePort(t))
	d := startDriver(t, ts.url, manifest(2))
	waitFor(t, "first poll", func() bool { _, ok := read(t, d)["Setpoint"]; return ok })
	_ = d.WriteOutputs(nio.Values{"Setpoint": int64(0)})
	quiet(t, d)
}

// No read yet (the server is down when the first push arrives): the value
// is held for the first read, never taken as already sent. It then decides.
func TestFirstOutputWaitsForTheFirstRead(t *testing.T) {
	for _, tc := range []struct {
		name string
		val  float64
		want int
	}{{"differs", 7.0, 1}, {"equal", 0.0, 0}} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			port := freePort(t)
			d := startDriver(t, fmt.Sprintf("opc.tcp://127.0.0.1:%d", port), manifest(2))
			_ = d.WriteOutputs(nio.Values{"Setpoint": tc.val}) // nothing read yet
			time.Sleep(200 * time.Millisecond)
			ts := startServer(t, port)
			waitFor(t, "online", func() bool { return read(t, d)["plant__Online"] == true })
			if tc.want == 1 {
				waitFor(t, "the held value", func() bool { return ts.get(t, "Setpoint") == tc.val })
			}
			time.Sleep(300 * time.Millisecond)
			if w := d.Health().Writes; w != uint64(tc.want) {
				t.Errorf("writes = %d, want %d", w, tc.want)
			}
		})
	}
}

// A node the server answers without a good value (no read access) is
// commanded once: there is nothing to compare with.
func TestFirstOutputOfAnUnreadableNodeIsCommandedOnce(t *testing.T) {
	ts := startServer(t, freePort(t))
	d := startDriver(t, ts.url, manifest(2))
	waitFor(t, "first poll", func() bool { _, ok := read(t, d)["Setpoint"]; return ok })
	b := d.byName["Setpoint"]
	d.mu.Lock()
	d.undecided["Setpoint"] = 0.0 // equal to what the server holds, were it readable
	d.apply(b, &ua.DataValue{EncodingMask: ua.DataValueStatusCode, Status: ua.StatusBadNotReadable})
	_, queued := d.pending["Setpoint"]
	d.mu.Unlock()
	if !queued {
		t.Fatal("an unreadable output was not queued as a command")
	}
	_ = ts
}

// An output that is never polled has nothing to read: commanded once.
func TestFirstOutputOfAnUnpolledNodeIsCommanded(t *testing.T) {
	ts := startServer(t, freePort(t))
	m := manifest(2)
	for i := range m.Tags {
		if m.Tags[i].Name == "Setpoint" {
			m.Tags[i].ScanClass = NoPoll
		}
	}
	d := startDriver(t, ts.url, m)
	waitFor(t, "online", func() bool { return read(t, d)["plant__Online"] == true })
	_ = d.WriteOutputs(nio.Values{"Setpoint": 7.0})
	waitFor(t, "the first value to land", func() bool { return ts.get(t, "Setpoint") == 7.0 })
}

// Later pushes before the first read keep the output undecided with the
// latest value: a runtime pushes the whole set whenever any output moves.
func TestSecondPushBeforeTheFirstReadStaysUndecided(t *testing.T) {
	port := freePort(t)
	d := startDriver(t, fmt.Sprintf("opc.tcp://127.0.0.1:%d", port), manifest(2))
	_ = d.WriteOutputs(nio.Values{"Setpoint": 0.0})
	_ = d.WriteOutputs(nio.Values{"Setpoint": 0.0})
	time.Sleep(200 * time.Millisecond)
	ts := startServer(t, port) // holds 0.0
	waitFor(t, "online", func() bool { return read(t, d)["plant__Online"] == true })
	quiet(t, d)
	_ = ts
}

// A first push while the session is down is not decided against the dead
// session's read: with no reassert, the new session's server is what counts.
func TestFirstPushOfferedWhileDownIsDecidedOnTheNextSession(t *testing.T) {
	port := freePort(t)
	ts := startServer(t, port)
	ts.set(t, "Setpoint", 5.0)
	d := startDriver(t, ts.url, manifest(2), WithReassertOnReconnect(false))
	waitFor(t, "first poll", func() bool { return read(t, d)["Setpoint"] == 5.0 })
	_ = ts.srv.Close()
	waitFor(t, "offline", func() bool { return read(t, d)["plant__Online"] == false })
	_ = d.WriteOutputs(nio.Values{"Setpoint": 5.0}) // equal to the dead session's read
	ts2 := startServer(t, port)                     // fresh state: 0.0
	waitFor(t, "back", func() bool { return read(t, d)["plant__Online"] == true })
	waitFor(t, "the program's value", func() bool { return ts2.get(t, "Setpoint") == 5.0 })
}

// A gate reopening forgets what was waiting on a first read.
func TestForgetWrittenClearsWhatWaitsForTheFirstRead(t *testing.T) {
	d, err := New("opc.tcp://127.0.0.1:1", manifest(2), fastOpts()...)
	if err != nil {
		t.Fatal(err)
	}
	d.undecided["Setpoint"] = 1.0
	d.forgetWritten()
	if len(d.undecided) != 0 {
		t.Fatalf("undecided = %v after forgetWritten", d.undecided)
	}
}

// The first scan computes from init values before anything was read; the
// baseline must decide on a push computed from the live inputs (#1123 review).
// Setpoint := Speed + 1; the server holds Speed = 4 and Setpoint = 5.
func runIncRig(t *testing.T, held float64, scans int) (*Driver, *testServer) {
	t.Helper()
	ts := startServerWith(t, freePort(t), "urn:nautilus:test", 4.0)
	ts.set(t, "Setpoint", held)
	d := startDriver(t, ts.url, manifest(2))
	rt, err := runtime.New(runtime.Options{
		Program: "PROGRAM Main\nVAR_EXTERNAL Speed : REAL; Setpoint : REAL; END_VAR\nSetpoint := Speed + 1.0;\nEND_PROGRAM",
		Driver:  d,
		Tags:    []runtime.TagDef{runtime.Input("Speed", runtime.Init(0.0)), runtime.Output("Setpoint", runtime.Init(0.0))},
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < scans; i++ {
		rt.Scan()
		time.Sleep(20 * time.Millisecond)
	}
	return d, ts
}

func TestStartupNeverDrivesTheInitDerivedValue(t *testing.T) {
	d, ts := runIncRig(t, 5.0, 40)
	if w := d.Health().Writes; w != 0 {
		t.Fatalf("writes = %d, want 0: the server held 5 and the live input makes 5 (the init-derived 1 must never go out); holds %v", w, ts.get(t, "Setpoint"))
	}
}

func TestStartupWritesTheLiveValueOnce(t *testing.T) {
	d, ts := runIncRig(t, 9.0, 40)
	if w := d.Health().Writes; w != 1 || ts.get(t, "Setpoint") != 5.0 {
		t.Fatalf("writes = %d, server holds %v, want one write of 5", w, ts.get(t, "Setpoint"))
	}
}

// A driver swapped in under HoldOutputsOnSwap commands nothing until an
// output changes, whatever the server holds: the runtime holds its baseline
// (io.BaselineHolder), which must not decide against the server (#1123).
func TestHeldBaselineRecordsAndNeverSends(t *testing.T) {
	ts := startServer(t, freePort(t))
	d := startDriver(t, ts.url, manifest(2))
	waitFor(t, "first poll", func() bool { _, ok := read(t, d)["Setpoint"]; return ok })
	bh, ok := any(d).(nio.BaselineHolder)
	if !ok {
		t.Fatal("the opcua driver is not an io.BaselineHolder: a held swap would decide against the server and write")
	}
	bh.HoldBaseline()
	_ = d.WriteOutputs(nio.Values{"Setpoint": 7.0}) // differs from the server's 0
	quiet(t, d)
	_ = d.WriteOutputs(nio.Values{"Setpoint": 8.0})
	waitFor(t, "the change", func() bool { return ts.get(t, "Setpoint") == 8.0 })
}
