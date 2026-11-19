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
var mutOn = os.Getenv("QUORUM_MUT")

func mut(name string) bool { return name == mutOn }
