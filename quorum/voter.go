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

	// grantNonce is the boot nonce the running promise was granted under:
	// while it runs, the same name under another nonce is a second process
	// claiming that name (design §2), refused and counted in duplicates.
	// In memory only, like promiseUntil: a restarted voter grants nothing
	// until every promise it made has run out anyway.
	grantNonce uint64
	duplicates uint64
	dupFrom    NodeID
	dupCause   DupCause

	// heard are the running promises other voters relayed (Hear), in
	// memory only: a restarted voter grants nothing for a lease, by when
	// every promise it had heard of has run out (#1029 layer 2).
	heard        []heardPromise
	relayRefused uint64
	// pushedTo, pushedNonce: the promise RelayChanged last reported.
	pushedTo    NodeID
	pushedNonce uint64
}

// heardPromise is a promise voter by relayed: to grantee to under nonce,
// counted a duplicate's sign until strict (receipt + left), and refusing
// until until (left stretched by the clock-rate margin).
type heardPromise struct {
	by, to        NodeID
	nonce         uint64
	strict, until Instant
}

// DupCause says why the voter's last duplicate was refused (Duplicates).
type DupCause uint8

const (
	// DupNonce: the name came under a boot nonce other than the one a
	// running promise to it was granted under, this voter's own or one it
	// heard relayed (design §2, #1028).
	DupNonce DupCause = iota
	// DupVoterPromised: the name campaigned while its own voter was heard
	// promised to another node. A real controller never campaigns then
	// (Candidate.Tick waits out its voter's promise), so another process
	// runs under its name (#1029 layer 2, rule (b)).
	DupVoterPromised
)

type heldGrant struct {
	need  uint64
	reply Reply
}

// NewVoter starts a voter at now from the state its file held: st is the
// zero VoterState when there was no file or it could not be read. Either
// way the voter grants nothing for one full lease. It has lost its
// promise timer, and any promise it made before the restart has run out
// by then (design §3.1).
//
// The wait is the longer of lease and the lease st says its last promises
// were made under (VoterState.Lease): a voter restarted with a shorter
// lease would otherwise grant inside a promise made under the longer one
// (#1021 review). Until its first grant the state keeps the longer lease,
// so a write before then (an adoption) doesn't shorten the next restart's
// wait.
func NewVoter(self NodeID, lease time.Duration, st VoterState, now Instant) *Voter {
	bound := max(lease, st.Lease)
	wait := now.Add(bound)
	if mut("restart_wait_current_lease") {
		wait = now.Add(lease)
	}
	if mut("restart_grants") {
		wait = now
	}
	v := &Voter{self: self, lease: lease, st: st, waitUntil: wait}
	if st.Lease != bound && !mut("lease_raise_volatile") {
		// The longer lease isn't in the file yet: it must be before any
		// grant, even an unchanged renewal (which writes nothing of its
		// own), or a promise made under it outlives the next restart's
		// shorter wait (#1021 re-review). Dirty, so the first grant comes
		// back as a Persist and leaves only once it is durable.
		v.dirty++
	}
	v.st.Lease = bound
	return v
}

// MaxRequestLease is the longest lease a request may state at now (the
// driver refuses a longer one before Handle): the voter's own lease, the
// length of every promise it makes, so no lease outlives a promise it rests
// on. During the restart wait, when it grants nothing, it is the longer
// lease the wait covers, so a live leader's renewals are still seen
// (LeaderSeen) across a change of lease.
func (v *Voter) MaxRequestLease(now Instant) time.Duration {
	if now < v.waitUntil {
		return max(v.lease, v.st.Lease)
	}
	return v.lease
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
	v.st = VoterState{Incarnation: inc, Lease: lease}
	v.dirty++
	return v, &Persist{Seq: v.dirty, State: v.st}
}

// Duplicates is how many requests this voter refused because their name
// held a running promise under another boot nonce, and the last such name:
// two processes claim one member (design §2). The driver raises
// redundancy.duplicate-member when it rises.
func (v *Voter) Duplicates() (uint64, NodeID) { return v.duplicates, v.dupFrom }

// DuplicateCause is why the last duplicate Duplicates counts was refused.
func (v *Voter) DuplicateCause() DupCause { return v.dupCause }

// Relay returns this voter's running promise to ANOTHER node at now, for
// the driver to stamp on every vote frame this node sends as the frame
// leaves (#1029 layer 2): the zero Relay when the promise is to its own
// candidate, has run out, or is not known (the restart wait). A promise to
// its own candidate stays off the wire: the other voters hear that from
// the candidate's own requests, and a clone's self-grant must never reach
// them as a promise.
func (v *Voter) Relay(now Instant) Relay {
	if now >= v.promiseUntil || v.st.GrantedTo == "" || (v.st.GrantedTo == v.self && !mut("relay_self_promise")) {
		return Relay{}
	}
	return Relay{To: v.st.GrantedTo, Nonce: v.grantNonce, Left: v.promiseUntil.Sub(now)}
}

// RelayChanged reports, once, that this voter's relayed promise names
// another grantee or boot than when it last reported: it has just promised
// another node (a new leader, or the same name's new boot). The driver then
// pushes a probe to the witness (Candidate.PushProbe).
func (v *Voter) RelayChanged(now Instant) bool {
	r := v.Relay(now)
	if r.To == "" || (r.To == v.pushedTo && r.Nonce == v.pushedNonce) {
		return false
	}
	v.pushedTo, v.pushedNonce = r.To, r.Nonce
	return true
}

