package host

import (
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/sparkplug"
	"github.com/joyautomation/nautilus/sparkplug/internal/linkcut"
)

// waitDisconnected blocks until the driver reports its broker session gone.
func waitDisconnected(t *testing.T, d *Driver) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if !d.Status().Connected {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("driver never noticed the broker connection was gone")
}

// The host losing its own broker connection takes every node and device it
// tracks offline (#540): __Online means "the host can reach it now", as for
// every other driver. Each fall counts one outage, and the node's data reads
// Stale at once, as after an NDEATH. The births that follow the reconnect
// bring them back without counting again, and Stop counts nothing.
func TestBrokerLossTakesNodesOfflineAndCountsOnce(t *testing.T) {
	srv, addr := startBroker(t, "")
	t.Cleanup(func() { _ = closeBroker(srv) })
	px := linkcut.New(t, addr) // only the host goes through the cut link

	host := startHostDriver(t, testManifest(), testConfig(px.Addr(), "h-brokerloss"))
	waitConnected(t, host)

	edge := edgePublisher(t, addr, "edge-brokerloss")
	birth := func(bd int64) {
		t.Helper()
		publishPayload(t, edge, "spBv1.0/G/NBIRTH/W6", sparkplug.Payload{
			Timestamp: uint64(time.Now().UnixMilli()),
			Metrics:   []sparkplug.Metric{bdSeq(bd), dbl("Well/Level", 4.2)},
		})
		publishPayload(t, edge, "spBv1.0/G/DBIRTH/W6/PLC1", sparkplug.Payload{
			Timestamp: uint64(time.Now().UnixMilli()), Seq: 1,
			Metrics: []sparkplug.Metric{boolean("Pump/Run", true)},
		})
		waitForValue(t, host, "W6_PLC1__Online", func(v any) bool { return v == true })
	}
	birth(1)
	if got := host.snapshot()["W6__Online"]; got != true {
		t.Fatalf("W6__Online = %#v after the births, want true", got)
	}
	if q := host.Quality(); q["W6_Well_Level"] != nio.Good || q["W6_PLC1_Pump_Run"] != nio.Good {
		t.Fatalf("Quality() = %v after the births, want the bindings Good", q)
	}

	px.Cut()
	waitDisconnected(t, host)
	vals := host.snapshot()
	for name, want := range map[string]any{
		"W6__Online":       false,
		"W6_PLC1__Online":  false,
		"W6__Outages":      int64(1),
		"W6_PLC1__Outages": int64(1),
	} {
		if got := vals[name]; got != want {
			t.Errorf("broker lost: %s = %#v, want %#v", name, got, want)
		}
	}
	q := host.Quality()
	for _, name := range []string{"W6_Well_Level", "W6_PLC1_Pump_Run"} {
		if q[name] != nio.Stale {
			t.Errorf("broker lost: Quality()[%s] = %v, want Stale", name, q[name])
		}
	}
	if vals["W6_Well_Level"] != 4.2 {
		t.Errorf("broker lost: W6_Well_Level = %#v, want the last value 4.2 kept", vals["W6_Well_Level"])
	}

	// The host reconnects (and asks for a rebirth); the edge births again.
	// The node and its device are already counted: the births bring them
	// back without a second outage.
	px.Restore()
	waitConnected(t, host)
	birth(1)
	waitForValue(t, host, "W6__Online", func(v any) bool { return v == true })
	vals = host.snapshot()
	for name, want := range map[string]any{
		"W6__Outages":      int64(1),
		"W6_PLC1__Outages": int64(1),
	} {
		if got := vals[name]; got != want {
			t.Errorf("after the reconnect and rebirth: %s = %#v, want %#v", name, got, want)
		}
	}
	q = host.Quality()
	for _, name := range []string{"W6_Well_Level", "W6_PLC1_Pump_Run"} {
		if q[name] != nio.Good {
			t.Errorf("after the rebirth Quality()[%s] = %v, want Good", name, q[name])
		}
	}

	// A deliberate Stop is not an outage.
	host.Stop()
	vals = host.snapshot()
	if vals["W6__Outages"] != int64(1) || vals["W6_PLC1__Outages"] != int64(1) {
		t.Errorf("Stop counted an outage: W6__Outages = %#v, W6_PLC1__Outages = %#v, want 1 and 1",
			vals["W6__Outages"], vals["W6_PLC1__Outages"])
	}
}

