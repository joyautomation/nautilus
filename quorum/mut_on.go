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
	"no_rho",                      // the leader's lease isn't shortened by ρ
	"restart_grants",              // a restarted voter grants at once (no restart wait)
	"answer_before_fsync",         // a voter's grant leaves before its state is durable
	"lease_from_reply",            // the lease is timed from the reply, not the send
	"no_backoff",                  // no back-off after a lapse or an unheard campaign
	"one_lease_backoff",           // a lapse backs off one lease, not two (#973)
	"any_incarnation",             // a witness grant counts from any incarnation
	"any_sender",                  // a grant counts from any sender
	"no_grantee_check",            // a grant meant for the other controller counts
	"no_nonce_check",              // a grant from an earlier boot counts
	"match_by_term",               // a grant counts toward a request of the same term, not id
	"renewals_unseen",             // a standby doesn't see renewals its voter refused (#973)
	"no_reterm",                   // a refused leader never moves to a higher term (#973)
	"no_yield",                    // a voter never yields its own candidate's promise (#979)
	"no_observe",                  // a candidate doesn't see the peer's requested terms (#979)
	"retry_on_grid",               // a refused campaign retries on the look grid (#979)
	"renew_on_look",               // a lease won waits for the next look to renew (#998)
	"no_quiet",                    // a newly adopted witness incarnation counts at once (#976)
	"no_floor",                    // an adoption doesn't raise the term floor (#976)
	"count_while_pending",         // a proposer keeps counting the old incarnation (forward only)
	"no_adopt_gen",                // a proposal is adopted whatever its generation
	"no_holdoff",                  // a switch never waits longer than a lease, however many came before
	"restart_counts_at_once",      // a restarted controller counts its adopted incarnation with no quiet period
	"no_adopt_echo",               // an uncommissioned controller takes an Adopt sent before its restart
	"switch_without_peer",         // a switch is proposed with the peer unheard (it may be down)
	"peer_from_any_adopt",         // an Adopt to an earlier boot of this controller says the peer is up
	"bump_cuts_quiet",             // a generation bump that keeps the incarnation ends a quiet period still owed
	"quiet_reply_unheard",         // a reply in the quiet period doesn't count as heard: a restart backs off two leases
	"uncommissioned_grants",       // a controller that lost its file grants before it re-commissions (#976)
	"propose_keeps_commit",        // a proposal leaves an adoption waiting for its write to count later (#1014 review)
	"count_over_pending",          // a write counts an adoption while a proposal is pending (#1014 review)
	"holdoff_from_boot",           // a failed adoption write leaves the hold-off measured from boot (#1014 review)
	"uncommissioned_takes_switch", // an uncommissioned controller adopts its peer's switch, not only its committed incarnation (two-of-three)
	"proposal_floor_volatile",     // a proposal's floor isn't durable with it: a restart re-sends it lower
	"both_lost_recommission",      // both controllers uncommissioned, the pair re-commissions on its own (#976's automatic recovery)
	"alarm_on_heard",              // an uncommissioned controller alarms although its peer's committed incarnation answered it
	"recommission_any_floor",      // an operator's floor at or below what the controller knows is taken
	"restart_wait_current_lease",  // a voter restarted with a shorter lease waits only that one (#1021 review)
	"lease_raise_volatile",        // a longer lease after a restart isn't written before an unchanged grant (#1021 re-review)
	"campaign_in_restart_wait",    // a controller campaigns while its own voter is in its restart wait (#1021 review)
	"duplicate_member_granted",    // a voter grants a name under another boot nonce while that name's promise runs (#1028 review)
	"relay_ignored",               // a voter ignores the promises other voters relay (#1029 layer 2)
	"relay_voter_rule_off",        // a name is granted while its own voter is heard promised elsewhere (#1029 layer 2, rule (b))
	"relay_self_promise",          // a voter relays its promise to its own candidate (a clone's self-grant reaches the voters)
	"relay_no_margin",             // a heard promise is held only for its time left, without the clock-rate margin
	"relay_tail_counted",          // a refusal in the clock-rate margin after a heard promise counts as a duplicate
	"standby_no_probe",            // a standby doesn't probe the witness every look
	"relay_not_pushed",            // a voter's new promise to another node reaches the witness only at the next look
	"dup_name_ignored",            // a voter grants a name it hears under two live boots (#1029 step 1)
	"dup_refuses_incumbent",       // a duplicated name's running promise is refused too, so the incumbent's protecting promises lapse (#1029 step 1)
	"dup_voter_counts",            // a candidate counts a grant from a voter its own voter knows is duplicated (#1029 step 1)
	"dup_not_relayed",             // a voter doesn't relay a name it sees duplicated (#1029 step 1)
	"standby_probes_witness_only", // a standby probes the witness only, not its peer (#1029 step 1)
	"yield_before_refusal",        // a voter yields its own round to a request it then refuses (#1079 review)
	"retry_after_refusal_only",    // a refused campaign whose own grant came back last waits out a lease (#1029 layer 2)
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
	"campaign_ignores_sync",        // node: an unsynced standby campaigns over a synced peer that may (the preference)
	"unsynced_leader_keeps_lead",   // node: an unsynced, stuck leader never yields to a synced peer
	"yield_undamped",               // node: a leader yields again within yieldEvery
	"yield_ignores_may",            // node: a leader yields to a peer that may not campaign
	"yield_ignores_can_sync",       // node: a leader yields to a synced peer that can't sync the others
	"vouch_refusal_blocks",         // node: a vouch the log refuses refuses the re-commission
	"vouch_alone_partial",          // oplog: a vouched catch-up goes ahead alone with a peer silent
	"vouch_alone_ignores_ahead",    // oplog: a vouched catch-up goes ahead alone over a longer copy that does not count yet
	"vouch_refuses_counting_ahead", // oplog: a vouch is refused for a later log on a peer that counts
	"can_sync_stale",               // oplog: can-sync counts a copy last heard 10 s ago, not within a lease
	"placeholder_keeps_synced",     // oplog: a mark under the placeholder identity is adopted synced
	"yield_until_due",              // node: a yield ends at the lease's due end, though a late grant moved it
	"term_not_persisted",           // oplog: the term lives only in memory, so a lost log takes a deposed leader's word
	"unnamed_leader_admitted",      // oplog: a marked replica takes replication when its election names no leader
	"vouch_unchecked",              // oplog: a vouch is taken over a lost log, a silent peer or a later log
	"vouch_alone_kept",             // oplog: a vouched log catches up alone at every leadership, not once
	"unsynced_log_writes",          // oplog: an unsynced log reads writable, so a write ends "outcome unknown"
	"mark_after_log",               // oplog: a new replica's mark is written after its log file exists
	"witness_log_unbound",          // naut: the witness's log admits replication from any member
	"witness_log_term_unbound",     // naut: the witness's log admits its granted member at any term
	"witness_log_any_incarnation",  // naut: the leader counts the witness's log at any incarnation
	"witness_not_a_replica",        // naut: the pair's log leaves the witness out (2 of 2, as before 3b-4)
	"log_sync_not_retried",         // naut: a leader whose term is unsynced is caught up only by a write
}

var mutOn = os.Getenv("QUORUM_MUT")

func init() {
	if mutOn != "" && !slices.Contains(Mutations, mutOn) {
		panic(fmt.Sprintf("quorum: QUORUM_MUT=%q is not a known mutation", mutOn))
	}
}

func mut(name string) bool { return name == mutOn }
