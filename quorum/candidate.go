package quorum

import (
	"errors"
	"fmt"
	"time"
)

// Config is a candidate's view of the pair.
type Config struct {
	Self    NodeID
	Peer    NodeID // the other controller
	Witness NodeID
	// Lease is how long a grant promises (design: default 1 s).
	Lease time.Duration
	// Rho bounds the RELATIVE rate of any two voters' clocks (default
	// 0.01). The leader's lease is shortened by it, so it ends before any
	// voter's promise does.
	Rho float64
	// Incarnation is the witness incarnation this controller accepts at
	// start; a witness grant from any other is not counted. 0 accepts
	// none. An Adoption changes it later (Candidate.Count).
	Incarnation uint64
	// Jitter returns a delay in [0, lease/4) added before each retry of a
	// refused campaign, so two candidates refused in the same round don't
	// retry in lockstep forever. Seeded per node; nil means none.
	Jitter func() time.Duration
	// Nonce names this boot of the candidate: drawn at random, non-zero,
	// at every start. Requests carry it and replies echo it, so a grant
	// from an earlier boot never counts.
	Nonce uint64
}

// MaxRho is the largest clock-rate margin accepted. Clocks further apart
// than 10% are broken hardware, not a margin to plan for.
const MaxRho = 0.1

// Validate refuses a Config the safety argument does not hold for: a
// zero or absurd ρ (a zero-value Config would be the "no ρ" mutation), a
// non-positive lease, a zero nonce, or missing or repeated node names.
func (c Config) Validate() error {
	switch {
	case !(c.Rho > 0 && c.Rho <= MaxRho):
		return fmt.Errorf("quorum: rho %v outside (0, %v]", c.Rho, MaxRho)
	case c.Lease <= 0:
		return errors.New("quorum: lease must be positive")
	case c.Nonce == 0:
		return errors.New("quorum: nonce must be drawn, non-zero, per boot")
	case c.Self == "" || c.Peer == "" || c.Witness == "":
		return errors.New("quorum: self, peer and witness must all be named")
	case c.Self == c.Peer || c.Self == c.Witness || c.Peer == c.Witness:
		return errors.New("quorum: self, peer and witness must be three different names")
	}
	return nil
}

func (c Config) leaseFor() time.Duration {
	if mut("no_rho") {
		return c.Lease
	}
	return time.Duration(float64(c.Lease) * (1 - c.Rho))
}

func (c Config) look() time.Duration { return c.Lease / 4 }

// round is one request sent to every voter: a campaign or a renewal.
type round struct {
	id      uint64
	term    uint64
	sent    Instant
	grants  map[NodeID]bool
	replies map[NodeID]bool
	// campaign: a request for a new term, not a renewal.
	campaign bool
}

