// state.go is the Sparkplug state machine: alias tables, NBIRTH/DBIRTH,
// sequence tracking and the reorder buffer, NDEATH/DDEATH, template assembly
// into ir.Values, and the snapshot ReadInputs serves. No MQTT types appear
// here — the only inbound seam is handleMessage(topic, payload).
//
// OWNED BY B2. Shared types live in types.go; add methods here rather than
// widening that file.

package host

import (
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/sparkplug"
	"github.com/joyautomation/nautilus/sparkplug/spb"
)

// namespace is the Sparkplug B topic namespace every message rides under.
const namespace = "spBv1.0"

// bdSeqMetric is the birth/death sequence metric NBIRTH and NDEATH carry. An
// NDEATH whose bdSeq does not match the node's current one is a late will
// from a prior session (spec §core, "Death Certificates").
const bdSeqMetric = "bdSeq"

// reorderFallback is used when Config.ReorderTimeout is unset, so a driver
// constructed by hand (tests, or a caller that skipped New's defaulting) still
// resolves sequence gaps instead of buffering forever.
const reorderFallback = 5 * time.Second

// maxPending caps the out-of-order buffer at the whole 8-bit sequence space.
// Beyond that the stream is not reorderable, only rebirthable.
const maxPending = 256

// ── the inbound seam ─────────────────────────────────────────────────────

// handleMessage routes one inbound Sparkplug message. topic is the full MQTT
// topic; payload is the raw wire bytes (protobuf for spBv1.0 message types,
// JSON or "ONLINE"/"OFFLINE" for STATE).
//
// Topic shapes:
//
//	spBv1.0/<group>/NBIRTH/<edge>            seq MUST be 0
//	spBv1.0/<group>/NDATA/<edge>             seq == (last+1)%256
//	spBv1.0/<group>/NDEATH/<edge>            bdSeq only, no seq
//	spBv1.0/<group>/DBIRTH/<edge>/<device>
//	spBv1.0/<group>/DDATA/<edge>/<device>
//	spBv1.0/<group>/DDEATH/<edge>/<device>
//	spBv1.0/STATE/<host_id>                  our own echo — ignored
//
// It is pure with respect to the network: it only mutates driver state under
// d.mu and may enqueue an outbound rebirth request. It must never block.
// Malformed topics, undecodable payloads, foreign groups and STATE echoes are
// dropped silently — a host consumes a shared broker and must not be knocked
// over by traffic it did not ask for.
func (d *Driver) handleMessage(topic string, payload []byte) {
	group, kind, edge, device, ok := parseTopic(topic)
	if !ok {
		return
	}
	if !d.groupAllowed(group) {
		return
	}
	p, err := sparkplug.DecodePayload(payload)
	if err != nil {
		d.stateLogger().Debug("sparkplug host: undecodable payload",
			"topic", topic, "bytes", len(payload), "error", err)
		return
	}

	d.mu.Lock()
	d.stats.Msgs++
	nk := nodeKey{Group: group, EdgeNode: edge}
	ns := d.nodeLocked(nk)
	ns.lastMs = time.Now().UnixMilli()
	ns.stale = false

	switch kind {
	case "NBIRTH":
		d.applyNBirthLocked(ns, p)
	case "NDEATH":
		d.applyNDeathLocked(ns, p)
	default:
		// Everything else consumes a sequence number.
		if !ns.seqPrimed {
			// Data before a birth: the host started mid-stream, or the gap
			// timer gave up. Retained births are forbidden by the spec, so
			// the only cure is to ask.
			d.askRebirthLocked(ns)
			break
		}
		if d.acceptSeqLocked(ns, kind, device, p) {
			d.applyOneLocked(ns, kind, device, p)
			d.drainPendingLocked(ns)
		}
	}
	d.mu.Unlock()
	d.flushRebirths()
}

