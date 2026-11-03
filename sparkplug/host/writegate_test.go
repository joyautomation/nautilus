package host

// The redundancy write gate (#325, SPH-036): `naut run` hands the elector's
// IsLeader to every driver with a SetWriteGate. For the Sparkplug host that
// gate decides who is the Primary Host Application — STATE — and who may
// command — NCMD/DCMD. A single host never gets a gate and must behave
// exactly as it always has.

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"

	"github.com/joyautomation/nautilus/sparkplug"
)

// startGated builds a driver, installs gate the way naut run does (before
// Start), and starts it.
func startGated(t *testing.T, cfg Config, gate func() bool) *Driver {
	t.Helper()
	d, err := New(testManifest(), cfg, WithLogger(quietLogger()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if gate != nil {
		d.SetWriteGate(gate)
	}
	d.Start(context.Background())
	t.Cleanup(d.Stop)
	return d
}

// stateLog records every STATE certificate published on one host's topic,
// and every command to group G, in arrival order.
type stateLog struct {
	mu   sync.Mutex
	msgs []recMsg
}

func watchHost(t *testing.T, addr, id, hostID string) *stateLog {
	t.Helper()
	l := &stateLog{}
	cli := mqtt.NewClient(mqtt.NewClientOptions().AddBroker(brokerURL(addr)).SetClientID(id).
		SetCleanSession(true).SetOrderMatters(true).
		SetDefaultPublishHandler(func(_ mqtt.Client, m mqtt.Message) {
			l.mu.Lock()
			l.msgs = append(l.msgs, recMsg{topic: m.Topic(), payload: append([]byte(nil), m.Payload()...)})
			l.mu.Unlock()
		}))
	if tok := cli.Connect(); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		t.Fatalf("watcher connect: %v", tok.Error())
	}
	t.Cleanup(func() { cli.Disconnect(50) })
	for _, f := range []string{stateTopic(hostID), "spBv1.0/G/NCMD/+", "spBv1.0/G/DCMD/+/+", "test/barrier"} {
		if tok := cli.Subscribe(f, 1, nil); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
			t.Fatalf("watcher subscribe %s: %v", f, tok.Error())
		}
	}
	return l
}

// states returns the STATE certificates seen so far, decoded.
func (l *stateLog) states(t *testing.T) []stateBody {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []stateBody
	for _, m := range l.msgs {
		if strings.HasPrefix(m.topic, "spBv1.0/STATE/") {
			var s stateBody
			if err := json.Unmarshal(m.payload, &s); err != nil {
				t.Fatalf("decode STATE %q: %v", m.payload, err)
			}
			out = append(out, s)
		}
	}
	return out
}

// commands returns the SpeedSP values commanded so far.
func (l *stateLog) commands(t *testing.T) []any {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []any
	for _, m := range l.msgs {
		if m.topic != "spBv1.0/G/DCMD/W6/PLC1" {
			continue
		}
		p, err := sparkplug.DecodePayload(m.payload)
		if err != nil {
			t.Fatalf("decode DCMD: %v", err)
		}
		for _, mm := range p.Metrics {
			if mm.Name != RebirthMetric {
				out = append(out, mm.Value)
			}
		}
	}
	return out
}

func waitUntil(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// retainedState reads the retained STATE certificate a late-joining edge
// node would get on SUBSCRIBE.
func retainedState(t *testing.T, addr, id, hostID string) stateBody {
	t.Helper()
	got := make(chan []byte, 1)
	cli := mqtt.NewClient(mqtt.NewClientOptions().AddBroker(brokerURL(addr)).SetClientID(id).
		SetCleanSession(true))
	if tok := cli.Connect(); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		t.Fatalf("late edge connect: %v", tok.Error())
	}
	defer cli.Disconnect(50)
	if tok := cli.Subscribe(stateTopic(hostID), 1, func(_ mqtt.Client, m mqtt.Message) {
		select {
		case got <- append([]byte(nil), m.Payload()...):
		default:
		}
	}); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		t.Fatalf("late edge subscribe: %v", tok.Error())
	}
	select {
	case b := <-got:
		return decodeState(t, b)
	case <-time.After(5 * time.Second):
		t.Fatal("no retained STATE on the broker")
		return stateBody{}
	}
}

// writeSpeed hands the driver an output snapshot with the setpoint moved.
func writeSpeed(t *testing.T, d *Driver, v float64) {
	t.Helper()
	if err := d.WriteOutputs(outputSnapshot(d, map[string]any{"W6_PLC1_Pump_SpeedSP": v})); err != nil {
		t.Fatal(err)
	}
}

