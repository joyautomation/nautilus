package sparkplug

import (
	"time"

	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/runtime"
)

// scanAndPublish samples the tag store once, applies each metric's RBE rule,
// and publishes NDATA (node) and DDATA (per device) for what passed. It also
// drives device DBIRTH/DDEATH from health transitions and triggers a rebirth
// when a new metric name appears (a metric must be in a birth before data).
func (n *Node) scanAndPublish() {
	// The whole tick holds the wire (see Node.pubMu): a birth cannot start
	// in the middle of it, and it cannot run while a birth is in progress.
	n.pubMu.Lock()
	defer n.pubMu.Unlock()

	n.mu.Lock()
	// Unborn — the broker is gone, or the node has not birthed yet — the
	// tick still runs when store-and-forward is on and there has been a
	// birth: every metric that birth introduced keeps sampling into the
	// buffer, to replay as historical after the rebirth. That is the
	// "buffers while the broker is unreachable" the option promises; before
	// this the tick returned here and an outage was a hole in the historian.
	// Before the first birth there is nothing a host could receive, so
	// nothing to buffer.
	if !n.liveLocked() && !n.bufferingLocked() {
		n.mu.Unlock()
		return
	}
	msgs, deviceEvents, rebirth := n.publishPassLocked(time.Now())
	if rebirth {
		n.mu.Unlock()
		return
	}
	// Deliverable = born, the primary host (if any) online, AND the
	// transport up right now. born alone is not enough: it is cleared by
	// paho's connection-lost handler, which runs only once paho has noticed
	// the loss, and a tick that lands in that window used to hand its
	// publish to a connection about to be torn down.
	// And not mid-rebirth: liveLocked is false from the moment a rebirth is
	// requested until its NBIRTH is issued.
	deliverable := n.liveLocked() && n.hostDeliverableLocked() && n.cli.IsConnectionOpen()
	n.mu.Unlock()

	for _, e := range deviceEvents {
		e(n) // DBIRTH/DDEATH, self-locking
	}

	// Backlog first (marked historical), then live, so the host sees history
	// then current; seq is assigned at publish time, in that wire order. The
	// first message that does not go out ends the tick: it and everything
	// after it is buffered when store-and-forward is on, dropped otherwise.
	// A QoS 0 publish that timed out is a lost sample, not a reason to stop
	// ticking — the next tick tries again, and the reconnect that a dead link
	// leads to births afresh.
	if deliverable {
		deliverable = n.drainStoreForward()
	}
	if deliverable {
		sent, _ := n.publishRecords(msgs)
		msgs = msgs[sent:]
	}
	if n.sf != nil {
		for _, m := range msgs {
			n.sf.enqueue(m)
		}
	}
}

// publishRecords publishes data records in order, one seq each, and returns
// how many went out. It stops at the first that did not: that record's seq
// is handed back (unsentSeq) so the next message reuses it and the host sees
// no gap — the record was never on the wire, or the link is dead and a birth
// is coming either way. Caller holds n.pubMu.
//
// It also stops — ok=false, the record not sent, so it is buffered or
// dropped like any other undelivered one — when the node stops being live
// part-way through: a rebirth request accepted mid-tick (rebirthPending)
// stops the very next record, not the next tick.
func (n *Node) publishRecords(recs []sfRecord) (sent int, ok bool) {
	for i, r := range recs {
		n.mu.Lock()
		if !n.liveLocked() {
			n.mu.Unlock()
			return i, false
		}
		seq := n.nextSeq()
		births := n.births
		p, err := Payload{Timestamp: r.ts, Seq: seq, Metrics: r.metrics}.Encode()
		if err != nil {
			n.unsentSeq(seq, births)
		}
		n.mu.Unlock()
		if err != nil {
			n.log.Warn("sparkplug: encode", "topic", r.topic, "error", err)
			continue // not a link problem; nothing to retry
		}
		if err := n.publish(r.topic, 0, false, p); err != nil {
			n.mu.Lock()
			n.unsentSeq(seq, births)
			n.mu.Unlock()
			n.log.Warn("sparkplug: publish not sent", "topic", r.topic, "error", err)
			return i, false
		}
		n.mu.Lock()
		n.msgs++
		n.lastPubMs = int64(nowMs())
		n.mu.Unlock()
	}
	return len(recs), true
}

// liveLocked reports whether the node may issue session messages (DATA,
// device DBIRTH/DDEATH): it is born and no rebirth is pending. Caller holds
// n.mu.
func (n *Node) liveLocked() bool {
	return n.born && !n.rebirthPending
}

// bufferingLocked reports whether an unborn node should still sample: it
// has store-and-forward, and a birth has told a host which metrics exist.
// Caller holds n.mu.
func (n *Node) bufferingLocked() bool {
	return n.sf != nil && len(n.known) > 0
}

