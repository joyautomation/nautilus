//go:build quorum_mut

package node

import (
	"os"
	"slices"
)

// Mutations of the driver (test plan §3.3), switched on like package
// quorum's: build tag quorum_mut and QUORUM_MUT=<name>. The names are in
// quorum.Mutations too, whose init refuses an unknown one, and each has a
// row in tools/quorum-mutants.
var mutations = []string{
	"gate_on_tick",                  // the gate is judged on the elector's look, not when it is read
	"peer_host_trusted",             // the peer listener answers a trusted Host without the peer token
	"persisted_before_write",        // a write is reported durable before it is on disk
	"state_in_place",                // the state file is rewritten in place, so a crash tears it
	"nonce_fixed",                   // the boot nonce is the same at every start
	"lease_unchecked",               // a voter grants a request whose lease outlives its own promise
	"witness_rebuilds_silently",     // the witness draws a new incarnation over a state file it can't read
	"controller_lease_unchecked",    // a controller's voter grants a request whose lease outlives its own promise
	"reply_relay_dropped",           // a controller's voter never hears the promise a reply relays (#1029 layer 2)
	"controller_write_ignored",      // a controller's failed state write is reported durable
	"witness_write_ignored",         // the witness's failed state write is reported durable
	"gate_ignores_health",           // the gate stays open for unhealthy scans or after Release
	"controller_unlocked",           // a controller's state file isn't locked to one process
	"witness_unlocked",              // the witness's state directory isn't locked to one process
	"no_term_cap",                   // the witness takes a vote that jumps the terms
	"controller_no_term_cap",        // a controller's voter takes a vote that jumps the terms
	"hello_unbound",                 // the client-token MAC binds no names or direction: a reflected question passes
	"term_cap_narrow",               // the term-jump cap is 2^20: an isolated controller crosses it in about 50 days and is refused for good
	"state_owner_unchecked",         // a controller starts over a state file another member (or another pair) wrote
	"config_differs_campaigns",      // a controller campaigns while its peer's hello names another configuration of the pair
	"hello_duplicate_unseen",        // two processes behind the peer's name (nonces N1, N2, N1 at the hello) raise nothing
	"box_unchecked",                 // a controller votes from a state file written on another box (a cloned disk, #1029)
	"box_wait_skipped",              // a controller whose box can't be read yet votes on the stored identity (#1034 review)
	"campaign_ignores_sync",         // an unsynced standby campaigns over a synced peer that may (#710 3b-4b)
	"unsynced_leader_keeps_lead",    // an unsynced, stuck leader never yields to a synced peer (#710 3b-4b)
	"yield_undamped",                // a leader yields again within yieldEvery (#710 3b-4b)
	"yield_ignores_may",             // a leader yields to a peer that may not campaign (#710 3b-4b)
	"yield_ignores_can_sync",        // a leader yields to a synced peer that can't sync the others (#1053 review)
	"yield_to_marked_peer",          // a leader yields to a peer its own voter has marked a confirmed duplicate (#1029 3c)
	"spare_flag_after_silence",      // node: a copy goes silent as a spare before its flag is durable (#1029 3c-2)
	"spare_answers_votes",           // node: a spare still answers the peer's votes, adopts and hellos (#1029 3c-2)
	"spare_still_looks",             // node: a spare still looks, probes and hellos its peer (#1029 3c-2)
	"peer_dial_lingers",             // node: a peer connection closes with a FIN, its unsent frames retransmitted after the process is gone (#1133 review)
	"spare_forgotten_on_box_change", // node: a spare's flag is read after the box check, which loses it on a copy's box (#1029 3c-2)
	"newer_state_moved_aside",       // node: a state file of a newer format is moved aside as lost, not refused (#1029 3c-2)
	"may_ignores_mark",              // a controller a voter marks a duplicate still says it may campaign (#1029 3c)
	"mark_alarm_from_cause",         // the confirmed alarm follows the last duplicate's cause, which a later count in one Handle overwrites, and is paced (#1113 review)
	"vouch_refusal_blocks",          // a vouch the log refuses refuses the re-commission (#1053 review)
	"yield_until_due",               // a yield ends at the lease's due end, though a late grant moved it (#710 3b-4b)
}

var mutOn = os.Getenv("QUORUM_MUT")

func mut(name string) bool { return name == mutOn }

// coreMutation reports whether QUORUM_MUT names a mutation of package
// quorum rather than of this driver. The loopback tests skip then: the
// core's own suite and the simulator catch those, and the runner runs the
// whole tree once per mutation.
//
// QUORUM_LOOPBACK=1 runs them anyway, to check by hand that a loopback
// case catches a core mutation too.
func coreMutation() bool {
	return mutOn != "" && !slices.Contains(mutations, mutOn) && os.Getenv("QUORUM_LOOPBACK") == ""
}
