package node

import (
	"context"
	"errors"
	"time"

	"github.com/joyautomation/nautilus/quorum"
)

// The bad-retain hand-back's election side (#710 S2c-3): a controller
// that just won a lease and must not run on its own store (cmd/naut
// decides) hands the lead to its peer for a while: its gate closes, it
// renews and campaigns no more until the hold is over, and then it
// campaigns as usual, so a peer that never takes the lead costs one hold,
// not the plant. The time of the hand-back is written to the voter state
// first, so a crash loop hands back once per window (quorum.HandBackHeld).

// EventHandedBack: this controller handed the lead to its peer, its own
// retain store unreadable. Detail says why.
const EventHandedBack EventKind = "redundancy.handed-back"

// ErrNotLeadingEpoch refuses a hand-back of an epoch this controller does
// not lead (any more).
var ErrNotLeadingEpoch = errors.New("quorum: this controller does not lead that epoch")

// handingBack reports whether a hand-back's hold is on at now. Under mu.
func (e *Elector) handingBack(now quorum.Instant) bool {
	return now < e.handBackUntil && !mut("handback_keeps_renewing")
}

// HandBack hands the lead of epoch to the peer for hold: the time is
// written durably first (it returns the write's error and hands nothing
// back if it fails), then the gate closes and this controller neither
// renews nor campaigns until the hold is over. detail goes in the event.
func (e *Elector) HandBack(ctx context.Context, epoch uint64, hold time.Duration, detail string) error {
	e.mu.Lock()
	if !e.c.Leading(e.now()) || e.c.Term() != epoch {
		e.mu.Unlock()
		return ErrNotLeadingEpoch
	}
	var p *quorum.Persist
	prev := e.v.HandedBackMs()
	if !mut("handback_guard_volatile") {
		p = e.v.NoteHandBack(time.Now().UnixMilli())
	}
	e.mu.Unlock()
	undo := func() {
		if !mut("handback_guard_kept_on_failure") {
			e.v.UndoHandBack(prev)
		}
	}
	if p != nil {
		e.w.ask(p)
		if err := e.waitDurable(ctx, p.Seq); err != nil {
			e.mu.Lock()
			undo()
			e.mu.Unlock()
			return err
		}
	}
	e.mu.Lock()
	now := e.now()
	if !e.c.Leading(now) || e.c.Term() != epoch {
		undo()
		e.mu.Unlock()
		return ErrNotLeadingEpoch
	}
	e.handBackUntil = now.Add(hold)
	e.publish(now)
	e.mu.Unlock()
	e.log.Warn("redundancy: handed the lead back to "+string(e.o.Peer), "epoch", epoch, "hold", hold, "why", detail)
	e.event(Event{Kind: EventHandedBack, Detail: detail})
	e.poke()
	return nil
}

// HandBackHeld reports whether this controller handed back within the
// guard's window (quorum.HandBackHeld), and when it last did.
func (e *Elector) HandBackHeld() (bool, time.Time) {
	e.mu.Lock()
	ms := e.v.HandedBackMs()
	e.mu.Unlock()
	var at time.Time
	if ms != 0 {
		at = time.UnixMilli(ms)
	}
	return quorum.HandBackHeld(ms, time.Now().UnixMilli(), e.o.Lease), at
}

// HandBackFloor is the newest earlier leadership the voters of this
// controller's winning round know of (quorum.HotCheck.HandBackFloor).
func (e *Elector) HandBackFloor() (uint64, bool, string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.c.Leading(e.now()) {
		return 0, false, quorum.HandBackNotLeading
	}
	return e.c.HotCheck().HandBackFloor()
}

// MayLead says whether this controller could take the lead now and keep
// it ("" when it could), as its peer's hand-back asks: a controller that
// would not campaign, or would hand straight back, is no one to hand to.
func (e *Elector) MayLead() string {
	if e.spare.Load() {
		return "this controller is a spare"
	}
	ls, hasLog := e.logState() // outside mu: the log calls into the election
	e.mu.Lock()
	h := e.health
	e.mu.Unlock()
	if h != nil && !h() {
		return "this controller is unhealthy"
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.mayLeadLocked(e.now(), true, ls, hasLog)
}
