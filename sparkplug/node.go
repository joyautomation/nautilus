package sparkplug

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/runtime"
	"github.com/joyautomation/nautilus/sparkplug/spb"
)

// Config is the Sparkplug B edge-node identity and broker connection.
type Config struct {
	BrokerURL string // e.g. "tcp://localhost:1883", "ssl://host:8883"
	GroupID   string // Sparkplug group_id
	EdgeNode  string // Sparkplug edge_node_id
	ClientID  string // MQTT client id (default "<group>-<edge>")
	Username  string
	Password  string
	Keepalive time.Duration // default 30s
	// BdSeqFile persists the birth-death sequence across restarts (TCK wants
	// bdSeq to increment across sessions). Empty = in-memory only (starts 0).
	BdSeqFile string
	// PublishInterval is how often the node samples the tag store for changes
	// (default 100ms). RBE decides what actually publishes.
	PublishInterval time.Duration
	// PrimaryHostID gates birth on a primary host's STATE (see host.go).
	// Empty = publish immediately, no gating.
	PrimaryHostID string
	Log           *slog.Logger
}

// Device is a Sparkplug device behind this edge node — one io.Driver's worth
// of tags, with a lifecycle (DBIRTH/DDEATH) that tracks its connection health.
type Device struct {
	ID     string      // Sparkplug device_id
	Tags   []string    // tag-store names this device contributes
	Health func() bool // current comms health; nil = always healthy
}

// Node is a Sparkplug B edge node publishing a runtime's tag store.
type Node struct {
	cfg Config
	rt  *runtime.Runtime
	log *slog.Logger
	cli mqtt.Client

	devices  []Device
	tagOwner map[string]string // tag -> device id ("" = node level)
	flatten  bool              // WithFlattenUDTs: UDT members as plain metrics, no Templates

	// publish classes
	classRBE    map[string]RBE
	assignments []classAssignment

	// pubMu serialises the node's session messages onto the wire. A birth
	// (NBIRTH, then each DBIRTH) and a publish tick (DATA, device
	// DBIRTH/DDEATH, store-and-forward replay) each hold it from the moment
	// they decide what to send until the last of it is handed to paho, so
	// the two never interleave — and paho writes publishes in the order they
	// are issued, so that is the wire order too. Lock order: pubMu, then mu.
	// The NCMD handler never waits on it (paho with OrderMatters must not
	// block in a callback) — see requestRebirth. The primary-host STATE
	// handler's birth does, as it always waited on its own NBIRTH publish;
	// every wait under pubMu is bounded by tokenTimeout or the lost signal.
	pubMu sync.Mutex

	mu           sync.Mutex
	bdSeq        uint64
	seq          uint64
	born         bool
	bornMs       int64  // epoch ms of the last NBIRTH
	msgs         uint64 // DATA messages published (NDATA + DDATA)
	lastPubMs    int64  // epoch ms of the last DATA publish
	rbeState     map[string]*rbeState
	known        map[string]bool // metric names present in the last birth
	devHealth    map[string]bool
	hostOnline   bool
	hostTS       int64 // last STATE timestamp seen (monotonic guard)
	rebirthTimer *time.Timer
	// cmdWarned dedupes the log-once diagnostics handleCommand emits (a
	// template command for an unknown tag, an unknown member), keyed by an
	// opaque reason string.
	cmdWarned map[string]bool
	// rebirthPending is set the moment a rebirth is requested — synchronously
	// in the NCMD handler, before the rebirth itself is scheduled — and
	// cleared by the birth that answers it. While it is set the node is not
	// live (liveLocked): no DATA, DBIRTH or DDEATH is issued.
	rebirthPending bool
	stopping       bool           // set by Stop before it mutates bdSeq/born; birth/Rebirth no-op once true
	inflight       sync.WaitGroup // in-flight birth()/Rebirth() calls; Stop waits for this to drain

	// Publish-tick working set, all under n.mu. The tag STORE decides when
	// these are stale: everything here is derived from the set of tag NAMES,
	// which changes only when a tag is created, so it is rebuilt on a change
	// of runtime.Tags.NameGeneration and never once per tick. Before this,
	// every 100ms tick re-sorted 550 names (once per destination) and
	// re-ran path.Match over every class pattern for every tag.
	shapeGen  uint64                    // Tags.NameGeneration this was built from
	shapeOK   bool                      // false until the first build
	pubNames  []string                  // published tags, sorted
	ownerName map[string][]string       // device id ("" = node) → its sorted tags
	tagRBE    map[string]RBE            // published tag → its resolved class rule
	snapBuf   map[string]runtime.Sample // reused across ticks; see Tags.SnapshotInto
	snapGen   uint64                    // store write generation snapBuf holds

	sf *storeForward // nil unless WithStoreForward

	tokenTimeout time.Duration // bound on every MQTT token wait; tests shorten it
	// lost is closed by connectionLost and replaced for the next connection.
	// A publish captures it before issuing its token and waits on it too, so
	// a token orphaned by a teardown is abandoned the moment paho reports the
	// loss instead of at tokenTimeout.
	lost chan struct{}
	// births counts birth() completions. A publish that started before a
	// birth and failed after it must not hand its seq back into the new
	// sequence; the count is what tells the two apart.
	births uint64

	cancel context.CancelFunc
	done   chan struct{}
}