// publishPassLocked is the CPU half of one publish tick: sample the tag
// store, notice a metric that was never birthed, detect device-health
// transitions, and run every published metric through its RBE rule. It
// returns the {topic, metrics} messages to deliver (or buffer), the
// DBIRTH/DDEATH closures to run after the lock is released, and rebirth=true
// when a rebirth was scheduled instead — in which case the caller publishes
// nothing this tick. Caller holds n.mu.
//
// Unborn (buffering for store-and-forward), the pass only samples: a metric
// that appeared since the last birth waits for the reconnect's birth rather
// than scheduling a rebirth into a dead connection, and device health is
// left alone — the next birth re-evaluates every device.
//
// Split out of scanAndPublish so the pass can be benchmarked without a
// broker: everything above the MQTT seam is here.
func (n *Node) publishPassLocked(now time.Time) (msgs []sfRecord, deviceEvents []func(*Node), rebirth bool) {
	tags := n.rt.Tags()
	n.refreshShapeLocked(tags)
	// Re-copy the store only when SOMETHING moved. The generation is read
	// before the copy, so it can only ever under-state what snapBuf holds —
	// which costs a redundant copy next tick, never a missed value.
	if gen := tags.Generation(); n.snapBuf == nil || gen != n.snapGen {
		n.snapBuf, n.snapGen = tags.SnapshotInto(n.snapBuf), gen
	}
	snap := n.snapBuf

	// A metric name we've never birthed → rebirth (debounced) so it appears
	// in a birth before any data references it. Tags owned by an OFFLINE
	// device are exempt: they cannot be born until the device is healthy —
	// its DBIRTH covers them on the health transition — and rebirthing for
	// them would storm empty births the whole time the device is down
	// (e.g. every startup, while the field driver is still connecting).
	if n.liveLocked() {
		for _, name := range n.pubNames {
			if n.known[name] {
				continue // already birthed — the overwhelmingly common case
			}
			if dev, owned := n.tagOwner[name]; owned && dev != "" && !n.devHealth[dev] {
				continue
			}
			n.scheduleRebirthLocked()
			return nil, nil, true
		}

		// Device health transitions.
		deviceEvents = n.deviceHealthLocked(snap)
	}

	// Collect changed metrics per destination.
	nodeChanged := n.collectChanged(snap, now, "")
	devChanged := map[string][]Metric{}
	for _, d := range n.devices {
		if !n.devHealth[d.ID] {
			continue
		}
		if ms := n.collectChanged(snap, now, d.ID); len(ms) > 0 {
			devChanged[d.ID] = ms
		}
	}

	// Gather the changed messages as {topic, metrics}. Delivery depends on
	// the primary host: when it's offline (and store-and-forward is on) we
	// buffer instead of publishing, replaying on recovery.
	ts := nowMs()
	if len(nodeChanged) > 0 {
		msgs = append(msgs, sfRecord{topic: n.topic("NDATA"), metrics: nodeChanged, ts: ts})
	}
	for _, d := range n.devices {
		if ms := devChanged[d.ID]; len(ms) > 0 {
			msgs = append(msgs, sfRecord{topic: n.deviceTopic("DDATA", d.ID), metrics: ms, ts: ts})
		}
	}
	return msgs, deviceEvents, false
}

// hostDeliverableLocked reports whether live data can be published now:
// with no primary host configured, always; otherwise only when the host is
// online. Caller holds n.mu.
func (n *Node) hostDeliverableLocked() bool {
	if n.cfg.PrimaryHostID == "" {
		return true
	}
	return n.hostOnline
}

// drainStoreForward replays a bounded batch of buffered messages as historical
// data. Rate-limited per call so a large backlog trickles rather than floods.
// It reports whether the link took everything it was given; on a failure the
// undelivered records go back to the front of the buffer, in order.
func (n *Node) drainStoreForward() bool {
	if n.sf == nil || n.sf.len() == 0 {
		return true
	}
	const batch = 50 // messages per publish tick
	recs := n.sf.drainBatch(batch)
	for _, r := range recs {
		for i := range r.metrics {
			r.metrics[i].IsHistorical = true
		}
	}
	sent, ok := n.publishRecords(recs)
	if !ok {
		n.sf.requeue(recs[sent:])
		return false
	}
	if left := n.sf.len(); left > 0 {
		n.log.Info("sparkplug: store-forward draining", "remaining", left)
	}
	return true
}

// refreshShapeLocked rebuilds the tick-invariant tables — which tags publish
// at all, their resolved class rule, and their destination — when and only
// when the tag store's NAME set has changed. Caller holds n.mu.
//
// Class assignment is glob matching (path.Match over every pattern, last
// match wins) and it depends on nothing but the tag's name, so doing it once
// per new tag instead of once per tag per tick is exact, not approximate.
func (n *Node) refreshShapeLocked(tags *runtime.Tags) {
	gen := tags.NameGeneration()
	if n.shapeOK && gen == n.shapeGen {
		return
	}
	n.pubNames = tags.AppendNames(n.pubNames[:0])
	n.tagRBE = make(map[string]RBE, len(n.pubNames))
	n.ownerName = map[string][]string{}
	kept := n.pubNames[:0]
	for _, name := range n.pubNames {
		rbe, pub := n.rbeFor(name)
		if !pub {
			continue // NoPublish: not a metric, not our business
		}
		n.tagRBE[name] = rbe
		owner := n.tagOwner[name]
		n.ownerName[owner] = append(n.ownerName[owner], name)
		kept = append(kept, name)
	}
	n.pubNames = kept
	n.shapeGen, n.shapeOK = gen, true
}

