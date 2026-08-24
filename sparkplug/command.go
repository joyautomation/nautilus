package sparkplug

import (
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/sparkplug/spb"
)

// handleCommand processes NCMD/DCMD. A "Node Control/Rebirth" = true triggers
// a rebirth (NBIRTH+DBIRTH, bdSeq unchanged, seq reset). Other command metrics
// are written back into the tag store (a SCADA host writing a setpoint), which
// the program then acts on and a driver may propagate to the field.
//
// A Template metric is a UDT (struct) tag, and Sparkplug allows a PARTIAL
// template update — a template carrying only the members that changed — in
// NCMD/DCMD just as it does in NDATA. So a template command MERGES into the
// tag's current value member by member rather than replacing it: a host
// commanding Motor1.START must not zero the Speed the edge is holding.
// Scalars are unchanged: they replace, as they always did.
//
// Writable policy is unchanged too. Command writes are not gated per tag on
// this node — every tag named by a command metric is written — so at member
// granularity the root tag's policy is what applies: a member of a writable
// (i.e. any) tag is writable, and nothing new is opened up.
func (n *Node) handleCommand(_ mqtt.Client, msg mqtt.Message) {
	payload, err := DecodePayload(msg.Payload())
	if err != nil {
		n.log.Warn("sparkplug: bad command payload", "topic", msg.Topic(), "error", err)
		return
	}
	n.applyCommand(payload)
}

// applyCommand is handleCommand minus the decode — the seam command_test.go
// drives, so the write semantics are testable without an MQTT message.
func (n *Node) applyCommand(payload Payload) {
	for _, m := range payload.Metrics {
		if m.Name == "Node Control/Rebirth" {
			if b, ok := m.Value.(bool); ok && b {
				go n.Rebirth()
				return
			}
		}
	}
	// Remaining metrics are writes into the tag store.
	tags := n.rt.Tags()
	for _, m := range payload.Metrics {
		if m.Name == "" || m.IsNull {
			continue
		}
		if m.Datatype == spb.DataType_Template {
			n.commandTemplate(m)
			continue
		}
		if v, ok := commandValue(m); ok {
			tags.Set(m.Name, v)
			n.log.Debug("sparkplug: command write", "tag", m.Name, "value", m.Value)
		}
	}
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

// Rebirth republishes the birth certificates without a new MQTT session:
// bdSeq stays the same, seq restarts at 0. Runs on its own goroutine (see
// handleCommand's `go n.Rebirth()` and the rebirth-debounce timer in
// data.go), so it registers itself as in-flight the same way birth() does —
// no-op once Stop has begun, so Stop can wait for it before mutating bdSeq.
func (n *Node) Rebirth() {
	if !n.beginInflight() {
		return // Stop already in progress; no-op
	}
	defer n.inflight.Done()

	n.mu.Lock()
	n.born = false
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