const (
	connectTimeout = 30 * time.Second
	// tokenTimeout bounds every MQTT token wait in the edge node (the same
	// bound sparkplug/host uses). A QoS 0 publish completes on the socket
	// write, so a token still pending after this long belongs to a link that
	// is dead, not slow.
	tokenTimeout = 10 * time.Second
)

var (
	// errPublishTimeout: the token neither completed nor failed within
	// tokenTimeout.
	errPublishTimeout = errors.New("sparkplug: publish timed out")
	// errConnectionLost: paho reported the connection the token was issued on
	// lost before the token completed.
	errConnectionLost = errors.New("sparkplug: publish abandoned, connection lost")
)

// Option configures a Node.
type Option func(*Node)

// WithDevice registers a Sparkplug device — typically an io.Driver's tags,
// with Health wired to the driver so the device births/dies with its link.
func WithDevice(d Device) Option {
	return func(n *Node) { n.devices = append(n.devices, d) }
}

// New builds an edge node over a runtime. Publish policy (classes, devices)
// is supplied via options.
func New(rt *runtime.Runtime, cfg Config, opts ...Option) (*Node, error) {
	if cfg.GroupID == "" || cfg.EdgeNode == "" {
		return nil, fmt.Errorf("sparkplug: GroupID and EdgeNode are required")
	}
	if cfg.BrokerURL == "" {
		cfg.BrokerURL = "tcp://localhost:1883"
	}
	if cfg.ClientID == "" {
		cfg.ClientID = cfg.GroupID + "-" + cfg.EdgeNode
	}
	if cfg.Keepalive == 0 {
		cfg.Keepalive = 30 * time.Second
	}
	if cfg.PublishInterval == 0 {
		cfg.PublishInterval = 100 * time.Millisecond
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	n := &Node{
		cfg:       cfg,
		rt:        rt,
		log:       cfg.Log,
		classRBE:  map[string]RBE{DefaultClass: {}},
		rbeState:  map[string]*rbeState{},
		known:     map[string]bool{},
		devHealth: map[string]bool{},
		tagOwner:  map[string]string{},

		tokenTimeout: tokenTimeout,
		lost:         make(chan struct{}),
	}
	for _, o := range opts {
		o(n)
	}
	// Validate metric-class assignments name a defined class.
	for _, a := range n.assignments {
		if a.class == NoPublish {
			continue
		}
		if _, ok := n.classRBE[a.class]; !ok {
			return nil, fmt.Errorf("sparkplug: WithMetricClass(%q, ...) names an undefined class — add WithPublishClass(%q, ...)", a.class, a.class)
		}
	}
	for _, d := range n.devices {
		for _, t := range d.Tags {
			n.tagOwner[t] = d.ID
		}
	}
	return n, nil
}

// ── topics ────────────────────────────────────────────────────────────────

func (n *Node) topic(msgType string) string {
	return fmt.Sprintf("spBv1.0/%s/%s/%s", n.cfg.GroupID, msgType, n.cfg.EdgeNode)
}

func (n *Node) deviceTopic(msgType, device string) string {
	return fmt.Sprintf("spBv1.0/%s/%s/%s/%s", n.cfg.GroupID, msgType, n.cfg.EdgeNode, device)
}

// ── lifecycle ─────────────────────────────────────────────────────────────

// Start connects to the broker and runs the node until ctx is cancelled or
// Stop is called. Connection loss is handled by paho's auto-reconnect (which
// re-fires onConnect → rebirth).
func (n *Node) Start(ctx context.Context) error {
	ctx, n.cancel = context.WithCancel(ctx)
	n.done = make(chan struct{})

	// bdSeq for this session: (persisted+1)%256. The will and the NBIRTH
	// must carry the same value, so load it before building the will.
	n.mu.Lock()
	n.bdSeq = n.loadBdSeq()
	n.stopping = false // clear any stop from a previous session (Node is reused across Start/Stop)
	n.mu.Unlock()

	willTopic := n.topic("NDEATH")
	willPayload, err := n.deathPayload()
	if err != nil {
		return err
	}
	n.saveBdSeq(n.bdSeq)

	opts := mqtt.NewClientOptions().
		AddBroker(n.cfg.BrokerURL).
		SetClientID(n.cfg.ClientID).
		SetKeepAlive(n.cfg.Keepalive).
		SetCleanSession(true).
		SetAutoReconnect(true).
		SetConnectTimeout(30*time.Second).
		SetOrderMatters(true).
		SetBinaryWill(willTopic, willPayload, 1, false).
		SetOnConnectHandler(n.onConnect).
		SetConnectionLostHandler(n.connectionLost)
	if n.cfg.Username != "" {
		opts.SetUsername(n.cfg.Username).SetPassword(n.cfg.Password)
	}

	n.cli = mqtt.NewClient(opts)
	tok := n.cli.Connect()
	if !tok.WaitTimeout(connectTimeout) {
		return fmt.Errorf("sparkplug: connect %s: timed out", n.cfg.BrokerURL)
	}
	if tok.Error() != nil {
		return fmt.Errorf("sparkplug: connect %s: %w", n.cfg.BrokerURL, tok.Error())
	}

	go n.run(ctx)
	return nil
}

// Stop publishes NDEATH, disconnects cleanly, and waits for the loop to exit.
func (n *Node) Stop() {
	if n.cancel == nil {
		return
	}
	// Block any birth()/Rebirth() that hasn't started yet (a connect- or
	// command-triggered one can fire concurrently with Stop, from paho's own
	// goroutines) and wait for whichever is already running to finish before
	// we touch bdSeq/born below. mu is not held across Wait — birth/Rebirth
	// take mu themselves to do their own bookkeeping.
	n.mu.Lock()
	n.stopping = true
	n.mu.Unlock()
	n.inflight.Wait()

	n.cancel()
	<-n.done
	// Graceful death: NDEATH before DISCONNECT (a clean disconnect does not
	// fire the will), then advance bdSeq for the next session.
	n.mu.Lock()
	born := n.born
	n.mu.Unlock()
	if born && n.cli.IsConnected() {
		if p, err := n.deathPayload(); err == nil {
			if err := n.publish(n.topic("NDEATH"), 1, false, p); err != nil {
				n.log.Warn("sparkplug: NDEATH not confirmed", "error", err)
			}
		}
	}
	if n.cli != nil {
		n.cli.Disconnect(250)
	}
	n.mu.Lock()
	n.bdSeq = (n.bdSeq + 1) % 256
	n.born = false
	n.mu.Unlock()
}

// onConnect (re)subscribes to command topics and births. Fires on first
// connect and on every auto-reconnect, from paho's own connection goroutine —
// asynchronously with respect to Start() returning, and (via reconnect) even
// after Stop() has begun. beginInflight/n.inflight is what lets Stop() wait
// for this before it mutates bdSeq/born.
func (n *Node) onConnect(_ mqtt.Client) {
	n.cli.Subscribe(n.topic("NCMD"), 1, n.handleCommand)
	n.cli.Subscribe(n.deviceTopic("DCMD", "+"), 1, n.handleCommand)
	n.subscribeHost() // no-op when no primary host configured
	if n.cfg.PrimaryHostID != "" && !n.primaryOnline() {
		n.log.Info("sparkplug: waiting for primary host before birth", "host", n.cfg.PrimaryHostID)
		return // birth deferred until STATE=ONLINE (see host.go)
	}
	if err := n.birth(); err != nil {
		n.log.Error("sparkplug: birth failed", "error", err)
	}
}

// connectionLost is paho's connection-lost handler, run from a paho goroutine
// once paho has noticed the loss — which can be long after the link actually
// died, and (on a fast reconnect) barely before onConnect births again.
// Besides clearing born it retires n.lost, which abandons any wait on a token
// issued on the connection that just died.
func (n *Node) connectionLost(_ mqtt.Client, e error) {
	n.mu.Lock()
	n.born = false
	close(n.lost)
	n.lost = make(chan struct{})
	n.mu.Unlock()
	n.log.Warn("sparkplug: connection lost", "error", e)
}

// beginInflight registers an in-flight birth/rebirth attempt and reports
// whether it may proceed. It reports false — no registration — once Stop has
// begun (n.stopping), so a rebirth triggered concurrently with Stop (by
// paho's connection or subscription goroutines: onConnect, handleCommand's
// `go n.Rebirth()`, the primary-host STATE handler, or the rebirth-debounce
// timer) no-ops instead of racing Stop's bdSeq/born mutation. A caller that
// gets true owns a matching n.inflight.Done(), typically via defer.
func (n *Node) beginInflight() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.stopping {
		return false
	}
	n.inflight.Add(1)
	return true
}