// parseTopic splits a Sparkplug topic into its parts. ok is false for
// anything this driver does not consume: a foreign namespace, a STATE topic
// (ours echoes back because the spec requires the subscription), a
// node-message topic carrying a device segment or vice versa, or junk.
func parseTopic(topic string) (group, kind, edge, device string, ok bool) {
	parts := strings.Split(topic, "/")
	if len(parts) < 3 || parts[0] != namespace {
		return "", "", "", "", false
	}
	if parts[1] == "STATE" {
		return "", "", "", "", false // our own birth/death certificate
	}
	if len(parts) < 4 || len(parts) > 5 {
		return "", "", "", "", false
	}
	group, kind, edge = parts[1], parts[2], parts[3]
	if len(parts) == 5 {
		device = parts[4]
	}
	if group == "" || edge == "" {
		return "", "", "", "", false
	}
	switch kind {
	case "NBIRTH", "NDATA", "NDEATH":
		if device != "" {
			return "", "", "", "", false
		}
	case "DBIRTH", "DDATA", "DDEATH":
		if device == "" {
			return "", "", "", "", false
		}
	default:
		// NCMD/DCMD are ours going out; anything else is not Sparkplug.
		return "", "", "", "", false
	}
	return group, kind, edge, device, true
}

// groupAllowed reports whether messages from group are consumed. An empty
// Config.GroupIDs means "every group on the broker".
func (d *Driver) groupAllowed(group string) bool {
	if len(d.cfg.GroupIDs) == 0 {
		return true
	}
	for _, g := range d.cfg.GroupIDs {
		if g == group {
			return true
		}
	}
	return false
}

// nodeLocked returns the state for one edge node, creating it on first sight.
// Caller holds d.mu.
func (d *Driver) nodeLocked(nk nodeKey) *nodeState {
	if d.nodes == nil {
		d.nodes = map[nodeKey]*nodeState{}
	}
	ns, ok := d.nodes[nk]
	if !ok {
		ns = &nodeState{
			group:   nk.Group,
			edge:    nk.EdgeNode,
			aliases: map[uint64]metricRef{},
			devices: map[string]*deviceState{},
		}
		d.nodes[nk] = ns
	}
	return ns
}

// ── births ───────────────────────────────────────────────────────────────

// applyNBirthLocked resets an edge node to its birth state: sequence primed at
// the birth's seq (which the spec pins at 0), a fresh alias table, the
// template definitions the birth carried, every previously-known device back
// to offline until its own DBIRTH, and the birth's metric values applied.
// Caller holds d.mu.
func (d *Driver) applyNBirthLocked(ns *nodeState, p sparkplug.Payload) {
	if p.Seq != 0 {
		// Not fatal — accept it and resynchronise from what we were sent,
		// rather than deadlocking on a non-conformant edge.
		d.stateLogger().Warn("sparkplug host: NBIRTH seq is not 0",
			"group", ns.group, "node", ns.edge, "seq", p.Seq)
	}
	ns.stopGap()
	ns.pending = nil
	ns.rebirthAsked = false
	ns.online = true
	ns.seq = p.Seq
	ns.seqPrimed = true
	ns.birthMs = birthTimestamp(p)
	ns.aliases = map[uint64]metricRef{}

	// A node rebirth invalidates every device: the spec requires a DBIRTH
	// before any device data, so hold the rows but mark them dark.
	for _, dev := range ns.devices {
		dev.online = false
	}

	if bd, ok := bdSeqOf(p.Metrics); ok {
		ns.bdSeq = bd
	}
	d.harvestTemplatesLocked(ns, p.Metrics)
	d.registerAliasesLocked(ns, "", p.Metrics)
	ns.metrics = d.applyMetricsLocked(ns, "", p.Metrics)
	d.syncTagsLocked(ns)

	d.stateLogger().Info("sparkplug host: node birth",
		"group", ns.group, "node", ns.edge, "bdSeq", ns.bdSeq, "metrics", ns.metrics)
}

// applyDBirthLocked marks one device online and applies its birth metrics.
// Device aliases join the node's single table — the spec scopes aliases to the
// edge node including its devices. Caller holds d.mu.
func (d *Driver) applyDBirthLocked(ns *nodeState, device string, p sparkplug.Payload) {
	dev := ns.device(device)
	dev.online = true
	dev.birthMs = birthTimestamp(p)
	d.registerAliasesLocked(ns, device, p.Metrics)
	dev.metrics = d.applyMetricsLocked(ns, device, p.Metrics)
	d.syncTagsLocked(ns)
}

