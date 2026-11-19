package quorum

import (
	"testing"
	"time"
)

const lease = 2 * time.Second

func at(d time.Duration) Instant { return Instant(d) }

// grant drives one request through a voter, confirming any persist, and
// returns whether it was granted.
func grant(t *testing.T, v *Voter, now Instant, r Request) bool {
	t.Helper()
	p, out := v.Handle(now, r)
	if p != nil {
		if len(out) != 0 {
			t.Fatalf("a reply came out with its persist still pending: %+v", out)
		}
		out = v.Persisted(p.Seq, true)
	}
	if len(out) != 1 {
		t.Fatalf("got %d replies, want 1", len(out))
	}
	if out[0].ID != r.ID || out[0].Term != r.Term {
		t.Fatalf("reply %+v does not echo request %+v", out[0], r)
	}
	return out[0].Granted
}

// A voter that has just (re)started grants nothing for one full lease: it
// lost its promise timer (design §3.1).
func TestVoterRestartWait(t *testing.T) {
	v := NewVoter("W", lease, VoterState{Promised: 7, GrantedTo: "A"}, at(10*time.Second))
	if grant(t, v, at(11999*time.Millisecond), Request{From: "B", Term: 8, ID: 1}) {
		t.Fatal("granted inside the restart wait (10 s + 2 s)")
	}
	if !grant(t, v, at(12*time.Second), Request{From: "B", Term: 8, ID: 2}) {
		t.Fatal("refused once the restart wait was over")
	}
}

// A voter restarted with a shorter lease waits out the longer one its
// last promise was made under, recorded in its state; a write before then
// keeps the longer one, and its first grant after records its own (#1021
// review, I1 at the witness).
func TestVoterRestartWaitsTheLongerLease(t *testing.T) {
	long := 2 * lease
	v := NewVoter("W", long, VoterState{}, 0)
	p, _ := v.Handle(at(long), Request{From: "A", Term: 7, ID: 1})
	if p == nil || p.State.Lease != long {
		t.Fatalf("the grant's write records lease %v, want %v", p.State.Lease, long)
	}
	st := p.State
	// Restarted at once with the short lease: A's promise runs to 2·long.
	r := NewVoter("W", lease, st, at(long))
	if got := r.State().Lease; got != long {
		t.Fatalf("restarted state lease %v, want %v kept until a grant", got, long)
	}
	if got := r.MaxRequestLease(at(long)); got != long {
		t.Fatalf("in the wait it accepts requests up to %v, want %v", got, long)
	}
	if grant(t, r, at(long+lease+time.Millisecond), Request{From: "B", Term: 8, ID: 2}) {
		t.Fatal("granted B after the short lease, inside the promise made to A under the long one")
	}
	if !grant(t, r, at(2*long), Request{From: "B", Term: 8, ID: 3}) {
		t.Fatal("refused once the long lease was over")
	}
	if got := r.State().Lease; got != lease {
		t.Fatalf("after its first grant the state records %v, want its own %v", got, lease)
	}
	if got := r.MaxRequestLease(at(2 * long)); got != lease {
		t.Fatalf("past the wait it accepts requests up to %v, want %v", got, lease)
	}
}

// The grant exists only after its state is durable, and a failed write
// sends nothing (F25).
func TestVoterGrantsOnlyAfterPersist(t *testing.T) {
	v := NewVoter("W", lease, VoterState{}, 0)
	p, out := v.Handle(at(lease), Request{From: "A", Term: 1, ID: 1})
	if p == nil || len(out) != 0 {
		t.Fatalf("a state-changing grant must wait for its persist: persist=%v replies=%v", p, out)
	}
	if p.State.Promised != 1 || p.State.GrantedTo != "A" {
		t.Fatalf("persisting %+v, want term 1 to A", p.State)
	}
	if got := v.Persisted(p.Seq, false); len(got) != 0 {
		t.Fatalf("a failed write released %v", got)
	}
	// The in-memory promise stands (it only refuses others); a retry
	// persists again and is granted.
	p, _ = v.Handle(at(lease+time.Millisecond), Request{From: "A", Term: 1, ID: 2})
	if p == nil {
		t.Fatal("the retry did not ask to persist the still-unconfirmed state")
	}
	if got := v.Persisted(p.Seq, true); len(got) != 1 || !got[0].Granted || got[0].ID != 2 {
		t.Fatalf("after a good write: %+v", got)
	}
}