// Candidate is a controller's campaign and lease state machine (design
// §3.1). Not safe for concurrent use.
type Candidate struct {
	cfg Config

	term    uint64 // the vote term the lease rests on, or the one being campaigned for
	maxSeen uint64 // highest term heard of, from any voter
	// epoch is the term leadership was acquired in, after a gap: the
	// fencing token Term reports. A re-term on renewal (renewTerm) leaves
	// it alone, so writes posted under it stay valid across one lease.
	epoch     uint64
	renewTerm uint64 // the term renewals go out at while leading
	// markSeq, markCurrent: the standby's in-sync mark, as the driver last
	// set it (SetMark); every renewal carries it (#710 S2c).
	markSeq     uint64
	markCurrent bool
	// reterm: a voter refused a renewal at a term it had promised to
	// another (a standby's lost campaign); the next renewal asks for a
	// higher term, which that voter can grant once its promise runs out.
	reterm bool
	nextID uint64
	// seq numbers every request this boot sends (Request.Seq).
	seq uint64
	// markedBy: the voters whose reply said this name is marked a
	// confirmed duplicate (Reply.Marked), with the boot of the voter that
	// said so. A later unmarked reply from that same voter boot clears
	// the entry (rule (i)): a voter cut off since keeps this candidate
	// from campaigning (#1029 3c, #1113 review). So does an unmarked reply
	// from another boot of it whose clear generation is above the one the
	// hold was set under: only an operator's clear raises it, so a voter
	// that restarted and forgot the mark (a crash before its write), a
	// wiped one, or a copy booted from an older disk can't end the hold,
	// and the clear always does (#1029 3c-3b).
	markedBy map[NodeID]markHold
	// retiredReply reports a reply from a voter boot an operator's clear
	// retired, marking the name again (Voter.RetiredReply; NewAdoption
	// wires it): its grant counts for nothing (#1029 3c-3b).
	retiredReply func(now Instant, name NodeID, boot uint64) bool
	// genSeen: per voter, the highest clear generation any of its replies
	// to this boot carried, never lowered. A hold is set under it, so a
	// word from an older disk of that voter (a backup, a copy) that is
	// below what this candidate already saw can't end a hold set under a
	// lower one by a wiped boot (#1144 review).
	genSeen map[NodeID]uint64
	// held: the latest HeldBy word of each of the two OTHER voters on
	// this boot's replies, and since when it has said so (NoteHeld).
	held     map[NodeID]heldWord
	rounds   []*round
	leading  bool
	leaseEnd Instant // leading only until here, on this node's clock

	nextLook     Instant
	backoffUntil Instant // the candidate sends nothing before this
	retryAt      Instant // a refused campaign retries no earlier
	// pending is the campaign in flight: no new campaign starts until a
	// majority has answered it or a lease has passed (expire).
	pending *round

	// countInc is the witness incarnation whose grants count (0: none),
	// and countFrom when they start to: a newly adopted incarnation is
	// counted only after its quiet period (Count).
	countInc  uint64
	countFrom Instant

	// holdUntil: no campaign before this, the end of its own voter's
	// restart wait (set by NewAdoption). A campaign then can't win without
	// its own vote, and it can take the witness's first grant from a
	// leader that rests on the two controllers (#1021 review).
	holdUntil Instant

	// probeAt: the next probe of the witness while not leading (Probe).
	probeAt Instant

	// duplicated reports a voter name its own voter knows runs as two
	// boots: a grant from it counts for nothing (NewAdoption sets it to
	// Voter.Duplicated). nil counts every grant.
	duplicated func(now Instant, name NodeID) bool

	// ownGen is this controller's committed adoption generation, which
	// every request carries (Request.Gen); fenced reports a voter name its
	// own voter fenced at a generation (Voter.Fenced): a reply from that
	// name below it counts for nothing. commissioned: this controller has
	// adopted an incarnation. NewAdoption wires the three (#1029
	// 3c-3c-1b). superseded: a voter refused this boot as fenced: its
	// state is from before an operator replaced its name (Superseded).
	ownGen       func() uint64
	commissioned func() bool
	ownFences    func() Fences
	fenced       func(name NodeID, gen uint64) bool
	superseded   NodeID
	// fencedBy: per voter, whether its latest word to this boot said this
	// state is fenced; witnessWord: the witness answered this boot
	// (fencedHold).
	fencedBy    map[NodeID]bool
	witnessWord bool
}

// fencedHold: this boot campaigns no more, as a state from before an
// operator replaced its name (#1029 3c-3c-1b): the witness's latest word
// said it is fenced, or the witness has not answered this boot and
// another voter's did. A complete --from-spare fences the witness last,
// so the old box holds wherever it is, a split beside the peer included
// (else it would campaign each look, moving its own voter's terms in
// vain). Only an incomplete one (naut-lead's ruling C: the peer fenced,
// the witness not) leaves the incumbent free to lead with the witness.
func (c *Candidate) fencedHold() bool {
	if mut("fenced_still_campaigns") {
		return false
	}
	if c.fencedBy[c.cfg.Witness] {
		return true
	}
	if c.witnessWord && !mut("fence_hold_ignores_witness") {
		return false
	}
	for _, f := range c.fencedBy {
		if f {
			return true
		}
	}
	return false
}

// fences are the fences this controller's requests carry.
func (c *Candidate) fences() Fences {
	if c.ownFences == nil {
		return Fences{}
	}
	return c.ownFences()
}

// gen is the adoption generation this controller's requests carry.
func (c *Candidate) gen() uint64 {
	if c.ownGen == nil {
		return 0
	}
	return c.ownGen()
}

