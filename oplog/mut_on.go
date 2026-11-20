//go:build quorum_mut

package oplog

import "os"

// Mutations of the operator-intent log, switched on like package quorum's
// (build tag quorum_mut, QUORUM_MUT=<name>), each breaking one rule so its
// tests can show they notice:
//
//   - lapse_ignored: a committed KindLapse is not looked at, so a late
//     substitute start it names is carried out (#1030 review).
//
// Three replicas, the witness the third (#710 PR 3b-4a):
//
//   - replicate_sequential: the leader asks its peers one after another,
//     so a silent peer holds up every write and catch-up by its timeout;
//   - admit_skipped: a replica with Admit takes replication from anyone;
//   - unsynced_counted: an unsynced replica's answer counts;
//   - log_loss_silent: a log lost under the held mark comes back empty and
//     counting;
//   - synced_by_unsynced_leader: a follower marks itself synced from any
//     leader's replication, synced or not;
//   - resend_unchunked: a resend and a catch-up read carry the whole log
//     in one body.
//
// #1050's review:
//
//   - unsynced_leader_writes: a leader not caught up for its term sends
//     heartbeats and resends, and a follower takes its entries by pulling;
//   - behind_counted: a peer still short of the leader's log after the
//     resend rounds counts as having stored the entry;
//   - synced_before_commit: a follower marks itself synced from a request
//     that proves its log only below the leader's commit index;
//   - unsynced_self_counted: an unsynced leader counts its own log in its
//     catch-up;
//   - entry_unbounded: an entry of any size is proposed.
//
// #710 PR 3b-4b:
//
//   - term_not_persisted: the term lives only in memory, so a log lost
//     under the mark takes a deposed leader's replication (and its word
//     that the log is synced);
//   - unnamed_leader_admitted: a marked replica takes replication while
//     its own election names no leader;
//   - vouch_unchecked: a vouch is taken over a lost log, a peer that does
//     not answer, or a peer holding a later log;
//   - vouch_alone_kept: a vouched log catches up alone at every
//     leadership, not once;
//   - mark_after_log: a new replica's mark is written after its log file
//     is created, so a start that dies in between leaves a log that reads
//     as one from before marks;
//   - unsynced_log_writes: a log that would sequence while unsynced reads
//     writable, so a write is proposed and ends "outcome unknown" instead
//     of being refused for certain.
var mutOn = os.Getenv("QUORUM_MUT")

func mutOplog(name string) bool { return name == mutOn }
