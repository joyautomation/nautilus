package sparkplug

import (
	"bytes"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/sparkplug/spb"
)

// wire records every message on the Plant/Edge1 node's topics, in order.
type wire struct {
	mu   sync.Mutex
	msgs []wireMsg
	cli  mqtt.Client
}

type wireMsg struct {
	kind string // NBIRTH, NDATA, NDEATH, DBIRTH, ...
	p    Payload
}

func watchWire(t *testing.T, broker string) *wire {
	t.Helper()
	w := &wire{}
	w.cli = mqtt.NewClient(mqtt.NewClientOptions().AddBroker(broker).
		SetClientID("wire-watcher").SetCleanSession(true).SetOrderMatters(true))
	if tok := w.cli.Connect(); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		t.Fatalf("watcher connect: %v", tok.Error())
	}
	t.Cleanup(func() { w.cli.Disconnect(50) })
	h := func(_ mqtt.Client, m mqtt.Message) {
		parts := strings.Split(m.Topic(), "/")
		p, _ := DecodePayload(m.Payload())
		w.mu.Lock()
		w.msgs = append(w.msgs, wireMsg{kind: parts[2], p: p})
		w.mu.Unlock()
	}
	for _, f := range []string{"spBv1.0/Plant/+/Edge1", "spBv1.0/Plant/+/Edge1/+"} {
		if tok := w.cli.Subscribe(f, 1, h); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
			t.Fatalf("watcher subscribe: %v", tok.Error())
		}
	}
	return w
}

func (w *wire) all() []wireMsg {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]wireMsg(nil), w.msgs...)
}

func (w *wire) count(kind string) int {
	n := 0
	for _, m := range w.all() {
		if m.kind == kind {
			n++
		}
	}
	return n
}

func (w *wire) waitCount(t *testing.T, kind string, n int) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		if w.count(kind) >= n {
			return
		}
	}
	t.Fatalf("%d %s on the wire, want %d: %v", w.count(kind), kind, n, w.kinds())
}

func (w *wire) kinds() []string {
	var out []string
	for _, m := range w.all() {
		out = append(out, m.kind)
	}
	return out
}

// metric is the named metric of p.
func metric(p Payload, name string) (Metric, bool) {
	for _, m := range p.Metrics {
		if m.Name == name {
			return m, true
		}
	}
	return Metric{}, false
}

func bdSeqOf(t *testing.T, p Payload) int64 {
	t.Helper()
	m, ok := metric(p, "bdSeq")
	if !ok {
		t.Fatalf("no bdSeq in %+v", p)
	}
	return m.Value.(int64)
}

