package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	mqttsrv "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/listeners"
	"github.com/mochi-mqtt/server/v2/packets"

	"github.com/joyautomation/nautilus/internal/testmqtt"
	"github.com/joyautomation/nautilus/runtime"
	"github.com/joyautomation/nautilus/sparkplug"
	"github.com/joyautomation/nautilus/sparkplug/spb"
)

// ── the supervisor, on fakes ──────────────────────────────────────────────

// fakeSPNode counts starts and stops and keeps the publish gate it is
// given.
type fakeSPNode struct {
	mu      sync.Mutex
	running bool
	starts  int
	stops   int
	fail    bool
	gate    func() bool
	// block, when set, holds Start (a connect that hangs) until closed;
	// entered is closed once a Start is held.
	block, entered chan struct{}
}

func (f *fakeSPNode) Start(context.Context) error {
	if f.block != nil {
		close(f.entered)
		<-f.block
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return errors.New("no broker")
	}
	f.running = true
	f.starts++
	return nil
}

func (f *fakeSPNode) Stop() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.running = false
	f.stops++
}

func (f *fakeSPNode) SetPublishGate(g func() bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gate = g
}

func (f *fakeSPNode) state() (running bool, starts, stops int, gateOpen bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.running, f.starts, f.stops, f.gate != nil && f.gate()
}

// fakeLead is an election gate with a term and the runtime's view of its
// own leadership.
type fakeLead struct {
	open   atomic.Bool
	term   atomic.Uint64
	rtTerm atomic.Uint64
	rtLead atomic.Bool
}

func (l *fakeLead) IsLeader() bool { return l.open.Load() }
func (l *fakeLead) Term() uint64   { return l.term.Load() }
func (l *fakeLead) LeadingTerm() (uint64, bool) {
	return l.rtTerm.Load(), l.rtLead.Load()
}

// lead opens the gate in term and has the runtime take over in it.
func (l *fakeLead) lead(term uint64) {
	l.term.Store(term)
	l.open.Store(true)
	l.rtTerm.Store(term)
	l.rtLead.Store(true)
}

func runSupervisor(t *testing.T, node spNode, l *fakeLead) *spSupervisor {
	t.Helper()
	s := newSPSupervisor(node, l, l)
	s.poll, s.retry = 5*time.Millisecond, 30*time.Millisecond
	s.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithCancel(context.Background())
	go s.Run(ctx)
	t.Cleanup(func() { cancel(); s.Close() })
	return s
}

func spWait(t *testing.T, what string, ok func() bool) {
	t.Helper()
	pairWait(t, what, 2*time.Second, ok)
}

// A standby's node never starts; a leader's starts only once its runtime
// has taken over in the gate's term (the store is live); it stops when the
// gate closes, and a term that moves on ends the session for a new one.
// The node's publish gate is the session's term, still open. spark_ungated
// turns it red.
func TestSparkplugSessionFollowsTheLeadership(t *testing.T) {
	node := &fakeSPNode{}
	l := &fakeLead{}
	runSupervisor(t, node, l)
	time.Sleep(50 * time.Millisecond)
	if running, starts, _, open := node.state(); running || starts != 0 || open {
		t.Fatalf("a standby's node: running %v, %d starts, gate open %v; want stopped and gated", running, starts, open)
	}
	// The gate opens, but the runtime has not taken over in this term yet.
	l.term.Store(4)
	l.open.Store(true)
	l.rtTerm.Store(3)
	l.rtLead.Store(true)
	time.Sleep(50 * time.Millisecond)
	if _, starts, _, _ := node.state(); starts != 0 {
		t.Fatal("the node started before the takeover in the gate's term was done")
	}
	l.lead(4)
	spWait(t, "the session to start", func() bool { r, _, _, open := node.state(); return r && open })

	// The gate closes: the session ends, and its publish gate is shut.
	l.open.Store(false)
	spWait(t, "the session to end", func() bool { r, _, _, _ := node.state(); return !r })
	if _, _, _, open := node.state(); open {
		t.Fatal("the publish gate is open with the election gate closed")
	}
	// A new term while the gate is open again: a new session, and the old
	// session's gate stays shut in it.
	l.lead(5)
	spWait(t, "the next session", func() bool { r, s, _, _ := node.state(); return r && s == 2 })
	l.lead(6)
	spWait(t, "a session for the new term", func() bool { r, s, _, _ := node.state(); return r && s == 3 })
	if _, _, stops, _ := node.state(); stops != 2 {
		t.Fatalf("%d stops, want 2 (the gate close and the term move)", stops)
	}
}

