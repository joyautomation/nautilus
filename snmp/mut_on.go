//go:build quorum_mut

package snmp

import "os"

// Mutations of the command path (test plan §3.3), switched on like
// package quorum's (build tag quorum_mut, QUORUM_MUT=<name>):
//
//   - set_retried: a SET is retransmitted after each per-try timeout, so
//     an action-valued object runs twice when only the answer was lost
//     (#1040 review).
var mutOn = os.Getenv("QUORUM_MUT")

func mut(name string) bool { return name == mutOn }
