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
	// Lease is how long a grant promises (design: default 2 s).
	Lease time.Duration
	// Rho bounds the RELATIVE rate of any two voters' clocks (default
	// 0.01). The leader's lease is shortened by it, so it ends before any
	// voter's promise does.
	Rho float64
	// Incarnation is the witness incarnation this controller accepts; a
	// witness grant from any other is not counted. 0 accepts none.
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
	// reterm: a voter refused a renewal at a term it had promised to
	// another (a standby's lost campaign); the next renewal asks for a
	// higher term, which that voter can grant once its promise runs out.
	reterm   bool
	nextID   uint64
	rounds   []*round
	leading  bool
	leaseEnd Instant // leading only until here, on this node's clock

	nextLook     Instant
	backoffUntil Instant // the candidate sends nothing before this
	retryAt      Instant // a refused campaign retries no earlier
	// pending is the campaign in flight: no new campaign starts until a
	// majority has answered it or a lease has passed (expire).
	pending *round
}

// NewCandidate starts a candidate at now that knows of terms up to known
// (its own voter's promised term). cfg must pass Validate.
func NewCandidate(cfg Config, known uint64, now Instant) (*Candidate, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &Candidate{cfg: cfg, maxSeen: known, nextLook: now}, nil
}

// Leading reports whether this controller holds a lease at now. It is
// judged when read, against the lease end, not on a tick: a leader whose
// renewals stopped closes its gate on time even if Tick is late.
func (c *Candidate) Leading(now Instant) bool { return c.leading && now < c.leaseEnd }

// Term is the fencing token: while leading, the term leadership was
// acquired in (the epoch), stable across renewals that move the vote term
// (VoteTerm); otherwise the term last campaigned for. A gap in leadership
// and a new acquisition always change it (terms only rise, and no term is
// led twice).
func (c *Candidate) Term() uint64 {
	if c.leading {
		return c.epoch
	}
	return c.term
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
			c.renewTerm = c.maxSeen + 1
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
	return c.send(now, c.maxSeen+1)
}

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
	return &Request{From: c.cfg.Self, Term: term, Nonce: c.cfg.Nonce, ID: c.nextID, Renewal: !rd.campaign}
}

// OnReply takes a voter's reply, received at now. Only the three voters
// count: this controller, its peer, and the witness at the accepted
// incarnation; a reply from anyone else (another witness copy under
// another name included) is ignored whole. A grant counts only toward the
// request it answers: addressed to this controller, from this boot (the
// nonce), with the same id and term. A lease is timed from the send instant
// of one round that a majority granted.
func (c *Candidate) OnReply(now Instant, r Reply) {
	c.expire(now)
	switch {
	case r.From == c.cfg.Self, r.From == c.cfg.Peer:
	case r.From == c.cfg.Witness && r.Incarnation == c.cfg.Incarnation && r.Incarnation != 0:
	default:
		return // not one of our voters, or a witness incarnation we don't count
	}
	if r.To != c.cfg.Self || r.Nonce != c.cfg.Nonce {
		return // meant for the other controller, or for an earlier boot
	}
	if r.Promised > c.maxSeen {
		c.maxSeen = r.Promised
	}
	var rd *round
	for _, x := range c.rounds {
		if x.id == r.ID && x.term == r.Term {
			rd = x
		}
	}
	if rd == nil {
		return // not ours, or long gone: a replay, or late
	}
	rd.replies[r.From] = true
	if r.Granted {
		rd.grants[r.From] = true
	} else if c.leading && !rd.campaign && rd.term == c.renewTerm && r.Promised >= rd.term {
		// A voter refused our renewal at the term we renew at now: it
		// promised this term or a later one elsewhere (a standby's lost
		// campaign). Renew at a higher term from now on, so it can grant us
		// again once that runs out. A refusal of an older round (late, or
		// a duplicate refused because we re-termed since) changes nothing,
		// or stragglers would re-term us without end.
		c.reterm = true
	}
	if !r.Granted && !c.leading && len(rd.replies) >= 2 && len(rd.grants) < 2 {
		// Heard, and refused by a majority: retry on a later look, after
		// the jitter.
		var j time.Duration
		if c.cfg.Jitter != nil {
			j = c.cfg.Jitter()
		}
		c.retryAt = now.Add(j)
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
		c.backoffUntil = c.leaseEnd.Add(2 * c.cfg.Lease)
	}
	keep := c.rounds[:0]
	for _, rd := range c.rounds {
		if now < rd.sent.Add(c.cfg.Lease) {
			keep = append(keep, rd)
			continue
		}
		if rd.campaign && len(rd.replies) < 2 {
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