// A switchover's StepDown ends the session of its term before the RESIGN
// and keeps it ended for the rest of that term, though the gate is still
// open; the next term starts one again.
func TestSparkplugStepDownEndsTheTermsSession(t *testing.T) {
	node := &fakeSPNode{}
	l := &fakeLead{}
	s := runSupervisor(t, node, l)
	l.lead(7)
	spWait(t, "the session", func() bool { r, _, _, _ := node.state(); return r })
	s.StepDown(7, time.Second)
	if r, _, stops, _ := node.state(); r || stops != 1 {
		t.Fatalf("after StepDown: running %v, %d stops; want stopped once", r, stops)
	}
	time.Sleep(50 * time.Millisecond) // gate still open in term 7
	if r, starts, _, _ := node.state(); r || starts != 1 {
		t.Fatalf("the session started again in the stepped-down term (running %v, %d starts)", r, starts)
	}
	l.lead(9)
	spWait(t, "a session in the next term", func() bool { r, _, _, _ := node.state(); return r })
}

// A session's gate that reads closed stays closed, though the election's
// gate opens again in the same term (an unhealthy scan, a late renewal):
// a rebirth caught in that moment would otherwise leave the session
// connected and silent for good. The supervisor starts a new session.
// spark_gate_unlatched turns it red (Fable on #798).
func TestSparkplugSessionGateLatchesAndTheSessionRestarts(t *testing.T) {
	node := &fakeSPNode{}
	l := &fakeLead{}
	runSupervisor(t, node, l)
	l.lead(4)
	spWait(t, "the session", func() bool { r, _, _, open := node.state(); return r && open })
	node.mu.Lock()
	gate := node.gate
	node.mu.Unlock()
	l.open.Store(false)
	if gate() {
		t.Fatal("the session's gate reads open with the election's gate closed")
	}
	l.open.Store(true) // the same term, before the supervisor looks
	if gate() {
		t.Fatal("the session's gate opened again in its term after reading closed")
	}
	spWait(t, "a new session", func() bool { r, starts, _, open := node.state(); return r && starts == 2 && open })
}

// A switchover's StepDown fences the session at once, even while the
// supervisor is inside a Start that hangs on its connect; once that Start
// returns, the session is ended, not kept. spark_step_unfenced turns it
// red (Fable on #798).
func TestSparkplugStepDownFencesASessionStillStarting(t *testing.T) {
	node := &fakeSPNode{block: make(chan struct{}), entered: make(chan struct{})}
	l := &fakeLead{}
	s := runSupervisor(t, node, l)
	unblock := sync.OnceFunc(func() { close(node.block) })
	t.Cleanup(unblock) // before the supervisor's: a failed check must not leave Start held
	l.lead(3)
	<-node.entered
	node.mu.Lock()
	gate := node.gate
	node.mu.Unlock()
	if !gate() {
		t.Fatal("the starting session's gate is closed while it leads")
	}
	s.StepDown(3, 30*time.Millisecond) // times out: Run is inside Start
	if gate() {
		t.Fatal("after StepDown the session still may publish in the term it gave up")
	}
	unblock()
	spWait(t, "the late session to end", func() bool { r, starts, stops, _ := node.state(); return !r && starts == 1 && stops == 1 })
}

// A Start that fails (no broker) is tried again while this controller
// leads.
func TestSparkplugFailedStartIsTriedAgain(t *testing.T) {
	node := &fakeSPNode{fail: true}
	l := &fakeLead{}
	runSupervisor(t, node, l)
	l.lead(2)
	time.Sleep(50 * time.Millisecond)
	node.mu.Lock()
	node.fail = false
	node.mu.Unlock()
	spWait(t, "the retried start", func() bool { r, _, _, _ := node.state(); return r })
}

