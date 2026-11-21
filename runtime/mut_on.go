//go:build quorum_mut

package runtime

import "os"

// Mutations of the runtime's side of a redundant pair, switched on like
// package quorum's (build tag quorum_mut, QUORUM_MUT=<name>):
//
//   - stale_retained_alarms_kept: a start after a superseded log keeps the
//     retain store's alarm acks and shelves (the old history's), so a
//     later unsynced takeover carries its shelves (#1177 review).
var mutOn = os.Getenv("QUORUM_MUT")

func mutRuntime(name string) bool { return name == mutOn }
