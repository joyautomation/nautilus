package project

// The manifest's sparkplug publish classes, end to end: YAML → Project.Sparkplug
// → an edge node publishing to a real in-process broker. sparkplug/rbe_test.go
// proves the RBE rules by calling shouldPublish directly; what only shows here
// is that each manifest key reaches the node and changes what it puts on the
// wire (default-class, classes, metric-classes, the reserved "none" class).

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	mqttsrv "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/hooks/auth"
	"github.com/mochi-mqtt/server/v2/listeners"

	"github.com/joyautomation/nautilus/internal/testmqtt"
	"github.com/joyautomation/nautilus/runtime"
	"github.com/joyautomation/nautilus/sparkplug"
)

type wireMsg struct {
	topic   string
	payload []byte
}

type wireObserver struct {
	mu   sync.Mutex
	msgs []wireMsg
}

func (o *wireObserver) all() []wireMsg {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]wireMsg(nil), o.msgs...)
}

// metricsOf decodes every message of one type and returns the metric names of
// each, in arrival order.
func (o *wireObserver) metricsOf(t *testing.T, msgType string) [][]string {
	t.Helper()
	var out [][]string
	for _, m := range o.all() {
		if !strings.HasSuffix(m.topic, "/"+msgType+"/plant") {
			continue
		}
		p, err := sparkplug.DecodePayload(m.payload)
		if err != nil {
			t.Fatalf("decode %s: %v", m.topic, err)
		}
		var names []string
		for _, mt := range p.Metrics {
			names = append(names, mt.Name)
		}
		out = append(out, names)
	}
	return out
}

// countOf is how many NDATA messages so far carry the named metric.
func (o *wireObserver) countOf(t *testing.T, name string) int {
	n := 0
	for _, names := range o.metricsOf(t, "NDATA") {
		for _, got := range names {
			if got == name {
				n++
			}
		}
	}
	return n
}

func (o *wireObserver) waitCount(t *testing.T, name string, atLeast int) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for o.countOf(t, name) < atLeast {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d NDATA carrying %s (have %d)", atLeast, name, o.countOf(t, name))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSparkplugManifestClassesReachTheNode(t *testing.T) {
	srv := mqttsrv.New(&mqttsrv.Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err := srv.AddHook(new(auth.AllowHook), nil); err != nil {
		t.Fatal(err)
	}
	tcp := listeners.NewTCP(listeners.Config{ID: "t", Address: "127.0.0.1:0"})
	if err := srv.AddListener(tcp); err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve() }()
	t.Cleanup(func() { _ = testmqtt.Close(srv) }) // not srv.Close: it can deadlock in mochi v2.7.9
	url := "tcp://" + tcp.Address()

	obs := &wireObserver{}
	cli := mqtt.NewClient(mqtt.NewClientOptions().AddBroker(url).SetClientID("rbe-obs").
		SetCleanSession(true).SetOrderMatters(true).
		SetDefaultPublishHandler(func(_ mqtt.Client, m mqtt.Message) {
			obs.mu.Lock()
			obs.msgs = append(obs.msgs, wireMsg{m.Topic(), append([]byte(nil), m.Payload()...)})
			obs.mu.Unlock()
		}))
	if tok := cli.Connect(); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		t.Fatalf("observer connect: %v", tok.Error())
	}
	t.Cleanup(func() { cli.Disconnect(50) })
	if tok := cli.Subscribe("spBv1.0/G/#", 1, nil); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		t.Fatalf("observer subscribe: %v", tok.Error())
	}

	// Fast rides "fast" (a tight deadband), Wide the default class (a wide
	// one), Every "every" (every transition, whatever the deadband), Hold
	// "slow" (rate-limited for an hour), Beat "heartbeat" (re-sent
	// unchanged), Hide the reserved "none" class. State tags: an input or
	// output belongs to the driver and rides a device, or not at all.
	m := strings.Replace(manifest, "  - { name: Alarm,",
		"  - { name: Fast, role: state, init: 0.0 }\n"+
			"  - { name: Wide, role: state, init: 0.0 }\n"+
			"  - { name: Every, role: state, init: 0.0 }\n"+
			"  - { name: Hold, role: state, init: 0.0 }\n"+
			"  - { name: Beat, role: state, init: 0.0 }\n"+
			"  - { name: Hide, role: state, init: 0.0 }\n"+
			"  - { name: Alarm,", 1) + `
sparkplug:
  broker: "` + url + `"
  group-id: G
  edge-node: plant
  publish-interval: 20ms
  default-class: { deadband: 5 }
  classes:
    fast: { deadband: 0.1 }
    every: { every-change: true, deadband: 100, min-interval: 1h }
    slow: { min-interval: 1h }
    heartbeat: { max-interval: 100ms }
  metric-classes:
    fast: ["Fast"]
    every: ["Every"]
    slow: ["Hold"]
    heartbeat: ["Beat"]
    none: ["Hide"]
`
	p, err := Load(fsys(m), "")
	if err != nil {
		t.Fatal(err)
	}
	rt, err := runtime.New(p.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	node, err := p.Sparkplug(rt)
	if err != nil || node == nil {
		t.Fatalf("node: %v, %v", node, err)
	}
	if err := node.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(node.Stop)

	deadline := time.Now().Add(8 * time.Second)
	for len(obs.metricsOf(t, "NBIRTH")) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no NBIRTH")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// The reserved class keeps a tag out of the birth; everything else is in.
	born := map[string]bool{}
	for _, name := range obs.metricsOf(t, "NBIRTH")[0] {
		born[name] = true
	}
	if born["Hide"] {
		t.Errorf("metric-classes none: [Hide] did not keep Hide out of the NBIRTH: %v", born)
	}
	for _, want := range []string{"Fast", "Wide", "Every", "Hold", "Beat"} {
		if !born[want] {
			t.Errorf("%s is missing from the NBIRTH: %v", want, born)
		}
	}

	// Moves inside each class's rule: nothing may be published. Fast +0.05
	// is under fast's 0.1 deadband, Wide +2 under the default's 5, and
	// Hold moves a long way but is rate-limited by slow's min-interval.
	tags := rt.Tags()
	fast, wide := tags.Real("Fast"), tags.Real("Wide")
	tags.SetReal("Fast", fast+0.05)
	tags.SetReal("Wide", wide+2)
	tags.SetReal("Hold", 99)
	tags.SetReal("Hide", 99)
	// Every +0.001 is far under every's deadband and inside its
	// min-interval: only every-change can publish it. Waiting for it also
	// proves the node had sampled the moves above by then.
	tags.SetReal("Every", tags.Real("Every")+0.001)
	obs.waitCount(t, "Every", 1)
	time.Sleep(200 * time.Millisecond)
	for _, name := range []string{"Fast", "Wide", "Hold", "Hide"} {
		if n := obs.countOf(t, name); n != 0 {
			t.Errorf("%s published %d times for a move its class says is quiet", name, n)
		}
	}

	// Moves past the rule: each publishes. Fast is now +1.05 from its last
	// published value (past fast's deadband, well inside the default's, so
	// this also shows Fast is on the fast class); Wide +10 clears the
	// default class's 5.
	tags.SetReal("Fast", fast+1)
	tags.SetReal("Wide", wide+10)
	obs.waitCount(t, "Fast", 1)
	obs.waitCount(t, "Wide", 1)

	// Beat never changes, and max-interval still re-sends it.
	obs.waitCount(t, "Beat", 2)
}
