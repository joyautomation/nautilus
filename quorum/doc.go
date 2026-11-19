// Package quorum is the vote protocol of a redundant pair: two controllers
// and a witness, each a voter, and each controller also a candidate
// (docs/design/redundancy.md §3.1, §3.3).
//
// The package is sans-IO. Voter and Candidate take (now, event) and return
// what to do: requests or replies to send, state to persist. No goroutine,
// socket or file lives here, so the production driver (naut run, naut
// witness) and the deterministic simulator (quorum/sim) run exactly the
// same code. Two rules that the safety argument rests on are enforced here
// rather than left to a driver:
//
//   - A voter's grant exists only after the state it promises is durable:
//     Voter.Handle hands back state to persist, and the grant comes out of
//     Voter.Persisted, never earlier (fsync before answer).
//   - A lease is timed from the send instant of ONE request that a majority
//     granted, matched by request id, shortened by the clock-rate margin ρ.
//     A late grant for an earlier request never counts toward a later one.
//
// Times are Instants: durations on the node's own clock since an arbitrary
// epoch (CLOCK_BOOTTIME on Linux, see Now). Wall-clock times are never
// compared across nodes. ρ bounds the RELATIVE rate of any two voters'
// clocks.
//
// Driver contract: the now passed to Handle or OnReply is read after the
// message is in hand, and the now passed with a request's Sent is read
// before it leaves.
package quorum
