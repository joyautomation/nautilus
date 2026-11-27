package optommp

import (
	"context"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
)

// lone runs a driver for m over l with no write gate (a lone controller),
// scanning every 5 ms.
func lone(t *testing.T, l *link, opts ...func(*ManifestOptions)) *Driver {
	t.Helper()
	mo := ManifestOptions{ScanRate: 5 * time.Millisecond}
	for _, f := range opts {
		f(&mo)
	}
	m := benchManifest(t, l.dev, mo)
	drv, err := New(m, WithScanRate(5*time.Millisecond), WithDialer(l.dial))
	if err != nil {
		t.Fatal(err)
	}
	drv.retryMin = 10 * time.Millisecond
	drv.chassis = time.Hour
	return drv
}

// #786: a lone controller's driver is not ready before the unit's first
// read, ready once it has delivered, and stays ready when the unit later
// drops (the answer is about the first scan only).
func TestInputsReadyAfterTheFirstDelivery(t *testing.T) {
	l := newLink(newBenchDevice())
	l.delay = 50 * time.Millisecond // the connect takes a while
	drv := lone(t, l)
	if drv.InputsReady() {
		t.Fatal("ready before Start")
	}
	drv.Start(context.Background())
	t.Cleanup(drv.Stop)
	if drv.InputsReady() {
		t.Fatal("ready before the unit answered")
	}
	eventually(t, "ready after the first delivery", drv.InputsReady)
	if drv.Health().Reads == 0 {
		t.Error("ready with no read delivered")
	}
	l.cut()
	eventually(t, "the session to end", func() bool { return !drv.Health().Connected })
	if !drv.InputsReady() {
		t.Error("not ready after a later outage: the answer must stay true")
	}
}

// A unit that cannot be reached at start: the first failed attempt ends
// the wait, so a dead unit does not cost the whole wait on every start.
func TestInputsReadyAfterAFailedFirstAttempt(t *testing.T) {
	l := newLink(newBenchDevice())
	l.down.Store(true)
	drv := lone(t, l)
	drv.Start(context.Background())
	t.Cleanup(drv.Stop)
	eventually(t, "ready after the failed connect", drv.InputsReady)
	if drv.Health().Reads != 0 {
		t.Error("the unreachable unit delivered a read")
	}
}

// The gate `naut run` gives a lone controller (io.SetPlainGate: open while
// the scans are live, which a held start keeps them) changes nothing: not
// ready before the unit's first read, ready after, with or without
// `watchdog:` (Fable).
func TestInputsReadyUnderThePlainGate(t *testing.T) {
	wd := 100 * time.Millisecond
	for _, tc := range []struct {
		name     string
		watchdog *time.Duration
	}{{"no watchdog", nil}, {"watchdog", &wd}} {
		t.Run(tc.name, func(t *testing.T) {
			l := newLink(newBenchDevice())
			l.delay = 50 * time.Millisecond
			drv := lone(t, l, func(o *ManifestOptions) { o.Watchdog = tc.watchdog })
			if n := nio.SetPlainGate(drv, func() bool { return true }); n != 1 {
				t.Fatalf("SetPlainGate took %d drivers, want 1", n)
			}
			drv.Start(context.Background())
			t.Cleanup(drv.Stop)
			if drv.InputsReady() {
				t.Fatal("ready before the unit answered")
			}
			eventually(t, "ready after the first delivery", drv.InputsReady)
			if drv.Health().Reads == 0 {
				t.Error("ready with no read delivered")
			}
		})
	}
}
