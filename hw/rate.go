package hw

import "time"

// Counter turns a monotonically increasing wire counter (ifHCInOctets, a
// node_exporter *_total) into a per-second rate across polls.
//
// Two facts about counters decide the rules. A 32-bit counter WRAPS: a
// gigabit port rolls ifInOctets every ~34 s, so a negative delta on a
// counter declared width 32 means one wrap and the rate is computed across
// it (a second wrap inside one interval is undetectable, which is why the
// generator prefers the 64-bit ifHC columns). A 64-bit counter does not wrap
// in any lifetime, so a negative delta there is a RESET — the device
// rebooted or the exporter restarted — and no honest rate exists for that
// interval: Observe reports ok=false and the caller keeps the last rate.
type Counter struct {
	seen  bool
	last  uint64
	lastF float64
	at    time.Time
}

// Observe records one sample and returns the rate since the previous one.
// ok is false on the first sample, on a reset, and when no time has passed.
func (c *Counter) Observe(v uint64, width int, now time.Time) (rate float64, ok bool) {
	defer func() { c.seen, c.last, c.at = true, v, now }()
	if !c.seen {
		return 0, false
	}
	dt := now.Sub(c.at).Seconds()
	if dt <= 0 {
		return 0, false
	}
	var delta float64
	switch {
	case v >= c.last:
		delta = float64(v - c.last)
	case width == 32:
		delta = float64(v + (1<<32 - c.last))
	default:
		return 0, false // 64-bit went backwards: a reset, not a wrap
	}
	return delta / dt, true
}

// ObserveFloat is Observe for a counter that is already a float with no
// wire width — a Prometheus *_total, an accumulator of fractional seconds.
// Such a counter never wraps; the ecosystem rule is that ANY decrease means
// the process or exporter restarted, so a step backwards is a reset.
func (c *Counter) ObserveFloat(v float64, now time.Time) (rate float64, ok bool) {
	defer func() { c.seen, c.lastF, c.at = true, v, now }()
	if !c.seen {
		return 0, false
	}
	dt := now.Sub(c.at).Seconds()
	if dt <= 0 || v < c.lastF {
		return 0, false
	}
	return (v - c.lastF) / dt, true
}

// Reset forgets the previous sample — a source that reconnected after an
// outage must not compute a rate across the gap it never observed.
func (c *Counter) Reset() { *c = Counter{} }
