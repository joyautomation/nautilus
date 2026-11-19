package quorum

import (
	"testing"
	"time"
)

// pair drives two candidates and three voters by hand: instant delivery,
// instant fsync. Each step, every candidate looks; then each voter takes
// the step's requests, its own candidate's first (so two campaigns in one
// step split the votes: a contested round).
type pair struct {
	t     *testing.T
	v     map[NodeID]*Voter
	c     map[NodeID]*Candidate
	down  map[NodeID]bool // a node that is down sends and answers nothing
	deaf  map[NodeID]bool // a node that can send but hears nothing (F9)
	nonce uint64
}

var ids = []NodeID{"A", "B"}

func newPair(t *testing.T) *pair {
	p := &pair{t: t, v: map[NodeID]*Voter{}, c: map[NodeID]*Candidate{}, down: map[NodeID]bool{}, deaf: map[NodeID]bool{}, nonce: 0x100}
	w, _ := NewWitness("W", lease, VoterState{Incarnation: 42}, 0, func() uint64 { return 42 })
	p.v["W"] = w
	return p
}

// start (re)starts controller id at now from its voter's durable state.
func (p *pair) start(id NodeID, now Instant) {
	var st VoterState
	if v := p.v[id]; v != nil {
		st = v.State() // fsync is instant here: in memory == on disk
	} else {
		st = VoterState{Incarnation: 42}
	}
	p.v[id] = NewVoter(id, lease, st, now)
	cfg := cfgA()
	if id == "B" {
		cfg.Self, cfg.Peer = "B", "A"
	}
	p.nonce++
	cfg.Nonce = p.nonce
	p.c[id] = newC(p.t, cfg, st.Promised, now)
	p.down[id] = false
}

func (p *pair) step(now Instant) {
	var reqs []*Request
	for _, id := range ids {
		c := p.c[id]
		if c == nil || p.down[id] {
			continue
		}
		from, until := p.v[id].LeaderSeen()
		if r := c.Tick(now, from, until, true); r != nil {
			reqs = append(reqs, r)
		}
	}
	for _, vid := range []NodeID{"A", "B", "W"} {
		if p.down[vid] || p.v[vid] == nil {
			continue
		}
		order := append([]*Request(nil), reqs...)
		for i, r := range order { // own candidate first
			if r.From == vid {
				order[0], order[i] = order[i], order[0]
			}
		}
		for _, r := range order {
			if p.deaf[vid] && r.From != vid {
				continue
			}
			ps, reps := p.v[vid].Handle(now, *r)
			if ps != nil {
				reps = append(reps, p.v[vid].Persisted(ps.Seq, true)...)
			}
			for _, rep := range reps {
				if c := p.c[rep.To]; c != nil && !p.down[rep.To] && (!p.deaf[rep.To] || rep.From == rep.To) {
					c.OnReply(now, rep)
				}
			}
		}
	}
}

func (p *pair) leader(now Instant) NodeID {
	for _, id := range ids {
		if c := p.c[id]; c != nil && !p.down[id] && c.Leading(now) {
			return id
		}
	}
	return ""
}

const step = Instant(10 * time.Millisecond)

// The three ways a standby ends up refusing a live leader (found by the
// quorum/sim T2 runs): it started while the leader's campaign ran (its
// voter was in its restart wait), it lost a contested cold start (its
// voter promised itself the leader's term), or it restarted under a live
// leader. In each, once settled, the standby must stop campaigning and its
// voter must grant the leader again, so that with the witness down from
// 10 s (F13) A and B remain a majority and the leader keeps its lease.
func TestStandbyRejoinsTheLeader(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(p *pair, now Instant)
	}{
		{"late standby", func(p *pair, now Instant) {
			if now == at(300*time.Millisecond) {
				p.start("B", now)
			}
		}},
		{"contested cold start", func(p *pair, now Instant) {
			if now == 0 {
				p.start("B", now)
			}
		}},
		{"standby restart under a live leader", func(p *pair, now Instant) {
			switch now {
			case 0:
				p.start("B", now)
			case at(4 * time.Second):
				p.down["B"] = true
			case at(4100 * time.Millisecond):
				p.start("B", now)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newPair(t)
			p.start("A", 0)
			var lead NodeID
			var settledTerm, leadTerm uint64
			for now := Instant(0); now < at(16*time.Second); now += step {
				tc.run(p, now)
				if now == at(10*time.Second) {
					p.down["W"] = true
				}
				p.step(now)
				switch {
				case now == at(3*time.Second):
					lead = p.leader(now)
					if lead == "" {
						t.Fatal("no leader at 3 s")
					}
				case now == at(9*time.Second):
					// Settled: the standby's voter has granted the leader
					// again, and the standby has stopped raising terms.
					sb := map[NodeID]NodeID{"A": "B", "B": "A"}[lead]
					if st := p.v[sb].State(); st.GrantedTo != lead {
						t.Fatalf("at 9 s the standby's voter is promised to %q, not the leader %s", st.GrantedTo, lead)
					}
					settledTerm = p.c[sb].VoteTerm()
					leadTerm = p.c[lead].VoteTerm()
				case now > at(3*time.Second) && p.leader(now) != lead:
					t.Fatalf("leader %s lost its lease at %v (standby's vote term %d)", lead, time.Duration(now),
						p.c[map[NodeID]NodeID{"A": "B", "B": "A"}[lead]].VoteTerm())
				}
			}
			sb := map[NodeID]NodeID{"A": "B", "B": "A"}[lead]
			if got := p.c[sb].VoteTerm(); got != settledTerm {
				t.Fatalf("the standby kept campaigning after settling: vote term %d at 9 s, %d at 16 s", settledTerm, got)
			}
			// Settled, the leader re-terms no more (the witness stays down).
			if got := p.c[lead].VoteTerm(); got != leadTerm {
				t.Fatalf("the leader kept re-terming after settling: vote term %d at 9 s, %d at 16 s", leadTerm, got)
			}
		})
	}
}

