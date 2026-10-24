// status.go is the driver's observability surface: Health for the
// /api/drivers adapter (internal/project/drivers.go modbusStatus) and
// Quality for the per-tag verdicts the runtime publishes beside the values
// (io.QualityReporter).
package modbus

import (
	"math"
	"sort"

	nio "github.com/joyautomation/nautilus/io"
)

// Health reports connection state and counters for diagnostics and the HMI
// driver-status card: one SourceHealth row per source, plus driver-wide
// free-running totals.
type Health struct {
	Sources []SourceHealth `json:"sources"`
	// Reads/Writes/Errors free-run with traffic — the adapter marks them
	// Volatile so they never push the status onto a delta stream by
	// themselves.
	Reads  uint64 `json:"reads"`
	Writes uint64 `json:"writes"`
	Errors uint64 `json:"errors"`
}

// SourceHealth is one source's row.
type SourceHealth struct {
	ID   string `json:"id"`
	Addr string `json:"addr"`
	// State is connected | connecting | error | parked (brief §2's state
	// machine; "error" is connecting-with-a-reason, between backoffs).
	State      string  `json:"state"`
	SinceMs    int64   `json:"sinceMs"` // epoch ms the current state began
	LastError  string  `json:"lastError,omitempty"`
	Retries    uint64  `json:"retries"`    // dial/IO failures (free-running)
	RTTMs      float64 `json:"rttMs"`      // EWMA of request round-trip
	Blocks     int     `json:"blocks"`     // read blocks per cycle
	BadBlocks  int     `json:"badBlocks"`  // blocks currently exception-marked
	Exceptions uint64  `json:"exceptions"` // Modbus exception responses (free-running)
	// QueuedWrites is how many commands are parked for this source RIGHT
	// NOW, waiting for a reconnect — a gauge that clears itself when the
	// source comes back and they go out.
	QueuedWrites int `json:"queuedWrites"`
}

// Health returns the current per-source states and counters.
func (d *Driver) Health() Health {
	h := Health{
		Reads:  d.reads.Load(),
		Writes: d.writes.Load(),
		Errors: d.errs.Load(),
	}
	for _, s := range d.sources {
		s.mu.Lock()
		row := SourceHealth{
			ID:           s.cfg.ID,
			Addr:         s.cfg.Addr(),
			State:        s.state,
			SinceMs:      s.sinceMs,
			Retries:      s.retries,
			RTTMs:        s.rttMs,
			Blocks:       len(s.blocks),
			Exceptions:   s.excs,
			QueuedWrites: len(s.pending),
		}
		if s.lastErr != nil {
			row.LastError = s.lastErr.Error()
		}
		for _, br := range s.blocks {
			if br.bad {
				row.BadBlocks++
			}
		}
		s.mu.Unlock()
		h.Sources = append(h.Sources, row)
	}
	return h
}

// Quality says, per tag, how much its last delivered value is worth
// believing (io.QualityReporter). Only non-Good entries appear:
//
//   - a tag whose source is connected is Good — unless its block is
//     exception-marked, which is Bad (the connection is fine, THIS span of
//     addresses is refused: FTIR's reg-46 collision made visible), or the
//     device delivered a float that is not a number — NaN or ±Inf, which
//     is how a groov EPIC reports an analog channel with nothing wired —
//     which is Bad too: the reading, not the link, is the problem.
//   - a tag delivered at least once whose source is now dead or parked is
//     Stale: the value holds, greyed, with its age — never a silent zero.
//   - a tag never delivered on a dead/parked source is NotConnected.
//
// The __Online companions are the driver's own truth and are always Good.
func (d *Driver) Quality() map[string]nio.Quality {
	var out map[string]nio.Quality
	mark := func(name string, q nio.Quality) {
		if out == nil {
			out = map[string]nio.Quality{}
		}
		out[name] = q
	}
	for _, s := range d.sources {
		s.mu.Lock()
		for _, br := range s.blocks {
			for _, t := range br.Bindings {
				switch {
				case s.online && br.bad:
					mark(t.Name, nio.Bad)
				case s.online:
					// Good — omitted, per the non-Good-only contract — once
					// the block has been delivered on some connection. A tag
					// the device has never answered for is NotConnected even
					// while the socket is up: connected is not the same as
					// heard from (a gateway that accepts TCP and never replies).
					v, delivered := s.snapshot[t.Name]
					switch {
					case !delivered:
						mark(t.Name, nio.NotConnected)
					case notANumber(v):
						mark(t.Name, nio.Bad)
					}
				default:
					if _, delivered := s.snapshot[t.Name]; delivered {
						mark(t.Name, nio.Stale)
					} else {
						mark(t.Name, nio.NotConnected)
					}
				}
			}
		}
		s.mu.Unlock()
	}
	return out
}

// notANumber reports a delivered float the device marked as no reading:
// NaN or ±Inf, the IEEE markers an open analog channel comes back as.
func notANumber(v any) bool {
	f, ok := v.(float64)
	return ok && (math.IsNaN(f) || math.IsInf(f, 0))
}

// Connections lists each source and the tags it delivers — its polled
// bindings and its __Online companion (io.ConnectionReporter).
func (d *Driver) Connections() []nio.Connection {
	out := make([]nio.Connection, 0, len(d.sources))
	for _, s := range d.sources {
		seen := map[string]bool{s.cfg.OnlineTagName(): true}
		for _, br := range s.blocks {
			for _, t := range br.Bindings {
				seen[t.Name] = true
			}
		}
		out = append(out, nio.Connection{ID: s.cfg.ID, Inputs: sortedKeys(seen)})
	}
	return out
}

// ConnectionUp reports whether the source is online — the same verdict its
// __Online companion delivers.
func (d *Driver) ConnectionUp(id string) bool {
	s, ok := d.bySource[id]
	if !ok {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.online
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
