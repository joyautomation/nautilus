package quorum

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

// ctl is one controller for the handshake tests: its voter, candidate and
// adoption, with a disk that confirms each write at once unless slow.
type ctl struct {
	t    *testing.T
	self NodeID
	v    *Voter
	c    *Candidate
	a    *Adoption
	slow []*Persist // writes not yet confirmed (when slow)
	hold bool       // keep writes unconfirmed until flush
	// peerDown: the peer is not heard (tick doesn't call OnPeer).
	peerDown bool
}

const incI, incJ, incK = 42, 77, 99

func newCtl(t *testing.T, self NodeID, st VoterState, now Instant) *ctl {
	t.Helper()
	peer, nonce := NodeID("B"), uint64(0xA1)
	if self == "B" {
		peer, nonce = "A", 0xB1
	}
	cfg := Config{Self: self, Peer: peer, Witness: "W", Lease: lease, Rho: 0.01, Incarnation: st.Incarnation, Nonce: nonce}
	v := NewVoter(self, lease, st, now)
	c := newC(t, cfg, st.Promised, now)
	return &ctl{t: t, self: self, v: v, c: c, a: NewAdoption(v, c, now)}
}

// write takes a Persist from the adoption: confirmed at once, or kept.
func (x *ctl) write(now Instant, p *Persist) []Adopt {
	if p == nil {
		return nil
	}
	if x.hold {
		x.slow = append(x.slow, p)
		return nil
	}
	_, out := x.a.Persisted(now, p.Seq, true)
	return out
}

// flush confirms (or fails) the kept writes.
func (x *ctl) flush(now Instant, ok bool) []Adopt {
	var out []Adopt
	for _, p := range x.slow {
		_, o := x.a.Persisted(now, p.Seq, ok)
		out = append(out, o...)
	}
	x.slow = nil
	return out
}

func (x *ctl) recv(now Instant, m Adopt) []Adopt {
	p, out := x.a.OnAdopt(now, m)
	return append(out, x.write(now, p)...)
}

func (x *ctl) tick(now Instant) ([]Adopt, *Request) {
	if !x.peerDown {
		x.a.OnPeer(now)
	}
	p, out, probe := x.a.Tick(now)
	return append(out, x.write(now, p)...), probe
}

// hear: the witness at inc answered this controller, promised up to term.
func (x *ctl) hear(now Instant, inc, promised uint64) {
	x.a.OnWitness(now, Reply{From: "W", To: x.self, Incarnation: inc, Promised: promised})
}

// talk delivers msgs from x to y and the answers back, until none is left.
func talk(now Instant, x, y *ctl, msgs []Adopt) {
	for range 20 {
		if len(msgs) == 0 {
			return
		}
		back := []Adopt{}
		for _, m := range msgs {
			back = append(back, y.recv(now, m)...)
		}
		msgs = back
		x, y = y, x
	}
	panic("handshake did not settle")
}

func adopted(st VoterState) (uint64, uint64) { return st.Gen, st.Incarnation }

func commissioned(gen, inc, promised uint64) VoterState {
	return VoterState{Gen: gen, Incarnation: inc, Promised: promised}
}

