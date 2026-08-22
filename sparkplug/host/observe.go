// observe.go is the *import-time* half of the host application: a throwaway,
// read-only Sparkplug client that listens to a group, asks the nodes it hears
// from to birth again, and hands back the decoded birth certificates.
//
// It exists because `nautilus sparkplug import` has a chicken-and-egg
// problem. Retained births are forbidden by the spec, so a client joining
// mid-stream sees nothing until it asks — and NCMD Node Control/Rebirth has to
// be addressed to a specific edge node, which we do not know until some
// traffic (NDATA, a death, a birth) names one. So: subscribe, listen, ask
// every node we hear from but have no birth for, and stop once everything
// seen has birthed and nothing new has arrived for a moment — or at the
// deadline.
//
// Deliberately NOT a *Driver:
//
//   - Driver.RequestRebirth only addresses nodes the manifest already
//     declares, and the manifest is what we are trying to generate.
//   - Publishing a retained STATE certificate under a throwaway host id would
//     leave garbage on a production broker forever. An importer is not a
//     primary host: it subscribes, it sends NCMD Rebirth, and it publishes
//     nothing retained.
//
// What it does share with the runtime is the decoder: births come back as
// sparkplug.Payload through sparkplug.DecodePayload, exactly what
// (*Driver).handleMessage consumes, so the generator cannot drift from the
// driver's own reading of the wire.
//
// ADDED BY C2 (`nautilus sparkplug import|browse` + sparkplug/host/codegen).
// New file; nothing else in the package changed.

package host

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/joyautomation/nautilus/sparkplug"
	"github.com/joyautomation/nautilus/sparkplug/spb"
)

// Birth is one decoded birth certificate: an NBIRTH when Device is empty, a
// DBIRTH otherwise. It is the generator's input — sparkplug/host/codegen turns
// a slice of these into sparkplug_types.st, sparkplug_manifest.yaml and
// tags/sparkplug.yaml.
type Birth struct {
	Group    string
	EdgeNode string
	Device   string // "" for an NBIRTH
	Payload  sparkplug.Payload
}

// IsNode reports whether this is a node birth (NBIRTH) rather than a device
// birth (DBIRTH).
func (b Birth) IsNode() bool { return b.Device == "" }

// DiscoverOptions configures one import-time listen.
type DiscoverOptions struct {
	// BrokerURL is required: tcp://host:1883, ssl://host:8883.
	BrokerURL string
	// ClientID defaults to "nautilus-import-<pid>". It is NOT a Sparkplug
	// host id — nothing retained is published under it.
	ClientID           string
	Username, Password string
	// Groups are the group_ids to listen to; empty means the "+" wildcard.
	Groups []string
	// Listen is the hard deadline for the whole discovery (default 30s).
	Listen time.Duration
	// Quiet is how long discovery must go without hearing anything NEW — a
	// node it had not seen, or a birth — with every node seen having birthed,
	// before it returns early (default 2s). 0 means "always listen for the
	// full Listen window".
	//
	// It counts NEW information rather than silence on purpose: a group with
	// sixty sites on it is never quiet, so a plain silence timer would never
	// fire and every import would cost the full window.
	Quiet time.Duration
	// Rebirth publishes NCMD Node Control/Rebirth to every node heard from
	// that has not birthed. Without it a mid-stream import sees only the
	// births that happen to occur inside the listen window.
	Rebirth bool
	// KnownNodes are edge_node_ids to ask for a rebirth immediately at
	// connect, before any traffic names them — a re-import from an existing
	// manifest passes its node list here so a quiet site still answers.
	KnownNodes []string
	// Keepalive defaults to Config's 30s.
	Keepalive time.Duration
	// Log receives progress at Info. Defaults to slog.Default().
	Log *slog.Logger
}

