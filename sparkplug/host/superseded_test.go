package host

// A command is owed only while it is the program's latest value for its
// tag (#940). A command queued while it could not go out (a closed write
// gate, a dark site) and then superseded by a move back to what the site
// already holds must never be sent: it would act on a value the program
// has withdrawn.

import (
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/lang/ir"
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
// When the site comes back the program's 11 goes out again (a reborn site
// may hold anything, so the host no longer vouches for its 11), and never
// the withdrawn 33. It used to send the parked 33 at the DBIRTH.
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
	if c := w.commands(t); len(c) != 2 || c[1] != 11.0 {
		t.Fatalf("commands = %v, want [11 11]: the program's 11 again at the rebirth, never the withdrawn 33", c)
	}
}

// A site that comes back holding something else gets the program's value
// (#940): 11 sent, NDEATH, 33 parked, the program back to 11, and the DBIRTH
// reports 50. The host used to keep vouching for the 11 it sent before the
// death, withdraw the revert, and leave the device at 50.
func TestRebornSiteGetsTheProgramsValue(t *testing.T) {
	srv, addr := startBroker(t, "")
	t.Cleanup(func() { _ = closeBroker(srv) })
	w := watchHost(t, addr, "watch-reborn", "h-reborn")
	host := startHostDriver(t, testManifest(), testConfig(addr, "h-reborn"))
	waitConnected(t, host)
	edge := edgePublisher(t, addr, "edge-reborn")
	birthW6AndPLC1(t, host, edge, 1)
	baselineScan(t, host)
	writeSpeed(t, host, 11)
	waitUntil(t, "the 11 command", func() bool { return len(w.commands(t)) == 1 })

	publishPayload(t, edge, "spBv1.0/G/NDEATH/W6", ndeath(1))
	waitForValue(t, host, "W6__Online", func(v any) bool { return v == false })
	writeSpeed(t, host, 33)
	waitQueuedOnW6(t, host, 1)
	writeSpeed(t, host, 11)

	publishPayload(t, edge, "spBv1.0/G/NBIRTH/W6", nbirth(2, dbl("Well/Level", 4.2)))
	publishPayload(t, edge, "spBv1.0/G/DBIRTH/W6/PLC1", data(1, boolean("Pump/Run", true), dbl("Pump/SpeedSP", 50)))
	waitForValue(t, host, "W6_PLC1__Online", func(v any) bool { return v == true })
	waitQueuedOnW6(t, host, 0)
	barrier(t, host, w)
	if c := w.commands(t); len(c) != 2 || c[1] != 11.0 {
		t.Fatalf("commands = %v, want [11 11]: the reborn PLC1 reports 50 and the program wants 11", c)
	}
}

// A command in flight when the program moves the tag back (#940): the
// flush has taken 22 out of pending when the program writes 11, which the
// site holds, so there is nothing to withdraw; 22 goes out. The site then
// holds 22 while the program wants 11, so 11 must follow. markWritten used
// to record 22 as the program's value too, and 11 never went.
func TestInFlightCommandIsFollowedByTheProgramsValue(t *testing.T) {
	srv, addr := startBroker(t, "")
	t.Cleanup(func() { _ = closeBroker(srv) })
	w := watchHost(t, addr, "watch-inflight", "h-inflight")
	host := startHostDriver(t, testManifest(), testConfig(addr, "h-inflight"))
	waitConnected(t, host)
	edge := edgePublisher(t, addr, "edge-inflight")
	birthW6AndPLC1(t, host, edge, 1)
	baselineScan(t, host)
	writeSpeed(t, host, 11)
	waitUntil(t, "the 11 command", func() bool { return len(w.commands(t)) == 1 })

	var once sync.Once
	hook := func() { once.Do(func() { writeSpeed(t, host, 11) }) }
	testHookAfterSwap.Store(&hook)
	t.Cleanup(func() { testHookAfterSwap.Store(nil) })
	writeSpeed(t, host, 22)
	waitUntil(t, "the program's 11 after the in-flight 22", func() bool {
		c := w.commands(t)
		return len(c) == 3
	})
	barrier(t, host, w)
	if c := w.commands(t); len(c) != 3 || c[1] != 22.0 || c[2] != 11.0 {
		t.Fatalf("commands = %v, want [11 22 11]: 22 was in flight, then the program's 11", c)
	}
}

// Parking a command superseded while its flush was in flight parks
// nothing, and owes the program's value unless the site holds it.
func TestParkSkipsASupersededCommand(t *testing.T) {
	d, _ := newTestDriver(t, testManifest(), Config{})
	d.initWriteMapsLocked()
	const name = "W6_PLC1_Pump_SpeedSP"
	v22, v11 := d.canonWrite(name, 22.0), d.canonWrite(name, 11.0)

	d.lastOut[name] = v11
	d.queueOffline(map[string]map[string]any{"W6": {name: v22}}, d.termGen.Load())
	if q := d.queued["W6"][name]; q != nil {
		t.Fatalf("parked %v: 22 was superseded by 11", q)
	}
	if !sameValue(d.pending[name], v11) {
		t.Fatalf("pending = %v, want the program's 11 owed", d.pending[name])
	}

	delete(d.pending, name)
	d.remote[name] = v11 // the site holds it: nothing is owed
	d.queueOffline(map[string]map[string]any{"W6": {name: v22}}, d.termGen.Load())
	if _, owed := d.pending[name]; owed || d.queued["W6"][name] != nil {
		t.Fatalf("pending %v, parked %v: the site holds the program's 11", d.pending[name], d.queued["W6"][name])
	}
}

