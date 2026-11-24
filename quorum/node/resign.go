package node

import (
	"context"
	"errors"
	"net/http"

	"github.com/joyautomation/nautilus/quorum"
)

// A planned switchover's election side (#710 d1): this controller gives
// up its leadership and tells the voters, so its standby takes over
// without waiting the lease out (quorum.ResignAt, Voter.handleResign).

// ResignReport says how a resign went: whether this controller resigned
// (it led), the epoch it resigned, and which voters took the RESIGN. A
// voter that did not (unreachable, or it refused) holds its promise to
// the lease's end: the standby then waits it out, as before.
type ResignReport struct {
	Resigned bool
	Epoch    uint64
	Witness  bool
	Peer     bool
	// WitnessErr and PeerErr say why a voter did not take it ("" when it
	// did, or when the RESIGN was refused rather than lost).
	WitnessErr, PeerErr string
}

// resign gives up this controller's leadership: in one step under mu its
// candidate stops leading and its own voter drops its promise, and the
// gate closes; only then does the RESIGN leave. It goes to the witness
// first and to the peer's voter after the witness's answer (or its
// timeout): the standby campaigns as soon as its own voter takes the
// RESIGN, and a campaign that reached the witness before the witness's
// RESIGN would be refused, its retry a look away. seq and current are
// the standby's acknowledgement of the final image, the mark the RESIGN
// carries. A controller that does not lead resigns nothing.
//
// Unexported until #710 d1b: RESIGN removes the lease-and-fence gap that
// lets a send that passed the gate just before finish, so a caller must
// first have drained every output path (package fence). d1b gives the
// exported form a drained token to prove it.
func (e *Elector) resign(ctx context.Context, seq uint64, current bool) ResignReport {
	e.mu.Lock()
	now := e.now()
	r := quorum.ResignAt(now, e.v, e.c, seq, current)
	if !mut("resign_publish_after_send") {
		e.publish(now) // the gate is shut before the first RESIGN leaves
	}
	e.mu.Unlock()
	if r == nil {
		return ResignReport{}
	}
	e.log.Info("redundancy: resigned the lead for a planned switchover", "epoch", r.Epoch, "finalSeq", seq, "current", current)
	rep := ResignReport{Resigned: true, Epoch: r.Epoch}
	order := []struct {
		to   quorum.NodeID
		base string
		took *bool
		err  *string
	}{
		{e.o.Witness, e.wit, &rep.Witness, &rep.WitnessErr},
		{e.o.Peer, e.peer, &rep.Peer, &rep.PeerErr},
	}
	if mut("resign_peer_first") {
		order[0], order[1] = order[1], order[0]
	}
	for _, v := range order {
		took, err := e.resignTo(ctx, v.to, v.base, *r)
		*v.took = took
		if err != nil {
			*v.err = err.Error()
		}
	}
	return rep
}

// errResignNoAnswer: the voter's answer could not be read as one.
var errResignNoAnswer = errors.New("no answer from the voter")

// resignTo sends the RESIGN to one voter and waits for its answer, at
// most lease/8 (a vote's own timeout). It reports whether the voter took
// it. The answer is heard as any vote reply is (its relayed promise).
func (e *Elector) resignTo(ctx context.Context, to quorum.NodeID, base string, r quorum.Request) (bool, error) {
	if ctx == nil || base == "" {
		return false, errResignNoAnswer
	}
	b, _ := r.MarshalBinary()
	body, _, err := e.call.callHeader(ctx, e.o.Lease/8, http.MethodPost, base+Prefix+e.o.Cluster+"/vote", b)
	if err != nil {
		return false, err
	}
	var rep quorum.Reply
	if body == nil || rep.UnmarshalBinary(body) != nil || rep.From != to {
		return false, errResignNoAnswer
	}
	e.reply(to, rep)
	return rep.Granted, nil
}

// EndResignHold ends this controller's resign hold (quorum
// Candidate.EndResignHold): its peer is handing the lead back to it.
func (e *Elector) EndResignHold() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.c.EndResignHold(e.now())
}