func TestAdoptRoundTripAndCorruption(t *testing.T) {
	m := Adopt{From: "A", Kind: Ack, Gen: 3, Incarnation: 77, Floor: 12, Nonce: 0xA1, Echo: 0xB1, Pending: 4, CommitGen: 3, CommitInc: 77, Operator: 1000}
	b, _ := m.MarshalBinary()
	var got Adopt
	if err := got.UnmarshalBinary(b); err != nil || got != m {
		t.Fatalf("adopt: %+v %v", got, err)
	}
	var r Reply
	if err := r.UnmarshalBinary(b); !errors.Is(err, ErrKind) {
		t.Fatalf("an adopt decoded as a reply: %v", err)
	}
	for n := 0; n < len(b); n++ {
		if err := got.UnmarshalBinary(b[:n]); err == nil {
			t.Fatalf("an adopt truncated to %d bytes decoded", n)
		}
	}
	for i := range b {
		for bit := 0; bit < 8; bit++ {
			c := append([]byte(nil), b...)
			c[i] ^= 1 << bit
			if err := got.UnmarshalBinary(c); err == nil {
				t.Fatalf("bit %d of byte %d flipped and the adopt decoded", bit, i)
			}
		}
	}
	bad, _ := Adopt{From: "A", Kind: 9}.MarshalBinary()
	if err := got.UnmarshalBinary(bad); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("kind 9: %v", err)
	}
	s := VoterState{Promised: 5, GrantedTo: "A", Incarnation: 77, Gen: 3, PendingGen: 4, PendingInc: 99, Floor: 12}
	sb, _ := s.MarshalBinary()
	var gs VoterState
	if err := gs.UnmarshalBinary(sb); err != nil || !reflect.DeepEqual(gs, s) {
		t.Fatalf("state: %+v %v", gs, err)
	}
}

// A voter grants no term at or below its floor, and reports the floor as
// promised so the candidate goes above it. A term-0 request (a probe) is
// always refused.
func TestVoterFloor(t *testing.T) {
	v := NewVoter("W", lease, VoterState{Floor: 7}, 0)
	if grant(t, v, at(lease), Request{From: "A", Term: 7, Nonce: 1, ID: 1}) {
		t.Fatal("granted a term at the floor")
	}
	_, out := v.Handle(at(lease), Request{From: "A", Term: 7, Nonce: 1, ID: 2})
	if out[0].Promised != 7 {
		t.Fatalf("a refusal reports promised %d, want the floor 7", out[0].Promised)
	}
	if !grant(t, v, at(lease), Request{From: "A", Term: 8, Nonce: 1, ID: 3}) {
		t.Fatal("refused a term above the floor")
	}
	w := NewVoter("W", lease, VoterState{}, 0)
	if grant(t, w, at(lease), Request{From: "A", Term: 0, Nonce: 1, ID: 0}) {
		t.Fatal("granted a probe")
	}
}

// #976: a controller with no incarnation adopted (a lost file) grants
// nothing, whatever the term, until it has re-commissioned.
func TestUncommissionedControllerGrantsNothing(t *testing.T) {
	a := newCtl(t, "A", VoterState{}, 0)
	if grant(t, a.v, at(2*lease), Request{From: "B", Term: 3, Nonce: 1, ID: 1}) {
		t.Fatal("an uncommissioned controller granted")
	}
	if inc, _ := a.c.Counting(); inc != 0 {
		t.Fatalf("an uncommissioned controller counts incarnation %d", inc)
	}
	b := newCtl(t, "A", commissioned(1, incI, 0), 0)
	if !grant(t, b.v, at(2*lease), Request{From: "B", Term: 3, Nonce: 1, ID: 1}) {
		t.Fatal("a commissioned controller refused")
	}
}

