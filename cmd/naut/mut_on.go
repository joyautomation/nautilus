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
var mutOn = os.Getenv("QUORUM_MUT")

func mutNaut(name string) bool { return name == mutOn }
