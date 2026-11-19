//go:build quorum_mut

package modbus

import "os"

// Mutations of the write path under a redundant pair (test plan §3.3),
// switched on like package quorum's (build tag quorum_mut,
// QUORUM_MUT=<name>):
//
//   - old_term_kept: a look that finds the gate open under another term of
//     the lease is not a reopen, so a write queued (or requeued) under the
//     earlier term still goes out (#1040 review).
var mutOn = os.Getenv("QUORUM_MUT")

func mut(name string) bool { return name == mutOn }