// The Redundancy/ folder: role, the stream's state for the peer, the last
// takeover's kind, time and reason, and the hold with an end that does not
// move while it holds.
func TestRedundancyMetrics(t *testing.T) {
	at := time.UnixMilli(1_700_000_000_000)
	now := at.Add(time.Second)
	holding := true
	age := 2 * time.Second
	r := &redundancyMetrics{
		role:     func() string { return "leader" },
		peer:     func() string { return "sending" },
		takeover: func() runtime.TakeoverReport { return runtime.TakeoverReport{At: at, Reason: "the image is too old"} },
		cause:    func() string { return causeFailover },
		hold:     func() (bool, time.Duration, time.Duration) { return holding, age, 10 * time.Second },
		now:      func() time.Time { return now },
	}
	get := func(ms []sparkplug.Metric, name string) sparkplug.Metric {
		for _, m := range ms {
			if m.Name == "Redundancy/"+name {
				return m
			}
		}
		t.Fatalf("no Redundancy/%s in %+v", name, ms)
		return sparkplug.Metric{}
	}
	ms := r.metrics()
	for name, want := range map[string]any{
		"Role": "leader", "PeerState": "sending", "Takeover": "cold",
		"LastSwitchoverReason": "failover (cold: the image is too old)",
		"LastSwitchoverAt":     at.UnixMilli(), "Hold/Active": true,
		"Hold/Until": now.Add(8 * time.Second).UnixMilli(),
	} {
		if got := get(ms, name).Value; got != want {
			t.Errorf("Redundancy/%s = %v, want %v", name, got, want)
		}
	}
	// A look a moment later, the hold older: the same end.
	now, age = now.Add(300*time.Millisecond), age+300*time.Millisecond+7*time.Millisecond
	if got := get(r.metrics(), "Hold/Until").Value; got != get(ms, "Hold/Until").Value {
		t.Errorf("Hold/Until moved while holding: %v, then %v", get(ms, "Hold/Until").Value, got)
	}
	holding = false
	if m := get(r.metrics(), "Hold/Until"); !m.IsNull {
		t.Errorf("Hold/Until with no hold = %+v, want null", m)
	}
}

// ── two replicas on the embedded broker ───────────────────────────────────

// spEvent is one Sparkplug message of the edge node, as the broker took
// it, by the MQTT client that sent it (a will: the client that died).
type spEvent struct {
	client string
	kind   string // NBIRTH, NDATA, NDEATH, DBIRTH, DDATA
	bdSeq  int64  // NBIRTH and NDEATH; -1 otherwise
	will   bool
	at     time.Time
	p      sparkplug.Payload
}

// spObserver is the broker's view of the edge node: every message by
// client, every subscription by client, and the client ids it refuses.
type spObserver struct {
	mqttsrv.HookBase
	mu     sync.Mutex
	events []spEvent
	subs   map[string][]string
	deny   map[string]bool
}

func (o *spObserver) ID() string { return "sp-observer" }
func (o *spObserver) Provides(b byte) bool {
	return b == mqttsrv.OnPublished || b == mqttsrv.OnWillSent || b == mqttsrv.OnSubscribed ||
		b == mqttsrv.OnConnectAuthenticate || b == mqttsrv.OnACLCheck
}

func (o *spObserver) OnConnectAuthenticate(cl *mqttsrv.Client, _ packets.Packet) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return !o.deny[cl.ID]
}

func (o *spObserver) OnACLCheck(*mqttsrv.Client, string, bool) bool { return true }

func (o *spObserver) OnSubscribed(cl *mqttsrv.Client, pk packets.Packet, _ []byte) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, f := range pk.Filters {
		o.subs[cl.ID] = append(o.subs[cl.ID], f.Filter)
	}
}

