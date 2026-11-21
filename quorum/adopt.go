package quorum

import (
	"errors"
	"time"
)

// EventKind names something an Adoption reports for the operator.
type EventKind int

const (
	// EventAdopted: this controller durably adopted (Gen, Incarnation).
	EventAdopted EventKind = iota + 1
	// EventTwoIncarnations: two witness incarnations (Incarnation and
	// Other) answered this controller within a lease of each other. Two
	// copies of the witness are running: a misconfiguration the operator
	// must see (design §3.1).
	EventTwoIncarnations
	// EventRecommission: this controller is uncommissioned, has been up a
	// lease, and stays so until an operator re-commissions it by hand with
	// an explicit term floor (Recommission; design §3.1, §7). Either the
	// incarnation its peer committed, (Gen, Incarnation), has not answered
	// it (if that witness is gone for good, this is the two-of-three
	// case; it may also only be unreachable from here: check before
	// acting), or Incarnation is 0: the peer is uncommissioned too (both
	// files lost, or a new pair), and no surviving voter is sure to hold
	// the terms the pair led.
	EventRecommission
	// EventReadoptOverSwitch: this controller, pending a switch to Other,
	// took its uncommissioned peer's re-adoption of (Gen, Incarnation)
	// instead. If the peer had adopted Other with its ack lost, led on
	// it, then lost its file, and Other is gone too, the floor may miss
	// the terms it led (design §3.1, "The residual"): the operator should
	// know.
	EventReadoptOverSwitch
)

// Event is one thing an Adoption reports (Adoption.Events).
type Event struct {
	Kind             EventKind
	Gen, Incarnation uint64
	Other            uint64 // EventTwoIncarnations: the other incarnation
}

// maxHoldShift caps the switch hold-off at lease << 5 = 32 leases.
const maxHoldShift = 5

// Adoption is a controller's side of the witness-incarnation handshake
// (design §3.1, "The witness incarnation"; #976). It decides which witness
// incarnation the controller's Candidate counts, and when, and raises the
// term floor of its Voter. The two controllers adopt an incarnation only
// together, over the A–B link:
//
//   - A proposal (Propose) names a generation above the proposer's own;
//     generations only rise, so a late, duplicated or earlier-boot message
//     can't move a peer that has moved on. The peer adopts it, persists,
//     then acks (Ack); otherwise it answers with what it counts (State).
//   - Forward only: a proposer stops counting any witness, durably, before
//     it asks, and counts again only on the matching Ack.
//   - Quiet period: a newly adopted incarnation counts only one lease after
//     the adoption is durable, by when every lease granted under the old
//     voter set has ended.
//   - Term floor: both voters, and both candidates' highest term seen, rise
//     to the higher of the two floors before the new incarnation counts.
//   - Uncommissioned (no incarnation adopted: a new install, or a lost
//     file), the voter grants nothing. It re-adopts its commissioned
//     peer's incarnation at a new generation, after hearing the witness
//     answer at it, so its floor includes the witness's promised term, and
//     takes only messages sent to its current boot (Adopt.Echo). It never
//     takes the peer's switch, nor a pending one: only the incarnation the
//     peer committed (Adopt.CommitInc) knows the terms this controller led
//     before its file was lost. If that incarnation is gone it stays
//     uncommissioned (EventRecommission) until an operator re-commissions
//     it with an explicit term floor (Recommission; design §3.1, §7). The
//     same when the peer is uncommissioned too (both files lost, or a new
//     pair): nothing commissions without the operator.
//   - A restart owes the quiet period again (NewAdoption).
//
// Like Voter and Candidate it does no I/O and is not safe for concurrent
// use. The driver, per controller:
//
//   - creates it with NewAdoption after the Voter and the Candidate;
//   - passes every witness reply to OnWitness (after Candidate.OnReply);
//   - passes every Adopt from the peer to OnAdopt, and calls OnPeer for
//     every other message from the peer (a request or a reply);
//   - calls Tick at every look of the candidate;
//   - persists every *Persist returned, and reports writes to
//     Adoption.Persisted (not Voter.Persisted);
//   - sends every Adopt returned to the peer, and the probe to the witness;
//   - drains Events for the operator.
type Adoption struct {
	v *Voter
	c *Candidate

	self, peer, witness NodeID
	lease               time.Duration

	// heard is every witness incarnation that answered this controller in
	// this boot: when last, and the highest term it said it promised.
	heard map[uint64]heardInc
	// current is when the counted incarnation last answered (or this
	// boot, or its adoption): the silence a switch waits on.
	current Instant

	held   []heldAdopt
	commit *commit

	// switches counts adoptions of a new incarnation replacing another,
	// in this boot: the switch hold-off doubles with each (holdoff).
	// standing is when the last one was made; one that stands 2 × the cap
	// resets the count.
	switches int
	standing Instant
	lastTwo  Instant // the last EventTwoIncarnations
	events   []Event

	// peerNonce is the boot nonce of the peer's last message (Echo), and
	// peerAt when the peer was last heard from at all (OnPeer).
	peerNonce uint64
	peerAt    Instant

	// born is this boot; alarmed the peer's committed adoption last
	// raised as EventRecommission (raised once each).
	born    Instant
	alarmed [2]uint64
	alarmOn bool // alarmed holds a raised one
	// allow is the incarnation an operator's Recommission lets this
	// uncommissioned controller adopt although its peer never committed
	// it. In memory only: a restart before it is proposed or taken needs
	// the command again.
	allow uint64
}