// barrier publishes a message on the driver's own connection and waits for
// the watcher to see it: everything the driver published before it has
// arrived by then.
func barrier(t *testing.T, d *Driver, l *stateLog) {
	t.Helper()
	d.flushWrites()
	p := d.publisher()
	if p == nil {
		t.Fatal("barrier: driver not connected")
	}
	n := l.count("test/barrier")
	if err := p.publish("test/barrier", 1, false, []byte("b")); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the barrier", func() bool { return l.count("test/barrier") > n })
}

func (l *stateLog) count(topic string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, m := range l.msgs {
		if m.topic == topic {
			n++
		}
	}
	return n
}

// SPH-036, driver level, through SetWriteGate: a standby publishes no STATE
// and no command; on promotion it publishes STATE online and its held
// command; on demotion it stops commanding at once and publishes its STATE
// death certificate — carrying its own session timestamp — and on
// re-promotion it is primary again with a newer one.
func TestWriteGateDrivesStateAndCommands(t *testing.T) {
	srv, addr := startBroker(t, "")
	t.Cleanup(func() { _ = closeBroker(srv) })

	var leader atomic.Bool // a standby to begin with
	cfg := testConfig(addr, "h-gate")
	cfg.NoRebirthOnStart = true
	w := watchHost(t, addr, "watch-gate", "h-gate")
	d := startGated(t, cfg, leader.Load)
	waitConnected(t, d)

	edge := edgePublisher(t, addr, "edge-gate")
	publishPayload(t, edge, "spBv1.0/G/NBIRTH/W6", sparkplug.Payload{
		Timestamp: uint64(time.Now().UnixMilli()),
		Metrics:   []sparkplug.Metric{bdSeq(1)},
	})
	waitForValue(t, d, "W6__Online", func(v any) bool { return v == true })
	baselineScan(t, d)

	// Standby: the operator moves the setpoint; nothing reaches the wire.
	writeSpeed(t, d, 7)
	barrier(t, d, w)
	if s := w.states(t); len(s) != 0 {
		t.Fatalf("a standby published STATE: %+v", s)
	}
	if c := w.commands(t); len(c) != 0 {
		t.Fatalf("a standby sent commands: %v", c)
	}

	// Promotion: STATE online, and the held command goes out.
	leader.Store(true)
	waitUntil(t, "STATE online after promotion", func() bool {
		s := w.states(t)
		return len(s) == 1 && s[0].Online
	})
	first := w.states(t)[0]
	waitUntil(t, "the held command after promotion", func() bool { return len(w.commands(t)) == 1 })

	// Demotion: the next command is held, and the outgoing leader declares
	// itself offline with ITS session timestamp — never a newer one that
	// could outrank a successor's birth.
	leader.Store(false)
	writeSpeed(t, d, 9)
	waitUntil(t, "STATE offline after demotion", func() bool { return len(w.states(t)) == 2 })
	if s := w.states(t)[1]; s.Online || s.Timestamp != first.Timestamp {
		t.Fatalf("demotion certificate = %+v, want offline at the session timestamp %d", s, first.Timestamp)
	}
	waitConnected(t, d) // back as a standby
	barrier(t, d, w)
	if c := w.commands(t); len(c) != 1 {
		t.Fatalf("a demoted leader sent commands: %v", c)
	}
	if got := retainedState(t, addr, "late-gate", "h-gate"); got.Online {
		t.Fatalf("retained STATE after demotion = %+v, want offline", got)
	}

	// Re-promotion: online again, newer session, and the held 9 goes out.
	leader.Store(true)
	waitUntil(t, "STATE online after re-promotion", func() bool { return len(w.states(t)) == 3 })
	if s := w.states(t)[2]; !s.Online || s.Timestamp <= first.Timestamp {
		t.Fatalf("re-promotion certificate = %+v, want online and newer than %d", s, first.Timestamp)
	}
	waitUntil(t, "the held command after re-promotion", func() bool { return len(w.commands(t)) == 2 })
	if c := w.commands(t); c[1] != 9.0 {
		t.Fatalf("commands = %v, want the held 9 last", c)
	}
}

