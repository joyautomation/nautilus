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
	"github.com/joyautomation/nautilus/lang/ir"
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
	// A reconnect deliberately does NOT re-arm the baseline. d.lastOut is
	// session-independent — it tracks the runtime's output tags, not the
	// broker — so it is already the baseline the moment we come back and
	// there is nothing to replay. Re-arming here would be strictly worse: it
	// would swallow an operator command that happened to land in the same
	// scan as the reconnect.
	//
	// Anything genuinely asked for while we were down is in d.pending and can
	// go now.
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

// publishRebirth sends NCMD Node Control/Rebirth to one edge node.
//
// Unlike publishCommand (gated on mayPublish — Primary && isLeader), a
// Rebirth request bypasses the Primary check: it does not contend for the
// STATE certificate and it commands no field device, so it does not "write
// to the group" in the sense mayPublish's doc comment means. It is gated on
// isLeader alone, so a standby replica under redundancy: still doesn't
// duplicate the request.
//
// This matters for host-as-edge (docs/design/sparkplug-host.md §8.8): a
// project consuming a group with primary: false is a passive consumer with
// no STATE and no outbound writes — but NBIRTH is never retained (spec), so
// without SOME way to ask, a passive consumer that starts (or reconnects)
// after the edge node's last birth sees nothing, ever, unless another host
// happens to trigger a fresh birth on that group. rebirth-on-start: true
// (the default) is documented as "the fix" for exactly this — silently
// failing it whenever primary: false breaks that promise. Confirmed by a
// host-as-edge bench (twin/ + site/ + host/, sparkplug-host branch): a
// site's `driver: {type: sparkplug-host, primary: false, ...}` consuming
// the twin's group never saw TWIN__Online go true until this fix.
//
// It does NOT bump Status.Rebirths: the state machine counts its own
// requests when it raises them (askRebirthLocked), so only the paths that
// originate here — RequestRebirth, the connect-time sweep, a __Rebirth
// rising edge — call countRebirth.
func (d *Driver) publishRebirth(group, edge string) error {
	p := d.publisher()
	if p == nil {
		err := fmt.Errorf("host: not connected")
		d.log.Warn("host: rebirth request failed", "group", group, "node", edge, "error", err)
		return err
	}
	if !d.isLeader() {
		err := fmt.Errorf("host: not the leader")
		d.log.Warn("host: rebirth request failed", "group", group, "node", edge, "error", err)
		return err
	}
	body, err := sparkplug.Payload{
		Timestamp: uint64(time.Now().UnixMilli()),
		OmitSeq:   true,
		Metrics: []sparkplug.Metric{{
			Name:      RebirthMetric,
			Datatype:  spb.DataType_Boolean,
			Timestamp: uint64(time.Now().UnixMilli()),
			Value:     true,
		}},
	}.Encode()
	if err != nil {
		err = fmt.Errorf("host: encode rebirth for %s: %w", cmdTopic(group, edge, ""), err)
		d.log.Warn("host: rebirth request failed", "group", group, "node", edge, "error", err)
		return err
	}
	if err := p.publish(cmdTopic(group, edge, ""), 0, false, body); err != nil {
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
// destination.
//
// A command to a DARK SITE IS DELIVERED WHEN IT COMES BACK, ONCE, unless the
// site already reports that value: the write is parked in d.queued (latest
// value per tag, at most maxQueuedPerNode per node) and released by
// releaseQueued on the node's next birth, after adoption has settled what the
// site actually holds. Only a command nothing can ever address — a node with
// no group, or a node whose queue is full — is dropped and counted.
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
	// at indexes the metrics already accumulated for a topic by metric name,
	// so two member writes to the same UDT in one coalesce window merge into
	// ONE partial template rather than two metrics sharing a name.
	at := map[string]map[string]int{}
	drops := uint64(0)
	// hold gathers the writes whose destination node is dark, per node, so
	// the whole batch parks under one wmu acquisition below.
	hold := map[string]map[string]any{}

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
		group, ok := d.groupFor(b.Node)
		if !ok {
			// No group resolves for this node, so no topic addresses it —
			// a manifest problem, not a dark site. Nothing to wait for.
			drops++
			d.log.Warn("host: dropping write to an unaddressable node",
				"tag", name, "node", b.Node)
			continue
		}
		if !d.nodeOnline(b.Node) {
			if hold[b.Node] == nil {
				hold[b.Node] = map[string]any{}
			}
			hold[b.Node][name] = v
			continue
		}
		iv, ok := irValueOf(v)
		if !ok {
			d.log.Warn("host: unwritable value type", "tag", name, "value", fmt.Sprintf("%T", v))
			continue
		}
		topic := cmdTopic(group, b.Node, b.Device)
		if at[topic] == nil {
			at[topic] = map[string]int{}
		}

		// A member binding goes out as a PARTIAL template: the parent
		// metric's name, carrying only the leaf this tag addresses. The edge
		// merges it member by member, so the site's other members — the ones
		// its own logic is driving — are untouched.
		if mo, isMember := d.members[name]; isMember {
			leaf, err := sparkplug.MetricFromValue(mo.path[len(mo.path)-1],
				ir.CoerceValue(iv, mo.leaf), "")
			if err != nil {
				d.log.Warn("host: encode member write failed",
					"tag", name, "metric", b.Metric, "member", b.Member, "error", err)
				continue
			}
			if i, merged := at[topic][b.Metric]; merged {
				if t, ok := byTopic[topic][i].Value.(*sparkplug.Template); ok && t != nil {
					setTemplateMember(t, mo, leaf)
					names[topic] = append(names[topic], name)
					continue
				}
			}
			root := &sparkplug.Template{TemplateRef: mo.refs[0]}
			setTemplateMember(root, mo, leaf)
			at[topic][b.Metric] = len(byTopic[topic])
			byTopic[topic] = append(byTopic[topic], sparkplug.Metric{
				Name:      b.Metric,
				Datatype:  spb.DataType_Template,
				Timestamp: uint64(time.Now().UnixMilli()),
				Value:     root,
			})
			names[topic] = append(names[topic], name)
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
		at[topic][b.Metric] = len(byTopic[topic])
		byTopic[topic] = append(byTopic[topic], m)
		names[topic] = append(names[topic], name)
	}

	if len(hold) > 0 {
		drops += d.queueOffline(hold)
		// A node that birthed while this flush was in flight found nothing
		// to release — it parked here, after the birth had passed. Close
		// that race rather than making the operator wait for the next
		// message from the site.
		for node := range hold {
			d.releaseQueued(node)
		}
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

// setTemplateMember inserts a leaf metric at mo's member path inside a
// template tree, creating the intermediate partial templates the path needs
// and reusing any that are already there. Each level carries its own
// TemplateRef, which is what makes the result a well-formed Sparkplug
// template instance rather than an anonymous bag of metrics.
func setTemplateMember(root *sparkplug.Template, mo memberOut, leaf sparkplug.Metric) {
	cur := root
	for i := 0; i < len(mo.path)-1; i++ {
		seg := mo.path[i]
		var next *sparkplug.Template
		for j := range cur.Metrics {
			if cur.Metrics[j].Name != seg {
				continue
			}
			if t, ok := cur.Metrics[j].Value.(*sparkplug.Template); ok && t != nil {
				next = t
			}
			break
		}
		if next == nil {
			next = &sparkplug.Template{TemplateRef: mo.refs[i+1]}
			cur.Metrics = append(cur.Metrics, sparkplug.Metric{
				Name:     seg,
				Datatype: spb.DataType_Template,
				Value:    next,
			})
		}
		cur = next
	}
	for j := range cur.Metrics {
		if cur.Metrics[j].Name == leaf.Name {
			cur.Metrics[j] = leaf
			return
		}
	}
	cur.Metrics = append(cur.Metrics, leaf)
}

// markWritten records that a value reached the node: it is both the last
// thing the runtime handed us for this tag and what we now believe the site
// holds, so neither a re-scan nor a re-write of the same value commands
// again.
func (d *Driver) markWritten(name string, v any) {
	d.wmu.Lock()
	d.initWriteMapsLocked()
	d.lastOut[name] = v
	d.remote[name] = v
	d.wmu.Unlock()
}

// maxQueuedPerNode bounds the dark-site queue. It is one value per TAG — a
// second write to the same setpoint replaces the first — so 256 is "every
// writable output a site plausibly has", not a burst budget. Past it the
// write is dropped and counted: a node with more than 256 distinct commands
// outstanding is a runaway program, not an operator.
const maxQueuedPerNode = 256

// queueOffline parks the writes whose destination node is dark, one wmu
// acquisition for the whole batch, and reports how many had to be dropped
// because the node's queue is full. Only the LATEST value per tag survives:
// an operator who moves a setpoint twice while the site is down commanded it
// once, to the second value.
func (d *Driver) queueOffline(hold map[string]map[string]any) uint64 {
	var dropped uint64
	var full []string
	var parked uint64

	d.wmu.Lock()
	if d.queued == nil {
		d.queued = map[string]map[string]any{}
	}
	for node, tags := range hold {
		q := d.queued[node]
		if q == nil {
			q = map[string]any{}
			d.queued[node] = q
		}
		for name, v := range tags {
			if _, replacing := q[name]; !replacing && len(q) >= maxQueuedPerNode {
				dropped++
				full = append(full, name)
				continue
			}
			q[name] = v
			parked++
		}
	}
	d.wmu.Unlock()

	if parked > 0 {
		d.mu.Lock()
		d.stats.WriteQueued += parked
		d.mu.Unlock()
		d.log.Debug("host: queued writes for a dark node", "writes", parked, "nodes", len(hold))
	}
	for _, name := range full {
		d.log.Warn("host: dropping write; the node's queue is full",
			"tag", name, "limit", maxQueuedPerNode)
	}
	return dropped
}

// releaseQueued delivers the commands parked for one edge node, now that it
// has birthed. It is called from the inbound path with NEITHER lock held —
// the same discipline flushAdopts documents, and for the same reason: wmu and
// d.mu are never nested in this package.
//
// It must run AFTER adoption (flushAdopts), because adoption is what settles
// the question the queue cannot answer for itself: the operator asked for a
// value while the site was dark, and the site has now told us what it holds.
// If they agree the command has already happened — drop it silently. If they
// differ it goes out, once. A queued write whose tag has been moved again
// since is superseded by the newer value already in pending.
func (d *Driver) releaseQueued(edge string) {
	d.wmu.Lock()
	waiting := len(d.queued[edge])
	d.wmu.Unlock()
	if waiting == 0 || !d.nodeOnline(edge) {
		return
	}

	d.wmu.Lock()
	d.initWriteMapsLocked()
	q := d.queued[edge]
	delete(d.queued, edge)
	sent, settled := 0, 0
	for name, v := range q {
		if _, newer := d.pending[name]; newer {
			continue // a later scan already queued this tag; that value wins
		}
		if prev, ok := d.remote[name]; ok && sameValue(prev, v) {
			settled++ // the site came back already holding it
			continue
		}
		d.pending[name] = v
		sent++
	}
	d.wmu.Unlock()

	if sent > 0 {
		d.log.Info("host: delivering commands queued while the node was dark",
			"node", edge, "commands", sent, "alreadyHeld", settled)
		d.kickWriter()
	}
}

// queuedDepths is how many commands are parked per edge node, for Status. It
// takes wmu alone — Status gathers it before touching d.mu, so the two locks
// stay unnested.
func (d *Driver) queuedDepths() map[string]int {
	d.wmu.Lock()
	defer d.wmu.Unlock()
	if len(d.queued) == 0 {
		return nil
	}
	out := make(map[string]int, len(d.queued))
	for node, q := range d.queued {
		if len(q) > 0 {
			out[node] = len(q)
		}
	}
	return out
}

// flushAdopts applies the member-output baselines the state machine queued
// while it held d.mu: the live value of a member IS what the site holds, so a
// later operator write of that same value is a no-op and a write of anything
// else goes out once. It must be called with NEITHER lock held: wmu and d.mu
// are never nested anywhere in this package, and this is the one path that
// crosses from the state machine's lock into the writer's, so it drains under
// d.mu, releases, and only then takes wmu.
//
// A tag with a command already queued is skipped: the operator's intent is
// newer than the birth that raced it.
func (d *Driver) flushAdopts() {
	d.mu.Lock()
	q := d.adoptQ
	d.adoptQ = nil
	d.mu.Unlock()
	if len(q) == 0 {
		return
	}
	d.wmu.Lock()
	d.initWriteMapsLocked()
	for _, a := range q {
		if _, queued := d.pending[a.name]; queued {
			continue
		}
		d.remote[a.name] = a.value
	}
	d.wmu.Unlock()
}

// nodeOnline reports whether an edge node has birthed and not died. An unknown
// node — one we have never heard from — is offline, which is exactly the case
// the dark-site queue exists for: a host that starts before its fleet can
// still be commanded, and the commands land as the sites birth.
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
