package sparkplug

import (
	"encoding/json"
	"strings"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// Primary-host STATE gating. When a PrimaryHostID is configured the edge node
// tracks that host's STATE birth-certificate: it defers its own birth until
// the host is ONLINE and, once born, dies (NDEATH) if the host goes offline —
// the spec's mechanism for coordinating store-and-forward with a designated
// consumer, so the node buffers exactly when its consumer isn't listening.
// With no primary host these are no-ops and the node births on connect.
//
// Two STATE encodings coexist in the wild and we accept both:
//   - Sparkplug 2.x: topic "STATE/<host>", retained payload "ONLINE"/"OFFLINE".
//   - Sparkplug 3.0: topic "spBv1.0/STATE/<host>", retained JSON
//     {"online":bool,"timestamp":ms}.

type hostState struct {
	Online    bool  `json:"online"`
	Timestamp int64 `json:"timestamp"`
}

// subscribeHost subscribes to the primary host's STATE topics. Safe to call on
// every (re)connect; no-op without a primary host.
func (n *Node) subscribeHost() {
	if n.cfg.PrimaryHostID == "" {
		return
	}
	n.cli.Subscribe("STATE/"+n.cfg.PrimaryHostID, 1, n.handleState)
	n.cli.Subscribe("spBv1.0/STATE/"+n.cfg.PrimaryHostID, 1, n.handleState)
}

// handleState processes a primary-host STATE message and drives birth/death on
// transitions. A monotonic-timestamp guard ignores stale retained replays.
func (n *Node) handleState(_ mqtt.Client, msg mqtt.Message) {
	online, ts, ok := parseState(msg.Payload())
	if !ok {
		return
	}
	n.mu.Lock()
	if ts != 0 && ts < n.hostTS {
		n.mu.Unlock()
		return // older than what we've already seen — stale retained message
	}
	if ts != 0 {
		n.hostTS = ts
	}
	was := n.hostOnline
	n.hostOnline = online
	born := n.born
	n.mu.Unlock()

	if online && !was {
		n.log.Info("sparkplug: primary host online", "host", n.cfg.PrimaryHostID)
		if !born {
			if err := n.birth(); err != nil {
				n.log.Error("sparkplug: birth on host-online failed", "error", err)
			}
		}
		// If already born, the next scan will drain any store-forward backlog.
	} else if !online && was {
		n.log.Warn("sparkplug: primary host offline", "host", n.cfg.PrimaryHostID)
		// Data now buffers (store-and-forward) until the host returns.
	}
}

// parseState decodes either STATE encoding. Returns online, timestamp (0 if
// absent), and ok.
func parseState(payload []byte) (online bool, ts int64, ok bool) {
	s := strings.TrimSpace(string(payload))
	switch strings.ToUpper(s) {
	case "ONLINE":
		return true, 0, true
	case "OFFLINE":
		return false, 0, true
	}
	var hs hostState
	if err := json.Unmarshal(payload, &hs); err != nil {
		return false, 0, false
	}
	return hs.Online, hs.Timestamp, true
}

// primaryOnline reports whether the configured primary host is online. With no
// host configured it returns true (no gating).
func (n *Node) primaryOnline() bool {
	if n.cfg.PrimaryHostID == "" {
		return true
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.hostOnline
}
