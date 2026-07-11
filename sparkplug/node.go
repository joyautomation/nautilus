package sparkplug

import (
	"context"
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

	// publish classes
	classRBE    map[string]RBE
	assignments []classAssignment

	mu           sync.Mutex
	bdSeq        uint64
	seq          uint64
	born         bool
	rbeState     map[string]*rbeState
	known        map[string]bool // metric names present in the last birth
	devHealth    map[string]bool
	hostOnline   bool
	hostTS       int64 // last STATE timestamp seen (monotonic guard)
	rebirthTimer *time.Timer

	sf *storeForward // nil unless WithStoreForward

	cancel context.CancelFunc
	done   chan struct{}
}

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
		SetConnectionLostHandler(func(_ mqtt.Client, e error) {
			n.mu.Lock()
			n.born = false
			n.mu.Unlock()
			n.log.Warn("sparkplug: connection lost", "error", e)
		})
	if n.cfg.Username != "" {
		opts.SetUsername(n.cfg.Username).SetPassword(n.cfg.Password)
	}

	n.cli = mqtt.NewClient(opts)
	if tok := n.cli.Connect(); tok.Wait() && tok.Error() != nil {
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
	n.cancel()
	<-n.done
	// Graceful death: NDEATH before DISCONNECT (a clean disconnect does not
	// fire the will), then advance bdSeq for the next session.
	n.mu.Lock()
	born := n.born
	n.mu.Unlock()
	if born && n.cli.IsConnected() {
		if p, err := n.deathPayload(); err == nil {
			n.cli.Publish(n.topic("NDEATH"), 1, false, p).Wait()
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
// connect and on every auto-reconnect.
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

// nextSeq advances and returns the Sparkplug sequence number (0-255).
// Caller holds n.mu.
func (n *Node) nextSeq() uint64 {
	n.seq = (n.seq + 1) % 256
	return n.seq
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
