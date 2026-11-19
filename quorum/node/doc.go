// Package node runs the quorum protocol on real sockets, files and clocks:
// a controller's Elector and the Witness (docs/design/redundancy.md §3.1,
// §6.1). Package quorum is the protocol itself and does no I/O; this is its
// production driver, as quorum/sim is its simulated one, and both follow
// the same driver contract (quorum.Adoption's doc):
//
//   - every request from the peer goes through Adoption.OnPeer,
//     Candidate.Observe, quorum.YieldTo and Voter.Handle, in that order;
//   - a reply goes to Candidate.OnReply, then to Adoption.OnPeer (from the
//     peer) or Adoption.OnWitness (from the witness);
//   - an Adopt from the peer goes to Adoption.OnAdopt;
//   - Adoption.Tick runs at every look of the candidate, and its probe goes
//     to the witness only;
//   - every Persist is written to the state file (fsync) before the write
//     is reported with Adoption.Persisted (Voter.Persisted on the witness),
//     and only then do the grants and Adopts it holds leave;
//   - Adoption.Events are drained after every call.
//
// The now passed with an event is read after the message is in hand; the
// now a request's lease is timed from is read before it is sent
// (quorum's doc).
//
// # Transport
//
// Every message travels over HTTP on the peer listener (Guard), never
// behind the tag API's handler: a vote is POST {pair}/vote with a Request
// frame, answered with the Reply frame; an Adopt is POST {pair}/adopt,
// answered 204; the client-token check is GET {pair}/hello. Each call has a
// deadline of lease/8 and connections are kept alive. Every request carries
// the peer token (NAUTILUS_PEER_TOKEN) in X-Nautilus-Cluster, as oplog's
// replication does, and its sender's lease in X-Nautilus-Lease: a voter
// refuses a request whose lease is longer than its own promise, since the
// lease would outlive the promise it rests on.
//
// # Files
//
// A voter's state is one small file, written whole to a temporary file
// beside it, fsynced, renamed over it, and the directory fsynced (State),
// so a crash at any instant leaves the old state or the new one. A file
// that can't be read is no state: a controller starts uncommissioned and
// grants nothing until it re-adopts (#976), and the witness refuses to
// start over it unless told to rebuild.
//
// # The boot nonce
//
// A controller's candidate draws its nonce from crypto/rand at every start
// (New) and never writes it anywhere: requests carry it and grants echo
// it, so a grant to an earlier boot of the same controller never counts
// toward this boot's lease.
package node