func (n *Node) run(ctx context.Context) {
	defer close(n.done)
	tick := time.NewTicker(n.cfg.PublishInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			n.scanAndPublish()
		}
	}
}

// publish issues one MQTT publish and waits for it, bounded. It returns nil
// once the flow completed cleanly, the token's error if it completed with
// one, errConnectionLost once paho reports the connection it was issued on
// gone, and errPublishTimeout after tokenTimeout. Every publish in this
// package comes through here.
//
// The connection-lost exit matters more than the bound. paho (1.5.1)
// completes a QoS 0 publish token only when the packet is written to the
// socket; a token handed to a connection that is then torn down is never
// completed at all. A tick that waited on it with Wait() parked the publish
// goroutine for good — the node reconnected, rebirthed from paho's own
// goroutine, and never sent data again. And a plain WaitTimeout is not
// enough either: paho can lose and re-establish the connection well inside
// the bound (the repro script sees it within a second), after which the
// connection is open and healthy while the orphaned token is still pending.
// So the wait also watches the lost signal captured before the token was
// issued.
func (n *Node) publish(topic string, qos byte, retained bool, payload []byte) error {
	n.mu.Lock()
	lost := n.lost
	n.mu.Unlock()
	tok := n.cli.Publish(topic, qos, retained, payload)
	timer := time.NewTimer(n.tokenTimeout)
	defer timer.Stop()
	select {
	case <-tok.Done():
		return tok.Error()
	case <-lost:
		// The connection went between the capture above and the publish,
		// and the token may have completed on its successor: a completed
		// token is the truth, and a message that did go out must not hand
		// its seq back.
		select {
		case <-tok.Done():
			return tok.Error()
		default:
			return errConnectionLost
		}
	case <-timer.C:
		return errPublishTimeout
	}
}

