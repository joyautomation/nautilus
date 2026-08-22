// mqtt.go is the host application's transport: the connect/reconnect loop,
// the subscription set, the STATE birth/death certificate and its matching
// will, and the outbound NCMD/DCMD writer.
//
// It deliberately DIVERGES from sparkplug/node.go's paho options block in
// three places (docs/design/sparkplug-host.md §2, §8.2):
//
//   - SetCleanSession(true) and SetOrderMatters(true) — seq tracking depends
//     on arrival order, so paho must not fan messages out concurrently.
//   - SetBinaryWill on spBv1.0/STATE/<HostID> at QoS 1 retain TRUE (the edge's
//     NDEATH is retain false; the host's will is not) carrying
//     {"online":false,"timestamp":ts}.
//   - SetAutoReconnect(FALSE) plus our own reconnect loop. Paho bakes the will
//     into CONNECT and reuses it across auto-reconnects, so a long-lived
//     client's will timestamp goes stale and diverges from a fresh birth. We
//     rebuild the client — and the will timestamp — per attempt, and the STATE
//     birth reuses that same ts byte-identically (TCK
//     host-topic-phid-birth-payload-timestamp compares ==).
//
// OWNED BY B3.

package host

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/joyautomation/nautilus/sparkplug"
	"github.com/joyautomation/nautilus/sparkplug/spb"
)

const (
	connectTimeout = 30 * time.Second
	tokenTimeout   = 10 * time.Second
	// rebirthStagger spaces the connect-time rebirth requests so 60 sites
	// don't all birth into the broker at once (design §8.6).
	rebirthStagger = 50 * time.Millisecond
	backoffMin     = time.Second
	backoffMax     = 30 * time.Second
	// leaderPoll is how often a live session re-checks Config.Leader. The will
	// is baked into CONNECT, so a leadership change can only be honoured by
	// reconnecting.
	leaderPoll = time.Second
)

// stateBody is the Sparkplug 3.0 STATE birth/death certificate. Field order
// matters only in that the will and the birth must marshal to identical
// bytes; both go through this struct, so they do.
type stateBody struct {
	Online    bool  `json:"online"`
	Timestamp int64 `json:"timestamp"`
}

// stateTopic is the Sparkplug 3.0 STATE topic — subscribed and published
// LITERALLY, never as a wildcard: the TCK's payloads-state-subscribe and
// message-flow-phid-sparkplug-subscription compare the SUBSCRIBE filter by
// exact string, so spBv1.0/STATE/# fails them.
func stateTopic(hostID string) string { return "spBv1.0/STATE/" + hostID }

// legacyStateTopic is the Sparkplug 2.x form, retained "ONLINE"/"OFFLINE" with
// no spBv1.0 prefix. nautilus's own edge subscribes to both (primaryhost.go).
func legacyStateTopic(hostID string) string { return "STATE/" + hostID }

// ── the publisher seam ───────────────────────────────────────────────────

// pahoPub is the paho-backed publisher. It is the only place in the package
// that touches an MQTT client; everything above it speaks topics and bytes.
type pahoPub struct{ cli mqtt.Client }

func (p *pahoPub) publish(topic string, qos byte, retain bool, payload []byte) error {
	tok := p.cli.Publish(topic, qos, retain, payload)
	if !tok.WaitTimeout(tokenTimeout) {
		return fmt.Errorf("host: publish %s timed out", topic)
	}
	return tok.Error()
}

// publisher returns the live publisher, or nil while disconnected.
func (d *Driver) publisher() publisher {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.pub
}

func (d *Driver) setErr(err error) {
	d.mu.Lock()
	d.lastErr = err
	d.mu.Unlock()
}

// isLeader reports whether this replica may publish. nil Leader means "always"
// — a single-instance project is always its own leader.
func (d *Driver) isLeader() bool {
	if d.cfg.Leader == nil {
		return true
	}
	return d.cfg.Leader()
}

