package host

// Stop on a hung broker (#942): the TCP session stays open and nothing is
// answered. Stop still offers the STATE death first (the TCK's order), but
// waits for it only stopDeathWait, not tokenTimeout, so a stop fits a
// supervisor's budget. It used to take the full 10 s.

import (
	"context"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/sparkplug/internal/linkcut"
)

func TestStopIsBoundedOnAHungBroker(t *testing.T) {
	srv, addr := startBroker(t, "")
	t.Cleanup(func() { _ = closeBroker(srv) })
	px := linkcut.New(t, addr)
	d, err := New(testManifest(), testConfig(px.Addr(), "h-stop-hung"), WithLogger(quietLogger()))
	if err != nil {
		t.Fatal(err)
	}
	d.Start(context.Background())
	waitConnected(t, d)

	px.Stall()
	start := time.Now()
	stopped := make(chan struct{})
	go func() { d.Stop(); close(stopped) }()
	bound := stopDeathWait + 2*time.Second
	select {
	case <-stopped:
		t.Logf("Stop took %s", time.Since(start).Round(time.Millisecond))
	case <-time.After(bound):
		t.Fatalf("Stop still running after %s with the broker hung (stopDeathWait is %s)", bound, stopDeathWait)
	}
}
