package io

// Quality is how much a tag's value is worth believing — the thing every
// real SCADA system carries beside the number and nautilus, until now, did
// not. An HMI without it has to GUESS: the Pomona WRD screens infer "comms
// bad" from a magic value (-9999, a stuck reading, a NaN), which is a
// heuristic that goes wrong in both directions — a legitimately -9999-scaled
// analog reads as dead, and a genuinely dead node whose last value was
// plausible reads as live. Quality replaces the guess with a fact the driver
// already knows.
//
// The enum is deliberately four values, not OPC-UA's 256. An operator screen
// makes exactly four distinctions: trust it, it's old, it's wrong, there's
// nothing there. Anything finer belongs in a driver's own status detail
// (server.DriverStatus), not on ten thousand tags.
type Quality uint8

const (
	// Good is a current value from a healthy source — the default for every
	// tag nobody says anything about, so a driver that reports no quality at
	// all behaves exactly as it did before quality existed.
	Good Quality = iota
	// Stale is a value that WAS good and is no longer being refreshed: the
	// source went away and this is its last known reading. The number is
	// still worth showing (it is what the plant last was), which is why this
	// is not Bad — an HMI greys it and shows its age rather than blanking it.
	Stale
	// Bad is a value the source itself reports as untrustworthy: a sensor
	// fault, an out-of-range conversion, a protocol-level error status on an
	// otherwise live connection. The connection is fine; this reading is not.
	Bad
	// NotConnected is "there is no value here yet" — a tag whose source has
	// never delivered. A Sparkplug node that has never birthed, a device
	// address that has never answered. Distinct from Stale because there is
	// no last-known value to show, and distinct from Bad because nothing has
	// failed: it simply has not started.
	NotConnected
)

// String renders a Quality as the token that crosses the HTTP API and lands
// in the HMI kit's `Quality` union type. Lower-camel so it reads as JSON.
func (q Quality) String() string {
	switch q {
	case Stale:
		return "stale"
	case Bad:
		return "bad"
	case NotConnected:
		return "notConnected"
	default:
		return "good"
	}
}

// Good reports whether this quality means the value can be used as-is —
// the single predicate almost every caller wants, so nobody re-derives
// `q == io.Good` and gets it subtly wrong when a fifth value appears.
func (q Quality) IsGood() bool { return q == Good }

// ParseQuality maps a String() token back to a Quality; ok is false for
// anything else. For a driver that carries quality over the wire as text
// (a REST-fronted rack, a store-and-forward relay) and for tests.
func ParseQuality(s string) (Quality, bool) {
	switch s {
	case "good":
		return Good, true
	case "stale":
		return Stale, true
	case "bad":
		return Bad, true
	case "notConnected":
		return NotConnected, true
	}
	return Good, false
}

// QualityReporter is an optional refinement a Driver may also implement:
// say, per tag, how much its last delivered value is worth believing.
//
// The runtime calls Quality after each successful input read and publishes
// what it returns; the server puts the non-Good entries on every frame. A
// driver that does not implement this reports nothing, and every tag it
// delivers is Good — which is exactly the behaviour that existed before,
// so implementing it is never required.
//
// Only NON-GOOD entries need to appear in the returned map: a name that is
// absent is Good. That is not just a convenience, it is the payload budget
// — a healthy 10,000-tag controller sends an empty map, and a controller
// with one dead RTU sends the forty tags behind it. A driver that returns
// Good entries is not wrong, merely wasteful; the runtime drops them.
//
// The map is READ, not retained: the runtime copies what it needs before
// returning, so a driver may hand back the same map every call.
//
// The natural implementation is the one the Sparkplug host driver has for
// free: a node that has never birthed → NotConnected on its metrics; a node
// that birthed and then died (or whose __Online went false) → Stale on the
// values it left behind; an individual metric the edge marked with a bad
// datatype/quality → Bad.
type QualityReporter interface {
	Driver
	Quality() map[string]Quality
}

// NOTE (sparkplug-host / demo-integration seam): this file is ported
// verbatim from st-struct-pins' io/quality.go, EXCEPT for the bottom
// section that file adds to Memory (SetQuality/Quality and the
// `_ BatchReader = (*Memory)(nil)` assertion). This branch's Memory (io.go)
// does not yet carry the `q map[string]Quality` field or the BatchReader
// interface those add — that lands with st-struct-pins' own io.go changes
// at the real merge. Everything above this line is byte-for-byte identical
// to st-struct-pins' io/quality.go, so that half of the merge is a no-op.
