package sparkplug

import (
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/joyautomation/nautilus/lang/ir"
)

// handleCommand processes NCMD/DCMD. A "Node Control/Rebirth" = true triggers
// a rebirth (NBIRTH+DBIRTH, bdSeq unchanged, seq reset). Other command metrics
// are written back into the tag store (a SCADA host writing a setpoint), which
// the program then acts on and a driver may propagate to the field.
func (n *Node) handleCommand(_ mqtt.Client, msg mqtt.Message) {
	payload, err := DecodePayload(msg.Payload())
	if err != nil {
		n.log.Warn("sparkplug: bad command payload", "topic", msg.Topic(), "error", err)
		return
	}
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
		if v, ok := commandValue(m); ok {
			tags.Set(m.Name, v)
			n.log.Debug("sparkplug: command write", "tag", m.Name, "value", m.Value)
		}
	}
}

// Rebirth republishes the birth certificates without a new MQTT session:
// bdSeq stays the same, seq restarts at 0.
func (n *Node) Rebirth() {
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

var _ = ir.TypeBool // keep ir import stable for future typed writeback
