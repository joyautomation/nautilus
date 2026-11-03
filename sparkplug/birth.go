package sparkplug

import (
	"time"

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

	// A birth belongs to an open connection. One asked for across
	// newSession (a rebirth command or timer from the session it ended)
	// would otherwise go out into the new client while it is still
	// connecting — paho queues a publish on a client retrying its connect.
	// The new session's onConnect births instead, with the bdSeq its will
	// carries. HostOfflineDisconnect: and with the primary host online; the
	// host's ONLINE STATE births otherwise.
	n.mu.Lock()
	ready := n.cli.IsConnectionOpen()
	if n.disconnectsOnHostOffline() {
		ready = ready && n.hostOnline
	}
	n.mu.Unlock()
	if !ready {
		return nil
	}

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
	// Flattened, there are no Template metrics to resolve.
	if !n.flatten {
		nbirth = append(nbirth, n.templateDefs(snap, ts)...)
	}
	for _, name := range nodeTags {
		if m, ok := n.birthMetric(name, snap[name], ts); ok {
			nbirth = append(nbirth, n.expand(m)...)
		}
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
		healthy := n.deviceReadyLocked(d, func(name string) bool { _, ok := snap[name]; return ok }, time.Now())
		n.devHealth[d.ID] = healthy
		if !healthy {
			continue
		}
		var ms []Metric
		for _, name := range devTags[d.ID] {
			if m, ok := n.birthMetric(name, snap[name], ts); ok {
				ms = append(ms, n.expand(m)...)
			}
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
//
// ok=false is a tag Sparkplug has no datatype for (an array of structs, say):
// it is logged once and left out of the birth — not allowed to fail it, which
// would take every other metric down with it — and marked known so it does
// not ask for a rebirth on every tick (see Node.unpublishable).
func (n *Node) birthMetric(name string, v ir.Value, ts uint64) (Metric, bool) {
	m, err := n.metricFor(name, v)
	if err != nil {
		if !n.unpublishable[name] {
			n.log.Warn("sparkplug: tag left out of the birth — no Sparkplug datatype for it",
				"tag", name, "error", err)
		}
		if n.unpublishable == nil {
			n.unpublishable = map[string]bool{}
		}
		n.unpublishable[name] = true
		n.known[name] = true
		return Metric{}, false
	}
	delete(n.unpublishable, name)
	m.Timestamp = ts
	attachMeta(&m, name, n.rt.Meta())
	n.known[name] = true
	st := &rbeState{}
	// gen 0: a birth has no store generation to hand on (it works from a
	// plain Snapshot), so the first publish pass after a birth compares
	// values once — conservative, and births are rare.
	st.record(v, 0, timeFromMs(ts))
	n.rbeState[name] = st
	return m, true
}

// metricFor builds the metric for one tag's value, birth or data. A struct
// instance references its type's definition by name; an array takes its
// element type from the tag's declared type when the value is empty and
// cannot say.
func (n *Node) metricFor(name string, v ir.Value) (Metric, error) {
	tmplRef := ""
	if v.Kind == ir.TypeStruct && v.Struct != nil {
		tmplRef = v.Struct.Name
	}
	var t *ir.Type
	if v.Kind == ir.TypeArray && len(v.Arr) == 0 {
		t = n.rt.Tags().TypeOf(name)
	}
	return MetricFromTypedValue(name, v, t, tmplRef)
}

// deviceReady reports whether a device may be born: its connection is
// healthy AND has delivered — every tag it contributes is in the store. A
// driver connects before its first read completes, and its snapshot fills
// tag by tag; the store catches up a runtime scan later. A DBIRTH sent while
// only some tags are in carries only those ("device birth device=RTU53
// metrics=27" of 47, the slow struct tags read member by member missing).
// One tag that never reads must not keep the device unborn, so once some
// tags are in and the device has waited (see Node.deviceReadyLocked) it is
// born with what it has; a tag arriving after the birth is a new metric,
// which rebirths as any new metric does. A device with no tags is ready
// when healthy.
func deviceReady(d Device, has func(string) bool, waited bool) bool {
	if d.Health != nil && !d.Health() {
		return false
	}
	if len(d.Tags) == 0 {
		return true
	}
	some, all := false, true
	for _, name := range d.Tags {
		if has(name) {
			some = true
		} else {
			all = false
		}
	}
	return all || some && waited
}

// deviceBirthWait is how long a healthy device with only some of its tags in
// the store waits for the rest before it is born without them.
const deviceBirthWait = 5 * time.Second

// deviceReadyLocked is deviceReady with the wait tracked per device: the
// clock starts when the device is healthy with some tags in, and resets
// whenever it is not.
func (n *Node) deviceReadyLocked(d Device, has func(string) bool, now time.Time) bool {
	wait := n.birthWait
	if wait == 0 {
		wait = deviceBirthWait
	}
	since, waiting := n.devPartialSince[d.ID]
	if deviceReady(d, has, waiting && now.Sub(since) >= wait) {
		delete(n.devPartialSince, d.ID)
		return true
	}
	if deviceReady(d, has, true) { // healthy, some tags in: start or keep waiting
		if !waiting {
			n.devPartialSince[d.ID] = now
		}
	} else {
		delete(n.devPartialSince, d.ID)
	}
	return false
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
		if owner, _ := n.ownerOf(name); owner != "" {
			dev[owner] = append(dev[owner], name)
		} else {
			node = append(node, name)
		}
	}
	return node, dev
}