// #976, the re-commissioning: a controller whose file was lost probes the
// witness, asks its peer what it counts, and re-adopts that at a new
// generation, with a floor that includes the witness's promised term (12
// here, above anything the peer knew). It never proposes before it has
// heard the witness answer at the peer's incarnation.
func TestLostControllerReadoptsThePeer(t *testing.T) {
	now := at(lease)
	a := newCtl(t, "A", VoterState{}, 0)
	b := newCtl(t, "B", commissioned(3, incI, 5), 0)
	q, probe := a.tick(now)
	if probe == nil || probe.Term != 0 || probe.From != "A" {
		t.Fatalf("no probe for the witness: %+v", probe)
	}
	if len(q) != 1 || q[0].Kind != Propose || q[0].Gen != 0 {
		t.Fatalf("want a query, got %+v", q)
	}
	ans := b.recv(now, q[0])
	if len(ans) != 1 || ans[0].Kind != State || ans[0].Incarnation != incI || ans[0].Gen != 3 {
		t.Fatalf("want B's state (3, %d), got %+v", incI, ans)
	}
	if out := a.recv(now, ans[0]); len(out) != 0 {
		t.Fatalf("proposed before hearing the witness: %+v", out)
	}
	a.hear(now, incI, 12)
	q, _ = a.tick(now)
	talk(now, a, b, q)
	ans = b.recv(now, Adopt{From: "A", Kind: Propose})
	talk(now, b, a, ans)
	if g, i := adopted(a.v.State()); g != 4 || i != incI {
		t.Fatalf("A adopted (%d, %d), want (4, %d)", g, i, incI)
	}
	if g, i := adopted(b.v.State()); g != 4 || i != incI {
		t.Fatalf("B adopted (%d, %d), want (4, %d)", g, i, incI)
	}
	if f := a.v.State().Floor; f < 12 {
		t.Fatalf("A's floor %d misses the witness's promised term 12", f)
	}
	if grant(t, a.v, at(2*lease), Request{From: "B", Term: 12, Nonce: 1, ID: 1}) {
		t.Fatal("A granted a term the witness had promised")
	}
	if !grant(t, a.v, at(2*lease), Request{From: "B", Term: 13, Nonce: 1, ID: 2}) {
		t.Fatal("A refused a term above its floor")
	}
	// The peer's incarnation didn't change: B counts it on, with no quiet
	// period; A counts it after one.
	if inc, from := b.c.Counting(); inc != incI || from > now {
		t.Fatalf("B counts (%d from %v), want %d at once", inc, from, incI)
	}
	if inc, from := a.c.Counting(); inc != incI || from != now.Add(lease) {
		t.Fatalf("A counts (%d from %v), want %d from %v", inc, from, incI, now.Add(lease))
	}
}

// A lost controller that hears only a stray copy (another incarnation)
// never proposes it, and never moves its commissioned peer off the real
// witness.
func TestLostControllerIgnoresAStrayCopy(t *testing.T) {
	now := at(lease)
	a := newCtl(t, "A", VoterState{}, 0)
	b := newCtl(t, "B", commissioned(3, incI, 5), 0)
	a.hear(now, incK, 0)
	for k := range 8 {
		t0 := now.Add(lease / 4 * time.Duration(k))
		q, _ := a.tick(t0)
		talk(t0, a, b, q)
	}
	if g, i := adopted(b.v.State()); g != 3 || i != incI {
		t.Fatalf("B moved to (%d, %d)", g, i)
	}
	if _, i := adopted(a.v.State()); i != 0 {
		t.Fatalf("A adopted the stray %d", i)
	}
}

// A rebuilt witness: both controllers adopt its new incarnation together.
// Forward only: the proposer counts no witness from its proposal until the
// Ack. Quiet period: each counts the new incarnation one lease after its
// own adoption is durable. Term floor: both rise to the higher floor.
func TestRebuiltWitnessAdopted(t *testing.T) {
	a := newCtl(t, "A", commissioned(1, incI, 9), 0)
	b := newCtl(t, "B", commissioned(1, incI, 4), 0)
	a.hear(at(100*ms), incI, 9)
	// The witness is rebuilt: its new incarnation answers, the old one is
	// silent for a lease.
	now := at(100*ms + lease)
	a.hear(now, incJ, 0)
	a.hold = true
	q, _ := a.tick(now)
	if len(q) != 0 {
		t.Fatalf("a proposal left before its write: %+v", q)
	}
	if inc, _ := a.c.Counting(); inc != 0 {
		t.Fatalf("the proposer still counts %d while its proposal is in flight", inc)
	}
	a.hold = false
	q = a.flush(now, true)
	if len(q) != 1 || q[0].Kind != Propose || q[0].Gen != 2 || q[0].Incarnation != incJ {
		t.Fatalf("want Propose(2, %d), got %+v", incJ, q)
	}
	// Restarted now, with the proposal durable, A still counts nothing.
	r := newCtl(t, "A", a.v.State(), now)
	if inc, _ := r.c.Counting(); inc != 0 {
		t.Fatalf("restarted mid-proposal, A counts %d", inc)
	}
	ack := b.recv(now, q[0])
	if inc, from := b.c.Counting(); inc != incJ || from != now.Add(lease) {
		t.Fatalf("B counts (%d from %v), want %d after the quiet period", inc, from, incJ)
	}
	later := now.Add(10 * ms)
	talk(later, b, a, ack)
	if inc, from := a.c.Counting(); inc != incJ || from != later.Add(lease) {
		t.Fatalf("A counts (%d from %v), want %d from %v", inc, from, incJ, later.Add(lease))
	}
	if b.v.State().Floor < 9 || a.v.State().Floor < 9 {
		t.Fatalf("floors A %d B %d, want both ≥ 9 (A's promised)", a.v.State().Floor, b.v.State().Floor)
	}
	// B's next campaign goes above A's terms although B never saw them.
	rq := b.c.Tick(later.Add(lease), "", 0, true)
	if rq == nil || rq.Term <= 9 {
		t.Fatalf("B campaigns %+v, want a term above 9", rq)
	}
	evs := a.a.Events()
	if len(evs) == 0 || evs[len(evs)-1].Kind != EventAdopted || evs[len(evs)-1].Incarnation != incJ {
		t.Fatalf("no adoption event: %+v", evs)
	}
}

