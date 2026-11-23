package quorum

import (
	"slices"
	"testing"
)

// #1029 3c-3c-1b: the fence recommission --from-spare sets on the name it
// replaces. A state of the name from before the command (the old box's
// disk) carries a committed adoption generation below the fence; the
// replacement re-adopts at it or above.

// A fenced state's request is refused and otherwise ignored: no grant at
// any term, no boot heard, the fence on the refusal; a probe's refusal
// still says the incarnation (a replacement's re-adoption needs it). A
// state at the fence is granted. fence_requests_off grants it.
func TestAFencedRequestIsRefusedAndIgnored(t *testing.T) {
	now := at(3 * lease)
	w, _ := NewWitness("W", lease, VoterState{Incarnation: 7}, 0, nil)
	if f := w.Fence("A", 5); f != 5 {
		t.Fatalf("the witness's fence %d, want the carried 5", f)
	}
	if p := w.MarkWrite(); p == nil || len(p.State.Dups) != 1 || p.State.Dups[0].Fence != 5 {
		t.Fatalf("the fence's write: %+v", p)
	}
	for _, r := range []Request{
		{From: "A", Term: 10, Nonce: 0xA1, ID: 1, Seq: 1, Gen: 4},
		{From: "A", Nonce: 0xA1, ID: ProbeID, Seq: 2, Gen: 4},
		{From: "A", Term: 11, Nonce: 0xA1, ID: 3, Seq: 3, Gen: 0},
	} {
		p, rs := w.Handle(now, r)
		if p != nil && p.State.Promised != 0 || len(rs) != 1 || rs[0].Granted || rs[0].Fence != 5 || rs[0].Incarnation != 7 {
			t.Fatalf("a request at generation %d: %+v %+v", r.Gen, p, rs)
		}
	}
	if b := w.LiveBoots(now, "A"); len(b) != 0 {
		t.Fatalf("a fenced state's boot was heard: %v", b)
	}
	p, rs := w.Handle(now, Request{From: "A", Term: 10, Nonce: 0xA2, ID: 4, Seq: 1, Gen: 5})
	if p == nil {
		t.Fatalf("a state at the fence: no grant (%+v)", rs)
	}
	if rs := w.Persisted(p.Seq, true); len(rs) != 1 || !rs[0].Granted || rs[0].Fence != 0 {
		t.Fatalf("a state at the fence: %+v", rs)
	}
}

// The fence is durable: a file with one is format 8, and a restarted
// voter refuses as before; one without stays at its format.
// fence_volatile forgets it.
func TestAFenceIsDurableAndOnlyAFencedFileIsFormat8(t *testing.T) {
	v := NewVoter("W", lease, VoterState{Incarnation: 7}, 0)
	if b, _ := v.State().MarshalBinary(); b[len(stateMagic)] != stateVersionPlain {
		t.Fatalf("a plain file is format %d", b[len(stateMagic)])
	}
	v.Fence("A", 5)
	b, _ := v.State().MarshalBinary()
	if b[len(stateMagic)] != stateVersionFence || stateVersionFence != 8 {
		t.Fatalf("a fenced file is format %d, want 8", b[len(stateMagic)])
	}
	var st VoterState
	if err := st.UnmarshalBinary(b); err != nil {
		t.Fatal(err)
	}
	r := NewVoter("W", lease, st, at(lease))
	if !r.Fenced("A", 4) || r.Fenced("A", 5) || r.FenceOf("A") != 5 || r.MaxFence() != 5 {
		t.Fatalf("restarted: fenced(4) %v fenced(5) %v fence %d", r.Fenced("A", 4), r.Fenced("A", 5), r.FenceOf("A"))
	}
}