type heardInc struct {
	at       Instant
	promised uint64
}

type heldAdopt struct {
	need uint64
	msg  Adopt
}

// commit is an adoption waiting for its write.
type commit struct {
	need     uint64
	gen, inc uint64
}

// NewAdoption starts the handshake for a controller at now, from the state
// its voter started with. It marks the voter as a controller's, which
// grants nothing while uncommissioned. A controller restarted with an
// adopted incarnation counts it only one lease after the restart: it may
// have adopted it just before it went down, so the quiet period is owed
// again. The peer may hold a lease granted under the old voter set by
// this node's own pre-restart voter, which the restart wait doesn't cover
// (#1008 review). One restarted with a proposal in flight counts none,
// and re-sends it.
func NewAdoption(v *Voter, c *Candidate, now Instant) *Adoption {
	v.commission = true
	a := &Adoption{v: v, c: c, self: c.cfg.Self, peer: c.cfg.Peer, witness: c.cfg.Witness,
		lease: c.cfg.Lease, heard: map[uint64]heardInc{}, current: now, standing: now, born: now}
	st := v.st
	switch {
	case st.PendingGen != 0 && !mut("count_while_pending"):
		c.Count(0, 0)
	case mut("restart_counts_at_once"):
		c.Count(st.Incarnation, now)
	default:
		c.Count(st.Incarnation, now.Add(a.lease))
	}
	c.raiseSeen(st.Floor)
	// No campaign while this controller's own voter is in its restart
	// wait: the campaign can't win without that vote, and it can take the
	// witness's first grant from a leader resting on the two controllers
	// (the witness in its own restart wait), whose renewals this voter
	// then refuses: the leader's lease lapses (#1021 review). It costs at
	// most one lease of takeover time after a restart.
	if !mut("campaign_in_restart_wait") {
		c.holdUntil = v.waitUntil
	}
	c.duplicated = v.Duplicated     // #1029 step 1
	v.ownHeld = c.Marked            // #1113 review, rule (ii)
	c.retiredReply = v.RetiredReply // #1029 3c-3b
	c.ownGen = func() uint64 { return v.st.Gen }
	c.commissioned = func() bool { return v.st.Incarnation != 0 }
	c.ownFences = v.Fences
	c.fenced = v.Fenced // #1029 3c-3c-1b
	return a
}