// The quiet period decides a lease: inside it a witness grant at the new
// incarnation doesn't make a majority; after it, it does.
func TestQuietPeriodHoldsOffTheNewWitness(t *testing.T) {
	now := at(lease)
	// try adopts J on a fresh pair at now, then A campaigns at t0: its own
	// voter and the new witness grant.
	try := func(t0 Instant) bool {
		a := newCtl(t, "A", commissioned(1, incI, 0), 0)
		b := newCtl(t, "B", commissioned(1, incI, 0), 0)
		a.hear(now, incJ, 0)
		q, _ := a.tick(now)
		talk(now, a, b, q)
		if _, i := adopted(a.v.State()); i != incJ {
			t.Fatalf("not adopted: %+v", a.v.State())
		}
		rq := a.c.Tick(t0, "", 0, true)
		if rq == nil {
			t.Fatalf("no campaign at %v", t0)
		}
		a.c.OnReply(t0, granted("A", rq, 0))
		a.c.OnReply(t0, granted("W", rq, incJ))
		return a.c.Leading(t0)
	}
	if try(now.Add(lease / 2)) {
		t.Fatal("led on a witness grant inside the quiet period")
	}
	if !try(now.Add(lease + lease/2)) {
		t.Fatal("no lead after the quiet period")
	}
}

// Generations only rise: a late, duplicated or earlier-boot proposal
// can't move a peer that has moved on.
func TestStaleProposalRefused(t *testing.T) {
	b := newCtl(t, "B", commissioned(2, incK, 0), 0)
	for _, m := range []Adopt{
		{From: "A", Kind: Propose, Gen: 2, Incarnation: incJ},
		{From: "A", Kind: Propose, Gen: 1, Incarnation: incJ},
		{From: "A", Kind: Ack, Gen: 3, Incarnation: incJ},
	} {
		out := b.recv(at(lease), m)
		if g, i := adopted(b.v.State()); g != 2 || i != incK {
			t.Fatalf("%+v moved B to (%d, %d)", m, g, i)
		}
		if m.Kind == Propose && (len(out) != 1 || out[0].Kind != State) {
			t.Fatalf("%+v: want B's State, got %+v", m, out)
		}
	}
	// A re-sent proposal for what B holds is acked again.
	out := b.recv(at(lease), Adopt{From: "A", Kind: Propose, Gen: 2, Incarnation: incK})
	if len(out) != 1 || out[0].Kind != Ack {
		t.Fatalf("a re-sent proposal: %+v", out)
	}
}

