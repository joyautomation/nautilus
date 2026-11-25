//go:build quorum_mut

package main

import (
	"os"
	"time"
)

// A bench build's slow disk (#710, the lost switchover campaign): with
// QUORUM_WRITE_DELAY set (a Go duration), every voter state write of this
// process waits that long before its fsync, the controller's and the
// witness's. Only in a quorum_mut build, never a release.
func init() {
	d, err := time.ParseDuration(os.Getenv("QUORUM_WRITE_DELAY"))
	if err != nil || d <= 0 {
		return
	}
	testStateWrite = func(f *os.File, b []byte) (int, error) {
		time.Sleep(d)
		return f.Write(b)
	}
}

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
//
// Peer TLS (#710):
//
//   - peer_tls_default_off: a pair member and the witness run plain HTTP
//     unless peer TLS is asked for;
//   - peer_bind_wildcard: a member's peer listener binds every interface,
//     not the address peers: names;
//   - peer_bind_gives_up: a peer port whose address is not up yet
//     refuses the start (#1230 review);
//   - peer_bind_loopback_taken: a peers: name that binds to loopback is
//     served (#1230 review).
//
// The state stream (#710 S1b):
//
//   - stream_leader_follows: a controller that leads runs the stream's
//     follower too;
//   - stream_persist_off: the standby never carries images into its
//     retain file (a takeover loads its own older values);
//   - stream_old_term_applied: the standby takes an image whatever its
//     term;
//   - stream_budget_drops: the budget never stretches the interval.
//   - stream_sends_unled: a leader's stream sends whether or not it leads (review);
//   - stream_flush_no_persist: the takeover's flush writes nothing (review);
//   - stream_flush_keeps_follower: the takeover's flush leaves the follower running (review);
//   - stream_flush_keeps_shadow: the takeover's flush keeps the shadow (review);
//   - stream_stale_marked_persisted: an image the retain file refused as older is shown as kept (review);
//   - stream_file_floor_unseeded: the standby's floor is not seeded from its retain file's term (review);
//   - stream_outlives_close: a closing member's stream handlers outlive it;
//   - stream_persist_error_hidden: a standby whose retain file takes no image still says in sync (review);
//   - lease_condition_dropped: a voter's lease refusal raises no condition (#710 1 s lease);
//   - witness_lease_no_alarm: the witness refusing the pair's lease raises no system alarm;
//   - lease_check_unwarned: naut check says nothing of a pair lease under 1 s;
//   - stream_format_ignored: the leader sends its newest image format whatever the standby reads (#710 S2a);
//   - hot_build_unchecked: a takeover goes hot on an image of another build (#710 S2b);
//   - hot_voters_unchecked: a takeover goes hot without the voters' check (#710 S2c-1);
//   - ack_old_epoch_counts: a leader takes its standby's acknowledgement of another epoch (#710 S2c-1);
//   - current_time_based: a standby counts as current by the time since its last acknowledgement, not by sequence (#710 S2c-1);
//   - current_without_fresh_send: a standby in step with the last image sent stays current however long ago it was sent (#710 S2c-1);
//   - interval_across_reconnect: the send-to-send time is measured across a reconnect's gap (#710 S2c-1);
//   - hot_cold_condition_never_clears: the cold-takeover warning stays after a standby follows again (#710 S2b);
//   - handback_long_deadline: the hand-back waits 2 s for the peer's answer, not a few round trips (#710 S2c-3);
//   - handback_ineligible_peer: a peer that could not lead answers good (#710 S2c-3);
//   - handback_peer_guard_off: a peer that handed back itself recently answers good (#710 S2c-3);
//   - handback_keeps_resign_hold: a resigner asked to take the lead back keeps its resign hold (#710 d1d);
//   - handback_empty_file_good: a peer's retain file with no stream position is good (#710 S2c-3);
//   - handback_peer_file_unjudged: a peer's retain file older than the voters' floor is good (#710 S2c-3);
//   - handback_taken_unalarmed: a takeover from the peer's hand-back raises no condition (#710 S2c-3);
//   - handback_restart_empty_trusted: a restart after a hand-back trusts the emptied retain file (#710 S2c-3, Fable M1);
//   - ack_other_build: a standby acknowledges an image of another build (#710 (b));
//   - peer_ready_ignores_forces: PEER_READY() is TRUE while the leader holds forces (#710 (b));
//   - ack_kept_across_reconnect: a leader keeps an acknowledgement across a new stream connection (#710 (b));
//   - final_image_unsent, capture_ignores_switching, final_ack_any_seq, switch_unready_target,
//     switch_ignores_standby_io, abort_keeps_final, final_waits_interval,
//     final_sent_once_per_connection, resign_unchecked: a planned switchover's stream and checks (#710 d1c, pairswitch.go);
var mutOn = os.Getenv("QUORUM_MUT")

func mutNaut(name string) bool { return name == mutOn }
