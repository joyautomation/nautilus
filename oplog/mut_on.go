//go:build quorum_mut

package oplog

import "os"

// Mutations of the operator-intent log, switched on like package quorum's
// (build tag quorum_mut, QUORUM_MUT=<name>), each breaking one rule so its
// tests can show they notice:
//
//   - lapse_ignored: a committed KindLapse is not looked at, so a late
//     substitute start it names is carried out (#1030 review);
//   - lapse_majority_stored: a leftover stored on a majority (committed) is
//     named in a KindLapse too.
var mutOn = os.Getenv("QUORUM_MUT")

func mutOplog(name string) bool { return name == mutOn }