// The single-host guarantee: with no gate (no redundancy: section, so naut
// run never calls SetWriteGate) the host is its own leader. It is primary
// from the first connect, its commands go out, its MQTT client ID is the
// configured one, and a STATE offline somebody else publishes on its topic
// is left alone exactly as before — no replica hand-over logic applies.
func TestNoWriteGateSingleHostIsAlwaysPrimary(t *testing.T) {
	srv, addr := startBroker(t, "")
	t.Cleanup(func() { _ = closeBroker(srv) })

	cfg := testConfig(addr, "h-single")
	cfg.NoRebirthOnStart = true
	w := watchHost(t, addr, "watch-single", "h-single")
	d := startGated(t, cfg, nil)
	waitConnected(t, d)

	if _, ok := srv.Clients.Get("nautilus-host-h-single"); !ok {
		t.Fatal("a single host's client ID changed; want nautilus-host-h-single")
	}
	waitUntil(t, "STATE online", func() bool {
		s := w.states(t)
		return len(s) == 1 && s[0].Online
	})

	edge := edgePublisher(t, addr, "edge-single")
	publishPayload(t, edge, "spBv1.0/G/NBIRTH/W6", sparkplug.Payload{
		Timestamp: uint64(time.Now().UnixMilli()),
		Metrics:   []sparkplug.Metric{bdSeq(1)},
	})
	waitForValue(t, d, "W6__Online", func(v any) bool { return v == true })
	baselineScan(t, d)
	writeSpeed(t, d, 7)
	waitUntil(t, "the command", func() bool { return len(w.commands(t)) == 1 })

	// Someone else's offline certificate on our topic: no re-assert.
	stale, _ := json.Marshal(stateBody{Online: false, Timestamp: 1})
	if tok := edge.Publish(stateTopic("h-single"), 1, true, stale); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		t.Fatal(tok.Error())
	}
	waitUntil(t, "the foreign certificate", func() bool { return len(w.states(t)) >= 2 })
	barrier(t, d, w)
	time.Sleep(300 * time.Millisecond)
	if s := w.states(t); len(s) != 2 {
		t.Fatalf("a single host reacted to a foreign STATE: %+v", s)
	}
}

// A leader whose retained STATE is overwritten by a stale death — the
// outgoing replica's demotion certificate or its will, landing after this
// replica's birth — re-asserts its birth with the same session timestamp,
// so an edge node that connects later still finds its primary host online.
func TestWriteGateLeaderReassertsStateOverAStaleDeath(t *testing.T) {
	srv, addr := startBroker(t, "")
	t.Cleanup(func() { _ = closeBroker(srv) })

	t.Setenv("POD_NAME", "plc-1")
	cfg := testConfig(addr, "h-pair")
	cfg.NoRebirthOnStart = true
	w := watchHost(t, addr, "watch-pair", "h-pair")
	d := startGated(t, cfg, func() bool { return true })
	waitConnected(t, d)
	if _, ok := srv.Clients.Get("nautilus-host-h-pair-plc-1"); !ok {
		t.Fatal("a replica's client ID does not carry its identity; want nautilus-host-h-pair-plc-1")
	}
	waitUntil(t, "STATE online", func() bool { return len(w.states(t)) == 1 })
	birth := w.states(t)[0]

	// The other replica's late certificate: older session, offline, retained.
	other := edgePublisher(t, addr, "nautilus-host-h-pair-plc-0")
	late, _ := json.Marshal(stateBody{Online: false, Timestamp: birth.Timestamp - 5000})
	if tok := other.Publish(stateTopic("h-pair"), 1, true, late); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		t.Fatal(tok.Error())
	}
	// The watcher hears both, in either order: the broker fans the late
	// death out to its subscribers one by one, in map order, so when the
	// leader hears it first, its re-assert can reach the watcher before the
	// death itself does (nautilus#418). The retained STATE below is what
	// settles which came last at the broker.
	waitUntil(t, "the leader to re-assert", func() bool { return len(w.states(t)) == 3 })
	heard := w.states(t)[1:]
	lateState := stateBody{Online: false, Timestamp: birth.Timestamp - 5000}
	if !(heard[0] == lateState && heard[1] == birth) && !(heard[0] == birth && heard[1] == lateState) {
		t.Fatalf("after the late death the watcher heard %+v, want the death %+v and the re-asserted birth %+v", heard, lateState, birth)
	}
	if got := retainedState(t, addr, "late-pair", "h-pair"); got != birth {
		t.Fatalf("retained STATE = %+v, want the leader's birth %+v", got, birth)
	}
}