// Hear takes the running promise voter by relayed on a frame received at
// now (#1029 layer 2). The driver calls it for every vote frame from
// another voter, a request or a reply, before Handle: a probe or a frame
// in the restart wait still tells this voter what by has promised.
//
// The promise is held until receipt + left·(1 + MaxRho): a refusal must
// outlast the original promise in real time, whatever the two clocks'
// rates. It never replaces what this voter already holds: its own running
// promise to the same name under another nonce, or one another voter
// relayed earlier, wins, and the conflict is counted as a duplicate. A
// newer relay from the same voter about the same name replaces its own
// earlier one: a voter holds one promise at a time.
func (v *Voter) Hear(now Instant, by NodeID, p Relay) {
	v.prune(now)
	if mut("relay_ignored") || by == v.self || p.To == "" || p.Nonce == 0 || p.Left <= 0 {
		return
	}
	left := min(p.Left, max(v.lease, v.st.Lease)) // no promise runs longer than a lease
	h := heardPromise{by: by, to: p.To, nonce: p.Nonce, strict: now.Add(left),
		until: now.Add(time.Duration(float64(left) * (1 + MaxRho)))}
	if mut("relay_no_margin") {
		h.until = h.strict
	}
	if now < v.promiseUntil && v.st.GrantedTo == p.To && v.grantNonce != p.Nonce && !mut("relay_last_wins") {
		v.duplicate(now < h.strict, p.To, DupNonce)
		return
	}
	for _, x := range v.heard {
		if x.by != by && x.to == p.To && x.nonce != p.Nonce && !mut("relay_last_wins") {
			v.duplicate(now < x.strict, p.To, DupNonce)
			return // the first heard wins while it runs
		}
	}
	keep := v.heard[:0]
	for _, x := range v.heard {
		if x.by == by && x.to == p.To {
			// The same voter's promise to the same name: the newer stands.
			if x.nonce == p.Nonce {
				h.strict, h.until = max(h.strict, x.strict), max(h.until, x.until)
			}
			continue
		}
		keep = append(keep, x)
	}
	v.heard = append(keep, h)
}

// prune drops the heard promises that have run out at now.
func (v *Voter) prune(now Instant) {
	keep := v.heard[:0]
	for _, x := range v.heard {
		if now < x.until {
			keep = append(keep, x)
		}
	}
	v.heard = keep
}

// heardRefuses reports whether a heard promise refuses r at now, and
// whether that counts as a duplicate (inside the promise itself, not only
// the clock-rate margin after it, so a fast real restart raises nothing).
// (a) A promise to r.From under another nonce: another process holds that
// name. (b) A promise BY r.From's own voter to another node: a real
// controller doesn't campaign while its own voter is promised elsewhere,
// so this is another process under its name (#1029 layer 2).
func (v *Voter) heardRefuses(now Instant, r Request) (refuse, counts bool, cause DupCause) {
	v.prune(now)
	for _, x := range v.heard {
		switch {
		case x.to == r.From && x.nonce != r.Nonce:
			cause = DupNonce
		case x.by == r.From && x.to != r.From && !mut("relay_voter_rule_off"):
			cause = DupVoterPromised
		default:
			continue
		}
		refuse = true
		if now < x.strict || mut("relay_tail_counted") {
			return true, true, cause
		}
	}
	return refuse, false, cause
}

func (v *Voter) duplicate(counts bool, who NodeID, cause DupCause) {
	if counts {
		v.duplicates++
		v.dupFrom, v.dupCause = who, cause
	}
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
	case active && v.st.GrantedTo == r.From && r.Nonce != v.grantNonce && !mut("duplicate_member_granted"):
		// The name holds a running promise under another boot nonce: two
		// processes claim it (an env file copied to the other box, a cloned
		// disk). A real restart of that name waits out its restart wait
		// first, by when this promise has run out, so this costs a restart
		// nothing (#1028 review).
		v.duplicate(true, r.From, DupNonce)
		return nil, refuse
	case !(active && v.st.GrantedTo == r.From && r.Nonce == v.grantNonce) && v.refusedByHeard(now, r):
		// A promise another voter relayed refuses it (#1029 layer 2). This
		// voter's own running promise to this very boot wins: it renews.
		return nil, refuse
	case r.Term == v.st.Promised && v.st.GrantedTo != r.From:
		return nil, refuse // this term is someone else's
	case r.Term > v.st.Promised && active && v.st.GrantedTo != r.From:
		return nil, refuse // promised to another, and not run out
	}
	// Grant. The promise runs from when the request was RECEIVED.
	v.promiseUntil = now.Add(v.lease)
	v.grantNonce = r.Nonce
	if r.Term != v.st.Promised || r.From != v.st.GrantedTo || v.st.Lease != v.lease {
		// Past the restart wait every promise made under a longer lease
		// has run out, so the state now records this voter's own.
		v.st.Promised, v.st.GrantedTo, v.st.Lease = r.Term, r.From, v.lease
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

// refusedByHeard is heardRefuses, counting the refusal and the duplicate
// it finds.
func (v *Voter) refusedByHeard(now Instant, r Request) bool {
	refuse, counts, cause := v.heardRefuses(now, r)
	if refuse {
		v.relayRefused++
		v.duplicate(counts, r.From, cause)
	}
	return refuse
}

// RelayRefused counts the requests a heard promise refused (#1029 layer
// 2), duplicates and refusals in a heard promise's clock-rate margin both.
func (v *Voter) RelayRefused() uint64 { return v.relayRefused }

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