// Superseded names a voter that refused this boot as fenced, "" when none
// did: this controller's state is from before an operator replaced its
// name with recommission --from-spare, and it can't vote any more (#1029
// 3c-3c-1b). The driver raises the alarm.
func (c *Candidate) Superseded() NodeID { return c.superseded }

// NewCandidate starts a candidate at now that knows of terms up to known
// (its own voter's promised term). cfg must pass Validate.
func NewCandidate(cfg Config, known uint64, now Instant) (*Candidate, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &Candidate{cfg: cfg, maxSeen: known, nextLook: now, countInc: cfg.Incarnation}, nil
}

// Count sets which witness incarnation's grants count: inc (0: none), and
// only grants received at or after from. The Adoption calls it.
func (c *Candidate) Count(inc uint64, from Instant) { c.countInc, c.countFrom = inc, from }

// Counting returns the witness incarnation whose grants count, and from
// when.
func (c *Candidate) Counting() (inc uint64, from Instant) { return c.countInc, c.countFrom }

// raiseSeen lifts the highest term heard of to t: an adoption's term floor.
func (c *Candidate) raiseSeen(t uint64) {
	if t > c.maxSeen {
		c.maxSeen = t
	}
}

// Leading reports whether this controller holds a lease at now. It is
// judged when read, against the lease end, not on a tick: a leader whose
// renewals stopped closes its gate on time even if Tick is late.
func (c *Candidate) Leading(now Instant) bool { return c.leading && now < c.leaseEnd }

// Observe tells the candidate a term its own voter was asked for by the
// other controller. Its next campaign goes above it: the replies alone
// carry only promised terms, and two controllers campaigning at
// maxSeen+1 from them land on equal terms, which never yield
// (YieldTo), round after round.
func (c *Candidate) Observe(term uint64) {
	if term > c.maxSeen && !mut("no_observe") {
		c.maxSeen = term
	}
}

// NextLook returns when Tick will next do anything: a driver arms one
// timer for it (and ticks again after any reply).
func (c *Candidate) NextLook() Instant {
	if !c.leading {
		return max(c.nextLook, c.backoffUntil, c.holdUntil)
	}
	return max(c.nextLook, c.backoffUntil)
}

// yield gives up every open campaign and renewal round at now, so no
// grant still in flight can make this candidate lead, and reports true;
// or, while it leads, does nothing and reports false (YieldTo). A dropped
// campaign that no majority had heard backs the candidate off as expire
// would have.
func (c *Candidate) yield(now Instant) bool {
	c.expire(now)
	if c.Leading(now) {
		return false
	}
	for _, rd := range c.rounds {
		if rd.campaign && len(rd.replies) < 2 {
			c.backoffUntil = max(c.backoffUntil, rd.sent.Add(2*c.cfg.Lease))
		}
	}
	c.rounds = c.rounds[:0]
	c.pending = nil
	return true
}

// above returns the term after t, saturating: terms are uint64 and never
// wrap to 0.
func above(t uint64) uint64 {
	if t == ^uint64(0) {
		return t
	}
	return t + 1
}

// Term is the fencing token: while leading, the term leadership was
// acquired in (the epoch), stable across renewals that move the vote term
// (VoteTerm); 0 when not leading, so a standby never holds a token above
// the live leader's. A gap in leadership and a new acquisition always
// change it (terms only rise, and no term is led twice). A receiver
// compares a token against the highest token it has received FROM A
// LEADER, never against its own Term.
func (c *Candidate) Term() uint64 {
	if c.leading {
		return c.epoch
	}
	return 0
}

// VoteTerm returns the vote term the current lease rests on (the term last
// campaigned for when not leading).
func (c *Candidate) VoteTerm() uint64 { return c.term }

// LeaseEnd returns when the current lease ends on this node's clock.
func (c *Candidate) LeaseEnd() Instant { return c.leaseEnd }

