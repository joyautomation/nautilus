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
// the first scan after a redundancy takeover, and a scan where the driver
// asked for them (OutputsRequester). A driver that needs a
// per-scan call regardless (a watchdog to re-arm, a bus whose outputs decay)
// asks for it with runtime.Options.AlwaysWriteOutputs.
//
// The map handed to WriteOutputs belongs to the caller and is REUSED on
// the next call: a driver that keeps values past the call copies them out
// (every driver in tree does) and never stores the map itself.
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

// OutputsRequester is an optional refinement a Driver may also implement:
// ask for every output on the next scan, changed or not.
//
// The runtime hands WriteOutputs the outputs only when one changed (see
// Driver), which every driver that diffs against what it last sent relies
// on. A driver that FORGETS a command it was handed (a write a gateway
// could not deliver, a queued command the write gate dropped, a write gate
// that closed and reopened while another master may have written) would
// otherwise not see that output again until the program changed it.
// OutputsWanted returns true once after such a forget; the runtime asks
// on every scan, beside its own push rule, and when the answer is true it
// hands WriteOutputs every output on that scan.
//
// It is a read-and-clear: one true per request, however many forgets came
// before it, and one push answers it. It is called every scan, so it must
// be cheap (an atomic load) and never block. The runtime uses it when the
// driver provides it — implementing it is never required.
type OutputsRequester interface {
	Driver
	OutputsWanted() bool
}

// InputsReadier is an optional refinement a Driver may also implement:
// say whether its inputs have arrived, so the runtime's first scan runs on
// them and not on seeds or nothing.
//
// A first scan is the one at start (#786) and the first on every takeover
// of a redundant pair (#626): the runtime holds it (no logic, no outputs;
// on a takeover, no frame reset) while InputsReady is false, up to a bound
// (runtime.Options.InputsWait), and its scan loop keeps ticking meanwhile.
// Past the bound it scans anyway, with a warning.
//
// The meaning, for a driver that talks to sources (units, nodes, servers):
// every source has delivered its first read since the process started, or
// failed its first attempt. A failed attempt counts, so one dead device
// does not cost the whole wait on every start and every takeover; its
// inputs read NotConnected (QualityReporter) until they arrive. The answer
// is sticky: once true it stays true for the life of the driver, so a
// standby whose driver polled all along takes over at once. A driver that
// held no session while its replica stood by (the groov EPIC standby with
// a watchdog set, #626) may answer false again from its gate's reopen
// until it has delivered a read made since.
//
// The runtime asks on every tick of a held first scan, so it must be
// cheap and never block. It may look at the driver's write gate (a look
// is how a driver sees the gate open). The runtime uses it when the driver
// provides it; a driver that does not (Memory, a simulation) is ready at
// once.
type InputsReadier interface {
	Driver
	InputsReady() bool
}

// InputsPending is an optional refinement for a set of drivers: name the
// members whose inputs have not arrived (InputsReadier), for the runtime's
// status and log while a first scan waits.
type InputsPending interface {
	InputsPending() []string
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

// ScanBeginner is a driver that wants to know which scan an output push
// belongs to. The runtime calls BeginScan at the start of every scan,
// before it reads the inputs: whatever the driver holds live at that moment
// is delivered to that scan's read, so the push that scan makes was computed
// from it. A driver whose start-up baseline decides each output against what
// the device holds (modbus, opcua) uses it to decide only on a push computed
// from the device's live inputs, never on one computed from init values
// before the first read (#1123). A composite forwards it to its children.
// Internal seam, not stable API.
type ScanBeginner interface{ BeginScan() }
