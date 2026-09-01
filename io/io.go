// Package io defines the field-I/O seam: the runtime reads inputs from a
// Driver before each scan and writes outputs after. Implement Driver to bring
// your own bus — Modbus, EtherNet/IP, OPC-UA, a REST-fronted rack, or an
// in-process plant simulation. A Memory driver is included for tests.
package io

import "sync"

// Values is a set of tag values keyed by name. Simple drivers exchange plain
// scalars (float64, bool, int64, string); typed field-bus drivers (EtherNet/IP
// UDTs, arrays) exchange ir.Value so field names and integer widths survive
// the crossing. The runtime accepts both on input and hands compound tags to
// WriteOutputs as ir.Value.
type Values map[string]any

// Driver is a field device. ReadInputs is called before each scan;
// WriteOutputs after. Both may block on real network I/O — the runtime runs
// them off the caller's goroutine and holds last-known values on error.
//
// WriteOutputs receives the outputs when at least one of them CHANGED since
// the previous call — plus the first scan, the scan after a failed write,
// and the first scan after a redundancy takeover. A driver that needs a
// per-scan call regardless (a watchdog to re-arm, a bus whose outputs decay)
// asks for it with runtime.Options.AlwaysWriteOutputs.
type Driver interface {
	ReadInputs() (Values, error)
	WriteOutputs(Values) error
}

// BatchReader is an optional refinement a Driver may also implement:
// refill the runtime's map in place instead of returning a fresh one.
//
// ReadInputs allocates the whole input set every scan, which on a controller
// with hundreds of UDT inputs is tens of kilobytes of garbage per scan for a
// map whose keys never change. A driver that implements this is handed the
// same map back each scan and only has to overwrite what it read; entries it
// no longer has must be deleted, so dst always describes THIS delivery.
//
// The runtime uses it when the driver provides it and falls back to
// ReadInputs otherwise — implementing it is never required.
type BatchReader interface {
	Driver
	ReadInputsInto(dst Values) error
}

// Memory is a trivial in-process driver: outputs written are readable as
// inputs. Useful for tests and loopback wiring. It also implements
// QualityReporter (see SetQuality), so the per-tag quality path can be
// exercised end to end without a field bus.
type Memory struct {
	mu sync.Mutex
	v  Values
	q  map[string]Quality
}

func NewMemory() *Memory { return &Memory{v: Values{}} }

func (m *Memory) ReadInputs() (Values, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(Values, len(m.v))
	for k, val := range m.v {
		out[k] = val
	}
	return out, nil
}

// ReadInputsInto is Memory's BatchReader form: the same delivery, into the
// caller's map.
func (m *Memory) ReadInputsInto(dst Values) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, val := range m.v {
		dst[k] = val
	}
	// m.v ⊆ dst now, so equal lengths prove the key sets match; only a key
	// that Memory no longer holds can make them differ.
	if len(dst) != len(m.v) {
		for k := range dst {
			if _, ok := m.v[k]; !ok {
				delete(dst, k)
			}
		}
	}
	return nil
}

func (m *Memory) WriteOutputs(v Values) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, val := range v {
		m.v[k] = val
	}
	return nil
}
