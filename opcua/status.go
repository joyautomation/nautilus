// status.go is the driver's observability surface: Health for the
// /api/drivers adapter (internal/project/drivers.go opcuaStatus) and
// Quality for the per-tag verdicts the runtime publishes beside the values
// (io.QualityReporter).
package opcua

import (
	nio "github.com/joyautomation/nautilus/io"
)

// Health reports session state and counters for diagnostics and the HMI
// driver-status card.
type Health struct {
	Name     string `json:"name"`
	Endpoint string `json:"endpoint"`
	// State is connected | connecting | error ("error" is
	// connecting-with-a-reason, between backoffs).
	State     string  `json:"state"`
	SinceMs   int64   `json:"sinceMs"`
	LastError string  `json:"lastError,omitempty"`
	Policy    string  `json:"policy,omitempty"` // negotiated security policy
	Mode      string  `json:"mode,omitempty"`   // negotiated message security mode
	Retries   uint64  `json:"retries"`          // dial/IO failures (free-running)
	RTTMs     float64 `json:"rttMs"`            // EWMA of request round-trip
	Tags      int     `json:"tags"`             // bindings polled
	BadTags   int     `json:"badTags"`          // bindings the server currently refuses
	Classes   int     `json:"classes"`          // scan classes
	MaxNodes  int     `json:"maxNodes"`         // nodes per request in use
	// QueuedWrites is how many commands are parked RIGHT NOW, waiting for
	// a reconnect — a gauge that clears itself when the session comes back.
	QueuedWrites int    `json:"queuedWrites"`
	Reads        uint64 `json:"reads"`
	Writes       uint64 `json:"writes"`
	Errors       uint64 `json:"errors"`
}

// Health returns the current state and counters.
func (d *Driver) Health() Health {
	d.mu.Lock()
	defer d.mu.Unlock()
	h := Health{
		Name:         d.name,
		Endpoint:     d.endpoint,
		State:        d.state,
		SinceMs:      d.sinceMs,
		Policy:       d.server.policy,
		Mode:         d.server.mode,
		Retries:      d.retries,
		RTTMs:        d.rttMs,
		Tags:         len(d.inputs) - 1,
		Classes:      len(d.classes),
		MaxNodes:     d.server.maxNodes,
		QueuedWrites: len(d.pending),
		Reads:        d.reads.Load(),
		Writes:       d.writes.Load(),
		Errors:       d.errs.Load(),
	}
	if d.lastErr != nil {
		h.LastError = d.lastErr.Error()
	}
	for _, b := range d.bindings {
		if b.bad {
			h.BadTags++
		}
	}
	return h
}

// Quality says, per tag, how much its last delivered value is worth
// believing (io.QualityReporter). Only non-Good entries appear:
//
//   - a tag the server answered with a Good status on a live session is
//     Good — unless its last read came back Bad or Uncertain, or with a
//     value the tag store cannot hold, which is Bad (the session is fine,
//     THIS node is not: unknown id, wrong namespace, a dead device behind
//     a gateway, an array).
//   - a tag delivered at least once whose session is now down is Stale:
//     the value holds, greyed, with its age — never a silent zero.
//   - a tag never delivered on a dead session, or never answered on a live
//     one, is NotConnected.
//
// The __Online companion is the driver's own truth and is always Good.
func (d *Driver) Quality() map[string]nio.Quality {
	var out map[string]nio.Quality
	mark := func(name string, q nio.Quality) {
		if out == nil {
			out = map[string]nio.Quality{}
		}
		out[name] = q
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, c := range d.classes {
		for _, b := range c.bindings {
			switch {
			case d.online && b.bad:
				mark(b.Name, nio.Bad)
			case d.online:
				if !b.delivered {
					mark(b.Name, nio.NotConnected)
				}
			default:
				if b.delivered {
					mark(b.Name, nio.Stale)
				} else {
					mark(b.Name, nio.NotConnected)
				}
			}
		}
	}
	return out
}