func (o *spObserver) OnPublished(cl *mqttsrv.Client, pk packets.Packet) { o.record(cl.ID, pk, false) }
func (o *spObserver) OnWillSent(cl *mqttsrv.Client, pk packets.Packet)  { o.record(cl.ID, pk, true) }

func (o *spObserver) record(client string, pk packets.Packet, will bool) {
	parts := strings.Split(pk.TopicName, "/")
	if len(parts) < 4 || parts[0] != "spBv1.0" || parts[1] != "Plant" || parts[3] != "Edge1" {
		return
	}
	switch parts[2] {
	case "NBIRTH", "NDATA", "NDEATH", "DBIRTH", "DDATA", "DDEATH":
	default:
		return // commands, which a host sends
	}
	p, _ := sparkplug.DecodePayload(pk.Payload)
	e := spEvent{client: client, kind: parts[2], bdSeq: -1, will: will, at: time.Now(), p: p}
	for _, m := range p.Metrics {
		if m.Name == "bdSeq" {
			e.bdSeq, _ = m.Value.(int64)
		}
	}
	o.mu.Lock()
	o.events = append(o.events, e)
	o.mu.Unlock()
}

func (o *spObserver) all() []spEvent {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]spEvent(nil), o.events...)
}

func (o *spObserver) subscribed(client string) []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.subs[client]...)
}

func (o *spObserver) setDeny(client string, deny bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.deny[client] = deny
}

// births is client's NBIRTH events.
func (o *spObserver) births(client string) []spEvent {
	var out []spEvent
	for _, e := range o.all() {
		if e.client == client && e.kind == "NBIRTH" {
			out = append(out, e)
		}
	}
	return out
}

// onePublisher fails when a client published a birth or data while
// another client's session was born (its last NBIRTH not yet followed by
// its NDEATH or will): two publishers for one edge node.
func (o *spObserver) onePublisher(t *testing.T) {
	t.Helper()
	born := map[string]bool{}
	for i, e := range o.all() {
		switch e.kind {
		case "NDEATH":
			born[e.client] = false
			continue
		}
		for c, b := range born {
			if b && c != e.client {
				t.Fatalf("event %d: %s %s while %s's session was born: two publishers for one edge node\n%s", i, e.client, e.kind, c, o.dump())
			}
		}
		if e.kind == "NBIRTH" {
			born[e.client] = true
		}
	}
}

func (o *spObserver) dump() string {
	var b strings.Builder
	for i, e := range o.all() {
		fmt.Fprintf(&b, "%3d %s %-14s %-6s bdSeq %d will %v\n", i, e.at.Format("15:04:05.000"), e.client, e.kind, e.bdSeq, e.will)
	}
	return b.String()
}

// spObsBroker starts the embedded broker with the observer.
func spObsBroker(t *testing.T) (*mqttsrv.Server, string, *spObserver) {
	t.Helper()
	srv := mqttsrv.New(&mqttsrv.Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	o := &spObserver{subs: map[string][]string{}, deny: map[string]bool{}}
	if err := srv.AddHook(o, nil); err != nil {
		t.Fatal(err)
	}
	tcp := testmqtt.NewTCP(srv, listeners.Config{ID: "sp", Address: "127.0.0.1:0"})
	if err := srv.AddListener(tcp); err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve() }()
	t.Cleanup(func() { _ = testmqtt.Close(srv) })
	return srv, "tcp://" + tcp.Address(), o
}

// sparkplugPairFiles is the hot pair's project with a Sparkplug edge node
// on broker, publishing every 50 ms.
func sparkplugPairFiles(broker string) func(a, b, w string) fstest.MapFS {
	return func(a, b, w string) fstest.MapFS {
		fs := hotPairProject(a, b, w)
		y := string(fs["nautilus.yaml"].Data) + fmt.Sprintf("sparkplug:\n  broker: %s\n  group-id: Plant\n  edge-node: Edge1\n  publish-interval: 50ms\n", broker)
		fs["nautilus.yaml"] = &fstest.MapFile{Data: []byte(y)}
		return fs
	}
}

func spClient(n string) string { return "Plant-Edge1-" + n }

