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
	"reflect"
	"sync"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	mqttsrv "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/hooks/auth"
	"github.com/mochi-mqtt/server/v2/listeners"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
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

// outputSnapshot is what the runtime hands WriteOutputs on EVERY scan: all of
// the driver's outputs at their current tag values (runtime.go's output
// rebuild). Absent an operator write an output tag sits at its init:, which
// is exactly the snapshot that used to command every online site's setpoints
// to the zero of their type. over supplies the tags the "operator" has moved.
func outputSnapshot(d *Driver, over map[string]any) map[string]any {
	out := map[string]any{}
	for _, s := range d.manifest.TagSpecs() {
		if s.Role != RoleOutput || s.Init == nil {
			continue
		}
		out[s.Name] = s.Init
	}
	for k, v := range over {
		out[k] = v
	}
	return out
}

// baselineScan feeds the driver the runtime's FIRST output snapshot. Nothing
// may come of it on the wire: an output nobody has written is not a command.
func baselineScan(t *testing.T, d *Driver) {
	t.Helper()
	if err := d.WriteOutputs(outputSnapshot(d, nil)); err != nil {
		t.Fatalf("baseline WriteOutputs: %v", err)
	}
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
	cfg.Primary = false         // passive consumer: no STATE, no output writes
	cfg.NoRebirthOnStart = true // isolate that claim from publishRebirth's
	// deliberate exception (TestMqttPassiveConsumerCanRequestRebirth below)
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

// TestMqttPassiveConsumerCanRequestRebirth is host-as-edge's load-bearing
// case (docs/design/sparkplug-host.md §8.8): a passive consumer (primary:
// false) of a group with no other primary host present would otherwise
// NEVER see a birth — Sparkplug forbids retaining them, so a driver that
// starts after the edge node's last birth has nothing to read until
// something asks for a rebirth. publishRebirth deliberately bypasses the
// Primary gate for exactly this NCMD (still gated on isLeader), so
// rebirth-on-start: true (the default) keeps its promise even when
// primary: false. STATE stays off; that part is
// TestMqttPassiveConsumerPublishesNoState's job.
func TestMqttPassiveConsumerCanRequestRebirth(t *testing.T) {
	srv, addr := startBroker(t, "")
	t.Cleanup(func() { _ = srv.Close() })

	obsState := newObserver(t, addr, "obs-passive-state", "spBv1.0/STATE/#")
	obsCmd := newObserver(t, addr, "obs-passive-cmd", "spBv1.0/G/NCMD/+")
	cfg := testConfig(addr, "h-passive-rebirth")
	cfg.Primary = false // passive, but rebirth-on-start defaults true
	startDriver(t, cfg)

	obsCmd.wait(t, "NCMD Rebirth despite primary: false", func(m recMsg) bool {
		return m.topic == "spBv1.0/G/NCMD/W6"
	})
	time.Sleep(150 * time.Millisecond)
	if n := obsState.count(func(recMsg) bool { return true }); n != 0 {
		t.Fatalf("passive consumer published %d STATE messages; want 0", n)
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

	// The runtime's first output snapshot is the baseline, not a command.
	baselineScan(t, d)

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

// bringW6Online births W6 so a command is not dropped as "write to a dark
// site", and returns once the driver has seen it.
func bringW6Online(t *testing.T, d *Driver, addr, id string) {
	t.Helper()
	edge := edgePublisher(t, addr, id)
	publishPayload(t, edge, "spBv1.0/G/NBIRTH/W6", sparkplug.Payload{
		Timestamp: uint64(time.Now().UnixMilli()),
		Metrics: []sparkplug.Metric{
			{Name: bdSeqMetric, Datatype: spb.DataType_Int64, Value: int64(1)},
		},
	})
	waitForValue(t, d, "W6__Online", func(v any) bool { return v == true })
}

// tmplOf asserts a metric is a Template and returns it.
func tmplOf(t *testing.T, m sparkplug.Metric) *sparkplug.Template {
	t.Helper()
	if m.Datatype != spb.DataType_Template {
		t.Fatalf("metric %q datatype = %v, want Template", m.Name, m.Datatype)
	}
	tm, ok := m.Value.(*sparkplug.Template)
	if !ok || tm == nil {
		t.Fatalf("metric %q value = %T, want *sparkplug.Template", m.Name, m.Value)
	}
	return tm
}

// memberOf finds one member of a template by name.
func memberOf(t *testing.T, tm *sparkplug.Template, name string) sparkplug.Metric {
	t.Helper()
	for _, m := range tm.Metrics {
		if m.Name == name {
			return m
		}
	}
	t.Fatalf("template (ref %q) has no member %q; has %d", tm.TemplateRef, name, len(tm.Metrics))
	return sparkplug.Metric{}
}

// TestMqttWriteMemberSendsPartialTemplate — writing a member binding puts an
// NCMD on the wire naming the PARENT metric and carrying ONLY that member.
// The whole point: the edge merges it, so the members it is driving survive.
func TestMqttWriteMemberSendsPartialTemplate(t *testing.T) {
	srv, addr := startBroker(t, "")
	t.Cleanup(func() { _ = srv.Close() })

	obs := newObserver(t, addr, "obs-member", "spBv1.0/G/NCMD/+")
	d := startDriver(t, testConfig(addr, "h-member"))
	waitConnected(t, d)
	bringW6Online(t, d, addr, "edge-member")
	baselineScan(t, d)

	if err := d.WriteOutputs(map[string]any{"W6_Pump1_Speed": 61.5}); err != nil {
		t.Fatalf("WriteOutputs: %v", err)
	}

	msg := obs.wait(t, "NCMD", func(m recMsg) bool {
		if m.topic != "spBv1.0/G/NCMD/W6" {
			return false
		}
		p, err := sparkplug.DecodePayload(m.payload)
		return err == nil && len(p.Metrics) == 1 && p.Metrics[0].Name == "Pump1"
	})
	if msg.retain {
		t.Error("NCMD must not be retained")
	}
	p, err := sparkplug.DecodePayload(msg.payload)
	if err != nil {
		t.Fatalf("decode NCMD: %v", err)
	}
	tm := tmplOf(t, p.Metrics[0])
	if tm.TemplateRef != "Motor" {
		t.Errorf("templateRef = %q, want Motor", tm.TemplateRef)
	}
	if len(tm.Metrics) != 1 {
		t.Fatalf("template carries %d members, want exactly 1 (a PARTIAL update)", len(tm.Metrics))
	}
	speed := memberOf(t, tm, "Speed")
	if speed.Datatype != spb.DataType_Double || speed.Value != 61.5 {
		t.Errorf("Speed = %v (%v), want 61.5 Double", speed.Value, speed.Datatype)
	}
}

// TestMqttWriteNestedMembersCoalesce — two member writes to the SAME metric in
// one coalesce window become ONE partial template (two metrics sharing a name
// would be ambiguous), and a nested path builds the intermediate template with
// its own TemplateRef.
func TestMqttWriteNestedMembersCoalesce(t *testing.T) {
	srv, addr := startBroker(t, "")
	t.Cleanup(func() { _ = srv.Close() })

	obs := newObserver(t, addr, "obs-nested", "spBv1.0/G/NCMD/+")
	d := startDriver(t, testConfig(addr, "h-nested"))
	waitConnected(t, d)
	bringW6Online(t, d, addr, "edge-nested")
	baselineScan(t, d)

	if err := d.WriteOutputs(map[string]any{
		"W6_Skid1_Drive_Run":   true,
		"W6_Skid1_Drive_Speed": 1450.0,
	}); err != nil {
		t.Fatalf("WriteOutputs: %v", err)
	}

	msg := obs.wait(t, "NCMD", func(m recMsg) bool {
		if m.topic != "spBv1.0/G/NCMD/W6" {
			return false
		}
		p, err := sparkplug.DecodePayload(m.payload)
		return err == nil && len(p.Metrics) == 1 && p.Metrics[0].Name == "Skid1"
	})
	p, err := sparkplug.DecodePayload(msg.payload)
	if err != nil {
		t.Fatalf("decode NCMD: %v", err)
	}
	if len(p.Metrics) != 1 {
		t.Fatalf("NCMD has %d metrics, want 1 — both members belong to Skid1", len(p.Metrics))
	}
	skid := tmplOf(t, p.Metrics[0])
	if skid.TemplateRef != "Skid" {
		t.Errorf("templateRef = %q, want Skid", skid.TemplateRef)
	}
	if len(skid.Metrics) != 1 {
		t.Fatalf("Skid template carries %d members, want 1 (Drive only)", len(skid.Metrics))
	}
	drive := tmplOf(t, memberOf(t, skid, "Drive"))
	if drive.TemplateRef != "Motor" {
		t.Errorf("nested templateRef = %q, want Motor", drive.TemplateRef)
	}
	if len(drive.Metrics) != 2 {
		t.Fatalf("Drive carries %d members, want 2 (Run + Speed merged)", len(drive.Metrics))
	}
	if run := memberOf(t, drive, "Run"); run.Value != true {
		t.Errorf("Drive.Run = %v, want true", run.Value)
	}
	if sp := memberOf(t, drive, "Speed"); sp.Value != 1450.0 {
		t.Errorf("Drive.Speed = %v, want 1450", sp.Value)
	}
	// Label is a sibling the host never wrote: it must not be on the wire at
	// all, or the edge's merge would overwrite whatever the site holds.
	for _, m := range drive.Metrics {
		if m.Name == "Label" {
			t.Error("a member the host never wrote leaked into the partial template")
		}
	}
}

// waitQueued blocks until the driver reports exactly n commands parked for
// dark nodes.
func waitQueued(t *testing.T, d *Driver, n int) Status {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if st := d.Status(); st.QueuedWrites == n {
			return st
		}
		time.Sleep(10 * time.Millisecond)
	}
	st := d.Status()
	t.Fatalf("QueuedWrites = %d, want %d (drops %d, queued total %d)",
		st.QueuedWrites, n, st.WriteDrops, st.WriteQueued)
	return st
}

// TestMqttWriteToOfflineNodeIsQueued — a command to a dark site is KEPT, not
// dropped. It used to be dropped, unaccounted and re-raised by the next
// scan's snapshot; under the runtime's change-push contract that re-raise
// never comes (WriteOutputs is called when an output MOVED), so the
// operator's setpoint would simply be lost.
func TestMqttWriteToOfflineNodeIsQueued(t *testing.T) {
	srv, addr := startBroker(t, "")
	t.Cleanup(func() { _ = srv.Close() })

	obs := newObserver(t, addr, "obs-queued", "spBv1.0/G/DCMD/+/+")
	d := startDriver(t, testConfig(addr, "h-queued"))
	waitConnected(t, d)
	baselineScan(t, d)

	if err := d.WriteOutputs(map[string]any{"W6_PLC1_Pump_SpeedSP": 7.0}); err != nil {
		t.Fatalf("WriteOutputs: %v", err)
	}
	st := waitQueued(t, d, 1)
	if st.WriteDrops != 0 {
		t.Errorf("WriteDrops = %d, want 0 — a dark site is not a drop", st.WriteDrops)
	}
	if st.WriteQueued != 1 {
		t.Errorf("WriteQueued = %d, want 1", st.WriteQueued)
	}
	if n := obs.count(func(recMsg) bool { return true }); n != 0 {
		t.Fatalf("write to an offline node produced %d DCMDs; want 0", n)
	}
	// The site is in the manifest but has never been heard from, so it has no
	// Status row yet; the fleet-level gauge is what an operator sees.
	if st.QueuedWrites != 1 {
		t.Errorf("QueuedWrites = %d, want 1", st.QueuedWrites)
	}
}

// TestMqttQueuedWriteIsDeliveredOnBirth — the site comes back and the command
// goes out, once, carrying the value the operator asked for.
func TestMqttQueuedWriteIsDeliveredOnBirth(t *testing.T) {
	srv, addr := startBroker(t, "")
	t.Cleanup(func() { _ = srv.Close() })

	obs := newObserver(t, addr, "obs-flush", "spBv1.0/G/DCMD/+/+")
	d := startDriver(t, testConfig(addr, "h-flush"))
	waitConnected(t, d)
	baselineScan(t, d)

	if err := d.WriteOutputs(map[string]any{"W6_PLC1_Pump_SpeedSP": 7.0}); err != nil {
		t.Fatalf("WriteOutputs: %v", err)
	}
	waitQueued(t, d, 1)

	bringW6Online(t, d, addr, "edge-flush")

	dcmd := isCommandFor("spBv1.0/G/DCMD/W6/PLC1")
	msg := obs.wait(t, "the queued DCMD, delivered on the birth", dcmd)
	p, err := sparkplug.DecodePayload(msg.payload)
	if err != nil {
		t.Fatalf("decode DCMD: %v", err)
	}
	if len(p.Metrics) != 1 || p.Metrics[0].Name != "Pump/SpeedSP" || p.Metrics[0].Value != 7.0 {
		t.Fatalf("delivered payload = %+v, want one Pump/SpeedSP = 7", p.Metrics)
	}

	// Once, and the queue is empty afterwards.
	st := waitQueued(t, d, 0)
	if st.WriteDrops != 0 {
		t.Errorf("WriteDrops = %d, want 0", st.WriteDrops)
	}
	scanFor(t, d, 3, map[string]any{"W6_PLC1_Pump_SpeedSP": 7.0})
	if n := obs.count(dcmd); n != 1 {
		t.Fatalf("the queued command was delivered %d times, want exactly 1", n)
	}
}

// TestMqttQueuedWriteTheSiteAlreadyHoldsIsNotSent — "unless the site already
// reports that value". The birth is both the trigger to flush the queue and
// the answer to whether flushing it would say anything: a member the site
// comes back already holding is settled, and only the member that still
// disagrees goes on the wire.
func TestMqttQueuedWriteTheSiteAlreadyHoldsIsNotSent(t *testing.T) {
	srv, addr := startBroker(t, "")
	t.Cleanup(func() { _ = srv.Close() })

	obs := newObserver(t, addr, "obs-settled", "spBv1.0/G/NCMD/+")
	d := startDriver(t, testConfig(addr, "h-settled"))
	waitConnected(t, d)
	baselineScan(t, d)

	// Two member writes to a dark site: one the site will turn out to hold
	// already (Pump1.Speed = 1450), one it will not (Skid1.Drive.Speed = 99).
	if err := d.WriteOutputs(map[string]any{
		"W6_Pump1_Speed":       1450.0,
		"W6_Skid1_Drive_Speed": 99.0,
	}); err != nil {
		t.Fatalf("WriteOutputs: %v", err)
	}
	waitQueued(t, d, 2)

	edge := edgePublisher(t, addr, "edge-settled")
	publishPayload(t, edge, "spBv1.0/G/NBIRTH/W6", sparkplug.Payload{
		Timestamp: uint64(time.Now().UnixMilli()),
		Seq:       0,
		Metrics: []sparkplug.Metric{
			{Name: bdSeqMetric, Datatype: spb.DataType_Int64, Value: int64(1)},
			{Name: "Pump1", Datatype: spb.DataType_Template,
				Value: &sparkplug.Template{TemplateRef: "Motor", Metrics: []sparkplug.Metric{
					{Name: "Run", Datatype: spb.DataType_Boolean, Value: true},
					{Name: "Speed", Datatype: spb.DataType_Double, Value: 1450.0},
				}}},
			{Name: "Skid1", Datatype: spb.DataType_Template,
				Value: &sparkplug.Template{TemplateRef: "Skid", Metrics: []sparkplug.Metric{
					{Name: "Hours", Datatype: spb.DataType_Int64, Value: int64(12)},
					{Name: "Drive", Datatype: spb.DataType_Template,
						Value: &sparkplug.Template{TemplateRef: "Motor", Metrics: []sparkplug.Metric{
							{Name: "Speed", Datatype: spb.DataType_Double, Value: 10.0},
						}}},
				}}},
		},
	})
	waitForValue(t, d, "W6__Online", func(v any) bool { return v == true })
	waitQueued(t, d, 0)

	cmd := isCommandFor("spBv1.0/G/NCMD/W6")
	msg := obs.wait(t, "the member the site does NOT hold", cmd)
	p, err := sparkplug.DecodePayload(msg.payload)
	if err != nil {
		t.Fatalf("decode NCMD: %v", err)
	}
	if len(p.Metrics) != 1 || p.Metrics[0].Name != "Skid1" {
		t.Fatalf("delivered %d metrics (%v), want only Skid1 — Pump1 was already at 1450",
			len(p.Metrics), p.Metrics)
	}
	drive := tmplOf(t, memberOf(t, tmplOf(t, p.Metrics[0]), "Drive"))
	if sp := memberOf(t, drive, "Speed"); sp.Value != 99.0 {
		t.Errorf("Skid1.Drive.Speed = %v, want 99", sp.Value)
	}
	time.Sleep(200 * time.Millisecond)
	if n := obs.count(cmd); n != 1 {
		t.Fatalf("the flush published %d commands, want exactly 1 — the member the "+
			"site already reported must not be re-commanded", n)
	}
}

// TestQueueOfflineIsBoundedPerNode — the queue is one value per TAG, bounded
// per node. Past the bound a write is a real drop (counted), and a second
// write to a tag already parked REPLACES it rather than consuming budget.
// A pure unit test: no broker, no goroutines.
func TestQueueOfflineIsBoundedPerNode(t *testing.T) {
	d := &Driver{log: quietLogger()}

	over := 40
	hold := map[string]map[string]any{"W6": {}}
	for i := 0; i < maxQueuedPerNode+over; i++ {
		hold["W6"][fmt.Sprintf("W6_T%04d", i)] = float64(i)
	}
	if got := d.queueOffline(hold); got != uint64(over) {
		t.Errorf("dropped %d, want %d (everything past the bound)", got, over)
	}
	if n := len(d.queued["W6"]); n != maxQueuedPerNode {
		t.Errorf("queue holds %d, want %d", n, maxQueuedPerNode)
	}
	if d.stats.WriteQueued != maxQueuedPerNode {
		t.Errorf("WriteQueued = %d, want %d", d.stats.WriteQueued, maxQueuedPerNode)
	}

	// Re-writing a tag that is already parked keeps the LATEST value and is
	// never a drop — the queue is full, but this one costs nothing.
	var parked string
	for name := range d.queued["W6"] {
		parked = name
		break
	}
	if got := d.queueOffline(map[string]map[string]any{"W6": {parked: 99.0}}); got != 0 {
		t.Errorf("replacing a parked tag dropped %d, want 0", got)
	}
	if got := d.queued["W6"][parked]; got != 99.0 {
		t.Errorf("%s = %v, want the later value 99", parked, got)
	}

	// The bound is per node: another site has its own budget.
	if got := d.queueOffline(map[string]map[string]any{"W9": {"W9_A": 1.0}}); got != 0 {
		t.Errorf("a different node dropped %d, want 0 — the bound is per node", got)
	}
	if n := len(d.queued["W9"]); n != 1 {
		t.Errorf("W9 queue holds %d, want 1", n)
	}

	depths := d.queuedDepths()
	if depths["W6"] != maxQueuedPerNode || depths["W9"] != 1 {
		t.Errorf("queuedDepths = %v, want W6:%d W9:1", depths, maxQueuedPerNode)
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
	baselineScan(t, d)

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

// ── outputs are commands (the baseline rule) ─────────────────────────────

// isCommandFor reports whether a recorded message is a real command — one
// carrying a metric other than Node Control/Rebirth. RebirthOnStart and the
// state machine's own gap recovery put Rebirth NCMDs on the same topics, and
// those are not what these tests are counting.
func isCommandFor(topics ...string) func(recMsg) bool {
	return func(m recMsg) bool {
		match := false
		for _, tp := range topics {
			if m.topic == tp {
				match = true
			}
		}
		if !match {
			return false
		}
		p, err := sparkplug.DecodePayload(m.payload)
		if err != nil {
			return false
		}
		for _, mm := range p.Metrics {
			if mm.Name != RebirthMetric {
				return true
			}
		}
		return false
	}
}

// scanFor hands the driver n output snapshots the way the runtime does — all
// outputs, every scan — and gives the coalescing writer time to act on them.
func scanFor(t *testing.T, d *Driver, n int, over map[string]any) {
	t.Helper()
	for i := 0; i < n; i++ {
		if err := d.WriteOutputs(outputSnapshot(d, over)); err != nil {
			t.Fatalf("WriteOutputs: %v", err)
		}
		time.Sleep(30 * time.Millisecond)
	}
	time.Sleep(150 * time.Millisecond)
}

// TestMqttStartCommandsNothing is the unit-scale half of the regression the
// PomonaSCADA demo found (host/README.md, "the host zeroes an edge's
// setpoints when it starts"). A host that connects to a group whose sites are
// already online used to publish EVERY writable output once, still holding
// the zero of its type because nobody had written it — and for a member
// binding that is a partial template of zeros the edge merges member by
// member, wiping every commissioned setpoint in the writable globs.
//
// Scanning an output set nobody has touched must put NOTHING on the wire.
func TestMqttStartCommandsNothing(t *testing.T) {
	srv, addr := startBroker(t, "")
	t.Cleanup(func() { _ = srv.Close() })

	obs := newObserver(t, addr, "obs-nocmd", "spBv1.0/G/NCMD/+", "spBv1.0/G/DCMD/+/+")
	d := startDriver(t, testConfig(addr, "h-nocmd"))
	waitConnected(t, d)
	// The site is ONLINE — a command would not be dropped as "dark site", so
	// nothing but the rule itself is holding the wire quiet.
	bringW6Online(t, d, addr, "edge-nocmd")

	scanFor(t, d, 5, nil)

	cmd := isCommandFor("spBv1.0/G/NCMD/W6", "spBv1.0/G/DCMD/W6/PLC1")
	if n := obs.count(cmd); n != 0 {
		t.Fatalf("a host that has been written to by nobody published %d commands; "+
			"outputs are commands, and an unchanged output since start is not one", n)
	}
}

// TestMqttOperatorWritePublishesOnce — the other side of the rule: a value an
// operator actually moves IS a command, exactly once, however many scans hand
// it back afterwards.
func TestMqttOperatorWritePublishesOnce(t *testing.T) {
	srv, addr := startBroker(t, "")
	t.Cleanup(func() { _ = srv.Close() })

	obs := newObserver(t, addr, "obs-once", "spBv1.0/G/DCMD/+/+")
	d := startDriver(t, testConfig(addr, "h-once"))
	waitConnected(t, d)
	bringW6Online(t, d, addr, "edge-once")
	baselineScan(t, d)

	dcmd := isCommandFor("spBv1.0/G/DCMD/W6/PLC1")
	over := map[string]any{"W6_PLC1_Pump_SpeedSP": 42.5}
	scanFor(t, d, 1, over)
	obs.wait(t, "DCMD for the operator's setpoint", dcmd)

	// The runtime keeps handing the same snapshot back every scan.
	scanFor(t, d, 5, over)
	if n := obs.count(dcmd); n != 1 {
		t.Fatalf("one operator write produced %d DCMDs, want exactly 1", n)
	}
}

// TestMqttReconnectDoesNotReplayOutputs — a session that comes back must not
// re-command the world. The change detector tracks the runtime's output tags,
// not the broker session, so it survives the reconnect and there is nothing
// to replay.
func TestMqttReconnectDoesNotReplayOutputs(t *testing.T) {
	addr := freePort(t)
	srv, _ := startBroker(t, addr)

	d := startDriver(t, testConfig(addr, "h-replay"))
	waitConnected(t, d)
	bringW6Online(t, d, addr, "edge-replay")
	baselineScan(t, d)

	// One genuine command before the drop.
	over := map[string]any{"W6_PLC1_Pump_SpeedSP": 42.5, "W6_Pump1_Speed": 61.5}
	scanFor(t, d, 2, over)

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
	waitConnected(t, d)

	// Watch the NEW session only. The site is online again, so a replay would
	// land — and with the same values the operator set, which is precisely
	// what makes it invisible until a member write clobbers a sibling.
	obs := newObserver(t, addr, "obs-replay", "spBv1.0/G/NCMD/+", "spBv1.0/G/DCMD/+/+")
	bringW6Online(t, d, addr, "edge-replay2")
	scanFor(t, d, 5, over)

	cmd := isCommandFor("spBv1.0/G/NCMD/W6", "spBv1.0/G/DCMD/W6/PLC1")
	if n := obs.count(cmd); n != 0 {
		t.Fatalf("the reconnect replayed %d commands; a reconnect is not a command", n)
	}
}

// TestMqttMemberBaselineAdoptsLiveValue — the birth settles what the SITE
// holds, so a member output's baseline stops being "the zero of its type" the
// moment its parent template arrives: writing the value the panel already has
// is a no-op, and writing anything else goes out once.
func TestMqttMemberBaselineAdoptsLiveValue(t *testing.T) {
	srv, addr := startBroker(t, "")
	t.Cleanup(func() { _ = srv.Close() })

	obs := newObserver(t, addr, "obs-adopt", "spBv1.0/G/NCMD/+")
	d := startDriver(t, testConfig(addr, "h-adopt"))
	waitConnected(t, d)

	// The site births Pump1 with a commissioned Speed. Nothing in the host's
	// own tag store knows that number; the birth is where it learns it.
	edge := edgePublisher(t, addr, "edge-adopt")
	publishPayload(t, edge, "spBv1.0/G/NBIRTH/W6", sparkplug.Payload{
		Timestamp: uint64(time.Now().UnixMilli()),
		Seq:       0,
		Metrics: []sparkplug.Metric{
			{Name: bdSeqMetric, Datatype: spb.DataType_Int64, Value: int64(1)},
			{Name: "Pump1", Datatype: spb.DataType_Template,
				Value: &sparkplug.Template{TemplateRef: "Motor", Metrics: []sparkplug.Metric{
					{Name: "Run", Datatype: spb.DataType_Boolean, Value: true},
					{Name: "Speed", Datatype: spb.DataType_Double, Value: 1450.0},
					{Name: "Label", Datatype: spb.DataType_String, Value: "M1"},
				}}},
		},
	})
	waitForValue(t, d, "W6__Online", func(v any) bool { return v == true })
	waitForValue(t, d, "W6_Pump1", func(v any) bool {
		s, ok := v.(ir.Value)
		return ok && s.Kind == ir.TypeStruct && fieldByName(s, "Speed").F == 1450
	})
	baselineScan(t, d)

	cmd := isCommandFor("spBv1.0/G/NCMD/W6")

	// An operator dialling the member to the value the panel already holds is
	// asking for nothing.
	scanFor(t, d, 3, map[string]any{"W6_Pump1_Speed": 1450.0})
	if n := obs.count(cmd); n != 0 {
		t.Fatalf("writing a member the value the site already reported produced %d NCMDs, want 0", n)
	}

	// Any other value is a real command, published once.
	scanFor(t, d, 3, map[string]any{"W6_Pump1_Speed": 61.5})
	if n := obs.count(cmd); n != 1 {
		t.Fatalf("moving the member to a new value produced %d NCMDs, want exactly 1", n)
	}
	msg := obs.wait(t, "member NCMD", cmd)
	p, err := sparkplug.DecodePayload(msg.payload)
	if err != nil {
		t.Fatalf("decode NCMD: %v", err)
	}
	tm := tmplOf(t, p.Metrics[0])
	if sp := memberOf(t, tm, "Speed"); sp.Value != 61.5 {
		t.Errorf("Speed = %v, want 61.5", sp.Value)
	}
}

// fieldByName reads one field of a struct ir.Value by name.
func fieldByName(v ir.Value, name string) ir.Value {
	if v.Struct == nil {
		return ir.Value{}
	}
	i, ok := v.Struct.FieldIndex[name]
	if !ok || i >= len(v.Fld) {
		return ir.Value{}
	}
	return v.Fld[i]
}

// ── the change-push cadence (io.Driver's newer contract) ─────────────────

// pushScan hands the driver ONE output snapshot the way the change-push
// runtime does: ALL outputs, but only on the scans where at least one of them
// actually moved. The scans in between call WriteOutputs not at all — which
// is the whole point of the contract, and the thing the driver may not lean
// on. scanFor above is the older, every-scan cadence; both must be correct.
func pushScan(t *testing.T, d *Driver, over map[string]any) {
	t.Helper()
	if err := d.WriteOutputs(outputSnapshot(d, over)); err != nil {
		t.Fatalf("WriteOutputs: %v", err)
	}
	time.Sleep(150 * time.Millisecond)
}

// TestMqttChangePushCadence walks the whole output contract at the cadence
// the runtime now uses (runtime.Scan: WriteOutputs on the first scan, when an
// output CHANGED, after a failed write, and after a leadership takeover):
//
//	baseline               → nothing on the wire
//	one operator write     → exactly one command
//	site goes dark, write  → queued, not dropped, nothing on the wire
//	site births            → delivered once
//	nothing moves          → the runtime calls nothing, and neither do we
func TestMqttChangePushCadence(t *testing.T) {
	srv, addr := startBroker(t, "")
	t.Cleanup(func() { _ = srv.Close() })

	obs := newObserver(t, addr, "obs-push", "spBv1.0/G/NCMD/+", "spBv1.0/G/DCMD/+/+")
	d := startDriver(t, testConfig(addr, "h-push"))
	waitConnected(t, d)
	bringW6Online(t, d, addr, "edge-push")

	cmd := isCommandFor("spBv1.0/G/NCMD/W6", "spBv1.0/G/DCMD/W6/PLC1")
	dcmd := isCommandFor("spBv1.0/G/DCMD/W6/PLC1")

	// 1. The first call is the baseline: the world at t=0, not a command.
	pushScan(t, d, nil)
	if n := obs.count(cmd); n != 0 {
		t.Fatalf("the baseline call published %d commands, want 0", n)
	}

	// 2. An operator moves a setpoint: one call, one command.
	over := map[string]any{"W6_PLC1_Pump_SpeedSP": 42.5}
	pushScan(t, d, over)
	obs.wait(t, "the operator's DCMD", dcmd)
	if n := obs.count(dcmd); n != 1 {
		t.Fatalf("one operator write produced %d DCMDs, want 1", n)
	}

	// 3. Nothing moves, so the runtime makes no call at all — and a call it
	//    DOES make (AlwaysWriteOutputs, a failed write, a takeover) is still
	//    not a second command.
	pushScan(t, d, over)
	pushScan(t, d, over)
	if n := obs.count(dcmd); n != 1 {
		t.Fatalf("re-handing an unchanged snapshot produced %d DCMDs, want 1", n)
	}

	// 4. The site goes dark and the operator writes again. Under the old
	//    contract this was dropped and re-raised by the next scan's snapshot;
	//    there is no next scan now, so it must be kept.
	edge := edgePublisher(t, addr, "edge-push-death")
	publishPayload(t, edge, "spBv1.0/G/NDEATH/W6", ndeath(1))
	waitForValue(t, d, "W6__Online", func(v any) bool { return v == false })

	over["W6_PLC1_Pump_SpeedSP"] = 55.0
	pushScan(t, d, over)
	st := waitQueued(t, d, 1)
	if st.WriteDrops != 0 {
		t.Errorf("WriteDrops = %d, want 0 — a dark site is not a drop", st.WriteDrops)
	}
	if st.WriteQueued != 1 {
		t.Errorf("WriteQueued = %d, want 1", st.WriteQueued)
	}
	if n := obs.count(dcmd); n != 1 {
		t.Fatalf("a write to a dark site published %d DCMDs, want none (1 from step 2)", n)
	}

	// 5. The site comes back: the command is delivered, once, with NO further
	//    WriteOutputs call — nothing moved, so the runtime makes none.
	bringW6Online(t, d, addr, "edge-push-back")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && obs.count(dcmd) < 2 {
		time.Sleep(10 * time.Millisecond)
	}
	if n := obs.count(dcmd); n != 2 {
		t.Fatalf("after the site birthed, DCMD count = %d, want 2 (the queued command)", n)
	}
	msg := obs.wait(t, "the delivered setpoint", func(m recMsg) bool {
		if !dcmd(m) {
			return false
		}
		p, err := sparkplug.DecodePayload(m.payload)
		return err == nil && len(p.Metrics) == 1 && p.Metrics[0].Value == 55.0
	})
	_ = msg

	st = waitQueued(t, d, 0)
	if st.WriteDrops != 0 {
		t.Errorf("WriteDrops = %d, want 0", st.WriteDrops)
	}
	if st.WriteQueued != 1 {
		t.Errorf("WriteQueued = %d, want 1 (cumulative)", st.WriteQueued)
	}

	// 6. And the runtime keeps making no calls; a stray one changes nothing.
	pushScan(t, d, over)
	time.Sleep(200 * time.Millisecond)
	if n := obs.count(dcmd); n != 2 {
		t.Fatalf("the delivered command was re-sent: DCMD count = %d, want 2", n)
	}
}

// ── io.BatchReader ───────────────────────────────────────────────────────

// batchReader mirrors io.BatchReader as the runtime branch declares it
// (st-struct-pins: a Driver that can also refill the caller's map). The
// interface itself does not exist on this branch, so the SHAPE is pinned
// against a local copy here rather than with an assertion the package cannot
// yet compile — when the branches merge, the runtime's own type assertion
// finds the method precisely because this does.
type batchReader interface {
	nio.Driver
	ReadInputsInto(dst nio.Values) error
}

var _ batchReader = (*Driver)(nil)

// TestReadInputsIntoMatchesReadInputs — ReadInputsInto is the same delivery
// as ReadInputs, into the caller's map: same error before Start, same values
// after, and dst left holding EXACTLY what the driver holds (a name the
// caller had that the driver does not is removed, never served as stale).
func TestReadInputsIntoMatchesReadInputs(t *testing.T) {
	srv, addr := startBroker(t, "")
	t.Cleanup(func() { _ = srv.Close() })

	d, err := New(testManifest(), testConfig(addr, "h-into"), WithLogger(quietLogger()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Before Start both refuse, identically — a project wired up but never
	// started fails loudly rather than scanning zeros.
	_, err1 := d.ReadInputs()
	err2 := d.ReadInputsInto(nio.Values{})
	if err1 == nil || err2 == nil {
		t.Fatalf("before Start: ReadInputs err = %v, ReadInputsInto err = %v; want both non-nil", err1, err2)
	}
	if err1.Error() != err2.Error() {
		t.Errorf("errors differ: %q vs %q", err1, err2)
	}

	d.Start(context.Background())
	t.Cleanup(d.Stop)
	waitConnected(t, d)

	edge := edgePublisher(t, addr, "edge-into")
	publishPayload(t, edge, "spBv1.0/G/NBIRTH/W6", sparkplug.Payload{
		Timestamp: uint64(time.Now().UnixMilli()),
		Seq:       0,
		Metrics: []sparkplug.Metric{
			{Name: bdSeqMetric, Datatype: spb.DataType_Int64, Value: int64(1)},
			dbl("Well/Level", 42.5),
			str("Site", "W6"),
			{Name: "Pump1", Datatype: spb.DataType_Template,
				Value: &sparkplug.Template{TemplateRef: "Motor", Metrics: []sparkplug.Metric{
					{Name: "Run", Datatype: spb.DataType_Boolean, Value: true},
					{Name: "Speed", Datatype: spb.DataType_Double, Value: 1450.0},
				}}},
		},
	})
	waitForValue(t, d, "W6_Well_Level", func(v any) bool { return v == 42.5 })

	want, err := d.ReadInputs()
	if err != nil {
		t.Fatalf("ReadInputs: %v", err)
	}
	// A dirty buffer, the way the runtime's reused map would be if the driver
	// ever stopped serving a name.
	got := nio.Values{"W6_Well_Level": 0.0, "gone_stale": true}
	if err := d.ReadInputsInto(got); err != nil {
		t.Fatalf("ReadInputsInto: %v", err)
	}
	if _, stale := got["gone_stale"]; stale {
		t.Error("ReadInputsInto left a name the driver does not serve in dst")
	}
	if !reflect.DeepEqual(map[string]any(got), map[string]any(want)) {
		t.Errorf("ReadInputsInto delivered a different set than ReadInputs:\n into: %v\n  new: %v",
			sortedKeys(got), sortedKeys(want))
		for k, w := range want {
			if g, ok := got[k]; !ok || !reflect.DeepEqual(g, w) {
				t.Errorf("  %s: into = %#v, new = %#v", k, g, w)
			}
		}
	}

	// It allocates nothing: the same map comes back filled, scan after scan.
	for i := 0; i < 3; i++ {
		if err := d.ReadInputsInto(got); err != nil {
			t.Fatalf("ReadInputsInto (repeat): %v", err)
		}
	}
	if !reflect.DeepEqual(map[string]any(got), map[string]any(want)) {
		t.Error("repeated ReadInputsInto calls diverged from the snapshot")
	}
}