// A switch proposed by the peer B: the incumbent A, its receiver, adopts
// first, and its Ack is lost; A is then switched off. B shows the
// proposal still pending, so its fence goes above it, and A's state is
// fenced. The replacement re-adopts from B at the fence: not fenced.
// fence_peer_own_gen_only fences only B's committed generation: A's
// state, one above it, passes.
func TestThePeersFenceCoversAReceiverThatAdoptedFirst(t *testing.T) {
	a := newCtl(t, "A", commissioned(1, incI, 9), 0)
	b := newCtl(t, "B", commissioned(1, incI, 4), 0)
	b.hear(at(100*ms), incI, 4)
	now := at(100*ms + lease)
	b.hear(now, incJ, 0)
	q, _ := b.tick(now)
	if len(q) != 1 || q[0].Kind != Propose || q[0].Gen != 2 {
		t.Fatalf("B's proposal: %+v", q)
	}
	q[0].Echo = a.c.cfg.Nonce // A's boot, as B learnt it
	if ack := a.recv(now, q[0]); len(ack) != 1 || ack[0].Kind != Ack {
		t.Fatalf("A's answer: %+v", ack) // lost on the way back
	}
	if g, _ := adopted(a.v.State()); g != 2 || b.v.State().PendingGen != 2 {
		t.Fatalf("A at %d, B pending %d", g, b.v.State().PendingGen)
	}
	fence := b.v.Fence("A", 0)
	if !b.v.Fenced("A", a.v.State().Gen) {
		t.Fatalf("B's fence %d lets A's state at %d pass", fence, a.v.State().Gen)
	}
	// The replacement: uncommissioned, it re-adopts B's committed
	// incarnation, above B's generation and its proposal's.
	r := newCtl(t, "A", VoterState{}, now)
	r.hear(now, incI, 4)
	out, _ := r.tick(now)
	talk(now, r, b, out)
	if g, i := adopted(r.v.State()); i != incI || b.v.Fenced("A", g) {
		t.Fatalf("the replacement at (%d, %d), fenced at B (fence %d)", g, i, fence)
	}
}

// A switch the incumbent A proposed: B adopts, the Ack is lost, A is
// switched off pending. The replacement re-adopts from B; then the old
// box is back on its old disk, re-sending its proposal. B takes nothing
// from it: answered, it would see B's State as stale, re-propose above
// it and climb over the fence. fence_adopt_off answers it.
func TestAnOldBoxCannotClimbOverItsFence(t *testing.T) {
	a := newCtl(t, "A", commissioned(1, incI, 9), 0)
	b := newCtl(t, "B", commissioned(1, incI, 4), 0)
	a.hear(at(100*ms), incI, 9)
	now := at(100*ms + lease)
	a.hear(now, incJ, 0)
	q, _ := a.tick(now)
	if len(q) != 1 || q[0].Kind != Propose || q[0].Gen != 2 {
		t.Fatalf("A's proposal: %+v", q)
	}
	q[0].Echo = b.c.cfg.Nonce
	b.recv(now, q[0]) // its Ack is lost
	old := a.v.State()
	if old.Gen != 1 || old.PendingGen != 2 || b.v.State().Gen != 2 {
		t.Fatalf("A %+v, B at %d", old, b.v.State().Gen)
	}
	fence := b.v.Fence("A", 0)
	r := newCtl(t, "A", VoterState{}, now)
	r.hear(now, incJ, 0)
	out, _ := r.tick(now)
	talk(now, r, b, out)
	if g, i := adopted(r.v.State()); i != incJ || g < fence {
		t.Fatalf("the replacement at (%d, %d), fence %d", g, i, fence)
	}
	o := newCtl(t, "A", old, now.Add(lease))
	o.c.cfg.Nonce = 0xA2 // another boot of the old box
	later := now.Add(3 * lease)
	o.hear(later, incJ, 0)
	out, _ = o.tick(later)
	talk(later, o, b, out)
	if g := o.v.State().Gen; !b.v.Fenced("A", g) {
		t.Fatalf("the old box climbed to generation %d, over the fence %d", g, fence)
	}
}

