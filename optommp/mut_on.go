//go:build quorum_mut

package optommp

import "os"

// Mutations of the output path under a redundant pair (test plan §3.3),
// switched on like package quorum's (build tag quorum_mut,
// QUORUM_MUT=<name>):
//
//   - old_term_kept: a look that finds the gate open under another term of
//     the lease is not a reopen, so a command queued under the earlier
//     term still goes out (#1040, 3b-3b).
var mutOn = os.Getenv("QUORUM_MUT")

func mut(name string) bool { return name == mutOn }
