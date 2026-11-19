package quorum

import "testing"

func rv2(t *testing.T, v *Voter, now Instant, r Request) Reply {
	t.Helper()
	p, out := v.Handle(now, r)
	if p != nil {
		out = append(out, v.Persisted(p.Seq, true)...)
	}
	return out[0]
}

// A failed adoption write leaves a.commit set; a later proposal's write
// then makes it durable and counts the failed adoption's incarnation while
// the new proposal is pending (forward only broken). With B's acks lost,
// A counts J and B counts I: both lead.
func TestReviewFailedWriteCountsWhilePending(t *testing.T) {
	a := newCtl(t, "A", commissioned(1, incI, 5), 0)
	b := newCtl(t, "B", commissioned(1, incI, 5), 0)
	wI, _ := NewWitness("W", lease, VoterState{Incarnation: incI, Promised: 5}, 0, nil)
	wJ, _ := NewWitness("W", lease, VoterState{Incarnation: incJ}, 0, nil)
	t1 := at(2 * lease)
	b.a.OnPeer(t1)
	a.hear(t1, incJ, 0)
	q, _ := a.tick(t1) // Propose(2, J)
	ack := b.recv(t1, q[0])
	a.hold = true
	a.recv(t1, ack[0]) // A adopts (2, J); its write ...
	a.flush(t1, false) // ... fails
	a.hold = false
	// J falls silent for A, I answers again: A proposes (3, I).
	t2 := t1.Add(2*lease + lease/4)
	a.hear(t2, incI, 5)
	q, _ = a.tick(t2)
	st := a.v.State()
	inc, from := a.c.Counting()
	t.Logf("A state %+v; counting %d from %v", st, inc, from)
	if st.PendingGen != 0 && inc != 0 {
		t.Errorf("A counts %d while its proposal (%d, %d) is pending", inc, st.PendingGen, st.PendingInc)
	}
	// B takes (3, I); its Ack is lost.
	b.recv(t2, q[0])
	t3 := t2.Add(lease + lease/2)
	ra := a.c.Tick(t3, "", 0, true)
	rb := b.c.Tick(t3, "", 0, true)
	a.c.OnReply(t3, rv2(t, a.v, t3, *ra))
	a.c.OnReply(t3, rv2(t, wJ, t3, *ra))
	b.c.OnReply(t3, rv2(t, b.v, t3, *rb))
	b.c.OnReply(t3, rv2(t, wI, t3, *rb))
	if a.c.Leading(t3) && b.c.Leading(t3) {
		t.Fatalf("SPLIT BRAIN: A term %d on J, B term %d on I", a.c.Term(), b.c.Term())
	}
}

// The same with a SLOW write: A's adoption write of (2, J) has not landed
// when A proposes (3, I); both then land. A must count nothing while
// (3, I) is pending.
func TestReviewSlowWriteCountsWhilePending(t *testing.T) {
	a := newCtl(t, "A", commissioned(1, incI, 5), 0)
	b := newCtl(t, "B", commissioned(1, incI, 5), 0)
	t1 := at(2 * lease)
	b.a.OnPeer(t1)
	a.hear(t1, incJ, 0)
	q, _ := a.tick(t1)
	ack := b.recv(t1, q[0])
	a.hold = true
	a.recv(t1, ack[0]) // (2, J) adopted; its write is slow
	t2 := t1.Add(2*lease + lease/4)
	a.hear(t2, incI, 5)
	a.tick(t2) // proposes (3, I); its write is held too
	a.flush(t2, true)
	if st := a.v.State(); st.PendingGen == 0 {
		t.Fatalf("no proposal pending: %+v", st)
	}
	if inc, _ := a.c.Counting(); inc != 0 {
		t.Fatalf("A counts %d while its proposal is pending", inc)
	}
}

// Belt and braces, each pinned on its own (either alone closes the two
// cases above, so neither mutation alone can turn them red):

// A proposal drops an adoption still waiting for its write.
func TestProposeDropsAnAdoptionInFlight(t *testing.T) {
	a := newCtl(t, "A", commissioned(1, incI, 0), 0)
	now := at(2 * lease)
	a.hold = true
	a.recv(now, Adopt{From: "B", Kind: Propose, Gen: 2, Incarnation: incJ, Nonce: 0xB1, Echo: 0xA1})
	if a.a.commit == nil {
		t.Fatal("setup: no adoption waiting for its write")
	}
	a.a.propose(3, incI)
	if a.a.commit != nil {
		t.Fatalf("the proposal left the adoption of %d to count on a later write", a.a.commit.inc)
	}
}

// A write that lands while a proposal is pending counts nothing, whatever
// adoption waited for it.
func TestNoCountWhilePending(t *testing.T) {
	a := newCtl(t, "A", commissioned(1, incI, 0), 0)
	now := at(2 * lease)
	a.c.Count(0, 0)
	a.v.st.PendingGen, a.v.st.PendingInc = 3, incI
	a.a.commit = &commit{need: 1, gen: 2, inc: incJ}
	a.a.Persisted(now, 1, true)
	if inc, _ := a.c.Counting(); inc != 0 {
		t.Fatalf("counts %d while (3, I) is pending", inc)
	}
}

// The hold-off runs from an adoption even when its write fails: A adopted
// J at t1 (its second incarnation this boot, so the hold-off is 2 leases),
// the write failed, and one lease later I answers: no proposal yet.
func TestHoldOffFromAFailedAdoption(t *testing.T) {
	a := newCtl(t, "A", commissioned(1, incI, 0), 0)
	t1 := at(4 * lease)
	a.hold = true
	a.recv(t1, Adopt{From: "B", Kind: Propose, Gen: 2, Incarnation: incJ, Nonce: 0xB1, Echo: 0xA1})
	a.flush(t1, false)
	a.hold = false
	t2 := t1.Add(lease)
	a.hear(t2, incI, 0)
	if q, _ := a.tick(t2); len(q) != 0 {
		t.Fatalf("proposed %+v one lease after a failed adoption, inside the 2-lease hold-off", q)
	}
}
