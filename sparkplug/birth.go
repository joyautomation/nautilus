package sparkplug

import (
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/sparkplug/spb"
)

// birth publishes NBIRTH (node metrics) followed by a DBIRTH for each healthy
// device. It (re)assigns aliases, resets the RBE baseline to the birth values,
// and records which metric names are known — the set a later data message may
// reference. Rebirth calls this again with bdSeq unchanged.
func (n *Node) birth() error {
	snap := n.rt.Tags().Snapshot()

	n.mu.Lock()
	n.seq = 0
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
	n.mu.Unlock()

	// Publish outside the lock (paho tokens).
	if tok := n.cli.Publish(n.topic("NBIRTH"), 0, false, nbirthPayload); tok.Wait() && tok.Error() != nil {
		return tok.Error()
	}
	for _, b := range births {
		n.cli.Publish(n.deviceTopic("DBIRTH", b.device), 0, false, dbirthPayloads[b.device]).Wait()
	}
	n.log.Info("sparkplug: born", "group", n.cfg.GroupID, "node", n.cfg.EdgeNode,
		"bdSeq", n.bdSeq, "nodeMetrics", len(nodeTags), "devices", len(births))
	return nil
}

// birthMetric builds a birth metric (name + datatype + value) and seeds its
// RBE baseline + known table. Caller holds n.mu.
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
	n.known[name] = true
	st := &rbeState{}
	st.record(v, timeFromMs(ts))
	n.rbeState[name] = st
	return m, nil
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
