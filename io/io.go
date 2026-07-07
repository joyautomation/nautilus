// Package io defines the field-I/O seam: the runtime reads inputs from a
// Driver before each scan and writes outputs after. Implement Driver to bring
// your own bus — Modbus, EtherNet/IP, OPC-UA, a REST-fronted rack, or an
// in-process plant simulation. A Memory driver is included for tests.
package io

import "sync"

// Values is a set of tag values keyed by name (float64 or bool).
type Values map[string]any

// Driver is a field device. ReadInputs is called before each scan;
// WriteOutputs after. Both may block on real network I/O — the runtime runs
// them off the caller's goroutine and holds last-known values on error.
type Driver interface {
	ReadInputs() (Values, error)
	WriteOutputs(Values) error
}

// Memory is a trivial in-process driver: outputs written are readable as
// inputs. Useful for tests and loopback wiring.
type Memory struct {
	mu sync.Mutex
	v  Values
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

func (m *Memory) WriteOutputs(v Values) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, val := range v {
		m.v[k] = val
	}
	return nil
}
