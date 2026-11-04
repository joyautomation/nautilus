package host

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	mqttsrv "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/packets"

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

// birthW6AndPLC1 publishes W6's NBIRTH (bdSeq bd, Well/Level 4.2) and PLC1's
// DBIRTH (Pump/Run true), the way an edge answers a rebirth request, and
// waits for the host to see the device online.
func birthW6AndPLC1(t *testing.T, host *Driver, edge mqtt.Client, bd int64) {
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
	birth := func(bd int64) { t.Helper(); birthW6AndPLC1(t, host, edge, bd) }
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
	d.sessionDown = false
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

// A leader whose link stalls (bytes stop, no FIN or RST) can lose its lease
// before paho notices the dead broker. serve then ends on the leadership
// change, not on a lost connection, and every reconnect fails. Those failed
// reconnects are what tell the host it is cut off: every site goes offline
// and counts once, instead of reading online and Good for the whole
// partition.
func TestPartitionedLeaderTakesNodesOffline(t *testing.T) {
	srv, addr := startBroker(t, "")
	t.Cleanup(func() { _ = closeBroker(srv) })
	px := linkcut.New(t, addr)

	var leader atomic.Bool
	leader.Store(true)
	cfg := testConfig(px.Addr(), "h-partition")
	cfg.Leader = leader.Load
	host := startHostDriver(t, testManifest(), cfg)
	waitConnected(t, host)
	birthW6AndPLC1(t, host, edgePublisher(t, addr, "edge-partition"), 1)

	px.Stall()
	leader.Store(false)

	// leaderPoll, then the demotion death's publish times out on the stalled
	// link (tokenTimeout), then the reconnect is refused.
	deadline := time.Now().Add(30 * time.Second)
	for host.snapshot()["W6__Online"] != false {
		if time.Now().After(deadline) {
			vals := host.snapshot()
			t.Fatalf("partitioned and demoted: W6__Online = %#v, W6__Outages = %#v; want false and 1",
				vals["W6__Online"], vals["W6__Outages"])
		}
		time.Sleep(20 * time.Millisecond)
	}
	vals := host.snapshot()
	for name, want := range map[string]any{
		"W6_PLC1__Online": false, "W6__Outages": int64(1), "W6_PLC1__Outages": int64(1),
	} {
		if vals[name] != want {
			t.Errorf("partitioned: %s = %#v, want %#v", name, vals[name], want)
		}
	}
	if q := host.Quality(); q["W6_Well_Level"] != nio.Stale {
		t.Errorf("partitioned: Quality()[W6_Well_Level] = %v, want Stale", q["W6_Well_Level"])
	}

	// The next failed attempt (backoff 1 s) counts nothing more.
	time.Sleep(2500 * time.Millisecond)
	if vals := host.snapshot(); vals["W6__Outages"] != int64(1) || vals["W6_PLC1__Outages"] != int64(1) {
		t.Errorf("repeated failed reconnects counted again: W6__Outages = %#v, W6_PLC1__Outages = %#v",
			vals["W6__Outages"], vals["W6_PLC1__Outages"])
	}
}

// killOnSubscribe drops a client's connection when it subscribes to a filter
// containing match, after hold: a CONNECT that succeeds and a SUBSCRIBE that
// fails part way, with the earlier subscriptions live for hold.
type killOnSubscribe struct {
	mqttsrv.HookBase
	match string
	hold  time.Duration
}

func (h *killOnSubscribe) ID() string { return "kill-on-subscribe" }

func (h *killOnSubscribe) Provides(b byte) bool { return b == mqttsrv.OnSubscribe }

func (h *killOnSubscribe) OnSubscribe(cl *mqttsrv.Client, pk packets.Packet) packets.Packet {
	for _, f := range pk.Filters {
		if strings.Contains(f.Filter, h.match) {
			time.Sleep(h.hold)
			cl.Stop(packets.ErrNotAuthorized)
		}
	}
	return pk
}

// A session that never finishes subscribing is not one the host can trust:
// a birth it delivers meanwhile must not bring a node online, since the host
// is about to lose the session again.
func TestHalfOpenSessionAppliesNothing(t *testing.T) {
	srv, addr := startBroker(t, "")
	t.Cleanup(func() { _ = closeBroker(srv) })
	if err := srv.AddHook(&killOnSubscribe{match: "/DDEATH/", hold: 300 * time.Millisecond}, nil); err != nil {
		t.Fatal(err)
	}

	host := startHostDriver(t, testManifest(), testConfig(addr, "h-halfopen"))
	edge := edgePublisher(t, addr, "edge-halfopen")

	// Births throughout the first attempt and its retry (backoff 1 s).
	end := time.Now().Add(2500 * time.Millisecond)
	for time.Now().Before(end) {
		publishPayload(t, edge, "spBv1.0/G/NBIRTH/W6", sparkplug.Payload{
			Timestamp: uint64(time.Now().UnixMilli()),
			Metrics:   []sparkplug.Metric{bdSeq(1)},
		})
		if v := host.snapshot()["W6__Online"]; v != false {
			t.Fatalf("a half-open session brought W6 online: W6__Online = %#v", v)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if host.Status().Connected {
		t.Fatal("the host reports connected though every SUBSCRIBE failed")
	}
	if v := host.snapshot()["W6__Outages"]; v != int64(0) {
		t.Errorf("W6__Outages = %#v, want 0", v)
	}
}

// darkW6WithParkedCommand births W6 and PLC1, kills W6 with an NDEATH, and
// moves PLC1's setpoint to 33 while it is dark, so the command is parked.
// It returns the edge and the DCMD observer.
func darkW6WithParkedCommand(t *testing.T, host *Driver, addr, id string) (mqtt.Client, func() []cmdMsg) {
	t.Helper()
	get := cmdObserver(t, addr, "spBv1.0/G/DCMD/+/+")
	edge := edgePublisher(t, addr, id)
	birthW6AndPLC1(t, host, edge, 1)
	baselineScan(t, host)

	publishPayload(t, edge, "spBv1.0/G/NDEATH/W6", ndeath(1))
	waitForValue(t, host, "W6__Online", func(v any) bool { return v == false })
	if err := host.WriteOutputs(map[string]any{"W6_PLC1_Pump_SpeedSP": 33.0}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for host.Status().Nodes[0].QueuedWrites != 1 {
		if time.Now().After(deadline) {
			t.Fatalf("the command was not parked: %+v", host.Status().Nodes)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return edge, get
}

// speedSPs is every Pump/SpeedSP value the DCMDs observed so far carried.
func speedSPs(t *testing.T, get func() []cmdMsg) []any {
	t.Helper()
	var out []any
	for _, m := range get() {
		p, err := sparkplug.DecodePayload(m.payload)
		if err != nil {
			t.Fatal(err)
		}
		for _, mm := range p.Metrics {
			if mm.Name == "Pump/SpeedSP" {
				out = append(out, mm.Value)
			}
		}
	}
	return out
}

// A command parked for a dark device waits for that device's DBIRTH, not
// just its node's NBIRTH: the NBIRTH darks every device until its own birth,
// and only the DBIRTH says what the device holds. Every broker loss parks
// commands this way, so it matters on every reconnect.
func TestParkedDeviceCommandWaitsForItsDBIRTH(t *testing.T) {
	srv, addr := startBroker(t, "")
	t.Cleanup(func() { _ = closeBroker(srv) })
	host := startHostDriver(t, testManifest(), testConfig(addr, "h-parked-device"))
	waitConnected(t, host)
	edge, get := darkW6WithParkedCommand(t, host, addr, "edge-parked-device")

	publishPayload(t, edge, "spBv1.0/G/NBIRTH/W6", nbirth(2))
	waitForValue(t, host, "W6__Online", func(v any) bool { return v == true })
	time.Sleep(300 * time.Millisecond)
	if got := speedSPs(t, get); len(got) != 0 {
		t.Fatalf("the parked DCMD went out on the NBIRTH, before PLC1's DBIRTH: %v", got)
	}

	publishPayload(t, edge, "spBv1.0/G/DBIRTH/W6/PLC1", data(1, boolean("Pump/Run", true)))
	waitCmd(t, get, "the parked DCMD after the DBIRTH", "spBv1.0/G/DCMD/W6/PLC1")
	if got := speedSPs(t, get); len(got) != 1 || got[0] != 33.0 {
		t.Fatalf("DCMD Pump/SpeedSP values = %v, want [33]", got)
	}
}

// A newer command that goes out while the device is still dark supersedes
// the parked one, which must not follow it at the DBIRTH.
func TestNewerCommandSupersedesAParkedOne(t *testing.T) {
	srv, addr := startBroker(t, "")
	t.Cleanup(func() { _ = closeBroker(srv) })
	host := startHostDriver(t, testManifest(), testConfig(addr, "h-supersede"))
	waitConnected(t, host)
	edge, get := darkW6WithParkedCommand(t, host, addr, "edge-supersede")

	publishPayload(t, edge, "spBv1.0/G/NBIRTH/W6", nbirth(2))
	waitForValue(t, host, "W6__Online", func(v any) bool { return v == true })
	if err := host.WriteOutputs(map[string]any{"W6_PLC1_Pump_SpeedSP": 44.0}); err != nil {
		t.Fatal(err)
	}
	waitCmd(t, get, "the newer DCMD", "spBv1.0/G/DCMD/W6/PLC1")

	publishPayload(t, edge, "spBv1.0/G/DBIRTH/W6/PLC1", data(1, boolean("Pump/Run", true)))
	waitForValue(t, host, "W6_PLC1__Online", func(v any) bool { return v == true })
	time.Sleep(400 * time.Millisecond)
	if got := speedSPs(t, get); len(got) != 1 || got[0] != 44.0 {
		t.Fatalf("DCMD Pump/SpeedSP values = %v, want [44] (the parked 33 is superseded)", got)
	}
	if n := host.Status().Nodes[0].QueuedWrites; n != 0 {
		t.Errorf("QueuedWrites = %d after the DBIRTH, want 0", n)
	}
}