// Concurrent proposals at one generation: the higher id wins on both.
func TestConcurrentProposals(t *testing.T) {
	a := newCtl(t, "A", commissioned(1, incI, 0), 0)
	b := newCtl(t, "B", commissioned(1, incI, 0), 0)
	now := at(lease)
	a.hear(now, incJ, 0)
	b.hear(now, incK, 0)
	qa, _ := a.tick(now)
	qb, _ := b.tick(now)
	toB, toA := qa, qb
	for range 10 {
		var nb, na []Adopt
		for _, m := range toB {
			na = append(na, b.recv(now, m)...)
		}
		for _, m := range toA {
			nb = append(nb, a.recv(now, m)...)
		}
		toB, toA = nb, na
	}
	if g, i := adopted(a.v.State()); g != 2 || i != incK {
		t.Fatalf("A at (%d, %d), want (2, %d)", g, i, incK)
	}
	if g, i := adopted(b.v.State()); g != 2 || i != incK {
		t.Fatalf("B at (%d, %d), want (2, %d)", g, i, incK)
	}
}

// A late proposal from before the peer moved on can't split the pair:
// after (2, K) is agreed, A's old Propose(2, J) arriving late leaves B on K.
// With generations ignored, B would count J while A counts K.
func TestLateProposalCannotSplitThePair(t *testing.T) {
	a := newCtl(t, "A", commissioned(1, incI, 0), 0)
	b := newCtl(t, "B", commissioned(1, incI, 0), 0)
	now := at(lease)
	a.hear(now, incJ, 0)
	b.hear(now, incK, 0)
	late, _ := a.tick(now) // Propose(2, J), delayed
	qb, _ := b.tick(now)   // Propose(2, K)
	talk(now, b, a, qb)
	if _, i := adopted(a.v.State()); i != incK {
		t.Fatalf("A at %d, want %d", i, incK)
	}
	b.recv(now.Add(lease), late[0])
	if _, i := adopted(b.v.State()); i != incK {
		t.Fatalf("a late proposal moved B to %d while A counts %d", i, incK)
	}
}

// An Ack leaves only once the adoption it confirms is durable; a failed
// write drops it, and the re-sent proposal gets it again.
func TestAckWaitsForItsWrite(t *testing.T) {
	b := newCtl(t, "B", commissioned(1, incI, 0), 0)
	b.hold = true
	now := at(lease)
	m := Adopt{From: "A", Kind: Propose, Gen: 2, Incarnation: incJ}
	if out := b.recv(now, m); len(out) != 0 {
		t.Fatalf("acked before the write: %+v", out)
	}
	if out := b.flush(now, false); len(out) != 0 {
		t.Fatalf("acked after a failed write: %+v", out)
	}
	if inc, _ := b.c.Counting(); inc != 0 {
		t.Fatalf("counts %d after a failed write", inc)
	}
	b.hold = false
	out := b.recv(now, m)
	if len(out) != 1 || out[0].Kind != Ack {
		t.Fatalf("re-sent proposal after a failed write: %+v", out)
	}
	if inc, _ := b.c.Counting(); inc != incJ {
		t.Fatalf("counts %d after the retried write", inc)
	}
}

// The switch hold-off grows: after one switch, a second needs two leases
// of silence from the counted incarnation; a stray answering on and off
// can't switch the pair every lease.
func TestSwitchHoldOffGrows(t *testing.T) {
	a := newCtl(t, "A", commissioned(1, incI, 0), 0)
	b := newCtl(t, "B", commissioned(1, incI, 0), 0)
	now := at(lease)
	a.hear(now, incJ, 0)
	q, _ := a.tick(now)
	talk(now, a, b, q)
	if _, i := adopted(a.v.State()); i != incJ {
		t.Fatal("first switch not made")
	}
	// Now J falls silent and I answers again, every look.
	sw := now
	for k := int64(1); k <= 12; k++ {
		t0 := sw.Add(lease / 4 * time.Duration(k))
		a.hear(t0, incI, 0)
		q, _ := a.tick(t0)
		if len(q) > 0 {
			if gap := t0 - sw; gap < Instant(2*lease) {
				t.Fatalf("switched back after %v of silence, want ≥ 2 leases", gap)
			}
			return
		}
	}
	t.Fatal("never switched back")
}

