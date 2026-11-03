package sparkplug

import (
	"strings"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/sparkplug/spb"
)

// handleCommand processes NCMD/DCMD. A "Node Control/Rebirth" = true triggers
// a rebirth (NBIRTH+DBIRTH, bdSeq unchanged, seq reset). Other command metrics
// are writes into the tag store (a SCADA host writing a setpoint), which the
// program then acts on and a driver may propagate to the field — but only
// through the write gate (writegate.go): the tag must be opted in with
// WithWritable, and it must belong to the node or device the command was
// addressed to. Everything else is refused and logged.
//
// A Template metric is a UDT (struct) tag, and Sparkplug allows a PARTIAL
// template update — a template carrying only the members that changed — in
// NCMD/DCMD just as it does in NDATA. So a template command MERGES into the
// tag's current value member by member rather than replacing it: a host
// commanding Motor1.START must not zero the Speed the edge is holding.
// Scalars are unchanged: they replace, as they always did. The gate applies
// per member: a template carrying a writable member and a read-only one
// writes the first and refuses the second.
func (n *Node) handleCommand(_ mqtt.Client, msg mqtt.Message) {
	payload, err := DecodePayload(msg.Payload())
	if err != nil {
		n.log.Warn("sparkplug: bad command payload", "topic", msg.Topic(), "error", err)
		return
	}
	n.applyCommand(commandDevice(msg.Topic()), payload)
}

// applyCommand is handleCommand minus the decode — the seam command_test.go
// drives, so the write semantics are testable without an MQTT message.
// device is the DCMD's device id, "" for an NCMD.
func (n *Node) applyCommand(device string, payload Payload) {
	for _, m := range payload.Metrics {
		if m.Name == "Node Control/Rebirth" {
			if b, ok := m.Value.(bool); ok && b {
				n.requestRebirth()
				return
			}
		}
	}
	// Remaining metrics are writes into the tag store. A metric named after
	// a TEMPLATE MEMBER ("P101/Speed" flattened to "P101.Speed" by the host)
	// resolves through Tags.Set to that member of the UDT tag and
	// read-modify-writes the whole struct; a name that resolves to nothing
	// is dropped rather than stored, so a host's typo can no longer create a
	// junk top-level tag that the next NBIRTH would publish back at it.
	tags := n.rt.Tags()
	for _, m := range payload.Metrics {
		if m.Name == "" || m.IsNull || m.Name == "Node Control/Rebirth" {
			continue
		}
		if n.flatten {
			// Flattened, a member is published as "P101/Speed" and a host
			// writes it back under that name: the store's path for it is
			// "P101.Speed" (see flatten.go). Translated first, so the device
			// check and the write gate see the tag and member path.
			m.Name = memberPath(m.Name)
		}
		if owner, _ := n.ownerOf(rootTag(m.Name)); owner != device {
			n.refuseCommand("addressed to the wrong device", m.Name,
				"addressedTo", deviceLabel(device), "publishedOn", deviceLabel(owner))
			continue
		}
		if isArrayType(m.Datatype) {
			n.refuseCommand(arrayWriteRefusal, m.Name, "datatype", m.Datatype.String())
			continue
		}
		// An indexed name ("Levels[1]", "Drives[0].Speed") is an element
		// write. The node never births one — a scalar array is one array
		// metric, an array of structs is not published — so it is refused
		// like a whole-array command, whatever sparkplug.writable opens.
		if strings.ContainsRune(m.Name, '[') {
			n.refuseCommand(arrayWriteRefusal, m.Name)
			continue
		}
		if m.Datatype == spb.DataType_Template {
			pruned, arrays := WithoutArrays(m.Name, m)
			for _, r := range arrays {
				n.refuseCommand(arrayWriteRefusal, r)
			}
			pruned, refused := n.writableTemplate(m.Name, pruned)
			for _, r := range refused {
				n.refuseCommand("not writable", r)
			}
			if t, _ := pruned.Value.(*Template); t != nil && len(t.Metrics) > 0 {
				n.commandTemplate(pruned)
			}
			continue
		}
		if !n.writeAllowed(m.Name) {
			n.refuseCommand("not writable", m.Name)
			continue
		}
		if v, ok := commandValue(m); ok {
			tags.Set(m.Name, v)
			n.log.Debug("sparkplug: command write", "tag", m.Name, "value", m.Value)
		}
	}
}

// arrayWriteRefusal is the reason an array write is refused. Sparkplug can
// carry one (an Int64Array in an NCMD), but the edge node does not apply
// array writes yet, so a command carrying one is refused — counted and
// logged like the write gate's refusals — rather than dropped silently or
// half-applied.
const arrayWriteRefusal = "array writes are not supported"

// WithoutArrays strips the array members out of a template metric, at any
// depth, and returns their dotted paths (under p) so each can be refused by
// name — the edge for a command it receives, the host for one it sends. The
// template's other members are kept; a non-template metric is returned as is.
func WithoutArrays(p string, m Metric) (Metric, []string) {
	t, ok := m.Value.(*Template)
	if !ok || t == nil {
		return m, nil
	}
	var kept []Metric
	var arrays []string
	for _, member := range t.Metrics {
		mp := p + "." + member.Name
		if isArrayType(member.Datatype) {
			arrays = append(arrays, mp)
			continue
		}
		if _, nested := member.Value.(*Template); nested {
			pruned, a := WithoutArrays(mp, member)
			arrays = append(arrays, a...)
			member = pruned
		}
		kept = append(kept, member)
	}
	if len(arrays) == 0 {
		return m, nil
	}
	out := *t
	out.Metrics = kept
	m.Value = &out
	return m, arrays
}

