// status.go is the driver's health snapshot: what internal/project's
// DriverStatus adapter renders on /api/drivers. Discovered() — the other half
// of the reporting surface — lives in state.go beside the discovery bookkeeping
// it reads.
//
// OWNED BY B3.

package host

// Status returns the driver's health: broker identity, connection state, the
// monotonic counters, and one row per edge node. The per-node rows are what
// give the HMI its comms-status page with zero HMI changes — they adapt
// straight onto server.DriverDevice.
func (d *Driver) Status() Status {
	// nodeStatuses takes d.mu itself, so gather it outside the lock.
	nodes := d.nodeStatuses()

	d.mu.Lock()
	defer d.mu.Unlock()

	st := Status{
		Broker:     d.cfg.BrokerURL,
		HostID:     d.cfg.HostID,
		Groups:     append([]string(nil), d.groups...),
		Connected:  d.connected,
		Msgs:       d.stats.Msgs,
		Rebirths:   d.stats.Rebirths,
		SeqGaps:    d.stats.SeqGaps,
		WriteDrops: d.stats.WriteDrops,
		Unknown:    len(d.unknown),
		Degraded:   d.degraded,
		Nodes:      nodes,
	}
	if d.connected {
		// The STATE birth timestamp — the same value the will carries, so a
		// consumer can correlate the certificate it sees with this session.
		st.StateOnlineMs = d.sessionTS
	}
	if d.lastErr != nil {
		st.LastError = d.lastErr.Error()
	}
	return st
}
