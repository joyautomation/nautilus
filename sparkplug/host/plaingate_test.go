package host

// naut run's liveness gate (io.SetPlainGate), in a project without
// redundancy: (#943, maintainer's decision): while the scans hang, the host
// sends no commands, and stays the primary host. STATE is untouched. When
// the gate reopens the latest command goes out, with no further write.

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
)

func TestPlainGateHoldsCommandsAndKeepsState(t *testing.T) {
	srv, addr := startBroker(t, "")
	t.Cleanup(func() { _ = closeBroker(srv) })

	cfg := testConfig(addr, "h-plain")
	cfg.NoRebirthOnStart = true
	w := watchHost(t, addr, "watch-plain", "h-plain")
	d, err := New(testManifest(), cfg, WithLogger(quietLogger()))
	if err != nil {
		t.Fatal(err)
	}
	var live atomic.Bool
	live.Store(true)
	if n := nio.SetPlainGate(d, live.Load); n != 1 {
		t.Fatalf("io.SetPlainGate reached %d drivers: the host must take naut run's liveness gate", n)
	}
	if id := d.clientID(); id != d.cfg.ClientID {
		t.Fatalf("client ID = %q, want the configured %q: a plain gate does not make the host a replica", id, d.cfg.ClientID)
	}
	d.Start(context.Background())
	t.Cleanup(d.Stop)
	waitConnected(t, d)

	edge := edgePublisher(t, addr, "edge-plain")
	publishPayload(t, edge, "spBv1.0/G/NBIRTH/W6", nbirth(1))
	waitForValue(t, d, "W6__Online", func(v any) bool { return v == true })
	baselineScan(t, d)
	waitUntil(t, "STATE online", func() bool { return len(w.states(t)) == 1 })
	writeSpeed(t, d, 11)
	waitUntil(t, "the 11 command", func() bool { return len(w.commands(t)) == 1 })

	// The scans hang: the gate closes. Commands are held, and the host
	// stays primary: no offline certificate, no session rebuild, across
	// more than one leaderPoll.
	live.Store(false)
	writeSpeed(t, d, 22)
	writeSpeed(t, d, 33)
	time.Sleep(leaderPoll + 500*time.Millisecond) // a negative check: nothing may happen
	barrier(t, d, w)
	if c := w.commands(t); len(c) != 1 {
		t.Fatalf("commands = %v while the liveness gate was closed, want only the first 11", c)
	}
	if s := w.states(t); len(s) != 1 || !s[0].Online {
		t.Fatalf("STATE certificates = %+v while the liveness gate was closed, want only the first online one", s)
	}

	// The scans resume: the latest held command goes out on its own.
	live.Store(true)
	waitUntil(t, "the held command after the gate reopened", func() bool { return len(w.commands(t)) == 2 })
	if c := w.commands(t); c[1] != 33.0 {
		t.Fatalf("commands = %v, want the latest held 33 after 11", c)
	}
	if s := w.states(t); len(s) != 1 {
		t.Fatalf("STATE certificates = %+v after the gate reopened, want only the first", s)
	}
}