// Events returns what happened since the last call, and forgets it.
func (a *Adoption) Events() []Event {
	e := a.events
	a.events = nil
	return e
}

// OnWitness notes a witness reply this controller received at now,
// granted or refused: which incarnation answered, and the term it has
// promised.
func (a *Adoption) OnWitness(now Instant, r Reply) {
	if r.From != a.witness || r.To != a.self || r.Incarnation == 0 {
		return
	}
	h := a.heard[r.Incarnation]
	a.heard[r.Incarnation] = heardInc{at: now, promised: max(h.promised, r.Promised)}
	if r.Incarnation == a.v.st.Incarnation {
		a.current = now
	}
	if a.v.st.Incarnation == 0 && r.Fence != 0 && !mut("fence_unproposed") {
		// A replacement after --from-spare: the witness refuses its
		// probes as its name's older state; it re-adopts at the fence or
		// above (#1029 3c-3c-1b).
		a.ProposeAtLeast(r.Fence)
	}
	for inc, o := range a.heard {
		if inc != r.Incarnation && now < o.at.Add(a.lease) && (a.lastTwo == 0 || now >= a.lastTwo.Add(a.lease)) {
			a.lastTwo = now
			a.events = append(a.events, Event{Kind: EventTwoIncarnations, Incarnation: r.Incarnation, Other: inc})
			break
		}
	}
}

// holdoff is how long the counted incarnation must have been silent
// before this controller proposes a new one: a lease, doubling with each
// switch this boot, up to 32 leases, so a stray copy that answers on and
// off can't make the pair switch back and forth every lease.
func (a *Adoption) holdoff() time.Duration {
	if mut("no_holdoff") {
		return a.lease
	}
	return a.lease << min(a.switches, maxHoldShift)
}

// floorFor is the term floor this controller vouches for when it proposes
// or adopts: the highest term its voter promised, its floor, the highest
// its candidate heard of, and the highest any witness incarnation said it
// promised in this boot (a stray's only raises it).
func (a *Adoption) floorFor() uint64 {
	st := a.v.st
	f := max(st.Promised, st.Floor, a.c.maxSeen)
	for _, h := range a.heard {
		f = max(f, h.promised)
	}
	return f
}

// Fenced reports whether m comes from a state of the peer's name from
// before an operator replaced it (recommission --from-spare, #1029
// 3c-3c-1b): OnAdopt drops it unanswered, and the driver does not count it
// as the peer heard. Two shapes. A commissioned state below the fence
// (CommitGen): answered, an old box re-sending a stale proposal would
// re-propose above the fence and climb over it. A proposal below the
// fence from any state, an uncommissioned one included: an old disk that
// lost its file and died with a durable re-adoption proposal would else be
// acked, or answered and re-propose (#1164 review, B3). The replacement's
// query (Gen 0) is answered, with the fence (Adopt.Fence).
func (a *Adoption) Fenced(m Adopt) bool {
	if m.From != a.peer {
		return false
	}
	f := a.v.FenceOf(m.From)
	switch {
	case f == 0:
		return false
	case m.CommitInc != 0 && m.CommitGen < f && !mut("fence_adopt_off"):
		return true
	case m.Kind == Propose && m.Gen != 0 && m.Gen < f && !mut("fenced_propose_answered"):
		return true
	}
	return false
}

// OnPeer notes that a message from the peer (a request, a reply or an
// Adopt) arrived at now. A switch is proposed only while the peer has been
// heard within a lease: a proposer counts no witness until the peer acks,
// so proposing to a peer that is down would leave it with none.
func (a *Adoption) OnPeer(now Instant) { a.peerAt = now }

