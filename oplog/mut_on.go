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
//   - log_sender_unbound: replication is taken from a connection whose
//     peer TLS certificate names another member than the request (#710);
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
//   - vouch_alone_partial: a vouched catch-up goes ahead alone while a
//     peer is silent, so a write a majority committed without this log is
//     numbered over (#1053 review);
//   - vouch_alone_ignores_ahead: a vouched catch-up goes ahead alone over a
//     longer copy that answers but doesn't count yet (a witness in its
//     quiet period), losing its entries (#1053 load run);
//   - vouch_refuses_counting_ahead: a vouch is refused for a later log on a
//     peer that counts, whose entries the catch-up would have taken;
//   - can_sync_stale: can-sync counts a copy last heard 10 s ago, not
//     within the caller's bound (a lease);
//   - placeholder_keeps_synced: a mark under the placeholder identity is
//     adopted as synced;
//   - unsynced_log_writes: a log that would sequence while unsynced reads
//     writable, so a write is proposed and ends "outcome unknown" instead
//     of being refused for certain.
//
// #1029 3c-3c-2a, accepting a loss and the log history:
//
//   - accept_loss_blanket: a vouched catch-up overrides a copy outside
//     what the person accepted (silent now, though it answered then; a
//     later log beyond the one accepted);
//   - accept_kept_after_sync: a log synced by the normal path keeps its
//     acceptance;
//   - history_not_bumped: a catch-up that overrides a copy starts no new
//     history, so the copy it overrode counts when it returns;
//   - history_after_write: the new history is recorded after the copy is
//     synced for its term, so a failed write leaves it synced at the old
//     one with its vouch spent;
//   - history_lower_counted: a copy at a lower history counts;
//   - history_fork_counted: a copy at the same history number under
//     another override's id (a fork) counts, and its replication is taken;
//   - fork_resolved_without_bump: a vouched catch-up that overrides an
//     accepted fork starts no new history, so the other side never moves;
//   - superseded_not_reset: a superseded copy keeps its log at its next
//     start (and replays it);
//   - superseded_takes: a superseded copy takes, commits and applies
//     replication before its restart;
//   - supersede_not_durable: a heard higher history is held in memory
//     only, so a restart replays the old history;
//   - history_moved_commits: a leader that hears a higher history while
//     replicating commits what it stored;
//   - lost_log_vouch_unchecked: a lost log is vouched for without
//     --accept-lost-log;
//   - synced_alone_without_accept: a synced copy alone among the copies
//     is granted the one-shot catch-up without --accept-loss;
//   - override_all_silent: a vouched catch-up overrides with every other
//     copy silent, leaving the new history on this copy alone;
//   - stale_under_lock: RetainedStale takes the node's lock, which a
//     commit's Apply holds while it takes the runtime's and the alarm
//     log's (#1177 review: a takeover deadlocks);
//   - vouch_again_noop: a vouch with its one-shot pending does nothing, so
//     "vouch again" after a refused catch-up can't change what was
//     accepted;
//   - noop_digest_unchecked: a vouch with nothing to accept takes any
//     digest;
//   - spend_failure_keeps_alone: a vouch's one-shot whose spend can't be
//     written stays armed in memory (#1177 CI: fail open);
//   - accept_survives_reboot: a vouch's one-shot and acceptance survive a
//     restart (a spend lost to a crash re-arms them, for a picture that may
//     no longer be true);
//   - vouch_keeps_term_synced: a vouch on a leader caught up for its term
//     waits for its next term to use the one-shot (a fork chosen there
//     stays a fork);
//   - empty_pair_needs_accept: a new pair's synced copy alone (every copy
//     answering, none lost, all at history 0, every log empty) still needs
//     --accept-loss, as after a controller's restart before its first
//     catch-up (#1029 3c-3c-2b, S2);
//   - set_aside_forgotten: a second identity change before a copy synced
//     forgets the first one's set-aside log (#1207 re-check);
//   - empty_pair_unchecked: the plain vouch for a new pair skips its four
//     conditions, so a copy silent, lost, at a history or holding entries
//     is overridden without --accept-loss.
//
// One sync per batch (#1455):
//
//   - batch_not_synced: a batch of entries is written but not synced
//     before the answer that acknowledges it;
//   - batch_kept_on_sync_failure: a batch whose write or sync failed stays
//     in memory, so the next answer counts entries the disk may not hold.
//
// Peer TLS (#710):
//
//   - peer_tls_log_plain: the log's calls ignore Options.TLS and go out
//     as plain HTTP, which a pair's TLS peer port refuses.
var mutOn = os.Getenv("QUORUM_MUT")

func mutOplog(name string) bool { return name == mutOn }