// Two incarnations answering within a lease is reported.
func TestTwoIncarnationsReported(t *testing.T) {
	a := newCtl(t, "A", commissioned(1, incI, 0), 0)
	a.hear(at(lease), incI, 0)
	a.hear(at(lease+lease/2), incK, 0)
	evs := a.a.Events()
	if len(evs) != 1 || evs[0].Kind != EventTwoIncarnations {
		t.Fatalf("events %+v, want one EventTwoIncarnations", evs)
	}
	a.hear(at(lease+lease/2+ms), incI, 0) // within the same lease: not again
	if evs := a.a.Events(); len(evs) != 0 {
		t.Fatalf("reported again within a lease: %+v", evs)
	}
}

// A lost controller doesn't take a proposal either for an incarnation it
// hasn't heard answer this boot: its floor would miss that witness's
// promised term. It answers with its State (uncommissioned) instead. (B
// re-proposes the incarnation it committed, the only kind a lost
// controller takes.)
func TestLostControllerAdoptsOnlyAHeardIncarnation(t *testing.T) {
	a := newCtl(t, "A", VoterState{}, 0)
	m := Adopt{From: "B", Kind: Propose, Gen: 4, Incarnation: incI, Floor: 3, Echo: 0xA1, CommitGen: 3, CommitInc: incI}
	out := a.recv(at(lease), m)
	if _, i := adopted(a.v.State()); i != 0 || len(out) != 1 || out[0].Kind != State {
		t.Fatalf("adopted an unheard incarnation: %+v, out %+v", a.v.State(), out)
	}
	a.hear(at(lease), incI, 11)
	a.recv(at(lease), m)
	if g, i := adopted(a.v.State()); g != 4 || i != incI || a.v.State().Floor < 11 {
		t.Fatalf("after hearing it: %+v", a.v.State())
	}
}

// A restarted controller owes the quiet period again: it may have adopted
// its incarnation just before it went down (#1008 review, F1).
func TestRestartOwesTheQuietPeriod(t *testing.T) {
	T := at(10 * lease)
	x := newCtl(t, "B", commissioned(2, incJ, 0), T)
	if inc, from := x.c.Counting(); inc != incJ || from != T.Add(lease) {
		t.Fatalf("counts (%d from %v), want %d from %v", inc, from, incJ, T.Add(lease))
	}
}

// The stale-Ack path of #1008's F2: a lost controller's own exchange is
// pending (g, I) when an Ack for the same pair, sent to its earlier boot
// with a stale floor, arrives. It is not taken; the fresh one is.
func TestLostControllerIgnoresAnAckToItsEarlierBoot(t *testing.T) {
	now := at(lease)
	a := newCtl(t, "A", VoterState{}, 0)
	a.hear(now, incI, 3)
	a.recv(now, Adopt{From: "B", Kind: State, Gen: 3, Incarnation: incI, Floor: 3, Nonce: 0xB1, Echo: 0xA1})
	if st := a.v.State(); st.PendingGen != 4 || st.PendingInc != incI {
		t.Fatalf("no proposal after the peer's state: %+v", st)
	}
	a.recv(now, Adopt{From: "B", Kind: Ack, Gen: 4, Incarnation: incI, Floor: 3, Nonce: 0xB1, Echo: 0xA0})
	if _, i := adopted(a.v.State()); i != 0 {
		t.Fatalf("adopted on an Ack to an earlier boot: %+v", a.v.State())
	}
	a.recv(now, Adopt{From: "B", Kind: Ack, Gen: 4, Incarnation: incI, Floor: 14, Nonce: 0xB1, Echo: 0xA1})
	if g, i := adopted(a.v.State()); g != 4 || i != incI || a.v.State().Floor < 14 {
		t.Fatalf("the fresh Ack: %+v", a.v.State())
	}
}