// Tick runs the handshake at the candidate's look: it re-sends a proposal
// in flight, asks an uncommissioned controller's peer what it counts, and
// proposes a rebuilt witness's new incarnation once the counted one has
// been silent for the hold-off. It returns a write to persist (the Adopt
// then comes from Persisted), messages for the peer, and, while
// uncommissioned, a probe for the witness: a term-0 request, always
// refused, whose reply says the witness's incarnation and promised term
// (a standby under a live leader sends the witness nothing else).
func (a *Adoption) Tick(now Instant) (*Persist, []Adopt, *Request) {
	for inc, h := range a.heard {
		if now >= h.at.Add(64*a.lease) {
			delete(a.heard, inc)
		}
	}
	if a.switches > 0 && now >= a.standing.Add(2*(a.lease<<maxHoldShift)) {
		a.switches = 0
	}
	st := a.v.st
	var probe *Request
	if st.Incarnation == 0 {
		probe = &Request{From: a.self, Nonce: a.c.cfg.Nonce, Fences: a.v.Fences(), Uncommissioned: true}
	}
	if len(a.held) > 0 {
		return nil, nil, probe // a message waits for its write; it goes then
	}
	switch {
	case st.PendingGen != 0:
		return a.flush(), a.hold(a.msg(Propose, st.PendingGen, st.PendingInc, a.floorFor(), a.peerNonce)), probe
	case st.Incarnation == 0:
		return nil, []Adopt{a.msg(Propose, 0, 0, 0, a.peerNonce)}, probe // a query
	}
	if now < a.current.Add(a.holdoff()) || (now >= a.peerAt.Add(a.lease) && !mut("switch_without_peer")) {
		return nil, nil, nil
	}
	// The counted incarnation has been silent for the hold-off: propose
	// the newest that answered within the last lease (a rebuilt witness).
	var next uint64
	var at Instant
	for inc, h := range a.heard {
		if inc != st.Incarnation && now < h.at.Add(a.lease) && (h.at > at || (h.at == at && inc > next)) {
			next, at = inc, h.at
		}
	}
	if next == 0 {
		return nil, nil, nil
	}
	p, out := a.propose(st.Gen+1, next)
	return p, out, nil
}

// msg builds an Adopt addressed to the peer's boot echo.
func (a *Adoption) msg(k AdoptKind, gen, inc, floor, echo uint64) Adopt {
	return Adopt{From: a.self, Kind: k, Gen: gen, Incarnation: inc, Floor: floor,
		Nonce: a.c.cfg.Nonce, Echo: echo, Pending: a.v.st.PendingGen,
		CommitGen: a.v.st.Gen, CommitInc: a.v.st.Incarnation, Operator: a.operator(k, inc), Fence: a.v.FenceOf(a.peer)}
}

// operator is the Adopt.Operator of a message: the floor of an operator's
// Recommission, on a proposal of the incarnation it named.
func (a *Adoption) operator(k AdoptKind, inc uint64) uint64 {
	if k != Propose || a.allow == 0 || inc != a.allow || a.v.st.Incarnation != 0 {
		return 0
	}
	return a.v.st.Floor
}

// propose asks the peer to adopt (gen, inc). Forward only: it stops
// counting any witness first, and the proposal leaves only once that is
// durable.
func (a *Adoption) propose(gen, inc uint64) (*Persist, []Adopt) {
	if !mut("count_while_pending") {
		a.c.Count(0, 0)
	}
	// An adoption still waiting for its write (a failed or slow one) is
	// superseded: the write that carries this proposal must not start
	// counting it (#1014 review).
	if !mut("propose_keeps_commit") {
		a.commit = nil
	}
	a.v.st.PendingGen, a.v.st.PendingInc = gen, inc
	// The floor the proposal vouches for is durable with it. A controller
	// that lost its file learnt part of it from the witness (the terms it
	// led before the loss); restarted with the proposal in flight, it has
	// heard nothing yet, and the peer's Ack carries only the peer's floor.
	if !mut("proposal_floor_volatile") && !mut("no_floor") {
		a.v.st.Floor = max(a.v.st.Floor, a.floorFor())
		a.c.raiseSeen(a.v.st.Floor)
	}
	p := a.v.change()
	return p, a.hold(a.msg(Propose, gen, inc, a.floorFor(), a.peerNonce))
}

