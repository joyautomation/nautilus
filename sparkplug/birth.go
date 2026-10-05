package sparkplug

import (
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/runtime"
	"github.com/joyautomation/nautilus/sparkplug/spb"
)

// birth publishes NBIRTH (node metrics) followed by a DBIRTH for each healthy
// device. It (re)assigns aliases, resets the RBE baseline to the birth values,
// and records which metric names are known — the set a later data message may
// reference. Rebirth calls this again with bdSeq unchanged.
func (n *Node) birth() error {
	if !n.beginInflight() {
		return nil // Stop already in progress; no-op
	}
	defer n.inflight.Done()

	// Hold the wire for the whole birth: no publish tick may issue anything
	// between the moment this birth restarts seq / marks the node born and
	// the moment its NBIRTH and DBIRTHs are handed to paho. Without it a tick
	// could see born=true and send NDATA seq=1 ahead of NBIRTH seq=0 — or
	// finish an old-session NDATA after a rebirth request but before the
	// NBIRTH answering it.
	n.pubMu.Lock()
	defer n.pubMu.Unlock()

	snap := n.rt.Tags().Snapshot()

	n.mu.Lock()
	n.seq = 0
	n.births++
	session := n.births
	n.known = map[string]bool{}
	n.rbeState = map[string]*rbeState{}

	// Partition published metrics into node-level and per-device.
	nodeTags, devTags := n.partition(snap)

	// NBIRTH metrics: bdSeq + Node Control/Rebirth, then the node-level data
	// metrics — all carrying full names, no aliases. Aliases are unusable
	// under the Sparkplug TCK: it requires Node Control/Rebirth to have no
	// alias, yet once any metric is aliased it requires every metric to be,
	// and Rebirth is mandatory in every NBIRTH — so the two rules conflict.
	// Full names (like tentacle) sidestep it and stay conformant.
	ts := nowMs()
	nbirth := []Metric{
		{Name: "bdSeq", Datatype: spb.DataType_Int64, Timestamp: ts, Value: int64(n.bdSeq)},
		{Name: "Node Control/Rebirth", Datatype: spb.DataType_Boolean, Timestamp: ts, Value: false},
	}
	// UDT template definitions precede instances so a host can resolve them.
	nbirth = append(nbirth, n.templateDefs(snap, ts)...)
	for _, name := range nodeTags {
		m, err := n.birthMetric(name, snap[name], ts)
		if err != nil {
			n.mu.Unlock()
			return err
		}
		nbirth = append(nbirth, m)
	}
	nbirthSeq := n.seq

	// Build DBIRTH payloads for healthy devices.
	type dbirth struct {
		device  string
		seq     uint64
		metrics []Metric
	}
	var births []dbirth
	for _, d := range n.devices {
		healthy := d.Health == nil || d.Health()
		n.devHealth[d.ID] = healthy
		if !healthy {
			continue
		}
		var ms []Metric
		for _, name := range devTags[d.ID] {
			m, err := n.birthMetric(name, snap[name], ts)
			if err != nil {
				n.mu.Unlock()
				return err
			}
			ms = append(ms, m)
		}
		births = append(births, dbirth{device: d.ID, seq: n.nextSeq(), metrics: ms})
	}

	nbirthPayload, err := Payload{Timestamp: ts, Seq: nbirthSeq, Metrics: nbirth}.Encode()
	if err != nil {
		n.mu.Unlock()
		return err
	}
	dbirthPayloads := make(map[string][]byte, len(births))
	for _, b := range births {
		p, err := Payload{Timestamp: ts, Seq: b.seq, Metrics: b.metrics}.Encode()
		if err != nil {
			n.mu.Unlock()
			return err
		}
		dbirthPayloads[b.device] = p
	}
	n.born = true
	n.rebirthPending = false // this NBIRTH answers any rebirth requested before now
	n.bornMs = int64(nowMs())
	bd := n.bdSeq // captured under the lock — Stop() may mutate n.bdSeq concurrently once unlocked
	n.mu.Unlock()

	// Publish outside the lock (paho tokens). An NBIRTH that did not go out
	// leaves the node unborn: data before a birth is a protocol error, and
	// the reconnect that follows a dead link births again from onConnect.
	// (Unless a later birth already owns n.born — hence the births check.)
	if err := n.publish(n.topic("NBIRTH"), 0, false, nbirthPayload); err != nil {
		n.mu.Lock()
		if n.births == session {
			n.born = false
		}
		n.mu.Unlock()
		return err
	}
	for _, b := range births {
		if err := n.publish(n.deviceTopic("DBIRTH", b.device), 0, false, dbirthPayloads[b.device]); err != nil {
			n.log.Warn("sparkplug: DBIRTH not sent", "device", b.device, "error", err)
		}
	}
	n.log.Info("sparkplug: born", "group", n.cfg.GroupID, "node", n.cfg.EdgeNode,
		"bdSeq", bd, "nodeMetrics", len(nodeTags), "devices", len(births))
	return nil
}

// birthMetric builds a birth metric (name + datatype + value + properties)
// and seeds its RBE baseline + known table. Caller holds n.mu.
func (n *Node) birthMetric(name string, v ir.Value, ts uint64) (Metric, error) {
	tmplRef := ""
	if v.Kind == ir.TypeStruct && v.Struct != nil {
		tmplRef = v.Struct.Name
	}
	m, err := MetricFromValue(name, v, tmplRef)
	if err != nil {
		return Metric{}, err
	}
	m.Timestamp = ts
	attachMeta(&m, name, n.rt.Meta())
	n.known[name] = true
	st := &rbeState{}
	// gen 0: a birth has no store generation to hand on (it works from a
	// plain Snapshot), so the first publish pass after a birth compares
	// values once — conservative, and births are rare.
	st.record(v, 0, timeFromMs(ts))
	n.rbeState[name] = st
	return m, nil
}

// attachMeta states a tag's documentation as birth properties: `unit:` as
// engUnit and `desc:` as documentation — the keys Ignition and Cirrus Link
// hosts map onto tag properties, so a host discovers them from the birth
// instead of someone retyping them on the SCADA side. A template instance's
// members are documented under their dotted path (`Motor1.Speed`), the same
// key a manifest's tag-meta: uses. Births only: data messages carry no
// properties (see collectChanged).
func attachMeta(m *Metric, path string, meta map[string]runtime.TagMeta) {
	if tm, ok := meta[path]; ok {
		if tm.Unit != "" {
			m.Properties = append(m.Properties, Property{PropEngUnit, tm.Unit})
		}
		if tm.Desc != "" {
			m.Properties = append(m.Properties, Property{PropDocumentation, tm.Desc})
		}
	}
	if t, ok := m.Value.(*Template); ok && t != nil {
		for i := range t.Metrics {
			attachMeta(&t.Metrics[i], path+"."+t.Metrics[i].Name, meta)
		}
	}
}

// partition splits the published metrics (RBE class != NoPublish) into
// node-level tags and per-device tags, each sorted for deterministic births.
// Caller holds n.mu.
func (n *Node) partition(snap map[string]ir.Value) (node []string, dev map[string][]string) {
	dev = map[string][]string{}
	for _, name := range sortedNames(snap) {
		if _, ok := n.rbeFor(name); !ok {
			continue // NoPublish
		}
		if owner := n.tagOwner[name]; owner != "" {
			dev[owner] = append(dev[owner], name)
		} else {
			node = append(node, name)
		}
	}
	return node, dev
}