// mayPublish gates the STATE certificate and every outbound NCMD/DCMD. A
// passive consumer (Primary false) never writes to the group at all, and a
// standby replica under `redundancy:` never publishes STATE online — its LWT
// would otherwise flap store-and-forward across every site (design §8.1).
func (d *Driver) mayPublish() bool { return d.cfg.Primary && d.isLeader() }

// ── connect / reconnect loop ─────────────────────────────────────────────

// run owns the connection: connect → subscribe → STATE birth → rebirths →
// consume, reconnecting with capped backoff. It also owns the outbound writer
// and the stale sweeper, so a single <-d.done in Stop waits for everything.
func (d *Driver) run(ctx context.Context) {
	defer close(d.done)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); d.writeLoop(ctx) }()
	if d.cfg.StaleAfter > 0 {
		wg.Add(1)
		go func() { defer wg.Done(); d.staleLoop(ctx) }()
	}
	defer wg.Wait()

	backoff := backoffMin
	for ctx.Err() == nil {
		cli, lost, err := d.connect(ctx)
		if err != nil {
			d.setErr(err)
			d.log.Warn("host: connect failed", "broker", d.cfg.BrokerURL, "error", err, "retryIn", backoff)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff < backoffMax {
				backoff *= 2
				if backoff > backoffMax {
					backoff = backoffMax
				}
			}
			continue
		}
		backoff = backoffMin

		d.serve(ctx, lost)

		d.mu.Lock()
		d.connected = false
		d.pub = nil
		d.mu.Unlock()
		// Death-before-DISCONNECT: Stop publishes the certificate and only
		// then cancels, so by the time we get here the wire is already clear.
		cli.Disconnect(250)
	}
}

// serve holds a live session open until the connection drops, the context
// ends, or leadership changes (which needs a new CONNECT to install — or
// remove — the will).
func (d *Driver) serve(ctx context.Context, lost <-chan struct{}) {
	wasLeader := d.mayPublish()
	t := time.NewTicker(leaderPoll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-lost:
			d.log.Warn("host: connection lost", "broker", d.cfg.BrokerURL)
			return
		case <-t.C:
			if d.mayPublish() != wasLeader {
				d.log.Info("host: leadership changed; reconnecting to refresh STATE",
					"leader", !wasLeader)
				return
			}
		}
	}
}

// connect builds a fresh client (fresh will, fresh timestamp), connects,
// subscribes, publishes the STATE birth and kicks off the connect-time
// rebirth requests. The order is mandatory: every SUBSCRIBE precedes the first
// PUBLISH.
func (d *Driver) connect(ctx context.Context) (mqtt.Client, <-chan struct{}, error) {
	// One timestamp per session, captured before CONNECT and shared verbatim
	// by the will and the birth.
	ts := time.Now().UnixMilli()
	will, err := json.Marshal(stateBody{Online: false, Timestamp: ts})
	if err != nil {
		return nil, nil, fmt.Errorf("host: encode will: %w", err)
	}

	lost := make(chan struct{})
	var lostOnce sync.Once

	opts := mqtt.NewClientOptions().
		AddBroker(d.cfg.BrokerURL).
		SetClientID(d.cfg.ClientID).
		SetKeepAlive(d.cfg.Keepalive).
		SetCleanSession(true).
		SetAutoReconnect(false).
		SetConnectTimeout(connectTimeout).
		SetOrderMatters(true).
		SetDefaultPublishHandler(func(_ mqtt.Client, m mqtt.Message) {
			d.handleMessage(m.Topic(), m.Payload())
		}).
		SetConnectionLostHandler(func(_ mqtt.Client, e error) {
			d.setErr(e)
			lostOnce.Do(func() { close(lost) })
		})
	if d.mayPublish() {
		opts.SetBinaryWill(stateTopic(d.cfg.HostID), will, 1, true)
	}
	if d.cfg.Username != "" {
		opts.SetUsername(d.cfg.Username).SetPassword(d.cfg.Password)
	}

	cli := mqtt.NewClient(opts)
	tok := cli.Connect()
	if !tok.WaitTimeout(connectTimeout) {
		return nil, nil, fmt.Errorf("host: connect %s timed out", d.cfg.BrokerURL)
	}
	if err := tok.Error(); err != nil {
		return nil, nil, fmt.Errorf("host: connect %s: %w", d.cfg.BrokerURL, err)
	}

	p := &pahoPub{cli: cli}
	if err := d.subscribe(cli); err != nil {
		cli.Disconnect(0)
		return nil, nil, err
	}
	if err := d.publishStateBirth(p, ts); err != nil {
		cli.Disconnect(0)
		return nil, nil, err
	}

	d.mu.Lock()
	d.pub = p
	d.connected = true
	d.lastErr = nil
	d.sessionTS = ts
	d.mu.Unlock()

	d.log.Info("host: connected", "broker", d.cfg.BrokerURL, "hostID", d.cfg.HostID,
		"groups", d.groups, "nodes", len(d.manifest.Nodes))

	// Retained births are forbidden by spec, so a host starting mid-stream
	// sees nothing until it asks (design §8.6).
	if d.cfg.RebirthOnStart {
		go d.rebirthAll(ctx)
	}
	// Anything queued while we were down can go now.
	d.kickWriter()
	return cli, lost, nil
}

