// claims_test.go: tests written against the claims of the Sparkplug host guide
// (docs/claims/sparkplug-host.yaml) that earlier tests only brushed past:
// LastBirthMs follows the newest birth, a member output adopts the site's live
// value on every trigger, commands leave at QoS 0 under full names, the
// offline queue's bound shows as WriteDrops and per-site depth, commands
// raised during a broker outage go out when the site births again, on-unknown logs or stays
// silent, and the leadership gate on STATE and commands.

package host

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"

	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/sparkplug"
	"github.com/joyautomation/nautilus/sparkplug/spb"
)

// ── SPH-014: __LastBirthMs follows the newest NBIRTH ─────────────────────

func TestStateLastBirthMsMovesToTheNewerBirth(t *testing.T) {
	d, _ := newTestDriver(t, testManifest(), Config{})
	first := nbirth(1, dbl("Well/Level", 1))
	first.Timestamp = 1_000
	d.handleMessage("spBv1.0/G/NBIRTH/W6", enc(t, first))
	if got := d.snapshot()["W6__LastBirthMs"]; got != int64(1_000) {
		t.Fatalf("W6__LastBirthMs = %#v after the first birth, want 1000", got)
	}

	// The node dies and births again later: the companion moves on.
	d.handleMessage("spBv1.0/G/NDEATH/W6", enc(t, ndeath(1)))
	if got := d.snapshot()["W6__LastBirthMs"]; got != int64(1_000) {
		t.Errorf("an NDEATH moved __LastBirthMs to %#v; only a birth does", got)
	}
	second := nbirth(2, dbl("Well/Level", 2))
	second.Timestamp = 9_000
	d.handleMessage("spBv1.0/G/NBIRTH/W6", enc(t, second))
	if got := d.snapshot()["W6__LastBirthMs"]; got != int64(9_000) {
		t.Errorf("W6__LastBirthMs = %#v after the second birth, want 9000", got)
	}

	// A rebirth with no death in between moves it too.
	third := nbirth(2, dbl("Well/Level", 3))
	third.Timestamp = 12_000
	d.handleMessage("spBv1.0/G/NBIRTH/W6", enc(t, third))
	if got := d.snapshot()["W6__LastBirthMs"]; got != int64(12_000) {
		t.Errorf("W6__LastBirthMs = %#v after a rebirth, want 12000", got)
	}
}

// ── SPH-032: a member output adopts the live value on every trigger ──────

func pumpTemplate(speed float64) sparkplug.Metric {
	return sparkplug.Metric{Name: "Pump1", Datatype: spb.DataType_Template,
		Value: &sparkplug.Template{TemplateRef: "Motor", Metrics: []sparkplug.Metric{
			{Name: "Run", Datatype: spb.DataType_Boolean, Value: true},
			{Name: "Speed", Datatype: spb.DataType_Double, Value: speed},
			{Name: "Label", Datatype: spb.DataType_String, Value: "M1"},
		}}}
}

func devMotorTemplate(speed float64) sparkplug.Metric {
	return sparkplug.Metric{Name: "Mtr", Datatype: spb.DataType_Template,
		Value: &sparkplug.Template{TemplateRef: "Motor", Metrics: []sparkplug.Metric{
			{Name: "Speed", Datatype: spb.DataType_Double, Value: speed},
		}}}
}

// adoptedValue is what the driver believes the site holds for a member output.
func adoptedValue(t *testing.T, d *Driver, tag string) (float64, bool) {
	t.Helper()
	d.wmu.Lock()
	defer d.wmu.Unlock()
	v, ok := d.remote[tag]
	if !ok {
		return 0, false
	}
	iv, isIR := v.(ir.Value)
	if !isIR {
		t.Fatalf("remote[%s] = %T, want an ir.Value", tag, v)
	}
	return iv.F, true
}