// The fencing token (Term) stays the same across a re-term on renewal: a
// write posted under it every 1 ms is still valid 1 ms later whenever the
// lease is continuous, though the vote term moved (#797 checks the posted
// term against Term right before the write). Late standby case, where the
// leader must re-term to win the standby's voter back.
func TestEpochStableAcrossReterm(t *testing.T) {
	p := newPair(t)
	p.start("A", 0)
	var posted uint64
	postedOK, drops := false, 0
	for now := Instant(0); now < at(8*time.Second); now += Instant(time.Millisecond) {
		if now == at(300*time.Millisecond) {
			p.start("B", now)
		}
		if now%step == 0 {
			p.step(now)
		}
		c := p.c["A"]
		if postedOK && c.Leading(now) && c.Term() != posted {
			drops++
		}
		postedOK, posted = c.Leading(now), c.Term()
	}
	c := p.c["A"]
	if c.VoteTerm() == c.Term() {
		t.Fatalf("no re-term happened (vote term %d = epoch): the case proves nothing", c.VoteTerm())
	}
	if drops != 0 {
		t.Fatalf("%d writes refused on a continuous lease: Term moved with the vote term", drops)
	}
}

// A real takeover does change the token: after A is cut off and B leads,
// B's Term differs from A's, and when A leads again its new Term differs
// from the one its old writes were posted under, so #797 refuses them.
func TestEpochChangesOnTakeover(t *testing.T) {
	p := newPair(t)
	p.start("A", 0)
	p.start("B", 0)
	var first uint64
	var bTerm uint64
	for now := Instant(0); now < at(30*time.Second); now += step {
		switch now {
		case at(3 * time.Second):
			if p.leader(now) != "A" {
				t.Fatalf("leader at 3 s: %q", p.leader(now))
			}
			first = p.c["A"].Term()
			p.down["A"] = true // isolated: it neither sends nor hears
		case at(12 * time.Second):
			if p.leader(now) != "B" {
				t.Fatalf("B did not take over: %q", p.leader(now))
			}
			bTerm = p.c["B"].Term()
			p.down["B"] = true
			p.down["A"] = false
		}
		p.step(now)
	}
	if p.leader(at(30*time.Second)-step) != "A" {
		t.Fatal("A did not lead again")
	}
	if bTerm <= first || p.c["A"].Term() <= bTerm {
		t.Fatalf("tokens %d (A), %d (B), %d (A again): each takeover must raise it", first, bTerm, p.c["A"].Term())
	}
}

// A voter notes a renewal from another candidate even when it refuses it
// (here, in its restart wait), but not a campaign, nor its own candidate's.
func TestVoterLeaderSeen(t *testing.T) {
	v := NewVoter("B", lease, VoterState{}, 0)
	if from, until := v.LeaderSeen(); from != "" || until != 0 {
		t.Fatalf("fresh voter saw %q until %v", from, until)
	}
	v.Handle(at(time.Second), Request{From: "A", Term: 3, ID: 1}) // a campaign
	if from, _ := v.LeaderSeen(); from != "" {
		t.Fatal("a campaign counted as a live lease")
	}
	v.Handle(at(time.Second), Request{From: "B", Term: 3, ID: 1, Renewal: true}) // its own
	if from, _ := v.LeaderSeen(); from != "" {
		t.Fatal("its own candidate's renewal counted")
	}
	_, out := v.Handle(at(1500*time.Millisecond), Request{From: "A", Term: 3, ID: 2, Renewal: true})
	if len(out) != 1 || out[0].Granted {
		t.Fatalf("in the restart wait: %+v, want a refusal", out)
	}
	if from, until := v.LeaderSeen(); from != "A" || until != at(3500*time.Millisecond) {
		t.Fatalf("LeaderSeen = %q, %v; want A until 3.5 s (1.5 s + one lease)", from, time.Duration(until))
	}
	// A promise to A that runs later than the renewal seen wins.
	if !grant(t, v, at(3*time.Second), Request{From: "A", Term: 4, ID: 3}) {
		t.Fatal("not granted after the wait")
	}
	if from, until := v.LeaderSeen(); from != "A" || until != at(5*time.Second) {
		t.Fatalf("LeaderSeen = %q, %v; want A until 5 s", from, time.Duration(until))
	}
}

