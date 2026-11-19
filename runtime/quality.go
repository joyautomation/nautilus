package runtime

import (
	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
)

// Quality reports, per tag, how much its value is worth believing — the
// non-Good entries only, so a healthy controller answers with nil and the
// whole feature costs a 10,000-tag plant nothing.
//
// A SUBSTITUTED input (substitute.go, #707) is served at the substitute,
// so on a leader it reports Substituted, never Good: a whole-tag substitute
// over anything the sources below say (the value served IS the
// substitute), a member substitute only over Good (the root's other
// members are still the delivery, so Bad, NotConnected and Stale outrank
// it). On a standby nothing is served live, a substitute included: Stale,
// as below.
//
// Two sources, in this order, and one rule for a redundant replica:
//
//  1. The DRIVER, when it implements io.QualityReporter. It is the only
//     thing that actually knows: a Sparkplug host driver knows which edge
//     node died and which has never birthed; an EtherNet/IP driver knows
//     which connection dropped. Whatever it says about a tag stands.
//  2. The RUNTIME's own derivation, for tags the driver said nothing about:
//     when the last input READ failed — or has not happened yet — every
//     driver-bound input is Stale. The scan ran on last-known values (see
//     Scan step 1), which is exactly what Stale means, and it is a fact the
//     runtime holds regardless of whether the driver reports quality at
//     all. It is why a plain Memory or a hand-written driver still gets
//     honest staleness for free, and why a controller that has not
//     completed a scan does not present its seed values as field readings.
//
// A STANDBY (a Coordinator, and this replica is not the one scanning; see
// standby) does not scan, so everything a scan refreshes is frozen at its
// last scan as leader, or at its seeds: the driver-bound inputs, every
// global a program writes, and the measured-dt tags. All of them are
// Stale (#924). A driver's non-Good verdict still stands, being more
// specific; its Good does not, because what it vouches for has not
// reached the tag store. A setpoint the logic only reads is the
// operator's value, not a reading, and keeps its quality. The first scan
// after a takeover restores the sources above. A driver's Substituted is
// not such a verdict: it vouches for a value the standby is not serving,
// so it reads Stale too.
//
// Called on demand — by the server's broadcast tick and by GET /api/state,
// a few times a second — never from the scan loop, so quality costs the
// control cycle nothing. The driver's Quality method is therefore called
// from a goroutine other than the one calling ReadInputs, and must be safe
// for that; every driver in tree already guards its state with a mutex.
//
// The returned map is the caller's own: mutate it freely. A tag named here
// need not exist in the store — NotConnected is precisely the case of a
// source that has never delivered a value, and an HMI binding shows the
// reason instead of an unexplained blank.
func (r *Runtime) Quality() map[string]nio.Quality {
	var out map[string]nio.Quality
	if qr, ok := r.driver.(nio.QualityReporter); ok {
		for name, q := range qr.Quality() {
			if q == nio.Good {
				continue // "absent means Good" — don't spend payload saying it
			}
			if out == nil {
				out = make(map[string]nio.Quality)
			}
			out[name] = q
		}
	}
	standby := r.standby()
	if standby {
		r.scanFed(func(name string) {
			if out == nil {
				out = make(map[string]nio.Quality)
			}
			if _, said := out[name]; !said {
				out[name] = nio.Stale
			}
		})
		// A driver's Substituted is not a verdict that stands here.
		for name, q := range out {
			if q == nio.Substituted {
				out[name] = nio.Stale
			}
		}
	}
	if !r.readOK.Load() && len(r.inputs) > 0 {
		if out == nil {
			out = make(map[string]nio.Quality, len(r.inputs))
		}
		for _, name := range r.inputs {
			if _, said := out[name]; !said {
				out[name] = nio.Stale
			}
		}
	}
	if !standby {
		r.tags.eachSubstitutedRoot(func(root string, s subState) {
			key := root
			for k := range out {
				if ir.SameName(k, root) {
					key = k
					break
				}
			}
			q, said := out[key]
			if s == subWhole || !said || q == nio.Good {
				if out == nil {
					out = make(map[string]nio.Quality)
				}
				out[key] = nio.Substituted
			}
		})
	}
	return out
}

