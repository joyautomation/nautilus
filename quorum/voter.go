package quorum

import (
	"slices"
	"time"
)

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
	// seen are the boot nonces this voter heard per name, each live until
	// its time: a request's for a lease after it arrived, a relayed
	// promise's until its hold ends. Two live nonces for one name mean two
	// processes run under it (#1029 step 1). An old boot alive only in a
	// relayed promise is refused by that relay first (heardRefuses), whose
	// margin tail counts nothing, so a fast real restart raises no false
	// alarm. dupHeard are the names other voters relayed as duplicated,
	// refused until their time. In memory only, as heard.
	seen     map[NodeID][]seenNonce
	dupHeard map[NodeID]Instant
	// boots: per name, the boots heard in that name's OWN requests (never
	// a relay), with the first receipt and the highest Seq of each.
	// marked: the names confirmed as run by two processes at once (one
	// boot kept sending after another had started, beyond the one-way
	// delay). A marked name gets no new grant and its vote counts nowhere
	// this voter's candidate counts, until ClearDuplicate (#1029 3c). In
	// memory only: a restart clears them, and a copy still running is
	// marked again within a quarter lease and a look.
	boots  map[NodeID][]bootSeen
	banned map[NodeID][]uint64 // evicted_boot_banned only
	marked map[NodeID]bool
	// relaySeq: dup_mark_from_relay's stand-in Seq (mutation only).
	relaySeq uint64
	// boot identifies this voter's process boot on its replies (SetBoot).
	boot uint64
	// ownHeld reports whether this node's own candidate is held by a mark on
	// its name (Candidate.Marked; NewAdoption wires it): the voter then
	// makes no new grant to another node (#1113 review).
	ownHeld func() bool
	// marks counts the marks this voter made, lastMarked the latest name
	// (MarkCount): the alarm follows these, not DuplicateCause, which a
	// later count in the same Handle can overwrite.
	marks      uint64
	lastMarked NodeID
	// pushedTo, pushedNonce: the promise RelayChanged last reported.
	pushedTo    NodeID
	pushedNonce uint64
}

// bootSeen is one boot of a name as its own requests showed it: the
// nonce, when its first and its latest new request arrived, and the
// highest Seq heard. Once the name is marked, last and seq go on
// following the boots already known (HeldBy).
type bootSeen struct {
	nonce       uint64
	first, last Instant // when its first and its latest new request arrived
	seq         uint64
}

// maxBoots is how many boots per name a voter keeps evidence for.
const maxBoots = 8

// noteBoot takes a request from name under nonce with Seq seq, received
// at now, as evidence (#1029 3c). A frame is new evidence only when its
// Seq is above every Seq heard from that boot: a duplicated or replayed
// frame proves nothing. Two boots ran at once when EACH sent a new frame
// that arrived more than D after the other's first frame. A real restart
// stops the old boot before the new one starts, so every frame of the old
// boot left before the new boot's first did, and arrives within the
// one-way delay of it (at most a quarter lease, the documented rule): the
// old boot never qualifies, whichever of the two this voter heard first
// (a voter that restarted, or whose link just came up, may hear the new
// boot before an old frame still in flight). Two live copies both keep
// sending, and both qualify within D and a look. D is that quarter lease,
// stretched by the clock-rate margin (all receipts are this voter's own
// clock). The name is then marked.
func (v *Voter) noteBoot(now Instant, name NodeID, nonce, seq uint64) {
	if nonce == 0 || seq == 0 || name == "" || mut("dup_mark_off") {
		return
	}
	if v.marked[name] {
		// Marked: no more evidence to weigh, but whether each boot it
		// knows still sends new frames names the incumbent (HeldBy). A
		// boot first heard after the mark is never added, so never named.
		for i := range v.boots[name] {
			if b := &v.boots[name][i]; b.nonce == nonce && seq > b.seq {
				b.seq, b.last = seq, now
			}
		}
		return
	}
	if mut("evicted_boot_banned") {
		for _, x := range v.banned[name] {
			if x == nonce {
				return
			}
		}
	}
	if v.boots == nil {
		v.boots = map[NodeID][]bootSeen{}
	}
	bs := v.boots[name]
	i := -1
	for j := range bs {
		if bs[j].nonce == nonce {
			i = j
		}
	}
	if i < 0 {
		if len(bs) >= maxBoots {
			// Forget the boot heard from longest ago. Forgotten, not
			// banned: an original cut from this voter while a copy
			// restarted over and over comes back as a new boot, and is
			// marked beside the copy (#1113 review). The price: a frame
			// of a forgotten boot late beyond the delay rule can mark.
			old := 0
			for j := range bs {
				if bs[j].last < bs[old].last {
					old = j
				}
			}
			if mut("evicted_boot_banned") {
				if v.banned == nil {
					v.banned = map[NodeID][]uint64{}
				}
				v.banned[name] = append(v.banned[name], bs[old].nonce)
			}
			bs = append(bs[:old:old], bs[old+1:]...)
		}
		v.boots[name] = append(bs, bootSeen{nonce: nonce, first: now, last: now, seq: seq})
		return
	}
	if seq <= bs[i].seq && !mut("dup_mark_counts_replays") {
		return // a replay or a duplicate: no new evidence
	}
	bs[i].seq = max(bs[i].seq, seq)
	bs[i].last = now
	d := time.Duration(float64(max(v.lease, v.st.Lease)/4) * (1 + MaxRho))
	if mut("dup_mark_no_delay") {
		d = 0
	}
	// One new frame past D from each boot, not two: two frames would let
	// a split that follows a clone's start by under a lease find no voter
	// that marked it (633 two-leader runs in 3,600 with the split within
	// half a second, against 45 with one; #1113 re-check). A late frame
	// of a boot this voter remembers is caught by Seq; one of a boot it
	// forgot, beyond the delay rule, can mark (a false mark costs that
	// controller's redundancy until cleared, never the leader).
	x := bs[i]
	for _, y := range bs {
		if y.nonce == nonce || x.last <= y.first.Add(d) {
			continue
		}
		if y.last > x.first.Add(d) || (mut("dup_mark_one_sided") && x.first <= y.first) {
			if v.marked == nil {
				v.marked = map[NodeID]bool{}
			}
			v.marked[name] = true
			v.marks++
			v.lastMarked = name
			v.duplicate(true, name, DupConfirmed)
			return
		}
	}
}