// harvestTemplatesLocked builds ir.StructDefs from the IsDefinition Templates
// an NBIRTH carries.
//
// Definition harvesting is best-effort by design. A nested-struct definition
// member is only interpretable when it carries a templateRef pointing at the
// nested type's own definition metric; an edge that emits it as a bare null
// Template instead (nautilus's own did until templates.go was fixed, and
// other stacks still do) makes StructDefsFromTemplates fail — correctly.
// The manifest is the authoritative source for those shapes anyway
// (targetType prefers d.defs), so a harvest failure is logged once and the
// birth still applies. Caller holds d.mu.
func (d *Driver) harvestTemplatesLocked(ns *nodeState, ms []sparkplug.Metric) {
	ns.tmpl = map[string]*sparkplug.Template{}
	for _, m := range ms {
		if t, ok := m.Value.(*sparkplug.Template); ok && t != nil && t.IsDefinition && m.Name != "" {
			ns.tmpl[m.Name] = t
		}
	}
	if len(ns.tmpl) == 0 {
		ns.defs = nil
		return
	}
	defs, err := sparkplug.StructDefsFromTemplates(ms)
	if err != nil {
		ns.defs = nil
		d.warnOnceLocked("tmpl:"+ns.group+"/"+ns.edge,
			"sparkplug host: NBIRTH template definitions unusable; falling back to the manifest types",
			"group", ns.group, "node", ns.edge, "error", err)
		return
	}
	ns.defs = defs
}

// registerAliasesLocked records every metric that carries both a name and an
// alias, so later NDATA/DDATA may reference the alias alone. nautilus's own
// edge sends full names and no aliases (birth.go); Ignition and tentacle
// alias, so both shapes must work. Caller holds d.mu.
func (d *Driver) registerAliasesLocked(ns *nodeState, device string, ms []sparkplug.Metric) {
	if ns.aliases == nil {
		ns.aliases = map[uint64]metricRef{}
	}
	for _, m := range ms {
		if m.Name == "" || m.Alias == 0 {
			continue
		}
		ns.aliases[m.Alias] = metricRef{Device: device, Metric: m.Name}
	}
}

// bdSeqOf pulls the bdSeq metric out of a birth or death payload.
func bdSeqOf(ms []sparkplug.Metric) (int64, bool) {
	for _, m := range ms {
		if m.Name != bdSeqMetric || m.IsNull {
			continue
		}
		switch v := m.Value.(type) {
		case int64:
			return v, true
		case float64:
			return int64(v), true
		}
	}
	return 0, false
}

// birthTimestamp prefers the payload's own timestamp — that is when the edge
// was born, which is what <site>__LastBirthMs means — and falls back to the
// host clock for a payload that omitted it.
func birthTimestamp(p sparkplug.Payload) int64 {
	if p.Timestamp != 0 {
		return int64(p.Timestamp)
	}
	return time.Now().UnixMilli()
}

// ── deaths ───────────────────────────────────────────────────────────────

// applyNDeathLocked takes an edge node and all its devices offline, provided
// the death's bdSeq matches the node's current one. Tag values are kept:
// Sparkplug's last value *is* the value, and quality rides on the synthesized
// __Online companion tags. Caller holds d.mu.
func (d *Driver) applyNDeathLocked(ns *nodeState, p sparkplug.Payload) {
	bd, ok := bdSeqOf(p.Metrics)
	if !ok {
		d.stateLogger().Warn("sparkplug host: NDEATH without bdSeq, ignored",
			"group", ns.group, "node", ns.edge)
		return
	}
	if !ns.seqPrimed && !ns.online {
		// Never saw this node's birth; nothing to kill, and no bdSeq to
		// match against. A will from before we connected.
		return
	}
	if bd != ns.bdSeq {
		d.stateLogger().Info("sparkplug host: stale NDEATH ignored",
			"group", ns.group, "node", ns.edge, "bdSeq", bd, "current", ns.bdSeq)
		return
	}
	ns.stopGap()
	ns.pending = nil
	ns.online = false
	ns.seqPrimed = false
	for _, dev := range ns.devices {
		dev.online = false
	}
	d.syncTagsLocked(ns)
	d.stateLogger().Info("sparkplug host: node death",
		"group", ns.group, "node", ns.edge, "bdSeq", bd)
}