// A switch is proposed only while the peer is heard: a proposer counts no
// witness until the peer acks, so with the peer down a stray copy
// answering while the real witness is silent would leave it with none.
func TestSwitchNeedsThePeer(t *testing.T) {
	a := newCtl(t, "A", commissioned(1, incI, 0), 0)
	a.peerDown = true
	now := at(lease)
	a.hear(now, incK, 0)
	if q, _ := a.tick(now); len(q) != 0 || a.v.State().PendingGen != 0 {
		t.Fatalf("proposed a switch with the peer unheard: %+v", q)
	}
	if inc, _ := a.c.Counting(); inc != incI {
		t.Fatalf("stopped counting %d", incI)
	}
	a.peerDown = false
	if q, _ := a.tick(now); len(q) != 1 {
		t.Fatal("no switch with the peer heard")
	}
}

// #1008 review, 4b: B is uncommissioned and can't hear J; A pends (2, J),
// B pends (2, I) with I < J. Each refuses the other. The uncommissioned
// side gives way: outbid, it re-proposes I above A's pending, A takes it,
// and the pair is on I (A may switch to J later, after its hold-off).
func TestUncommissionedSideBreaksTheDeadlock(t *testing.T) {
	now := at(lease)
	a := newCtl(t, "A", commissioned(1, incI, 0), 0)
	b := newCtl(t, "B", VoterState{}, 0)
	b.hear(now, incI, 0)
	// B's own exchange: query, A's state, B proposes (2, I).
	q, _ := b.tick(now)
	st := a.recv(now, q[0])
	pb := b.recv(now, st[0])
	if len(pb) != 1 || pb[0].Gen != 2 || pb[0].Incarnation != incI {
		t.Fatalf("B's proposal: %+v", pb)
	}
	// Meanwhile A heard J and proposed (2, J); B can't take it.
	a.hear(now.Add(lease), incJ, 0)
	pa, _ := a.tick(now.Add(lease))
	if len(pa) != 1 || pa[0].Gen != 2 || pa[0].Incarnation != incJ {
		t.Fatalf("A's proposal: %+v", pa)
	}
	toA, toB := pb, pa
	for range 10 {
		var na, nb []Adopt
		for _, m := range toA {
			nb = append(nb, a.recv(now, m)...)
		}
		for _, m := range toB {
			na = append(na, b.recv(now, m)...)
		}
		toA, toB = na, nb
	}
	ga, ia := adopted(a.v.State())
	gb, ib := adopted(b.v.State())
	if ia != incI || ib != incI || ga != gb || a.v.State().PendingGen != 0 || b.v.State().PendingGen != 0 {
		t.Fatalf("not settled on I: A %+v, B %+v", a.v.State(), b.v.State())
	}
}

// The floor counts every incarnation heard this boot, not only the one
// adopted: a stray's promised term only raises it (#1008 review, 3).
func TestFloorOverEveryIncarnationHeard(t *testing.T) {
	now := at(lease)
	a := newCtl(t, "A", VoterState{}, 0)
	b := newCtl(t, "B", commissioned(3, incI, 5), 0)
	a.hear(now, incI, 6)
	a.hear(now, incK, 20)
	q, _ := a.tick(now)
	talk(now, a, b, q)
	if _, i := adopted(a.v.State()); i != incI {
		t.Fatalf("not re-commissioned on %d: %+v", incI, a.v.State())
	}
	if f := a.v.State().Floor; f < 20 {
		t.Fatalf("floor %d, want ≥ 20 (the stray's promised term)", f)
	}
}

// #1008 re-review (a): A proposed (2, J) and pends. The peer may have
// adopted J with its Ack lost, and lead on J's witness. A proposal of the
// OLD incarnation at a higher generation then reaches A: A re-adopts I,
// which it never stopped holding, but it must still count I only one
// lease after the write. Counting at once, it could win on {A, W_I} while
// the peer's lease on W_J runs: a split brain.
func TestReadoptOverPendingOwesTheQuietPeriod(t *testing.T) {
	a := newCtl(t, "A", commissioned(1, incI, 0), 0)
	now := at(lease)
	a.hear(now, incJ, 0)
	if q, _ := a.tick(now); len(q) != 1 || a.v.State().PendingGen != 2 {
		t.Fatalf("no proposal of J: %+v", q)
	}
	out := a.recv(now, Adopt{From: "B", Kind: Propose, Gen: 3, Incarnation: incI, Nonce: 0xB1, Echo: 0xA1})
	if len(out) != 1 || out[0].Kind != Ack {
		t.Fatalf("the higher proposal of I not acked: %+v", out)
	}
	if g, i := adopted(a.v.State()); g != 3 || i != incI || a.v.State().PendingGen != 0 {
		t.Fatalf("not re-adopted: %+v", a.v.State())
	}
	if inc, from := a.c.Counting(); inc != incI || from < now.Add(lease) {
		t.Fatalf("counts %d from %v, want %d from %v (the quiet period)", inc, from, incI, now.Add(lease))
	}
}

