package plcnexttest_test

import (
	"context"
	"io"
	"log/slog"
	"os"
	"slices"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/plcnext"
	"github.com/joyautomation/nautilus/plcnext/plcnexttest"
)

// The fake spells its port names out itself (it cannot import plcnext);
// they must stay the driver's defaults, which the bench manifest relies on.
func TestNamesAreTheDriversDefaults(t *testing.T) {
	for _, c := range []struct{ fake, driver string }{
		{plcnexttest.Inputs, plcnext.DefaultInputs},
		{plcnexttest.Outputs, plcnext.DefaultOutputs},
		{plcnexttest.RunPort, plcnext.DefaultRunPort},
		{plcnexttest.Component, plcnext.DefaultComponent},
	} {
		if c.fake != c.driver {
			t.Errorf("plcnexttest serves %q, the driver's default is %q", c.fake, c.driver)
		}
	}
}

// bench is the bench project's manifest on the gRPC transport, pointed at
// the fake's socket, and its driver, started.
func bench(t *testing.T, g *plcnexttest.GDS) *plcnext.Driver {
	t.Helper()
	raw, err := os.ReadFile("../../examples/plcnext-bench/plcnext_manifest.yaml")
	if err != nil {
		t.Fatal(err)
	}
	m, err := plcnext.ParseManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	m.Transport, m.Socket = plcnext.TransportGRPC, g.Listen(t)
	d, err := plcnext.New(m, plcnext.WithScanRate(10*time.Millisecond),
		plcnext.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	d.Start(ctx)
	t.Cleanup(func() { cancel(); d.Stop() })
	return d
}

func waitFor(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(d); !cond(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("after %s: %s", d, what)
		}
	}
}

func online(d *plcnext.Driver) func() bool  { return func() bool { return d.Health().Online } }
func offline(d *plcnext.Driver) func() bool { return func() bool { return !d.Health().Online } }

func quality(d *plcnext.Driver, tag string) nio.Quality { return d.Quality()[tag] }

// The real driver against the fake, through what a Sparkplug device's
// health and a project's tags see: the handshake brings it online, an
// input arrives, an output lands in the image, a stopped bus is Bad and
// offline on a live socket, a dropped controller is Stale and offline,
// and both come back.
func TestTheDriverRunsAgainstTheFake(t *testing.T) {
	g := plcnexttest.New()
	d := bench(t, g)

	waitFor(t, 5*time.Second, "online after the BusConductor handshake", online(d))
	if !g.Running() {
		t.Fatal("online, but the fake's bus never started")
	}

	g.SetInput(2, 0x01) // DI_00 = ch:1 of the DI16 at input byte 2
	waitFor(t, 2*time.Second, "DI_00 true", func() bool {
		v, _ := d.ReadInputs()
		return v["DI_00"] == true
	})

	if err := d.WriteOutputs(nio.Values{"DO_00": true}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, "DO_00 in output byte 0", func() bool { return g.Output(0)&0x01 != 0 })

	g.SetRun(false)
	waitFor(t, 2*time.Second, "offline once the bus stopped", offline(d))
	if q := quality(d, "DI_00"); q != nio.Bad {
		t.Errorf("DI_00 with the bus stopped: %s, want Bad (a live socket, stale bytes)", q)
	}
	g.SetRun(true)
	waitFor(t, 2*time.Second, "online once the bus runs again", online(d))

	g.Drop()
	waitFor(t, 5*time.Second, "offline once the controller dropped", offline(d))
	if q := quality(d, "DI_00"); q != nio.Stale {
		t.Errorf("DI_00 with the controller gone: %s, want Stale", q)
	}
	time.Sleep(200 * time.Millisecond)
	if d.Health().Online {
		t.Fatal("online again while the controller is still dropped")
	}
	g.Restore()
	waitFor(t, 10*time.Second, "online once the controller is back", online(d))
	waitFor(t, 2*time.Second, "DI_00 Good again", func() bool { return quality(d, "DI_00") == nio.Good })
}

// A stopped bus stays stopped across a reconnect: the driver's handshake
// asks for a start, and a faulted bus does not give one, so a test of a
// stopped bus cannot flap back online on a redial.
func TestAStoppedBusStaysStoppedAcrossAReconnect(t *testing.T) {
	g := plcnexttest.New()
	d := bench(t, g)
	waitFor(t, 5*time.Second, "online", online(d))

	g.SetRun(false)
	waitFor(t, 2*time.Second, "offline", offline(d))
	g.Drop()
	waitFor(t, 5*time.Second, "the session ended", func() bool { return !d.Health().Connected })
	n := len(g.Writes())
	g.Restore()
	// The new session asks for a start (START_IO_REQ low, then high).
	waitFor(t, 5*time.Second, "a start request on the new session", func() bool {
		return slices.Contains(g.Writes()[n:], plcnexttest.Component+"/BusConductor.START_IO_REQ=true")
	})
	time.Sleep(300 * time.Millisecond)
	if g.Running() || d.Health().Online {
		t.Fatalf("a faulted bus started on a start request (running %v, online %v)", g.Running(), d.Health().Online)
	}
	g.SetRun(true)
	waitFor(t, 30*time.Second, "online once the bus runs again", online(d))
}
