// hostoffline_test.go: what the edge node does when its primary host's
// STATE goes offline, against a real broker. By default (HostOfflineBuffer)
// it keeps its session; with HostOfflineDisconnect it ends it as Sparkplug
// 3.0 has it — NDEATH, disconnect, reconnect with the next bdSeq, and birth
// only once the host is ONLINE again (#333).

package sparkplug

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/joyautomation/nautilus/internal/testmqtt"
	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/runtime"
)

// spEvent is one edge-node message a subscriber saw on the broker.
type spEvent struct {
	kind       string // NBIRTH, NDEATH, NDATA, ...
	bdSeq      int64  // NBIRTH/NDEATH; -1 otherwise
	historical bool
	level      float64 // LevelSP in an NDATA, if present
	hasLevel   bool
}

// watchNode subscribes to every message of edge node Plant/Edge1.
func watchNode(t *testing.T, addr string) chan spEvent {
	t.Helper()
	ch := make(chan spEvent, 256)
	cli := mqtt.NewClient(mqtt.NewClientOptions().AddBroker("tcp://" + addr).
		SetClientID("node-watcher").SetCleanSession(true).SetOrderMatters(true))
	if tok := cli.Connect(); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		t.Fatalf("watcher connect: %v", tok.Error())
	}
	t.Cleanup(func() { cli.Disconnect(50) })
	tok := cli.Subscribe("spBv1.0/Plant/+/Edge1", 1, func(_ mqtt.Client, m mqtt.Message) {
		parts := strings.Split(m.Topic(), "/")
		ev := spEvent{kind: parts[2], bdSeq: -1}
		p, err := DecodePayload(m.Payload())
		if err != nil {
			return
		}
		for _, mt := range p.Metrics {
			switch mt.Name {
			case "bdSeq":
				ev.bdSeq = mt.Value.(int64)
			case "LevelSP":
				if v, ok := mt.Value.(float64); ok {
					ev.level, ev.hasLevel = v, true
				}
			}
			if mt.IsHistorical {
				ev.historical = true
			}
		}
		ch <- ev
	})
	if !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		t.Fatalf("watcher subscribe: %v", tok.Error())
	}
	return ch
}

// nextOf waits for the next event of one of the given kinds, skipping
// others (NDATA from a live tick, say).
func nextOf(t *testing.T, ch chan spEvent, kinds ...string) spEvent {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case ev := <-ch:
			for _, k := range kinds {
				if ev.kind == k {
					return ev
				}
			}
		case <-deadline:
			t.Fatalf("no %v arrived", kinds)
			return spEvent{}
		}
	}
}

// noneOf fails if an event of one of the given kinds arrives within d.
func noneOf(t *testing.T, ch chan spEvent, d time.Duration, kinds ...string) {
	t.Helper()
	deadline := time.After(d)
	for {
		select {
		case ev := <-ch:
			for _, k := range kinds {
				if ev.kind == k {
					t.Fatalf("unexpected %s (bdSeq %d)", ev.kind, ev.bdSeq)
				}
			}
		case <-deadline:
			return
		}
	}
}

// stateHost publishes the primary host's retained 3.0 STATE.
func stateHost(t *testing.T, addr string) func(online bool, ts int64) {
	t.Helper()
	cli := mqtt.NewClient(mqtt.NewClientOptions().AddBroker("tcp://" + addr).
		SetClientID("scada-host").SetCleanSession(true))
	if tok := cli.Connect(); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		t.Fatalf("host connect: %v", tok.Error())
	}
	t.Cleanup(func() { cli.Disconnect(50) })
	return func(online bool, ts int64) {
		t.Helper()
		payload := fmt.Sprintf(`{"online":%t,"timestamp":%d}`, online, ts)
		tok := cli.Publish("spBv1.0/STATE/ScadaHost", 1, true, []byte(payload))
		if !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
			t.Fatalf("publish STATE: %v", tok.Error())
		}
	}
}

func startHostNode(t *testing.T, addr string, action HostOfflineAction, bdFile string, opts ...Option) (*Node, *runtime.Runtime) {
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
	n, err := New(rt, Config{
		BrokerURL: "tcp://" + addr, GroupID: "Plant", EdgeNode: "Edge1",
		PrimaryHostID: "ScadaHost", PrimaryHostOffline: action,
		PublishInterval: 50 * time.Millisecond, BdSeqFile: bdFile,
	}, opts...)
	if err != nil {
		t.Fatal(err)
	}
	if err := n.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(n.Stop)
	return n, rt
}