// Tick runs the candidate's look at now. seenFrom, seenUntil are the other
// candidate its own voter last saw holding a lease, and until when
// (Voter.LeaderSeen): a standby campaigns only once that has run out,
// plus ρ. healthy is whether
// this replica's scans are live (#621): an unhealthy leader stops renewing
// and an unhealthy standby does not campaign. It returns the request to
// send to every voter (self included), or nil.
func (c *Candidate) Tick(now Instant, seenFrom NodeID, seenUntil Instant, healthy bool) *Request {
	c.expire(now)
	if now < c.nextLook || now < c.backoffUntil {
		return nil
	}
	c.nextLook = now.Add(c.cfg.look())
	if !healthy {
		return nil
	}
	if c.Leading(now) {
		if c.reterm {
			c.reterm = false
			c.renewTerm = above(c.maxSeen)
		}
		return c.send(now, c.renewTerm) // renewal
	}
	// A standby campaigns once the lease its own voter last saw another
	// hold has run out by its own clock, plus ρ.
	if seenFrom != "" && seenFrom != c.cfg.Self &&
		now < seenUntil.Add(time.Duration(c.cfg.Rho*float64(c.cfg.Lease))) {
		return nil
	}
	if now < c.retryAt || c.pending != nil {
		return nil
	}
	if now < c.holdUntil {
		return nil // its own voter is in its restart wait
	}
	if c.Marked() && !mut("marked_still_campaigns") {
		return nil // a voter refuses this name every new grant (NoteMark)
	}
	if c.fencedHold() {
		return nil // a state from before an operator replaced this name (#1029 3c-3c-1b)
	}
	return c.send(now, above(c.maxSeen))
}

// Probe returns a probe for the witness, a term-0 request (id ProbeID) it
// always refuses, once per look while this candidate doesn't lead: in its
// restart hold and its back-off too, since a probe keeps no promise alive.
// The probe and the witness's reply carry each voter's running promise
// (Relay), so this controller's voter and the witness hear what the other
// promised even when no campaign runs (#1029 layer 2): a clone of either
// controller then finds the voter it needs refusing it. nil otherwise.
// The driver sends it to the witness only, stamped with its own voter's
// Relay, and calls Probe at every look.
func (c *Candidate) Probe(now Instant) *Request {
	if mut("standby_no_probe") || c.Leading(now) || now < c.probeAt {
		return nil
	}
	c.probeAt = now.Add(c.cfg.look())
	c.seq++
	return &Request{From: c.cfg.Self, Nonce: c.cfg.Nonce, ID: ProbeID, Seq: c.seq, Gen: c.gen(), Fences: c.fences(), Uncommissioned: c.commissioned != nil && !c.commissioned()}
}

// PushProbe is Probe at once, whatever the look: the driver sends it when
// its own voter has just promised another node (Voter.RelayChanged), so
// the witness hears that promise within a one-way delay instead of up to a
// look later. Until it does, the witness can still grant a clone the name
// its peer's voter has just promised (#1029 layer 2). nil while leading.
func (c *Candidate) PushProbe(now Instant) *Request {
	if mut("standby_no_probe") || mut("relay_not_pushed") || c.Leading(now) {
		return nil
	}
	c.probeAt = now.Add(c.cfg.look())
	c.seq++
	return &Request{From: c.cfg.Self, Nonce: c.cfg.Nonce, ID: ProbeID, Seq: c.seq, Gen: c.gen(), Fences: c.fences(), Uncommissioned: c.commissioned != nil && !c.commissioned()}
}

// ProbesPeer reports whether a driver sends the standby's probe (Probe,
// not PushProbe) to its peer as well as to the witness. It does (#1029
// step 1): a standby that probed only the witness would be heard by
// nobody else while cut from it, and a clone of it would then be the only
// boot of its name any voter sees.
func ProbesPeer() bool { return !mut("standby_probes_witness_only") }

// AdoptProbesPeer reports whether a driver sends an uncommissioned boot's
// adoption probe (Adoption.Tick's) to its peer as well as to the witness.
// It does (#1029 3c-3c-2b, D1): a wiped old box beside its replacement
// that reached only the witness would leave the peer's voter blind to it,
// and once a split moved the box to the peer's side, nobody there would
// have marked its name.
func AdoptProbesPeer() bool { return !mut("adopt_probe_witness_only") }

