package quorum

import "time"

// Persist asks the driver to write State durably (fsync) and then call
// Voter.Persisted with Seq. The driver may coalesce: persisting a later
// Seq covers every earlier one.
type Persist struct {
	Seq   uint64
	State VoterState
}

// Voter is one voter's state machine (design §3.1). Every controller and
// the witness runs one. Not safe for concurrent use; the driver serialises
// calls.
type Voter struct {
	self  NodeID
	lease time.Duration

	st           VoterState // in memory: what this voter has promised
	promiseUntil Instant    // when the promise to st.GrantedTo runs out
	waitUntil    Instant    // the restart wait: no grant before this

	// dirty is the Seq of the last change to st, durable the highest Seq
	// the driver has confirmed. A grant waits until durable covers the
	// state it promises (held), so no grant ever leaves ahead of its fsync.
	dirty, durable uint64
	held           []heldGrant

	// isWitness: this voter is the witness, and stamps its incarnation on
	// every reply. Set by NewWitness.
	isWitness bool
	// commission: this is a controller's voter run with an Adoption, so it
	// grants nothing while uncommissioned (no incarnation adopted: a new
	// install or a lost file, #976). Set by NewAdoption.
	commission bool

	// renewalFrom renewed a lease through this voter last, granted or
	// not, and renewalUntil is one lease after it arrived. In memory only:
	// a restarted voter's candidate learns it again from the next renewal.
	renewalFrom  NodeID
	renewalUntil Instant
}

type heldGrant struct {
	need  uint64
	reply Reply
}

// NewVoter starts a voter at now from the state its file held: st is the
// zero VoterState when there was no file or it could not be read. Either
// way the voter grants nothing for one full lease. It has lost its
// promise timer, and any promise it made before the restart has run out
// by then (design §3.1).
func NewVoter(self NodeID, lease time.Duration, st VoterState, now Instant) *Voter {
	wait := now.Add(lease)
	if mut("restart_grants") {
		wait = now
	}
	return &Voter{self: self, lease: lease, st: st, waitUntil: wait}
}

// NewWitness starts the witness's voter, like NewVoter. A witness with no
// readable state is a new incarnation: it draws one with draw (a random
// non-zero id) and persists it before it can grant, so the controllers
// count none of its grants until they adopt it (design §3.1). The first
// Persist is returned with the voter; until the driver confirms it, the
// witness grants nothing anyway (the restart wait is longer).
func NewWitness(self NodeID, lease time.Duration, st VoterState, now Instant, draw func() uint64) (*Voter, *Persist) {
	v := NewVoter(self, lease, st, now)
	v.isWitness = true
	if st.Incarnation != 0 {
		return v, nil
	}
	inc := draw()
	for inc == 0 {
		inc = draw()
	}
	v.st = VoterState{Incarnation: inc}
	v.dirty++
	return v, &Persist{Seq: v.dirty, State: v.st}
}

// State returns the voter's in-memory state.
func (v *Voter) State() VoterState { return v.st }

// PromiseUntil returns the term promised, to whom, and until when on this
// node's clock. It is NOT what a candidate waits on: it misses renewals
// this voter refused. Candidate.Tick takes LeaderSeen.
func (v *Voter) PromiseUntil() (term uint64, to NodeID, until Instant) {
	return v.st.Promised, v.st.GrantedTo, v.promiseUntil
}

// LeaderSeen returns the other candidate this node last saw holding a
// lease, and until when on this node's clock: the later of the promise
// this voter gave it and one lease after the last renewal it sent here,
// granted or refused (a voter in its restart wait, or still promised to
// its own candidate, refuses a live leader's renewals but sees them). A
// standby's candidate campaigns only once that has run out
// (Candidate.Tick), so it doesn't campaign under a live leader. ("", 0)
// when it has seen none.
func (v *Voter) LeaderSeen() (NodeID, Instant) {
	var from NodeID
	var until Instant
	if v.st.GrantedTo != "" && v.st.GrantedTo != v.self {
		from, until = v.st.GrantedTo, v.promiseUntil
	}
	if v.renewalFrom != "" && v.renewalUntil > until && !mut("renewals_unseen") {
		from, until = v.renewalFrom, v.renewalUntil
	}
	return from, until
}

// wouldYield reports whether r, received at now, asks for a term strictly
// above the one this voter promised to its OWN candidate, while that
// promise runs (YieldTo).
func (v *Voter) wouldYield(now Instant, r Request) bool {
	return r.From != v.self && v.st.GrantedTo == v.self && now >= v.waitUntil &&
		now < v.promiseUntil && r.Term > v.st.Promised
}