// applyDDeathLocked takes one device offline, keeping its values.
// Caller holds d.mu.
func (d *Driver) applyDDeathLocked(ns *nodeState, device string) {
	dev := ns.device(device)
	dev.online = false
	d.syncTagsLocked(ns)
}

// device returns one device's state, creating it on first sight.
func (ns *nodeState) device(id string) *deviceState {
	if ns.devices == nil {
		ns.devices = map[string]*deviceState{}
	}
	dev, ok := ns.devices[id]
	if !ok {
		dev = &deviceState{id: id}
		ns.devices[id] = dev
	}
	return dev
}

// ── sequence tracking and the reorder buffer ─────────────────────────────

// acceptSeqLocked is the sequence gate for everything that consumes a seq
// (NDATA, DBIRTH, DDATA, DDEATH). It returns true when the message is the
// expected successor and should be applied now. An out-of-order message is
// buffered and starts the reorder timer; a duplicate is dropped.
// Caller holds d.mu.
func (d *Driver) acceptSeqLocked(ns *nodeState, kind, device string, p sparkplug.Payload) bool {
	want := (ns.seq + 1) % 256
	switch {
	case p.Seq == want:
		ns.seq = p.Seq
		return true
	case p.Seq == ns.seq:
		// A redelivery of the message we just took. QoS 1 permits it.
		return false
	}
	if ns.pending == nil {
		ns.pending = map[uint64]queued{}
	}
	if len(ns.pending) >= maxPending {
		return false
	}
	ns.pending[p.Seq] = queued{kind: kind, device: device, payload: p, at: time.Now()}
	d.startGapLocked(ns)
	return false
}

// drainPendingLocked applies buffered messages that have become the expected
// successor, in sequence order, and cancels the reorder timer once the buffer
// is empty. Caller holds d.mu.
func (d *Driver) drainPendingLocked(ns *nodeState) {
	for len(ns.pending) > 0 {
		next := (ns.seq + 1) % 256
		q, ok := ns.pending[next]
		if !ok {
			return // still a hole; leave the timer running
		}
		delete(ns.pending, next)
		ns.seq = next
		d.applyOneLocked(ns, q.kind, q.device, q.payload)
	}
	ns.stopGap()
}

// applyOneLocked dispatches one in-sequence message. Caller holds d.mu.
func (d *Driver) applyOneLocked(ns *nodeState, kind, device string, p sparkplug.Payload) {
	switch kind {
	case "DBIRTH":
		d.applyDBirthLocked(ns, device, p)
	case "NDATA":
		d.applyMetricsLocked(ns, "", p.Metrics)
	case "DDATA":
		d.applyMetricsLocked(ns, device, p.Metrics)
	case "DDEATH":
		d.applyDDeathLocked(ns, device)
	}
}

// startGapLocked arms the reorder timer if it is not already running.
// Caller holds d.mu.
func (d *Driver) startGapLocked(ns *nodeState) {
	if ns.gapTimer != nil {
		return
	}
	to := d.cfg.ReorderTimeout
	if to <= 0 {
		to = reorderFallback
	}
	nk := nodeKey{Group: ns.group, EdgeNode: ns.edge}
	gen := ns.gapGen
	ns.gapTimer = time.AfterFunc(to, func() { d.gapExpired(nk, gen) })
}

// stopGap cancels the reorder timer and invalidates any callback already in
// flight: Timer.Stop cannot report whether an AfterFunc has begun running, so
// the generation counter does it instead.
func (ns *nodeState) stopGap() {
	if ns.gapTimer != nil {
		ns.gapTimer.Stop()
		ns.gapTimer = nil
	}
	ns.gapGen++
}