// NoteMark takes whether one of this candidate's voters marks its name a
// confirmed duplicate (Reply.Marked), from every reply meant for this
// boot, a relay probe's included: the driver calls it before handing the
// reply on (OnReply, or the voter alone for a probe's). While any voter's
// last word was "marked", the candidate campaigns no more (Tick): that
// voter grants this name nothing new, and a campaign it can't win only
// makes its own voter promise it in vain. It keeps probing, which is how
// it hears the mark cleared, and keeps renewing a lease it holds (a mark
// leaves the running promise to this very boot alone). The entry stays
// until that voter answers again, so a voter cut off since keeps the hold:
// a clone's copy whose far voter restarted inside a split stays quiet
// (#1029 3c).
func (c *Candidate) NoteMark(r Reply) {
	if r.To != c.cfg.Self || r.Nonce != c.cfg.Nonce {
		return
	}
	if r.From != c.cfg.Self && r.From != c.cfg.Peer && r.From != c.cfg.Witness {
		return
	}
	// Fenced: this commissioned state is below the voter's fence for its
	// name (#1029 3c-3c-1b). A probe's refusal says so too, so a standby
	// that hears a leader and never campaigns learns it. Not a
	// replacement's: uncommissioned, it re-adopts at the fence, and a
	// late refusal of one of its earlier probes is not below it.
	fenced := r.Fence != 0 && c.commissioned != nil && c.commissioned() && c.gen() < r.Fence && !mut("fence_unnoted")
	if fenced && c.superseded == "" {
		c.superseded = r.From
	}
	if c.fencedBy == nil {
		c.fencedBy = map[NodeID]bool{}
	}
	c.fencedBy[r.From] = fenced
	if r.From == c.cfg.Witness {
		c.witnessWord = true
	}
	if c.genSeen == nil {
		c.genSeen = map[NodeID]uint64{}
	}
	seen := c.genSeen[r.From]
	if !mut("hold_gen_reset_on_boot") {
		c.genSeen[r.From] = max(seen, r.ClearGen)
	}
	if !r.Marked {
		if h, ok := c.markedBy[r.From]; ok && h.releasedBy(r) {
			delete(c.markedBy, r.From)
		}
		return
	}
	if c.markedBy == nil {
		c.markedBy = map[NodeID]markHold{}
	}
	h, ok := c.markedBy[r.From]
	if !ok || h.boot != r.VoterBoot {
		h = markHold{boot: r.VoterBoot, gen: h.gen}
		if mut("hold_gen_reset_on_boot") {
			h.gen = 0
		}
	}
	h.gen = max(h.gen, r.ClearGen, c.genSeen[r.From])
	c.markedBy[r.From] = h
}

// noteRetired takes a reply matched to a round of this boot: from a voter
// boot an operator's clear retired, the copy runs again (paused or hung at
// the clear), and answers now. Its grant backs nothing, and the name is
// marked again (#1135's stated residual, #1029 3c-3b). Only a matched
// reply: a late or replayed one proves nothing (#1144 review). It reports
// whether a grant was discounted.
func (c *Candidate) noteRetired(now Instant, r *Reply) bool {
	if r.From == c.cfg.Self || c.retiredReply == nil || !c.retiredReply(now, r.From, r.VoterBoot) {
		return false
	}
	if !r.Granted {
		return false
	}
	r.Granted = false
	return !mut("discount_reterms")
}

// markHold is one voter's mark on this candidate's name (NoteMark): the
// voter boot that told it, and the clear generation it was marked under.
type markHold struct {
	boot uint64
	gen  uint64
}

// releasedBy reports whether an unmarked reply ends the hold.
func (h markHold) releasedBy(r Reply) bool {
	switch {
	case h.boot == r.VoterBoot, mut("hold_released_by_any_boot"):
		return true // rule (i): the very boot that marked
	case mut("release_ignores_gen"):
		return false // 3c-1's rule (i) alone: only the marking boot
	}
	// Another boot: only after a clear since the hold, not a voter that
	// forgot (restarted before its write, or wiped), nor an older copy.
	return r.ClearGen > h.gen
}

