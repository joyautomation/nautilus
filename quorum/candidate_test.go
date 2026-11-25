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

// A campaign no majority even heard within a lease backs the candidate
// off two leases from its last ask (#710): sent at 0 and asked again (same
// term, a new id) at the looks of 0.5 s and 1 s (up to three quarters of a
// lease, 1.5 s, from the first send); unheard at 2 s, silent until 5 s,
// since a voter that took the 1 s ask promised it until 3 s.
func TestCandidateBackOffWhenUnheard(t *testing.T) {
	c := newC(t, cfgA(), 0, 0)
	r := c.Tick(0, "", 0, true)
	if r == nil {
		t.Fatal("no campaign")
	}
	var asks []Instant
	for now := 500 * ms; now <= 2*time.Second; now += 500 * ms {
		q := c.Tick(at(now), "A", 0, true)
		if q == nil {
			continue
		}
		if q.Term != r.Term || q.ID <= r.ID || q.Seq <= r.Seq || q.Renewal {
			t.Fatalf("at %v: %+v, want the campaign %+v asked again under a new id and Seq", now, q, r)
		}
		asks = append(asks, at(now))
	}
	if len(asks) != 2 || asks[0] != at(500*ms) || asks[1] != at(time.Second) {
		t.Fatalf("asked again at %v, want 0.5 s and 1 s (not from 1.5 s on)", asks)
	}
	if r := c.Tick(at(4999*ms), "A", 0, true); r != nil {
		t.Fatalf("campaigned at 4.999 s inside the back-off from the last ask: %+v", r)
	}
	if r := c.Tick(at(5*time.Second), "A", 0, true); r == nil || r.Term <= 1 {
		t.Fatalf("at 5 s, once the back-off ended: %+v, want a new campaign", r)
	}
}

// A pending campaign that reask declines to ask again never leaves
// NextLook at a look already past (#710): a driver arms its timer there,
// and one in the past fires at once, again and again, so it would spin
// until the round expires (the simulator hung on it). Whatever stops the
// re-ask (the three quarters of a lease run out, the controller unhealthy,
// another leader's lease seen on its own voter), the looks move on.
func TestCandidateNextLookNeverPastWhenAReaskIsDeclined(t *testing.T) {
	for _, tc := range []struct {
		name     string
		seenFrom NodeID
		healthy  func(Instant) bool
	}{
		{"window run out", "", func(Instant) bool { return true }},
		{"unhealthy", "", func(now Instant) bool { return now < at(300*ms) }},
		{"another leader seen", "B", func(Instant) bool { return true }},
	} {
		c := newC(t, cfgA(), 0, 0)
		if c.Tick(0, "", 0, true) == nil {
			t.Fatalf("%s: no campaign", tc.name)
		}
		// A re-ask at a look's instant leaves that look to the next Tick at
		// the same instant: two Ticks at one instant, never three.
		now, same := Instant(0), 0
		for now < at(3*time.Second) {
			next := c.NextLook()
			if next < now {
				t.Fatalf("%s: at %v the next look is %v, already past", tc.name, time.Duration(now), time.Duration(next))
			}
			if same = map[bool]int{true: same + 1, false: 0}[next == now]; same > 1 {
				t.Fatalf("%s: at %v the next look is now again: the driver would spin", tc.name, time.Duration(now))
			}
			now = next
			seenFrom, seenUntil := NodeID(""), Instant(0)
			if tc.seenFrom != "" && now >= at(300*ms) {
				seenFrom, seenUntil = tc.seenFrom, now.Add(time.Second)
			}
			c.Tick(now, seenFrom, seenUntil, tc.healthy(now))
		}
	}
}