// nextSeq advances and returns the Sparkplug sequence number (0-255).
// Caller holds n.mu.
func (n *Node) nextSeq() uint64 {
	n.seq = (n.seq + 1) % 256
	return n.seq
}

// unsentSeq hands back the sequence number of a message that did not go out,
// so the next message reuses it and the host sees no gap. It is a no-op if a
// birth has restarted the sequence since the number was taken. Caller holds
// n.mu.
func (n *Node) unsentSeq(seq, births uint64) {
	if n.births != births || n.seq != seq {
		return
	}
	n.seq = (seq + 255) % 256
}

// ── bdSeq persistence ─────────────────────────────────────────────────────

// loadBdSeq reads the persisted bdSeq and returns the value to use for this
// session: (saved+1)%256, since the saved value was the previous session's.
func (n *Node) loadBdSeq() uint64 {
	if n.cfg.BdSeqFile == "" {
		return 0
	}
	b, err := os.ReadFile(n.cfg.BdSeqFile)
	if err != nil {
		return 0
	}
	v, err := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
	if err != nil {
		return 0
	}
	return (v + 1) % 256
}

func (n *Node) saveBdSeq(v uint64) {
	if n.cfg.BdSeqFile == "" {
		return
	}
	tmp := n.cfg.BdSeqFile + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.FormatUint(v, 10)), 0o644); err != nil {
		n.log.Warn("sparkplug: bdSeq save", "error", err)
		return
	}
	_ = os.Rename(tmp, n.cfg.BdSeqFile)
	_ = filepath.Clean(n.cfg.BdSeqFile)
}

