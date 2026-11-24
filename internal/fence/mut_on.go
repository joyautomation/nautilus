//go:build quorum_mut

package fence

import "os"

// Mutations of the send fence (test plan §3.3), switched on like package
// quorum's (build tag quorum_mut, QUORUM_MUT=<name>):
//
//   - send_no_deadline: a send gets no deadline, and one whose budget has
//     run out still starts;
//   - fence_fin: a fenced socket is closed with a FIN, so what the kernel
//     still held is delivered late, instead of reset.
//   - fragments_unchecked: a send split into several requests (Write Tag
//     Fragmented) reads the gate once, not before each request.
//   - enter_after_read: a send is counted for a drain only after its gate
//     read (#710 d1b).
//   - drain_no_wait: Drain does not wait for the sends in flight.
//   - drain_no_reset: Drain cuts nothing short (no ResetAll, no reset at
//     arm, no context cancel, Started still true).
//   - drain_no_settle: Drain does not wait settle after the last send.
var mutOn = os.Getenv("QUORUM_MUT")

func mut(name string) bool { return name == mutOn }
