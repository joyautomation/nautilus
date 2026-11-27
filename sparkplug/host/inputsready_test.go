package host

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/sparkplug"
	"github.com/joyautomation/nautilus/sparkplug/internal/linkcut"
)

// InputsReady (io.InputsReadier, #786): a node is a source; delivered is
// its NBIRTH plus its bound devices' DBIRTHs, failed is silence for
// birthWait after its rebirth request, or an unreachable broker.

func waitReady(t *testing.T, d *Driver, what string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if d.InputsReady() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("InputsReady never became true: %s", what)
}

// newReadyDriver builds a driver with a chosen birthWait, not yet started.
func newReadyDriver(t *testing.T, addr, id string, wait time.Duration) *Driver {
	t.Helper()
	d, err := New(testManifest(), testConfig(addr, id), WithLogger(quietLogger()))
	if err != nil {
		t.Fatal(err)
	}
	d.birthWait = wait
	return d
}

func startReady(t *testing.T, d *Driver) {
	t.Helper()
	d.Start(context.Background())
	t.Cleanup(d.Stop)
}

func TestInputsReadyOnTheNodeAndItsDeviceBirthing(t *testing.T) {
	srv, addr := startBroker(t, "")
	t.Cleanup(func() { _ = closeBroker(srv) })
	d := newReadyDriver(t, addr, "h-ready-births", time.Hour) // silence never counts as failed here
	if d.InputsReady() {
		t.Fatal("ready before Start")
	}
	startReady(t, d)
	waitConnected(t, d)
	time.Sleep(50 * time.Millisecond)
	if d.InputsReady() {
		t.Fatal("ready with no birth delivered")
	}
	edge := edgePublisher(t, addr, "edge-ready-births")
	publishPayload(t, edge, "spBv1.0/G/NBIRTH/W6", sparkplug.Payload{
		Timestamp: uint64(time.Now().UnixMilli()),
		Metrics:   []sparkplug.Metric{bdSeq(1), dbl("Well/Level", 4.2)},
	})
	waitForValue(t, d, "W6__Online", func(v any) bool { return v == true })
	if d.InputsReady() {
		t.Fatal("ready on the NBIRTH alone, with a bound device not yet born")
	}
	publishPayload(t, edge, "spBv1.0/G/DBIRTH/W6/PLC1", sparkplug.Payload{
		Timestamp: uint64(time.Now().UnixMilli()), Seq: 1,
		Metrics: []sparkplug.Metric{boolean("Pump/Run", true)},
	})
	waitReady(t, d, "after the NBIRTH and the DBIRTH")
}

func TestInputsReadyWhenARequestedNodeStaysSilent(t *testing.T) {
	srv, addr := startBroker(t, "")
	t.Cleanup(func() { _ = closeBroker(srv) })
	d := newReadyDriver(t, addr, "h-ready-silent", 300*time.Millisecond)
	startReady(t, d)
	waitConnected(t, d)
	if d.InputsReady() {
		t.Fatal("ready before the silent node's wait is over")
	}
	waitReady(t, d, "birthWait after the rebirth request, no answer")
}

func TestInputsReadyOnAnUnreachableBroker(t *testing.T) {
	srv, addr := startBroker(t, "")
	t.Cleanup(func() { _ = closeBroker(srv) })
	px := linkcut.New(t, addr)
	px.Cut()
	d := newReadyDriver(t, px.Addr(), "h-ready-nobroker", time.Hour)
	if d.InputsReady() {
		t.Fatal("ready before Start")
	}
	startReady(t, d)
	waitReady(t, d, "after the first connect failed")
}

func TestInputsReadyStaysTrueAfterTheBrokerIsLost(t *testing.T) {
	srv, addr := startBroker(t, "")
	t.Cleanup(func() { _ = closeBroker(srv) })
	px := linkcut.New(t, addr)
	d := newReadyDriver(t, px.Addr(), "h-ready-sticky", time.Hour)
	startReady(t, d)
	waitConnected(t, d)
	birthW6AndPLC1(t, d, edgePublisher(t, addr, "edge-ready-sticky"), 1)
	waitReady(t, d, "after the births")
	px.Cut()
	waitDisconnected(t, d)
	if !d.InputsReady() {
		t.Fatal("InputsReady went false after the broker was lost")
	}
}

func TestInputsReadyWithNoDataBindings(t *testing.T) {
	m := testManifest()
	m.Tags = nil
	d, err := New(m, testConfig("127.0.0.1:1", "h-ready-none"), WithLogger(quietLogger()))
	if err != nil {
		t.Skipf("a manifest without bindings is refused: %v", err)
	}
	if !d.InputsReady() {
		t.Fatal("a driver with no data bindings must be ready at once")
	}
}

// A birth from a same-named edge in another group is not the bound node's
// (Fable): it does not end the wait; the bound group's does.
func TestInputsReadyCountsBirthsFromTheBoundGroupOnly(t *testing.T) {
	d := newReadyDriver(t, "127.0.0.1:1", "h-ready-group", time.Hour)
	d.mu.Lock()
	d.in.firstConn = time.Now()
	birthAll := func(group func(edge string) string) {
		for edge, devs := range d.in.srcs {
			d.noteBirthLocked(group(edge), edge)
			for dev := range devs {
				d.noteDeviceBirthLocked(group(edge), edge, dev)
			}
		}
	}
	birthAll(func(string) string { return "some-other-group" })
	d.mu.Unlock()
	if d.InputsReady() {
		t.Fatal("ready on births from another group")
	}
	d.mu.Lock()
	birthAll(func(edge string) string {
		g, ok := d.groupFor(edge)
		if !ok {
			t.Fatalf("no group for %s", edge)
		}
		return g
	})
	d.mu.Unlock()
	if !d.InputsReady() {
		t.Fatal("not ready after the bound group's births")
	}
}

// A node the start sweep has not asked yet is timed from the session, not
// from the sweep's end: a large fleet's sweep must not hold every start
// past the runtime's 2 s wait (Fable).
func TestInputsReadyDoesNotWaitOutALargeFleetsSweep(t *testing.T) {
	cfg := testConfig("127.0.0.1:1", "h-ready-fleet")
	cfg.RebirthOnStart = true
	d, err := New(testManifest(), cfg, WithLogger(quietLogger()))
	if err != nil {
		t.Fatal(err)
	}
	d.birthWait = time.Second
	for i := range 100 { // a fleet: its sweep alone would take 5 s
		d.manifest.Nodes = append(d.manifest.Nodes, Node{EdgeNode: fmt.Sprintf("fleet-%d", i)})
	}
	d.mu.Lock()
	d.in.firstConn = time.Now().Add(-1500 * time.Millisecond)
	d.mu.Unlock()
	if !d.InputsReady() {
		t.Fatal("not ready 1.5 s after the session with no birth: a silent node costs birthWait, not the sweep")
	}
}
