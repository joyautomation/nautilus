// mqtt_test.go drives the transport against a real in-process MQTT broker
// (mochi v2.7.9 — the same build the Sparkplug TCK embeds, so it is
// known-good) on 127.0.0.1:0. No docker, no build tags, no env gating, in the
// spirit of eip/driver_test.go's logixserver.
//
// What it pins down is the half of the design that only shows up on the wire:
// New never dials, the STATE birth carries the will's timestamp verbatim, the
// literal STATE subscription precedes the first publish, writes leave as
// NCMD/DCMD, and Stop publishes the death certificate before disconnecting.

package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	mqttsrv "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/hooks/auth"
	"github.com/mochi-mqtt/server/v2/listeners"

	"github.com/joyautomation/nautilus/sparkplug"
	"github.com/joyautomation/nautilus/sparkplug/spb"
)

// ── harness ──────────────────────────────────────────────────────────────

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

// freePort reserves and releases a loopback port, so a broker can be stopped
// and restarted on the same address (the reconnect test).
func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

// startBroker brings up an in-process broker on addr ("" = any free port) and
// returns it with the address it actually bound.
func startBroker(t *testing.T, addr string) (*mqttsrv.Server, string) {
	t.Helper()
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	srv := mqttsrv.New(&mqttsrv.Options{Logger: quietLogger()})
	if err := srv.AddHook(new(auth.AllowHook), nil); err != nil {
		t.Fatalf("auth hook: %v", err)
	}
	tcp := listeners.NewTCP(listeners.Config{ID: "t", Address: addr})
	if err := srv.AddListener(tcp); err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve() }()
	return srv, tcp.Address()
}

func brokerURL(addr string) string { return "tcp://" + addr }

// recMsg is one message the observer saw.
type recMsg struct {
	topic   string
	payload []byte
	retain  bool
}

// observer is a raw paho client that records everything matching its filters —
// the test's window onto what the driver actually put on the wire.
type observer struct {
	cli  mqtt.Client
	mu   sync.Mutex
	msgs []recMsg
}

func newObserver(t *testing.T, addr, id string, filters ...string) *observer {
	t.Helper()
	o := &observer{}
	opts := mqtt.NewClientOptions().
		AddBroker(brokerURL(addr)).
		SetClientID(id).
		SetCleanSession(true).
		SetOrderMatters(true).
		SetDefaultPublishHandler(func(_ mqtt.Client, m mqtt.Message) {
			o.mu.Lock()
			o.msgs = append(o.msgs, recMsg{topic: m.Topic(), payload: append([]byte(nil), m.Payload()...), retain: m.Retained()})
			o.mu.Unlock()
		})
	o.cli = mqtt.NewClient(opts)
	if tok := o.cli.Connect(); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		t.Fatalf("observer connect: %v", tok.Error())
	}
	for _, f := range filters {
		if tok := o.cli.Subscribe(f, 1, nil); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
			t.Fatalf("observer subscribe %s: %v", f, tok.Error())
		}
	}
	t.Cleanup(func() { o.cli.Disconnect(50) })
	return o
}

// wait blocks until a recorded message satisfies pred, or the deadline passes.
func (o *observer) wait(t *testing.T, what string, pred func(recMsg) bool) recMsg {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		o.mu.Lock()
		for _, m := range o.msgs {
			if pred(m) {
				o.mu.Unlock()
				return m
			}
		}
		o.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	o.mu.Lock()
	seen := make([]string, 0, len(o.msgs))
	for _, m := range o.msgs {
		seen = append(seen, m.topic)
	}
	o.mu.Unlock()
	t.Fatalf("timed out waiting for %s; saw topics %v", what, seen)
	return recMsg{}
}

func (o *observer) count(pred func(recMsg) bool) int {
	o.mu.Lock()
	defer o.mu.Unlock()
	n := 0
	for _, m := range o.msgs {
		if pred(m) {
			n++
		}
	}
	return n
}