// Two replicas on the embedded broker (#798): only the leader holds the
// edge node session, under its own client id; the standby never connects,
// so never subscribes to NCMD. Through a switchover the old leader's
// NDEATH is out before the new leader's NBIRTH, and the new bdSeq is not
// the old one's; through a failover (the leader killed: its will, no
// reconnect) the new bdSeq is not the will's, and that will arriving late
// leaves the node online at a host that matches deaths without bdSeq; and
// back again (the killed box restarts as the standby and is switched to).
// At no instant do two clients publish for the edge node. The leader's
// NBIRTH carries the Redundancy/ folder.
// spark_ungated, spark_no_death_on_stepdown, spark_bdseq_clash,
// spark_publish_after_gate and spark_death_watch_off turn it red.
func TestPairSparkplugOneSessionThroughSwitchoverFailoverAndBack(t *testing.T) {
	srv, broker, obs := spObsBroker(t)
	testPairAPIToken = "api-secret"
	t.Cleanup(func() { testPairAPIToken = "" })
	testPairFiles = sparkplugPairFiles(broker)
	t.Cleanup(func() { testPairFiles = nil })
	// A stop the gate's close asks for comes late: only the switchover's
	// own step-down puts the NDEATH ahead of the new leader's NBIRTH.
	testSPStopDelay = 2 * time.Second
	t.Cleanup(func() { testSPStopDelay = 0 })
	r := newPairRigFull(t, nil, nil)

	lead := r.leader()
	stby := other(lead)
	if !spWaitFor(10*time.Second, func() bool { return len(obs.births(spClient(lead))) > 0 }) {
		t.Fatalf("no NBIRTH from the leader %s; status %+v\n%s", lead, r.nodes[lead].sp.Status(), obs.dump())
	}
	first := obs.births(spClient(lead))[0]
	for name, want := range map[string]any{"Redundancy/Role": "leader", "Redundancy/Hold/Active": false} {
		if m, ok := spMetric(first.p, name); !ok || m.Value != want {
			t.Errorf("the leader's NBIRTH %s = %+v (%v), want %v", name, m.Value, ok, want)
		}
	}
	time.Sleep(300 * time.Millisecond)
	if subs := obs.subscribed(spClient(stby)); len(subs) != 0 {
		t.Fatalf("the standby subscribed to %v, want no session at all", subs)
	}
	if st := r.nodes[stby].sp.Status(); !st.Standby || st.Connected {
		t.Fatalf("the standby's node status %+v, want standby, not connected", st)
	}

	// 1. Switchover.
	r.set(t, lead, "Mode", 5)
	if code, ans := r.readySwitchover(t, lead); code != http.StatusOK || ans.Outcome != "done" {
		t.Fatalf("switchover: %d %+v", code, ans)
	}
	r.waitNewLeader(t, stby, 5*time.Second)
	pairWait(t, "the new leader's NBIRTH", 10*time.Second, func() bool { return len(obs.births(spClient(stby))) > 0 })
	nb := obs.births(spClient(stby))[0]
	oldBd := obs.births(spClient(lead))[len(obs.births(spClient(lead)))-1].bdSeq
	var death *spEvent
	for _, e := range obs.all() {
		if e.client == spClient(lead) && e.kind == "NDEATH" {
			death = &e
			break
		}
	}
	if death == nil || death.will || !death.at.Before(nb.at) {
		t.Fatalf("the old leader's NDEATH %+v, want its own (not a will) before the new leader's NBIRTH at %s\n%s", death, nb.at.Format("15:04:05.000"), obs.dump())
	}
	if nb.bdSeq == oldBd {
		t.Fatalf("the new leader births with bdSeq %d, the old leader's", nb.bdSeq)
	}
	if m, ok := spMetric(nb.p, "Redundancy/Takeover"); !ok || m.Value != "hot" {
		t.Errorf("the new leader's NBIRTH Redundancy/Takeover = %+v, want hot", m.Value)
	}
	if m, ok := spMetric(nb.p, "Redundancy/LastSwitchoverAt"); !ok || m.IsNull {
		t.Errorf("the new leader's NBIRTH Redundancy/LastSwitchoverAt = %+v, want a time", m)
	}
	obs.waitReason(t, spClient(stby), causeSwitchover)
	obs.onePublisher(t)

	// 2. Failover: the new leader dies (its broker link cut, so its will
	// fires; it never reconnects), and the old one takes over.
	killed := stby
	survivor := lead
	killedBd := nb.bdSeq
	// The standby has the leader's session from its images (the stream
	// carries it continuously; only a box that never heard one can clash).
	r.shadowed(t, killed)
	if v := r.nodes[survivor].m.peerEdge.Load(); v != uint64(killedBd)+1 {
		t.Fatalf("the standby's peer edge session %d, want the leader's bdSeq %d + 1", v, killedBd)
	}
	obs.setDeny(spClient(killed), true)
	// The will fires now; the late copy is replayed below.
	if cl, ok := srv.Clients.Get(spClient(killed)); ok {
		cl.Stop(errors.New("killed"))
	}
	r.nodes[killed].crash()
	r.waitNewLeader(t, survivor, 15*time.Second)
	before := len(obs.births(spClient(survivor)))
	pairWait(t, "the survivor's NBIRTH", 10*time.Second, func() bool { return len(obs.births(spClient(survivor))) > before })
	sb := obs.births(spClient(survivor))[before]
	if sb.bdSeq == killedBd {
		t.Fatalf("the survivor births with bdSeq %d, the killed leader's will's", sb.bdSeq)
	}
	obs.waitReason(t, spClient(survivor), causeFailover)
	obs.onePublisher(t)

	// The killed leader's will, arriving late (as a broker fires it once
	// it notices): the survivor births again at once.
	host := mqtt.NewClient(mqtt.NewClientOptions().AddBroker(broker).SetClientID("late-will").SetCleanSession(true))
	if tok := host.Connect(); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		t.Fatalf("late will client: %v", tok.Error())
	}
	defer host.Disconnect(50)
	will, err := (sparkplug.Payload{OmitSeq: true, Metrics: []sparkplug.Metric{{Name: "bdSeq", Datatype: spb.DataType_Int64, Value: killedBd}}}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	host.Publish("spBv1.0/Plant/NDEATH/Edge1", 1, false, will).WaitTimeout(time.Second)
	pairWait(t, "the survivor's rebirth after the late will", 5*time.Second, func() bool {
		return len(obs.births(spClient(survivor))) > before+1
	})
	if online := spOnlineIgnoringBdSeq(obs.all()); !online {
		t.Fatalf("a host that ignores bdSeq sees the edge node offline after the late will\n%s", obs.dump())
	}

	// 3. Back: the killed box restarts as the standby (no session), and a
	// switchover hands the lead back to it.
	obs.setDeny(spClient(killed), false)
	subsBefore := len(obs.subscribed(spClient(killed)))
	r.restart(killed)
	r.inSync(t, killed)
	time.Sleep(300 * time.Millisecond)
	if subs := obs.subscribed(spClient(killed)); len(subs) != subsBefore {
		t.Fatalf("the restarted standby subscribed to %v, want nothing (a standby has no session)", subs[subsBefore:])
	}
	survBd := obs.births(spClient(survivor))[len(obs.births(spClient(survivor)))-1].bdSeq
	kb := len(obs.births(spClient(killed)))
	if code, ans := r.readySwitchover(t, survivor); code != http.StatusOK || ans.Outcome != "done" {
		t.Fatalf("switchover back: %d %+v", code, ans)
	}
	r.waitNewLeader(t, killed, 5*time.Second)
	pairWait(t, "the restarted box's NBIRTH", 10*time.Second, func() bool { return len(obs.births(spClient(killed))) > kb })
	if bd := obs.births(spClient(killed))[kb].bdSeq; bd == survBd {
		t.Fatalf("back on the restarted box: bdSeq %d, the survivor's", bd)
	}
	obs.onePublisher(t)
}

