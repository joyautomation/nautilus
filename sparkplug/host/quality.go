// quality.go implements per-tag quality reporting: which of a Sparkplug
// binding's tag values is worth believing right now, and why. It satisfies
// io.QualityReporter, so the runtime picks it up automatically and the
// server puts the non-Good entries on every frame — see io/quality.go for
// the four-value enum and the wire contract.
//
// Only DATA bindings (d.inputs: the read-only bindings backed by a
// Sparkplug metric) carry a quality verdict. Everything else the driver
// hands the runtime is host-authored, not a field reading, and stays Good
// (never appears in the map):
//
//   - member output tags — an operator control resolved from a Template
//     leaf. They live in d.byName, not d.inputs, so Quality never sees them.
//   - any other writable binding (a plain operator-command metric) — same
//     reason, same exclusion.
//   - the synthesized companions (__Online, __LastBirthMs, __Rebirth) — they
//     ARE the driver's own liveness signal in tag form (__Online IS what
//     Stale would otherwise be telling you), so putting a quality verdict on
//     top of them would be quality about quality. They live in
//     d.synthInputs/d.synthOutputs, never in d.inputs, so they are excluded
//     the same way.
//
// OWNED BY: this file. Reads d.nodes/d.values (B2's state.go) and
// d.inputs/d.manifest (B1's mapping.go/manifest.go) but writes neither —
// Quality is read-only with respect to driver state.
package host

import nio "github.com/joyautomation/nautilus/io"

// Quality reports, for every DATA binding whose value is not currently
// trustworthy, why. Bindings that are Good are omitted — the "non-Good
// only" contract io.QualityReporter documents — so a fleet where every edge
// node is birthed and current answers with an empty (nil) map.
//
// The verdict, per binding:
//
//   - NO value has ever been delivered for this binding's nautilus tag
//     (nothing under its name in d.values) → NotConnected. This is the
//     "node has never birthed since manifest load" case, and it is also
//     what a binding gets whose metric a birth simply never carries: the
//     driver has no way to distinguish "hasn't happened yet" from "this
//     metric is not actually part of what the node sends" (it logs the
//     latter once elsewhere, as an unmanifested-metric mismatch runs the
//     other way — see recordUnknownLocked), so both read the same to an
//     HMI: there is nothing here to show.
//   - a value EXISTS, but its source is not currently online: the node
//     died (NDEATH), the node has gone quiet longer than StaleAfter
//     (sweepStale), or — for a device-scoped metric — just that device
//     died (DDEATH) while the node itself stayed up → Stale. The value is
//     real, it is just old.
//   - a value exists and its source is online → Good (omitted).
//
// Built under d.mu from the state state.go already maintains — no extra
// bookkeeping, no scan-loop cost. Called off the scan loop (the runtime's
// broadcast tick, GET /api/state), so it takes d.mu itself rather than
// assuming the caller already holds it.
func (d *Driver) Quality() map[string]nio.Quality {
	d.mu.Lock()
	defer d.mu.Unlock()

	var out map[string]nio.Quality
	for _, b := range d.inputs {
		q, ok := d.bindingQualityLocked(b)
		if !ok {
			continue // Good — omit, per the non-Good-only contract
		}
		if out == nil {
			out = make(map[string]nio.Quality, len(d.inputs))
		}
		out[b.Name] = q
	}
	return out
}

// bindingQualityLocked is one data binding's verdict; ok is false for Good,
// which the caller omits. Caller holds d.mu.
func (d *Driver) bindingQualityLocked(b Binding) (nio.Quality, bool) {
	if _, delivered := d.values[b.Name]; !delivered {
		return nio.NotConnected, true
	}
	ns, known := d.nodes[nodeKey{Group: d.manifest.Group, EdgeNode: b.Node}]
	if !known || !ns.online || ns.stale {
		// !known should not happen — a delivered value implies the message
		// that delivered it created ns first (nodeLocked) — but a value
		// with no state behind it is closer to Stale (something was once
		// true, nothing is tracking it now) than to Good.
		return nio.Stale, true
	}
	if b.Device != "" {
		if dev, ok := ns.devices[b.Device]; ok && !dev.online {
			return nio.Stale, true // DDEATH on just this device
		}
	}
	return nio.Good, false
}

// Driver satisfies io.QualityReporter now that io/quality.go (ported from
// st-struct-pins) defines the interface on this branch. Unlike
// ReadInputsInto's io.BatchReader note in driver.go, this assertion is safe
// to state directly: nio.QualityReporter is real here, not merely shaped
// like something that lives on another branch.
var _ nio.QualityReporter = (*Driver)(nil)