// gapExpired runs when a sequence gap went unfilled for ReorderTimeout: the
// buffered tail is unusable, so drop it, unprime the sequence, and ask the
// node to birth again.
func (d *Driver) gapExpired(nk nodeKey, gen uint64) {
	d.mu.Lock()
	ns := d.nodes[nk]
	if ns == nil || ns.gapGen != gen {
		d.mu.Unlock()
		return // cancelled, or superseded by a newer gap
	}
	held := len(ns.pending)
	ns.gapTimer = nil
	ns.gapGen++
	ns.pending = nil
	ns.seqPrimed = false
	d.stats.SeqGaps++
	d.askRebirthLocked(ns)
	d.mu.Unlock()

	d.stateLogger().Warn("sparkplug host: sequence gap, requesting rebirth",
		"group", nk.Group, "node", nk.EdgeNode, "buffered", held)
	d.flushRebirths()
}

// ── rebirth requests ─────────────────────────────────────────────────────

// askRebirthLocked queues one NCMD Node Control/Rebirth for this node,
// debounced to one per birth cycle so a dark site cannot storm the broker.
// Caller holds d.mu; the hook runs later, in flushRebirths.
func (d *Driver) askRebirthLocked(ns *nodeState) {
	if ns.rebirthAsked {
		return
	}
	ns.rebirthAsked = true
	d.stats.Rebirths++
	d.rebirthQ = append(d.rebirthQ, nodeKey{Group: ns.group, EdgeNode: ns.edge})
}

// flushRebirths invokes the transport's rebirth hook for everything queued
// while the lock was held. Caller must NOT hold d.mu. With no transport
// attached (the unit-test case) the queue simply drains.
func (d *Driver) flushRebirths() {
	d.mu.Lock()
	q := d.rebirthQ
	d.rebirthQ = nil
	hook := d.onRebirthNeeded
	d.mu.Unlock()
	if hook == nil {
		return
	}
	for _, nk := range q {
		hook(nk.Group, nk.EdgeNode)
	}
}

// ── value application ────────────────────────────────────────────────────

// applyMetricsLocked writes one payload's metrics into d.values and returns
// how many carried data (protocol metrics and template definitions do not
// count). Caller holds d.mu.
func (d *Driver) applyMetricsLocked(ns *nodeState, device string, ms []sparkplug.Metric) int {
	n := 0
	for _, m := range ms {
		name, dev, ok := d.resolveMetricLocked(ns, device, m)
		if !ok {
			continue
		}
		if isProtocolMetric(name) || isDefinition(m) {
			continue
		}
		n++
		key := metricKey{EdgeNode: ns.edge, Device: dev, Metric: name}
		b, bound := d.byMetric[key]
		if !bound {
			d.recordUnknownLocked(ns.group, key, m)
			continue
		}
		d.applyBindingLocked(ns, b, m)
	}
	return n
}

// resolveMetricLocked turns a metric into (name, device): a named metric
// answers for itself and keeps the topic's device, while an alias-only metric
// resolves through the node's alias table, which also carries the device the
// alias was born under.
func (d *Driver) resolveMetricLocked(ns *nodeState, device string, m sparkplug.Metric) (string, string, bool) {
	if m.Name != "" {
		return m.Name, device, true
	}
	if m.Alias == 0 {
		return "", "", false
	}
	ref, ok := ns.aliases[m.Alias]
	if !ok {
		d.warnOnceLocked(fmt.Sprintf("alias:%s/%s/%d", ns.group, ns.edge, m.Alias),
			"sparkplug host: unknown metric alias; requesting rebirth",
			"group", ns.group, "node", ns.edge, "alias", m.Alias)
		d.askRebirthLocked(ns)
		return "", "", false
	}
	return ref.Metric, ref.Device, true
}

// isProtocolMetric reports whether a metric is Sparkplug plumbing rather than
// process data.
func isProtocolMetric(name string) bool {
	return name == bdSeqMetric || name == RebirthMetric ||
		strings.HasPrefix(name, "Node Control/") || strings.HasPrefix(name, "Device Control/")
}

