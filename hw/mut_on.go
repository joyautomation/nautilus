//go:build quorum_mut

package hw

import "os"

// Mutations of the command path under a redundant pair (test plan §3.3),
// switched on like package quorum's (build tag quorum_mut,
// QUORUM_MUT=<name>):
//
//   - gate_per_batch: the gate is read once per batch, as before #710 PR
//     3b-3a, not before each command, and commands get no deadline.
//   - old_term_sent: a command queued under an earlier term of the lease
//     is sent after this replica wins it again (#1040 review).
var mutOn = os.Getenv("QUORUM_MUT")

func mut(name string) bool { return name == mutOn }