// A proposal is taken only by the boot it addresses: an old box beside
// the peer must not adopt the peer's proposal meant for the replacement.
// The proposer learns the boot from the State that refusal answers, and
// re-sends at once, once per peer boot: a peer that refuses for good is
// not asked in a loop. propose_any_echo takes it; propose_resent_at_look
// leaves the switch for the next look.
func TestAProposalIsTakenOnlyByTheBootItAddresses(t *testing.T) {
	a := newCtl(t, "A", commissioned(1, incI, 9), 0)
	m := Adopt{From: "B", Kind: Propose, Gen: 5, Incarnation: incJ, Nonce: 0xB1, Echo: 0xDEAD, CommitGen: 4, CommitInc: incJ}
	out := a.recv(at(lease), m)
	if g, _ := adopted(a.v.State()); g != 1 || len(out) != 1 || out[0].Kind != State || out[0].Echo != 0xB1 {
		t.Fatalf("a proposal to another boot: A at %d, answered %+v", g, out)
	}
	m.Echo = a.c.cfg.Nonce
	a.recv(at(lease), m)
	if g, i := adopted(a.v.State()); g != 5 || i != incJ {
		t.Fatalf("a proposal to this boot: A at (%d, %d)", g, i)
	}

	// A switch between two controllers that never exchanged a handshake:
	// B's first proposal addresses no boot of A, and is re-sent at once.
	a = newCtl(t, "A", commissioned(1, incI, 9), 0)
	b := newCtl(t, "B", commissioned(1, incI, 4), 0)
	b.hear(at(100*ms), incI, 4)
	now := at(100*ms + lease)
	b.hear(now, incJ, 0)
	q, _ := b.tick(now)
	talk(now, b, a, q)
	if g, i := adopted(a.v.State()); g != 2 || i != incJ {
		t.Fatalf("A at (%d, %d) after one look of B's: want the switch (2, %d)", g, i, incJ)
	}

	// An uncommissioned A refuses a switch for good: B asks again once,
	// not in a loop (talk panics on one).
	a = newCtl(t, "A", VoterState{}, 0)
	b = newCtl(t, "B", commissioned(1, incI, 4), 0)
	b.hear(at(100*ms), incI, 4)
	b.hear(now, incJ, 0)
	q, _ = b.tick(now)
	talk(now, b, a, q)
	if _, i := adopted(a.v.State()); i != 0 {
		t.Fatalf("an uncommissioned A took B's switch to %d", i)
	}
}

// A controller a voter refuses as fenced learns it from any reply, a
// probe's too, and campaigns no more in this boot (Superseded). An
// uncommissioned one, a replacement before its re-adoption, does not; nor
// a late refusal of an earlier state of its own that it is above now.
// fence_unnoted never learns it; fenced_still_campaigns campaigns on.
// Fenced at the peer only, with the witness answering unfenced (an
// incomplete command, naut-lead's ruling C), it still campaigns: it may
// lead with the witness. fence_hold_ignores_witness holds it.
func TestAFencedControllerSaysSoAndCampaignsNoMore(t *testing.T) {
	a := newCtl(t, "A", commissioned(1, incI, 9), 0)
	now := at(3 * lease)
	a.c.NoteMark(Reply{From: "W", To: "A", Nonce: a.c.cfg.Nonce, ID: ProbeID, Fence: 3})
	if a.c.Superseded() != "W" {
		t.Fatalf("superseded %q, want W", a.c.Superseded())
	}
	if r := a.c.Tick(now, "", 0, true); r != nil {
		t.Fatalf("a fenced controller campaigns: %+v", r)
	}
	p := newCtl(t, "A", commissioned(1, incI, 9), 0)
	p.c.NoteMark(Reply{From: "B", To: "A", Nonce: p.c.cfg.Nonce, ID: ProbeID, Fence: 3})
	if p.c.Superseded() != "B" || p.c.Tick(now, "", 0, true) != nil {
		t.Fatalf("fenced at the peer, no word from the witness: superseded %q, campaigns", p.c.Superseded())
	}
	p.c.NoteMark(Reply{From: "W", To: "A", Nonce: p.c.cfg.Nonce, ID: ProbeID})
	if p.c.Tick(now.Add(lease), "", 0, true) == nil {
		t.Fatal("fenced at the peer only, the witness answering unfenced: no campaign")
	}
	r := newCtl(t, "A", VoterState{}, 0)
	r.c.NoteMark(Reply{From: "W", To: "A", Nonce: r.c.cfg.Nonce, ID: ProbeID, Fence: 3})
	up := newCtl(t, "A", commissioned(3, incI, 9), 0)
	up.c.NoteMark(Reply{From: "B", To: "A", Nonce: up.c.cfg.Nonce, ID: ProbeID, Fence: 3})
	if r.c.Superseded() != "" || up.c.Superseded() != "" {
		t.Fatalf("superseded: uncommissioned %q, at the fence %q", r.c.Superseded(), up.c.Superseded())
	}
}