// deviceLabel names a command destination for a log line: the device id, or
// "node" for the edge node itself.
func deviceLabel(device string) string {
	if device == "" {
		return "node"
	}
	return device
}

// commandTemplate merges a Template command metric into the struct tag it
// names, member by member (ValueFromMetricInto with the tag's CURRENT value as
// prev). Members the template does not carry keep the value the edge holds;
// members the tag's type does not know are logged once and ignored.
//
// The read-modify-write is not atomic against a concurrent scan — the tag
// store has no set-one-field primitive here — which is the same window a
// whole-tag command write already has, and a scan writing the same struct
// field an operator is commanding is a program bug either way.
func (n *Node) commandTemplate(m Metric) {
	tags := n.rt.Tags()
	prev, err := tags.ReadGlobal(m.Name)
	if err != nil {
		n.warnCommandOnce("unknown:"+m.Name,
			"sparkplug: template command for a tag this node does not have", "tag", m.Name)
		return
	}
	if prev.Kind != ir.TypeStruct || prev.Struct == nil {
		n.warnCommandOnce("notstruct:"+m.Name,
			"sparkplug: template command for a tag that is not a UDT", "tag", m.Name, "kind", prev.Kind)
		return
	}
	merged, unknown, err := ValueFromMetricInto(m, prev)
	if err != nil {
		n.warnCommandOnce("merge:"+m.Name,
			"sparkplug: template command rejected", "tag", m.Name, "error", err)
		return
	}
	for _, u := range unknown {
		n.warnCommandOnce("member:"+m.Name+"."+u,
			"sparkplug: template command names a member the type does not have; ignored",
			"tag", m.Name, "member", u, "type", prev.Struct.Name)
	}
	tags.Set(m.Name, merged)
	n.log.Debug("sparkplug: template command write", "tag", m.Name, "members", len(templateMembers(m)))
}

// templateMembers is the member count of a template metric, for logging.
func templateMembers(m Metric) []Metric {
	if t, ok := m.Value.(*Template); ok && t != nil {
		return t.Metrics
	}
	return nil
}

// warnCommandOnce logs a command diagnostic the first time its reason is seen,
// so a host retrying a bad member every scan does not flood the log.
func (n *Node) warnCommandOnce(reason, msg string, args ...any) {
	n.mu.Lock()
	if n.cmdWarned == nil {
		n.cmdWarned = map[string]bool{}
	}
	seen := n.cmdWarned[reason]
	n.cmdWarned[reason] = true
	n.mu.Unlock()
	if !seen {
		n.log.Warn(msg, args...)
	}
}

// requestRebirth answers a "Node Control/Rebirth" command. The spec says an
// edge node that receives one MUST stop sending DATA immediately and resume
// only after its new NBIRTH. So the gate closes HERE, synchronously, before
// the handler returns — rebirthPending stops every DATA not yet issued — and
// only the birth itself runs on its own goroutine: an MQTT message handler
// must not block on a publish (paho with OrderMatters), and the birth waits
// on pubMu behind any tick in progress. See the pubMu invariant in node.go.
//
// "Not yet issued" is exact: publishRecords checks the gate under n.mu per
// record, so the one record that can still go out after this returns is one
// that passed its check before the gate closed — the same, on the wire, as a
// record sent just before the command arrived.
func (n *Node) requestRebirth() {
	n.mu.Lock()
	n.rebirthPending = true
	n.mu.Unlock()
	go n.Rebirth()
}

// Rebirth republishes the birth certificates without a new MQTT session:
// bdSeq stays the same, seq restarts at 0. Runs on its own goroutine (see
// requestRebirth and the rebirth-debounce timer below), so it registers
// itself as in-flight the same way birth() does — no-op once Stop has begun,
// so Stop can wait for it before mutating bdSeq.
//
// It does not clear born: the rebirthPending gate stops data instead, which
// leaves born meaning what Stop reads it as — "an NBIRTH is out, so an
// NDEATH is owed" — even when Stop cancels the rebirth.
func (n *Node) Rebirth() {
	if !n.beginInflight() {
		return // Stop already in progress; no-op
	}
	defer n.inflight.Done()

	n.mu.Lock()
	n.rebirthPending = true
	n.mu.Unlock()
	if err := n.birth(); err != nil {
		n.log.Error("sparkplug: rebirth failed", "error", err)
	}
}

// scheduleRebirthLocked debounces a rebirth (500ms) after a new metric
// appears, so bursts of new tags coalesce into one rebirth. Caller holds n.mu.
func (n *Node) scheduleRebirthLocked() {
	if n.rebirthTimer != nil {
		return
	}
	n.rebirthTimer = time.AfterFunc(500*time.Millisecond, func() {
		n.mu.Lock()
		n.rebirthTimer = nil
		n.mu.Unlock()
		n.Rebirth()
	})
}

// commandValue converts a decoded command metric to a tag value the store
// accepts (bool/int64/float64/string).
func commandValue(m Metric) (any, bool) {
	switch v := m.Value.(type) {
	case bool, int64, float64, string:
		return v, true
	}
	return nil, false
}