// HoldGen is the highest clear generation any hold on this candidate was
// set under, or could be (the highest any voter's reply carried,
// genSeen: a hold set between the clear's check and its clear phase
// starts there, #1144 re-check): the operator's clear sets every member
// above it, so a member restored from a backup with an older generation
// can't leave this candidate held (#1029 3c-3b).
func (c *Candidate) HoldGen() uint64 {
	var g uint64
	for _, h := range c.markedBy {
		g = max(g, h.gen)
	}
	if !mut("hold_gen_ignores_seen") {
		for _, s := range c.genSeen {
			g = max(g, s)
		}
	}
	return g
}

// Marked reports whether a voter's last reply marked this name a
// confirmed duplicate (NoteMark): the candidate then doesn't campaign.
func (c *Candidate) Marked() bool { return len(c.markedBy) > 0 }

// heldWord is what one voter last said holds this candidate's name
// (Reply.HeldBy), since when it has said so without a break, and when it
// last said it.
type heldWord struct {
	by          uint64
	since, last Instant
}

// NoteHeld takes the incumbent a voter names for this candidate's marked
// name (Reply.HeldBy), from every reply meant for this boot, a relay
// probe's included, received at now (#1029 3c-2). Only the two OTHER
// voters count: this node's own voter may run from a copied disk, and
// its word would be the copy's own. A changed word starts over.
func (c *Candidate) NoteHeld(now Instant, r Reply) {
	if r.To != c.cfg.Self || r.Nonce != c.cfg.Nonce {
		return
	}
	if r.From != c.cfg.Peer && r.From != c.cfg.Witness && !(r.From == c.cfg.Self && mut("spare_own_voter_counts")) {
		return
	}
	if c.held == nil {
		c.held = map[NodeID]heldWord{}
	}
	h := c.held[r.From]
	if r.HeldBy == 0 || r.HeldBy != h.by {
		h = heldWord{by: r.HeldBy, since: now}
	}
	h.last = now
	c.held[r.From] = h
}

// Spare reports whether this boot should demote itself to a spare
// (#1029 3c-2): BOTH other voters have named the SAME other boot of this
// name the incumbent (NoteHeld) for at least a lease each without a
// break. Both, never one: a late frame of a real restart's old boot can
// make one voter name it (false spares up to 300 of 300 with one voter's
// word, 0 with both), and a partitioned original then can't be demoted
// by the one voter it still reaches. Never while this boot holds a
// lease: a leader is not demoted under its own lease (it can't be the
// copy two voters agree on while they renew it, so this only guards a
// rule change). The words are each voter's latest: one cut off since
// keeps its word, as a mark's hold does. The driver then makes the flag
// durable (Voter.MakeSpare) and goes quiet.
func (c *Candidate) Spare(now Instant) bool {
	if c.Leading(now) && !mut("spare_while_leading") {
		return false
	}
	span := c.cfg.Lease
	if mut("spare_no_span") {
		span = 0
	}
	need := 2 // both other voters (held keeps only theirs)
	if mut("spare_one_voter") {
		need = 1
	}
	agree := map[uint64]int{}
	for _, h := range c.held {
		if h.by != 0 && h.by != c.cfg.Nonce && h.last.Sub(h.since) >= span {
			agree[h.by]++
		}
	}
	for _, n := range agree {
		if n >= need {
			return true
		}
	}
	return false
}

// ProbeID is the id of a standby's relay probe (Candidate.Probe), which
// its reply echoes. A driver hands that reply to its voter's Hear only:
// the probe is there for the relay, and its reply tells neither the
// candidate nor the handshake anything (IsRelayProbe).
const ProbeID = ^uint64(0)

// IsRelayProbe reports whether r answers a standby's relay probe.
func IsRelayProbe(r Reply) bool { return r.Term == 0 && r.ID == ProbeID }

func (c *Candidate) send(now Instant, term uint64) *Request {
	c.nextID++
	if term > c.maxSeen {
		c.maxSeen = term
	}
	rd := &round{id: c.nextID, term: term, sent: now,
		grants: map[NodeID]bool{}, replies: map[NodeID]bool{}, campaign: !c.leading}
	if rd.campaign {
		c.term = term
		c.pending = rd
	}
	c.rounds = append(c.rounds, rd)
	c.seq++
	q := &Request{From: c.cfg.Self, Term: term, Nonce: c.cfg.Nonce, ID: c.nextID, Renewal: !rd.campaign, Seq: c.seq, Gen: c.gen(), Fences: c.fences(), Uncommissioned: c.commissioned != nil && !c.commissioned()}
	if q.Renewal {
		q.Epoch, q.MarkSeq, q.MarkCurrent = c.epoch, c.markSeq, c.markCurrent
	}
	return q
}

