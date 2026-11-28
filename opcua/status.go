// status.go is the driver's observability surface: Health for the
// /api/drivers adapter (internal/project/drivers.go opcuaStatus) and
// Quality for the per-tag verdicts the runtime publishes beside the values
// (io.QualityReporter).
package opcua

import (
	"time"

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
	QueuedWrites int `json:"queuedWrites"`
	// DroppedEvents counts, since the driver started, the event commands
	// (TagBinding.Event) raised while the session was down and dropped at
	// its reconnect rather than sent late. It moves only at a reconnect.
	DroppedEvents uint64 `json:"droppedEvents"`
	Reads         uint64 `json:"reads"`
	Writes        uint64 `json:"writes"`
	Errors        uint64 `json:"errors"`
	// ReadMode is poll or subscribe. With subscribe, Subscriptions is how
	// many the server granted on this session, Notifications counts the
	// data change notifications received, and RevisedMs is each scan
	// class's publishing interval as the server revised it (a server may
	// publish slower than the class asked).
	ReadMode      string             `json:"readMode"`
	Subscriptions int                `json:"subscriptions,omitempty"`
	Notifications uint64             `json:"notifications,omitempty"`
	RevisedMs     map[string]float64 `json:"revisedMs,omitempty"`
	// Probe is how a subscribed session is checked: SetPublishingMode on
	// the driver's subscriptions, or a Read where the server does not
	// support that service.
	Probe string `json:"probe,omitempty"`
}

// Health returns the current state and counters.
func (d *Driver) Health() Health {
	d.mu.Lock()
	defer d.mu.Unlock()
	h := Health{
		Name:          d.name,
		Endpoint:      d.endpoint,
		State:         d.state,
		SinceMs:       d.sinceMs,
		Policy:        d.server.policy,
		Mode:          d.server.mode,
		Retries:       d.retries,
		RTTMs:         d.rttMs,
		Tags:          len(d.inputs) - 1,
		Classes:       len(d.classes),
		MaxNodes:      d.server.maxNodes,
		QueuedWrites:  len(d.pending),
		DroppedEvents: d.droppedEvents,
		Reads:         d.reads.Load(),
		Writes:        d.writes.Load(),
		Errors:        d.errs.Load(),
		ReadMode:      d.readMode,
	}
	if d.readMode == ReadModeSubscribe {
		h.Subscriptions = d.server.subscriptions
		h.Notifications = d.notifications.Load()
		h.Probe = d.server.probe
		if len(d.server.revised) > 0 {
			h.RevisedMs = map[string]float64{}
			for class, r := range d.server.revised {
				h.RevisedMs[class] = float64(r) / float64(time.Millisecond)
			}
		}
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
//   - a tag delivered at least once whose session is now down, or whose
//     new session has not read it yet, is Stale: the value holds, greyed,
//     with its age — never a silent zero.
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
			case d.online && b.answered && b.subst:
				mark(b.Name, nio.Substituted)
			case d.online && b.answered:
				// Good: the server answered it on this session.
			case b.delivered:
				// Down, or up but not yet read on this session: the value
				// is the last one delivered, not a current reading.
				mark(b.Name, nio.Stale)
			default:
				mark(b.Name, nio.NotConnected)
			}
		}
	}
	return out
}