// flush returns a write of the in-memory state when it isn't durable: a
// failed write is retried by the next message that depends on it.
func (a *Adoption) flush() *Persist {
	if a.v.durable >= a.v.dirty {
		return nil
	}
	return &Persist{Seq: a.v.dirty, State: a.v.st}
}

// hold returns m to send now when the voter's state is durable, or keeps
// it for Persisted: no Adopt leaves ahead of the state it vouches for.
func (a *Adoption) hold(m Adopt) []Adopt {
	if a.v.durable >= a.v.need {
		return []Adopt{m}
	}
	a.held = append(a.held, heldAdopt{need: a.v.need, msg: m})
	return nil
}

// OnAdopt takes a handshake message from the peer, received at now. It
// returns a write to persist and messages for the peer (any that depend
// on the write come from Persisted instead).
func (a *Adoption) OnAdopt(now Instant, m Adopt) (*Persist, []Adopt) {
	if m.From != a.peer {
		return nil, nil
	}
	if a.Fenced(m) {
		return nil, nil
	}
	st := a.v.st
	// fresh: m was sent to this boot. An uncommissioned controller (a lost
	// file) takes nothing that isn't: a proposal or an ack from before the
	// loss carries a floor that misses what was led since (#1008 review).
	fresh := m.Echo == a.c.cfg.Nonce || mut("no_adopt_echo")
	// The peer is heard, and its boot learnt, only from a message that
	// answers this boot: a late one sent before this controller restarted
	// would say the peer is up when it may be down (a switch then waits on
	// it), or name a boot the peer has left. A peer's first message after
	// its own restart echoes no boot of ours; the reply to it echoes its
	// nonce directly, and its next message echoes ours.
	learned := false
	if m.Echo == a.c.cfg.Nonce || mut("peer_from_any_adopt") {
		learned = a.peerNonce != m.Nonce
		a.peerAt, a.peerNonce = now, m.Nonce
	}
	if fresh && st.Incarnation == 0 {
		a.alarm(now, m)
		if m.Fence != 0 && !mut("fence_unproposed") {
			// The peer fenced this name (#1029 3c-3c-1b): this controller
			// is its replacement, and re-adopts at the fence or above.
			a.ProposeAtLeast(m.Fence)
			st = a.v.st
		}
	}
	switch m.Kind {
	case Propose:
		if m.Gen != 0 && m.Gen == st.Gen && m.Incarnation == st.Incarnation {
			return a.flush(), a.hold(a.msg(Ack, st.Gen, st.Incarnation, st.Floor, m.Nonce)) // a re-sent proposal
		}
		accept := m.Gen > st.Gen && m.Incarnation != 0 &&
			(st.PendingGen == 0 || m.Gen > st.PendingGen ||
				(m.Gen == st.PendingGen && m.Incarnation >= st.PendingInc))
		if mut("no_adopt_gen") {
			accept = m.Gen != 0 && m.Incarnation != 0
		}
		// A proposal is taken only by the boot it addresses (#1029
		// 3c-3c-1b): an old box an operator replaced, booted beside the
		// peer, must not adopt the peer's proposal meant for the
		// replacement and climb over its fence. The proposer learns this
		// boot from the State this refusal answers, and re-sends at its
		// next look.
		if !fresh && !mut("propose_any_echo") {
			accept = false
		}
		// Uncommissioned, adopt only a proposal sent to this boot, of an
		// incarnation heard answering in this boot (the floor needs its
		// promised term), and only a re-adoption of the incarnation the
		// peer committed: never its switch, nor a pending one. This
		// controller may have led terms on that incarnation that the peer
		// never saw, and only it knows them: re-commissioned on another,
		// at the peer's floor, it would vote in them again (the two-of-three
		// case, design §3.1). From a peer that is uncommissioned too (both
		// files lost, or a new pair) it takes only a proposal an operator
		// made (Adopt.Operator, with the operator's floor): no surviving
		// voter is sure to hold the terms the pair led. An operator's
		// Recommission here allows the incarnation it named.
		if st.Incarnation == 0 {
			var ok bool
			switch {
			case a.allow != 0 && m.Incarnation == a.allow:
				ok = true
			case m.CommitInc != 0:
				ok = m.Incarnation == m.CommitInc || mut("uncommissioned_takes_switch")
			default:
				ok = m.Operator != 0 || mut("both_lost_recommission")
			}
			if !fresh || !a.heardAt(m.Incarnation) || !ok {
				accept = false
			}
		}
		if !accept {
			return a.flush(), a.hold(a.msg(State, st.Gen, st.Incarnation, st.Floor, m.Nonce))
		}
		if st.Incarnation != 0 && st.PendingGen != 0 && m.CommitInc == 0 && m.Operator == 0 && m.Incarnation != st.PendingInc {
			a.events = append(a.events, Event{Kind: EventReadoptOverSwitch, Gen: m.Gen, Incarnation: m.Incarnation, Other: st.PendingInc})
		}
		p := a.adopt(now, m.Gen, m.Incarnation, max(m.Floor, m.Operator))
		return p, a.hold(a.msg(Ack, m.Gen, m.Incarnation, a.v.st.Floor, m.Nonce))
	case Ack:
		if fresh && st.PendingGen != 0 && m.Gen == st.PendingGen && m.Incarnation == st.PendingInc {
			return a.adopt(now, m.Gen, m.Incarnation, m.Floor), nil
		}
	case State:
		if !fresh {
			return nil, nil // it answers an earlier boot of ours
		}
		if m.Incarnation != 0 {
			// The peer counts (Gen, Incarnation). A proposal of ours it
			// can't take is dead (stale). An uncommissioned controller
			// re-adopts what its commissioned peer counts (lost), and gives
			// way when the peer has a proposal of its own at the same or a
			// higher generation (outbid): it re-proposes the peer's
			// incarnation above both, which the peer takes over its own,
			// so neither waits on the other for ever. Either way only
			// once it has heard the witness at it (the floor needs its
			// promised term).
			// An operator's Recommission names the incarnation instead.
			stale := st.PendingGen != 0 && m.Gen >= st.PendingGen
			above := max(m.Gen, m.Pending)
			if st.Incarnation == 0 {
				above = max(above, st.Gen) // ProposeAtLeast's
			}
			lost := st.Incarnation == 0 && st.PendingGen == 0
			outbid := st.Incarnation == 0 && st.PendingGen != 0 && m.Pending >= st.PendingGen
			inc := m.Incarnation
			if st.Incarnation == 0 && a.allow != 0 {
				inc = a.allow
			}
			if (stale || lost || outbid) && (st.Incarnation != 0 || a.heardAt(inc)) {
				return a.propose(above+1, inc)
			}
			if learned && st.PendingGen != 0 && !mut("propose_resent_at_look") {
				// This State is the first word from the peer's boot: the
				// proposal in flight addressed another (a proposal is taken
				// only by the boot it echoes). Re-sent now, not a look on;
				// once per peer boot, so a peer that refuses it for good
				// is not asked in a loop.
				return a.flush(), a.hold(a.msg(Propose, st.PendingGen, st.PendingInc, a.floorFor(), a.peerNonce))
			}
			return nil, nil
		}
		// Both uncommissioned (both files lost, or a new pair): only an
		// operator's Recommission commissions the pair. This controller
		// proposes the incarnation it names, once heard, with the
		// operator's floor; the peer takes it (Adopt.Operator).
		if st.Incarnation == 0 {
			inc := a.allow
			if mut("both_lost_recommission") {
				inc = a.newest() // before #710 B2: the one that answered last
			}
			if inc != 0 && a.heardAt(inc) && (st.PendingGen == 0 || inc != st.PendingInc) {
				return a.propose(max(m.Gen, m.Pending, st.Gen, st.PendingGen)+1, inc)
			}
		}
	}
	return nil, nil
}

