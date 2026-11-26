//go:build quorum_mut

package modbus

import "os"

// Mutations of the write path under a redundant pair (test plan §3.3),
// switched on like package quorum's (build tag quorum_mut,
// QUORUM_MUT=<name>):
//
//   - old_term_kept: a look that finds the gate open under another term of
//     the lease is not a reopen, so a write queued (or requeued) under the
//     earlier term still goes out (#1040 review).
//   - hold_forget_nothing: ForgetOutputs forgets nothing, so a rewrite:
//     binding re-asserts an earlier leadership's value under the takeover
//     hold (#710 (b2)).
//   - hold_safe_never_lands: a safe value's write landing never marks it
//     delivered (#710 (b2)).
var mutOn = os.Getenv("QUORUM_MUT")

func mut(name string) bool { return name == mutOn }
