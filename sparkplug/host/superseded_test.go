package host

// A command is owed only while it is the program's latest value for its
// tag (#940). A command queued while it could not go out (a closed write
// gate, a dark site) and then superseded by a move back to what the site
// already holds must never be sent: it would act on a value the program
// has withdrawn.

import (
	"sync/atomic"
	"testing"
)

// The write gate: a leader commands 11; demoted, the program moves the
// setpoint to 22 and back to 11; promoted again, nothing goes out, since
// the site holds 11. It used to send the withdrawn 22 on promotion.
func TestSupersededWhileGatedNeverGoesOut(t *testing.T) {
	srv, addr := startBroker(t, "")
	t.Cleanup(func() { _ = closeBroker(srv) })

	var leader atomic.Bool
	leader.Store(true)
	cfg := testConfig(addr, "h-supersede")
	cfg.NoRebirthOnStart = true
	w := watchHost(t, addr, "watch-supersede", "h-supersede")
	d := startGated(t, cfg, leader.Load)
	waitConnected(t, d)

	edge := edgePublisher(t, addr, "edge-supersede")
	publishPayload(t, edge, "spBv1.0/G/NBIRTH/W6", nbirth(1))
	waitForValue(t, d, "W6__Online", func(v any) bool { return v == true })
	baselineScan(t, d)

	writeSpeed(t, d, 11)
	waitUntil(t, "the 11 command", func() bool { return len(w.commands(t)) == 1 })

	leader.Store(false)
	waitUntil(t, "STATE offline after demotion", func() bool { return len(w.states(t)) == 2 })
	waitConnected(t, d) // back as a standby
	writeSpeed(t, d, 22)
	writeSpeed(t, d, 11)

	leader.Store(true)
	waitUntil(t, "STATE online after re-promotion", func() bool { return len(w.states(t)) == 3 })
	waitConnected(t, d)
	barrier(t, d, w) // flushes, then everything the driver sent has arrived
	if c := w.commands(t); len(c) != 1 {
		t.Fatalf("commands = %v, want only the first 11: 22 was withdrawn before the gate reopened", c)
	}
}

// A dark site: the program commands 11 (the site takes it); the node dies;
// the program moves the setpoint to 33, which is parked, and back to 11.
// When the site comes back nothing more goes out. It used to send the
// parked 33 at the DBIRTH.
func TestSupersededParkedCommandNeverGoesOut(t *testing.T) {
	srv, addr := startBroker(t, "")
	t.Cleanup(func() { _ = closeBroker(srv) })
	w := watchHost(t, addr, "watch-supersede-park", "h-supersede-park")
	host := startHostDriver(t, testManifest(), testConfig(addr, "h-supersede-park"))
	waitConnected(t, host)
	edge := edgePublisher(t, addr, "edge-supersede-park")
	birthW6AndPLC1(t, host, edge, 1)
	baselineScan(t, host)

	writeSpeed(t, host, 11)
	waitUntil(t, "the 11 command", func() bool { return len(w.commands(t)) == 1 })

	publishPayload(t, edge, "spBv1.0/G/NDEATH/W6", ndeath(1))
	waitForValue(t, host, "W6__Online", func(v any) bool { return v == false })
	writeSpeed(t, host, 33)
	waitQueuedOnW6(t, host, 1)
	writeSpeed(t, host, 11)

	birthW6AndPLC1(t, host, edge, 2)
	// Whatever the births released has left the park (under the lock that
	// puts it in pending) once nothing is parked; the barrier then flushes
	// pending and waits for everything the driver sent.
	waitQueuedOnW6(t, host, 0)
	barrier(t, host, w)
	if c := w.commands(t); len(c) != 1 {
		t.Fatalf("commands = %v, want only the first 11: the parked 33 was withdrawn before the site came back", c)
	}
}

// A failed publish puts its commands back only while the program still
// wants them: not one it has moved away from since, and never over a
// newer pending value.
func TestRequeueSkipsASupersededCommand(t *testing.T) {
	d, _ := newTestDriver(t, testManifest(), Config{})
	d.initWriteMapsLocked()
	const name = "W6_PLC1_Pump_SpeedSP"
	work := map[string]any{name: d.canonWrite(name, 22.0)}

	d.lastOut[name] = d.canonWrite(name, 11.0) // moved back since
	d.requeueLocked([]string{name}, work)
	if v, ok := d.pending[name]; ok {
		t.Fatalf("pending = %v: 22 was superseded by 11 and must not be requeued", v)
	}

	d.lastOut[name] = work[name] // still wanted
	d.requeueLocked([]string{name}, work)
	if v, ok := d.pending[name]; !ok || !sameValue(v, work[name]) {
		t.Fatalf("pending = %v, want the still-wanted 22 requeued", v)
	}

	newer := d.canonWrite(name, 33.0)
	d.pending[name] = newer
	d.requeueLocked([]string{name}, work)
	if !sameValue(d.pending[name], newer) {
		t.Fatalf("pending = %v, want the newer 33 kept", d.pending[name])
	}
}