// SetMark is how far this leader's standby is in sync (#710 S2c): the
// sequence of the last state image it acknowledged in this epoch, and
// whether that was current. Every renewal from then on carries it, and a
// voter that grants one keeps it as this leader's mark. The driver sets
// it; a new lease starts from none (a leader's standby has acknowledged
// nothing of its new epoch yet).
func (c *Candidate) SetMark(seq uint64, current bool) { c.markSeq, c.markCurrent = seq, current }

// OnReply takes a voter's reply, received at now. Only the three voters
// count: this controller, its peer, and the witness at the accepted
// incarnation; a reply from anyone else (another witness copy under
// another name included) is ignored whole. A grant counts only toward the
// request it answers: addressed to this controller, from this boot (the
// nonce), with the same id and term. A lease is timed from the send instant
// of one round that a majority granted.
func (c *Candidate) OnReply(now Instant, r Reply) {
	c.expire(now)
	// quiet: the counted incarnation answered inside its quiet period
	// (Count). Its reply shows the witness hears this controller, so the
	// round counts it as heard (no back-off as if unheard), but its grant
	// does not count toward a lease.
	quiet := r.From == c.cfg.Witness && r.Incarnation == c.countInc && r.Incarnation != 0 && now < c.countFrom &&
		!mut("quiet_reply_unheard")
	switch {
	case r.From == c.cfg.Self, r.From == c.cfg.Peer:
	case r.From == c.cfg.Witness && r.Incarnation == c.countInc && r.Incarnation != 0 && now >= c.countFrom:
	case quiet:
	case r.From == c.cfg.Witness && mut("any_incarnation"):
	case mut("any_sender") && r.From != c.cfg.Witness:
	default:
		return // not one of our voters, or a witness incarnation we don't count
	}
	if (r.To != c.cfg.Self && !mut("no_grantee_check")) || (r.Nonce != c.cfg.Nonce && !mut("no_nonce_check")) {
		return // meant for the other controller, or for an earlier boot
	}
	if r.From != c.cfg.Self && c.fenced != nil && c.fenced(r.From, r.Gen) && !mut("fence_replies_off") {
		// The voter's state is from before an operator replaced its name
		// (recommission --from-spare): its word counts for nothing here,
		// grant, refusal or promised term (#1029 3c-3c-1b).
		return
	}
	discounted := false
	if mut("retired_reply_unmatched") {
		discounted = c.noteRetired(now, &r)
	}
	if r.Granted && c.duplicated != nil && c.duplicated(now, r.From) && !mut("dup_voter_counts") {
		// Two processes cast this voter's vote: it backs no lease, this
		// controller's own included (#1029 step 1). It is still a grant,
		// not a term conflict: it never re-terms a leader.
		r.Granted = false
		discounted = !mut("discount_reterms")
	}
	if r.Promised > c.maxSeen {
		c.maxSeen = r.Promised
	}
	var rd *round
	for _, x := range c.rounds {
		if (x.id == r.ID || mut("match_by_term")) && x.term == r.Term {
			rd = x
		}
	}
	if rd == nil {
		return // not ours, or long gone: a replay, or late
	}
	if !mut("retired_reply_unmatched") && c.noteRetired(now, &r) {
		discounted = true
	}
	rd.replies[r.From] = true
	if r.Granted {
		if !quiet {
			rd.grants[r.From] = true
		}
	} else if !discounted && c.leading && !rd.campaign && rd.term == c.renewTerm && r.Promised >= rd.term {
		// A voter refused our renewal at the term we renew at now: it
		// promised this term or a later one elsewhere (a standby's lost
		// campaign). Renew at a higher term from now on, so it can grant us
		// again once that runs out. A refusal of an older round (late, or
		// a duplicate refused because we re-termed since) changes nothing,
		// or stragglers would re-term us without end. Nor does a grant
		// discounted above: re-terming would not change it, and a voter
		// marked for good would re-term every renewal (#1029 3c).
		c.reterm = !mut("no_reterm")
	}
	if (!r.Granted || (len(rd.replies) == 2 && !mut("retry_after_refusal_only"))) && !c.leading && len(rd.replies) >= 2 && len(rd.grants) < 2 {
		// Heard, and refused by a majority: retry on a later look, after
		// the jitter. Whichever reply completes the two: its own voter's
		// grant can come back after the other voter's refusal (its fsync),
		// and the round would otherwise wait out a whole lease (#1029
		// layer 2: a stale relayed promise refuses a takeover's first
		// campaign at the witness).
		var j time.Duration
		if c.cfg.Jitter != nil {
			j = c.cfg.Jitter()
		}
		// The retry moves the look itself, to half a look plus the jitter
		// (lease/8 to 3·lease/8 on): on the lease/4 grid a jitter under
		// lease/4 would change nothing, and two controllers started
		// together would campaign in lockstep at equal terms for ever.
		c.retryAt = now.Add(c.cfg.look()/2 + j)
		if !mut("retry_on_grid") {
			c.nextLook = c.retryAt
		}
		if c.pending == rd {
			c.pending = nil
		}
	}
	if len(rd.grants) < 2 || rd.term < c.term || (!c.leading && rd.term != c.term) {
		return
	}
	if !c.leading && now < c.backoffUntil {
		return // a lapse is final: no lease from a late majority in the back-off
	}
	end := rd.sent.Add(c.cfg.leaseFor())
	if mut("lease_from_reply") {
		end = now.Add(c.cfg.leaseFor())
	}
	if !c.leading {
		if c.pending == rd {
			c.pending = nil
		}
		if now >= end {
			return // granted too late to lead at all
		}
		c.leading = true
		c.leaseEnd = end
		c.epoch, c.renewTerm = rd.term, rd.term
		c.markSeq, c.markCurrent = 0, false
		c.reterm = false // a refusal from before this lease means nothing to it
		// Renew at once, not at the next look: a slow vote (a voter's
		// fsync, a long link) may have left less of this lease than a look.
		// The renewal changes nothing durable at a voter (same term, same
		// grantee), so it is answered without waiting for a write.
		if !mut("renew_on_look") {
			c.nextLook = now
		}
	} else if end > c.leaseEnd {
		// A renewal majority, possibly at a higher vote term (a re-term):
		// the lease now rests on it. The epoch stays.
		c.leaseEnd = end
		c.term = rd.term
	}
}