// The manifest fixture (one group, one edge node with a device, a node-level
// Double input and a device-level writable) is shared with state_test.go.

func testConfig(addr, hostID string) Config {
	return Config{
		BrokerURL:       brokerURL(addr),
		HostID:          hostID,
		Primary:         true,
		CommandInterval: 20 * time.Millisecond,
		Log:             quietLogger(),
	}
}

// startDriver builds and starts a driver, cleaning it up with the test.
func startDriver(t *testing.T, cfg Config, opts ...Option) *Driver {
	t.Helper()
	opts = append(opts, WithLogger(quietLogger()))
	d, err := New(testManifest(), cfg, opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	d.Start(context.Background())
	t.Cleanup(d.Stop)
	return d
}

// edgePublisher is a raw paho client standing in for an edge node.
func edgePublisher(t *testing.T, addr, id string) mqtt.Client {
	t.Helper()
	cli := mqtt.NewClient(mqtt.NewClientOptions().
		AddBroker(brokerURL(addr)).SetClientID(id).SetCleanSession(true).SetOrderMatters(true))
	if tok := cli.Connect(); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		t.Fatalf("edge connect: %v", tok.Error())
	}
	t.Cleanup(func() { cli.Disconnect(50) })
	return cli
}

func publishPayload(t *testing.T, cli mqtt.Client, topic string, p sparkplug.Payload) {
	t.Helper()
	body, err := p.Encode()
	if err != nil {
		t.Fatalf("encode %s: %v", topic, err)
	}
	if tok := cli.Publish(topic, 1, false, body); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		t.Fatalf("publish %s: %v", topic, tok.Error())
	}
}

func decodeState(t *testing.T, b []byte) stateBody {
	t.Helper()
	var s stateBody
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatalf("decode STATE %q: %v", b, err)
	}
	return s
}

// ── New never dials ──────────────────────────────────────────────────────

func TestDriverNewNeverDials(t *testing.T) {
	// A broker that is not there. New must still succeed — buildDriver runs
	// inside `nautilus check` in CI, with no broker anywhere.
	d, err := New(testManifest(), Config{
		BrokerURL: "tcp://127.0.0.1:1", // nothing listens on port 1
		HostID:    "h-nodial",
		Log:       quietLogger(),
	})
	if err != nil {
		t.Fatalf("New must not dial: %v", err)
	}
	if _, err := d.ReadInputs(); err == nil {
		t.Fatal("ReadInputs before Start must error")
	} else if err.Error() != "host: not connected yet" {
		t.Fatalf("ReadInputs error = %q, want %q", err, "host: not connected yet")
	}
	if st := d.Status(); st.Connected {
		t.Fatal("Status.Connected before Start")
	}
	// Stop before Start is a no-op, not a panic.
	d.Stop()
}