// A renewal that changes nothing durable is answered at once, unless an
// earlier change is still unconfirmed: then it waits for that write too.
func TestVoterRenewal(t *testing.T) {
	v := NewVoter("W", lease, VoterState{}, 0)
	if !grant(t, v, at(lease), Request{From: "A", Term: 1, ID: 1}) {
		t.Fatal("campaign refused")
	}
	p, out := v.Handle(at(lease+500*time.Millisecond), Request{From: "A", Term: 1, ID: 2})
	if p != nil || len(out) != 1 || !out[0].Granted {
		t.Fatalf("a durable renewal: persist=%v replies=%+v, want an immediate grant", p, out)
	}

	v2 := NewVoter("W", lease, VoterState{}, 0)
	p1, _ := v2.Handle(at(lease), Request{From: "A", Term: 1, ID: 1})
	p2, out := v2.Handle(at(lease+100*time.Millisecond), Request{From: "A", Term: 1, ID: 2})
	if len(out) != 0 || p2 == nil {
		t.Fatalf("a renewal answered ahead of the unconfirmed campaign write: %+v", out)
	}
	if got := v2.Persisted(p1.Seq, true); len(got) != 2 {
		t.Fatalf("the write released %d grants, want both", len(got))
	}
}

// The promise runs one lease from when the request was RECEIVED, and
// until then the voter refuses every other candidate, at any term.
func TestVoterPromiseFromReceipt(t *testing.T) {
	v := NewVoter("W", lease, VoterState{}, 0)
	if !grant(t, v, at(3*time.Second), Request{From: "A", Term: 1, ID: 1}) {
		t.Fatal("campaign refused")
	}
	if grant(t, v, at(4999*time.Millisecond), Request{From: "B", Term: 2, ID: 1}) {
		t.Fatal("granted to B while the promise to A (3 s + 2 s) still ran")
	}
	if !grant(t, v, at(5*time.Second), Request{From: "B", Term: 2, ID: 2}) {
		t.Fatal("refused B after the promise to A ran out at 5 s")
	}
}

// Terms only rise at a voter (I4): an older term is refused, and the same
// term is never granted to a second candidate, even after the promise ran
// out.
func TestVoterTermsOnlyRise(t *testing.T) {
	v := NewVoter("W", lease, VoterState{}, 0)
	grant(t, v, at(lease), Request{From: "A", Term: 5, ID: 1})
	later := at(10 * time.Second)
	if grant(t, v, later, Request{From: "B", Term: 4, ID: 1}) {
		t.Fatal("granted an older term")
	}
	if grant(t, v, later, Request{From: "B", Term: 5, ID: 2}) {
		t.Fatal("granted term 5 to a second candidate")
	}
	p, out := v.Handle(later, Request{From: "B", Term: 4, ID: 3})
	if p != nil || len(out) != 1 || out[0].Promised != 5 {
		t.Fatalf("a refusal must name the promised term: %+v", out)
	}
}

// A witness with no state draws an incarnation and persists it before
// it can grant; one with state keeps its incarnation. Its replies carry
// it, a controller's carry 0.
func TestWitnessIncarnation(t *testing.T) {
	w, p := NewWitness("W", lease, VoterState{}, 0, func() uint64 { return 42 })
	if p == nil || p.State.Incarnation != 42 {
		t.Fatalf("a new witness must persist its incarnation: %+v", p)
	}
	if got := w.Persisted(p.Seq, true); len(got) != 0 {
		t.Fatalf("nothing was waiting: %v", got)
	}
	_, out := w.Handle(0, Request{From: "A", Term: 1, ID: 1})
	if len(out) != 1 || out[0].Incarnation != 42 {
		t.Fatalf("witness reply %+v, want one refusal (restart wait) stamped incarnation 42", out)
	}
	w2, p2 := NewWitness("W", lease, VoterState{Incarnation: 9, Promised: 3}, 0, func() uint64 { return 42 })
	if p2 != nil || w2.State().Incarnation != 9 || w2.State().Promised != 3 {
		t.Fatalf("a witness with state must keep it: %+v %+v", p2, w2.State())
	}
	c := NewVoter("B", lease, VoterState{Incarnation: 9}, 0)
	_, out = c.Handle(0, Request{From: "A", Term: 1, ID: 1})
	if len(out) != 1 || out[0].Incarnation != 0 {
		t.Fatalf("controller reply %+v, want one refusal stamped incarnation 0", out)
	}
}

// A reply names the requester and echoes the request's nonce, so it can
// only ever count for the request it answers.
func TestVoterReplyAddressesTheRequester(t *testing.T) {
	v := NewVoter("W", lease, VoterState{}, 0)
	_, out := v.Handle(0, Request{From: "B", Term: 1, Nonce: 77, ID: 3})
	if len(out) != 1 || out[0].To != "B" || out[0].Nonce != 77 || out[0].ID != 3 {
		t.Fatalf("replies %+v: want one addressing B's request (nonce 77, id 3)", out)
	}
}