// A request carries the sender's committed generation, a reply the
// voter's.
func TestFramesCarryTheCommittedGeneration(t *testing.T) {
	a := newCtl(t, "A", VoterState{Gen: 4, PendingGen: 6, PendingInc: incJ, Incarnation: incI, Promised: 9}, 0)
	now := at(3 * lease)
	req := a.c.Tick(now, "", 0, true)
	if req == nil || req.Gen != 4 {
		t.Fatalf("a's request: %+v", req)
	}
	if p := a.c.Probe(now.Add(lease)); p == nil || p.Gen != 4 {
		t.Fatalf("a's probe: %+v", p)
	}
	_, rs := a.v.Handle(now, Request{From: "B", Term: 0, Nonce: 0xB1, ID: ProbeID, Seq: 1})
	if len(rs) != 1 || rs[0].Gen != 4 {
		t.Fatalf("a's reply: %+v", rs)
	}
}

// #1164 review, B3: the incumbent lost its file, re-adopts, and dies with
// its proposal (B's Gen + 1) durable and undelivered. The fence goes two
// above B's adoption, so that proposal is below it, and B drops it
// unanswered: acked, or answered and re-proposed, the old disk would
// commit at the fence. The replacement learns the fence from B's answer
// to its query alone (no witness fence here) and re-adopts at it.
// fence_plus_one: the old disk is acked at the fence. fenced_propose_answered:
// B answers it. fence_unproposed: the replacement proposes below the
// fence and is never adopted.
func TestTheFenceIsAboveAnyProposalAnOldDiskHolds(t *testing.T) {
	b := newCtl(t, "B", commissioned(1, incI, 4), 0)
	a := newCtl(t, "A", VoterState{}, 0) // the incumbent, its file lost
	now := at(lease)
	a.hear(now, incI, 4)
	q, _ := a.tick(now)
	st := b.recv(now, q[0]) // the query: B answers its State
	if len(st) != 1 || st[0].Kind != State {
		t.Fatalf("B's answer to the query: %+v", st)
	}
	prop := a.recv(now, st[0])
	if len(prop) != 1 || prop[0].Kind != Propose || prop[0].Gen != 2 || a.v.State().PendingGen != 2 {
		t.Fatalf("the incumbent's re-adoption: %+v (pending %d)", prop, a.v.State().PendingGen)
	}
	// Lost; the incumbent dies with it durable. The operator's command:
	fence := b.v.Fence("A", 0)
	if fence != 3 {
		t.Fatalf("fence %d, want 3: two above B's generation 1", fence)
	}
	old := newCtl(t, "A", a.v.State(), now.Add(lease))
	old.c.cfg.Nonce = 0xA2
	later := now.Add(3 * lease)
	old.hear(later, incI, 4)
	re, _ := old.tick(later)
	if len(re) != 1 || re[0].Kind != Propose || re[0].Gen != 2 {
		t.Fatalf("the old disk's re-sent proposal: %+v", re)
	}
	re[0].Echo = b.c.cfg.Nonce // as if it had heard B's boot
	if out := b.recv(later, re[0]); len(out) != 0 {
		t.Fatalf("B answered the old disk's proposal below the fence: %+v", out)
	}
	if g, i := adopted(b.v.State()); g != 1 || i != incI || old.v.State().Incarnation != 0 {
		t.Fatalf("B at (%d, %d), the old disk at %+v", g, i, old.v.State())
	}
	// The replacement: its query is answered with the fence.
	r := newCtl(t, "A", VoterState{}, later)
	r.c.cfg.Nonce = 0xA3
	r.hear(later, incI, 4)
	out, _ := r.tick(later)
	talk(later, r, b, out)
	if g, i := adopted(r.v.State()); i != incI || g < fence {
		t.Fatalf("the replacement at (%d, %d), fence %d", g, i, fence)
	}
}

// A second --from-spare fences above the first: the first replacement,
// dead with its re-adoption at the first fence durable, is below the
// second. fence_ignores_previous: it is not.
func TestASecondFromSpareFencesAboveTheFirst(t *testing.T) {
	b := newCtl(t, "B", commissioned(1, incI, 4), 0)
	first := b.v.Fence("A", 0)
	r1 := newCtl(t, "A", VoterState{}, 0)
	now := at(lease)
	r1.hear(now, incI, 4)
	q, _ := r1.tick(now)
	prop := r1.recv(now, b.recv(now, q[0])[0])
	if len(prop) != 1 || prop[0].Gen != first {
		t.Fatalf("the first replacement's proposal %+v, want it at the fence %d", prop, first)
	}
	// Lost; the first replacement dies. A second spare, a second command:
	second := b.v.Fence("A", 0)
	if second <= first {
		t.Fatalf("second fence %d, not above the first %d", second, first)
	}
	if !b.v.Fenced("A", r1.v.State().PendingGen) {
		t.Fatalf("the first replacement's proposal at %d passes the fence %d", r1.v.State().PendingGen, second)
	}
}