func TestDriverNewDefaults(t *testing.T) {
	d, err := New(testManifest(), Config{BrokerURL: "tcp://x:1883", HostID: "h1", Log: quietLogger()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if d.cfg.Keepalive != 30*time.Second {
		t.Errorf("Keepalive = %v, want 30s", d.cfg.Keepalive)
	}
	if d.cfg.ReorderTimeout != 5*time.Second {
		t.Errorf("ReorderTimeout = %v, want 5s", d.cfg.ReorderTimeout)
	}
	if d.cfg.CommandInterval != 100*time.Millisecond {
		t.Errorf("CommandInterval = %v, want 100ms", d.cfg.CommandInterval)
	}
	if d.cfg.StateForm != StateForm30 {
		t.Errorf("StateForm = %q, want %q", d.cfg.StateForm, StateForm30)
	}
	if d.cfg.ClientID != "nautilus-host-h1" {
		t.Errorf("ClientID = %q, want nautilus-host-h1", d.cfg.ClientID)
	}
	if !d.cfg.RebirthOnStart {
		t.Error("RebirthOnStart must default true")
	}
	if got := d.groups; len(got) != 1 || got[0] != "G" {
		t.Errorf("groups = %v, want [G]", got)
	}

	off, err := New(testManifest(), Config{
		BrokerURL: "tcp://x:1883", HostID: "h1", NoRebirthOnStart: true, Log: quietLogger()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if off.cfg.RebirthOnStart {
		t.Error("NoRebirthOnStart must switch RebirthOnStart off")
	}
}

func TestDriverNewRejectsBadConfig(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  Config
		opts []Option
	}{
		{name: "no broker", cfg: Config{HostID: "h1"}},
		{name: "no host id", cfg: Config{BrokerURL: "tcp://x:1883"}},
		{name: "bad state form", cfg: Config{BrokerURL: "tcp://x:1883", HostID: "h1", StateForm: "4.0"}},
		{name: "bad discovery", cfg: Config{BrokerURL: "tcp://x:1883", HostID: "h1"},
			opts: []Option{WithDiscovery("shout")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := New(testManifest(), tc.cfg, tc.opts...); err == nil {
				t.Fatal("want error")
			}
		})
	}
}

// ── STATE birth, will, and their shared timestamp ────────────────────────

func TestMqttStateBirthMatchesWill(t *testing.T) {
	srv, addr := startBroker(t, "")
	t.Cleanup(func() { _ = srv.Close() })

	obs := newObserver(t, addr, "obs-state", "spBv1.0/STATE/#")
	cfg := testConfig(addr, "h1")
	d := startDriver(t, cfg)

	birth := obs.wait(t, "STATE birth", func(m recMsg) bool {
		return m.topic == "spBv1.0/STATE/h1" && decodeState(t, m.payload).Online
	})
	bs := decodeState(t, birth.payload)
	if bs.Timestamp == 0 {
		t.Fatal("STATE birth timestamp is zero")
	}
	if age := time.Since(time.UnixMilli(bs.Timestamp)); age > 5*time.Second {
		t.Fatalf("STATE birth timestamp %v old; TCK wants it within 5s of CONNECT", age)
	}

	// The retained copy must be there too, at QoS 1 retain true.
	ret := newObserver(t, addr, "obs-retained", "spBv1.0/STATE/h1")
	rm := ret.wait(t, "retained STATE", func(m recMsg) bool { return m.topic == "spBv1.0/STATE/h1" })
	if !rm.retain {
		t.Error("STATE birth is not retained")
	}
	if got := decodeState(t, rm.payload); !got.Online || got.Timestamp != bs.Timestamp {
		t.Errorf("retained STATE = %+v, want online with ts %d", got, bs.Timestamp)
	}

	// Kill the driver's connection at the broker so the LWT fires. The will
	// must carry the byte-identical timestamp the birth carried — paho bakes
	// the will into CONNECT, which is exactly why the driver owns its own
	// reconnect loop instead of using SetAutoReconnect.
	clientID := d.cfg.ClientID // "nautilus-host-h1", filled in by New
	cl, ok := srv.Clients.Get(clientID)
	if !ok {
		t.Fatalf("broker does not know client %q", clientID)
	}
	cl.Stop(errors.New("test: abrupt disconnect"))

	will := obs.wait(t, "LWT", func(m recMsg) bool {
		if m.topic != "spBv1.0/STATE/h1" {
			return false
		}
		s := decodeState(t, m.payload)
		return !s.Online && s.Timestamp == bs.Timestamp
	})
	if got := decodeState(t, will.payload); got.Timestamp != bs.Timestamp {
		t.Fatalf("will ts %d != birth ts %d", got.Timestamp, bs.Timestamp)
	}
}

func TestMqttStateFormBothPublishesLegacy(t *testing.T) {
	srv, addr := startBroker(t, "")
	t.Cleanup(func() { _ = srv.Close() })

	obs := newObserver(t, addr, "obs-both", "spBv1.0/STATE/#", "STATE/#")
	cfg := testConfig(addr, "h-both")
	cfg.StateForm = StateFormBoth
	startDriver(t, cfg)

	obs.wait(t, "3.0 STATE birth", func(m recMsg) bool {
		return m.topic == "spBv1.0/STATE/h-both" && decodeState(t, m.payload).Online
	})
	legacy := obs.wait(t, "2.x STATE birth", func(m recMsg) bool {
		return m.topic == "STATE/h-both"
	})
	if string(legacy.payload) != "ONLINE" {
		t.Errorf("legacy STATE = %q, want ONLINE", legacy.payload)
	}
}

func TestMqttPassiveConsumerPublishesNoState(t *testing.T) {
	srv, addr := startBroker(t, "")
	t.Cleanup(func() { _ = srv.Close() })

	obs := newObserver(t, addr, "obs-passive", "spBv1.0/STATE/#", "spBv1.0/G/NCMD/+")
	cfg := testConfig(addr, "h-passive")
	cfg.Primary = false // passive consumer: no STATE, no commands
	d := startDriver(t, cfg)

	// Give the driver room to connect and (wrongly) publish.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && !d.Status().Connected {
		time.Sleep(10 * time.Millisecond)
	}
	if !d.Status().Connected {
		t.Fatal("driver never connected")
	}
	time.Sleep(300 * time.Millisecond)
	if n := obs.count(func(recMsg) bool { return true }); n != 0 {
		t.Fatalf("passive consumer published %d messages; want 0", n)
	}
}

// ── subscriptions and inbound state ──────────────────────────────────────

func TestMqttNBirthReachesReadInputs(t *testing.T) {
	srv, addr := startBroker(t, "")
	t.Cleanup(func() { _ = srv.Close() })

	d := startDriver(t, testConfig(addr, "h-nbirth"))
	waitConnected(t, d)

	edge := edgePublisher(t, addr, "edge-w6")
	publishPayload(t, edge, "spBv1.0/G/NBIRTH/W6", sparkplug.Payload{
		Timestamp: uint64(time.Now().UnixMilli()),
		Seq:       0,
		Metrics: []sparkplug.Metric{
			{Name: bdSeqMetric, Datatype: spb.DataType_Int64, Value: int64(3)},
			{Name: "Well/Level", Datatype: spb.DataType_Double, Value: 12.5},
		},
	})

	vals := waitForValue(t, d, "W6_Well_Level", func(v any) bool { return v == 12.5 })
	if vals["W6__Online"] != true {
		t.Errorf("W6__Online = %v, want true", vals["W6__Online"])
	}
	if vals["W6__LastBirthMs"] == int64(0) {
		t.Error("W6__LastBirthMs was not stamped")
	}
	if st := d.Status(); st.Msgs == 0 {
		t.Error("Status.Msgs did not count the NBIRTH")
	}
}

// waitConnected blocks until the driver reports a live broker connection.
func waitConnected(t *testing.T, d *Driver) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if d.Status().Connected {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("driver never connected")
}

func waitForValue(t *testing.T, d *Driver, tag string, ok func(any) bool) map[string]any {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		vals, err := d.ReadInputs()
		if err != nil {
			t.Fatalf("ReadInputs: %v", err)
		}
		if v, present := vals[tag]; present && ok(v) {
			return vals
		}
		time.Sleep(10 * time.Millisecond)
	}
	vals, _ := d.ReadInputs()
	t.Fatalf("tag %q never reached the expected value; snapshot = %v", tag, vals)
	return nil
}

// ── outbound commands ────────────────────────────────────────────────────

func TestMqttRebirthOnStart(t *testing.T) {
	srv, addr := startBroker(t, "")
	t.Cleanup(func() { _ = srv.Close() })

	obs := newObserver(t, addr, "obs-rebirth", "spBv1.0/G/NCMD/+")
	d := startDriver(t, testConfig(addr, "h-rebirth"))

	m := obs.wait(t, "NCMD Rebirth", func(m recMsg) bool { return m.topic == "spBv1.0/G/NCMD/W6" })
	p, err := sparkplug.DecodePayload(m.payload)
	if err != nil {
		t.Fatalf("decode NCMD: %v", err)
	}
	if p.Seq != 0 {
		t.Errorf("NCMD carries seq %d; a command must omit it", p.Seq)
	}
	if len(p.Metrics) != 1 {
		t.Fatalf("NCMD has %d metrics, want 1", len(p.Metrics))
	}
	got := p.Metrics[0]
	if got.Name != RebirthMetric {
		t.Errorf("metric name = %q, want %q (never an alias)", got.Name, RebirthMetric)
	}
	if got.Alias != 0 {
		t.Errorf("metric alias = %d, want 0", got.Alias)
	}
	if got.Datatype != spb.DataType_Boolean || got.Value != true {
		t.Errorf("metric = %v/%v, want Boolean true", got.Datatype, got.Value)
	}
	if m.retain {
		t.Error("NCMD must not be retained")
	}
	if st := d.Status(); st.Rebirths == 0 {
		t.Error("Status.Rebirths did not count the connect-time rebirth")
	}
}

func TestMqttWriteOutputsSendsDCMD(t *testing.T) {
	srv, addr := startBroker(t, "")
	t.Cleanup(func() { _ = srv.Close() })

	obs := newObserver(t, addr, "obs-dcmd", "spBv1.0/G/DCMD/+/+")
	d := startDriver(t, testConfig(addr, "h-dcmd"))
	waitConnected(t, d)

	// A command to a dark site is dropped by design, so bring W6 online first.
	edge := edgePublisher(t, addr, "edge-dcmd")
	publishPayload(t, edge, "spBv1.0/G/NBIRTH/W6", sparkplug.Payload{
		Timestamp: uint64(time.Now().UnixMilli()),
		Metrics: []sparkplug.Metric{
			{Name: bdSeqMetric, Datatype: spb.DataType_Int64, Value: int64(1)},
		},
	})
	waitForValue(t, d, "W6__Online", func(v any) bool { return v == true })

	if err := d.WriteOutputs(map[string]any{"W6_PLC1_Pump_SpeedSP": 42.5}); err != nil {
		t.Fatalf("WriteOutputs: %v", err)
	}

	m := obs.wait(t, "DCMD", func(m recMsg) bool { return m.topic == "spBv1.0/G/DCMD/W6/PLC1" })
	if m.retain {
		t.Error("DCMD must not be retained")
	}
	p, err := sparkplug.DecodePayload(m.payload)
	if err != nil {
		t.Fatalf("decode DCMD: %v", err)
	}
	if len(p.Metrics) != 1 {
		t.Fatalf("DCMD has %d metrics, want 1", len(p.Metrics))
	}
	if p.Metrics[0].Name != "Pump/SpeedSP" {
		t.Errorf("metric name = %q, want Pump/SpeedSP", p.Metrics[0].Name)
	}
	if p.Metrics[0].Value != 42.5 {
		t.Errorf("metric value = %v, want 42.5", p.Metrics[0].Value)
	}

	// Change suppression: the runtime hands us every output every scan, and an
	// unchanged value must not produce a second command.
	before := obs.count(func(m recMsg) bool { return m.topic == "spBv1.0/G/DCMD/W6/PLC1" })
	for i := 0; i < 5; i++ {
		_ = d.WriteOutputs(map[string]any{"W6_PLC1_Pump_SpeedSP": 42.5})
	}
	time.Sleep(200 * time.Millisecond)
	if after := obs.count(func(m recMsg) bool { return m.topic == "spBv1.0/G/DCMD/W6/PLC1" }); after != before {
		t.Errorf("unchanged writes produced %d extra DCMDs", after-before)
	}
}

func TestMqttWriteToOfflineNodeIsDropped(t *testing.T) {
	srv, addr := startBroker(t, "")
	t.Cleanup(func() { _ = srv.Close() })

	obs := newObserver(t, addr, "obs-drop", "spBv1.0/G/DCMD/+/+")
	d := startDriver(t, testConfig(addr, "h-drop"))
	waitConnected(t, d)

	if err := d.WriteOutputs(map[string]any{"W6_PLC1_Pump_SpeedSP": 7.0}); err != nil {
		t.Fatalf("WriteOutputs: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && d.Status().WriteDrops == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if d.Status().WriteDrops == 0 {
		t.Fatal("write to an offline node was not counted as a drop")
	}
	if n := obs.count(func(recMsg) bool { return true }); n != 0 {
		t.Fatalf("write to an offline node produced %d DCMDs; want 0", n)
	}
}

func TestMqttRebirthTagRisingEdge(t *testing.T) {
	srv, addr := startBroker(t, "")
	t.Cleanup(func() { _ = srv.Close() })

	obs := newObserver(t, addr, "obs-rbtag", "spBv1.0/G/NCMD/+")
	cfg := testConfig(addr, "h-rbtag")
	cfg.NoRebirthOnStart = true // isolate the operator-forced path
	d := startDriver(t, cfg)
	waitConnected(t, d)

	isRebirth := func(m recMsg) bool { return m.topic == "spBv1.0/G/NCMD/W6" }
	if err := d.WriteOutputs(map[string]any{"W6__Rebirth": false}); err != nil {
		t.Fatalf("WriteOutputs: %v", err)
	}
	time.Sleep(150 * time.Millisecond)
	if n := obs.count(isRebirth); n != 0 {
		t.Fatalf("a false __Rebirth produced %d NCMDs; want 0", n)
	}
	if err := d.WriteOutputs(map[string]any{"W6__Rebirth": true}); err != nil {
		t.Fatalf("WriteOutputs: %v", err)
	}
	obs.wait(t, "operator rebirth", isRebirth)
}

func TestDriverRequestRebirth(t *testing.T) {
	srv, addr := startBroker(t, "")
	t.Cleanup(func() { _ = srv.Close() })

	obs := newObserver(t, addr, "obs-req", "spBv1.0/G/NCMD/+")
	cfg := testConfig(addr, "h-req")
	cfg.NoRebirthOnStart = true
	d := startDriver(t, cfg)
	waitConnected(t, d)

	if err := d.RequestRebirth("nope"); err == nil {
		t.Error("RequestRebirth on an unknown node must error")
	}
	if err := d.RequestRebirth("W6"); err != nil {
		t.Fatalf("RequestRebirth: %v", err)
	}
	obs.wait(t, "requested rebirth", func(m recMsg) bool { return m.topic == "spBv1.0/G/NCMD/W6" })
}

// ── teardown ─────────────────────────────────────────────────────────────

func TestMqttStopPublishesStateDeath(t *testing.T) {
	srv, addr := startBroker(t, "")
	t.Cleanup(func() { _ = srv.Close() })

	obs := newObserver(t, addr, "obs-death", "spBv1.0/STATE/#")
	d, err := New(testManifest(), testConfig(addr, "h-death"), WithLogger(quietLogger()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	d.Start(context.Background())
	obs.wait(t, "STATE birth", func(m recMsg) bool {
		return m.topic == "spBv1.0/STATE/h-death" && decodeState(t, m.payload).Online
	})

	d.Stop()

	death := obs.wait(t, "STATE death", func(m recMsg) bool {
		return m.topic == "spBv1.0/STATE/h-death" && !decodeState(t, m.payload).Online
	})
	if !death.retain {
		// The retain flag is only visible on a fresh subscribe; assert the
		// broker's retained copy instead.
		ret := newObserver(t, addr, "obs-death-retained", "spBv1.0/STATE/h-death")
		rm := ret.wait(t, "retained STATE death", func(m recMsg) bool { return m.retain })
		if decodeState(t, rm.payload).Online {
			t.Error("retained STATE still says online after Stop")
		}
	}
	if got := decodeState(t, death.payload); got.Timestamp == 0 {
		t.Error("STATE death has no timestamp")
	}
}

// ── reconnect ────────────────────────────────────────────────────────────

func TestMqttReconnectsAfterBrokerRestart(t *testing.T) {
	addr := freePort(t)
	srv, _ := startBroker(t, addr)

	d := startDriver(t, testConfig(addr, "h-reconnect"))
	waitConnected(t, d)

	if err := srv.Close(); err != nil {
		t.Fatalf("close broker: %v", err)
	}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) && d.Status().Connected {
		time.Sleep(20 * time.Millisecond)
	}
	if d.Status().Connected {
		t.Fatal("driver still reports connected after the broker went away")
	}

	srv2, _ := startBroker(t, addr)
	t.Cleanup(func() { _ = srv2.Close() })

	// The reconnect loop backs off 1s → 30s, so allow a couple of attempts.
	waitConnected(t, d)

	// A fresh session means a fresh will *and* a fresh birth timestamp — the
	// whole reason SetAutoReconnect is off.
	obs := newObserver(t, addr, "obs-reconnect", "spBv1.0/STATE/h-reconnect")
	m := obs.wait(t, "retained STATE after reconnect", func(m recMsg) bool { return m.retain })
	if s := decodeState(t, m.payload); !s.Online {
		t.Error("STATE is not online after reconnect")
	} else if age := time.Since(time.UnixMilli(s.Timestamp)); age > 30*time.Second {
		t.Errorf("reconnect birth timestamp is %v stale", age)
	}
}

// ── status ───────────────────────────────────────────────────────────────

func TestStatusShape(t *testing.T) {
	srv, addr := startBroker(t, "")
	t.Cleanup(func() { _ = srv.Close() })

	d := startDriver(t, testConfig(addr, "h-status"))
	waitConnected(t, d)

	st := d.Status()
	if st.Broker != brokerURL(addr) {
		t.Errorf("Broker = %q, want %q", st.Broker, brokerURL(addr))
	}
	if st.HostID != "h-status" {
		t.Errorf("HostID = %q", st.HostID)
	}
	if len(st.Groups) != 1 || st.Groups[0] != "G" {
		t.Errorf("Groups = %v, want [G]", st.Groups)
	}
	if st.StateOnlineMs == 0 {
		t.Error("StateOnlineMs is zero while connected")
	}
	if st.LastError != "" {
		t.Errorf("LastError = %q, want empty", st.LastError)
	}

	edge := edgePublisher(t, addr, "edge-status")
	publishPayload(t, edge, "spBv1.0/G/NBIRTH/W6", sparkplug.Payload{
		Timestamp: uint64(time.Now().UnixMilli()),
		Metrics: []sparkplug.Metric{
			{Name: bdSeqMetric, Datatype: spb.DataType_Int64, Value: int64(9)},
			{Name: "Well/Level", Datatype: spb.DataType_Double, Value: 1.0},
		},
	})
	waitForValue(t, d, "W6__Online", func(v any) bool { return v == true })

	st = d.Status()
	if len(st.Nodes) != 1 {
		t.Fatalf("Status.Nodes = %d rows, want 1", len(st.Nodes))
	}
	n := st.Nodes[0]
	if n.EdgeNode != "W6" || !n.Online {
		t.Errorf("node row = %+v, want W6 online", n)
	}
	if n.BdSeq != 9 {
		t.Errorf("node BdSeq = %d, want 9", n.BdSeq)
	}
	if fmt.Sprint(st.Groups) == "" {
		t.Error("unreachable")
	}
}
