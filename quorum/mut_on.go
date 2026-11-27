//go:build quorum_mut

package quorum

import (
	"fmt"
	"os"
	"slices"
)

// Mutations (test plan §3.3): each breaks one rule of the vote protocol, so
// the suite can show it notices. Only a build with the quorum_mut tag has
// them, and then QUORUM_MUT names the one switched on. The runner
// (tools/quorum-mutants) checks that each turns its named tests red.
var Mutations = []string{
	"no_rho",                        // the leader's lease isn't shortened by ρ
	"restart_grants",                // a restarted voter grants at once (no restart wait)
	"answer_before_fsync",           // a voter's grant leaves before its state is durable
	"lease_from_reply",              // the lease is timed from the reply, not the send
	"no_backoff",                    // no back-off after a lapse or an unheard campaign
	"one_lease_backoff",             // a lapse backs off one lease, not two (#973)
	"any_incarnation",               // a witness grant counts from any incarnation
	"any_sender",                    // a grant counts from any sender
	"no_grantee_check",              // a grant meant for the other controller counts
	"no_nonce_check",                // a grant from an earlier boot counts
	"match_by_term",                 // a grant counts toward a request of the same term, not id
	"renewals_unseen",               // a standby doesn't see renewals its voter refused (#973)
	"no_reterm",                     // a refused leader never moves to a higher term (#973)
	"no_yield",                      // a voter never yields its own candidate's promise (#979)
	"no_observe",                    // a candidate doesn't see the peer's requested terms (#979)
	"retry_on_grid",                 // a refused campaign retries on the look grid (#979)
	"renew_on_look",                 // a lease won waits for the next look to renew (#998)
	"no_quiet",                      // a newly adopted witness incarnation counts at once (#976)
	"no_floor",                      // an adoption doesn't raise the term floor (#976)
	"count_while_pending",           // a proposer keeps counting the old incarnation (forward only)
	"no_adopt_gen",                  // a proposal is adopted whatever its generation
	"no_holdoff",                    // a switch never waits longer than a lease, however many came before
	"restart_counts_at_once",        // a restarted controller counts its adopted incarnation with no quiet period
	"no_adopt_echo",                 // an uncommissioned controller takes an Adopt sent before its restart
	"switch_without_peer",           // a switch is proposed with the peer unheard (it may be down)
	"peer_from_any_adopt",           // an Adopt to an earlier boot of this controller says the peer is up
	"bump_cuts_quiet",               // a generation bump that keeps the incarnation ends a quiet period still owed
	"quiet_reply_unheard",           // a reply in the quiet period doesn't count as heard: a restart backs off two leases
	"uncommissioned_grants",         // a controller that lost its file grants before it re-commissions (#976)
	"propose_keeps_commit",          // a proposal leaves an adoption waiting for its write to count later (#1014 review)
	"count_over_pending",            // a write counts an adoption while a proposal is pending (#1014 review)
	"holdoff_from_boot",             // a failed adoption write leaves the hold-off measured from boot (#1014 review)
	"uncommissioned_takes_switch",   // an uncommissioned controller adopts its peer's switch, not only its committed incarnation (two-of-three)
	"proposal_floor_volatile",       // a proposal's floor isn't durable with it: a restart re-sends it lower
	"both_lost_recommission",        // both controllers uncommissioned, the pair re-commissions on its own (#976's automatic recovery)
	"alarm_on_heard",                // an uncommissioned controller alarms although its peer's committed incarnation answered it
	"recommission_any_floor",        // an operator's floor at or below what the controller knows is taken
	"restart_wait_current_lease",    // a voter restarted with a shorter lease waits only that one (#1021 review)
	"lease_raise_volatile",          // a longer lease after a restart isn't written before an unchanged grant (#1021 re-review)
	"campaign_in_restart_wait",      // a controller campaigns while its own voter is in its restart wait (#1021 review)
	"relay_ignored",                 // a voter ignores the promises other voters relay (#1029 layer 2)
	"relay_voter_rule_off",          // a name is granted while its own voter is heard promised elsewhere (#1029 layer 2, rule (b))
	"relay_self_promise",            // a voter relays its promise to its own candidate (a clone's self-grant reaches the voters)
	"relay_no_margin",               // a heard promise is held only for its time left, without the clock-rate margin
	"relay_tail_counted",            // a refusal in the clock-rate margin after a heard promise counts as a duplicate
	"standby_no_probe",              // a standby doesn't probe the witness every look
	"relay_not_pushed",              // a voter's new promise to another node reaches the witness only at the next look
	"dup_name_ignored",              // a voter grants a name it hears under two live boots (#1029 step 1)
	"dup_refuses_incumbent",         // a duplicated name's running promise is refused too, so the incumbent's protecting promises lapse (#1029 step 1)
	"dup_voter_counts",              // a candidate counts a grant from a voter its own voter knows is duplicated (#1029 step 1)
	"dup_not_relayed",               // a voter doesn't relay a name it sees duplicated (#1029 step 1)
	"standby_probes_witness_only",   // a standby probes the witness only, not its peer (#1029 step 1)
	"yield_before_refusal",          // a voter yields its own round to a request it then refuses (#1079 review)
	"dup_strict_ignores_delay",      // a relayed promise counts a conflict as a duplicate up to receipt+left, so a fast restart on a slow link raises a false alarm
	"rule_b_counts_own_boot",        // rule (b) counts a duplicate for a relay from the very boot it refuses (a reordered frame: a false alarm)
	"same_term_other_boot",          // a voter grants a term it granted to one boot of a name to another boot of it (#1029 step 1)
	"grant_nonce_volatile",          // the grant's boot nonce is not reloaded at start, so a restarted voter gives a term to a second boot of a name (#1094 review)
	"grant_nonce_unwritten",         // a renewal under a standing promise from a format-4 file never writes the nonce, so it is lost at the next restart (#1094 re-check)
	"retry_after_refusal_only",      // a refused campaign whose own grant came back last waits out a lease (#1029 layer 2)
	"dup_mark_off",                  // a voter never marks a name it hears run as two boots at once (#1029 3c)
	"dup_mark_counts_replays",       // a duplicated or replayed frame of an old boot counts as that boot still running (#1029 3c)
	"dup_mark_no_delay",             // an old boot's frame within the one-way delay of the new boot's first marks the name (#1029 3c)
	"dup_mark_one_sided",            // the boot heard first sending past the delay marks the name, though the other never did (a voter that missed the old boot marks a restart, #1029 3c)
	"evicted_boot_banned",           // a boot evicted at the cap is ignored for good, so an original cut off while a copy restarted escapes the mark (#1113 review)
	"hold_released_by_any_boot",     // an unmarked reply from a restarted voter (which forgot its mark) releases a hold (#1113 review, rule (i))
	"held_voter_grants",             // a voter whose own candidate is held still makes new grants to the other node (#1113 review, rule (ii))
	"dup_mark_from_relay",           // a promise relayed to an old boot counts as that boot still running (#1029 3c)
	"marked_not_told",               // a voter's refusal doesn't tell a marked name it is marked (#1029 3c)
	"marked_still_campaigns",        // a candidate told its name is marked keeps campaigning (#1029 3c)
	"held_by_stale",                 // a voter names a boot the incumbent though it sent no new frame within half a lease (#1029 3c-2)
	"held_by_last_heard",            // a voter names the boot heard LAST the incumbent, not the first (#1029 3c-2)
	"held_by_before_frame",          // a refusal names the incumbent as of before the frame it answers (#1133 review)
	"held_by_names_to_unknown",      // a voter names the incumbent to a boot of the name it first heard after the mark (#1133 review)
	"spare_flag_kept_after_failure", // a spare flag whose write failed stays set in memory, for a later write to carry (#1133 review)
	"spare_one_voter",               // one other voter's word demotes a copy to a spare, not both (#1029 3c-2)
	"spare_own_voter_counts",        // a copy's own voter's word counts toward demoting it (#1029 3c-2)
	"spare_no_span",                 // a copy demotes on the first word, not one held for a lease (#1029 3c-2)
	"spare_while_leading",           // a copy holding a lease can demote itself (#1029 3c-2)
	"state_always_v6",               // every state file is written in format 6, a spare's or not (#1029 3c-2)
	"discount_reterms",              // a discounted grant re-terms the leader, every renewal while the mark stands (#1029 3c)
	"clear_keeps_mark",              // ClearDuplicate leaves the mark standing (#1029 3c)
	"dup_alarm_unconfirmed",         // two live boots count as a duplicate before they are confirmed (a fast restart's false alarm, #1029 3c)
	// The driver's (quorum/node, #710 PR 3a): switched on there, listed
	// here so this init accepts them.
	"gate_on_tick",               // the gate is judged on the elector's look, not when it is read
	"no_fence_margin",            // the gate closes at the lease's end, not a fence margin before it (design §3.4)
	"peer_host_trusted",          // the peer listener answers a trusted Host without the peer token
	"persisted_before_write",     // a write is reported durable before it is on disk
	"state_in_place",             // the state file is rewritten in place, so a crash tears it
	"nonce_fixed",                // the boot nonce is the same at every start
	"lease_unchecked",            // a voter grants a request whose lease outlives its own promise
	"witness_rebuilds_silently",  // the witness draws a new incarnation over a state file it can't read
	"controller_lease_unchecked", // a controller's voter grants a request whose lease outlives its own promise
	"controller_write_ignored",   // a controller's failed state write is reported durable
	"witness_write_ignored",      // the witness's failed state write is reported durable
	"gate_ignores_health",        // the gate stays open for unhealthy scans or after Release
	"controller_unlocked",        // a controller's state file isn't locked to one process
	"witness_unlocked",           // the witness's state directory isn't locked to one process
	"no_term_cap",                // the witness takes a vote that jumps the terms
	"controller_no_term_cap",     // a controller's voter takes a vote that jumps the terms
	"hello_unbound",              // the client-token MAC binds no names or direction: a reflected question passes
	"hello_retry_slow",           // node: a hello with no verified answer is retried only an interval later (#1120)
	"hello_not_asked_again",      // node: a peer restart or link-up asks for no hello (#1120)
	"hello_asks_unspaced",        // node: asks for a hello send them back to back (#1120 review)
	"creds_unbound",              // the credential check's MAC leaves the digest out, so differing users files read as the same (#708)
	"creds_names_unbound",        // the credential MAC leaves the asker and answerer out, so a reflected answer can pass (#1046 review)
	"creds_challenge_unbound",    // the credential MAC leaves the challenge out, so a replayed answer can pass (#1046 review)
	"term_cap_narrow",            // the term-jump cap is 2^20: an isolated controller crosses it in about 50 days and is refused for good
	"state_owner_unchecked",      // a controller starts over a state file another member (or another pair) wrote
	"config_differs_campaigns",   // a controller campaigns while its peer's hello names another configuration of the pair
	"hello_duplicate_unseen",     // two processes behind the peer's name (nonces N1, N2, N1 at the hello) raise nothing
	"box_unchecked",              // a controller votes from a state file written on another box (a cloned disk, #1029)
	"reply_relay_dropped",        // a controller's voter never hears the promise a reply relays (#1029 layer 2)
	"box_wait_skipped",           // a controller whose box can't be read yet votes on the stored identity (#1034 review)
	// Outside the quorum tree, listed so this init accepts them when a
	// package that imports quorum runs under one (the runner's pkgs rows).
	"lapse_ignored",             // oplog: a committed KindLapse is not looked at; a late start runs (#1030 review)
	"replicate_sequential",      // oplog: peers asked one after another; a silent one holds up every write (3b-4a)
	"admit_skipped",             // oplog: a replica with Admit takes replication from anyone (3b-4a)
	"unsynced_counted",          // oplog: an unsynced replica's answer counts (3b-4a)
	"log_loss_silent",           // oplog: a log lost under the held mark comes back empty and counting (3b-4a)
	"synced_by_unsynced_leader", // oplog: a follower marks itself synced from an unsynced leader (3b-4a)
	"resend_unchunked",          // oplog: a resend or catch-up read carries the whole log in one body (3b-4a)
	"unsynced_leader_writes",    // oplog: a leader not caught up for its term sends, and is pulled from (#1050 review)
	"behind_counted",            // oplog: a peer still behind after the resend rounds counts as stored (#1050 review)
	"synced_before_commit",      // oplog: a follower syncs from a request proving its log below the commit index (#1050 review)
	"unsynced_self_counted",     // oplog: an unsynced leader counts itself in its catch-up (#1050 review)
	"entry_unbounded",           // oplog: an entry of any size is proposed (#1050 review)
	"ack_clear_off",             // server: an acked alarm back to normal proposes no ack-clear (#710 PR 3b-2c)
	"alarm_view_ignored",        // server: a synced takeover restores its own retain, not the log's alarm view
	"engine_not_reset",          // server: a takeover restores the view over the engine's frozen operator state
	"reconcile_before_restore",  // server: the alarm log is live from the lease on, before the takeover restored it (#1033 CI)
	"gate_per_batch",            // hw: the gate is read once per batch, not before each command (design §3.4)
	"old_term_sent",             // hw: a command queued under an earlier term is sent after a re-lead (#1040 review)
	"old_term_kept",             // modbus: a write queued under an earlier term of the lease goes out after a re-lead (#1040 review)
	"set_retried",               // snmp: a SET is retransmitted after each per-try timeout (#1040 review)
	"send_no_deadline",          // fence: a send gets no deadline, and one past its budget still starts (design §3.4)
	"fence_fin",                 // fence: a fenced socket is closed with a FIN, not reset (design §3.4)
	"fragments_unchecked",       // fence: a send split into requests reads the gate once, not before each (3b-3b-2)
	// #710 PR 3b-4b: the witness as the log's third replica, wired.
	"campaign_ignores_sync",         // node: an unsynced standby campaigns over a synced peer that may (the preference)
	"unsynced_leader_keeps_lead",    // node: an unsynced, stuck leader never yields to a synced peer
	"yield_undamped",                // node: a leader yields again within yieldEvery
	"yield_ignores_may",             // node: a leader yields to a peer that may not campaign
	"yield_ignores_can_sync",        // node: a leader yields to a synced peer that can't sync the others
	"yield_to_marked_peer",          // node: a leader yields to a peer its own voter marked a confirmed duplicate
	"may_ignores_mark",              // node: a controller a voter marks a duplicate still says it may campaign
	"mark_alarm_from_cause",         // node: the confirmed alarm follows the last duplicate's cause, which a later count in one Handle overwrites, and is paced
	"vouch_refusal_blocks",          // node: a vouch the log refuses refuses the re-commission
	"vouch_alone_partial",           // oplog: a vouched catch-up goes ahead alone with a peer silent
	"vouch_alone_ignores_ahead",     // oplog: a vouched catch-up goes ahead alone over a longer copy that does not count yet
	"vouch_refuses_counting_ahead",  // oplog: a vouch is refused for a later log on a peer that counts
	"can_sync_stale",                // oplog: can-sync counts a copy last heard 10 s ago, not within a lease
	"placeholder_keeps_synced",      // oplog: a mark under the placeholder identity is adopted synced
	"yield_until_due",               // node: a yield ends at the lease's due end, though a late grant moved it
	"term_not_persisted",            // oplog: the term lives only in memory, so a lost log takes a deposed leader's word
	"unnamed_leader_admitted",       // oplog: a marked replica takes replication when its election names no leader
	"vouch_unchecked",               // oplog: a vouch is taken over a lost log, a silent peer or a later log
	"vouch_alone_kept",              // oplog: a vouched log catches up alone at every leadership, not once
	"unsynced_log_writes",           // oplog: an unsynced log reads writable, so a write ends "outcome unknown"
	"mark_after_log",                // oplog: a new replica's mark is written after its log file exists
	"witness_log_unbound",           // naut: the witness's log admits replication from any member
	"witness_log_term_unbound",      // naut: the witness's log admits its granted member at any term
	"witness_log_any_incarnation",   // naut: the leader counts the witness's log at any incarnation
	"witness_not_a_replica",         // naut: the pair's log leaves the witness out (2 of 2, as before 3b-4)
	"log_sync_not_retried",          // naut: a leader whose term is unsynced is caught up only by a write
	"spare_log_runs",                // naut: a spare's peer listener still answers and its log drive still runs (#1029 3c-2)
	"spare_flag_after_silence",      // node: a copy goes silent as a spare before its flag is durable (#1029 3c-2)
	"spare_answers_votes",           // node: a spare still answers the peer's votes, adopts and hellos (#1029 3c-2)
	"spare_still_looks",             // node: a spare still looks, probes and hellos its peer (#1029 3c-2)
	"peer_dial_lingers",             // node: a peer connection closes with a FIN, its unsent frames retransmitted after the process is gone (#1133 review)
	"spare_forgotten_on_box_change", // node: a spare's flag is read after the box check, which loses it on a copy's box (#1029 3c-2)
	"newer_state_moved_aside",       // node: a state file of a newer format is moved aside as lost, not refused (#1029 3c-2)
	// #1029 3c-3a: the operator's clear-duplicate command.
	"clear_per_member",          // quorum: the clear's check phase asks each member alone, not the members' boots together
	"clear_partial_check",       // node: the clear goes ahead with a member that did not answer the check phase
	"clear_done_ignores_clear",  // node: the clear reports done though a member missed the clear phase
	"clear_rerun_refused",       // node: a clear that missed a member can't run again once no member marks
	"clear_ignores_keep",        // node: a member clears though it hears a boot other than the one the check kept
	"clear_without_keep",        // node: the clear goes ahead when the check heard no boot of the name to keep
	"clear_retires_marked_only", // node: a member that marks nothing retires no boot on the clear
	"recheck_marks_only",        // node: the re-check counts only re-marks, not two boots heard across the members
	"clear_unsettled",           // quorum: a voter started under a lease and D ago answers the clear's check as if it heard every boot
	"clear_retires_own_only",    // quorum: each member retires only the boots it knew, not every member's
	"clear_forgets_boots",       // quorum: the clear retires no boot, so a copy unheard at the clear comes back unmarked (#1135 review)
	"cluster_ops_proxied",       // server: the pair's operator commands go through the standby proxy (503 with no leader)
	"redundancy_same_origin",    // server: without an auth: block, the pair's operator commands take the same-origin write rule
	// #1029 3c-3b: durable marks.
	"marks_volatile",           // quorum: the duplicate records are never written: a restart forgets every mark (today's 3c-3a)
	"mark_not_restored",        // quorum: a restart reads the records but marks nothing from them
	"restored_boot_fresh",      // quorum: a restored boot counts as just heard, so HeldBy names a dead incumbent after a restart
	"retired_not_restored",     // quorum: a restart forgets the boots an operator's clear retired
	"mark_write_on_grant_path", // quorum: a grant waits for the mark's write too, so a failing one drops the incumbent's renewals
	"retired_reply_counted",    // quorum: a reply from a retired boot counts and marks nothing (#1135's residual)
	"release_ignores_gen",      // quorum: a hold ends only on the marking boot's word (3c-1's rule (i) alone), never on a clear's generation
	"clear_gen_not_bumped",     // quorum: a clear leaves the name's clear generation as it was
	"spare_failure_unwritten",  // quorum: a failed spare write takes the flag back in memory only, leaving a write in flight that carries it (#1144 review)
	"retired_reply_unmatched",  // quorum: a retired boot's reply marks before it is matched to a round (late or replayed ones too, #1144 review)
	"done_ignores_held",        // node: the clear reports done though the held controller is still held at the re-check (#1144 re-check)
	"hold_gen_ignores_seen",    // quorum: HoldGen covers the holds only, not the generations seen, so a hold set between the clear's check and its clear outlives it (#1144 re-check)
	"hold_gen_reset_on_boot",   // quorum: a hold's generation starts from the reply alone, below what this candidate already saw (#1144 review)
	"clear_ignores_held",       // node/sim: the clear's check counts a held controller no member marks as nothing to clear (#1144 review)
	"clear_gen_ignores_holds",  // node/sim: the clear's generation is 1, not above every member's and every hold's
	// #1029 3c-3c: commissioning and the spare's way back.
	"commission_unchecked",           // node: commission goes ahead on a pair a member says was commissioned or used terms
	"recommission_local_floor_only",  // node: recommission's floor is checked against this controller's terms only, not the peer's and the witness's
	"box_replaced_unchecked",         // node: a box-changed controller is re-commissioned without the operator's --box-replaced
	"from_spare_not_spare",           // node: --from-spare runs on a controller that is not a spare
	"from_spare_own_view",            // node: --from-spare checks only its own (silent) voter, not the other two
	"from_spare_one_voter",           // node: --from-spare checks the witness only, not the peer's voter too
	"from_spare_write_after_move",    // node: a write after --from-spare's move puts the spare's state file back
	"uncommissioned_looks_unpaced",   // node: an uncommissioned controller looks whenever a frame pokes it: two ping-pong at network speed
	"from_spare_peer_unchecked",      // node: --from-spare runs with the other controller uncommissioned, moving aside what may be the pair's only terms
	"from_spare_clears_past_refusal", // node: --from-spare asks the witness to clear after the peer refused
	"from_spare_moves_after_partial", // node: --from-spare moves the spare's file aside though a voter missed the clear
	"fence_requests_off",             // a voter takes a request from a fenced state as any other (#1029 3c-3c-1b)
	"fence_replies_off",              // a controller counts a reply from a fenced state's voter, relay and all
	"fence_witness_unset",            // the witness takes no fence: only the peer refuses the replaced member's old state
	"fence_peer_own_gen_only",        // the peer's fence ignores its pending proposal: a receiver that adopted it first (its Ack lost) is above the fence
	"fence_adopt_off",                // a controller takes handshake messages from a fenced peer: an old box re-proposes over the fence
	"fence_volatile",                 // a restarted voter forgets its fences
	"fence_unnoted",                  // a controller never learns its state is fenced: no alarm, and it campaigns on
	"fenced_still_campaigns",         // a fenced controller campaigns each look, moving its own voter's terms in vain
	"fence_hold_ignores_witness",     // a controller one voter fenced campaigns no more though the witness answers it unfenced: an incomplete --from-spare leaves no leader once the peer dies
	"propose_any_echo",               // a commissioned controller takes a proposal addressed to another boot of the peer
	"propose_resent_at_look",         // a proposal refused for its echo waits a look before it goes again
	"fence_plus_one",                 // the fence one above the peer's own adoption: an old disk that died re-adopting holds a proposal at it (#1164 review, B3)
	"fence_ignores_previous",         // a second --from-spare's fence ignores the first: the first replacement, dead re-adopting at it, passes
	"fenced_propose_answered",        // the peer answers (or acks) a proposal below the fence from an uncommissioned old disk
	"fence_unproposed",               // a replacement re-adopts at the peer's generation + 1, ignoring the fence it was told: it is fenced, no leader
	"fence_not_learned",              // a voter that lost its state never learns the fences: a wiped peer grants the old box (#1164 review, B1)
	"fence_hides_lookalike",          // a fenced voter hears no boot of an uncommissioned sender: a wiped old box is never marked beside the replacement (#1164 re-check, N1)
	"fence_learns_any_name",          // a voter learns a fence on any name a frame carries, the witness's included
	"fence_takes_carried",            // a controller takes the carried fence value, which it can't check, and raises the name's fence for good
	"recommission_under_fence",       // node: a commission or re-commission adopts below a member's fence, which then refuses the pair
	"fence_any_name",                 // node: a member takes a fence on any name, the witness's or its own included
	"dup_answered_before_write",      // node: a clear or fence is answered before its write is durable: a crash then loses what the command reported done
	"dup_wait_lease_scaled",          // node: a member waits for a clear's or a fence's write only its share of the lease (125 ms on a 500 ms witness): a slow disk ends the command incomplete (#1329)
	// #1029 3c-3c-2a: accepting a loss, and the log history.
	"accept_loss_blanket",               // oplog: a vouched catch-up overrides a copy outside what the person accepted
	"accept_kept_after_sync",            // oplog: a log synced by the normal path keeps its acceptance
	"history_not_bumped",                // oplog: an override starts no new history: the overridden copy counts when it returns
	"history_after_write",               // oplog: the new history is recorded after the copy is synced for its term
	"history_lower_counted",             // oplog: a copy at a lower history counts
	"history_fork_counted",              // oplog: a forked copy (same number, another id) counts and is replicated to
	"fork_resolved_without_bump",        // oplog: a catch-up overriding an accepted fork starts no new history
	"superseded_not_reset",              // oplog: a superseded copy keeps (and replays) its log at its next start
	"superseded_takes",                  // oplog: a superseded copy takes and applies replication before its restart
	"supersede_not_durable",             // oplog: a heard higher history is held in memory only
	"history_moved_commits",             // oplog: a leader that hears a higher history while replicating commits what it stored
	"lost_log_vouch_unchecked",          // oplog: a lost log is vouched for without --accept-lost-log
	"synced_alone_without_accept",       // oplog: a synced copy alone is granted the one-shot without --accept-loss
	"override_all_silent",               // oplog: a vouched catch-up overrides with every other copy silent, the new history on one copy alone
	"stale_under_lock",                  // oplog: RetainedStale takes the node's lock: a takeover and a commit's Apply wait on each other (#1177 review)
	"vouch_keeps_term_synced",           // oplog: #1177 review
	"spend_failure_keeps_alone",         // oplog: a vouch's one-shot whose spend can't be written stays armed in memory (#1177 CI)
	"accept_survives_reboot",            // oplog: a vouch's one-shot and acceptance survive a restart (#1177, naut-lead)
	"stale_retained_alarms_kept",        // runtime: a start after a superseded log keeps the retain store's acks and shelves (#1177 review)
	"takeover_keeps_first_scan",         // runtime: a takeover resets the frames but not FIRST_SCAN (#710)
	"image_copy_in_scan_encodes",        // runtime: the scan's frame copy also hashes the layout (guard a: the scan copies values only, #710 S1)
	"image_seq_from_scans",              // runtime: an image is numbered by its program's scan count, which an online edit resets (#710 S1)
	"image_crc_unchecked",               // runtime: a state image's checksum is neither written nor checked (#710 S1)
	"shadow_older_image_applied",        // runtime: the standby's shadow applies an image of an older (term, seq) (#710 S1)
	"shadow_stale_frame_kept",           // runtime: a task an image did not apply keeps offering the older frame (#710 S1)
	"image_size_unbounded",              // runtime: images over the size limits are encoded and decoded (#710 S1)
	"shadow_layout_unchecked",           // runtime: the shadow applies a frame to a program of another layout (#710 S1)
	"shadow_kept_after_leading",         // runtime: a reset (this controller led) keeps the shadow's frames and position (#710 S1)
	"stream_carries_log_intent",         // retain: Carry takes the leader's acks, shelves, flood and substitutes, which the log owns (#710 S1)
	"noop_digest_unchecked",             // oplog: #1177 review
	"vouch_again_noop",                  // oplog: #1177 review
	"lower_history_peer_synced",         // node: a peer's log at a lower or forked history is one to defer and yield to
	"hello_history_ignored",             // naut: a higher history in the other controller's hello is not learnt
	"restart_while_leading",             // naut: a superseded controller restarts itself while it leads
	"restart_peer_unchecked",            // naut: a superseded controller restarts itself though the other does not lead synced at the new history
	"restart_unsupervised",              // naut: a superseded controller restarts itself with no supervisor to start it again
	"restart_loops",                     // naut: a controller restarts itself again for a second superseded log after one at its start
	"witness_supersede_waits",           // naut: the witness's copy waits for a restart to take a newer history, as a controller's
	"stale_retained_kept",               // naut: a superseded log's substitutes stay in the retain store after its restart
	"superseded_lead_keeps_substitutes", // naut: a controller that learns it is superseded keeps running the old history's substitutes until a takeover
	"retained_stale_ignored",            // server: a takeover restores a superseded log's retained substitutes
	"superseded_alarms_kept",            // server: a superseded log's acks and shelves are carried by a takeover
	"contacts_part_users",               // node: the alarm contacts' credMAC takes the users part's label, so a users MAC verifies as contacts (#715)
	"occurrence_per_activation",         // alarm: a flap through unack-rtn draws a new occurrence (#715)
	"ack_any_occurrence",                // alarm: an ack naming another occurrence is applied (#715)
	"carry_on_false",                    // alarm: a carried occurrence continues after a false reading past the grace (#715)
	"carry_ack_after_false",             // alarm: a carried ack rides a false reading inside the grace (#715)
	"carry_unbounded",                   // alarm: an unreadable condition holds a carried occurrence forever (#715)
	"carry_through_enable",              // alarm: enable false does not end a carried occurrence (#715)
	"carry_wait_reset",                  // alarm: a carried occurrence's wait restarts, so escalation restarts at level 1 (#715)
	"chatter_never",                     // alarm: no activation ever chatters (#1249)
	"chatter_no_merge",                  // alarm: an overlapping window does not continue a chatter episode (#1249)
	"flood_no_hysteresis",               // alarm: a flood ends the moment the count is at the threshold (#1249)
	"grace_counts",                      // alarm: activations inside a standalone start's grace count toward chatter and floods (#1249)
	"restart_counts",                    // alarm: a condition that stood through a re-baseline counts when it activates (#1249)
	"first_out_repeats",                 // alarm: a flood's first out names one alarm as often as it activated (#1249)
	"clock_step_unhandled",              // alarm: a clock stepped back leaves detection times in the future (#1249)
	"standing_sticks",                   // alarm: a false reading does not end a standing condition, so its next activation is not counted (#1249)
	"unsuppress_always_rebaselines",     // alarm: a return from suppression re-baselines a condition that read false before it (#1249)
	"standing_unmarked",                 // alarm: an activation detection did not count is journalled unmarked, so the reports count it (#1249)
	"seen_when_unmarked",                // alarm: an activation found as a site or equipment came back reads as if it began then (#1249)
	"carry_detect_unclamped",            // alarm: a carried chatter or flood time ahead of the clock is kept, so the episode outlives a window after the restore (#1249)
	"takeover_drops_episodes",           // server: a synced takeover carries no flood or chattering episode (#1249)
	"episodes_not_synced",               // server: the leader proposes no alarm-detect entry (#1249)
	"episodes_stamped_now",              // server: a takeover dates the view's episodes now, so a cold start shows hours-old ones as live (#1249 review)
	"carried_chatter_splits",            // alarm: a carried chatter episode ends and starts again while its empty ring refills (#1249 review)
	"flood_end_any",                     // server: a flood end naming another flood ends the view's (#1249)
	"chatter_pages_per_bounce",          // escalate: a chattering alarm's bounce clocks are not held, so each bounce pages (#1249 slice 3)
	"chatter_pin_latched",               // escalate: a latched wait that began with the episode is pinned and loses its own clock (#1249 slice 3)
	"chatter_clock_per_wait",            // escalate: the pinned clock ends with the wait, so a bounce through normal sends the notice again (#1249 slice 3)
	"regroup_unbounded",                 // escalate: a policy step's grouped pages are not spaced (#1249 slice 3)
	"group_covers_on_failure",           // escalate: a grouped page that failed covers its alarms, so nobody hears of them (#1249 slice 3)
	"group_individual_ignored",          // escalate: critical and safety alarms are grouped in a flood (#1249 slice 3)
	"notice_carries_ack",                // escalate: a chatter notice carries an ack link and code (#1249 slice 3)
	"takeover_resend_individual",        // escalate: a takeover's first look under a flood resends each covered alarm on its own (#1249 slice 3)
	"chatter_policy_at_start",           // escalate: the pinned clock keeps the policy at the episode's start, missing a when: window (#1249 review)
	"chatter_joins_late",                // escalate: the pinned clock skips to the latest due level, so a level 1 no bounce reached is never sent (#1249 review)
	"chatter_end_burst",                 // escalate: after a chatter its own clock resends every level the notices sent (#1249 review)
	"chatter_end_skips_new",             // escalate: an activation after the chatter ended skips its due levels (#1249 review)
	"group_drop_lost",                   // escalate: a grouped send the queue could not take is lost once its batch is delivered (#1249 review)
	"released_batch_floors",             // escalate: a grouped page released before it left holds the next back 5 minutes (#1249 review)
	"safety_case_exact",                 // escalate: only the class spelled "safety" pages on its own in a flood (#1249 review)
	"chatter_repeats_burst",             // escalate: a pinned clock that starts or moves late sends every due repeat at once (#1249 re-check)
	"ack_clear_keeps_occurrence",        // server: an ack-clear leaves the ended occurrence in the view (#715)
	"takeover_drops_occurrences",        // server: a synced takeover continues no occurrence (#715)
	"ack_entry_unbound",                 // server: a pair's ack entry names no occurrence (#715)
	"apply_ack_any_occurrence",          // server: a committed ack applies to whatever occurrence is on (#715)
	"carry_end_silent",                  // alarm: an acknowledged carried occurrence that ends leaves no journal note (#715)
	"adopt_off",                         // alarm: a committed ack never adopts the occurrence that stood through an unsynced takeover (#715)
	"adopt_ignores_log",                 // server: a late ack adopts the standing occurrence though the log shows its own ended (#715)
	"carry_unclamped",                   // alarm: a carried or adopted time ahead of the clock is kept, so a wait in the future pages nobody (#715)
	"adopt_after_suppress",              // alarm: an alarm suppressed since the restore may still adopt a late committed ack (#715)
	"carry_ack_resaved_after_false",     // alarm: a held carry re-saves its ack after a false reading (#715)
	"adopt_under_mu",                    // server: Reconcile asks the oplog node under AlarmLog.mu, inverting the apply's lock order (#715)
	"engine_event_waits",                // server: the engine's hook takes AlarmLog.mu on the scan instead of handing off (#715, #1053)
	"clear_any_later_ack",               // server: a late hand-off clears an ack of a later occurrence (#715)
	"clear_ack_taken_after",             // server: a late hand-off clears an ack the view took after its event (#715)
	"engine_event_blocks",               // server: the engine's hook blocks when the hand-off is full (#715, #1053)
	"clear_while_engine_holds",          // server: a late hand-off clears an ack the engine holds (#715)
	"clear_without_livemu",              // server: a late hand-off is judged without waiting out an in-flight Apply (#715)
	"reconcile_without_livemu",          // server: Reconcile reads the engine's acks and the view's without liveMu (#715)
	"ack_clear_any_occurrence",          // server: an ack-clear naming one occurrence removes another occurrence's ack (#715)
	"engine_event_takes_livemu",         // server: the engine's hook waits on liveMu on the scan (#715, #1053)
	"occurrences_not_synced",            // server: the leader proposes no occurrence-start or -end (#715)
	"link_replay_applied",               // server: a committed ack naming a spent ack link is applied again (#715)
	"link_spent_unchecked",              // server: an ack link already used is not refused before it is proposed or applied (#715)
	"link_ignores_standing",             // server: an ack link acts for a user who is not enabled or not in the users file (#715)
	"link_sign_in_ignored",              // server: ack-from-notification: sign-in lets the link alone acknowledge (#715)
	"link_needs_write_rule",             // server: an ack link without an auth: block needs NAUTILUS_TOKEN too (#715)
	"link_not_forwarded",                // server: a standby answers an ack link itself instead of forwarding it to the leader (#715)
	"leader_headers_unbounded",          // server: a standby's forward waits for the leader's response headers without a deadline (#710)
	"link_reason_html",                  // server: a refused ack link is journalled with its page's HTML as the reason (#715)
	"link_acks_on_standby",              // server: a replica that does not lead acknowledges an ack link on its own engine (#715)
	"link_session_cookie_refused",       // server: a signed-in browser from another origin is refused on a link in link mode instead of the link alone acting (#715)
	"inbound_unsigned",                  // server: a provider's reply or keypress is taken without its signature checking (#715)
	"inbound_tries_unlimited",           // server: a person who sent too many wrong reply codes is not refused (#715)
	"inbound_callee_unchecked",          // server: a keypress acknowledges though the call's number is not the person's (#715)
	"inbound_any_key",                   // server: a keypress other than 1 acknowledges (#715)
	"inbound_status_on_wire",            // server: a refused reply or keypress answers the provider with its status instead of 200 TwiML (#715)
	"inbound_not_forwarded",             // server: a standby answers a provider's reply itself instead of forwarding it to the leader (#715)
	"voice_link_acks_here",              // server: a call's keypress URL acknowledges as an ack link (#715)
	"inbound_replies_unlimited",         // server: a person is texted every refusal of their replies, without limit (#715)
	"inbound_no_global_budget",          // server: the controller-wide budget of wrong reply codes is not enforced (#715)
	"inbound_any_alarm",                 // server: a reply code is tried against every standing occurrence, not only those whose escalation can page the person by SMS (#715)
	"inbound_unknown_as_refused",        // server: an inbound ack the pair's log did not confirm is journalled as refused (#715)
	"sms_ack_offered_while_suspended",   // server: an SMS page offers a reply code while SMS acknowledgements are suspended (#715)
	"inbound_stale_code_counted",        // server: a good code for an occurrence acknowledged since counts as a wrong code (#715)
	"adopt_probe_unseq",                 // an uncommissioned boot's adoption probe carries no Seq, so no voter notes its boot (#1029 3c-3c-2b, D1)
	"adopt_probe_witness_only",          // an uncommissioned boot's adoption probe goes to the witness only, not the peer (#1029 3c-3c-2b, D1)
	"peer_takes_beside_live",            // a commissioned controller takes an uncommissioned peer boot's re-adoption while its voter hears another live boot of the name (#1029 3c-3c-2b, D1)
	"recommission_beside_live",          // node: recommission goes ahead while a voter hears another live boot of this controller's name (#1029 3c-3c-2b)
	"live_check_witness_skipped",        // node: recommission's live-copy check skips a witness that does not answer it (#1207 review)
	"empty_pair_needs_accept",           // oplog: a new pair's vouch after a restart needs --accept-loss though no copy holds anything (#1029 3c-3c-2b, S2)
	"set_aside_forgotten",               // oplog: a second identity change before a copy synced forgets the first one's set-aside log (#1207 re-check)
	"empty_pair_unchecked",              // oplog: the plain vouch for a new pair skips its four conditions (#1029 3c-3c-2b, S2)
	"peer_tls_short_token",              // peertls: a peer token under 32 characters keys peer TLS (#710)
	"peer_tls_server_any_client",        // peertls: a peer listener asks for no client certificate (#710)
	"peer_tls_client_any_server",        // peertls: a peer call accepts any server key (#710)
	"pin_accepts_changed_key",           // peertls: a pinned name's other key is accepted (#710 per-box keys)
	"pin_unsigned_accepted",             // peertls: a certificate the token's key did not sign is accepted (#710 per-box keys)
	"pin_any_name",                      // peertls: a certificate naming another member than the one dialed, or one a listener takes no calls from, is accepted (#710 per-box keys)
	"pins_unreadable_silent",            // peertls: an unreadable pins file is read as none, unreported and not moved aside (#710 per-box keys)
	"pin_checked_once",                  // peertls: the pin is checked at a name's first contact only, not on every request (#710 per-box keys)
	"box_key_unbound",                   // peertls: a key file written on another box is used (#710 per-box keys)
	"key_on_unknown_box",                // peertls: a box key is made while the box is not known yet (#710 per-box keys)
	"key_read_error_remakes",            // peertls: a key file that exists but can't be read is replaced by a new key (#710 per-box keys, Fable)
	"key_newer_version_remakes",         // peertls: a key file a newer release wrote is replaced (a downgrade destroys the box's identity) (#710 per-box keys, Fable)
	"key_no_nics_other_box",             // peertls: a box whose NICs can't be read yet counts as another box (#710 per-box keys, Fable)
	"drop_key_repins",                   // peertls: a dropped pin's key is pinned again by a connection kept from before the drop (#710 per-box keys, Fable)
	"refusals_undeduped",                // peertls: every refusal of one key is an event, so a replayed certificate floods the journal (#710 per-box keys, Fable)
	"witness_cert_one_inc",              // peertls: the witness's certificate names witness@0 whatever pair calls (#710 2b)
	"witness_names_uncapped",            // peertls: a controller pins any number of witness incarnations the pair does not count (#710 2b)
	"witness_call_any_inc",              // peertls: a call to the witness that is not a vote takes any incarnation's certificate (#710 2b)
	"drop_ban_in_memory",                // peertls: a dropped pin's key is refused only until a restart, not in the pins file (#710 2b)
	"adopt_keeps_witness_pins",          // peertls: an adoption keeps the pins of the witness incarnations the pair no longer counts (#710 2b)
	"reply_inc_unbound",                 // node: a witness vote reply counts whatever incarnation its certificate names (#710 2b)
	"witness_ops_any_inc",               // node: a witness answer to a call that is not a vote counts under any incarnation's certificate (#710 2b)
	"bound_reply_keeps_conn",            // node: a reply refused by its certificate keeps its connection, so a pair's first vote's witness@0 stays (#710 2b)
	"unbound_refusal_keeps_conn",        // node: a witness refusal under an uncounted certificate keeps its connection, so the refusal is never heard under the right one (#1120)
	"unbound_close_uncapped",            // node: an unbound witness refusal closes the shared connections on every vote, not once a lease (#1384 review)
	"unbound_close_on_no_answer",        // node: a witness call with no answer (a timeout) closes the shared connections (#1384 review)
	"unadopted_reply_heard",             // node: a reply from a witness incarnation the controller has not adopted counts in full (marks, fences, held) (#710 2b, Fable)
	"witness_refusals_unbound",          // node: a witness refusal or reply under an incarnation the pair does not count raises and clears conditions (#710 2b, Fable)
	"witness_any_new_key",               // peertls: a commissioned controller pins a new witness incarnation under any key (#710 2b B')
	"unpin_pending_volatile",            // peertls: an unpin's unconfirmed witness leg is not recorded, so a retry may lift the refusal (#710 2b, Fable)
	"witness_key_forgotten",             // peertls: the counted witness key is not kept across adoptions, so a name adopted on the peer's proposal pins any key (#710 2b, Fable re-check)
	"unknown_key_any_name",              // peertls: with the counted witness key unknown, any incarnation pins at first contact on a commissioned controller (#710 2b)
	"allowance_any_key",                 // peertls: naut redundancy unpin witness allows whatever new key arrives next, not the one waiting (#710 2b, Fable re-check)
	"allowance_kept_at_adoption",        // peertls: an allowance of a new witness key outlives an adoption (#710 2b, Fable re-check)
	"allowance_cleared_unheard",         // peertls: an adoption of an incarnation not pinned here ends an allowance its key still needs (#710 2b)
	"retry_skips_local_drop",            // naut: an unpin retry finishes only the witness's leg, not a local drop the first run never made (#710 2b, Fable re-check)
	"lift_here_first",                   // naut: a second unpin lifts the refusal here before the witness's, so a failed witness lift leaves them apart (#710 2b, Fable re-check)
	"key_box_virtual_nics",              // naut: a virtual interface's MAC (docker, a bridge, a veth) binds the peer key (#710 per-box keys)
	"pin_journal_unbuffered",            // naut: peer key events from before the journal is attached are lost (#710 per-box keys)
	"unpin_self",                        // naut: naut redundancy unpin of this controller's own name is taken (#710 2b)
	"unpin_lifts_on_retry",              // naut: an unpin retried after an unreached witness lifts the refusal of the old key (#710 2b)
	"unpin_other_cluster",               // naut: the witness takes an unpin from a controller of another cluster (#710 2b)
	"witness_unpin_self",                // naut: the witness takes a controller's unpin of its own name (#710 2b)
	"unpin_newer_caller",                // naut: the witness takes an unpin from a controller pinned after the one it drops (#710 2b)
	"unpin_ignores_key",                 // naut: the witness drops a pin whatever key the asking controller drops (#710 2b)
	"log_witness_any_inc",               // naut: the operator-intent log reaches any incarnation of the witness (#710 2b)
	"log_witness_checked_at_dial",       // naut: the log's witness connection checks the adopted incarnation at its dial only (#710 2b, Fable)
	"sender_unbound_vote",               // node: a controller's voter takes a vote naming another member than its certificate (#710 per-box keys)
	"sender_unbound_adopt",              // node: a controller takes an adopt naming another member than its certificate (#710 per-box keys)
	"sender_unbound_hello",              // node: a controller answers a hello naming another member than its certificate (#710 per-box keys)
	"sender_unbound_dup",                // node: a controller takes a check, clear or fence from another member than its peer by certificate (#710 per-box keys)
	"witness_sender_unbound_vote",       // node: the witness takes a vote naming another member, or cluster, than its certificate (#710 per-box keys)
	"witness_sender_unbound_dup",        // node: the witness takes a check, clear or fence from outside the cluster by certificate (#710 per-box keys)
	"stream_sender_unbound",             // naut: the state stream, ack, hand-back and switch-ask are taken from another member than the peer by certificate (#710 per-box keys)
	"witness_log_cluster_unbound",       // naut: the witness's log of a cluster answers a member of another cluster (#710 per-box keys)
	"log_sender_unbound",                // oplog: replication naming another member than its certificate is taken (#710 per-box keys)
	"handback_no_standdown_wait",        // naut: a hand-back's judge refuses while its runtime has not yet stood down from a lead its elector no longer holds (#710 per-box keys, test race)
	"peer_tls_default_off",              // naut: a pair and a witness run plain HTTP unless peer TLS is asked for (#710)
	"peer_tls_log_plain",                // oplog: the log's calls ignore peer TLS and go out plain (#710)
	"peer_bind_gives_up",                // naut: a peer port whose address is not up yet refuses the start (#1230 review)
	"peer_bind_loopback_taken",          // naut: a peers: name that binds to loopback is served (#1230 review)
	"peer_bind_wildcard",                // naut: a pair member's peer listener binds every interface, not the address peers: names (#710)
	"peer_tls_handshake_unbounded",      // node: a peer TLS handshake has no bound of its own (#1230 review)
	"peer_tls_mismatch_unnamed",         // node: a peer that speaks the other protocol, or holds another key, raises nothing (#710)
	"peer_api_guessed",                  // node: a standby names the leader's tag API by its own API port (#710 demo finding 1)
	"peer_api_unbound",                  // node: the announced tag API is outside the hello's InfoMAC
	"peer_api_self",                     // node: a peer announcing this controller's own tag API is proxied to
	"asked_unrecorded",                  // quorum: a voter records no term a peer asked at, so a standby takes no image (#710 S1b)
	"stream_leader_unseen",              // quorum: a standby takes an image with no live lease of the peer seen (#710 S1b)
	"stream_floor_unchecked",            // quorum: a standby takes an image of a term it led itself, or below one it holds (#710 S1b)
	"stream_term_unbounded",             // quorum: a standby takes an image of a term the peer never asked at (#710 S1b)
	"stream_takeover_no_flush",          // runtime: a takeover reads the retain store without flushing the newest state image first (#710 S1b)
	"stream_leader_carries",             // runtime: a controller that leads still carries an image into its retain store (#710 S1b)
	"stream_old_image_carried",          // runtime: a standby carries an image older than the position its retain store records (#710 S1b)
	"stream_leader_saves_unmarked",      // runtime: a pair leader's saves do not record its term in the retain store (#710 S1b)
	"stream_stale_capture_sent",         // runtime: a frame copy made before a takeover survives its reset, for the new leadership's stream (#710 S1b)
	"stream_leader_follows",             // naut: a controller that leads runs the stream's follower too (#710 S1b)
	"stream_persist_off",                // naut: the standby never carries images into its retain file (#710 S1b)
	"stream_old_term_applied",           // naut: the standby takes an image whatever its term (#710 S1b)
	"stream_budget_drops",               // naut: the stream's budget never stretches the interval (#710 S1b)
	"stream_flush_after_io_read",        // runtime: a takeover's store read for live I/O comes before the flush, and the takeover loads that pre-flush read (#710 S1b review)
	"stream_flush_unbounded",            // runtime: a takeover waits for a flush that never ends (#710 S1b review)
	"stream_flush_term_unkeyed",         // runtime: a flush that ran for an earlier term counts for the new one (#710 S1b review)
	"stream_failed_load_streams",        // runtime: a takeover whose retain store could not be read is not flagged, so its leader streams initial values (#710 S1b review)
	"stream_store_bad_forgotten",        // runtime: a store read that failed before the takeover (the file moved aside) is not counted as its failed load (#710 S1b review)
	"stream_sends_unled",                // naut: a leader's stream sends whether or not it leads (#710 S1b review)
	"stream_flush_no_persist",           // naut: the takeover's flush writes nothing (#710 S1b review)
	"stream_flush_keeps_follower",       // naut: the takeover's flush leaves the follower running (#710 S1b review)
	"stream_flush_keeps_shadow",         // naut: the takeover's flush keeps the shadow (#710 S1b review)
	"stream_stale_marked_persisted",     // naut: an image the retain file refused as older is shown as kept (#710 S1b review)
	"stream_file_floor_unseeded",        // naut: the standby's floor is not seeded from its retain file's term (#710 S1b review)
	"stream_persist_error_hidden",       // naut: a standby whose retain file takes no image still says in sync (#710 S1b review)
	"stream_outlives_close",             // naut: a closing member's stream handlers outlive it, so its shutdown waits on them (#710 S1b)
	"lease_mismatch_never_clears",       // node: a lease refusal stays raised after the voter takes the lease again (#710 1 s lease)
	"lease_clear_on_wait",               // node: a voter's restart wait, which still takes a lease it refuses after, clears a lease refusal (#710 1 s lease review)
	"lease_refusal_one_way",             // node: a refusal given and one received share one report, so a pair whose leases differ flaps (#710 1 s lease)
	"lease_condition_dropped",           // naut: a voter's lease refusal raises no condition (#710 1 s lease)
	"witness_lease_no_alarm",            // naut: the witness refusing the pair's lease raises no system alarm (#710 1 s lease)
	"lease_check_unwarned",              // naut: naut check says nothing of a pair lease under 1 s (#710 1 s lease)
	"image_tags_carry_io",               // runtime: an image carries the inputs and outputs, which the field gives back and the first scan recomputes (#710 S2a)
	"image_tags_written_only",           // runtime: an image carries only the tags a program writes, not an operator's (#710 S2a)
	"image_tag_forced_value",            // runtime: an image carries a forced tag's forced value, not its actual one (#710 S2a)
	"image_tags_at_take",                // runtime: a task's tags are copied when the image is taken, not at the end of the scan its frame was copied at (#710 S2a)
	"copy_first_scan_only",              // runtime: an image is taken from the first copies, with no second round for the faster tasks (#710 S2c)
	"copy_from_older_round",             // runtime: an image takes a copy made for an earlier, untaken round (#710 S2c)
	"image_source_unhashed",             // runtime: a task's image names no running source (#710 S2a)
	"shadow_tags_layout_unchecked",      // runtime: the shadow applies the tags of a leader that carries another set (#710 S2a)
	"image_tags_retained_twice",         // runtime: an image's store section carries the retained tags the retain section already carries (#710 S2a)
	"stream_format_ignored",             // naut: the leader sends its newest image format whatever the standby reads (#710 S2a)
	"hot_no_age_check",                  // runtime: a takeover goes hot on an image older than hot-max-age, or with hot-max-age 0 (#710 S2b)
	"hot_source_unchecked",              // runtime: a takeover goes hot with a task running another source than the image's (#710 S2b)
	"hot_partial_tasks",                 // runtime: a takeover installs the tasks that fit and resets the rest, not all or none (#710 S2b)
	"hot_globals_not_carried",           // runtime: a hot takeover installs the frames but not the image's tags (#710 S2b)
	"hot_timer_not_rebased",             // runtime: a hot takeover keeps the timers on the old leader's clock (#710 S2b)
	"hot_age_not_elapsed",               // runtime: a hot takeover rebases the timers to the image's arrival, not counting its age and the inputs wait (#710 S2b)
	"hot_late_flush_installs",           // runtime: a takeover waits out a late flush and installs what it hands over (#710 S2b)
	"hot_failed_flush_installs",         // runtime: a takeover whose flush failed (not on an unreadable own store) still goes hot (#710 S2b)
	"hot_ticket_carried",                // runtime: a hot takeover keeps a READ/WRITE ticket of the old controller (#710 S2b)
	"retain_memory_only_forgotten",      // runtime: a hot takeover over an unreadable own store does not say its retained values are only in memory, or a failed save clears it (#710 S2b)
	"hot_retain_before_checks",          // runtime: a takeover over an unreadable own store loads the image's retained values before its checks, and keeps them when one fails (#710 S2b)
	"hot_rebase_per_task",               // runtime: a hot takeover rebases each task from its own copy time, not the image's one leader reference, so a fast task beside a slow one loses the gap (#710 S2b, Fable)
	"hot_build_unchecked",               // naut: a takeover goes hot on an image of another build (#710 S2b)
	"hot_cold_condition_never_clears",   // naut: the cold-takeover warning stays after a standby follows again (#710 S2b)
	"grant_keeps_record",                // a campaign's grant records no epoch (#710 S2c-1)
	"mark_from_refused_renewal",         // a voter keeps the mark of a renewal it refused (#710 S2c-1)
	"reply_mark_after_grant",            // a voter keeps a renewal's mark before its grant is durable, so a failed write leaves it (#710 S2c-1)
	"mark_term_compared",                // a mark holds the renewal's vote term, not the leader's epoch (#710 S2c-1)
	"mark_from_losing_round",            // a hot takeover reads earlier campaign rounds' replies too, not only the winning one's (#710 S2c-1)
	"unknown_counts_toward_majority",    // a reply whose records don't cover the image's epoch counts toward the known majority (#710 S2c-1)
	"own_record_ignored",                // a hot takeover misses this controller's own leadership after the image (#710 S2c-1)
	"mark_tie_current_wins",             // of two marks at one (epoch, sequence), the current one wins (#710 S2c-1)
	"later_leader_ignored",              // a hot takeover goes on over an epoch the voters granted the old leader after its image (#710 S2c-1)
	"records_from_floor_unset",          // a commissioned controller's voter with no records yet covers nothing, not what came after its floor (#710 S2c-1)
	"one_mark_enough",                   // a hot takeover takes one voter's mark though the other covering voter has none (#710 S2c-1, Fable M2)
	"evidence_from_covering_only",       // a later epoch or a newer mark from a non-covering winning reply is ignored (#710 S2c-1, Fable S2)
	"mark_from_older_renewal",           // a delayed older renewal's mark replaces a newer one from the same boot (#710 S2c-1, Fable S1)
	"current_without_fresh_send",        // naut: a standby in step with the last image sent stays current however long ago it was sent (#710 S2c-1, Fable M1)
	"interval_across_reconnect",         // naut: the send-to-send time is measured across a reconnect's gap (#710 S2c-1, Fable M1)
	"hot_voters_unchecked",              // naut: a takeover goes hot without the voters' check (#710 S2c-1)
	"ack_old_epoch_counts",              // naut: a leader takes its standby's acknowledgement of another epoch (#710 S2c-1)
	"current_time_based",                // naut: a standby counts as current by the time since its last acknowledgement, not by sequence (#710 S2c-1)
	"discounted_vouches",                // a discounted voter's reply (two processes cast it) vouches for a hot takeover (#710 S2c-3, naut-lead S5)
	"prior_boot_overwritten",            // a campaign's grant to a new boot keeps no record of the earlier boots' epoch (#710 S2c-3)
	"own_record_after_grant",            // a reply reports the asker's record with its own campaign's grant in it (#710 S2c-3)
	"handback_guard_off",                // a controller hands back again however recently it handed back (#710 S2c-3)
	"handback_future_held",              // a hand-back time in the future (the clock went back) holds the guard (#710 S2c-3)
	"handback_own_record_ignored",       // the hand-back's floor leaves out this controller's earlier leaderships (#710 S2c-3)
	"handback_unknown_counts",           // the hand-back's floor counts a voter whose records don't cover it (#710 S2c-3)
	"handback_keeps_renewing",           // node: a controller that handed the lead back keeps renewing and campaigning (#710 S2c-3)
	"handback_guard_volatile",           // node: a hand-back's time is not written to the voter state (#710 S2c-3)
	"handback_guard_kept_on_failure",    // node: a hand-back whose write failed arms the guard all the same (#710 S2c-3, Fable S3)
	"handback_store_bad_forgotten",      // runtime: an edge's failed store read is forgotten once a hand-back closes the gate (#710 S2c-3, Fable M1)
	"handback_restart_empty_trusted",    // naut: a restart after a hand-back trusts the emptied retain file (#710 S2c-3, Fable M1)
	"handback_floor_max_only",           // the hand-back's floor takes the highest record even when all three voters' records say no leadership reached it (#710 S2c-3, Fable S1)
	"every_acquisition_a_takeover",      // node: every leadership acquired counts as a takeover, a new pair's first too (#710 (b))
	"hot_first_scan_true",               // runtime: a hot takeover makes FIRST_SCAN() TRUE (#710 (b))
	"takeover_unmarked",                 // runtime: a takeover sets no TAKEOVER_SCAN()/HOT_TAKEOVER() and counts nothing (#710 (b))
	"takeover_flag_kept",                // runtime: TAKEOVER_SCAN()/HOT_TAKEOVER() stay TRUE past the first scan (#710 (b))
	"takeover_flag_survives_step_down",  // runtime: a takeover flag a program never scanned after survives a step-down into a same-term resume (#710 (b))
	"peer_ready_never_set",              // runtime: PEER_READY() never takes what the pair sets (#710 (b))
	"ack_other_build",                   // naut: a standby acknowledges an image of another build (#710 (b))
	"ack_kept_across_reconnect",         // naut: a leader keeps an acknowledgement across a new stream connection (#710 (b))
	"peer_ready_ignores_forces",         // naut: PEER_READY() is TRUE while the leader holds forces a takeover drops (#710 (b))
	"handback_long_deadline",            // naut: the hand-back waits 2 s for the peer's answer, not a few round trips (#710 S2c-3)
	"handback_ineligible_peer",          // naut: a peer that could not lead (unhealthy, released, held) answers good (#710 S2c-3)
	"handback_peer_guard_off",           // naut: a peer that handed back itself recently answers good (#710 S2c-3)
	"handback_empty_file_good",          // naut: a peer's retain file with no stream position (none, or kept aside) is good (#710 S2c-3)
	"handback_peer_file_unjudged",       // naut: a peer's retain file older than the voters' floor is good (#710 S2c-3)
	"handback_taken_unalarmed",          // naut: a takeover from the peer's hand-back raises no condition (#710 S2c-3)
	"handback_on_readable_store",        // runtime: a takeover asks for a hand-back with its own store readable (#710 S2c-3)
	"handback_when_hot",                 // runtime: a takeover that will go hot asks for a hand-back (#710 S2c-3)
	"hold_never_starts",                 // runtime: a takeover never starts the takeover hold (#710 (b2))
	"hold_release_tag_kept",             // runtime: a hold's start leaves a carried TRUE in its release tag (#710 (b2))
	"hold_drivers_remember",             // runtime: a hold's start does not make the drivers forget what they sent (#710 (b2))
	"hold_safe_unsent",                  // runtime: a hold never writes its safe values (#710 (b2))
	"hold_not_inherited",                // runtime: a hot takeover from a holding leader does not inherit its hold (#710 (b2))
	"hold_age_restarts",                 // runtime: an inherited hold's age starts over at the takeover (#710 (b2))
	"hold_no_max",                       // runtime: a hold never reaches its max (no alarm) (#710 (b2))
	"hold_release_tag_writable",         // runtime: a write or a force from outside the program may set the release tag (#710 (b2))
	"hold_channel_writes",               // runtime: a channel WRITE goes out while the hold holds (#710 (b2))
	"hold_not_imaged",                   // runtime: a holding leader's image says it is not holding (#710 (b2))
	"hold_forget_nothing",               // modbus: ForgetOutputs forgets nothing, so a rewrite: binding re-asserts an earlier leadership's value under the hold (#710 (b2))
	"hold_safe_never_lands",             // modbus: a safe value's landed write never marks it delivered (#710 (b2))
	"hold_release_at_max_ignored",       // runtime: on-max: release never releases at max (#710 (b2), Fable)
	"hold_replace_unsafe",               // runtime: a driver swapped in during a hold never gets the safe values (#710 (b2), Fable)
	"hold_channel_held_at_post",         // runtime: a channel WRITE posted while held goes out when it comes due after the release (#710 (b2), Fable)
	"hold_safe_wait_restarts",           // runtime: every safe re-send restarts the 2 s wait, so retries keep the alarm down (#710 (b2), Fable re-check)
	"resign_ignores_epoch",              // a voter takes a RESIGN of another leadership of the same boot (#710 d1)
	"resign_keeps_renewal_seen",         // a voter that takes a RESIGN still sees the resigned leader's renewals (LeaderSeen) (#710 d1)
	"resign_keeps_heard",                // a voter that takes a RESIGN keeps the promises it heard relayed to the resigned boot (#710 d1)
	"resign_renewal_granted",            // a renewal of a resigned leadership is taken, and seen (#710 d1)
	"resign_relay_heard",                // a stale relay of a promise to a resigned boot is heard (#710 d1)
	"resign_record_only",                // a voter matches a RESIGN against its campaign record only, not the renewals it granted (#710 d1)
	"resign_ignores_seq",                // a voter takes a RESIGN its boot sent before a later campaign or renewal it heard (#710 d1, advisor)
	"resign_keeps_rounds",               // a resigned leader keeps its open rounds, so a late renewal grant re-leads it once the hold ends early (#710 d1, advisor)
	"resign_quiet_outlives_grant",       // a voter ignores relays to a resigned boot for the whole quiet, even after granting that boot again (#710 d1, advisor)
	"resign_dup_acts",                   // a standby acts on a duplicate RESIGN's answer as on a fresh one: it campaigns again, its hold over (#710 d1, Fable)
	"resign_mark_dropped",               // a voter that takes a RESIGN does not keep its final mark (#710 d1, Fable)
	"resign_after_promise",              // a voter takes a RESIGN after its promise to that boot ran out (#710 d1, Fable)
	"resign_peer_first",                 // node: a RESIGN goes to the peer's voter before the witness, so the standby's campaign can reach the witness first (#710 d1)
	"resign_publish_after_send",         // node: a resigning leader's gate is published shut only after its RESIGN left (#710 d1, Fable)
	"resign_keeps_own_hold",             // node: a controller whose voter took its peer's RESIGN keeps its own resign hold, so a switchover back waits it out (#710 d1)
	"resign_waits_for_look",             // node: a standby whose voter took its peer's RESIGN campaigns at its next look, not at once (#710 d1)
	"resign_no_hold",                    // a resigned leader does not hold off campaigning (#710 d1)
	"resign_own_keeps_heard",            // a resigned leader's own voter keeps the promises it heard relayed to it, so it refuses the standby's campaign (#710 d1)
	"resign_before_drain",               // node: a resigning leader's RESIGN leaves before the outputs' drain ends (#710 d1b)
	"resign_despite_drain_error",        // node: a RESIGN leaves after a drain that did not finish (#710 d1b)
	"resign_frees_own_before_drain",     // a resigned leader's own voter frees its promise before the outputs' drain is proven (#710 d1b, Fable)
	"hold_ends_in_drain",                // a resign hold ends inside the resign's drain window, so ResignDrained can free a new campaign's promise (#710 d1d, Fable)
	"handback_any_epoch",                // a hand-back of the resigned epoch or an earlier one ends the resign hold (#710 d1d, Fable)
	"handback_keeps_resign_hold",        // naut: a resigner asked to take the lead back keeps its resign hold, so it waits two leases out (#710 d1d)
	"resign_despite_late_drain",         // node: a RESIGN leaves after a drain that finished only after the resigned lease (#710 d1d, naut-lead nit on d1b)
	"resign_drained_unchecked",          // a late ResignDrained (after the resigned lease) still frees the own voter's promise, which may back a new campaign (#710 d1b, Fable re-check)
	"enter_after_read",                  // fence: a send is counted for the drain only after its gate read, so a drain misses one that read it open just before it closed (#710 d1b)
	"drain_no_wait",                     // fence: a drain does not wait for the sends in flight (#710 d1b)
	"drain_no_reset",                    // fence: a drain cuts nothing short: armed sends wait out their deadlines, late ones still start (#710 d1b)
	"drain_no_settle",                   // fence: a drain does not wait the fence margin after the last send (#710 d1b)
	"host_enter_after_gate",             // sparkplug host: an NCMD is counted for the drain only after its gate read (#710 d1b)
	"switch_writes_allowed",             // runtime: writes, forces, substitutes and online edits land while switching over (#710 d1c)
	"switch_scans_on",                   // runtime: the scans go on while switching over (#710 d1c)
	"switch_no_wait",                    // runtime: a switchover starts without waiting for the scan under way to end (#710 d1c)
	"switch_ignores_forces",             // runtime: a switchover starts with forces held (#710 d1c)
	"switch_ignores_hold",               // runtime: a switchover starts while a takeover hold holds (#710 d1c)
	"abort_keeps_switching",             // runtime: an aborted switchover leaves the scans stopped and writes refused (#710 d1c)
	"resume_ends_switching",             // runtime: a gate that closes for a tick and reopens in the same term ends the switching state, so scans run under a sent final image (#710 d1c, Fable M1)
	"takeover_keeps_switching",          // runtime: a new leadership keeps an earlier switchover's switching state, so it never scans (#710 d1c, Fable M1)
	"final_keeps_round",                 // runtime: the final image leaves the stream's open round, so an image numbered after it goes out (#710 d1c, Fable S1)
	"isdu_write_unfenced",               // opcua: an IO-Link parameter write goes without the write gate, from a standby too, uncounted by the drain (#710 d1c)
	"final_image_unsent",                // naut: the switchover's final image never goes out on the stream (#710 d1c)
	"capture_ignores_switching",         // naut: the stream waits for an end-of-scan copy while switching, and drops (#710 d1c)
	"final_ack_any_seq",                 // naut: a switchover takes an acknowledgement of any image as the final image's (#710 d1c)
	"switch_unready_target",             // naut: a switchover goes to a standby that is not current or may not lead (#710 d1c)
	"failback_unknown_as_known",         // runtime: a takeover on an unreadable retain store hands the failback keeper a known, empty state (#710 d2)
	"failback_not_carried",              // retain: Carry leaves the standby's failback state, not the leader's (#710 d2)
	"switch_ignores_io_reach",           // naut: a switchover goes to a standby that does not reach a field driver the leader reaches (#710 d2)
	"failback_ready_never_resets",       // naut: a break in the preferred standby's readiness does not restart failback's clock (#710 d2)
	"failback_ignores_suspend",          // naut: failback runs while suspended (#710 d2)
	"failback_never_stops",              // naut: the ping-pong stop never stops failback (#710 d2)
	"failback_count_never_resets",       // naut: a failback that holds does not reset the ping-pong count (#710 d2)
	"failback_manual_not_suspended",     // naut: a manual switchover away from the preferred controller leaves failback on (#710 d2)
	"failback_failover_not_counted",     // naut: a failback that ended as a failover is not counted for the ping-pong stop (#710 d2, Fable)
	"failback_undo_overwrites",          // naut: an aborted switchover's undo overwrites an operator's suspend (#710 d2, Fable)
	"failback_abort_keeps_suspend",      // naut: a manual switchover from the preferred controller that aborts leaves failback suspended (#710 d2, Fable)
	"failback_suspend_not_saved",        // naut: a failback suspend is answered before the retain store holds it (#710 d2, Fable)
	"failback_ignores_prefer_mismatch",  // naut: a failback goes to a standby whose manifest prefers otherwise (#710 d2, Fable)
	"witness_covers_below_floor",        // quorum: the hot check takes a witness reply's coverage of an epoch below the controller's voter floor (#710 S3)
	"witness_reason_any",                // quorum: the verdict names witness-readopted though the other voter's missing coverage made it cold anyway (#710 S3, Fable)
	"witness_floor_unwired",             // quorum: NewAdoption does not give the candidate its voter's floor for the witness check (#710 S3)
	"switch_ignores_standby_io",         // naut: a switchover goes to a standby whose field I/O is not healthy (#710 d1c)
	"abort_keeps_final",                 // naut: an aborted switchover's final image stays, so the stream sends nothing more (#710 d1c)
	"final_sent_once_per_connection",    // naut: a stream connection sends one final image per leadership, so a switchover after an aborted one never sends its own (#710 d1c bench)
	"resign_unchecked",                  // naut: a switchover resigns although its switching state or leadership ended meanwhile (#710 d1c, Fable re-check)
	"flush_next_tick",                   // runtime: a takeover edge leaves its image flush to the next tick (#710 d1e)
	"catchup_next_tick",                 // runtime: a takeover edge leaves its operator-intent catch-up to the next tick (#710 d1e)
	"leading_term_locks",                // runtime: LeadingTerm and a flush's carry wait on leadMu, which the edge's tick holds while it waits for that flush (#710 d1e, Fable M1)
	"wait_keeps_lead_sample",            // runtime: a takeover edge takes over on a lease sampled before its wait (#710 d1e, Fable S1)
	"beginswitch_reads_snapshot",        // runtime: BeginSwitch checks the leadership on the snapshot, so a new leadership's takeover can land between the check and the flag (#710 d1e, Fable re-check)
	"carry_snapshot_any_wait",           // runtime: a carry reads the snapshot during the catch-up's wait too, landing over the store the takeover loaded (#710 d1e, Fable re-check)
	"abort_twice_fails",                 // fence: a second Abort of a connection already reset reports nothing reset (CI on main after #1359)
	"flush_wait_after_start",            // runtime: a takeover edge raises its flush-wait flags only when its tick starts to wait, after the flush began (#710 d1e, CI on main)
	"save_waits_on_edge",                // runtime: a retain save during a takeover edge's flush wait parks the saver on leadMu, holding the flush's carry (#710 d1e, CI on #1359)
	"edge_wait_blocks_others",           // runtime: other tasks' ticks queue on leadMu while a takeover edge waits for its step (#710 d1e)
	"final_waits_interval",              // naut: a final image waits out the stream's interval when its wake came first (#710 d1c bench)
	"no_reask",                          // quorum: a pending campaign is never asked again, so on a slow disk no campaign hears its grants (#710 lost campaign)
	"reask_new_term",                    // quorum: a campaign's re-ask asks a new term, which needs a new write at each voter (#710 lost campaign)
	"reask_lease_from_first_send",       // quorum: a campaign won on a re-ask's grants leases from its first send, so on a slow link its first renewal lands after the lease (#710 lost campaign, sim seeds)
	"reask_lease_from_last_ask",         // quorum: a campaign's lease runs from its latest ask, though a grant answers an earlier one (#710 lost campaign)
	"backoff_from_first_send",           // quorum: an unheard campaign backs off from its first send, not its last re-ask (#710 lost campaign, advisor)
	"reask_declined_stays",              // quorum: a declined re-ask leaves its look in the past, so a driver's timer spins (#710 lost campaign, sim hang)
	"grant_sent_last_reply",             // quorum: a voter's grant of an earlier ask, landing last, moves the lease's start back (#710 lost campaign, Fable)
	"reterm_with_majority",              // quorum: a refusal from a voter that promised this leader its term re-terms although the other two still grant it, so on a slow disk the re-term's write can lose the lease (#710, #1120)
	"own_promise_unchecked",             // quorum: any refusal at the leader's term counts as from a voter that promised it that term, so a standby's voter that promised itself the term is never won back (#710, #1120; F13)
	"reterm_on_own_promise",             // quorum: a refusal at the leader's term from a voter that promised it that term counts as a promise elsewhere (#710, #1120)
	"held_promise_answers",              // quorum: a voter answers an ask of a term it promised this boot before that promise's write is durable (#710, #1120, lead check 4)
	"spark_ungated",                     // naut: the Sparkplug edge node's session runs on every replica, not only the leader (#798)
	"spark_no_death_on_stepdown",        // naut: a switchover resigns before its edge node session has ended, so the new leader can birth before the old one's NDEATH (#798)
	"spark_bdseq_clash",                 // sparkplug: a new edge node session's bdSeq may be the peer's last (#798)
	"spark_publish_after_gate",          // sparkplug: the edge node's publish gate is always open, so a session the gate closed on births, publishes and buffers until its stop (#798)
	"spark_death_watch_off",             // sparkplug: the edge node does not listen to its own NDEATH topic, so a late will of the old leader's session leaves it offline at a host that ignores bdSeq (#798)
	"spark_send_uncounted",              // sparkplug: a fenced edge node publish is not counted for a switchover's drain (#798)
	"spark_gate_unlatched",              // naut: an edge node session's gate opens again in its term after reading closed, so a rebirth caught in the gap leaves the session silent (#798, Fable)
	"cause_resign_unnoted",              // node: a leadership taken from the peer's RESIGN is not noted as one, so a planned switchover reads as a failover (#798 follow-up)
	"switch_by_dropped",                 // naut: a switch-ask's by label is not kept, so a failback reads as a switchover (#798 follow-up)
	"spark_step_unfenced",               // naut: a switchover's step-down fences the session only once the supervisor's goroutine gets to it, not during a blocked Start (#798, Fable)
	"write_timed_from_take",             // node: a voter state write is timed from when the writer takes it, not from the oldest ask: its queue behind a write in flight goes unseen (#1436)
	"slow_write_at_budget",              // node: a slow voter state write is warned of only past the whole write budget, when the pair may already be unable to elect (#1436)
	"slow_write_unlimited",              // node: every slow voter state write is warned of, unlimited (#1436)
	"late_grant_any_miss",               // node: a grant a write released to no waiter counts as late though a re-ask of the same campaign was answered by it (#1436)
	"leaderless_from_boot",              // node: the no-leader report counts from the start, not from the end of the restart wait (#1436)
	"leaderless_never_quiet",            // node: the no-leader error is logged where a report of its own already covers it (an uncommissioned controller) (#1436)
	"cause_cut_as_slow",                 // node: the no-leader cause says a voter that does not answer at all has not confirmed its write, blaming its disk for a cut link (#1436)
	"own_disk_blamed_always",            // node: the no-leader cause blames this controller's own slow disk though its grant would make no majority (#1436)
	"driver_drops_unseen",               // node: a campaign reply the driver drops (an unadopted witness incarnation, a fenced state) leaves the no-leader cause saying the voter is silent (#1436)
	"probe_error_as_info",               // node: the start-up write probe logs a disk too slow for the lease as info, not an error (#1436)
	"witness_write_by_own_lease",        // node: the witness judges a pair's write times by its own --lease, not the pair's (#1436)
	"rewrite_timed",                     // node: a write of a Seq already on disk (a re-ask's, queued behind the first) is timed, counting the first write's time twice (#1436, Fable)
	"words_stale_at_backoff",            // node: a voter's campaign word goes stale after two leases, so the no-leader cause flips at the look that ends an unheard campaign's back-off (#1436, Fable)
	"answered_unbound",                  // node: a witness answer under a certificate this controller does not count proves the witness reachable, so the no-leader cause blames its write (#1436, Fable)

	"leaderless_alarm_one_episode",         // node: the no-leader alarm keeps one episode across spells, so a page queued in one spell may leave in the next (#1436)
	"leaderless_alarm_second_never_defers", // node: the second-named controller raises the no-leader alarm despite the first's fresh word that it pages it: paged twice (#1436)
	"leaderless_alarm_stale_word",          // node: the second-named controller defers to the first's no-leader alarm word however old: paged by nobody (#1436)
	"leaderless_alarm_no_grace",            // node: the second-named controller raises the no-leader alarm at once, before the first's word can come: paged twice (#1436)
	"leaderless_alarm_unlatched",           // node: a controller drops the no-leader alarm when the first's word comes back, so a flapping link pages again at each hand-off (#1436)
	"leaderless_alarm_uncommissioned",      // node: an uncommissioned controller (a pair being set up) raises the no-leader alarm (#1436)
	"leaderless_alarm_reach_ignores_peer",  // node: the witness's refusal alone silences the no-leader alarm, so a duel (the #1436 slow-write case) pages nobody (#1436)
	"leaderless_word_unbound",              // node: the no-leader alarm word in a hello answer is not bound by its MAC: anyone between the controllers can silence the second's page (#1436)
	"pair_alarm_none",                      // naut: a pair member builds no no-leader alarm, so a pair with no leader pages nobody (#1436)
}

var mutOn = os.Getenv("QUORUM_MUT")

func init() {
	if mutOn != "" && !slices.Contains(Mutations, mutOn) {
		panic(fmt.Sprintf("quorum: QUORUM_MUT=%q is not a known mutation", mutOn))
	}
}

func mut(name string) bool { return name == mutOn }