func isDefinition(m sparkplug.Metric) bool {
	t, ok := m.Value.(*sparkplug.Template)
	return ok && t != nil && t.IsDefinition
}

// applyBindingLocked converts one metric to the binding's declared shape and
// stores it. Scalars cross the io.Driver seam as plain Go values and compound
// values as ir.Value, exactly as eip's ReadInputs hands them over.
// Caller holds d.mu.
func (d *Driver) applyBindingLocked(ns *nodeState, b Binding, m sparkplug.Metric) {
	if m.IsNull {
		return // "no value" — keep whatever we had
	}
	if b.ArrayLen > 0 {
		d.warnOnceLocked("array:"+b.Name,
			"sparkplug host: array bindings are not supported yet, metric ignored",
			"tag", b.Name, "metric", b.Metric)
		return
	}
	if d.values == nil {
		d.values = nio.Values{}
	}
	t := d.targetType(ns, b)
	prev := ir.Zero(t)
	if t != nil && t.Kind == ir.TypeStruct {
		// Partial template updates are legal: merge onto the previous value
		// so members this message omits keep theirs.
		if cur, ok := d.values[b.Name].(ir.Value); ok && cur.Kind == ir.TypeStruct && cur.Struct == t.Struct {
			prev = cur
		}
	}
	v, unknown, err := sparkplug.ValueFromMetricInto(m, prev)
	if err != nil {
		d.warnOnceLocked("conv:"+b.Name,
			"sparkplug host: metric does not fit its binding",
			"tag", b.Name, "node", b.Node, "metric", b.Metric, "type", b.Type, "error", err)
		return
	}
	for _, u := range unknown {
		d.warnOnceLocked("member:"+b.Name+"."+u,
			"sparkplug host: template member is not in the manifest type, ignored",
			"tag", b.Name, "type", b.Type, "member", u)
	}
	d.values[b.Name] = plainOrIR(v)
}

// targetType is the ir.Type a binding's value must take.
//
// The manifest wins over the birth: it is the committed contract, it is what
// the generated ST TYPEs were built from, and it resolves nested templates the
// birth's own definition metrics may not (see harvestTemplatesLocked). Only a
// type the manifest does not name falls back to the definitions this node
// birthed, and then to the metric's own Sparkplug datatype (nil).
func (d *Driver) targetType(ns *nodeState, b Binding) *ir.Type {
	if t, err := bindingType(b, d.defs); err == nil {
		return t
	}
	if ns != nil {
		if sd, ok := ns.defs[b.Type]; ok && sd != nil {
			return &ir.Type{Kind: ir.TypeStruct, Struct: sd}
		}
	}
	return nil
}

// plainOrIR normalises a value for the io.Driver seam: scalars go over as
// plain Go values, compound values as ir.Value so field names and the
// StructDef survive. This is exactly what eip/driver.go's snapshot holds.
func plainOrIR(v ir.Value) any {
	switch v.Kind {
	case ir.TypeBool:
		return v.B
	case ir.TypeInt, ir.TypeTime:
		return v.I
	case ir.TypeReal:
		return v.F
	case ir.TypeString:
		return v.S
	}
	return v
}

// ── unmanifested metrics ─────────────────────────────────────────────────

// recordUnknownLocked counts a metric seen on the wire but absent from the
// manifest and, under the default "log" policy, logs it once at INFO with the
// exact manifest line to paste. Under "strict" the driver reports degraded.
// Nothing is ever silently dropped. Caller holds d.mu.
func (d *Driver) recordUnknownLocked(group string, key metricKey, m sparkplug.Metric) {
	if d.unknown == nil {
		d.unknown = map[metricKey]*discovery{}
	}
	e, ok := d.unknown[key]
	if !ok {
		e = &discovery{key: key, datatype: m.Datatype.String()}
		d.unknown[key] = e
	}
	e.count++
	if e.datatype == "" || e.datatype == spb.DataType_Unknown.String() {
		e.datatype = m.Datatype.String()
	}
	switch d.discovery {
	case DiscoveryIgnore:
		return
	case DiscoveryStrict:
		d.degraded = true
		if !e.logged {
			e.logged = true
			d.stateLogger().Warn("sparkplug host: unmanifested metric (strict)",
				"group", group, "node", key.EdgeNode, "device", key.Device,
				"metric", key.Metric, "yaml", d.discoveryYAML(e))
		}
	default: // DiscoveryLog and anything unset
		if !e.logged {
			e.logged = true
			d.stateLogger().Info("sparkplug host: metric not in the manifest",
				"group", group, "node", key.EdgeNode, "device", key.Device,
				"metric", key.Metric, "add", d.discoveryYAML(e))
		}
	}
}

