//go:build quorum_mut

package retain

import "os"

// Mutations of the retain store's side of a redundant pair, switched on
// like package quorum's (build tag quorum_mut, QUORUM_MUT=<name>):
//
//   - stream_carries_log_intent: Carry takes the leader's alarm acks,
//     shelves, flood and substitutes too, which a pair keeps in its
//     replicated log (#710 S1).
var mutOn = os.Getenv("QUORUM_MUT")

func mutRetain(name string) bool { return name == mutOn }
