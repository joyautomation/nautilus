package project

import (
	"fmt"

	"github.com/joyautomation/nautilus/hw"
	"github.com/joyautomation/nautilus/server"
)

// hwStatus adapts an IT-hardware driver's Health (snmp, redfish,
// prometheus — all hw.Base) onto the status envelope: one row, one device
// per source, the Kind being the protocol so the HMI card can label it.
// Same reasoning as modbusStatus: categorical text, free-runners marked
// Volatile, so the block rides a delta stream only when something an
// operator would call a change happened.
func hwStatus(h hw.Health) server.DriverStatus {
	s := server.DriverStatus{
		Kind: h.Kind,
		Name: h.Kind,
	}
	connected, fresh, parked, down, badTags, tags, queued := 0, 0, 0, 0, 0, 0, 0
	var lastErr string
	for _, src := range h.Sources {
		tags += src.Tags
		badTags += src.BadTags
		queued += src.QueuedWrites
		switch src.State {
		case "connected":
			connected++
			if src.Fresh {
				fresh++
			}
		case "parked":
			parked++
		default: // connecting | error
			down++
		}
		if src.LastError != "" {
			lastErr = src.ID + ": " + src.LastError
		}
		if src.SinceMs > s.SinceMs {
			s.SinceMs = src.SinceMs
		}
	}
	total := len(h.Sources)
	s.Detail = fmt.Sprintf("%d %s · %d %s", total, plural(total, "source"), tags, plural(tags, "tag"))
	s.LastError = lastErr

	// connected → degraded (a source dark, silent past stale-after, or a
	// tag refused) → error (nothing answers) → connecting/waiting. Parked
	// sources are deliberate and count against nothing.
	silent := connected - fresh
	switch {
	case down == 0 && connected > 0 && silent > 0:
		s.State = "degraded"
		s.Message = fmt.Sprintf("%d %s silent past stale-after — values held", silent, plural(silent, "source"))
	case down == 0 && connected > 0 && badTags > 0:
		s.State = "degraded"
		s.Message = fmt.Sprintf("%d %s refused — check the bindings", badTags, plural(badTags, "tag"))
	case down == 0 && connected > 0:
		s.State = "connected"
		s.Message = fmt.Sprintf("Polling %d %s", connected, plural(connected, "source"))
	case down == 0 && parked > 0:
		s.State = "waiting"
		s.Message = "All sources parked (enable tags false)"
	case connected > 0:
		s.State = "degraded"
		s.Message = fmt.Sprintf("%d of %d sources down", down, total)
	case lastErr != "":
		s.State = "error"
		s.Message = "Poll failed — retrying"
	default:
		s.State = "connecting"
		s.Message = fmt.Sprintf("Connecting to %d %s", total, plural(total, "source"))
	}

	s.Metrics = []server.DriverMetric{
		{Label: "sources", Value: float64(total), Text: fmt.Sprintf("%d / %d", fresh, total)},
		{Label: "tags", Value: float64(tags)},
		{Label: "polls", Value: float64(h.Polls), Volatile: true},
		{Label: "writes", Value: float64(h.Writes), Volatile: true},
		{Label: "errors", Value: float64(h.Errors), Volatile: true},
	}
	if queued > 0 {
		s.Metrics = append(s.Metrics, server.DriverMetric{Label: "queued writes", Value: float64(queued)})
	}

	rows := make([]hwSourceRow, 0, total)
	for _, src := range h.Sources {
		s.Devices = append(s.Devices, server.DriverDevice{
			ID:     src.ID,
			Online: src.Fresh,
			Detail: hwSourceDetail(src),
		})
		rows = append(rows, hwSourceRow{
			ID: src.ID, Addr: src.Addr, State: src.State, Fresh: src.Fresh, SinceMs: src.SinceMs,
			Tags: src.Tags, BadTags: src.BadTags, QueuedWrites: src.QueuedWrites,
			RTTMs: src.RTTMs, Retries: src.Retries, LastPollMs: src.LastPollMs,
		})
	}
	s.Extra = map[string]any{"sources": rows}
	s.VolatileExtra = []string{"sources.rttMs", "sources.retries", "sources.lastPollMs"}
	return s
}

// hwSourceRow is one source in Extra["sources"].
type hwSourceRow struct {
	ID           string  `json:"id"`
	Addr         string  `json:"addr"`
	State        string  `json:"state"`
	Fresh        bool    `json:"fresh"`
	SinceMs      int64   `json:"sinceMs,omitempty"`
	Tags         int     `json:"tags"`
	BadTags      int     `json:"badTags,omitempty"`
	QueuedWrites int     `json:"queuedWrites,omitempty"`
	RTTMs        float64 `json:"rttMs,omitempty"`
	Retries      uint64  `json:"retries,omitempty"`
	LastPollMs   int64   `json:"lastPollMs,omitempty"`
}

// hwSourceDetail is one source's row text — categorical, no ages.
func hwSourceDetail(src hw.SourceHealth) string {
	switch src.State {
	case "connected":
		switch {
		case !src.Fresh:
			return fmt.Sprintf("%d %s · silent", src.Tags, plural(src.Tags, "tag"))
		case src.BadTags > 0:
			return fmt.Sprintf("%d %s · %d refused", src.Tags, plural(src.Tags, "tag"), src.BadTags)
		case src.QueuedWrites > 0:
			return fmt.Sprintf("%d %s · %d queued", src.Tags, plural(src.Tags, "tag"), src.QueuedWrites)
		}
		return fmt.Sprintf("%d %s", src.Tags, plural(src.Tags, "tag"))
	case "parked":
		return "parked"
	case "error":
		return "error — retrying"
	default:
		return "connecting"
	}
}