// SetBoot sets the id of this voter's process boot, stamped on every reply
// (Reply.VoterBoot): the driver's boot nonce, or any value that differs
// from one start to the next.
func (v *Voter) SetBoot(boot uint64) { v.boot = boot }

// MarkCount is how many times this voter has marked a name, and the latest
// name it marked. The driver raises the confirmed duplicate alarm when it
// rises (#1029 3c).
func (v *Voter) MarkCount() (uint64, NodeID) {
	return v.marks, v.lastMarked
}

// Marked returns the names this voter has confirmed as run by two
// processes at once, sorted. Each gets no new grant here, and no vote
// from it counts toward this node's own lease, until ClearDuplicate.
func (v *Voter) Marked() []NodeID {
	out := make([]NodeID, 0, len(v.marked))
	for n := range v.marked {
		out = append(out, n)
	}
	slices.Sort(out)
	return out
}

// ClearDuplicate drops the mark on name and the evidence that set it: the
// operator has stopped the copy. A copy still running is marked again as
// soon as it is heard beside the original. It reports whether name was
// marked. Restarting this voter clears every mark too.
func (v *Voter) ClearDuplicate(name NodeID) bool {
	was := v.marked[name]
	if !mut("clear_keeps_mark") {
		delete(v.marked, name)
		delete(v.boots, name)
	}
	return was
}

// seenNonce is one boot nonce heard under a name, live until until.
type seenNonce struct {
	nonce uint64
	until Instant
}

// noteNonce records that name ran under nonce, live until until.
func (v *Voter) noteNonce(name NodeID, nonce uint64, until Instant) {
	if nonce == 0 || name == "" || mut("dup_name_ignored") {
		return
	}
	if v.seen == nil {
		v.seen = map[NodeID][]seenNonce{}
	}
	xs := v.seen[name][:0]
	for _, x := range v.seen[name] {
		if x.until > until.Add(-2*max(v.lease, v.st.Lease)) || x.nonce == nonce {
			xs = append(xs, x) // drop the long dead
		}
	}
	for i := range xs {
		if xs[i].nonce == nonce {
			xs[i].until = max(xs[i].until, until)
			v.seen[name] = xs
			return
		}
	}
	v.seen[name] = append(xs, seenNonce{nonce: nonce, until: until})
}

// dupLeft is how long name stays seen under two live nonces at now: until
// the second-latest of them runs out; 0 when it isn't.
func (v *Voter) dupLeft(now Instant, name NodeID) time.Duration {
	var a, b Instant // the two latest untils
	for _, x := range v.seen[name] {
		switch {
		case x.until > a:
			a, b = x.until, a
		case x.until > b:
			b = x.until
		}
	}
	if b <= now {
		return 0
	}
	return b.Sub(now)
}