// waitReason waits for client's latest Redundancy/LastSwitchoverReason
// (an NBIRTH's, or an NDATA's since) to be want, a cold takeover's reason
// after it or not: the takeover's report,
// which names the cause, can land just after the birth.
func (o *spObserver) waitReason(t *testing.T, client, want string) {
	t.Helper()
	var last any
	ok := spWaitFor(5*time.Second, func() bool {
		for _, e := range o.all() {
			if e.client != client {
				continue
			}
			if m, ok := spMetric(e.p, "Redundancy/LastSwitchoverReason"); ok {
				last = m.Value
			}
		}
		r, _ := last.(string)
		return r == want || strings.HasPrefix(r, want+" (cold: ")
	})
	if !ok {
		t.Fatalf("%s's Redundancy/LastSwitchoverReason %v, want %q", client, last, want)
	}
}

// What a takeover is called, from what the new leader knows (#798
// follow-up): an operator alarms on a failover, not on a switchover.
func TestTakeoverCause(t *testing.T) {
	for _, c := range []struct {
		handBack, fromResign, takeover bool
		by                             string
		askedEpoch, term               uint64
		want                           string
	}{
		{false, false, false, "", 0, 1, causeStart},
		{false, false, true, "", 0, 5, causeFailover},
		{false, true, true, "", 0, 5, causeSwitchover}, // a leader before the label
		{false, true, true, switchByOperator, 4, 5, causeSwitchover},
		{false, true, true, switchByFailback, 4, 5, causeFailback},
		{false, true, true, "something new", 4, 5, causeSwitchover},  // unknown: a switchover
		{false, true, true, switchByFailback, 5, 5, causeSwitchover}, // an ask of this very term: not the one resigned
		{true, true, true, switchByFailback, 4, 5, causeHandBack},
		{false, false, true, switchByFailback, 4, 5, causeFailover}, // no RESIGN: the label says nothing
	} {
		if got := takeoverCause(c.handBack, c.fromResign, c.takeover, c.by, c.askedEpoch, c.term); got != c.want {
			t.Errorf("%+v: %q, want %q", c, got, c.want)
		}
	}
}

