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
	"gate_on_tick",               // the gate is judged on the elector's look, not when it is read
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
	"term_cap_narrow",            // the term-jump cap is 2^20: an isolated controller crosses it in about 50 days and is refused for good
	"state_owner_unchecked",      // a controller starts over a state file another member (or another pair) wrote
	"config_differs_campaigns",   // a controller campaigns while its peer's hello names another configuration of the pair
	"hello_duplicate_unseen",     // two processes behind the peer's name (nonces N1, N2, N1 at the hello) raise nothing
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