// #1008 re-review (b): a late Adopt sent to an EARLIER boot of A (it
// echoes A's old nonce) says nothing about the peer now: it may be down.
// It must not count as hearing the peer, or A proposes a switch to a
// stray it then waits on, counting no witness, until the peer is back.
func TestLateAdoptToAnEarlierBootIsNotThePeer(t *testing.T) {
	a := newCtl(t, "A", commissioned(1, incI, 0), 0)
	a.peerDown = true
	now := at(lease)
	a.hear(now, incK, 0)
	a.recv(now, Adopt{From: "B", Kind: State, Gen: 1, Incarnation: incI, Nonce: 0xB1, Echo: 0xA0})
	if q, _ := a.tick(now); len(q) != 0 || a.v.State().PendingGen != 0 {
		t.Fatalf("a late Adopt to an earlier boot let A propose a switch: %+v", q)
	}
	a.recv(now, Adopt{From: "B", Kind: State, Gen: 1, Incarnation: incI, Nonce: 0xB1, Echo: 0xA1})
	if q, _ := a.tick(now); len(q) != 1 || q[0].Echo != 0xB1 {
		t.Fatalf("an Adopt to this boot: no switch to the peer's boot: %+v", q)
	}
}

// A generation bump that keeps the incarnation needs no quiet period of
// its own, but must not cut short one already owed: B restarted at T owes
// one lease before it counts I (NewAdoption). A proposal of (2, I) that it
// adopts at T + lease/4 must leave it counting I only from T + lease.
func TestGenBumpKeepsTheRestartQuiet(t *testing.T) {
	T := at(10 * lease)
	b := newCtl(t, "B", commissioned(1, incI, 0), T)
	now := T.Add(lease / 4)
	out := b.recv(now, Adopt{From: "A", Kind: Propose, Gen: 2, Incarnation: incI, Nonce: 0xA1, Echo: 0xB1})
	if len(out) != 1 || out[0].Kind != Ack {
		t.Fatalf("the proposal of (2, I) not acked: %+v", out)
	}
	if inc, from := b.c.Counting(); inc != incI || from < T.Add(lease) {
		t.Fatalf("counts %d from %v, want %d from %v (the restart's quiet period)", inc, from, incI, T.Add(lease))
	}
}

// Two adoptions before the first one's write lands: (2, J) is new, so it
// owes the quiet period; (3, J) keeps J. The write that makes both durable
// must still start J's quiet period then, not count J at once.
func TestSecondAdoptionInFlightKeepsTheQuiet(t *testing.T) {
	b := newCtl(t, "B", commissioned(1, incI, 0), 0)
	now := at(2 * lease)
	b.hold = true
	b.recv(now, Adopt{From: "A", Kind: Propose, Gen: 2, Incarnation: incJ, Nonce: 0xA1, Echo: 0xB1})
	b.recv(now, Adopt{From: "A", Kind: Propose, Gen: 3, Incarnation: incJ, Nonce: 0xA1, Echo: 0xB1})
	b.flush(now, true)
	if g, i := adopted(b.v.State()); g != 3 || i != incJ {
		t.Fatalf("not adopted (3, J): %+v", b.v.State())
	}
	if inc, from := b.c.Counting(); inc != incJ || from < now.Add(lease) {
		t.Fatalf("counts %d from %v, want %d from %v (J's quiet period)", inc, from, incJ, now.Add(lease))
	}
}