// Only a voter that lost its state learns fences from frames (naut-lead's
// ruling on #1164): an uncommissioned controller and a witness with no
// record of the name; not a commissioned controller, nor a witness the
// command reached (it keeps the name's clear record), so an incomplete
// fence does not spread (ruling C). A fence on its own name is never
// taken. fence_not_learned learns none.
func TestOnlyAVoterThatLostItsStateLearnsFences(t *testing.T) {
	fs := Fences{{Name: "A", Fence: 5}, {Name: "B", Fence: 7}}
	wiped := NewVoter("B", lease, VoterState{}, 0)
	NewAdoption(wiped, newC(t, Config{Self: "B", Peer: "A", Witness: "W", Lease: lease, Rho: 0.01, Nonce: 0xB1}, 0, 0), 0)
	if !wiped.LearnFences(fs, "A") || wiped.FenceOf("A") != 5 || wiped.FenceOf("B") != 0 {
		t.Fatalf("an uncommissioned controller: A %d B %d", wiped.FenceOf("A"), wiped.FenceOf("B"))
	}
	if p := wiped.MarkWrite(); p == nil || p.State.Dups[0].Fence != 5 {
		t.Fatalf("the learnt fence's write: %+v", p)
	}
	kept := NewVoter("B", lease, commissioned(2, incI, 4), 0)
	NewAdoption(kept, newC(t, Config{Self: "B", Peer: "A", Witness: "W", Lease: lease, Rho: 0.01, Nonce: 0xB1, Incarnation: incI}, 4, 0), 0)
	if kept.LearnFences(fs, "A") || kept.FenceOf("A") != 0 {
		t.Fatalf("a commissioned controller learnt %d", kept.FenceOf("A"))
	}
	rebuilt, _ := NewWitness("W", lease, VoterState{Incarnation: 7}, 0, nil)
	if !rebuilt.LearnFences(fs, "A", "B") || rebuilt.FenceOf("A") != 5 || rebuilt.FenceOf("B") != 7 {
		t.Fatalf("a witness with no record: A %d B %d", rebuilt.FenceOf("A"), rebuilt.FenceOf("B"))
	}
	marked, _ := markA(t) // a rebuilt witness heard the old box beside the replacement
	marked.isWitness = true
	if !slices.Contains(marked.Marked(), "A") || !marked.LearnFences(Fences{{Name: "A", Fence: 5}}, "A", "B") {
		t.Fatalf("a rebuilt witness that marked A itself: marked %v, fence %d", marked.Marked(), marked.FenceOf("A"))
	}
	reached, _ := NewWitness("W", lease, VoterState{Incarnation: 7}, 0, nil)
	reached.ClearDuplicateKeeping("A", 0, nil, 3) // the command's clear reached it
	if reached.LearnFences(Fences{{Name: "A", Fence: 5}}, "A", "B") || reached.FenceOf("A") != 0 {
		t.Fatalf("a witness the command reached learnt %d", reached.FenceOf("A"))
	}
}

// #1164 review, B2: the peer lost its file after the command. The
// witness's reply carries A's fence; learnt from it before the same reply
// makes the witness heard, the old box's State is dropped: no proposal.
// fence_not_learned re-adopts from the old box at the fence.
func TestAWitnessReplyThenTheOldBoxsStateGivesNoProposal(t *testing.T) {
	b := newCtl(t, "B", VoterState{}, 0) // wiped
	now := at(2 * lease)
	r := Reply{From: "W", To: "B", Nonce: b.c.cfg.Nonce, ID: ProbeID, Incarnation: incI, Promised: 4, Fences: Fences{{Name: "A", Fence: 3}}}
	b.v.LearnFences(r.Fences, "A") // the driver, first
	b.a.OnWitness(now, r)
	q, _ := b.tick(now)
	st := Adopt{From: "A", Kind: State, Gen: 2, Incarnation: incI, Nonce: 0xA1, Echo: b.c.cfg.Nonce, CommitGen: 2, CommitInc: incI}
	if p, out := b.a.OnAdopt(now, st); p != nil || len(out) != 0 {
		t.Fatalf("the wiped peer took the old box's State: %+v %+v (query %+v)", p, out, q)
	}
	if b.v.State().PendingGen != 0 {
		t.Fatalf("the wiped peer proposes %d", b.v.State().PendingGen)
	}
}