// deathPayload builds the NDEATH / will payload: only bdSeq, no seq.
func (n *Node) deathPayload() ([]byte, error) {
	n.mu.Lock()
	bd := n.bdSeq
	n.mu.Unlock()
	return Payload{OmitSeq: true, Metrics: []Metric{
		{Name: "bdSeq", Datatype: spb.DataType_Int64, Value: int64(bd)},
	}}.Encode()
}

// nowMs returns the current time in Sparkplug milliseconds.
func nowMs() uint64 { return uint64(time.Now().UnixMilli()) }

// sortedNames returns the metric names of a snapshot in a stable order so
// births are deterministic.
func sortedNames(snap map[string]ir.Value) []string {
	names := make([]string, 0, len(snap))
	for k := range snap {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// ── status (diagnostics / HMI) ──────────────────────────────────────────────

// Status is a snapshot of the edge node's health for a driver-status card.
type Status struct {
	Group    string `json:"group"`
	EdgeNode string `json:"edgeNode"`
	Broker   string `json:"broker"`

	Connected bool `json:"connected"` // MQTT transport up
	Born      bool `json:"born"`      // NBIRTH published (node online in Sparkplug terms)

	// Primary-host gating (zero values when no PrimaryHostID is configured).
	PrimaryHost     string `json:"primaryHost,omitempty"`
	PrimaryHostSeen bool   `json:"primaryHostSeen"`
	HostOnline      bool   `json:"hostOnline"`

	BdSeq     uint64 `json:"bdSeq"`
	Seq       uint64 `json:"seq"`
	Msgs      uint64 `json:"msgs"`      // DATA messages published this session
	BornMs    int64  `json:"bornMs"`    // epoch ms of the last NBIRTH
	LastPubMs int64  `json:"lastPubMs"` // epoch ms of the last DATA publish

	StoreForward *StoreForwardStatus `json:"storeForward,omitempty"`
	Devices      []DeviceStatus      `json:"devices,omitempty"`
}

// StoreForwardStatus reports the buffer when store-and-forward is enabled.
type StoreForwardStatus struct {
	Buffered int `json:"buffered"`
	Max      int `json:"max"`
}

// DeviceStatus is one Sparkplug device's birth/health state.
type DeviceStatus struct {
	ID     string `json:"id"`
	Online bool   `json:"online"` // DBIRTH published, not yet DDEATH
	Tags   int    `json:"tags"`
}

// Status returns the node's current health snapshot.
func (n *Node) Status() Status {
	n.mu.Lock()
	defer n.mu.Unlock()
	connected := n.cli != nil && n.cli.IsConnected()
	s := Status{
		Group:           n.cfg.GroupID,
		EdgeNode:        n.cfg.EdgeNode,
		Broker:          n.cfg.BrokerURL,
		Connected:       connected,
		Born:            n.born,
		PrimaryHost:     n.cfg.PrimaryHostID,
		PrimaryHostSeen: n.hostTS > 0,
		HostOnline:      n.cfg.PrimaryHostID == "" || n.hostOnline,
		BdSeq:           n.bdSeq,
		Seq:             n.seq,
		Msgs:            n.msgs,
		BornMs:          n.bornMs,
		LastPubMs:       n.lastPubMs,
	}
	if n.sf != nil {
		s.StoreForward = &StoreForwardStatus{Buffered: n.sf.len(), Max: n.sf.max}
	}
	for _, d := range n.devices {
		s.Devices = append(s.Devices, DeviceStatus{
			ID:     d.ID,
			Online: n.devHealth[d.ID],
			Tags:   len(d.Tags),
		})
	}
	return s
}