// Discover connects, listens, and returns every birth certificate it saw,
// sorted by group, edge node, then device (node birth first).
//
// It returns an error only for a failure to connect or subscribe: hearing
// nothing is a legitimate (if disappointing) outcome, and the caller reports
// it with the context the CLI has.
func Discover(ctx context.Context, o DiscoverOptions) ([]Birth, error) {
	if o.BrokerURL == "" {
		return nil, fmt.Errorf("host: discover: BrokerURL is required (tcp://host:1883)")
	}
	if o.Listen <= 0 {
		o.Listen = 30 * time.Second
	}
	if o.Quiet < 0 {
		o.Quiet = 0
	}
	if o.Keepalive <= 0 {
		o.Keepalive = defaultKeepalive
	}
	if o.ClientID == "" {
		o.ClientID = fmt.Sprintf("nautilus-import-%d", os.Getpid())
	}
	groups := o.Groups
	if len(groups) == 0 {
		groups = []string{"+"}
	}
	log := o.Log
	if log == nil {
		log = slog.Default()
	}

	d := &discoverer{
		log:     log,
		seen:    map[nodeKey]bool{},
		birthed: map[nodeKey]bool{},
		asked:   map[nodeKey]bool{},
		lastNew: time.Now(),
	}

	opts := mqtt.NewClientOptions().
		AddBroker(o.BrokerURL).
		SetClientID(o.ClientID).
		SetKeepAlive(o.Keepalive).
		SetCleanSession(true).
		SetAutoReconnect(false).
		SetConnectTimeout(connectTimeout).
		SetOrderMatters(true).
		SetDefaultPublishHandler(func(_ mqtt.Client, m mqtt.Message) {
			d.onMessage(m.Topic(), m.Payload())
		})
	if o.Username != "" {
		opts.SetUsername(o.Username).SetPassword(o.Password)
	}

	cli := mqtt.NewClient(opts)
	tok := cli.Connect()
	if !tok.WaitTimeout(connectTimeout) {
		return nil, fmt.Errorf("host: discover: connect %s timed out", o.BrokerURL)
	}
	if err := tok.Error(); err != nil {
		return nil, fmt.Errorf("host: discover: connect %s: %w", o.BrokerURL, err)
	}
	defer cli.Disconnect(250)

	for _, g := range groups {
		for _, suffix := range [...]string{
			"/NBIRTH/+", "/NDEATH/+", "/NDATA/+",
			"/DBIRTH/+/+", "/DDEATH/+/+", "/DDATA/+/+",
		} {
			f := namespace + "/" + g + suffix
			st := cli.Subscribe(f, 1, nil)
			if !st.WaitTimeout(tokenTimeout) {
				return nil, fmt.Errorf("host: discover: subscribe %s timed out", f)
			}
			if err := st.Error(); err != nil {
				return nil, fmt.Errorf("host: discover: subscribe %s: %w", f, err)
			}
		}
	}
	log.Info("sparkplug import: listening", "broker", o.BrokerURL,
		"groups", strings.Join(groups, ","), "for", o.Listen)

	// Seed the rebirth targets a caller already knows about, so a site that
	// would otherwise stay silent for the whole window answers immediately.
	if o.Rebirth {
		for _, g := range groups {
			if g == "+" {
				continue // cannot address a wildcard group
			}
			for _, edge := range o.KnownNodes {
				d.requestRebirth(cli, nodeKey{Group: g, EdgeNode: edge})
			}
		}
	}

	deadline := time.After(o.Listen)
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return d.births(), ctx.Err()
		case <-deadline:
			return d.births(), nil
		case <-tick.C:
			if o.Rebirth {
				for _, nk := range d.unbirthed() {
					d.requestRebirth(cli, nk)
				}
			}
			if o.Quiet > 0 && d.settled(o.Quiet) {
				return d.births(), nil
			}
		}
	}
}

// discoverer is Discover's mutable state. paho delivers messages on its own
// goroutine, so everything here is behind mu.
type discoverer struct {
	log *slog.Logger

	mu      sync.Mutex
	out     []Birth
	seen    map[nodeKey]bool // any traffic named this node
	birthed map[nodeKey]bool // an NBIRTH arrived
	asked   map[nodeKey]bool // NCMD Rebirth already sent
	// lastNew is when discovery last learned something: a node it had not
	// seen, or a birth. Ordinary NDATA does not move it — see Quiet.
	lastNew time.Time
}

