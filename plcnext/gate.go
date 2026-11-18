package plcnext

import nio "github.com/joyautomation/nautilus/io"

// The write gate (#863, #861 G1). naut run gives every PlainGated driver
// one, open while the program's scans are live (#625, #858); with
// redundancy it is the lease. Closed, nothing leaves this driver:
//
//   - shm: no page writes, the heartbeat included, so the shim's output
//     watchdog (output-watchdog, 1 s by default) drives the output frame to
//     zero: the shim's own safe state;
//   - gRPC: no output span is written, and the controller's outputs hold
//     (that route has no watchdog; shm is the transport with a safe state);
//   - BusConductor: a session started while closed only reads the bus,
//     and configures and starts it once the gate opens.
//
// It is looked at on every poll. The first look that finds it open again
// after one found it closed asks for every output (OutputsWanted), and the
// next write is the whole frame, as it always is.

// SetWriteGate is io.WriteGated.
func (d *Driver) SetWriteGate(gate func() bool) {
	d.gateMu.Lock()
	d.writeGate = gate
	d.gateMu.Unlock()
}

// PlainWriteGate marks this driver's gate as only a gate (io.PlainGated).
func (d *Driver) PlainWriteGate() {}

// OutputsWanted is io.OutputsRequester: true once after a reopen.
func (d *Driver) OutputsWanted() bool { return d.wanted.Swap(false) }

var (
	_ nio.PlainGated       = (*Driver)(nil)
	_ nio.OutputsRequester = (*Driver)(nil)
)

// gateOpen looks at the gate and handles a transition. Not under mu.
func (d *Driver) gateOpen() bool {
	d.gateMu.Lock()
	gate := d.writeGate
	d.gateMu.Unlock()
	if gate == nil {
		return true
	}
	open := gate()
	if was := d.gateShut.Swap(!open); was == !open {
		return open // no change
	}
	if open {
		d.mu.Lock()
		d.outDirty = true // the whole frame on the next write
		d.mu.Unlock()
		d.wanted.Store(true)
		d.log.Info("plcnext: write gate open again; every output goes out with the next write")
	} else if d.m.Transport == TransportShm {
		d.log.Warn("plcnext: write gate closed: nothing is written; the shim's watchdog drives the output frame to zero")
	} else {
		// Loud: on this route nothing takes the outputs to a safe state.
		d.log.Error("plcnext: write gate closed: nothing is written, and the outputs HOLD their last values: gRPC has no output watchdog, so there is no safe state on this route (transport: shm has one)")
	}
	return open
}