// A revert to what the site holds withdraws a command parked for a dark
// site, not only a pending one.
func TestRevertWithdrawsAParkedCommand(t *testing.T) {
	d, _ := newTestDriver(t, testManifest(), Config{})
	d.initWriteMapsLocked()
	const name = "W6_PLC1_Pump_SpeedSP"
	d.armBaseline()
	if err := d.WriteOutputs(map[string]any{name: 0.0}); err != nil { // baseline
		t.Fatal(err)
	}
	d.remote[name] = d.canonWrite(name, 11.0)
	d.lastOut[name] = d.canonWrite(name, 33.0)
	d.queued = map[string]map[string]any{"W6": {name: d.canonWrite(name, 33.0)}}
	if err := d.WriteOutputs(map[string]any{name: 11.0}); err != nil {
		t.Fatal(err)
	}
	if v, parked := d.queued["W6"][name]; parked {
		t.Fatalf("still parked: %v; the program went back to the 11 the site holds", v)
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
	d.requeueLocked([]string{name}, work, d.termGen.Load())
	if v, ok := d.pending[name]; ok {
		t.Fatalf("pending = %v: 22 was superseded by 11 and must not be requeued", v)
	}

	d.lastOut[name] = work[name] // still wanted
	d.requeueLocked([]string{name}, work, d.termGen.Load())
	if v, ok := d.pending[name]; !ok || !sameValue(v, work[name]) {
		t.Fatalf("pending = %v, want the still-wanted 22 requeued", v)
	}

	newer := d.canonWrite(name, 33.0)
	d.pending[name] = newer
	d.requeueLocked([]string{name}, work, d.termGen.Load())
	if !sameValue(d.pending[name], newer) {
		t.Fatalf("pending = %v, want the newer 33 kept", d.pending[name])
	}
}

// NaN is a value like any other here: written once, it is sent once.
// irEqual used NaN != NaN, so a NaN marked written was owed again and
// re-sent every CommandInterval forever, and one for a dark site was
// re-owed instead of parked.
func TestNaNIsTheSameAsNaN(t *testing.T) {
	nan := ir.RealVal(math.NaN())
	if !irEqual(nan, nan) {
		t.Fatal("irEqual(NaN, NaN) = false: a NaN command would be owed forever")
	}
	if irEqual(nan, ir.RealVal(1)) || irEqual(ir.RealVal(1), nan) {
		t.Fatal("NaN equals 1")
	}
	s := ir.Value{Kind: ir.TypeStruct, Fld: []ir.Value{ir.RealVal(1), nan}} // irEqual compares members
	if !irEqual(s, s) {
		t.Fatal("a struct with a NaN member does not equal itself")
	}

	d, _ := newTestDriver(t, testManifest(), Config{})
	d.log = quietLogger() // queueOffline logs what it parks
	d.initWriteMapsLocked()
	const name = "W6_PLC1_Pump_SpeedSP"
	v := d.canonWrite(name, math.NaN())
	d.lastOut[name] = v
	d.queueOffline(map[string]map[string]any{"W6": {name: v}}, d.termGen.Load())
	if _, parked := d.queued["W6"][name]; !parked {
		t.Fatal("a NaN for a dark site was not parked")
	}
	if p, owed := d.pending[name]; owed {
		t.Fatalf("a NaN for a dark site is owed again: pending %v", p)
	}
}

func TestNaNCommandIsSentOnce(t *testing.T) {
	srv, addr := startBroker(t, "")
	t.Cleanup(func() { _ = closeBroker(srv) })
	w := watchHost(t, addr, "watch-nan", "h-nan")
	host := startHostDriver(t, testManifest(), testConfig(addr, "h-nan"))
	waitConnected(t, host)
	edge := edgePublisher(t, addr, "edge-nan")
	birthW6AndPLC1(t, host, edge, 1)
	baselineScan(t, host)

	writeSpeed(t, host, math.NaN())
	waitUntil(t, "the NaN command", func() bool { return len(w.commands(t)) == 1 })
	// Many coalesce windows (testConfig: 20 ms), with no further write.
	time.Sleep(15 * testConfig(addr, "h-nan").CommandInterval) // a negative check: nothing more may go out
	barrier(t, host, w)
	if c := w.commands(t); len(c) != 1 {
		t.Fatalf("%d commands for one NaN write, want 1", len(c))
	}
}