// TagQuality is Quality for one tag, without building the map — for a
// caller checking a single binding (an alarm rule that must not trip on a
// dead reading, an ST expression's guard).
//
// A DOTTED path resolves to its root tag: quality is a property of the
// delivery, and a UDT arrives from its source whole, so P101.Drive.Speed is
// exactly as trustworthy as P101 is.
func (r *Runtime) TagQuality(name string) nio.Quality {
	root := name
	for i := 0; i < len(root); i++ {
		if root[i] == '.' {
			root = root[:i]
			break
		}
	}
	standby := r.standby()
	q := r.liveQuality(root, standby)
	if standby {
		return q
	}
	switch r.tags.substitutedRoot(root) {
	case subWhole:
		return nio.Substituted
	case subMember:
		if q == nio.Good {
			return nio.Substituted
		}
	}
	return q
}

// liveQuality is TagQuality for a root tag without this runtime's own
// substitutes: what the delivery is worth. Substitutes reports it beside
// each substitute.
func (r *Runtime) liveQuality(root string, standby bool) nio.Quality {
	if qr, ok := r.driver.(nio.QualityReporter); ok {
		// On a standby the driver's Good does not stand (see Quality), and
		// nor does its Substituted.
		if q, said := qr.Quality()[root]; said && (q != nio.Good || !standby) {
			if standby && q == nio.Substituted {
				return nio.Stale
			}
			return q
		}
	}
	if standby && r.isScanFed(root) {
		return nio.Stale
	}
	if !r.readOK.Load() {
		for _, in := range r.inputs {
			if in == root {
				return nio.Stale
			}
		}
	}
	return nio.Good
}

// ReportsQuality says whether anything on this controller can ever report a
// non-Good quality — a driver that implements io.QualityReporter, or any
// driver-bound input at all (which the runtime can mark Stale itself). It
// backs /api/meta's capability flag, so an HMI can tell "everything is
// good" apart from "this controller has no idea", and render a quality
// indicator only where one means something.
//
// A replica with a Coordinator always can: as a standby it marks every
// scan-fed tag Stale, driver or none (see Quality).
func (r *Runtime) ReportsQuality() bool {
	if _, ok := r.driver.(nio.QualityReporter); ok {
		return true
	}
	return r.coord != nil || len(r.inputs) > 0
}

// standby reports whether this replica's scan-fed tags are frozen: it has
// a Coordinator, and either no scan of the current leadership has read
// its inputs yet (still standing by; a takeover holding for live inputs,
// #626, on a new term too; a takeover whose first read is in flight; one
// an additional task's gate opened), or its coordinator says another
// replica leads now, which a leader whose scans have stalled has not yet
// acted on. Two atomic reads, so a leader's Quality stays as cheap as a
// standalone's.
func (r *Runtime) standby() bool {
	return r.coord != nil && (!r.fresh.Load() || !r.coord.IsLeader())
}

// scanFed calls fn with the stored name of every tag a scan refreshes: the
// driver-bound inputs, every global a program in any task writes, and the
// measured-dt tags. A name may come more than once.
func (r *Runtime) scanFed(fn func(name string)) {
	for _, name := range r.inputs {
		fn(name)
	}
	r.forEachProgram(func(p *Program) {
		for key := range p.writtenKeys() {
			if name, ok := r.tags.Canonical(key); ok {
				fn(name)
			}
		}
	})
	if r.dtTag != "" {
		fn(r.dtTag)
	}
	for _, tr := range r.tasks {
		if tr.dtTag != "" {
			fn(tr.dtTag)
		}
	}
}

// isScanFed is scanFed for one root tag name, without building the set.
func (r *Runtime) isScanFed(root string) bool {
	if r.dtTag != "" && ir.SameName(root, r.dtTag) {
		return true
	}
	for _, name := range r.inputs {
		if ir.SameName(name, root) {
			return true
		}
	}
	for _, tr := range r.tasks {
		if tr.dtTag != "" && ir.SameName(tr.dtTag, root) {
			return true
		}
	}
	key, fed := ir.NameKey(root), false
	r.forEachProgram(func(p *Program) {
		fed = fed || p.writtenKeys()[key]
	})
	return fed
}

// forEachProgram calls fn with the main task's program, then each
// additional task's.
func (r *Runtime) forEachProgram(fn func(*Program)) {
	fn(r.prog)
	for _, tr := range r.tasks {
		fn(tr.prog)
	}
}
