package modbus

import (
	"fmt"
	"slices"

	nio "github.com/joyautomation/nautilus/io"
)

// A redundant pair's takeover hold (#710 (b2), io/hold.go): the driver
// forgets what it sent when a hold starts, and writes the hold's safe
// values on their own, telling the runtime when each has landed.

// ForgetOutputs is the driver's io.OutputsForgetter: every source forgets
// what it wrote and what it has queued, so no rewrite: binding re-asserts
// and no queued command goes out while the hold holds (forgetWritten).
func (d *Driver) ForgetOutputs() {
	if mut("hold_forget_nothing") {
		return
	}
	d.forgetWritten()
}

// WriteSafe is the driver's io.SafeWriter: each value is queued as a
// command, whatever was written or decided before, and tracked until its
// write lands. With the write gate closed nothing is queued, and the
// values stay undelivered.
func (d *Driver) WriteSafe(vals nio.Values) error {
	open := d.gateOpen()
	d.srcMu.RLock()
	defer d.srcMu.RUnlock()
	owned := map[string]bool{}
	for _, s := range d.sources {
		s.mu.Lock()
		s.safe = map[string]any{}
		queued := false
		for _, name := range s.outputs {
			v, ok := vals[name]
			if !ok {
				continue
			}
			if _, binding := s.byName[name]; !binding {
				continue // an enable tag is no output to the device
			}
			owned[name] = true
			s.safe[name] = v
			if !open {
				continue
			}
			delete(s.undecided, name)
			s.written[name] = v
			s.pending[name] = v
			queued = true
		}
		s.mu.Unlock()
		if queued {
			nudge(s.kick)
		}
	}
	var missing []string
	for name := range vals {
		if !owned[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		slices.Sort(missing)
		return fmt.Errorf("modbus: safe values for %v: no writable binding of that name", missing)
	}
	return nil
}

// SafeUndelivered is the driver's io.SafeWriter: the latest WriteSafe's
// outputs whose write has not landed.
func (d *Driver) SafeUndelivered() []string {
	d.srcMu.RLock()
	defer d.srcMu.RUnlock()
	var out []string
	for _, s := range d.sources {
		s.mu.Lock()
		for name := range s.safe {
			out = append(out, name)
		}
		s.mu.Unlock()
	}
	return out
}

// safeLandedLocked marks the safe values among a landed write's tags
// delivered: only the value WriteSafe asked for, not a later one. Caller
// holds s.mu.
func (s *source) safeLandedLocked(names []string, work map[string]any) {
	if len(s.safe) == 0 || mut("hold_safe_never_lands") {
		return
	}
	for _, n := range names {
		if v, ok := s.safe[n]; ok && sameScalar(v, work[n]) {
			delete(s.safe, n)
		}
	}
}

var (
	_ nio.OutputsForgetter = (*Driver)(nil)
	_ nio.SafeWriter       = (*Driver)(nil)
)