// expire ends a lapsed lease, and drops rounds no reply can still matter
// to. A lapsed lease, or a campaign not even HEARD by a majority within a
// lease (this controller can send but not hear, or reaches nobody), backs
// the candidate off: it sends nothing for two leases after the lease ended,
// or after the round was sent, so the promises it may be keeping alive run
// out and a reachable controller wins first (design §3.1, one-way
// partitions). Two, not one: the standby campaigns up to lease + ρ·lease +
// lease/4 after the last renewal it saw, and a candidate back sooner would
// win its voter again with a campaign it can't hear answered.
// The back-off silences only the candidate; this node's voter keeps
// answering.
func (c *Candidate) expire(now Instant) {
	if c.leading && now >= c.leaseEnd {
		c.leading = false
		c.reterm = false
		switch {
		case mut("no_backoff"):
		case mut("one_lease_backoff"):
			c.backoffUntil = c.leaseEnd.Add(c.cfg.Lease)
		default:
			c.backoffUntil = c.leaseEnd.Add(2 * c.cfg.Lease)
		}
	}
	keep := c.rounds[:0]
	for _, rd := range c.rounds {
		if now < rd.sent.Add(c.cfg.Lease) {
			keep = append(keep, rd)
			continue
		}
		if rd.campaign && len(rd.replies) < 2 && !mut("no_backoff") {
			if b := rd.sent.Add(2 * c.cfg.Lease); b > c.backoffUntil {
				c.backoffUntil = b
			}
		}
		if c.pending == rd {
			c.pending = nil
		}
	}
	c.rounds = keep
}