// releaseSelf ends this voter's running promise to its own candidate for
// r, re-checking wouldYield itself rather than trusting the caller: had
// the candidate campaigned again since it yielded, its voter's promise
// would back that open round, and is kept (it then refuses r as usual).
// The promised term stays: terms only rise.
func (v *Voter) releaseSelf(now Instant, r Request) bool {
	if !v.wouldYield(now, r) {
		return false
	}
	v.promiseUntil = 0
	return true
}

// YieldTo lets two counted voters elect. Sometimes only the two
// controllers count (the witness down, at an incarnation not adopted, or
// a controller that lost the one it accepts). Each campaign then makes
// the own voter promise itself for a whole lease, and the two would
// refuse each other for ever. So when v's running promise is to its own
// candidate c and the other controller asks for a strictly higher term,
// c gives up every open round (unless it leads, when nothing happens),
// and only then does v drop its promise, in one call. Nothing can run in
// between, so the dropped promise never backs a lease. Equal terms never
// yield: one term, one grantee. YieldTo reports whether it yielded; the
// driver calls it, then Handle, for every request from the other
// controller, after Candidate.Observe. The witness has no candidate and
// never yields.
func YieldTo(now Instant, v *Voter, c *Candidate, r Request) bool {
	if mut("no_yield") || v == nil || c == nil || r.From != c.cfg.Peer || c.cfg.Self != v.self || !v.wouldYield(now, r) {
		return false
	}
	if !c.yield(now) {
		return false
	}
	return v.releaseSelf(now, r)
}

// Handle answers r, received at now. A refusal comes back at once. A grant
// that changes the persisted state comes back as a Persist, and the grant
// itself only from Persisted. A renewal that changes nothing durable is
// answered at once, unless an earlier change is still unconfirmed.
func (v *Voter) Handle(now Instant, r Request) (*Persist, []Reply) {
	refuse := []Reply{v.reply(r, false)}
	if r.Renewal && r.From != v.self && now.Add(v.lease) > v.renewalUntil {
		v.renewalFrom, v.renewalUntil = r.From, now.Add(v.lease)
	}
	if now < v.waitUntil || r.Term < v.st.Promised || r.Term == 0 || r.Term <= v.st.Floor {
		return nil, refuse // a term-0 request is a probe: always refused
	}
	if v.commission && v.st.Incarnation == 0 && !mut("uncommissioned_grants") {
		return nil, refuse // uncommissioned: no floor yet, so no grant (#976)
	}
	active := now < v.promiseUntil
	switch {
	case r.Term == v.st.Promised && v.st.GrantedTo != r.From:
		return nil, refuse // this term is someone else's
	case r.Term > v.st.Promised && active && v.st.GrantedTo != r.From:
		return nil, refuse // promised to another, and not run out
	}
	// Grant. The promise runs from when the request was RECEIVED.
	v.promiseUntil = now.Add(v.lease)
	if r.Term != v.st.Promised || r.From != v.st.GrantedTo {
		v.st.Promised, v.st.GrantedTo = r.Term, r.From
		v.dirty++
	}
	g := v.reply(r, true)
	if v.durable >= v.dirty {
		return nil, []Reply{g}
	}
	if mut("answer_before_fsync") {
		return &Persist{Seq: v.dirty, State: v.st}, []Reply{g}
	}
	v.held = append(v.held, heldGrant{need: v.dirty, reply: g})
	return &Persist{Seq: v.dirty, State: v.st}, nil
}

// Persisted reports the driver's write of Seq seq: ok when it is durable.
// It releases every grant that write covers. On a failed write the grants
// waiting on it are dropped, never sent (F25); the in-memory promise
// stands, which only refuses others, and the next grant retries the write.
func (v *Voter) Persisted(seq uint64, ok bool) []Reply {
	var out []Reply
	keep := v.held[:0]
	for _, h := range v.held {
		switch {
		case ok && h.need <= seq:
			out = append(out, h.reply)
		case !ok && h.need <= seq:
			// dropped
		default:
			keep = append(keep, h)
		}
	}
	v.held = keep
	if ok && seq > v.durable {
		v.durable = seq
	}
	return out
}

func (v *Voter) reply(r Request, granted bool) Reply {
	return Reply{From: v.self, To: r.From, Term: r.Term, Nonce: r.Nonce, ID: r.ID, Granted: granted,
		Promised: max(v.st.Promised, v.st.Floor), Incarnation: v.witnessIncarnation()}
}

// change marks the in-memory state changed and returns the write that
// makes it durable.
func (v *Voter) change() *Persist {
	v.dirty++
	return &Persist{Seq: v.dirty, State: v.st}
}

// witnessIncarnation is what this voter stamps on its replies: the
// witness's incarnation, or 0 from a controller.
func (v *Voter) witnessIncarnation() uint64 {
	if v.isWitness {
		return v.st.Incarnation
	}
	return 0
}
