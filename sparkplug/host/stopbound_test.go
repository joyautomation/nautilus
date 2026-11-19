package host

// Stop on a hung link to the broker (#942): the TCP session stays open and
// nothing gets through. Stop still offers the STATE death first (the TCK's
// order), but waits for it only stopDeathWait, not tokenTimeout, so a stop
// fits a supervisor's budget. It used to take the full 10 s. And the world
// still learns the host is gone: a subscriber sees an offline STATE (the
// death, or the will the broker fires when the session times out).

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
	w := watchHost(t, addr, "watch-stop-hung", "h-stop-hung") // straight to the broker
	// The default keepalive (30 s): a short one would have paho notice the
	// dead link and fail the death on its own, and the bound would not be
	// what ends the wait.
	d, err := New(testManifest(), testConfig(px.Addr(), "h-stop-hung"), WithLogger(quietLogger()))
	if err != nil {
		t.Fatal(err)
	}
	d.Start(context.Background())
	waitConnected(t, d)
	waitUntil(t, "STATE online", func() bool { return len(w.states(t)) == 1 })

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
	// The broker learns of the dead session (here the link closes; on a
	// real network its keepalive times out) and fires the will.
	px.Cut()
	waitUntil(t, "an offline STATE (the death or the will) after Stop", func() bool {
		s := w.states(t)
		return len(s) > 1 && !s[len(s)-1].Online
	})
}
