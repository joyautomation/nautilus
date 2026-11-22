//go:build quorum_mut

package main

import "os"

// Mutations of the pair's wiring of its operator-intent log with the
// witness as the third replica (#710 PR 3b-4b), switched on like package
// quorum's (build tag quorum_mut, QUORUM_MUT=<name>):
//
//   - witness_log_unbound: the witness's log admits replication from any
//     member, not only the one its vote names;
//   - witness_log_term_unbound: it admits that member at any term;
//   - witness_log_any_incarnation: the leader counts the witness's log at
//     any incarnation, not only the one the election counts;
//   - witness_not_a_replica: the pair's log leaves the witness out;
//   - log_sync_not_retried: a leader whose term is not synced is caught up
//     only by its next write, never by the log's own drive.
//   - spare_log_runs: a spare's peer listener still answers (votes and the
//     log's replication), and its log drive still runs (#1029 3c-2).
//
// #1029 3c-3c-2a, a superseded operator-intent log:
//
//   - hello_history_ignored: a higher history in the other controller's
//     hello is not learnt;
//   - restart_while_leading, restart_peer_unchecked, restart_unsupervised,
//     restart_loops: a superseded controller restarts itself while it
//     leads, before the other leads synced at the new history, with no
//     supervisor, or again after a start that superseded (ruling 20d);
//   - witness_supersede_waits: the witness's copy waits for a restart;
//   - stale_retained_kept: a restart after a supersede leaves the old
//     history's substitutes in the retain store;
//   - superseded_lead_keeps_substitutes: a controller that learns it is
//     superseded keeps running the old history's substitutes until a
//     takeover.
var mutOn = os.Getenv("QUORUM_MUT")

func mutNaut(name string) bool { return name == mutOn }