// Duplicated reports whether this voter knows name to run as two boots at
// now: it heard two live nonces for it, or another voter relayed so. A
// candidate counts no grant from a duplicated voter (NewAdoption wires
// it): two processes cast that vote (#1029 step 1).
func (v *Voter) Duplicated(now Instant, name NodeID) bool {
	return v.marked[name] || v.dupLeft(now, name) > 0 || now < v.dupHeard[name]
}

// dupRefuses reports whether r asks a NEW grant for a duplicated name, and
// whether that counts as a duplicate's sign. A running promise to that
// very boot still renews: the incumbent keeps the promises that protect
// it; only a grant it doesn't hold yet is refused, to every boot (#1029
// step 1; refusing the incumbent too lets a clone in, see the design).
func (v *Voter) dupRefuses(now Instant, r Request, active bool) (refuse, counts bool) {
	if mut("dup_name_ignored") {
		return false, false
	}
	if active && v.st.GrantedTo == r.From && r.Nonce == v.grantNonce && !mut("dup_refuses_incumbent") {
		return false, false
	}
	for _, x := range v.seen[r.From] {
		if x.nonce != r.Nonce && now < x.until {
			// Two live boots: refused, but not yet an alarm. A real
			// restart can look like this for a moment (an old frame
			// late); a copy running beside it is confirmed (marked)
			// within a quarter lease and a look, and that counts.
			refuse, counts = true, mut("dup_alarm_unconfirmed")
		}
	}
	if now < v.dupHeard[r.From] || v.marked[r.From] {
		refuse = true
	}
	return refuse, counts
}

