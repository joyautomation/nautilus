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
	"no_rho",              // the leader's lease isn't shortened by ρ
	"restart_grants",      // a restarted voter grants at once (no restart wait)
	"answer_before_fsync", // a voter's grant leaves before its state is durable
	"lease_from_reply",    // the lease is timed from the reply, not the send
	"no_backoff",          // no back-off after a lapse or an unheard campaign
	"one_lease_backoff",   // a lapse backs off one lease, not two (#973)
	"any_incarnation",     // a witness grant counts from any incarnation
	"any_sender",          // a grant counts from any sender
	"no_grantee_check",    // a grant meant for the other controller counts
	"no_nonce_check",      // a grant from an earlier boot counts
	"match_by_term",       // a grant counts toward a request of the same term, not id
	"renewals_unseen",     // a standby doesn't see renewals its voter refused (#973)
	"no_reterm",           // a refused leader never moves to a higher term (#973)
	"no_yield",            // a voter never yields its own candidate's promise (#979)
	"no_observe",          // a candidate doesn't see the peer's requested terms (#979)
	"retry_on_grid",       // a refused campaign retries on the look grid (#979)
}

var mutOn = os.Getenv("QUORUM_MUT")

func init() {
	if mutOn != "" && !slices.Contains(Mutations, mutOn) {
		panic(fmt.Sprintf("quorum: QUORUM_MUT=%q is not a known mutation", mutOn))
	}
}

func mut(name string) bool { return name == mutOn }