// discoveryYAML renders the manifest tags: line that would bind this metric,
// ready to paste (or, better, to re-run `nautilus sparkplug import` for).
func (d *Driver) discoveryYAML(e *discovery) string {
	typ := e.datatype
	if typ == "" {
		typ = spb.DataType_Unknown.String()
	}
	return fmt.Sprintf("- { name: %s, node: %s, device: %q, metric: %s, type: %s, arraylen: 0, writable: false }",
		d.discoveryTagName(e.key), e.key.EdgeNode, e.key.Device, e.key.Metric, typ)
}

// discoveryTagName composes the nautilus tag name this metric would get:
// <prefix>_<sanitize(device)>_<sanitize(metric)>, the device segment omitted
// for a node-level metric.
func (d *Driver) discoveryTagName(key metricKey) string {
	prefix := Sanitize(key.EdgeNode)
	if n, ok := d.nodeCfg[key.EdgeNode]; ok {
		prefix = n.TagPrefix()
	}
	return TagName(prefix, key.Device, key.Metric)
}

// Discovered returns every metric seen on the wire but absent from the
// manifest, sorted, each with the manifest line that would bind it. It backs
// /api/drivers' extra.unknown and the "what did I miss" half of
// `nautilus sparkplug browse`.
func (d *Driver) Discovered() []Discovered {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]Discovered, 0, len(d.unknown))
	for _, e := range d.unknown {
		out = append(out, Discovered{
			Group:    d.groupOf(e.key.EdgeNode),
			EdgeNode: e.key.EdgeNode,
			Device:   e.key.Device,
			Metric:   e.key.Metric,
			Datatype: e.datatype,
			Count:    e.count,
			YAML:     d.discoveryYAML(e),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].EdgeNode != out[j].EdgeNode {
			return out[i].EdgeNode < out[j].EdgeNode
		}
		if out[i].Device != out[j].Device {
			return out[i].Device < out[j].Device
		}
		return out[i].Metric < out[j].Metric
	})
	return out
}

// groupOf finds the group an edge node was seen under. Caller holds d.mu.
func (d *Driver) groupOf(edge string) string {
	for nk := range d.nodes {
		if nk.EdgeNode == edge {
			return nk.Group
		}
	}
	return d.manifest.Group
}

// unknownCount is the number of distinct unmanifested metrics, for
// Status.Unknown.
func (d *Driver) unknownCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.unknown)
}

// isDegraded reports whether strict discovery has seen an unmanifested metric.
func (d *Driver) isDegraded() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.degraded
}

// ── synthesized companion tags ───────────────────────────────────────────

// initValues seeds the synthesized quality tags so they exist from t=0: alarm
// logic and ST interlocks work before the first birth, while data metrics stay
// absent until first seen (an unseen metric must fault the scan that reads it
// — loudly and on purpose). New calls this after buildIndexes.
func (d *Driver) initValues() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.values == nil {
		d.values = nio.Values{}
	}
	for _, n := range d.manifest.Nodes {
		if t := n.OnlineTagName(); t != "" {
			d.values[t] = false
		}
		if t := n.BirthTagName(); t != "" {
			d.values[t] = int64(0)
		}
		for _, dev := range n.Devices {
			if t := n.DeviceOnlineTagName(dev); t != "" {
				d.values[t] = false
			}
		}
	}
}