// collectChanged returns the data metrics for one destination (owner==""
// is node level) whose values passed RBE, recording new baselines.
// Caller holds n.mu.
//
// It walks only that destination's tags, in the order refreshShapeLocked
// sorted them, so metric order in a message is what it always was — a full
// re-sort of the whole store per destination per tick is not the price of a
// stable order.
func (n *Node) collectChanged(snap map[string]runtime.Sample, now time.Time, owner string) []Metric {
	var out []Metric
	for _, name := range n.ownerName[owner] {
		if !n.known[name] {
			continue
		}
		s, ok := snap[name]
		if !ok {
			continue
		}
		st := n.rbeState[name]
		if st == nil {
			st = &rbeState{}
			n.rbeState[name] = st
		}
		if !n.tagRBE[name].shouldPublishSample(st, s, now) {
			continue
		}
		v := s.Value
		tmplRef := ""
		if v.Kind == ir.TypeStruct && v.Struct != nil {
			tmplRef = v.Struct.Name
		}
		m, err := MetricFromValue(name, v, tmplRef)
		if err != nil {
			continue
		}
		// Data messages carry the full metric name (aliases are unusable
		// under the TCK — see birth.go).
		m.Timestamp = nowMs()
		out = append(out, m)
		st.record(v, s.Gen, now)
	}
	return out
}

// deviceHealthLocked detects device health transitions and returns closures
// that publish the corresponding DBIRTH/DDEATH after the lock is released.
// Caller holds n.mu.
func (n *Node) deviceHealthLocked(snap map[string]runtime.Sample) []func(*Node) {
	var events []func(*Node)
	for _, d := range n.devices {
		healthy := d.Health == nil || d.Health()
		was := n.devHealth[d.ID]
		if healthy == was {
			continue
		}
		n.devHealth[d.ID] = healthy
		dev := d
		if healthy {
			events = append(events, func(nn *Node) { nn.publishDeviceBirth(dev) })
		} else {
			events = append(events, func(nn *Node) { nn.publishDeviceDeath(dev.ID) })
		}
	}
	return events
}

// publishDeviceBirth sends a DBIRTH for a device that came online.
func (n *Node) publishDeviceBirth(d Device) {
	snap := n.rt.Tags().Snapshot()
	n.mu.Lock()
	if !n.liveLocked() {
		// A rebirth was requested since the tick saw the transition; its
		// birth re-evaluates every device and DBIRTHs the healthy ones.
		n.mu.Unlock()
		return
	}
	ts := nowMs()
	var ms []Metric
	for _, name := range n.deviceTagsSortedLocked(d.ID) {
		v, ok := snap[name]
		if !ok {
			continue
		}
		m, err := n.birthMetric(name, v, ts)
		if err != nil {
			continue
		}
		ms = append(ms, m)
	}
	p, err := Payload{Timestamp: ts, Seq: n.nextSeq(), Metrics: ms}.Encode()
	n.mu.Unlock()
	if err != nil {
		return
	}
	if err := n.publish(n.deviceTopic("DBIRTH", d.ID), 0, false, p); err != nil {
		n.log.Warn("sparkplug: DBIRTH not sent", "device", d.ID, "error", err)
		return
	}
	n.log.Info("sparkplug: device birth", "device", d.ID, "metrics", len(ms))
}

// publishDeviceDeath sends a DDEATH for a device that went offline.
func (n *Node) publishDeviceDeath(id string) {
	n.mu.Lock()
	if !n.liveLocked() {
		// Mid-rebirth: the NBIRTH that follows supersedes the device, and
		// the birth does not DBIRTH an unhealthy one.
		n.mu.Unlock()
		return
	}
	p, err := Payload{Timestamp: nowMs(), Seq: n.nextSeq()}.Encode()
	n.mu.Unlock()
	if err != nil {
		return
	}
	if err := n.publish(n.deviceTopic("DDEATH", id), 0, false, p); err != nil {
		n.log.Warn("sparkplug: DDEATH not sent", "device", id, "error", err)
		return
	}
	n.log.Info("sparkplug: device death", "device", id)
}

func (n *Node) deviceTagsSortedLocked(id string) []string {
	var out []string
	for _, d := range n.devices {
		if d.ID == id {
			out = append(out, d.Tags...)
		}
	}
	return out
}

// timeFromMs converts Sparkplug ms back to a time.Time for RBE baselines.
func timeFromMs(ms uint64) time.Time { return time.UnixMilli(int64(ms)) }
