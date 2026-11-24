//go:build quorum_mut

package node

import (
	"net"
	"os"
	"slices"
	"strings"
)

// Mutations of the driver (test plan §3.3), switched on like package
// quorum's: build tag quorum_mut and QUORUM_MUT=<name>. The names are in
// quorum.Mutations too, whose init refuses an unknown one, and each has a
// row in tools/quorum-mutants.
var mutations = []string{
	"every_acquisition_a_takeover",   // every leadership acquired counts as a takeover, a new pair's first too (#710 (b))
	"resign_peer_first",              // a RESIGN goes to the peer's voter before the witness (#710 d1)
	"resign_keeps_own_hold",          // a controller whose voter took its peer's RESIGN keeps its own resign hold, so a switchover back waits it out (#710 d1)
	"resign_waits_for_look",          // a standby whose voter took its peer's RESIGN campaigns at its next look, not at once (#710 d1)
	"handback_keeps_renewing",        // a controller that handed the lead back keeps renewing and campaigning (#710 S2c-3)
	"handback_guard_volatile",        // a hand-back's time is not written to the voter state, so a restart forgets it (#710 S2c-3)
	"handback_guard_kept_on_failure", // a hand-back whose write failed keeps its time in memory, arming the guard (#710 S2c-3, Fable S3)
	"gate_on_tick",                   // the gate is judged on the elector's look, not when it is read
	"peer_host_trusted",              // the peer listener answers a trusted Host without the peer token
	"persisted_before_write",         // a write is reported durable before it is on disk
	"state_in_place",                 // the state file is rewritten in place, so a crash tears it
	"nonce_fixed",                    // the boot nonce is the same at every start
	"lease_unchecked",                // a voter grants a request whose lease outlives its own promise
	"witness_rebuilds_silently",      // the witness draws a new incarnation over a state file it can't read
	"controller_lease_unchecked",     // a controller's voter grants a request whose lease outlives its own promise
	"reply_relay_dropped",            // a controller's voter never hears the promise a reply relays (#1029 layer 2)
	"controller_write_ignored",       // a controller's failed state write is reported durable
	"witness_write_ignored",          // the witness's failed state write is reported durable
	"gate_ignores_health",            // the gate stays open for unhealthy scans or after Release
	"controller_unlocked",            // a controller's state file isn't locked to one process
	"witness_unlocked",               // the witness's state directory isn't locked to one process
	"no_term_cap",                    // the witness takes a vote that jumps the terms
	"controller_no_term_cap",         // a controller's voter takes a vote that jumps the terms
	"hello_unbound",                  // the client-token MAC binds no names or direction: a reflected question passes
	"term_cap_narrow",                // the term-jump cap is 2^20: an isolated controller crosses it in about 50 days and is refused for good
	"state_owner_unchecked",          // a controller starts over a state file another member (or another pair) wrote
	"config_differs_campaigns",       // a controller campaigns while its peer's hello names another configuration of the pair
	"hello_duplicate_unseen",         // two processes behind the peer's name (nonces N1, N2, N1 at the hello) raise nothing
	"hello_retry_slow",               // a hello with no verified answer is tried again only a whole interval later (#1120)
	"hello_not_asked_again",          // a peer's restart or a link coming up asks for no hello: the backed-off retry waits (#1120)
	"hello_asks_unspaced",            // asks for a hello send one after another: a storm with a flapping link or two processes behind one name (#1120 review)
	"box_unchecked",                  // a controller votes from a state file written on another box (a cloned disk, #1029)
	"box_wait_skipped",               // a controller whose box can't be read yet votes on the stored identity (#1034 review)
	"campaign_ignores_sync",          // an unsynced standby campaigns over a synced peer that may (#710 3b-4b)
	"unsynced_leader_keeps_lead",     // an unsynced, stuck leader never yields to a synced peer (#710 3b-4b)
	"yield_undamped",                 // a leader yields again within yieldEvery (#710 3b-4b)
	"yield_ignores_may",              // a leader yields to a peer that may not campaign (#710 3b-4b)
	"yield_ignores_can_sync",         // a leader yields to a synced peer that can't sync the others (#1053 review)
	"yield_to_marked_peer",           // a leader yields to a peer its own voter has marked a confirmed duplicate (#1029 3c)
	"spare_flag_after_silence",       // node: a copy goes silent as a spare before its flag is durable (#1029 3c-2)
	"spare_answers_votes",            // node: a spare still answers the peer's votes, adopts and hellos (#1029 3c-2)
	"spare_still_looks",              // node: a spare still looks, probes and hellos its peer (#1029 3c-2)
	"peer_dial_lingers",              // node: a peer connection closes with a FIN, its unsent frames retransmitted after the process is gone (#1133 review)
	"spare_forgotten_on_box_change",  // node: a spare's flag is read after the box check, which loses it on a copy's box (#1029 3c-2)
	"newer_state_moved_aside",        // node: a state file of a newer format is moved aside as lost, not refused (#1029 3c-2)
	"clear_partial_check",            // the clear goes ahead with a member that did not answer its check phase (#1029 3c-3a)
	"clear_done_ignores_clear",       // the clear reports done though a member missed the clear phase (#1135 re-check)
	"clear_rerun_refused",            // a clear that missed a member can't be run again once no member marks (#1135 re-check)
	"clear_ignores_keep",             // a member clears though it hears a boot other than the one the check kept (#1135 review)
	"clear_without_keep",             // a clear goes ahead when its check heard no boot of the name to keep (#1135 ruling)
	"clear_retires_marked_only",      // a member that marks nothing retires no boot on the clear (#1135 ruling)
	"done_ignores_held",              // the clear reports done though the held controller is still held at the re-check (#1144 re-check)
	"clear_ignores_held",             // the clear's check counts a held controller with no member marking it as nothing to clear (#1144 review)
	"clear_gen_ignores_holds",        // the clear's generation is 1, not above every member's and every hold's (#1029 3c-3b)
	"recheck_marks_only",             // the re-check reports a copy only when a member marks it again, not when the members together hear two (#1135 review)
	"may_ignores_mark",               // a controller a voter marks a duplicate still says it may campaign (#1029 3c)
	"mark_alarm_from_cause",          // the confirmed alarm follows the last duplicate's cause, which a later count in one Handle overwrites, and is paced (#1113 review)
	"vouch_refusal_blocks",           // a vouch the log refuses refuses the re-commission (#1053 review)
	"yield_until_due",                // a yield ends at the lease's due end, though a late grant moved it (#710 3b-4b)
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
	"recommission_under_fence",       // node: a commission or re-commission adopts below a member's fence, which then refuses the pair
	"fence_any_name",                 // node: a member takes a fence on any name, the witness's or its own included
	"dup_answered_before_write",      // node: a clear or fence is answered before its write is durable: a crash then loses what the command reported done
	"dup_wait_lease_scaled",          // node: a member waits for a clear's or a fence's write only its share of the lease (125 ms on a 500 ms witness): a slow disk ends the command incomplete (#1329)
	"lower_history_peer_synced",      // node: a peer's log at a lower or forked history is one to defer and yield to (#1029 3c-3c-2a)
	"contacts_part_users",            // node: the alarm contacts' credMAC uses the users part's label, so a users MAC verifies as contacts (#715)
	"recommission_beside_live",       // node: recommission goes ahead while a voter hears another live boot of this controller's name (#1029 3c-3c-2b)
	"live_check_witness_skipped",     // node: recommission's live-copy check skips a witness that does not answer it (#1207 review)
	"peer_tls_handshake_unbounded",   // node: a peer TLS handshake has no bound of its own, so a silent peer holds a connection per call (#1230 review)
	"peer_tls_mismatch_unnamed",      // node: a voter that speaks the other protocol, or holds another key, raises nothing (#710)
	"peer_api_guessed",               // node: a standby names the leader's tag API as the peer's host and its OWN API port (#710 demo finding 1)
	"peer_api_unbound",               // node: the announced tag API is outside the hello's InfoMAC, so whoever answers can name it
	"peer_api_self",                  // node: a peer that announces this controller's own tag API is proxied to: the standby proxies to itself
	"lease_mismatch_never_clears",    // node: a lease refusal stays raised after the voter takes the lease again (#710 1 s lease)
	"lease_clear_on_wait",            // node: a voter's restart wait, which still takes a lease it refuses after, clears a lease refusal (#710 1 s lease review)
	"lease_refusal_one_way",          // node: a refusal given and one received share one report, so a pair whose leases differ flaps (#710 1 s lease)
}

// guessPeerAPI is what a standby named before the hello announced the
// leader's tag API: the peer's host and this controller's own API port
// (peer_api_guessed brings it back).
func guessPeerAPI(peerAddr, selfAPI string) string {
	host, _, err := net.SplitHostPort(peerAddr)
	_, port, perr := net.SplitHostPort(selfAPI)
	if err != nil || perr != nil {
		return ""
	}
	return net.JoinHostPort(host, port)
}

var mutOn = os.Getenv("QUORUM_MUT")

func mut(name string) bool { return name == mutOn }

// coreMutation reports whether QUORUM_MUT names a mutation of package
// quorum rather than of this driver. The loopback tests skip then: the
// core's own suite and the simulator catch those, and the runner runs the
// whole tree once per mutation.
//
// QUORUM_LOOPBACK=1 runs them anyway, to check by hand that a loopback
// case catches a core mutation too. Peer TLS's mutations (package
// peertls) are the loopback tests' own: the wire is what they break.
func coreMutation() bool {
	return mutOn != "" && !slices.Contains(mutations, mutOn) && !strings.HasPrefix(mutOn, "peer_tls_") && os.Getenv("QUORUM_LOOPBACK") == ""
}