// syncTagsLocked republishes a node's synthesized companion tags after any
// state change. Only manifest nodes get them — a node nobody imported has no
// tags in the project to write into. Caller holds d.mu.
func (d *Driver) syncTagsLocked(ns *nodeState) {
	cfg, ok := d.nodeCfg[ns.edge]
	if !ok {
		return
	}
	if d.values == nil {
		d.values = nio.Values{}
	}
	if t := cfg.OnlineTagName(); t != "" {
		d.values[t] = ns.online
	}
	if t := cfg.BirthTagName(); t != "" {
		d.values[t] = ns.birthMs
	}
	declared := make(map[string]Device, len(cfg.Devices))
	for _, dev := range cfg.Devices {
		declared[dev.Device] = dev
	}
	for id, live := range ns.devices {
		dev, known := declared[id]
		if !known {
			dev = Device{Device: id}
		}
		if t := cfg.DeviceOnlineTagName(dev); t != "" {
			d.values[t] = live.online
		}
	}
}

// ── snapshot, status, staleness ──────────────────────────────────────────

// snapshot is the copy ReadInputs serves: every value the driver holds,
// offline nodes included, with their __Online tags false. Values survive
// death by design — the last value *is* the value.
func (d *Driver) snapshot() nio.Values {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make(nio.Values, len(d.values))
	for k, v := range d.values {
		out[k] = v
	}
	return out
}

// nodeStatuses is the per-site rows Status() and /api/drivers' Devices list
// render, sorted by group then edge node for a stable UI.
func (d *Driver) nodeStatuses() []NodeStatus {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]NodeStatus, 0, len(d.nodes))
	for nk, ns := range d.nodes {
		st := NodeStatus{
			Group:     nk.Group,
			EdgeNode:  nk.EdgeNode,
			Online:    ns.online,
			Stale:     ns.stale,
			BdSeq:     ns.bdSeq,
			Seq:       ns.seq,
			BirthMs:   ns.birthMs,
			LastMsgMs: ns.lastMs,
			Metrics:   ns.metrics,
		}
		for _, dev := range ns.devices {
			st.Devices = append(st.Devices, DeviceStatus{
				ID: dev.id, Online: dev.online, BirthMs: dev.birthMs, Metrics: dev.metrics,
			})
		}
		sort.Slice(st.Devices, func(i, j int) bool { return st.Devices[i].ID < st.Devices[j].ID })
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Group != out[j].Group {
			return out[i].Group < out[j].Group
		}
		return out[i].EdgeNode < out[j].EdgeNode
	})
	return out
}

// sweepStale marks online nodes that have gone quiet for longer than
// StaleAfter and asks each once to birth again — a silent node is either dead
// without a will or has drifted out of our sequence window, and both are cured
// by a rebirth. Values and __Online are untouched: staleness is a separate
// axis from the death certificate. Start's ticker calls this; StaleAfter == 0
// turns it off.
func (d *Driver) sweepStale(now time.Time) {
	if d.cfg.StaleAfter <= 0 {
		return
	}
	cutoff := now.Add(-d.cfg.StaleAfter).UnixMilli()
	d.mu.Lock()
	for _, ns := range d.nodes {
		if !ns.online || ns.lastMs == 0 || ns.lastMs >= cutoff {
			continue
		}
		if !ns.stale {
			d.stateLogger().Warn("sparkplug host: node stale",
				"group", ns.group, "node", ns.edge, "silent", now.Sub(time.UnixMilli(ns.lastMs)))
		}
		ns.stale = true
		d.askRebirthLocked(ns)
	}
	d.mu.Unlock()
	d.flushRebirths()
}

// ── logging ──────────────────────────────────────────────────────────────

// stateLogger is the driver's logger, defaulting so a hand-built Driver (the
// unit tests) never panics.
func (d *Driver) stateLogger() *slog.Logger {
	if d.log != nil {
		return d.log
	}
	return slog.Default()
}

// warnOnceLocked logs a diagnostic the first time its reason is seen, so a
// per-scan fault cannot flood the log. Caller holds d.mu.
func (d *Driver) warnOnceLocked(reason, msg string, args ...any) {
	if d.warned == nil {
		d.warned = map[string]bool{}
	}
	if d.warned[reason] {
		return
	}
	d.warned[reason] = true
	d.stateLogger().Warn(msg, args...)
}