// newRedundantNode is an edge node over SpeedSP (REAL), publishing every
// 20 ms to broker, with opts, not started.
func newRedundantNode(t *testing.T, broker string, opts ...Option) *Node {
	t.Helper()
	n, err := New(newRuntimeForGate(t), Config{BrokerURL: broker, GroupID: "Plant", EdgeNode: "Edge1", PublishInterval: 20 * time.Millisecond}, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// While its publish gate is closed a node births nothing, publishes no
// data and buffers none (store-and-forward), even with its session up;
// open, it births; closed again, its data stops at once and only a stop's
// NDEATH goes out (#798). spark_publish_after_gate turns it red.
func TestAGatedNodeNeitherBirthsNorPublishesNorBuffers(t *testing.T) {
	broker := "tcp://" + testBroker(t)
	w := watchWire(t, broker)
	var open atomic.Bool
	n := newRedundantNode(t, broker, WithStoreForward(100))
	n.SetPublishGate(open.Load)
	if err := n.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer n.Stop()
	tags := n.rt.Tags()
	time.Sleep(200 * time.Millisecond)
	if k := w.kinds(); len(k) != 0 {
		t.Fatalf("a gated node sent %v, want nothing", k)
	}

	open.Store(true)
	n.Rebirth() // as the supervisor's first open look does through onConnect
	w.waitCount(t, "NBIRTH", 1)
	_ = tags.SetPath("SpeedSP", 20.0)
	w.waitCount(t, "NDATA", 1)

	open.Store(false)
	time.Sleep(60 * time.Millisecond) // a tick in flight lands
	before := len(w.all())
	for i := range 5 {
		_ = tags.SetPath("SpeedSP", 30.0+float64(i))
		time.Sleep(40 * time.Millisecond)
	}
	if got := w.all()[before:]; len(got) != 0 {
		t.Fatalf("after the gate closed the node sent %d messages (%v), want none", len(got), w.kinds()[before:])
	}
	if l := n.sf.len(); l != 0 {
		t.Fatalf("after the gate closed %d data messages were buffered, want none", l)
	}
	n.Stop()
	w.waitCount(t, "NDEATH", 1)
}

// A command to a gated node writes nothing: its controller does not lead.
// spark_publish_after_gate turns it red.
func TestAGatedNodeRefusesCommandWrites(t *testing.T) {
	var buf bytes.Buffer
	n := newCommandNode(t, &buf, WithWritable("SpeedSP"))
	n.SetPublishGate(func() bool { return false })
	n.applyCommand("", Payload{Metrics: []Metric{{Name: "SpeedSP", Datatype: spb.DataType_Double, Value: 99.0}}})
	if v, err := n.rt.Tags().ReadGlobal("SpeedSP"); err != nil || v.Kind != ir.TypeReal || v.F != 12.5 {
		t.Fatalf("SpeedSP after a command to a gated node = %+v (%v), want 12.5", v, err)
	}
	if n.Status().CommandsRefused != 1 || !strings.Contains(buf.String(), notLeaderRefusal) {
		t.Fatalf("refused %d, log %q; want one refusal saying %q", n.Status().CommandsRefused, buf.String(), notLeaderRefusal)
	}
	if !n.Status().Standby {
		t.Error("a gated node's status does not say standby")
	}
}

// A new session's bdSeq is never the peer's last: the first Start, and a
// reconnect's new session both skip it (#798). spark_bdseq_clash turns it
// red.
func TestANewSessionsBdSeqSkipsThePeersLast(t *testing.T) {
	broker := "tcp://" + testBroker(t)
	w := watchWire(t, broker)
	var peer atomic.Int64
	peer.Store(0) // a fresh node's first bdSeq is 0
	n := newRedundantNode(t, broker, WithBdSeqAvoid(func() (uint64, bool) { return uint64(peer.Load()), true }))
	if err := n.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer n.Stop()
	w.waitCount(t, "NBIRTH", 1)
	if bd := bdSeqOf(t, w.all()[0].p); bd != 1 {
		t.Fatalf("first session bdSeq %d, want 1 (0 is the peer's)", bd)
	}
	// The next session would be 2: the peer's is 2 now.
	peer.Store(2)
	n.mu.Lock()
	old := n.cli
	n.mu.Unlock()
	n.newSession(old, true)
	w.waitCount(t, "NBIRTH", 2)
	var last Payload
	for _, m := range w.all() {
		if m.kind == "NBIRTH" {
			last = m.p
		}
	}
	if bd := bdSeqOf(t, last); bd != 3 {
		t.Fatalf("second session bdSeq %d, want 3 (2 is the peer's)", bd)
	}
	if bd, up := n.SessionBdSeq(); !up || bd != 3 {
		t.Fatalf("SessionBdSeq %d %v, want 3 up", bd, up)
	}
}

// Without a bdseq-file a node started again goes on from the bdSeq its
// stop moved to, not from 0 (a pair's leader that leads again).
func TestANodeStartedAgainGoesOnFromItsLastBdSeq(t *testing.T) {
	broker := "tcp://" + testBroker(t)
	w := watchWire(t, broker)
	n := newRedundantNode(t, broker)
	for range 2 {
		if err := n.Start(t.Context()); err != nil {
			t.Fatal(err)
		}
		w.waitCount(t, "NBIRTH", w.count("NBIRTH")+1)
		n.Stop()
	}
	var bds []int64
	for _, m := range w.all() {
		if m.kind == "NBIRTH" {
			bds = append(bds, bdSeqOf(t, m.p))
		}
	}
	if len(bds) != 2 || bds[0] != 0 || bds[1] != 1 {
		t.Fatalf("NBIRTH bdSeqs %v, want [0 1]", bds)
	}
}

// An NDEATH of another session that arrives while the node is born (the
// old leader's will, fired late) makes it birth again at once, same bdSeq:
// a host that matches deaths without their bdSeq ends with the node
// online. spark_death_watch_off turns it red.
func TestALateDeathOfAnotherSessionBirthsAgain(t *testing.T) {
	broker := "tcp://" + testBroker(t)
	w := watchWire(t, broker)
	n := newRedundantNode(t, broker, WithDeathWatch())
	if err := n.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer n.Stop()
	w.waitCount(t, "NBIRTH", 1)
	bd := bdSeqOf(t, w.all()[0].p)

	will, err := deathPayloadFor(uint64(bd + 7))
	if err != nil {
		t.Fatal(err)
	}
	w.cli.Publish("spBv1.0/Plant/NDEATH/Edge1", 1, false, will).WaitTimeout(time.Second)
	w.waitCount(t, "NBIRTH", 2)

	// A host that ignores bdSeq: online is whatever came last.
	online := false
	var last Payload
	for _, m := range w.all() {
		switch m.kind {
		case "NBIRTH":
			online, last = true, m.p
		case "NDEATH":
			online = false
		}
	}
	if !online {
		t.Fatalf("a host ignoring bdSeq sees the node offline: %v", w.kinds())
	}
	if got := bdSeqOf(t, last); got != bd {
		t.Fatalf("the rebirth's bdSeq %d, want the session's %d", got, bd)
	}
	// Its own stop's NDEATH does not make it birth again.
	n.Stop()
	time.Sleep(200 * time.Millisecond)
	if c := w.count("NBIRTH"); c != 2 {
		t.Fatalf("%d NBIRTH after the stop, want 2", c)
	}
}

// The node's own metrics are in every NBIRTH and in an NDATA only when one
// changed; a tag of the same name is left out (#798).
func TestNodeMetricsInTheBirthAndOnChange(t *testing.T) {
	broker := "tcp://" + testBroker(t)
	w := watchWire(t, broker)
	var role atomic.Value
	role.Store("leader")
	n := newRedundantNode(t, broker, WithNodeMetrics(func() []Metric {
		return []Metric{
			{Name: "Redundancy/Role", Datatype: spb.DataType_String, Value: role.Load().(string)},
			{Name: "SpeedSP", Datatype: spb.DataType_String, Value: "mine"},
		}
	}))
	if err := n.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer n.Stop()
	w.waitCount(t, "NBIRTH", 1)
	birth := w.all()[0].p
	if m, ok := metric(birth, "Redundancy/Role"); !ok || m.Value != "leader" {
		t.Fatalf("NBIRTH Redundancy/Role %+v %v, want leader", m, ok)
	}
	if m, ok := metric(birth, "SpeedSP"); !ok || m.Value != "mine" {
		t.Fatalf("NBIRTH SpeedSP %+v, want the node's own metric, not the tag", m)
	}
	time.Sleep(150 * time.Millisecond)
	if c := w.count("NDATA"); c != 0 {
		t.Fatalf("%d NDATA with nothing changed, want none", c)
	}
	role.Store("standby")
	w.waitCount(t, "NDATA", 1)
	_ = n.rt.Tags().SetPath("SpeedSP", 40.0) // the shadowed tag: nothing
	time.Sleep(150 * time.Millisecond)
	var data []Metric
	for _, m := range w.all() {
		if m.kind == "NDATA" {
			data = append(data, m.p.Metrics...)
		}
	}
	if len(data) != 1 || data[0].Name != "Redundancy/Role" || data[0].Value != "standby" {
		t.Fatalf("NDATA metrics %+v, want Redundancy/Role=standby alone", data)
	}
}

// A Start that cannot connect leaves the node startable and its Stop a
// no-op: before, Stop waited forever for a tick loop that never ran.
func TestAFailedStartLeavesAStopThatReturns(t *testing.T) {
	n := newRedundantNode(t, "tcp://127.0.0.1:1") // nothing listens
	if err := n.Start(t.Context()); err == nil {
		t.Fatal("Start against no broker succeeded")
	}
	done := make(chan struct{})
	go func() { n.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop after a failed Start did not return")
	}
	if _, up := n.SessionBdSeq(); up {
		t.Fatal("a failed Start reads as a session up")
	}
}

// Every fenced publish (births, data) is counted for a switchover's drain
// and ended once it has left; a stop's NDEATH is not (the gate is already
// shut when it goes). spark_send_uncounted turns it red.
func TestFencedPublishesAreCountedForTheDrain(t *testing.T) {
	broker := "tcp://" + testBroker(t)
	w := watchWire(t, broker)
	var entered, ended atomic.Int64
	n := newRedundantNode(t, broker, WithSendCount(func() func() {
		entered.Add(1)
		return func() { ended.Add(1) }
	}))
	if err := n.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	w.waitCount(t, "NBIRTH", 1)
	_ = n.rt.Tags().SetPath("SpeedSP", 21.0)
	w.waitCount(t, "NDATA", 1)
	time.Sleep(50 * time.Millisecond)
	if e := entered.Load(); e < 2 || e != ended.Load() {
		t.Fatalf("%d publishes counted, %d ended; want the NBIRTH and the NDATA at least, all ended", e, ended.Load())
	}
	e := entered.Load()
	n.Stop()
	w.waitCount(t, "NDEATH", 1)
	if entered.Load() != e {
		t.Fatalf("the stop's NDEATH was counted (%d, then %d)", e, entered.Load())
	}
}
