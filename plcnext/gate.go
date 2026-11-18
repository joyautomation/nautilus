package plcnext

import nio "github.com/joyautomation/nautilus/io"

// The write gate (#863, #861 G1). naut run gives every PlainGated driver
// one, open while the program's scans are live (#625, #858); with
// redundancy it is the lease. Closed, nothing leaves this driver:
//
//   - shm: no page writes, the heartbeat included, so the shim's output
//     watchdog (output-watchdog, 1 s by default) drives the output frame to
//     zero: the shim's own safe state. That zeroing is the driver's choice,
//     so it is logged at info and not counted in __WatchdogTrips;
//   - gRPC, or shm with output-watchdog 0: nothing is written, and the
//     controller's outputs HOLD (Manifest.safeOnStop): an error in the log,
//     Health.GateHolds, "outputs held" in the status row;
//   - BusConductor: the gate is looked at before each request the
//     handshake writes; once closed, the bus is only read, and the rest of
//     the handshake runs, before any output, once the gate opens.
//
// It is looked at on every poll, after the read (a gRPC read may take up
// to its call timeout) and just before the write. A gate that closes after
// that look still lets that one poll's write out: up to one write, within
// a scan-rate tick, as eip's per-command look allows one command.
// WriteOutputs does not look: it only merges into the frame in memory, and
// nothing from before a reopen is written (below).
//
// The first look that finds it open again after one found it closed asks
// for every output (OutputsWanted), and nothing is written until the
// runtime's push: then the whole frame. A driver the runtime never handed
// an output to writes its frame as it stands.

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
		// Forget what was sent: the whole frame goes out, and only once
		// the runtime has pushed this term's outputs (OutputsWanted). A
		// runtime that never handed this driver an output will not push
		// one now, so then the frame goes out as it stands.
		d.mu.Lock()
		d.outDirty = true
		d.awaitPush = d.pushed
		d.mu.Unlock()
		d.wanted.Store(true)
		d.log.Info("plcnext: write gate open again; every output goes out with the runtime's next push")
	} else if d.m.safeOnStop() {
		d.log.Warn("plcnext: write gate closed: nothing is written; the shim's watchdog drives the output frame to zero")
	} else {
		// Loud: nothing takes the outputs to a safe state.
		d.log.Error("plcnext: write gate closed: nothing is written, and the outputs HOLD their last values: no output watchdog on this route, so no safe state (transport: shm with a non-zero output-watchdog has one)",
			"transport", d.m.Transport, "output_watchdog", d.m.OutputWatchdog)
	}
	return open
}
