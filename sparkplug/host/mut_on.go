//go:build quorum_mut

package host

import "os"

// Mutations of the command path under a redundant pair (test plan §3.3),
// switched on like package quorum's (build tag quorum_mut,
// QUORUM_MUT=<name>):
//
//   - old_term_kept: a look that finds the lease under another term than
//     the last is not seen, so a command held or parked under the earlier
//     term is still published (#1040, 3b-3b).
var mutOn = os.Getenv("QUORUM_MUT")

func mut(name string) bool { return name == mutOn }
