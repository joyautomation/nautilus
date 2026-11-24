package io

// A redundant pair's takeover hold (#710 (b2), redundancy.takeover-hold):
// after a takeover the new leader scans with its outputs held, sending
// nothing to the field until the program (or an audited operator command)
// releases them. Two optional refinements let a driver take part. The
// runtime uses them when the driver provides them; neither is required.

// OutputsForgetter is a driver that keeps what it sent and re-sends it on
// its own (a Modbus rewrite: keep-alive, a queued command flushed on
// reconnect). ForgetOutputs empties all of it, so nothing this replica
// sent in an earlier leadership goes out again while the hold holds.
// Afterwards the driver behaves as after its write gate reopened: the
// next push commands every output.
type OutputsForgetter interface {
	ForgetOutputs()
}

// SafeWriter is a driver that can command some outputs on their own,
// outside the program's push, and say when each one has reached its
// device: the hold's safe: values.
//
// WriteSafe commands each output in v once, whatever the driver sent
// before, and starts tracking their delivery afresh (a later call
// replaces the earlier set). A value equal to what the device already
// holds is still commanded. Undelivered names those of the latest
// WriteSafe's outputs the device has not yet confirmed: still queued,
// waiting for a connection, refused by the device, or dropped by the
// write gate. It is called on every scan while the hold holds, so it must
// be cheap.
type SafeWriter interface {
	WriteSafe(v Values) error
	SafeUndelivered() []string
}
