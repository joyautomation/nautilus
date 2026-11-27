package optommp

import (
	"context"
	"testing"
	"time"
)

// lone runs a driver for m over l with no write gate (a lone controller),
// scanning every 5 ms.
func lone(t *testing.T, l *link) *Driver {
	t.Helper()
	m := benchManifest(t, l.dev, ManifestOptions{ScanRate: 5 * time.Millisecond})
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