// A host that never reached its broker has nothing to lose: no node was
// online, so nothing counts, however many connect attempts fail.
func TestBrokerNeverReachedCountsNothing(t *testing.T) {
	srv, addr := startBroker(t, "")
	t.Cleanup(func() { _ = closeBroker(srv) })
	px := linkcut.New(t, addr)
	px.Cut()

	host := startHostDriver(t, testManifest(), testConfig(px.Addr(), "h-neverconnected"))
	time.Sleep(1500 * time.Millisecond) // the first attempt and its retry, both refused
	if host.Status().Connected {
		t.Fatal("the host connected through a cut link")
	}
	vals := host.snapshot()
	for name, want := range map[string]any{
		"W6__Online":       false,
		"W6_PLC1__Online":  false,
		"W6__Outages":      int64(0),
		"W6_PLC1__Outages": int64(0),
	} {
		if got := vals[name]; got != want {
			t.Errorf("%s = %#v, want %#v", name, got, want)
		}
	}
	host.Stop()
	if vals := host.snapshot(); vals["W6__Outages"] != int64(0) || vals["W6_PLC1__Outages"] != int64(0) {
		t.Errorf("Stop counted an outage on a host that never connected: %v", vals)
	}
}

// After a broker loss the host has missed whatever the node sent while it
// was away, so the sequence is unprimed: a message the dead session still
// delivers is dropped, and on the new session data that arrives before a
// birth asks for one instead of being applied (a host with
// rebirth-on-start off depends on that to bring its nodes back).
func TestBrokerLossUnprimesTheSequence(t *testing.T) {
	d, rl := newTestDriver(t, testManifest(), Config{})
	d.handleMessage("spBv1.0/G/NBIRTH/W6", enc(t, nbirth(1, dbl("Well/Level", 1))))
	d.handleMessage("spBv1.0/G/DBIRTH/W6/PLC1", enc(t, data(1, boolean("Pump/Run", false))))

	d.mu.Lock()
	d.brokerLostLocked()
	d.mu.Unlock()

	// Still delivered by the dropped session: not applied.
	d.handleMessage("spBv1.0/G/NBIRTH/W6", enc(t, nbirth(1, dbl("Well/Level", 2))))
	if vals := d.snapshot(); vals["W6__Online"] != false || vals["W6_Well_Level"] != 1.0 {
		t.Fatalf("a message from the dropped session was applied: W6__Online = %#v, W6_Well_Level = %#v",
			vals["W6__Online"], vals["W6_Well_Level"])
	}

	// The next CONNECT clears it (connect does this).
	d.mu.Lock()
	d.brokerLost = false
	d.mu.Unlock()
	d.handleMessage("spBv1.0/G/NDATA/W6", enc(t, data(2, dbl("Well/Level", 3))))
	if nk := rl.wait(t, time.Second); nk.EdgeNode != "W6" {
		t.Fatalf("rebirth asked for %+v, want W6", nk)
	}
	if vals := d.snapshot(); vals["W6__Online"] != false || vals["W6_Well_Level"] != 1.0 {
		t.Fatalf("data before the birth was applied: W6__Online = %#v, W6_Well_Level = %#v",
			vals["W6__Online"], vals["W6_Well_Level"])
	}

	d.handleMessage("spBv1.0/G/NBIRTH/W6", enc(t, nbirth(2, dbl("Well/Level", 4))))
	d.handleMessage("spBv1.0/G/DBIRTH/W6/PLC1", enc(t, data(1, boolean("Pump/Run", true))))
	vals := d.snapshot()
	for name, want := range map[string]any{
		"W6__Online": true, "W6_PLC1__Online": true, "W6_Well_Level": 4.0,
		"W6__Outages": int64(1), "W6_PLC1__Outages": int64(1),
	} {
		if vals[name] != want {
			t.Errorf("after the rebirth %s = %#v, want %#v", name, vals[name], want)
		}
	}
}