// alarm raises EventRecommission for this uncommissioned controller once
// it has been up a lease (the witness has had time to answer its probe),
// on a message m sent to its boot: when the peer's committed incarnation
// has not answered it, or when the peer is uncommissioned too (m names
// none). Once per committed adoption the peer names.
func (a *Adoption) alarm(now Instant, m Adopt) {
	c := [2]uint64{m.CommitGen, m.CommitInc}
	if now < a.born.Add(a.lease) || (a.alarmOn && c == a.alarmed) {
		return
	}
	if m.CommitInc != 0 && a.heardAt(m.CommitInc) && !mut("alarm_on_heard") {
		return // it can re-adopt it
	}
	a.alarmed, a.alarmOn = c, true
	a.events = append(a.events, Event{Kind: EventRecommission, Gen: m.CommitGen, Incarnation: m.CommitInc})
}

// ErrCommissioned: Recommission was called on a controller that has an
// adopted incarnation; there is nothing to re-commission.
var ErrCommissioned = errors.New("quorum: controller is commissioned")

// ErrNoFloor: Recommission was called with incarnation or floor 0.
var ErrNoFloor = errors.New("quorum: re-commission needs an incarnation and a term floor")

// ErrFloorTooLow: Recommission was given a floor at or below what the
// controller already knows was promised: the operator's floor must add a
// margin above it.
var ErrFloorTooLow = errors.New("quorum: re-commission floor is not above the terms this controller knows")