// subscribe issues the whole subscription set, literal STATE first. Six
// filters per group, preferred over spBv1.0/<group>/#, which would echo our
// own NCMD/DCMD back and pull in other hosts' retained STATE.
func (d *Driver) subscribe(cli mqtt.Client) error {
	filters := []string{stateTopic(d.cfg.HostID)}
	for _, g := range d.groups {
		filters = append(filters,
			"spBv1.0/"+g+"/NBIRTH/+",
			"spBv1.0/"+g+"/NDEATH/+",
			"spBv1.0/"+g+"/NDATA/+",
			"spBv1.0/"+g+"/DBIRTH/+/+",
			"spBv1.0/"+g+"/DDEATH/+/+",
			"spBv1.0/"+g+"/DDATA/+/+",
		)
	}
	for _, f := range filters {
		tok := cli.Subscribe(f, 1, nil)
		if !tok.WaitTimeout(tokenTimeout) {
			return fmt.Errorf("host: subscribe %s timed out", f)
		}
		if err := tok.Error(); err != nil {
			return fmt.Errorf("host: subscribe %s: %w", f, err)
		}
	}
	return nil
}

// ── STATE certificate ────────────────────────────────────────────────────

// publishStateBirth publishes {"online":true,"timestamp":ts} retained at
// QoS 1. It must be the first PUBLISH after CONNECT, and ts must be the
// byte-identical value the will carries.
func (d *Driver) publishStateBirth(p publisher, ts int64) error {
	if !d.mayPublish() {
		return nil
	}
	body, err := json.Marshal(stateBody{Online: true, Timestamp: ts})
	if err != nil {
		return fmt.Errorf("host: encode STATE birth: %w", err)
	}
	if err := p.publish(stateTopic(d.cfg.HostID), 1, true, body); err != nil {
		return fmt.Errorf("host: publish STATE birth: %w", err)
	}
	if d.cfg.StateForm == StateForm2x || d.cfg.StateForm == StateFormBoth {
		if err := p.publish(legacyStateTopic(d.cfg.HostID), 1, true, []byte("ONLINE")); err != nil {
			return fmt.Errorf("host: publish legacy STATE birth: %w", err)
		}
	}
	return nil
}

