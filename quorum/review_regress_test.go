package quorum

import "testing"

// deliver a request to a voter, confirming its write; returns the reply.
func rv(t *testing.T, v *Voter, now Instant, r Request) Reply {
	t.Helper()
	p, out := v.Handle(now, r)
	if p != nil {
		out = append(out, v.Persisted(p.Seq, true)...)
	}
	if len(out) != 1 {
		t.Fatalf("replies %d", len(out))
	}
	return out[0]
}

// #1008 review, F1: a restart right after persisting an adoption counts J at once. The
// peer A leads on {B-voter, W_I} while A's own voter is in its restart
// wait; once A's voter is out of its wait (before A's lease ends), B wins
// {A-voter, W_J}.
func TestReviewRestartSkipsQuiet(t *testing.T) {
	T0 := at(10 * lease)
	b := newCtl(t, "B", commissioned(1, incI, 5), 0)
	wI, _ := NewWitness("W", lease, VoterState{Incarnation: incI, Promised: 5}, 0, nil)
	wJ, _ := NewWitness("W", lease, VoterState{Incarnation: incJ}, 0, nil)
	// A restarts at T0: its voter waits until T0+L.
	a := newCtl(t, "A", commissioned(1, incI, 5), T0)
	t1 := T0.Add(lease / 10)
	rq := a.c.Tick(t1, "", 0, true)
	a.c.OnReply(t1, rv(t, a.v, t1, *rq)) // refused: restart wait
	a.c.OnReply(t1, rv(t, b.v, t1, *rq)) // B's voter grants
	a.c.OnReply(t1, rv(t, wI, t1, *rq))  // W_I grants
	if !a.c.Leading(t1) {
		// The fix: restarted at T0, A counts W_I only from T0+L, when its
		// own voter's wait ends too, so no lease of A can rest on B's
		// voter and W_I alone. The sequence below runs (and splits) only
		// with the restart_counts_at_once mutation.
		if inc, from := a.c.Counting(); inc != incI || from != T0.Add(lease) {
			t.Fatalf("restarted A counts (%d from %v), want %d from %v", inc, from, incI, T0.Add(lease))
		}
		return
	}
	// B is cut from W_I, hears W_J, proposes; A adopts; B adopts.
	t2 := t1.Add(10 * ms)
	b.hear(t2, incJ, 0)
	q, _ := b.tick(t2)
	talk(t2, b, a, q)
	if _, i := adopted(b.v.State()); i != incJ {
		t.Fatalf("B not adopted: %+v", b.v.State())
	}
	// B crashes right after the persist and restarts (A's renewals to B
	// lost, or A unhealthy and not renewing).
	t3 := t2.Add(10 * ms)
	b2 := newCtl(t, "B", b.v.State(), t3)
	t4 := T0.Add(lease + ms)
	from, until := b2.v.LeaderSeen()
	rq2 := b2.c.Tick(t4, from, until, true)
	if rq2 == nil {
		t.Fatal("B did not campaign")
	}
	b2.c.OnReply(t4, rv(t, b2.v, t4, *rq2))
	b2.c.OnReply(t4, rv(t, a.v, t4, *rq2))
	b2.c.OnReply(t4, rv(t, wJ, t4, *rq2))
	if b2.c.Leading(t4) && a.c.Leading(t4) {
		t.Fatalf("SPLIT BRAIN at %v: A until %v (term %d), B term %d", t4, a.c.LeaseEnd(), a.c.Term(), b2.c.Term())
	}
}

// #1008 review, F2: a lost controller takes a late duplicate Propose (sent before its
// file was lost) and adopts with that message's stale floor. A term led
// since on {A, B} without the witness is missed, and A leads it again.
func TestReviewStaleProposeFloor(t *testing.T) {
	// B led term 11 on {A-voter, B-voter} (W cut); B's voter remembers it.
	b := newCtl(t, "B", VoterState{Gen: 1, Incarnation: incI, Promised: 11, GrantedTo: "B", Floor: 10}, 0)
	w, _ := NewWitness("W", lease, VoterState{Incarnation: incI, Promised: 9}, 0, nil)
	_ = b
	// A lost its file and restarts.
	a := newCtl(t, "A", VoterState{}, 0)
	now := at(lease)
	_, probe := a.tick(now)
	a.a.OnWitness(now, rv(t, w, now, *probe))
	// The stale duplicate of B's commissioning proposal arrives (made by
	// the operator: since #710 B2 nothing else commissions a pair, so it
	// carries Operator, and only its echo of A's earlier boot refuses it).
	stale := Adopt{From: "B", Kind: Propose, Gen: 1, Incarnation: incI, Floor: 10, Nonce: 0xB1, Echo: 0xA0, Operator: 10} // to A's earlier boot
	a.recv(now, stale)
	t.Logf("A state after stale propose: %+v", a.v.State())
	t5 := now.Add(lease + lease/2)
	rq := a.c.Tick(t5, "", 0, true)
	if rq == nil {
		t.Fatal("no campaign")
	}
	a.c.OnReply(t5, rv(t, a.v, t5, *rq))
	a.c.OnReply(t5, rv(t, w, t5, *rq))
	if a.c.Leading(t5) && a.c.Term() <= 11 {
		t.Fatalf("A leads at term %d; term 11 was already led by B", a.c.Term())
	}
}

// #1016 review (naut-lead), verbatim: both files lost, the real witness intact. Before the loss B led term 11
// on {A, B} while W was cut (W promised 9). The pair re-commissions on
// its own at a floor that misses 11.
func TestReviewBothLostWitnessIntact(t *testing.T) {
	a := newCtl(t, "A", VoterState{}, 0)
	b := newCtl(t, "B", VoterState{}, 0)
	now := at(lease + lease/4)
	a.hear(now, incI, 9)
	b.hear(now, incI, 9)
	qa, _ := a.tick(now)
	talk(now, a, b, qa)
	qb, _ := b.tick(now)
	talk(now, b, a, qb)
	for range 4 {
		qa, _ = a.tick(now)
		talk(now, a, b, qa)
	}
	t.Logf("A %+v", a.v.State())
	t.Logf("B %+v", b.v.State())
	if a.v.State().Incarnation != 0 && a.v.State().Floor < 11 {
		t.Errorf("re-commissioned with floor %d: term 10/11 can be led again", a.v.State().Floor)
	}
}