// A leader whose renewal is refused by a voter promised to another renews
// at a higher vote term from then on; Term (the epoch) stays.
func TestCandidateRetermsOnRefusedRenewal(t *testing.T) {
	c := newC(t, cfgA(), 0, 0)
	r := c.Tick(0, "", 0, true)
	if r.Renewal {
		t.Fatal("a campaign went out marked as a renewal: a standby would wait on it")
	}
	c.OnReply(at(10*time.Millisecond), granted("A", r, 0))
	c.OnReply(at(10*time.Millisecond), granted("W", r, 42))
	r2 := c.Tick(at(500*time.Millisecond), "", 0, true)
	if !r2.Renewal || r2.Term != 1 {
		t.Fatalf("renewal: %+v", r2)
	}
	c.OnReply(at(510*time.Millisecond), refused("B", r2, 1, 0)) // B promised term 1 to itself
	r3 := c.Tick(at(1000*time.Millisecond), "", 0, true)
	if r3 == nil || !r3.Renewal || r3.Term != 2 {
		t.Fatalf("after a refused renewal: %+v, want a renewal at term 2", r3)
	}
	c.OnReply(at(1010*time.Millisecond), granted("A", r3, 0))
	c.OnReply(at(1010*time.Millisecond), granted("W", r3, 42))
	if c.VoteTerm() != 2 || c.Term() != 1 || c.LeaseEnd() != at(2980*time.Millisecond) {
		t.Fatalf("vote term %d, epoch %d, lease end %v; want 2, 1, 2.98 s", c.VoteTerm(), c.Term(), time.Duration(c.LeaseEnd()))
	}
	r4 := c.Tick(at(1500*time.Millisecond), "", 0, true)
	if r4.Term != 2 {
		t.Fatalf("renewals went back to term %d", r4.Term)
	}
	// A refusal for the older round (r2, term 1), late or a duplicate
	// refused because we re-termed since, changes nothing: renewals stay
	// at term 2. (Before this rule, stragglers re-termed a leader 103
	// times a minute under duplication in the simulator.)
	c.OnReply(at(1600*time.Millisecond), refused("W", r2, 2, 42))
	r5 := c.Tick(at(2000*time.Millisecond), "", 0, true)
	if r5.Term != 2 {
		t.Fatalf("a stale refusal re-termed the leader to %d", r5.Term)
	}
	c.OnReply(at(2010*time.Millisecond), granted("A", r5, 0))
	c.OnReply(at(2010*time.Millisecond), granted("W", r5, 42)) // lease to 3.98 s
	// A refusal at the current renewal term with a lower promise (a voter
	// in its restart wait that never heard of term 2) doesn't either.
	r6 := c.Tick(at(2500*time.Millisecond), "", 0, true)
	c.OnReply(at(2510*time.Millisecond), refused("B", r6, 1, 0))
	if r7 := c.Tick(at(3000*time.Millisecond), "", 0, true); r7.Term != 2 {
		t.Fatalf("a refusal below our term re-termed the leader to %d", r7.Term)
	}
}

// F9: a leader that can send but not hear fences at its lease end and
// backs off long enough for the standby to win first. Its renewals keep
// the standby waiting until one lease after the last of them; had it come
// back after one lease, its campaign would win the standby's voter (it
// still hears A) and hold the takeover off another lease. With a 2 s
// lease, B must lead within 4.6 s of the cut at every phase of the
// renewal cycle (A's lease ≤ 2 s, B's wait one lease + ρ after A's last
// renewal, a look ≤ 0.5 s, and the vote).
func TestOneWayLeaderLosesTheRace(t *testing.T) {
	for k := range 8 {
		cut := at(4*time.Second) + Instant(k)*Instant(lease/8)
		cut -= cut % step
		p := newPair(t)
		p.start("A", 0)
		p.start("B", at(300*time.Millisecond))
		var took Instant
		for now := Instant(0); now < at(20*time.Second); now += step {
			if now == cut {
				if p.leader(now) != "A" {
					t.Fatalf("phase %d: A not leading at the cut", k)
				}
				p.deaf["A"] = true
			}
			p.step(now)
			if took == 0 && now >= cut && p.leader(now) == "B" {
				took = now
			}
		}
		if took == 0 || took-cut > at(4600*time.Millisecond) {
			t.Errorf("phase %d: B led %v after the cut, want ≤ 4.6 s", k, time.Duration(took-cut))
		}
	}
}