// heardPromise is a promise voter by relayed: to grantee to under nonce,
// counted a duplicate's sign until strict (receipt + left), and refusing
// until until (left stretched by the clock-rate margin).
type heardPromise struct {
	by, to        NodeID
	byBoot        uint64 // the boot nonce of the frame that carried it (0: a reply, unknown)
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
	// DupConfirmed: this voter heard two boots of the name running at
	// once, and marked it (Marked, #1029 3c).
	DupConfirmed
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
	if !mut("grant_nonce_volatile") {
		v.grantNonce = st.GrantNonce // a term stays one boot's across a restart
	}
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
	var r Relay
	if !(now >= v.promiseUntil || v.st.GrantedTo == "" || (v.st.GrantedTo == v.self && !mut("relay_self_promise"))) {
		r = Relay{To: v.st.GrantedTo, Nonce: v.grantNonce, Left: v.promiseUntil.Sub(now)}
	}
	if !mut("dup_not_relayed") {
		// A name this voter itself sees duplicated, never one only relayed
		// to it (no echo). With both controllers duplicated, the one seen
		// longer goes.
		for name := range v.seen {
			if d := v.dupLeft(now, name); d > r.DupLeft || (d > 0 && d == r.DupLeft && name < r.Dup) {
				r.Dup, r.DupLeft = name, d // ties by name: map order isn't
			}
		}
	}
	return r
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
// rates. A relay naming a name under another boot than this voter's own
// running promise, or than one another voter relayed, is a duplicate's
// sign and counted. Both boots are then live (noteNonce), so neither gets
// a new grant here, while a running promise to either renews (#1029 step
// 1). A newer relay from the same voter about the same name replaces its
// own earlier one: a voter holds one promise at a time.
//
// boot is the nonce of the frame's sender when the frame says it (a
// request), 0 for a reply: rule (b) counts a duplicate only for a relay
// from another boot than the request it refuses, since one boot's own
// frames can arrive out of order (a campaign overtaken by the push probe
// of the promise its voter made just after it; #1079 review).
func (v *Voter) Hear(now Instant, by NodeID, boot uint64, p Relay) {
	v.prune(now)
	if by != v.self && p.Dup != "" && p.DupLeft > 0 && !mut("dup_not_relayed") {
		if v.dupHeard == nil {
			v.dupHeard = map[NodeID]Instant{}
		}
		dl := min(p.DupLeft, max(v.lease, v.st.Lease))
		v.dupHeard[p.Dup] = max(v.dupHeard[p.Dup], now.Add(time.Duration(float64(dl)*(1+MaxRho))))
	}
	if mut("relay_ignored") || by == v.self || p.To == "" || p.Nonce == 0 || p.Left <= 0 {
		return
	}
	left := min(p.Left, max(v.lease, v.st.Lease)) // no promise runs longer than a lease
	if mut("dup_mark_from_relay") {
		v.relaySeq++ // above any real Seq: every relay counts as a new frame
		v.noteBoot(now, p.To, p.Nonce, 1<<62+v.relaySeq)
	}
	v.noteNonce(p.To, p.Nonce, now.Add(time.Duration(float64(left)*(1+MaxRho))))
	// strict: until when the promise certainly still runs, and a conflict
	// with it is a duplicate's sign. The relay left its sender up to a
	// one-way delay ago (at most lease/4, the documented rule), so a fast
	// real restart on a slow link raises no false alarm. The refusal holds
	// to until, past the promise's end whatever the delay.
	strict := now.Add(left)
	if !mut("dup_strict_ignores_delay") {
		strict = strict.Add(-max(v.lease, v.st.Lease) / 4)
	}
	h := heardPromise{by: by, byBoot: boot, to: p.To, nonce: p.Nonce, strict: strict,
		until: now.Add(time.Duration(float64(left) * (1 + MaxRho)))}
	if mut("relay_no_margin") {
		h.until = now.Add(left)
	}
	if now < v.promiseUntil && v.st.GrantedTo == p.To && v.grantNonce != p.Nonce {
		v.duplicate(now < h.strict, p.To, DupNonce)
	}
	for _, x := range v.heard {
		if x.by != by && x.to == p.To && x.nonce != p.Nonce {
			v.duplicate(now < x.strict, p.To, DupNonce)
			break
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
		if cause == DupVoterPromised && (x.byBoot == 0 || x.byBoot == r.Nonce) && !mut("rule_b_counts_own_boot") {
			continue // the same boot's frames out of order, or a boot unknown: refuse, count nothing
		}
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

// heldRefuses reports whether r asks this voter for a NEW grant to another
// node while this node's own candidate is held by a mark (rule (ii) of the
// #1113 review): a copy of a controller carries a copy of its voter, and
// the held copy's voter must not lead the other controller into a lease
// beside the original's. A running promise to that very boot renews.
func (v *Voter) heldRefuses(r Request, active bool) bool {
	if r.From == v.self || v.ownHeld == nil || mut("held_voter_grants") || !v.ownHeld() {
		return false
	}
	return !(active && v.st.GrantedTo == r.From && r.Nonce == v.grantNonce)
}

// wouldRefuseName reports whether Handle would refuse r for its name alone
// (a duplicated name, or a promise relayed to it under another boot),
// counting nothing: YieldTo asks it first, so a voter never gives up its
// own candidate's round to a request it then refuses (#1079 review).
func (v *Voter) wouldRefuseName(now Instant, r Request) bool {
	if refuse, _ := v.dupRefuses(now, r, false); refuse {
		return true
	}
	if v.heldRefuses(r, false) {
		return true
	}
	refuse, _, _ := v.heardRefuses(now, r)
	return refuse
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
	if v.wouldRefuseName(now, r) && !mut("yield_before_refusal") {
		return false // it would refuse r anyway: no reason to give up its own round
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
	refuse := []Reply{v.reply(now, r, false)}
	// Every request says its sender runs under that boot now: a probe, a
	// campaign or a renewal, its own candidate's too (#1029 step 1).
	v.noteNonce(r.From, r.Nonce, now.Add(v.lease))
	v.noteBoot(now, r.From, r.Nonce, r.Seq)
	if !mut("held_by_before_frame") {
		// The incumbent as of this very frame: a marked standby gets only
		// refusals, and a lossy original's frames can be half a lease
		// apart, so a word that missed its own frame would name the copy
		// to it (#1133 review).
		refuse[0].HeldBy = v.HeldBy(now, r.From, r.Nonce)
	}
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
	case !(active && v.st.GrantedTo == r.From && r.Nonce == v.grantNonce) && v.refusedByHeard(now, r):
		// A promise another voter relayed refuses it (#1029 layer 2). This
		// voter's own running promise to this very boot wins: it renews.
		return nil, refuse
	case v.dupRefusedCount(now, r, active):
		// Two boots of this name are live: no new grant to it, to either
		// boot (#1029 step 1).
		return nil, refuse
	case v.heldRefuses(r, active):
		// This node's own name is marked: another process runs as it, and
		// one of the two copies of this very voter must not hand the
		// other controller a lease (#1113 review, route 2).
		return nil, refuse
	case r.Term == v.st.Promised && v.st.GrantedTo == r.From && v.grantNonce != 0 && r.Nonce != v.grantNonce && !mut("same_term_other_boot"):
		// This term went to another boot of this name: a term is one
		// process's, so the other asks a higher one (#1029 step 1: a clone
		// and its original, one after the other). A plain restart asks
		// above it anyway, from the Promised in this refusal.
		return nil, refuse
	case r.Term == v.st.Promised && v.st.GrantedTo != r.From:
		return nil, refuse // this term is someone else's
	case r.Term > v.st.Promised && active && v.st.GrantedTo != r.From:
		return nil, refuse // promised to another, and not run out
	}
	// Grant. The promise runs from when the request was RECEIVED.
	v.promiseUntil = now.Add(v.lease)
	v.grantNonce = r.Nonce
	if r.Term != v.st.Promised || r.From != v.st.GrantedTo || v.st.Lease != v.lease || (r.Nonce != v.st.GrantNonce && !mut("grant_nonce_unwritten")) {
		// Past the restart wait every promise made under a longer lease
		// has run out, so the state now records this voter's own. The
		// grant's boot nonce goes with it (one term, one boot).
		v.st.Promised, v.st.GrantedTo, v.st.Lease, v.st.GrantNonce = r.Term, r.From, v.lease, r.Nonce
		v.dirty++
	}
	g := v.reply(now, r, true)
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

// dupRefusedCount is dupRefuses, counting the refusal and the duplicate.
func (v *Voter) dupRefusedCount(now Instant, r Request, active bool) bool {
	refuse, counts := v.dupRefuses(now, r, active)
	if refuse {
		v.relayRefused++
		v.duplicate(counts, r.From, DupNonce)
	}
	return refuse
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

func (v *Voter) reply(now Instant, r Request, granted bool) Reply {
	return Reply{From: v.self, To: r.From, Term: r.Term, Nonce: r.Nonce, ID: r.ID, Granted: granted,
		Promised: max(v.st.Promised, v.st.Floor), Incarnation: v.witnessIncarnation(),
		Marked: v.marked[r.From] && !mut("marked_not_told"), VoterBoot: v.boot, HeldBy: v.HeldBy(now, r.From, r.Nonce)}
}

// Spare reports whether this voter's state says its controller demoted
// itself to a spare (VoterState.Spare, #1029 3c-2).
func (v *Voter) Spare() bool { return v.st.Spare }

// MakeSpare records that this voter's controller is a spare (Candidate.Spare)
// and returns the write that makes it durable: every call, so a driver
// whose write failed asks again. The flag rides on every later write of
// this voter's state. The driver goes quiet only once that write is
// durable: a crash before it leaves a held copy that decides again, never
// a spare that forgot it is one.
func (v *Voter) MakeSpare() *Persist {
	v.st.Spare = true
	return v.change()
}

// SpareWriteFailed takes back the flag MakeSpare set, its write having
// failed: a later write must not carry it to disk unasked (a held copy
// whose incumbent then stopped would start silent). The driver decides
// again on a later reply.
func (v *Voter) SpareWriteFailed() {
	if !mut("spare_flag_kept_after_failure") {
		v.st.Spare = false
	}
}

// Unspare clears the spare flag and returns the write. Tests use it, and
// the operator's recommission --from-spare will (3c-3): a spare never
// clears it itself.
func (v *Voter) Unspare() *Persist {
	if !v.st.Spare {
		return nil
	}
	v.st.Spare = false
	return v.change()
}

// HeldBy is the boot of a marked name this voter holds the incumbent,
// told to that name's boot asker (Reply.HeldBy, #1029 3c-2): of the boots
// it heard under that name before the mark, the one heard FIRST among
// those that sent a new frame (a Seq above its last) within half a lease.
// 0 when name is not marked, is this voter's own, no boot it knows is
// fresh, or asker is a boot it doesn't know: a boot first heard after the
// mark is never named, and never told another holds the name, so an
// original restarted beside a running copy stays held, not spare (#1133
// review). First heard is who was
// running first, not which box is real: the protocol can't tell a copy
// from its original, and on a same-time boot the name may go to the copy.
// A fresh frame, not a live nonce: a boot that stopped is no incumbent
// within half a lease, though its nonce stays live for a lease, so the
// original's restart after a spare was named finds no dead boot named
// over it.
func (v *Voter) HeldBy(now Instant, name NodeID, asker uint64) uint64 {
	if !v.marked[name] || name == v.self {
		return 0
	}
	if !mut("held_by_names_to_unknown") && !slices.ContainsFunc(v.boots[name], func(b bootSeen) bool { return b.nonce == asker }) {
		return 0
	}
	fresh := time.Duration(float64(max(v.lease, v.st.Lease)) * 0.5)
	var best *bootSeen
	for i := range v.boots[name] {
		b := &v.boots[name][i]
		if now.Sub(b.last) > fresh && !mut("held_by_stale") {
			continue
		}
		if best == nil || b.first < best.first || (mut("held_by_last_heard") && b.first > best.first) {
			best = b
		}
	}
	if best == nil {
		return 0
	}
	return best.nonce
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
