package node

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

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
	// Drained: the outputs' drain finished, so the RESIGN was sent.
	Drained bool
	Witness bool
	Peer    bool
	// WitnessErr and PeerErr say why a voter did not take it ("" when it
	// did, or when the RESIGN was refused rather than lost).
	WitnessErr, PeerErr string
}

// DrainFunc ends every send that passed the write gate before it closed
// (a planned switchover's drain, package fence's Drain): it returns once
// each has ended and settle more has passed after the last one that went
// out (its bytes then delivered), or an error when that is unproven
// (ctx ended first). quorum/node knows no field driver: the caller hands
// the drain in (leader.Quorum.Resign).
type DrainFunc func(ctx context.Context, settle time.Duration) error

// ErrDrainUnproven: the outputs' drain did not finish, so no RESIGN left.
// This controller has stopped leading all the same: its lease runs out at
// the voters, and the standby takes over as after a failover.
var ErrDrainUnproven = errors.New("the outputs' drain did not finish: no RESIGN sent; the lease runs out as in a failover")

// Resign gives up this controller's leadership for a planned switchover
// (#710 d1), in this order:
//
//  1. In one step under mu its candidate stops leading (quorum.ResignAt),
//     and the gate closes. Its own voter keeps its promise to it.
//  2. drain ends every send that passed the gate before it closed, and
//     waits the fence margin (m_send + d_net) after the last one, so
//     nothing this leadership sent can arrive once its standby leads.
//     RESIGN removes the lease-and-fence gap that otherwise covers such a
//     send, so it may leave only after a drain that finished: one that
//     did not (an error, or longer than half a lease) sends no RESIGN,
//     and the lease runs out as in a failover (ErrDrainUnproven).
//  3. Its own voter drops its promise (quorum.ResignDrained), as the
//     RESIGN makes every voter do, only while the resigned lease would
//     still run: a drain that ended later (a stall) sends no RESIGN
//     either. Then the RESIGN goes to the witness first and to the peer's
//     voter after the witness's answer (or its timeout): the standby
//     campaigns as soon as its own voter takes it, and a campaign that
//     reached the witness before the witness's RESIGN would be refused,
//     its retry a look away.
//
// seq and current are the standby's acknowledgement of the final image,
// the mark the RESIGN carries. A controller that does not lead resigns
// nothing (and drains nothing). A nil drain is refused before anything.
func (e *Elector) Resign(ctx context.Context, seq uint64, current bool, drain DrainFunc) (ResignReport, error) {
	if drain == nil {
		return ResignReport{}, errors.New("redundancy: a resign needs the outputs' drain")
	}
	e.mu.Lock()
	now := e.now()
	r := quorum.ResignAt(now, e.v, e.c, seq, current)
	if !mut("resign_publish_after_send") {
		e.publish(now) // the gate is shut before the drain and the first RESIGN
	}
	e.mu.Unlock()
	if r == nil {
		return ResignReport{}, nil
	}
	e.log.Info("redundancy: resigned the lead for a planned switchover; draining the outputs", "epoch", r.Epoch, "finalSeq", seq, "current", current)
	rep := ResignReport{Resigned: true, Epoch: r.Epoch}
	drained := func() error {
		dctx, cancel := context.WithTimeout(ctx, e.o.Lease/2)
		defer cancel()
		return drain(dctx, e.Fence())
	}
	var derr error
	if !mut("resign_before_drain") {
		derr = drained()
	} else {
		defer drained()
	}
	if derr != nil && !mut("resign_despite_drain_error") {
		e.log.Warn("redundancy: the outputs' drain did not finish; no RESIGN sent, the lease runs out as in a failover", "epoch", r.Epoch, "error", derr)
		return rep, fmt.Errorf("%w: %v", ErrDrainUnproven, derr)
	}
	e.mu.Lock()
	freed := quorum.ResignDrained(e.now(), e.v, e.c, r) // only now does its own voter free its promise
	e.mu.Unlock()
	if !freed && !mut("resign_despite_drain_error") {
		// The drain outlasted the lease the resign cut short (the process
		// stalled): its own voter's promise may back a new campaign now.
		e.log.Warn("redundancy: the outputs' drain ended after the resigned lease; no RESIGN sent, the lease has run out as in a failover", "epoch", r.Epoch)
		return rep, fmt.Errorf("%w: the drain ended after the resigned lease", ErrDrainUnproven)
	}
	rep.Drained = true
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
	return rep, nil
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
