package sparkplug

// Primary-host STATE gating. When a PrimaryHostID is configured, the edge node
// defers birth until that host announces STATE=ONLINE and dies when it goes
// offline — the spec's mechanism for coordinating store-and-forward with a
// designated consumer. With no primary host configured these are no-ops and
// the node births as soon as it connects.
//
// Full STATE subscription + store-and-forward land in a follow-up; this file
// holds the seams the lifecycle already calls so the wiring is in place.

// subscribeHost subscribes to the primary host's STATE topics. No-op until the
// STATE handler is implemented; safe to call on every (re)connect.
func (n *Node) subscribeHost() {
	if n.cfg.PrimaryHostID == "" {
		return
	}
	// TODO(sparkplug): subscribe STATE/<host> (2.x string) and
	// spBv1.0/STATE/<host> (3.0 JSON), track n.hostOnline with a monotonic
	// timestamp guard, and birth/death on transitions.
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