// The peer's proposal in flight can be well above its generation (a
// stale or outbid re-proposal goes above the other side's pending), and
// its receiver, the incumbent, may have adopted it with its Ack lost: the
// fence goes above the proposal, not only above the peer's generation.
// fence_peer_own_gen_only lets the incumbent's state pass.
func TestTheFenceCoversAPeersProposalFarAboveItsGeneration(t *testing.T) {
	b := newCtl(t, "B", VoterState{Gen: 1, Incarnation: incI, PendingGen: 6, PendingInc: incJ, Promised: 4}, 0)
	a := newCtl(t, "A", commissioned(1, incI, 9), 0)
	m := Adopt{From: "B", Kind: Propose, Gen: 6, Incarnation: incJ, Nonce: 0xB1, Echo: a.c.cfg.Nonce, Pending: 6, CommitGen: 1, CommitInc: incI}
	a.recv(at(lease), m) // adopted; its Ack is lost
	if g, _ := adopted(a.v.State()); g != 6 {
		t.Fatalf("A at %d", g)
	}
	if f := b.v.Fence("A", 0); !b.v.Fenced("A", 6) {
		t.Fatalf("B's fence %d lets the incumbent's state at 6 pass", f)
	}
}

// #1164 re-check, N1: a fenced voter still hears an uncommissioned sender
// of the name as a boot of it (a wiped old box is a lookalike of the
// replacement, marked when heard beside it), never a commissioned old
// state. fence_hides_lookalike hears neither.
func TestAFencedVoterStillHearsAnUncommissionedLookalike(t *testing.T) {
	now := at(3 * lease)
	w, _ := NewWitness("W", lease, VoterState{Incarnation: 7}, 0, nil)
	w.Fence("A", 5)
	_, rs := w.Handle(now, Request{From: "A", Nonce: 0xA1, ID: ProbeID, Seq: 1, Gen: 4})
	if len(rs) != 1 || rs[0].Fence != 5 || len(w.LiveBoots(now, "A")) != 0 {
		t.Fatalf("a commissioned old state: %+v, boots %v", rs, w.LiveBoots(now, "A"))
	}
	_, rs = w.Handle(now, Request{From: "A", Nonce: 0xA2, ID: ProbeID, Seq: 1, Uncommissioned: true})
	if len(rs) != 1 || rs[0].Granted || rs[0].Fence != 5 {
		t.Fatalf("an uncommissioned one: %+v", rs)
	}
	if b := w.LiveBoots(now, "A"); len(b) != 1 || b[0] != 0xA2 {
		t.Fatalf("the uncommissioned sender's boot not heard: %v", b)
	}
}

// A voter learns fences only on the names it may (a controller its
// peer's, the witness the pair's controllers'), and a controller sets its
// own fence from its own adoption, never a carried value it can't check
// (#1164 re-check, S5, S6). fence_learns_any_name, fence_takes_carried.
func TestAFenceIsLearntOnlyOnAPairNameAndNeverCarriedToAController(t *testing.T) {
	wiped := NewVoter("B", lease, VoterState{}, 0)
	NewAdoption(wiped, newC(t, Config{Self: "B", Peer: "A", Witness: "W", Lease: lease, Rho: 0.01, Nonce: 0xB1}, 0, 0), 0)
	if wiped.LearnFences(Fences{{Name: "W", Fence: 9}}, "A") || wiped.FenceOf("W") != 0 {
		t.Fatalf("learnt a fence on the witness's name: %d", wiped.FenceOf("W"))
	}
	b := newCtl(t, "B", commissioned(3, incI, 4), 0)
	if f := b.v.Fence("A", 1<<40); f != 5 {
		t.Fatalf("B's fence %d: want two above its own generation 3, the carried value ignored", f)
	}
}
