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
	barrier(t, d, w)
	if c := w.commands(t); len(c) != 2 || c[1] != 33.0 {
		t.Fatalf("commands = %v, want the latest held 33 after 11", c)
	}
	if s := w.states(t); len(s) != 1 {
		t.Fatalf("STATE certificates = %+v after the gate reopened, want only the first", s)
	}
}

// startPlain starts a host with naut run's liveness gate, live, and births
// W6; it returns the host, its watcher and the gate.
func startPlain(t *testing.T, id string) (*Driver, *stateLog, *atomic.Bool) {
	t.Helper()
	return startWithGate(t, id, func(d *Driver, g func() bool) { nio.SetPlainGate(d, g) })
}

// startWithGate is startPlain with the gate installed by install.
func startWithGate(t *testing.T, id string, install func(*Driver, func() bool)) (*Driver, *stateLog, *atomic.Bool) {
	t.Helper()
	srv, addr := startBroker(t, "")
	t.Cleanup(func() { _ = closeBroker(srv) })
	cfg := testConfig(addr, id)
	cfg.NoRebirthOnStart = true
	w := watchHost(t, addr, "watch-"+id, id)
	d, err := New(testManifest(), cfg, WithLogger(quietLogger()))
	if err != nil {
		t.Fatal(err)
	}
	live := new(atomic.Bool)
	live.Store(true)
	install(d, live.Load)
	d.Start(context.Background())
	t.Cleanup(d.Stop)
	waitConnected(t, d)
	edge := edgePublisher(t, addr, "edge-"+id)
	publishPayload(t, edge, "spBv1.0/G/NBIRTH/W6", nbirth(1))
	waitForValue(t, d, "W6__Online", func(v any) bool { return v == true })
	baselineScan(t, d)
	return d, w, live
}

// A gate closed for less than leaderPoll still releases what it held: the
// host used to compare two samples a second apart, saw no change, and kept
// the command until the next write, birth or reconnect.
func TestPlainGateShortClosureReleasesItsCommand(t *testing.T) {
	d, w, live := startPlain(t, "h-plain-short")
	live.Store(false)
	writeSpeed(t, d, 22)
	time.Sleep(250 * time.Millisecond) // well under leaderPoll
	live.Store(true)
	waitUntil(t, "the command held by a 250 ms closure", func() bool { return len(w.commands(t)) == 1 })
	if c := w.commands(t); c[0] != 22.0 {
		t.Fatalf("commands = %v, want [22]", c)
	}
}

// So does a redundancy leadership gate closed for less than leaderPoll:
// serve never sees the replica demoted, so no reconnect kicks the writer,
// and the held command used to wait for the next write, birth or reconnect.
// The conformance row hid this: a 1 s MQTT keepalive made its broker drop
// the host every second, and each reconnect sent the command (#943).
func TestLeadershipGateShortClosureReleasesItsCommand(t *testing.T) {
	d, w, leader := startWithGate(t, "h-lead-short", func(d *Driver, g func() bool) { d.SetWriteGate(g) })
	states := len(w.states(t))
	leader.Store(false)
	writeSpeed(t, d, 22)
	time.Sleep(250 * time.Millisecond) // well under leaderPoll
	leader.Store(true)
	waitUntil(t, "the command held by a 250 ms leadership-gate closure", func() bool { return len(w.commands(t)) == 1 })
	if c := w.commands(t); c[0] != 22.0 {
		t.Fatalf("commands = %v, want [22]", c)
	}
	if s := w.states(t); len(s) != states {
		t.Fatalf("STATE certificates = %+v, want the %d from before: the closure was meant to go unseen by serve, with no reconnect", s, states)
	}
}

// A plain gate holds tag commands only: an operator's __Rebirth is a
// rebirth request, and a primary host still sends it.
func TestPlainGateLetsARebirthRequestThrough(t *testing.T) {
	d, _, live := startPlain(t, "h-plain-rebirth")
	before := d.Status().Rebirths
	live.Store(false)
	if err := d.WriteOutputs(map[string]any{"W6__Rebirth": true}); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the rebirth request with the gate closed", func() bool { return d.Status().Rebirths > before })
}