// Recommission is the operator's way out when this controller can't
// re-commission on its own (EventRecommission, design §7): its peer's
// committed incarnation is gone, or the peer is uncommissioned too. No
// surviving voter is sure to know the terms the pair led. The operator
// supplies a term floor above every term the pair could have used (it
// must be above what this controller knows, floorFor, or ErrFloorTooLow),
// and the incarnation to adopt (the witness's). The floor is made durable
// at once (the returned write) and raises what this controller vouches
// for; the adoption itself is the ordinary handshake, through the peer,
// which takes the floor with it: this controller adopts inc when the peer
// proposes it, or proposes it to the peer (marked Adopt.Operator, so an
// uncommissioned peer takes it too), once it has heard inc answer. Once
// per pair is enough. The permission is held in memory: after a restart
// the operator issues the command again, with a floor above KnownFloor
// (the first floor stays, and the candidate's campaigns may have raised
// the witness's promise since); a proposal still pending then leaves
// marked again. The command and its audit record are the caller's (the
// runtime, #710 PR 3, audited under #709).
func (a *Adoption) Recommission(inc, floor uint64) (*Persist, error) {
	if a.v.st.Incarnation != 0 {
		return nil, ErrCommissioned
	}
	if inc == 0 || floor == 0 {
		return nil, ErrNoFloor
	}
	if floor <= a.floorFor() && !mut("recommission_any_floor") {
		return nil, ErrFloorTooLow
	}
	a.allow = inc
	a.v.st.Floor = max(a.v.st.Floor, floor)
	a.c.raiseSeen(a.v.st.Floor)
	return a.v.change(), nil
}

// ProposeAtLeast makes this uncommissioned controller's next proposal at
// generation gen or above: an operator's re-commission after an earlier
// recommission --from-spare left a fence at a voter that kept its state
// (Voter.Fence), which refuses every state below it (#1029 3c-3c-1b). The
// next write (Recommission's) carries it. Nothing on a commissioned
// controller, or for gen 0.
func (a *Adoption) ProposeAtLeast(gen uint64) {
	if a.v.st.Incarnation != 0 || gen == 0 || a.v.st.Gen >= gen-1 {
		return
	}
	a.v.st.Gen = gen - 1
}