func (n *Node) client() mqtt.Client {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.cli
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// The default is unchanged: an offline primary host leaves the node
// connected and born on the same MQTT session, with no NDEATH, and the host
// coming back does not rebirth it.
func TestPrimaryHostOfflineDefaultKeepsTheSession(t *testing.T) {
	addr := testBroker(t)
	events := watchNode(t, addr)
	setState := stateHost(t, addr)
	n, _ := startHostNode(t, addr, HostOfflineBuffer, "")

	setState(true, 1000)
	if b := nextOf(t, events, "NBIRTH", "NDEATH"); b.kind != "NBIRTH" || b.bdSeq != 0 {
		t.Fatalf("first message %s bdSeq %d, want NBIRTH bdSeq 0", b.kind, b.bdSeq)
	}
	cli := n.client()

	setState(false, 2000)
	waitFor(t, "the node to see the host offline", func() bool { return !n.Status().HostOnline })
	noneOf(t, events, time.Second, "NDEATH", "NBIRTH")
	if n.client() != cli {
		t.Fatal("the node replaced its MQTT client when the primary host went offline")
	}
	if !cli.IsConnectionOpen() {
		t.Fatal("the node disconnected when the primary host went offline")
	}
	if !n.Status().Born {
		t.Fatal("the node stopped being born when the primary host went offline")
	}

	setState(true, 3000)
	waitFor(t, "the node to see the host online", func() bool { return n.Status().HostOnline })
	noneOf(t, events, 500*time.Millisecond, "NDEATH", "NBIRTH")
}

// The CI flake in TestPrimaryHostOfflineDisconnectEndsTheSession, held
// open: the node ends its session for the offline host and reconnects with
// the same client ID at once, while the broker is still tearing the old
// session down (testmqtt.HoldTeardown keeps it in mochi's window). Without
// testBroker's SameIDSerializer, mochi v2.7.9's teardown then deletes the
// NEW session from its client map: the node got the retained offline STATE
// on subscribe and nothing after, and never births when the host returns
// ("no [NBIRTH NDEATH] arrived", runs 44237035250, 44202630486,
// 44147322368).
func TestHostOfflineReconnectHearsTheHostDuringASlowTeardown(t *testing.T) {
	hold := testmqtt.NewHoldTeardown("Plant-Edge1", 300*time.Millisecond)
	addr := testBroker(t, hold)
	events := watchNode(t, addr)
	setState := stateHost(t, addr)
	n, _ := startHostNode(t, addr, HostOfflineDisconnect, filepath.Join(t.TempDir(), "edge1.bdseq"))

	setState(true, 1000)
	if b := nextOf(t, events, "NBIRTH", "NDEATH"); b.kind != "NBIRTH" || b.bdSeq != 0 {
		t.Fatalf("first message %s bdSeq %d, want NBIRTH bdSeq 0", b.kind, b.bdSeq)
	}
	first := n.client()
	setState(false, 2000)
	if d := nextOf(t, events, "NBIRTH", "NDEATH"); d.kind != "NDEATH" || d.bdSeq != 0 {
		t.Fatalf("after the host went offline: %s bdSeq %d, want NDEATH bdSeq 0", d.kind, d.bdSeq)
	}
	select {
	case <-hold.Disconnected: // the old session's teardown has begun, and is held
	case <-time.After(5 * time.Second):
		t.Fatal("the broker never began tearing the first session down")
	}
	waitFor(t, "a new connection", func() bool {
		c := n.client()
		return c != first && c.IsConnectionOpen()
	})
	time.Sleep(400 * time.Millisecond) // past the held teardown

	setState(true, 3000)
	if b := nextOf(t, events, "NBIRTH", "NDEATH"); b.kind != "NBIRTH" || b.bdSeq != 1 {
		t.Fatalf("after the host returned: %s bdSeq %d, want NBIRTH bdSeq 1", b.kind, b.bdSeq)
	}
}

// HostOfflineDisconnect: on the host's offline STATE the node publishes
// NDEATH (this session's bdSeq) and disconnects, connects again with the
// next bdSeq, buffers meanwhile, and births only once the host is ONLINE —
// then replays what it buffered as historical. An offline STATE older than
// the last online one is ignored.
func TestPrimaryHostOfflineDisconnectEndsTheSession(t *testing.T) {
	addr := testBroker(t)
	events := watchNode(t, addr)
	setState := stateHost(t, addr)
	bdFile := filepath.Join(t.TempDir(), "edge1.bdseq")
	n, rt := startHostNode(t, addr, HostOfflineDisconnect, bdFile, WithStoreForward(100))

	// Births only once the host is online.
	noneOf(t, events, 300*time.Millisecond, "NBIRTH")
	setState(true, 1000)
	if b := nextOf(t, events, "NBIRTH", "NDEATH"); b.kind != "NBIRTH" || b.bdSeq != 0 {
		t.Fatalf("first message %s bdSeq %d, want NBIRTH bdSeq 0", b.kind, b.bdSeq)
	}
	first := n.client()

	// Host offline: NDEATH with this session's bdSeq, then the session ends.
	setState(false, 2000)
	if d := nextOf(t, events, "NBIRTH", "NDEATH"); d.kind != "NDEATH" || d.bdSeq != 0 {
		t.Fatalf("after the host went offline: %s bdSeq %d, want NDEATH bdSeq 0", d.kind, d.bdSeq)
	}
	waitFor(t, "a new connection", func() bool {
		c := n.client()
		return c != first && c.IsConnectionOpen()
	})
	if first.IsConnectionOpen() {
		t.Fatal("the first session's client is still connected")
	}
	if n.Status().Born {
		t.Fatal("the node is born with its primary host offline")
	}
	if b, err := os.ReadFile(bdFile); err != nil || strings.TrimSpace(string(b)) != "1" {
		t.Fatalf("bdseq-file = %q (%v), want the new session's 1", b, err)
	}

	// Disconnected from the host's point of view, the node keeps sampling
	// into store-and-forward and publishes nothing, birth included.
	for _, v := range []float64{77, 78} {
		rt.Tags().Set("LevelSP", ir.RealVal(v))
		want := int(v - 76)
		waitFor(t, "the change to buffer", func() bool { return n.Status().StoreForward.Buffered >= want })
	}
	noneOf(t, events, 500*time.Millisecond, "NBIRTH", "NDEATH", "NDATA")

	// Host back: NBIRTH with the next bdSeq, then the backlog as historical.
	setState(true, 3000)
	if b := nextOf(t, events, "NBIRTH", "NDEATH"); b.kind != "NBIRTH" || b.bdSeq != 1 {
		t.Fatalf("after the host returned: %s bdSeq %d, want NBIRTH bdSeq 1", b.kind, b.bdSeq)
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

	// A stale offline STATE (older than the last online) is ignored:
	// tck-id-operational-behavior-edge-node-termination-host-offline-timestamp.
	second := n.client()
	setState(false, 2500)
	noneOf(t, events, 500*time.Millisecond, "NDEATH", "NBIRTH")
	if n.client() != second || !second.IsConnectionOpen() || !n.Status().Born {
		t.Fatal("a stale offline STATE ended the session")
	}

	// A clean stop still ends with this session's NDEATH.
	n.Stop()
	if d := nextOf(t, events, "NBIRTH", "NDEATH"); d.kind != "NDEATH" || d.bdSeq != 1 {
		t.Fatalf("on Stop: %s bdSeq %d, want NDEATH bdSeq 1", d.kind, d.bdSeq)
	}
}

// HostOfflineDisconnect: a rebirth asked for in the session endSession
// ended (an NCMD, the new-metric timer) must not birth into the next one
// before the host is back; the next session's onConnect or the host's
// ONLINE STATE does that.
func TestHostOfflineDisconnectDoesNotBirthWhileTheHostIsOffline(t *testing.T) {
	n, sc, _ := newPrimaryHostNode(t)
	n.cfg.PrimaryHostOffline = HostOfflineDisconnect
	n.Rebirth()
	if err := n.birth(); err != nil {
		t.Fatal(err)
	}
	if got := len(sc.published("NBIRTH")); got != 0 {
		t.Fatalf("%d NBIRTH with the primary host offline, want 0", got)
	}
	n.handleState(sc, fakeMsg{"spBv1.0/STATE/ScadaHost", []byte(`{"online":true,"timestamp":1000}`)})
	if got := len(sc.published("NBIRTH")); got != 1 {
		t.Fatalf("%d NBIRTH once the host is ONLINE, want 1", got)
	}
	// Online but the connection not open (a new client still connecting):
	// no birth either.
	sc.set(false, false)
	n.Rebirth()
	if got := len(sc.published("NBIRTH")); got != 1 {
		t.Fatalf("%d NBIRTH with the connection not open, want still 1", got)
	}
}