// publishStateDeath publishes the death certificate and waits for the token.
// Stop calls it *before* tearing the connection down: the TCK requires the
// host's own death publish ahead of both clean and unclean teardown, and the
// broker firing the LWT is explicitly not a substitute.
func (d *Driver) publishStateDeath() {
	p := d.publisher()
	if p == nil || !d.mayPublish() {
		return
	}
	body, err := json.Marshal(stateBody{Online: false, Timestamp: time.Now().UnixMilli()})
	if err != nil {
		d.log.Error("host: encode STATE death", "error", err)
		return
	}
	if err := p.publish(stateTopic(d.cfg.HostID), 1, true, body); err != nil {
		d.log.Error("host: publish STATE death", "error", err)
	}
	if d.cfg.StateForm == StateForm2x || d.cfg.StateForm == StateFormBoth {
		if err := p.publish(legacyStateTopic(d.cfg.HostID), 1, true, []byte("OFFLINE")); err != nil {
			d.log.Error("host: publish legacy STATE death", "error", err)
		}
	}
}

// ── outbound commands ────────────────────────────────────────────────────

// cmdTopic is the destination for one binding: node-level metrics go out as
// NCMD, device metrics as DCMD.
func cmdTopic(group, edge, device string) string {
	if device == "" {
		return "spBv1.0/" + group + "/NCMD/" + edge
	}
	return "spBv1.0/" + group + "/DCMD/" + edge + "/" + device
}

// publishCommand encodes and sends one coalesced command payload. QoS 0,
// retain false, OmitSeq — a command carries no sequence number.
func (d *Driver) publishCommand(topic string, metrics []sparkplug.Metric) error {
	p := d.publisher()
	if p == nil {
		return fmt.Errorf("host: not connected")
	}
	if !d.mayPublish() {
		return fmt.Errorf("host: not the publishing host (primary=%v, leader=%v)",
			d.cfg.Primary, d.isLeader())
	}
	body, err := sparkplug.Payload{
		Timestamp: uint64(time.Now().UnixMilli()),
		OmitSeq:   true,
		Metrics:   metrics,
	}.Encode()
	if err != nil {
		return fmt.Errorf("host: encode command for %s: %w", topic, err)
	}
	return p.publish(topic, 0, false, body)
}

// publishRebirth sends NCMD Node Control/Rebirth to one edge node. It does
// NOT bump Status.Rebirths: the state machine counts its own requests when it
// raises them (askRebirthLocked), so only the paths that originate here —
// RequestRebirth, the connect-time sweep, a __Rebirth rising edge — call
// countRebirth.
func (d *Driver) publishRebirth(group, edge string) error {
	err := d.publishCommand(cmdTopic(group, edge, ""), []sparkplug.Metric{{
		Name:      RebirthMetric,
		Datatype:  spb.DataType_Boolean,
		Timestamp: uint64(time.Now().UnixMilli()),
		Value:     true,
	}})
	if err != nil {
		d.log.Warn("host: rebirth request failed", "group", group, "node", edge, "error", err)
		return err
	}
	d.log.Info("host: requested rebirth", "group", group, "node", edge)
	return nil
}

func (d *Driver) countRebirth() {
	d.mu.Lock()
	d.stats.Rebirths++
	d.mu.Unlock()
}

// rebirthAll asks every manifest node to rebirth, staggered so 60 sites don't
// birth simultaneously.
func (d *Driver) rebirthAll(ctx context.Context) {
	for _, n := range d.manifest.Nodes {
		group, ok := d.groupFor(n.EdgeNode)
		if !ok {
			continue
		}
		if err := d.publishRebirth(group, n.EdgeNode); err != nil {
			return // connection is gone; the next connect will retry
		}
		d.countRebirth()
		select {
		case <-ctx.Done():
			return
		case <-time.After(rebirthStagger):
		}
	}
}

// writeLoop drains the rebirth queue immediately and flushes queued writes one
// CommandInterval after the first change — the coalesce window, so a burst of
// operator writes to one site becomes one payload.
func (d *Driver) writeLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case k := <-d.rebirths:
			_ = d.publishRebirth(k.Group, k.EdgeNode)
		case <-d.wkick:
			select {
			case <-ctx.Done():
				return
			case <-time.After(d.cfg.CommandInterval):
			}
			select { // collapse any kicks that arrived inside the window
			case <-d.wkick:
			default:
			}
			d.flushWrites()
		}
	}
}