// A campaign asked again (#710) goes under a new id, same term, and its
// lease runs from the send of the request each grant answers, the earlier
// of the two: a voter promises from when it took the request it grants.
// Sent at 0 (id 1), asked again at 250 ms (lease/8):
//
//   - both grants answer the re-ask: the lease ends at 2.23 s, from
//     250 ms, not at 1.98 s from the first send (on a slow link the first
//     renewal would land after that: sim TestASingleRestartRaisesNo
//     Duplicate, seed 12);
//
//   - both answer the first ask, landing after the re-ask left: 1.98 s,
//     from 0, not from the latest ask;
//
//   - one of each: from the earlier, 1.98 s;
//
//   - A grants the re-ask, then its grant of the first ask lands: still
//     from 250 ms, since A's promise runs from the later ask it took;
//
//   - three grants: from the second latest, which two voters both
//     promised from: A first, W and B the re-ask, 2.23 s; A and W first,
//     B the re-ask, 1.98 s.
func TestCandidateLeaseFromTheAskItsGrantsAnswer(t *testing.T) {
	for _, tc := range []struct {
		name     string
		aFirst   bool // A's grant answers the first ask
		wFirst   bool // W's grant answers the first ask
		b        bool // B grants the re-ask too
		aLate    bool // A's grant of the first ask lands after its re-ask's
		leaseEnd time.Duration
	}{
		{"both answer the re-ask", false, false, false, false, 2230 * ms},
		{"A's first-ask grant lands last", false, false, false, true, 2230 * ms},
		{"both answer the first ask", true, true, false, false, 1980 * ms},
		{"one of each", true, false, false, false, 1980 * ms},
		{"three, two answer the re-ask", true, false, true, false, 2230 * ms},
		{"three, two answer the first ask", true, true, true, false, 1980 * ms},
	} {
		c := newC(t, cfgA(), 0, 0)
		r := c.Tick(0, "", 0, true)
		if r == nil {
			t.Fatal("no campaign")
		}
		if c.NextLook() != at(250*ms) {
			t.Fatalf("%s: next look %v, want the re-ask at 250 ms", tc.name, time.Duration(c.NextLook()))
		}
		q := c.Tick(at(250*ms), "", 0, true)
		if q == nil || q.Term != r.Term || q.ID == r.ID || q.Seq <= r.Seq || q.Renewal {
			t.Fatalf("%s: at 250 ms: %+v, want %+v asked again under a new id", tc.name, q, r)
		}
		ga, gw := q, q
		if tc.aFirst {
			ga = r
		}
		if tc.wFirst {
			gw = r
		}
		c.OnReply(at(300*ms), granted("A", ga, 0))
		if tc.aLate {
			c.OnReply(at(300*ms), granted("A", r, 0))
		}
		c.OnReply(at(300*ms), granted("W", gw, 42))
		if tc.b {
			c.OnReply(at(300*ms), granted("B", q, 0))
		}
		if !c.Leading(at(300 * ms)) {
			t.Fatalf("%s: not leading on a majority", tc.name)
		}
		if c.LeaseEnd() != at(tc.leaseEnd) {
			t.Fatalf("%s: lease end %v, want %v", tc.name, time.Duration(c.LeaseEnd()), tc.leaseEnd)
		}
		if q2 := c.Tick(at(300*ms), "", 0, true); q2 == nil || !q2.Renewal {
			t.Fatalf("%s: after winning: %+v, want a renewal at once, not another ask", tc.name, q2)
		}
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

// A refusal at the term a leader renews at, from a voter that granted
// this boot that very term (a restarted voter in its restart wait, a
// promise relayed to it, a floor at the term), re-terms only once the term
// stops renewing: while the other two grant it, a re-term would cost a
// write at every voter (#1120: on a slow disk it lost the lease) and win
// nothing. Once they don't (a witness adoption raised the floor to the
// term at this controller's own voter), it re-terms as before.
// reterm_on_own_promise and reterm_with_majority turn it red.
func TestCandidateOwnPromiseRefusalReTermsOnlyWithoutAMajority(t *testing.T) {
	c := newC(t, cfgA(), 0, 0)
	r := c.Tick(0, "", 0, true)
	c.OnReply(at(10*ms), granted("A", r, 0))
	c.OnReply(at(10*ms), granted("B", r, 0)) // B promised term 1 to this boot
	r2 := c.Tick(at(500*ms), "", 0, true)
	if r2 == nil || !r2.Renewal || r2.Term != 1 {
		t.Fatalf("renewal: %+v", r2)
	}
	c.OnReply(at(510*ms), granted("A", r2, 0))
	c.OnReply(at(510*ms), granted("W", r2, 42))
	c.OnReply(at(510*ms), refused("B", r2, 1, 0)) // its restart wait
	r3 := c.Tick(at(time.Second), "", 0, true)
	if r3 == nil || r3.Term != 1 {
		t.Fatalf("with A and W granting, the next renewal %+v; want term 1 still (no re-term)", r3)
	}
	// Now the witness is silent: term 1 no longer renews.
	c.OnReply(at(1010*ms), granted("A", r3, 0))
	c.OnReply(at(1010*ms), refused("B", r3, 1, 0))
	r4 := c.Tick(at(1500*ms), "", 0, true)
	if r4 == nil || !r4.Renewal || r4.Term <= 1 {
		t.Fatalf("with no majority at term 1, the next renewal %+v; want a re-term", r4)
	}
}

// A refusal at the leader's term from a voter that never granted this
// boot that term promised it elsewhere (a standby that won its own voter
// in a contested cold start): it re-terms at once, majority or not, or that
// voter is never won back (F13, TestStandbyRejoinsTheLeader).
// own_promise_unchecked turns it red.
func TestCandidateRefusalFromAVoterThatNeverGrantedTheTermReTerms(t *testing.T) {
	c := newC(t, cfgA(), 0, 0)
	r := c.Tick(0, "", 0, true)
	c.OnReply(at(10*ms), granted("A", r, 0))
	c.OnReply(at(10*ms), granted("W", r, 42))
	r2 := c.Tick(at(500*ms), "", 0, true)
	c.OnReply(at(510*ms), granted("A", r2, 0))
	c.OnReply(at(510*ms), granted("W", r2, 42))
	c.OnReply(at(510*ms), refused("B", r2, 1, 0)) // B promised term 1 to itself
	r3 := c.Tick(at(time.Second), "", 0, true)
	if r3 == nil || !r3.Renewal || r3.Term <= 1 {
		t.Fatalf("the next renewal %+v; want a re-term above 1", r3)
	}
}
