package quorum

import (
	"testing"
	"time"
)

const ms = time.Millisecond

func cfgA() Config {
	return Config{Self: "A", Peer: "B", Witness: "W", Lease: lease, Rho: 0.01, Incarnation: 42, Nonce: 0xA1}
}

func newC(t *testing.T, cfg Config, known uint64, now Instant) *Candidate {
	t.Helper()
	c, err := NewCandidate(cfg, known, now)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// granted is what voter from answers to r: a grant addressed to r's sender,
// echoing its term, nonce and id.
func granted(from NodeID, r *Request, inc uint64) Reply {
	return Reply{From: from, To: r.From, Term: r.Term, Nonce: r.Nonce, ID: r.ID, Granted: true, Promised: r.Term, Incarnation: inc}
}

func refused(from NodeID, r *Request, promised, inc uint64) Reply {
	return Reply{From: from, To: r.From, Term: r.Term, Nonce: r.Nonce, ID: r.ID, Promised: promised, Incarnation: inc}
}

// The lease runs from the SEND instant of the granted request, shortened
// by ρ: 2 s × (1 − 0.01) = 1.98 s, so a request sent at 0 and granted at
// 100 ms leads until 1.98 s, not 2.08 s.
func TestCandidateLeaseFromSendMinusRho(t *testing.T) {
	c := newC(t, cfgA(), 0, 0)
	r := c.Tick(0, "", 0, true)
	if r == nil || r.Term != 1 {
		t.Fatalf("first look: %+v, want a campaign for term 1", r)
	}
	c.OnReply(at(100*ms), granted("A", r, 0))
	if c.Leading(at(100 * ms)) {
		t.Fatal("leading on one grant of three")
	}
	c.OnReply(at(100*ms), granted("B", r, 0))
	if !c.Leading(at(100 * ms)) {
		t.Fatal("not leading on a majority")
	}
	if c.LeaseEnd() != at(1980*ms) {
		t.Fatalf("lease end = %v, want 1.98 s", time.Duration(c.LeaseEnd()))
	}
	if !c.Leading(at(1979*ms)) || c.Leading(at(1980*ms)) {
		t.Fatal("the gate is not judged against 1.98 s when read")
	}
}

// A late grant for one request never counts toward a later one: the
// renewal sent at 500 ms extends the lease to 2.48 s only on its OWN
// majority, not on a grant for the campaign that arrives after it left.
func TestCandidateGrantCountsOnlyForItsRequest(t *testing.T) {
	c := newC(t, cfgA(), 0, 0)
	r1 := c.Tick(0, "", 0, true)
	c.OnReply(at(10*ms), granted("A", r1, 0))
	c.OnReply(at(10*ms), granted("B", r1, 0))
	r2 := c.Tick(at(500*ms), "A", 0, true)
	if r2 == nil || r2.Term != 1 || r2.ID == r1.ID {
		t.Fatalf("renewal: %+v", r2)
	}
	c.OnReply(at(510*ms), granted("A", r2, 0))
	c.OnReply(at(600*ms), granted("W", r1, 42)) // late, for r1
	if c.LeaseEnd() != at(1980*ms) {
		t.Fatalf("a late r1 grant moved the lease to %v", time.Duration(c.LeaseEnd()))
	}
	c.OnReply(at(610*ms), granted("W", r2, 42))
	if c.LeaseEnd() != at(2480*ms) {
		t.Fatalf("lease end = %v, want 0.5 s + 1.98 s = 2.48 s", time.Duration(c.LeaseEnd()))
	}
	// A replay of an answered request is ignored too (F31).
	c.OnReply(at(700*ms), Reply{From: "W", To: "A", Term: 1, Nonce: 0xA1, ID: 999, Granted: true, Incarnation: 42})
	if c.LeaseEnd() != at(2480*ms) {
		t.Fatal("a reply to no request of ours counted")
	}
}

// A witness grant counts only from the accepted incarnation, and a
// controller with none accepted counts no witness grant at all.
func TestCandidateWitnessIncarnation(t *testing.T) {
	c := newC(t, cfgA(), 0, 0)
	r := c.Tick(0, "", 0, true)
	c.OnReply(at(10*ms), granted("A", r, 0))
	c.OnReply(at(10*ms), granted("W", r, 7)) // another copy
	if c.Leading(at(10 * ms)) {
		t.Fatal("led on a grant from a witness incarnation it does not accept")
	}
	c.OnReply(at(20*ms), granted("W", r, 42))
	if !c.Leading(at(20 * ms)) {
		t.Fatal("did not lead on the accepted incarnation")
	}

	cfg := cfgA()
	cfg.Incarnation = 0
	c = newC(t, cfg, 0, 0)
	r = c.Tick(0, "", 0, true)
	c.OnReply(at(10*ms), granted("A", r, 0))
	c.OnReply(at(10*ms), granted("W", r, 0))
	if c.Leading(at(10 * ms)) {
		t.Fatal("counted a witness grant with no incarnation accepted")
	}
}

// A lease that lapses silences the candidate for two leases after its end:
// 1.98 s + 2 × 2 s = 5.98 s (design §3.1, one-way partitions). The
// standby campaigns up to lease + ρ·lease + lease/4 after the leader's
// last renewal, so one lease would let a leader that can send but not hear
// campaign first and win the standby's voter for another lease.
func TestCandidateBackOffAfterLapse(t *testing.T) {
	c := newC(t, cfgA(), 0, 0)
	r := c.Tick(0, "", 0, true)
	c.OnReply(at(10*ms), granted("A", r, 0))
	c.OnReply(at(10*ms), granted("B", r, 0))
	for now := 500 * ms; now < 5980*ms; now += 500 * ms {
		if r := c.Tick(at(now), "A", 0, true); r != nil && now >= 1980*ms {
			t.Fatalf("sent %+v at %v, inside the back-off", r, now)
		}
	}
	if c.Leading(at(1980 * ms)) {
		t.Fatal("still leading after the lease end")
	}
	r = c.Tick(at(5980*ms), "A", 0, true)
	if r == nil || r.Term != 2 {
		t.Fatalf("after the back-off: %+v, want a campaign for term 2", r)
	}
	// A late majority for a renewal sent before the lapse doesn't revive
	// the lease inside the back-off.
	c2 := newC(t, cfgA(), 0, 0)
	r = c2.Tick(0, "", 0, true)
	c2.OnReply(at(10*ms), granted("A", r, 0))
	c2.OnReply(at(10*ms), granted("B", r, 0))
	r2 := c2.Tick(at(1500*ms), "A", 0, true)
	c2.Tick(at(2000*ms), "A", 0, true) // lapses at 1.98 s
	c2.OnReply(at(2100*ms), granted("A", r2, 0))
	c2.OnReply(at(2100*ms), granted("B", r2, 0))
	if c2.Leading(at(2100 * ms)) {
		t.Fatal("a late majority revived a lapsed lease inside the back-off")
	}
}

// A campaign no majority even heard within a lease backs the candidate off
// for one lease from then: sent at 0, unheard at 2 s, silent until 4 s.
func TestCandidateBackOffWhenUnheard(t *testing.T) {
	c := newC(t, cfgA(), 0, 0)
	if c.Tick(0, "", 0, true) == nil {
		t.Fatal("no campaign")
	}
	for now := 500 * ms; now <= 2*time.Second; now += 500 * ms {
		if r := c.Tick(at(now), "A", 0, true); r != nil && now >= 2*time.Second {
			t.Fatalf("campaigned at %v, unheard rounds must back off", now)
		}
	}
	if r := c.Tick(at(3999*ms), "A", 0, true); r != nil {
		t.Fatalf("campaigned at 3.999 s inside the back-off: %+v", r)
	}
	if r := c.Tick(at(4*time.Second), "A", 0, true); r == nil {
		t.Fatal("no campaign once the back-off ended at 4 s")
	}
}

// A standby campaigns on its first look after its own voter's promise to
// the leader ran out, plus ρ·lease (20 ms). Looks fall every lease/4 =
// 500 ms from 0.
func TestCandidateCampaignsAfterPromisePlusRho(t *testing.T) {
	c := newC(t, cfgA(), 3, 0)
	until := at(5 * time.Second)
	for now := time.Duration(0); now <= 5*time.Second; now += 500 * ms {
		if r := c.Tick(at(now), "B", until, true); r != nil {
			t.Fatalf("campaigned at %v while B's promise ran to 5 s + 20 ms", now)
		}
	}
	r := c.Tick(at(5500*ms), "B", until, true)
	if r == nil || r.Term != 4 {
		t.Fatalf("at 5.5 s: %+v, want a campaign for term 4 (known 3 + 1)", r)
	}
	// A promise ending at 5.49 s: 5.5 s < 5.51 s, so the next look, 6 s.
	c = newC(t, cfgA(), 3, 0)
	until = at(5490 * ms)
	for now := time.Duration(0); now <= 5500*ms; now += 500 * ms {
		if r := c.Tick(at(now), "B", until, true); r != nil {
			t.Fatalf("campaigned at %v, before 5.51 s", now)
		}
	}
	if c.Tick(at(6*time.Second), "B", until, true) == nil {
		t.Fatal("no campaign at 6 s")
	}
}

// An unhealthy leader stops renewing, and an unhealthy standby does not
// campaign (#621).
func TestCandidateHealth(t *testing.T) {
	c := newC(t, cfgA(), 0, 0)
	if c.Tick(0, "", 0, false) != nil {
		t.Fatal("an unhealthy standby campaigned")
	}
	r := c.Tick(500*Instant(ms), "", 0, true)
	c.OnReply(at(510*ms), granted("A", r, 0))
	c.OnReply(at(510*ms), granted("B", r, 0))
	if c.Tick(at(1000*ms), "A", 0, false) != nil {
		t.Fatal("an unhealthy leader renewed")
	}
}

// A majority that arrives after its own lease would already have ended
// leads nothing.
func TestCandidateTooLateMajority(t *testing.T) {
	c := newC(t, cfgA(), 0, 0)
	r := c.Tick(0, "", 0, true)
	c.OnReply(at(1990*ms), granted("A", r, 0))
	c.OnReply(at(1990*ms), granted("B", r, 0))
	if c.Leading(at(1990 * ms)) {
		t.Fatal("led on a majority received after 1.98 s")
	}
}

// Refused by a majority: the retry waits for the jitter, then the next look.
func TestCandidateRetryJitter(t *testing.T) {
	cfg := cfgA()
	cfg.Jitter = func() time.Duration { return 300 * ms }
	c := newC(t, cfg, 0, 0)
	r := c.Tick(0, "", 0, true)
	c.OnReply(at(10*ms), refused("B", r, 5, 0))
	c.OnReply(at(10*ms), refused("W", r, 5, 42))
	// The retry is the next look: 10 ms + 250 ms (half a look) + 300 ms,
	// off the 500 ms grid, so two controllers refused together don't stay
	// in lockstep.
	if c.NextLook() != at(560*ms) || c.Tick(at(559*ms), "A", 0, true) != nil {
		t.Fatalf("next look %v, want 560 ms and nothing before it", time.Duration(c.NextLook()))
	}
	r = c.Tick(at(560*ms), "A", 0, true)
	if r == nil || r.Term != 6 {
		t.Fatalf("retry: %+v, want term 6 (promised 5 seen + 1)", r)
	}
	c.OnReply(at(600*ms), refused("B", r, 6, 0))
	c.OnReply(at(600*ms), refused("W", r, 6, 42))
	cfg.Jitter = func() time.Duration { return 450 * ms }
	c.cfg.Jitter = cfg.Jitter
	// 600 + 250 + 300 = 1.15 s (the jitter is drawn at the refusal).
	if c.Tick(at(1149*ms), "A", 0, true) != nil || c.Tick(at(1150*ms), "A", 0, true) == nil {
		t.Fatal("the retry is not at 1.15 s")
	}
}

// A campaign the witness refuses before its own voter's grant comes back
// (that grant waits for its fsync) retries half a look plus the jitter
// later, as when the refusal comes last, not a whole lease later (#1029
// layer 2: a stale relayed promise refuses a takeover's first campaign).
func TestCandidateRetriesWhenItsOwnGrantComesBackLast(t *testing.T) {
	cfg := cfgA()
	cfg.Jitter = func() time.Duration { return 300 * ms }
	c := newC(t, cfg, 0, 0)
	r := c.Tick(0, "", 0, true)
	c.OnReply(at(10*ms), refused("W", r, 0, 42))
	c.OnReply(at(15*ms), granted("A", r, 0))
	if c.NextLook() != at(565*ms) {
		t.Fatalf("next look %v, want 565 ms (15 ms + half a look + the jitter), not a lease on", time.Duration(c.NextLook()))
	}
	if r2 := c.Tick(at(565*ms), "", 0, true); r2 == nil || r2.Term <= r.Term {
		t.Fatalf("retry at 565 ms: %+v", r2)
	}
}

// Only the three voters count. A grant from a name that is none of them,
// a second witness copy under another name for instance, is ignored, even
// carrying the accepted incarnation (naut-lead's #961 review: A + "W2"
// made A leader).
func TestCandidateCountsOnlyItsVoters(t *testing.T) {
	c := newC(t, cfgA(), 0, 0)
	r := c.Tick(0, "", 0, true)
	c.OnReply(at(10*ms), granted("A", r, 0))
	c.OnReply(at(10*ms), granted("W2", r, 42))
	c.OnReply(at(10*ms), granted("", r, 0))
	if c.Leading(at(10 * ms)) {
		t.Fatal("led on a grant from a voter that is not self, peer or witness")
	}
	c.OnReply(at(20*ms), granted("B", r, 0))
	if !c.Leading(at(20 * ms)) {
		t.Fatal("did not lead on self + peer")
	}
}

// Both controllers campaign for the same term with the same id. The
// witness's grant to B, delivered to A (replayed or misrouted), is B's,
// not A's: otherwise A + self and B + witness both lead.
func TestCandidateIgnoresAGrantMeantForThePeer(t *testing.T) {
	a := newC(t, cfgA(), 0, 0)
	cb := cfgA()
	cb.Self, cb.Peer, cb.Nonce = "B", "A", 0xB1
	b := newC(t, cb, 0, 0)
	ra, rb := a.Tick(0, "", 0, true), b.Tick(0, "", 0, true)
	if ra.Term != rb.Term || ra.ID != rb.ID {
		t.Fatalf("setup: want the same (term, id), got %+v and %+v", ra, rb)
	}
	toB := granted("W", rb, 42)
	a.OnReply(at(10*ms), granted("A", ra, 0))
	a.OnReply(at(10*ms), toB)
	if a.Leading(at(10 * ms)) {
		t.Fatal("A led on the witness's grant to B")
	}
	// The same grant with the address forged to A still carries B's
	// boot nonce.
	toB.To = "A"
	a.OnReply(at(10*ms), toB)
	if a.Leading(at(10 * ms)) {
		t.Fatal("A led on a grant carrying B's nonce")
	}
}

// A grant from an earlier boot of this controller, for the same (term, id)
// the new boot just sent, does not count: the nonce differs.
func TestCandidateIgnoresAGrantFromAnEarlierBoot(t *testing.T) {
	old := cfgA()
	old.Nonce = 0x01
	c1 := newC(t, old, 4, 0)
	r1 := c1.Tick(0, "", 0, true)
	stale := granted("W", r1, 42)

	fresh := cfgA()
	fresh.Nonce = 0x02
	c2 := newC(t, fresh, 4, at(10*time.Second))
	r2 := c2.Tick(at(10*time.Second), "", 0, true)
	if r2.Term != r1.Term || r2.ID != r1.ID {
		t.Fatalf("setup: want the same (term, id) across boots, got %+v and %+v", r1, r2)
	}
	c2.OnReply(at(10*time.Second+ms), granted("A", r2, 0))
	c2.OnReply(at(10*time.Second+ms), stale)
	if c2.Leading(at(10*time.Second + ms)) {
		t.Fatal("led on a grant from an earlier boot")
	}
}

func TestConfigValidate(t *testing.T) {
	for name, mut := range map[string]func(*Config){
		"zero rho":        func(c *Config) { c.Rho = 0 },
		"negative rho":    func(c *Config) { c.Rho = -0.01 },
		"absurd rho":      func(c *Config) { c.Rho = 0.5 },
		"zero lease":      func(c *Config) { c.Lease = 0 },
		"zero nonce":      func(c *Config) { c.Nonce = 0 },
		"no witness":      func(c *Config) { c.Witness = "" },
		"peer is self":    func(c *Config) { c.Peer = c.Self },
		"witness is peer": func(c *Config) { c.Witness = c.Peer },
	} {
		cfg := cfgA()
		mut(&cfg)
		if _, err := NewCandidate(cfg, 0, 0); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := NewCandidate(Config{}, 0, 0); err == nil {
		t.Error("the zero Config (no ρ) was accepted")
	}
	cfg := cfgA()
	cfg.Rho = MaxRho
	if _, err := NewCandidate(cfg, 0, 0); err != nil {
		t.Errorf("ρ = MaxRho refused: %v", err)
	}
}

// The grantee is checked on its own, not only through the nonce: should
// both controllers draw the same nonce, a grant addressed to B still
// does not count for A.
func TestCandidateGranteeCheckedEvenOnANonceCollision(t *testing.T) {
	a := newC(t, cfgA(), 0, 0)
	cb := cfgA()
	cb.Self, cb.Peer = "B", "A" // same nonce as A
	b := newC(t, cb, 0, 0)
	ra, rb := a.Tick(0, "", 0, true), b.Tick(0, "", 0, true)
	a.OnReply(at(10*ms), granted("A", ra, 0))
	a.OnReply(at(10*ms), granted("W", rb, 42)) // addressed to B
	if a.Leading(at(10 * ms)) {
		t.Fatal("A led on a grant addressed to B")
	}
}

// In its quiet period (a restart, or a new adoption) the counted witness
// incarnation's grant does not count, but its reply shows the link works:
// a campaign it and the own voter refuse is refused by a majority and
// retries on a later look, not backed off two leases as if unheard.
func TestQuietReplyIsHeardNotCounted(t *testing.T) {
	c := newC(t, cfgA(), 0, 0)
	c.Count(42, at(lease)) // quiet until one lease
	r := c.Tick(0, "", 0, true)
	c.OnReply(at(10*ms), granted("A", r, 0))
	c.OnReply(at(10*ms), granted("W", r, 42))
	if c.Leading(at(10 * ms)) {
		t.Fatal("led on a grant in the quiet period")
	}
	c = newC(t, cfgA(), 0, 0)
	c.Count(42, at(lease))
	r = c.Tick(0, "", 0, true)
	c.OnReply(at(10*ms), refused("A", r, 0, 0))
	c.OnReply(at(10*ms), refused("W", r, 0, 42))
	if r2 := c.Tick(at(300*ms), "", 0, true); r2 == nil || r2.ID == r.ID {
		t.Fatal("no retry at 300 ms: the refusal in the quiet period was not heard")
	}
}