func TestMemberOutputAdoptsTheLiveValueOnEveryTrigger(t *testing.T) {
	m := testManifest()
	m.Tags = append(m.Tags,
		Binding{Name: "W6_PLC1_Mtr", Node: "W6", Device: "PLC1", Metric: "Mtr", Type: "Motor"},
		Binding{Name: "W6_PLC1_Mtr_Speed", Node: "W6", Device: "PLC1", Metric: "Mtr", Member: "Speed",
			Type: "Motor", Writable: true})
	d, _ := newTestDriver(t, m, Config{})

	if _, ok := adoptedValue(t, d, "W6_Pump1_Speed"); ok {
		t.Fatal("a member output holds an adopted value before its parent ever arrived")
	}

	d.handleMessage("spBv1.0/G/NBIRTH/W6", enc(t, nbirth(1, pumpTemplate(1450))))
	if v, ok := adoptedValue(t, d, "W6_Pump1_Speed"); !ok || v != 1450 {
		t.Fatalf("after the NBIRTH adopted Speed = %v (adopted %v), want 1450", v, ok)
	}

	d.handleMessage("spBv1.0/G/NDATA/W6", enc(t, data(1, pumpTemplate(900))))
	if v, _ := adoptedValue(t, d, "W6_Pump1_Speed"); v != 900 {
		t.Errorf("after an NDATA adopted Speed = %v, want 900", v)
	}

	d.handleMessage("spBv1.0/G/DBIRTH/W6/PLC1", enc(t, data(2, devMotorTemplate(77))))
	if v, ok := adoptedValue(t, d, "W6_PLC1_Mtr_Speed"); !ok || v != 77 {
		t.Errorf("after the DBIRTH adopted device Speed = %v (adopted %v), want 77", v, ok)
	}

	// A rebirth (a fresh NBIRTH, here without an NDEATH between) re-adopts.
	d.handleMessage("spBv1.0/G/NBIRTH/W6", enc(t, nbirth(2, pumpTemplate(500))))
	if v, _ := adoptedValue(t, d, "W6_Pump1_Speed"); v != 500 {
		t.Errorf("after a rebirth adopted Speed = %v, want 500", v)
	}
}

// ── SPH-021: commands leave at QoS 0, not retained, under full names ─────

// cmdMsg is one command the raw observer saw.
type cmdMsg struct {
	topic   string
	qos     byte
	retain  bool
	payload []byte
}

// cmdObserver subscribes at QoS 2, so the QoS the broker hands on is the
// publisher's own: a QoS 0 publish arrives as QoS 0.
func cmdObserver(t *testing.T, addr string, filters ...string) (get func() []cmdMsg) {
	t.Helper()
	var mu sync.Mutex
	var got []cmdMsg
	cli := mqtt.NewClient(mqtt.NewClientOptions().AddBroker(brokerURL(addr)).SetClientID("cmd-observer").
		SetCleanSession(true).SetOrderMatters(true).
		SetDefaultPublishHandler(func(_ mqtt.Client, m mqtt.Message) {
			mu.Lock()
			got = append(got, cmdMsg{m.Topic(), m.Qos(), m.Retained(), append([]byte(nil), m.Payload()...)})
			mu.Unlock()
		}))
	if tok := cli.Connect(); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		t.Fatalf("observer connect: %v", tok.Error())
	}
	t.Cleanup(func() { cli.Disconnect(50) })
	for _, f := range filters {
		if tok := cli.Subscribe(f, 2, nil); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
			t.Fatalf("observer subscribe %s: %v", f, tok.Error())
		}
	}
	return func() []cmdMsg {
		mu.Lock()
		defer mu.Unlock()
		return append([]cmdMsg(nil), got...)
	}
}

