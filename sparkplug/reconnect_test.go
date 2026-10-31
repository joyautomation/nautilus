// reconnect_test.go: a dropped broker connection is a new Sparkplug session
// (#364). The node does not let paho auto-reconnect with the old will; it
// makes a new client whose will carries bdSeq+1, and births with that same
// bdSeq, so the will the broker holds and the NBIRTH a host sees never
// disagree.

package sparkplug

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/runtime"
	"github.com/joyautomation/nautilus/sparkplug/internal/linkcut"
	"github.com/joyautomation/nautilus/sparkplug/spb"
)

// startProxiedNode starts edge node Plant/Edge1 against the broker at
// brokerAddr (through a proxy the caller cut), retrying its connect fast.
func startProxiedNode(t *testing.T, brokerAddr string, cfg Config, opts ...Option) (*Node, *runtime.Runtime) {
	t.Helper()
	rt, err := runtime.New(runtime.Options{
		Program: silentLinkProgramST,
		Driver:  nio.NewMemory(),
		Scan:    50 * time.Millisecond,
		Tags: []runtime.TagDef{
			runtime.Setpoint("LevelSP", 10.0),
			runtime.State("LevelFt", 10.0),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg.BrokerURL = "tcp://" + brokerAddr
	cfg.GroupID, cfg.EdgeNode = "Plant", "Edge1"
	cfg.PublishInterval = 50 * time.Millisecond
	n, err := New(rt, cfg, opts...)
	if err != nil {
		t.Fatal(err)
	}
	n.retryInterval = 100 * time.Millisecond
	if err := n.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(n.Stop)
	return n, rt
}

// commander returns a function that publishes an NCMD to Plant/Edge1.
func commander(t *testing.T, addr string) func(Payload) {
	t.Helper()
	cli := mqtt.NewClient(mqtt.NewClientOptions().AddBroker("tcp://" + addr).
		SetClientID("commander").SetCleanSession(true))
	if tok := cli.Connect(); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		t.Fatalf("commander connect: %v", tok.Error())
	}
	t.Cleanup(func() { cli.Disconnect(50) })
	return func(p Payload) {
		t.Helper()
		b, err := p.Encode()
		if err != nil {
			t.Fatal(err)
		}
		tok := cli.Publish("spBv1.0/Plant/NCMD/Edge1", 0, false, b)
		if !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
			t.Fatalf("publish NCMD: %v", tok.Error())
		}
	}
}

func bdSeqFile(t *testing.T, path string) string {
	t.Helper()
	b, _ := os.ReadFile(path)
	return strings.TrimSpace(string(b))
}

// A dropped connection: the broker fires the will (NDEATH bdSeq N), the node
// buffers, reconnects on a new client and births with bdSeq N+1 — whose will
// also carries N+1, so the next drop's NDEATH does — then replays the
// buffer as historical. Commands (writes, the write gate, rebirth) work on
// the new client, and a clean Stop ends with the current bdSeq.
func TestBrokerDropStartsANewSessionWithTheNextBdSeq(t *testing.T) {
	addr := testBroker(t)
	px := linkcut.New(t, addr)
	events := watchNode(t, addr)
	send := commander(t, addr)
	bdFile := filepath.Join(t.TempDir(), "edge1.bdseq")
	n, rt := startProxiedNode(t, px.Addr(), Config{BdSeqFile: bdFile},
		WithStoreForward(100), WithWritable("LevelSP"))

	if b := nextOf(t, events, "NBIRTH", "NDEATH"); b.kind != "NBIRTH" || b.bdSeq != 0 {
		t.Fatalf("first message %s bdSeq %d, want NBIRTH bdSeq 0", b.kind, b.bdSeq)
	}
	first := n.client()

	// Drop 1: the broker publishes the first client's will, bdSeq 0.
	px.Cut()
	if d := nextOf(t, events, "NBIRTH", "NDEATH"); d.kind != "NDEATH" || d.bdSeq != 0 {
		t.Fatalf("after the drop: %s bdSeq %d, want the will, NDEATH bdSeq 0", d.kind, d.bdSeq)
	}
	waitFor(t, "a new client", func() bool { return n.client() != first })
	waitFor(t, "bdseq-file to hold the new session's 1", func() bool { return bdSeqFile(t, bdFile) == "1" })
	if st := n.Status(); st.Born || st.Connected {
		t.Fatalf("during the outage Born=%v Connected=%v, want neither", st.Born, st.Connected)
	}

	// The outage buffers.
	for _, v := range []float64{77, 78} {
		rt.Tags().Set("LevelSP", ir.RealVal(v))
		want := int(v - 76)
		waitFor(t, "the change to buffer", func() bool { return n.Status().StoreForward.Buffered >= want })
	}

	// Back: NBIRTH with bdSeq 1, then the backlog as historical.
	px.Restore()
	if b := nextOf(t, events, "NBIRTH", "NDEATH"); b.kind != "NBIRTH" || b.bdSeq != 1 {
		t.Fatalf("after the reconnect: %s bdSeq %d, want NBIRTH bdSeq 1", b.kind, b.bdSeq)
	}
	var replay []float64
	for len(replay) < 2 {
		ev := nextOf(t, events, "NDATA", "NDEATH", "NBIRTH")
		if ev.kind != "NDATA" {
			t.Fatalf("%s during the replay", ev.kind)
		}
		if !ev.historical || !ev.hasLevel {
			t.Fatalf("NDATA during the replay is not the historical LevelSP: %+v", ev)
		}
		replay = append(replay, ev.level)
	}
	if replay[0] != 77 || replay[1] != 78 {
		t.Errorf("replayed LevelSP %v, want [77 78]", replay)
	}
	if got := n.Status().BdSeq; got != 1 {
		t.Errorf("Status().BdSeq = %d, want 1", got)
	}

	// NCMD on the new client: a write, a refusal by the write gate, and a
	// rebirth (same session, same bdSeq).
	send(Payload{Metrics: []Metric{{Name: "LevelSP", Datatype: spb.DataType_Double, Value: 42.0}}})
	waitFor(t, "the NCMD write", func() bool { return rt.Tags().Real("LevelSP") == 42 })
	send(Payload{Metrics: []Metric{{Name: "LevelFt", Datatype: spb.DataType_Double, Value: 1.0}}})
	waitFor(t, "the write gate to refuse LevelFt", func() bool { return n.Status().CommandsRefused == 1 })
	send(rebirthCommand())
	if b := nextOf(t, events, "NBIRTH", "NDEATH"); b.kind != "NBIRTH" || b.bdSeq != 1 {
		t.Fatalf("after a rebirth command: %s bdSeq %d, want NBIRTH bdSeq 1", b.kind, b.bdSeq)
	}

	// Drop 2: the will the broker holds now is the second client's, bdSeq 1.
	px.Cut()
	if d := nextOf(t, events, "NBIRTH", "NDEATH"); d.kind != "NDEATH" || d.bdSeq != 1 {
		t.Fatalf("after the second drop: %s bdSeq %d, want NDEATH bdSeq 1", d.kind, d.bdSeq)
	}
	px.Restore()
	if b := nextOf(t, events, "NBIRTH", "NDEATH"); b.kind != "NBIRTH" || b.bdSeq != 2 {
		t.Fatalf("after the second reconnect: %s bdSeq %d, want NBIRTH bdSeq 2", b.kind, b.bdSeq)
	}
	if got := bdSeqFile(t, bdFile); got != "2" {
		t.Errorf("bdseq-file = %q, want 2", got)
	}

	// A clean stop: NDEATH with the current bdSeq, published by the node.
	n.Stop()
	if d := nextOf(t, events, "NBIRTH", "NDEATH"); d.kind != "NDEATH" || d.bdSeq != 2 {
		t.Fatalf("on Stop: %s bdSeq %d, want NDEATH bdSeq 2", d.kind, d.bdSeq)
	}
	noneOf(t, events, 300*time.Millisecond, "NDEATH", "NBIRTH")
}

// With a primary host (the default, buffer), the new client subscribes to
// its STATE again: an offline STATE after the reconnect stops data without
// ending the session, as before the drop.
func TestBrokerDropResubscribesToThePrimaryHost(t *testing.T) {
	addr := testBroker(t)
	px := linkcut.New(t, addr)
	events := watchNode(t, addr)
	setState := stateHost(t, addr)
	n, rt := startProxiedNode(t, px.Addr(), Config{PrimaryHostID: "ScadaHost"}, WithStoreForward(100))

	setState(true, 1000)
	if b := nextOf(t, events, "NBIRTH", "NDEATH"); b.kind != "NBIRTH" || b.bdSeq != 0 {
		t.Fatalf("first message %s bdSeq %d, want NBIRTH bdSeq 0", b.kind, b.bdSeq)
	}
	px.Cut()
	if d := nextOf(t, events, "NBIRTH", "NDEATH"); d.kind != "NDEATH" || d.bdSeq != 0 {
		t.Fatalf("after the drop: %s bdSeq %d, want NDEATH bdSeq 0", d.kind, d.bdSeq)
	}
	px.Restore()
	if b := nextOf(t, events, "NBIRTH", "NDEATH"); b.kind != "NBIRTH" || b.bdSeq != 1 {
		t.Fatalf("after the reconnect: %s bdSeq %d, want NBIRTH bdSeq 1", b.kind, b.bdSeq)
	}
	cli := n.client()

	setState(false, 2000)
	waitFor(t, "the new client to hear the host go offline", func() bool { return !n.Status().HostOnline })
	rt.Tags().Set("LevelSP", ir.RealVal(55))
	waitFor(t, "the change to buffer", func() bool { return n.Status().StoreForward.Buffered >= 1 })
	noneOf(t, events, 300*time.Millisecond, "NDATA", "NDEATH", "NBIRTH")
	if n.client() != cli || !n.Status().Born {
		t.Fatal("the default buffer mode ended the session on an offline host")
	}
	setState(true, 3000)
	ev := nextOf(t, events, "NDATA", "NDEATH", "NBIRTH")
	if ev.kind != "NDATA" || !ev.historical || ev.level != 55 {
		t.Fatalf("after the host returned: %+v, want the historical LevelSP 55", ev)
	}
}

// HostOfflineDisconnect after a broker drop: the offline STATE arrives on
// the new client, and ending that session moves bdSeq on again.
func TestBrokerDropThenHostOfflineDisconnect(t *testing.T) {
	addr := testBroker(t)
	px := linkcut.New(t, addr)
	events := watchNode(t, addr)
	setState := stateHost(t, addr)
	startProxiedNode(t, px.Addr(), Config{
		PrimaryHostID: "ScadaHost", PrimaryHostOffline: HostOfflineDisconnect,
	})

	setState(true, 1000)
	if b := nextOf(t, events, "NBIRTH", "NDEATH"); b.kind != "NBIRTH" || b.bdSeq != 0 {
		t.Fatalf("first message %s bdSeq %d, want NBIRTH bdSeq 0", b.kind, b.bdSeq)
	}
	px.Cut()
	if d := nextOf(t, events, "NBIRTH", "NDEATH"); d.kind != "NDEATH" || d.bdSeq != 0 {
		t.Fatalf("after the drop: %s bdSeq %d, want NDEATH bdSeq 0", d.kind, d.bdSeq)
	}
	px.Restore()
	if b := nextOf(t, events, "NBIRTH", "NDEATH"); b.kind != "NBIRTH" || b.bdSeq != 1 {
		t.Fatalf("after the reconnect: %s bdSeq %d, want NBIRTH bdSeq 1", b.kind, b.bdSeq)
	}

	setState(false, 2000)
	if d := nextOf(t, events, "NBIRTH", "NDEATH"); d.kind != "NDEATH" || d.bdSeq != 1 {
		t.Fatalf("on the host going offline: %s bdSeq %d, want NDEATH bdSeq 1", d.kind, d.bdSeq)
	}
	noneOf(t, events, 300*time.Millisecond, "NBIRTH")
	setState(true, 3000)
	if b := nextOf(t, events, "NBIRTH", "NDEATH"); b.kind != "NBIRTH" || b.bdSeq != 2 {
		t.Fatalf("on the host returning: %s bdSeq %d, want NBIRTH bdSeq 2", b.kind, b.bdSeq)
	}
}

// A broker restarted without persistence comes back with no retained STATE,
// and the primary host reconnects on its own schedule. The node must not
// carry "host online" over from the connection it lost: it waits for a
// STATE on the new connection before it births
// (tck-id-message-flow-edge-node-birth-publish-phid-wait), and only then
// replays its store-and-forward backlog. Before the fix it birthed on the
// stale flag and drained the backlog into a broker no host was listening
// on; the host's own reconnect then asked for a rebirth, so the console
// showed two `born` lines with one bdSeq and the outage's history was gone.
func TestBrokerDropWaitsForTheHostsStateBeforeBirth(t *testing.T) {
	addr := testBroker(t)
	px := linkcut.New(t, addr)
	events := watchNode(t, addr)
	setState := stateHost(t, addr)
	n, rt := startProxiedNode(t, px.Addr(), Config{PrimaryHostID: "ScadaHost"}, WithStoreForward(100))

	setState(true, 1000)
	if b := nextOf(t, events, "NBIRTH", "NDEATH"); b.kind != "NBIRTH" || b.bdSeq != 0 {
		t.Fatalf("first message %s bdSeq %d, want NBIRTH bdSeq 0", b.kind, b.bdSeq)
	}
	px.Cut()
	if d := nextOf(t, events, "NBIRTH", "NDEATH"); d.kind != "NDEATH" || d.bdSeq != 0 {
		t.Fatalf("after the drop: %s bdSeq %d, want NDEATH bdSeq 0", d.kind, d.bdSeq)
	}
	// The restart: no retained STATE left (an empty retained payload
	// clears it), as a broker without persistence comes back.
	clr := mqtt.NewClient(mqtt.NewClientOptions().AddBroker("tcp://" + addr).
		SetClientID("state-clear").SetCleanSession(true))
	if tok := clr.Connect(); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		t.Fatalf("clear connect: %v", tok.Error())
	}
	if tok := clr.Publish("spBv1.0/STATE/ScadaHost", 1, true, []byte{}); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		t.Fatalf("clear STATE: %v", tok.Error())
	}
	clr.Disconnect(50)
	rt.Tags().Set("LevelSP", ir.RealVal(77))
	waitFor(t, "the change to buffer", func() bool { return n.Status().StoreForward.Buffered >= 1 })

	px.Restore()
	waitFor(t, "the reconnect", func() bool { return n.Status().Connected })
	noneOf(t, events, 500*time.Millisecond, "NBIRTH", "NDATA")
	if st := n.Status(); st.Born || st.HostOnline || st.StoreForward.Buffered < 1 {
		t.Fatalf("reconnected with no host: Born=%v HostOnline=%v Buffered=%d, want unborn, offline, still buffered",
			st.Born, st.HostOnline, st.StoreForward.Buffered)
	}

	// The host comes back: one birth with the new session's bdSeq, then
	// the backlog, historical, to a host that is there to hear it.
	setState(true, 2000)
	if b := nextOf(t, events, "NBIRTH", "NDEATH"); b.kind != "NBIRTH" || b.bdSeq != 1 {
		t.Fatalf("after the host returned: %s bdSeq %d, want NBIRTH bdSeq 1", b.kind, b.bdSeq)
	}
	ev := nextOf(t, events, "NDATA", "NDEATH", "NBIRTH")
	if ev.kind != "NDATA" || !ev.historical || ev.level != 77 {
		t.Fatalf("after the birth: %+v, want the historical LevelSP 77", ev)
	}
}
