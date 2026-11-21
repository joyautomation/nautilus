//go:build quorum_mut

package server

import "os"

// Mutations of the pair's operator-intent wiring (#710), switched on like
// package quorum's (build tag quorum_mut, QUORUM_MUT=<name>):
//
//   - ack_clear_off: an acknowledged alarm back to normal proposes no
//     ack-clear, so a takeover carries the ack to a new occurrence;
//   - alarm_view_ignored: a synced takeover restores the replica's own
//     retain instead of the log's alarm view;
//   - engine_not_reset: a takeover restores the view over the engine's
//     frozen operator state instead of resetting it first;
//   - reconcile_before_restore: the log counts as live from the lease on,
//     before the takeover restored the view, so Reconcile clears every
//     carried ack against the unrestored engine (#1033 CI);
//   - cluster_ops_proxied: the pair's operator commands go through the
//     standby proxy like a write, so a standby that knows no leader
//     refuses them with a 503 (#1029 3c-3a);
//   - redundancy_same_origin: without an auth: block, the pair's operator
//     commands take the write rule (same-origin with no credential);
//   - retained_stale_ignored: a takeover restores a superseded log's
//     retained substitutes (#1029 3c-3c-2a);
//   - superseded_alarms_kept: a superseded log's acks and shelves are
//     carried by a takeover, and kept by a leader that learns it.
var mutOn = os.Getenv("QUORUM_MUT")

func mutServer(name string) bool { return name == mutOn }
