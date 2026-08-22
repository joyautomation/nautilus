// state.go is the Sparkplug state machine: alias tables, NBIRTH/DBIRTH,
// sequence tracking and the reorder buffer, NDEATH/DDEATH, template assembly
// into ir.Values, and the snapshot ReadInputs serves. No MQTT types appear
// here — the only inbound seam is handleMessage(topic, payload).
//
// OWNED BY B2. Shared types live in types.go; add methods here rather than
// widening that file.

package host

import "strings"

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
//
// STUB — B2 implements the real state machine here. Keep the signature: mqtt.go
// (B3) and the unit tests both call it.
func (d *Driver) handleMessage(topic string, payload []byte) {
	parts := strings.Split(topic, "/")
	if len(parts) < 3 {
		return
	}
	_ = payload
}