// staleLoop marks nodes silent for longer than StaleAfter.
func (d *Driver) staleLoop(ctx context.Context) {
	every := d.cfg.StaleAfter / 4
	if every < time.Second {
		every = time.Second
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			d.sweepStale(now)
		}
	}
}

// flushWrites turns the queued tag values into NCMD/DCMD payloads, one per
// destination. Writes to an offline node are dropped and counted, never
// queued — a command to a dark site is meaningless and the operator is already
// looking at __Online=false (design §4).
func (d *Driver) flushWrites() {
	if d.publisher() == nil || !d.mayPublish() {
		return // stay queued; connect() kicks us again
	}
	d.wmu.Lock()
	if len(d.pending) == 0 {
		d.wmu.Unlock()
		return
	}
	work := d.pending
	d.pending = map[string]any{}
	d.wmu.Unlock()

	byTopic := map[string][]sparkplug.Metric{}
	names := map[string][]string{}
	drops := uint64(0)

	for name, v := range work {
		// Synthesized <site>__Rebirth: a rising edge is an operator-forced
		// rebirth. Change suppression upstream means only transitions get
		// here, so "value is true" *is* the rising edge.
		if edge, ok := d.rebirthTags[name]; ok {
			if truthy(v) {
				if group, ok := d.groupFor(edge); ok {
					if err := d.publishRebirth(group, edge); err == nil {
						d.countRebirth()
					}
				}
			}
			d.markWritten(name, v)
			continue
		}
		b, ok := d.byName[name]
		if !ok {
			continue
		}
		if !d.nodeOnline(b.Node) {
			drops++
			d.log.Debug("host: dropping write to offline node", "tag", name, "node", b.Node)
			continue
		}
		group, ok := d.groupFor(b.Node)
		if !ok {
			continue
		}
		iv, ok := irValueOf(v)
		if !ok {
			d.log.Warn("host: unwritable value type", "tag", name, "value", fmt.Sprintf("%T", v))
			continue
		}
		ref := ""
		if _, isType := d.defs[b.Type]; isType {
			ref = b.Type
		}
		m, err := sparkplug.MetricFromValue(b.Metric, iv, ref)
		if err != nil {
			d.log.Warn("host: encode write failed", "tag", name, "metric", b.Metric, "error", err)
			continue
		}
		m.Timestamp = uint64(time.Now().UnixMilli())
		topic := cmdTopic(group, b.Node, b.Device)
		byTopic[topic] = append(byTopic[topic], m)
		names[topic] = append(names[topic], name)
	}

	if drops > 0 {
		d.mu.Lock()
		d.stats.WriteDrops += drops
		d.mu.Unlock()
	}

	for topic, metrics := range byTopic {
		if err := d.publishCommand(topic, metrics); err != nil {
			d.setErr(err)
			d.log.Warn("host: command publish failed", "topic", topic, "error", err)
			// Requeue so the values go out after the next connect.
			d.wmu.Lock()
			for _, n := range names[topic] {
				if _, exists := d.pending[n]; !exists {
					d.pending[n] = work[n]
				}
			}
			d.wmu.Unlock()
			continue
		}
		for _, n := range names[topic] {
			d.markWritten(n, work[n])
		}
		d.log.Debug("host: command published", "topic", topic, "metrics", len(metrics))
	}
}

func (d *Driver) markWritten(name string, v any) {
	d.wmu.Lock()
	d.written[name] = v
	d.wmu.Unlock()
}

// nodeOnline reports whether an edge node has birthed and not died. An unknown
// node — one we have never heard from — is offline, which is exactly the case
// the write drop exists for.
func (d *Driver) nodeOnline(edge string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	for k, ns := range d.nodes {
		if k.EdgeNode == edge {
			return ns != nil && ns.online
		}
	}
	return false
}