// KnownFloor is the highest term this controller knows was promised
// (floorFor): an operator's Recommission floor must be above it.
func (a *Adoption) KnownFloor() uint64 { return a.floorFor() }

func (a *Adoption) heardAt(inc uint64) bool {
	_, ok := a.heard[inc]
	return ok
}

// newest is the incarnation that answered last (the higher id on a tie),
// or 0.
func (a *Adoption) newest() uint64 {
	var inc uint64
	var at Instant
	for i, h := range a.heard {
		if h.at > at || (h.at == at && i > inc) {
			inc, at = i, h.at
		}
	}
	return inc
}

// adopt takes (gen, inc) as the adoption, raises the term floor, and
// returns the write; the incarnation counts once it is durable
// (Persisted), after the quiet period if it is a new one. A new
// incarnation stops the old one counting now, before any Ack leaves.
func (a *Adoption) adopt(now Instant, gen, inc, peerFloor uint64) *Persist {
	st := &a.v.st
	// A new incarnation stops the old one counting now, before any Ack
	// leaves. Over a proposal in flight nothing counts already (forward
	// only), so re-adopting the old incarnation then also waits for its
	// quiet period (Persisted): the peer may have adopted the proposed one
	// with its Ack lost, and may lead on it still (#1008 re-review).
	quiet := inc != st.Incarnation
	if quiet && st.Incarnation != 0 {
		a.switches++
		a.standing = now
	}
	// The hold-off runs from this adoption, durable or not: a failed write
	// must not leave it measured from boot (#1014 review).
	if !mut("holdoff_from_boot") {
		a.current = now
	}
	floor := max(peerFloor, a.floorFor())
	st.Gen, st.Incarnation, st.PendingGen, st.PendingInc = gen, inc, 0, 0
	a.allow = 0
	if !mut("no_floor") {
		st.Floor = max(st.Floor, floor)
		a.c.raiseSeen(st.Floor)
	}
	if quiet {
		a.c.Count(0, 0)
	}
	p := a.v.change()
	a.commit = &commit{need: p.Seq, gen: gen, inc: inc}
	return p
}

// Persisted reports the driver's write of Seq seq, ok when durable, at
// now. It returns the grants (Voter.Persisted) and the Adopt messages the
// write releases, and starts counting an adoption the write made durable.
func (a *Adoption) Persisted(now Instant, seq uint64, ok bool) ([]Reply, []Adopt) {
	replies := a.v.Persisted(seq, ok)
	var out []Adopt
	keep := a.held[:0]
	for _, h := range a.held {
		switch {
		case h.need > seq:
			keep = append(keep, h)
		case ok:
			out = append(out, h.msg)
		}
	}
	a.held = keep
	if ok && a.commit != nil && seq >= a.commit.need && a.v.st.PendingGen != 0 && !mut("count_over_pending") {
		a.commit = nil // forward only: nothing counts while a proposal is in flight
	}
	if ok && a.commit != nil && seq >= a.commit.need {
		// The incarnation counts one lease after the write, unless this
		// controller was already counting it (a generation bump that kept
		// it): then it goes on as before, and a quiet period still owed (a
		// restart's, NewAdoption) is not cut short. Judged on what the
		// candidate counts now, not on this adoption alone: an earlier
		// adoption of a new incarnation still waiting for this same write
		// stopped the counting, and owes its quiet period here.
		from := now.Add(a.lease)
		if inc, f := a.c.Counting(); inc == a.commit.inc {
			from = max(now, f)
			if mut("bump_cuts_quiet") {
				from = now
			}
		}
		if mut("no_quiet") {
			from = now
		}
		a.c.Count(a.commit.inc, from)
		a.current = now
		a.events = append(a.events, Event{Kind: EventAdopted, Gen: a.commit.gen, Incarnation: a.commit.inc})
		a.commit = nil
	}
	return replies, out
}