func waitCmd(t *testing.T, get func() []cmdMsg, what, topic string) cmdMsg {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		for _, m := range get() {
			if m.topic != topic {
				continue
			}
			p, err := sparkplug.DecodePayload(m.payload)
			if err != nil {
				continue
			}
			for _, mm := range p.Metrics {
				if mm.Name != RebirthMetric {
					return m
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
	return cmdMsg{}
}

func TestMqttCommandsLeaveAtQoS0NotRetainedUnderFullNames(t *testing.T) {
	srv, addr := startBroker(t, "")
	t.Cleanup(func() { _ = closeBroker(srv) })

	m := testManifest()
	m.Tags = append(m.Tags, Binding{Name: "W6_Setpoint", Node: "W6", Metric: "Site/Setpoint",
		Type: "Double", Writable: true, Init: 0.0})
	get := cmdObserver(t, addr, "spBv1.0/G/NCMD/+", "spBv1.0/G/DCMD/+/+")
	d, err := New(m, testConfig(addr, "h-qos"), WithLogger(quietLogger()))
	if err != nil {
		t.Fatal(err)
	}
	d.Start(t.Context())
	t.Cleanup(d.Stop)
	waitConnected(t, d)

	// The edge aliases its metrics, as Ignition-style edges do; the host
	// still commands by full name.
	edge := edgePublisher(t, addr, "edge-qos")
	sp := dbl("Site/Setpoint", 1)
	sp.Alias = 11
	publishPayload(t, edge, "spBv1.0/G/NBIRTH/W6", sparkplug.Payload{
		Timestamp: uint64(time.Now().UnixMilli()), Seq: 0,
		Metrics: []sparkplug.Metric{bdSeq(1), sp},
	})
	speed := dbl("Pump/SpeedSP", 1)
	speed.Alias = 12
	publishPayload(t, edge, "spBv1.0/G/DBIRTH/W6/PLC1", sparkplug.Payload{
		Timestamp: uint64(time.Now().UnixMilli()), Seq: 1,
		Metrics: []sparkplug.Metric{speed},
	})
	waitForValue(t, d, "W6_PLC1__Online", func(v any) bool { return v == true })
	baselineScan(t, d)

	if err := d.WriteOutputs(map[string]any{"W6_Setpoint": 5.5, "W6_PLC1_Pump_SpeedSP": 42.5}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ topic, metric string }{
		{"spBv1.0/G/NCMD/W6", "Site/Setpoint"},
		{"spBv1.0/G/DCMD/W6/PLC1", "Pump/SpeedSP"},
	} {
		msg := waitCmd(t, get, "the command on "+c.topic, c.topic)
		if msg.qos != 0 {
			t.Errorf("%s left at QoS %d, want 0", c.topic, msg.qos)
		}
		if msg.retain {
			t.Errorf("%s was retained", c.topic)
		}
		// A live subscriber never sees the retain flag, so ask the broker
		// whether it kept the command.
		if kept := srv.Topics.Messages(c.topic); len(kept) != 0 {
			t.Errorf("the broker holds %d retained message(s) on %s; commands are not retained", len(kept), c.topic)
		}
		p, err := sparkplug.DecodePayload(msg.payload)
		if err != nil {
			t.Fatal(err)
		}
		var found bool
		for _, mm := range p.Metrics {
			if mm.Name == RebirthMetric {
				continue
			}
			found = true
			if mm.Name != c.metric {
				t.Errorf("%s carries metric %q, want the full name %q", c.topic, mm.Name, c.metric)
			}
			if mm.Alias != 0 {
				t.Errorf("%s addresses %q by alias %d; commands carry full names, never aliases", c.topic, mm.Name, mm.Alias)
			}
		}
		if !found {
			t.Errorf("%s carried no command metric", c.topic)
		}
	}
}

// ── SPH-024 / SPH-025: the offline queue's bound and depth ───────────────

func bigManifest(n int) Manifest {
	m := Manifest{Group: "G", Nodes: []Node{{EdgeNode: "W6", Prefix: "W6"}}}
	for i := 0; i < n; i++ {
		m.Tags = append(m.Tags, Binding{
			Name: fmt.Sprintf("W6_T%03d", i), Node: "W6", Metric: fmt.Sprintf("T%03d", i),
			Type: "Double", Writable: true, Init: 0.0})
	}
	return m
}

func TestMqttQueueOverflowIsCountedAsWriteDrops(t *testing.T) {
	srv, addr := startBroker(t, "")
	t.Cleanup(func() { _ = closeBroker(srv) })

	const bound = 256 // the page's number, not the constant: a changed bound must fail here
	const tags = bound + 44
	d, err := New(bigManifest(tags), testConfig(addr, "h-drops"), WithLogger(quietLogger()))
	if err != nil {
		t.Fatal(err)
	}
	d.Start(t.Context())
	t.Cleanup(d.Stop)
	waitConnected(t, d)

	zero, moved := map[string]any{}, map[string]any{}
	for i := 0; i < tags; i++ {
		zero[fmt.Sprintf("W6_T%03d", i)] = 0.0
		moved[fmt.Sprintf("W6_T%03d", i)] = float64(i + 1)
	}
	if err := d.WriteOutputs(zero); err != nil { // baseline
		t.Fatal(err)
	}
	if err := d.WriteOutputs(moved); err != nil { // W6 has never birthed: all dark
		t.Fatal(err)
	}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if st := d.Status(); st.WriteDrops == 44 && st.QueuedWrites == bound {
			if st.WriteQueued != bound {
				t.Errorf("WriteQueued = %d, want %d", st.WriteQueued, bound)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	st := d.Status()
	t.Fatalf("WriteDrops = %d, QueuedWrites = %d; want 44 dropped past the %d-tag bound", st.WriteDrops, st.QueuedWrites, bound)
}

func TestMqttQueuedWritesAreCountedPerSite(t *testing.T) {
	srv, addr := startBroker(t, "")
	t.Cleanup(func() { _ = closeBroker(srv) })

	// Two dark sites with different depths, so a per-site row that showed
	// the fleet total (or another site's depth) is caught.
	m := Manifest{Group: "G", Nodes: []Node{{EdgeNode: "W6", Prefix: "W6"}, {EdgeNode: "W7", Prefix: "W7"}}}
	for _, b := range []struct{ name, node string }{{"W6_A", "W6"}, {"W6_B", "W6"}, {"W7_A", "W7"}} {
		m.Tags = append(m.Tags, Binding{Name: b.name, Node: b.node, Metric: b.name[3:],
			Type: "Double", Writable: true, Init: 0.0})
	}
	d, err := New(m, testConfig(addr, "h-persite"), WithLogger(quietLogger()))
	if err != nil {
		t.Fatal(err)
	}
	d.Start(t.Context())
	t.Cleanup(d.Stop)
	waitConnected(t, d)
	edge := edgePublisher(t, addr, "edge-persite")
	for _, site := range []string{"W6", "W7"} {
		publishPayload(t, edge, "spBv1.0/G/NBIRTH/"+site, sparkplug.Payload{
			Timestamp: uint64(time.Now().UnixMilli()),
			Metrics:   []sparkplug.Metric{bdSeq(1)},
		})
		waitForValue(t, d, site+"__Online", func(v any) bool { return v == true })
		publishPayload(t, edge, "spBv1.0/G/NDEATH/"+site, sparkplug.Payload{
			Timestamp: uint64(time.Now().UnixMilli()), OmitSeq: true,
			Metrics: []sparkplug.Metric{bdSeq(1)},
		})
		waitForValue(t, d, site+"__Online", func(v any) bool { return v == false })
	}
	if err := d.WriteOutputs(map[string]any{"W6_A": 0.0, "W6_B": 0.0, "W7_A": 0.0}); err != nil { // baseline
		t.Fatal(err)
	}
	if err := d.WriteOutputs(map[string]any{"W6_A": 1.0, "W6_B": 2.0, "W7_A": 3.0}); err != nil {
		t.Fatal(err)
	}
	st := waitQueued(t, d, 3)
	per := map[string]int{}
	for _, n := range st.Nodes {
		per[n.EdgeNode] = n.QueuedWrites
	}
	if len(st.Nodes) != 2 || per["W6"] != 2 || per["W7"] != 1 {
		t.Fatalf("Status.Nodes = %+v, want W6 carrying QueuedWrites 2 and W7 carrying 1", st.Nodes)
	}
}

// ── SPH-033: a command raised during an outage goes out when the site births again ─

func TestMqttCommandRaisedDuringAnOutageGoesOutOnReconnect(t *testing.T) {
	addr := freePort(t)
	srv, _ := startBroker(t, addr)

	d := startDriver(t, testConfig(addr, "h-outage"))
	waitConnected(t, d)
	bringW6Online(t, d, addr, "edge-outage")
	baselineScan(t, d)

	if err := closeBroker(srv); err != nil {
		t.Fatalf("close broker: %v", err)
	}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) && d.Status().Connected {
		time.Sleep(20 * time.Millisecond)
	}
	if d.Status().Connected {
		t.Fatal("driver still reports connected after the broker went away")
	}

	// The operator moves a setpoint with the broker away.
	if err := d.WriteOutputs(map[string]any{"W6_PLC1_Pump_SpeedSP": 33.0}); err != nil {
		t.Fatalf("WriteOutputs during the outage: %v", err)
	}

	srv2, _ := startBroker(t, addr)
	t.Cleanup(func() { _ = closeBroker(srv2) })
	get := cmdObserver(t, addr, "spBv1.0/G/DCMD/+/+")
	waitConnected(t, d)

	// Losing the broker took W6 offline (#540), so the command waits for
	// the birth that answers the reconnect's rebirth request, like any
	// command to a dark site.
	time.Sleep(300 * time.Millisecond)
	if n := len(get()); n != 0 {
		t.Fatalf("%d DCMD went out before W6 birthed again", n)
	}
	bringW6Online(t, d, addr, "edge-outage-2")

	msg := waitCmd(t, get, "the command raised during the outage", "spBv1.0/G/DCMD/W6/PLC1")
	p, err := sparkplug.DecodePayload(msg.payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Metrics) != 1 || p.Metrics[0].Name != "Pump/SpeedSP" || p.Metrics[0].Value != 33.0 {
		t.Fatalf("delivered %+v, want Pump/SpeedSP = 33", p.Metrics)
	}
}

// ── SPH-041: on-unknown log / ignore / strict ────────────────────────────

func TestStateUnknownMetricLoggingFollowsOnUnknown(t *testing.T) {
	for _, tc := range []struct {
		mode      string
		wantLog   string
		degraded  bool
		wantSaidX bool
	}{
		{DiscoveryLog, "metric not in the manifest", false, true},
		{DiscoveryStrict, "unmanifested metric (strict)", true, true},
		{DiscoveryIgnore, "", false, false},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			var buf bytes.Buffer
			d, _ := newTestDriver(t, testManifest(), Config{})
			d.log = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
			d.discovery = tc.mode
			d.handleMessage("spBv1.0/G/NBIRTH/W6", enc(t, nbirth(1, dbl("Zone9/Unbound", 3))))

			out := buf.String()
			if got := strings.Contains(out, "Zone9/Unbound"); got != tc.wantSaidX {
				t.Errorf("on-unknown %s: log mentions the unbound metric = %v, want %v\n%s", tc.mode, got, tc.wantSaidX, out)
			}
			if tc.wantLog != "" && !strings.Contains(out, tc.wantLog) {
				t.Errorf("on-unknown %s: log lacks %q:\n%s", tc.mode, tc.wantLog, out)
			}
			d.mu.Lock()
			degraded, unknown := d.degraded, len(d.unknown)
			d.mu.Unlock()
			if degraded != tc.degraded {
				t.Errorf("on-unknown %s: degraded = %v, want %v", tc.mode, degraded, tc.degraded)
			}
			if unknown != 1 {
				t.Errorf("on-unknown %s: %d unknown metrics counted, want 1", tc.mode, unknown)
			}
		})
	}
}

// ── SPH-036: the leadership gate (driver level) ──────────────────────────

func TestMqttStandbyPublishesNoStateAndNoCommandsUntilLeader(t *testing.T) {
	srv, addr := startBroker(t, "")
	t.Cleanup(func() { _ = closeBroker(srv) })

	var leader atomic.Bool // false: a standby replica
	cfg := testConfig(addr, "h-standby")
	cfg.NoRebirthOnStart = true
	cfg.Leader = leader.Load

	get := cmdObserver(t, addr, "spBv1.0/STATE/#", "spBv1.0/G/NCMD/+", "spBv1.0/G/DCMD/+/+", "test/barrier")
	d := startDriver(t, cfg)
	waitConnected(t, d)

	// A site births and the operator moves a setpoint, but this replica is
	// not the leader: no STATE, no command.
	edge := edgePublisher(t, addr, "edge-standby")
	publishPayload(t, edge, "spBv1.0/G/NBIRTH/W6", sparkplug.Payload{
		Timestamp: uint64(time.Now().UnixMilli()),
		Metrics:   []sparkplug.Metric{bdSeq(1)},
	})
	waitForValue(t, d, "W6__Online", func(v any) bool { return v == true })
	baselineScan(t, d)
	if err := d.WriteOutputs(map[string]any{"W6_PLC1_Pump_SpeedSP": 7.0}); err != nil {
		t.Fatal(err)
	}
	// A barrier message on the driver's own connection: anything the driver
	// had published before it reaches the observer first.
	d.flushWrites()
	if err := d.publisher().publish("test/barrier", 1, false, []byte("b")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(8 * time.Second)
	for len(get()) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := get(); len(got) != 1 || got[0].topic != "test/barrier" {
		t.Fatalf("a standby replica put messages on the wire: %+v", got)
	}

	// Leadership arrives: the held command goes out, and the driver
	// reconnects with a STATE online birth.
	leader.Store(true)
	waitCmd(t, get, "the held command once leader", "spBv1.0/G/DCMD/W6/PLC1")
	deadline = time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		for _, m := range get() {
			if m.topic == "spBv1.0/STATE/h-standby" && decodeState(t, m.payload).Online {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the new leader never published STATE online")
}
