//go:build quorum_mut

package server

import "os"

// Mutations of the pair's operator-intent wiring (#710), switched on like
// package quorum's (build tag quorum_mut, QUORUM_MUT=<name>):
//
//   - ack_clear_off: an acknowledged alarm back to normal proposes no
//     ack-clear, so a takeover carries the ack to a new occurrence;
//   - alarm_view_ignored: a synced takeover restores the replica's own
//     retain instead of the log's alarm view.
var mutOn = os.Getenv("QUORUM_MUT")

func mutServer(name string) bool { return name == mutOn }
