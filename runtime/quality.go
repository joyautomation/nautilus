package runtime

import (
	nio "github.com/joyautomation/nautilus/io"
)

// Quality reports, per tag, how much its value is worth believing — the
// non-Good entries only, so a healthy controller answers with nil and the
// whole feature costs a 10,000-tag plant nothing.
//
// Two sources, in this order:
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
	if qr, ok := r.driver.(nio.QualityReporter); ok {
		if q, said := qr.Quality()[root]; said {
			return q
		}
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
func (r *Runtime) ReportsQuality() bool {
	if _, ok := r.driver.(nio.QualityReporter); ok {
		return true
	}
	return len(r.inputs) > 0
}