// onMessage records one inbound message. Topic parsing is parseTopic — the
// driver's own — so discovery and the runtime agree on what is Sparkplug.
func (d *discoverer) onMessage(topic string, payload []byte) {
	group, kind, edge, device, ok := parseTopic(topic)
	if !ok {
		return
	}
	nk := nodeKey{Group: group, EdgeNode: edge}

	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.seen[nk] {
		d.seen[nk] = true
		d.lastNew = time.Now()
		d.log.Info("sparkplug import: node seen", "group", group, "node", edge, "via", kind)
	}
	switch kind {
	case "NBIRTH", "DBIRTH":
	default:
		return
	}
	p, err := sparkplug.DecodePayload(payload)
	if err != nil {
		d.log.Warn("sparkplug import: undecodable birth",
			"topic", topic, "bytes", len(payload), "error", err)
		return
	}
	d.lastNew = time.Now()
	if kind == "NBIRTH" {
		d.birthed[nk] = true
		// A rebirth supersedes an earlier NBIRTH for the same node: keep the
		// newest, and drop the device births that belonged to the old one.
		d.out = dropNode(d.out, nk)
		d.log.Info("sparkplug import: node birth",
			"group", group, "node", edge, "metrics", len(p.Metrics))
	} else {
		d.log.Info("sparkplug import: device birth",
			"group", group, "node", edge, "device", device, "metrics", len(p.Metrics))
	}
	d.out = append(d.out, Birth{Group: group, EdgeNode: edge, Device: device, Payload: p})
}

// dropNode removes every birth belonging to one edge node — what a fresh
// NBIRTH means: forget the previous session entirely.
func dropNode(births []Birth, nk nodeKey) []Birth {
	out := births[:0]
	for _, b := range births {
		if b.Group == nk.Group && b.EdgeNode == nk.EdgeNode {
			continue
		}
		out = append(out, b)
	}
	return out
}

// unbirthed lists nodes heard from that have not birthed and have not yet
// been asked to.
func (d *discoverer) unbirthed() []nodeKey {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []nodeKey
	for nk := range d.seen {
		if !d.birthed[nk] && !d.asked[nk] {
			out = append(out, nk)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Group != out[j].Group {
			return out[i].Group < out[j].Group
		}
		return out[i].EdgeNode < out[j].EdgeNode
	})
	return out
}

// requestRebirth publishes one NCMD Node Control/Rebirth, at most once per
// node per discovery: an importer must not storm a production broker.
func (d *discoverer) requestRebirth(cli mqtt.Client, nk nodeKey) {
	d.mu.Lock()
	if d.asked[nk] {
		d.mu.Unlock()
		return
	}
	d.asked[nk] = true
	d.mu.Unlock()

	ts := uint64(time.Now().UnixMilli())
	body, err := sparkplug.Payload{Timestamp: ts, OmitSeq: true, Metrics: []sparkplug.Metric{{
		Name:      RebirthMetric,
		Datatype:  spb.DataType_Boolean,
		Timestamp: ts,
		Value:     true,
	}}}.Encode()
	if err != nil {
		d.log.Warn("sparkplug import: encode rebirth", "error", err)
		return
	}
	topic := namespace + "/" + nk.Group + "/NCMD/" + nk.EdgeNode
	tok := cli.Publish(topic, 0, false, body)
	if !tok.WaitTimeout(tokenTimeout) || tok.Error() != nil {
		d.log.Warn("sparkplug import: rebirth request failed",
			"group", nk.Group, "node", nk.EdgeNode, "error", tok.Error())
		return
	}
	d.log.Info("sparkplug import: requested rebirth", "group", nk.Group, "node", nk.EdgeNode)
}

// settled reports whether every node seen has birthed and nothing new has
// arrived for at least q — the "we have everything" early exit.
func (d *discoverer) settled(q time.Duration) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.seen) == 0 || len(d.out) == 0 {
		return false
	}
	for nk := range d.seen {
		if !d.birthed[nk] {
			return false
		}
	}
	return time.Since(d.lastNew) >= q
}

// births returns the collected certificates in a deterministic order: group,
// edge node, then device with the node birth first.
func (d *discoverer) births() []Birth {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := append([]Birth(nil), d.out...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Group != b.Group {
			return a.Group < b.Group
		}
		if a.EdgeNode != b.EdgeNode {
			return a.EdgeNode < b.EdgeNode
		}
		return a.Device < b.Device
	})
	return out
}
