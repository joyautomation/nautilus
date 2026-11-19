package io

import "time"

// SendGate is a redundant pair's write gate together with the time a send
// may still take and the lease's term, read in ONE call (design §3.4).
// Reading them in two calls could straddle a renewal, the gate's close, or
// a lease lost and won again.
//
//   - ok false: nothing may be sent now. The gate closes a fence margin
//     before the lease ends, not at its end.
//   - budget: how long a send that starts now may take before it would
//     arrive after the lease (the lease left, minus the plant network's
//     assumed delivery bound). A send whose budget has run out is not
//     started; one still in flight at the end of its budget is reset
//     (internal/fence). A negative budget means no deadline: a gate that
//     is not a pair's lease.
//   - term: the fencing token of the lease the gate is open under (0 for a
//     gate that is not a pair's lease). A command computed under one term
//     must not leave under another: a replica that lost the lease and won
//     it again has a new term, and what it queued before is stale.
//
// It is a duration, not an instant: the elector runs on its own clock, the
// sockets on the wall clock.
type SendGate func() (ok bool, budget time.Duration, term uint64)

// SendGated is a driver that checks a SendGate immediately before each
// send (one request, one datagram, one frame) and gives the send its
// budget as a deadline. `naut run` installs the pair's gate with it, after
// SetWriteGate: while one is installed, it is the gate the driver reads.
type SendGated interface {
	SetSendGate(SendGate)
}

// ClosedSendGate is a SendGate that is always closed.
func ClosedSendGate() (bool, time.Duration, uint64) { return false, 0, 0 }
