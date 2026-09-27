// rate.go is why prom keeps its own counter instead of hw.Counter for the
// actual per-second rate arithmetic, even though the harness's binding
// vocabulary and hw.Binding.Apply are otherwise reused as-is.
//
// hw.Counter (hw/rate.go) is built for a WIRE-WIDTH counter: a fixed 32- or
// 64-bit register that wraps at its width, so a step backwards on a 32-bit
// counter is a wrap (computed across it) and a step backwards on a 64-bit
// one is a reset (no rate for that interval). Binding.Apply's Rate path
// hands it a uint64 by truncating whatever Raw it was given
// (`u = uint64(fv)`).
//
// A Prometheus counter is neither: node_exporter (and every client library)
// exposes it as an ALREADY-FLOAT64 accumulator with no fixed width and no
// wraparound — the convention across the whole ecosystem is "a decrease
// means the process or exporter restarted", full stop, at any magnitude.
// Truncating node_cpu_seconds_total (a fractional-second accumulator) to a
// uint64 before differencing discards up to ~1 second of the numerator per
// observation, which is a few percent of a typical 15s scan interval's
// delta — visible jitter on CpuPct that has nothing to do with the CPU.
//
// counterF is hw.Counter's algorithm minus the width/wrap machinery that
// does not apply here: seen/last/at, Observe, Reset, byte for byte the same
// contract (ok=false on the first sample and on a reset; the caller keeps
// the last rate). The resolved rate is then run through
// hw.Binding.Apply — with Rate cleared, since counterF already computed the
// rate that field would have — so Scale/Offset/Map still apply the way
// every other member's binding does. See driver.go's resolveMember.
//
// The proposed hw/ patch, for the report rather than this file (prom must
// not edit hw/): give Counter.Observe a float64-native sibling, or a
// Width value that means "already a rate-bearing float, decrease = reset,
// no wraparound" — the same shape prom, and any future exporter-style
// driver, actually needs.
package prom

import "time"

// counterF is one rate-bound (tag, member[, selector]) counter's state.
type counterF struct {
	seen bool
	last float64
	at   time.Time
}

// observe records one sample and returns the rate since the previous one.
// ok is false on the first sample, on a reset (a decrease — the counted
// process or the exporter restarted), and when no time has passed.
func (c *counterF) observe(v float64, now time.Time) (rate float64, ok bool) {
	defer func() { c.seen, c.last, c.at = true, v, now }()
	if !c.seen {
		return 0, false
	}
	dt := now.Sub(c.at).Seconds()
	if dt <= 0 {
		return 0, false
	}
	if v < c.last {
		return 0, false
	}
	return (v - c.last) / dt, true
}

// reset forgets the previous sample — a source that reconnected after an
// outage must not compute a rate across a gap it never observed (brief §5,
// "Reset the counters of a source when it reconnects").
func (c *counterF) reset() { *c = counterF{} }
