package sparkplug

import (
	"time"

	"github.com/joyautomation/nautilus/lang/ir"
)

// scanAndPublish samples the tag store once, applies each metric's RBE rule,
// and publishes NDATA (node) and DDATA (per device) for what passed. It also
// drives device DBIRTH/DDEATH from health transitions and triggers a rebirth
// when a new metric name appears (a metric must be in a birth before data).
func (n *Node) scanAndPublish() {
	n.mu.Lock()
	if !n.born {
		n.mu.Unlock()
		return
	}
	snap := n.rt.Tags().Snapshot()
	now := time.Now()

	// A metric name we've never birthed → rebirth (debounced) so it appears
	// in a birth before any data references it.
	for name := range snap {
		if _, pub := n.rbeFor(name); !pub {
			continue
		}
		if !n.known[name] {
			n.scheduleRebirthLocked()
			n.mu.Unlock()
			return
		}
	}

	// Device health transitions.
	deviceEvents := n.deviceHealthLocked(snap)

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
	var msgs []sfRecord
	if len(nodeChanged) > 0 {
		msgs = append(msgs, sfRecord{topic: n.topic("NDATA"), metrics: nodeChanged, ts: ts})
	}
	for _, d := range n.devices {
		if ms := devChanged[d.ID]; len(ms) > 0 {
			msgs = append(msgs, sfRecord{topic: n.deviceTopic("DDATA", d.ID), metrics: ms, ts: ts})
		}
	}
	deliverable := n.hostDeliverableLocked()

	// Encode deliverable messages (assigning seq) under the lock.
	type pub struct {
		topic   string
		payload []byte
	}
	var pubs []pub
	if deliverable {
		for _, m := range msgs {
			if p, err := (Payload{Timestamp: m.ts, Seq: n.nextSeq(), Metrics: m.metrics}).Encode(); err == nil {
				pubs = append(pubs, pub{m.topic, p})
			}
		}
	}
	n.mu.Unlock()

	for _, e := range deviceEvents {
		e(n) // DBIRTH/DDEATH, self-locking
	}

	if !deliverable {
		if n.sf != nil {
			for _, m := range msgs {
				n.sf.enqueue(m)
			}
		}
		return
	}

	// Deliverable: replay any backlog (marked historical) before live data so
	// the host sees history then current.
	n.drainStoreForward()
	for _, p := range pubs {
		n.cli.Publish(p.topic, 0, false, p.payload).Wait()
	}
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
func (n *Node) drainStoreForward() {
	if n.sf == nil || n.sf.len() == 0 {
		return
	}
	const batch = 50 // messages per publish tick
	recs := n.sf.drainBatch(batch)
	for _, r := range recs {
		for i := range r.metrics {
			r.metrics[i].IsHistorical = true
		}
		n.mu.Lock()
		p, err := Payload{Timestamp: r.ts, Seq: n.nextSeq(), Metrics: r.metrics}.Encode()
		n.mu.Unlock()
		if err == nil {
			n.cli.Publish(r.topic, 0, false, p).Wait()
		}
	}
	if left := n.sf.len(); left > 0 {
		n.log.Info("sparkplug: store-forward draining", "remaining", left)
	}
}

// collectChanged returns the aliased data metrics for one destination
// (owner=="" is node level) whose values passed RBE, recording new baselines.
// Caller holds n.mu.
func (n *Node) collectChanged(snap map[string]ir.Value, now time.Time, owner string) []Metric {
	var out []Metric
	for _, name := range sortedNames(snap) {
		if n.tagOwner[name] != owner {
			continue
		}
		rbe, pub := n.rbeFor(name)
		if !pub || !n.known[name] {
			continue
		}
		v := snap[name]
		st := n.rbeState[name]
		if st == nil {
			st = &rbeState{}
			n.rbeState[name] = st
		}
		if !rbe.shouldPublish(st, v, now) {
			continue
		}
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
		st.record(v, now)
	}
	return out
}

// deviceHealthLocked detects device health transitions and returns closures
// that publish the corresponding DBIRTH/DDEATH after the lock is released.
// Caller holds n.mu.
func (n *Node) deviceHealthLocked(snap map[string]ir.Value) []func(*Node) {
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
	n.cli.Publish(n.deviceTopic("DBIRTH", d.ID), 0, false, p).Wait()
	n.log.Info("sparkplug: device birth", "device", d.ID, "metrics", len(ms))
}

// publishDeviceDeath sends a DDEATH for a device that went offline.
func (n *Node) publishDeviceDeath(id string) {
	n.mu.Lock()
	p, err := Payload{Timestamp: nowMs(), Seq: n.nextSeq()}.Encode()
	n.mu.Unlock()
	if err != nil {
		return
	}
	n.cli.Publish(n.deviceTopic("DDEATH", id), 0, false, p).Wait()
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