// The switch-ask's by is a label only: a standby answers an ask that
// carries by=failback exactly as one without it (#798 follow-up).
func TestPairSwitchAskByIsOnlyALabel(t *testing.T) {
	r := newSwitchRig(t)
	lead := r.leader()
	stby := other(lead)
	r.switchReady(t, lead)
	ask := func(body string) switchAnswer {
		t.Helper()
		w := httptest.NewRecorder()
		r.nodes[stby].m.stream.serveSwitchAsk(w, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)))
		var a switchAnswer
		if err := json.Unmarshal(w.Body.Bytes(), &a); err != nil {
			t.Fatalf("%s: %d %s", body, w.Code, w.Body.String())
		}
		return a
	}
	term := r.nodes[lead].m.q.Term()
	plain := ask(fmt.Sprintf(`{"epoch":%d}`, term))
	for _, by := range []string{switchByFailback, switchByOperator, "something new"} {
		got := ask(fmt.Sprintf(`{"epoch":%d,"by":%q}`, term, by))
		if !reflect.DeepEqual(got, plain) {
			t.Errorf("by=%q answered %+v, without it %+v", by, got, plain)
		}
	}
	if !plain.Good {
		t.Fatalf("a ready standby refused: %+v", plain)
	}
}

// spWaitFor waits up to d for ok.
func spWaitFor(d time.Duration, ok func() bool) bool {
	for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if ok() {
			return true
		}
	}
	return false
}

// spMetric is the named metric of p.
func spMetric(p sparkplug.Payload, name string) (sparkplug.Metric, bool) {
	for _, m := range p.Metrics {
		if m.Name == name {
			return m, true
		}
	}
	return sparkplug.Metric{}, false
}

// spOnlineIgnoringBdSeq is a host that takes the last NBIRTH or NDEATH as
// the node's state, whatever its bdSeq.
func spOnlineIgnoringBdSeq(evs []spEvent) bool {
	online := false
	for _, e := range evs {
		switch e.kind {
		case "NBIRTH":
			online = true
		case "NDEATH":
			online = false
		}
	}
	return online
}
