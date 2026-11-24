//go:build quorum_mut

package opcua

import "os"

// Mutations of the write path under a redundant pair (test plan §3.3),
// switched on like package quorum's (build tag quorum_mut,
// QUORUM_MUT=<name>):
//
//   - old_term_kept: a look that finds the gate open under another term of
//     the lease is not a reopen, and a flush that took its queue before a
//     reopen still sends it (#1040, 3b-3b).
//   - isdu_write_unfenced: a method call that writes to a device
//     (CallFenced, an IO-Link parameter write) goes without the write
//     gate and uncounted by a switchover's drain (#710 d1c).
var mutOn = os.Getenv("QUORUM_MUT")

func mut(name string) bool { return name == mutOn }
