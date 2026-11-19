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
	"gate_on_tick",              // the gate is judged on the elector's look, not when it is read
	"peer_host_trusted",         // the peer listener answers a trusted Host without the peer token
	"persisted_before_write",    // a write is reported durable before it is on disk
	"state_in_place",            // the state file is rewritten in place, so a crash tears it
	"nonce_fixed",               // the boot nonce is the same at every start
	"lease_unchecked",           // a voter grants a request whose lease outlives its own promise
	"witness_rebuilds_silently", // the witness draws a new incarnation over a state file it can't read
}

var mutOn = os.Getenv("QUORUM_MUT")

func mut(name string) bool { return name == mutOn }

// coreMutation reports whether QUORUM_MUT names a mutation of package
// quorum rather than of this driver. The loopback tests skip then: the
// core's own suite and the simulator catch those, and the runner runs the
// whole tree once per mutation.
func coreMutation() bool { return mutOn != "" && !slices.Contains(mutations, mutOn) }
