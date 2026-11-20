package quorum

import (
	"slices"
	"testing"
	"time"
)

// The confirmed-duplicate mark (#1029 3c). D is the one-way delay the
// mark allows a real restart's last frames: a quarter lease, stretched by
// the clock-rate margin.
const markD = time.Duration(float64(lease/4) * (1 + MaxRho))

// probe is a request from name under nonce with Seq seq, as a relay probe:
// always refused, it only tells the voter that boot is running.
func probe(name NodeID, nonce, seq uint64) Request {
	return Request{From: name, Nonce: nonce, ID: ProbeID, Seq: seq}
}

// A real restart: the old boot's frames all left before the new boot's
// first did, so a late one arrives within the one-way delay of it, and
// marks nothing. A new frame from EACH boot beyond that delay after the
// other's first means both still run: marked, and counted once (the
// alarm). dup_mark_no_delay turns the first half red, dup_mark_off the
// second.
func TestAMarkNeedsTheOldBootPastTheDelay(t *testing.T) {
	t0 := at(lease)
	v := ready("W")
	v.Handle(t0, probe("A", 0xA, 1))
	v.Handle(t0.Add(10*ms), probe("A", 0xB, 1)) // the new boot's first frame
	v.Handle(t0.Add(markD), probe("A", 0xA, 2))
	if len(v.Marked()) != 0 {
		t.Fatalf("marked %v on an old boot's frame within the one-way delay of the new boot's first: a restart", v.Marked())
	}
	if n, _ := v.Duplicates(); n != 0 {
		t.Fatalf("a restart counted %d duplicates", n)
	}

	v = ready("W")
	v.Handle(t0, probe("A", 0xA, 1))
	v.Handle(t0.Add(10*ms), probe("A", 0xB, 1))
	t1 := t0.Add(10*ms + markD + 10*ms)
	v.Handle(t1, probe("A", 0xA, 2))
	if len(v.Marked()) != 0 {
		t.Fatalf("marked %v before the second boot, too, sent past the delay", v.Marked())
	}
	v.Handle(t1.Add(10*ms), probe("A", 0xB, 2))
	if !slices.Equal(v.Marked(), []NodeID{"A"}) {
		t.Fatalf("marked %v, want [A]: both boots kept sending after the other started", v.Marked())
	}
	if n, who := v.Duplicates(); n != 1 || who != "A" || v.DuplicateCause() != DupConfirmed {
		t.Fatalf("duplicates %d (%s, cause %d), want 1 (A, confirmed)", n, who, v.DuplicateCause())
	}
	// Further frames add no alarm: the mark is counted once.
	v.Handle(t0.Add(2*lease), probe("A", 0xA, 4))
	v.Handle(t0.Add(2*lease), probe("A", 0xB, 4))
	if n, _ := v.Duplicates(); n != 1 {
		t.Fatalf("duplicates %d after more frames, want 1", n)
	}
}

// Whichever boot the voter heard first, a copy that keeps sending is
// marked: here the copy is heard first (the voter restarted while the
// original was cut off), and it is the copy's frame that marks.
func TestAMarkDoesNotDependOnWhichBootCameFirst(t *testing.T) {
	t0 := at(lease)
	v := ready("B")
	v.Handle(t0, probe("A", 0xC, 7))
	v.Handle(t0.Add(lease), probe("A", 0xA, 40))
	v.Handle(t0.Add(lease+lease/2), probe("A", 0xC, 8))
	v.Handle(t0.Add(lease+lease*3/4), probe("A", 0xA, 41))
	v.Handle(t0.Add(2*lease), probe("A", 0xC, 9))
	v.Handle(t0.Add(2*lease+lease/4), probe("A", 0xA, 42))
	if !slices.Equal(v.Marked(), []NodeID{"A"}) {
		t.Fatalf("marked %v, want [A]", v.Marked())
	}
}

// A frame whose Seq the voter has already seen from that boot is a
// duplicate or a replay (F31): it proves nothing about the boot running
// now, however late it arrives. dup_mark_counts_replays turns it red.
func TestAReplayedFrameNeverMarks(t *testing.T) {
	t0 := at(lease)
	v := ready("W")
	v.Handle(t0, probe("A", 0xA, 5))
	v.Handle(t0.Add(10*ms), probe("A", 0xB, 1))
	v.Handle(t0.Add(lease), probe("A", 0xA, 5))       // the same frame again, a lease late
	v.Handle(t0.Add(lease), probe("A", 0xA, 3))       // an older one
	v.Handle(t0.Add(lease+10*ms), probe("A", 0xB, 2)) // the new boot sends on
	v.Handle(t0.Add(lease+20*ms), probe("A", 0xB, 3))
	if len(v.Marked()) != 0 {
		t.Fatalf("marked %v on replayed frames of the old boot", v.Marked())
	}
}

// After a real restart, the other voters relay their promise to the OLD
// boot for up to a lease: that is no evidence the old boot still runs,
// and marks nothing. dup_mark_from_relay turns it red.
func TestARelayedOldBootNeverMarks(t *testing.T) {
	t0 := at(lease)
	v := ready("W")
	v.Handle(t0, probe("A", 0xA, 9))
	v.Handle(t0.Add(10*ms), probe("A", 0xB, 1))
	for i := time.Duration(1); i <= 4; i++ {
		v.Hear(t0.Add(i*lease/4), "B", 0xB0, Relay{To: "A", Nonce: 0xA, Left: lease - i*lease/4})
		v.Handle(t0.Add(i*lease/4+ms), probe("A", 0xB, uint64(1+i))) // the new boot sends on
	}
	if len(v.Marked()) != 0 {
		t.Fatalf("marked %v on promises relayed to the old boot", v.Marked())
	}
}

// markA marks A at a fresh W: A's first boot 0xA holds W's running
// promise for term 3, and a second boot 0xC runs beside it.
func markA(t *testing.T) (*Voter, Instant) {
	t.Helper()
	v := ready("W")
	now := at(lease)
	if !grant(t, v, now, Request{From: "A", Term: 3, Nonce: 0xA, ID: 1, Seq: 1}) {
		t.Fatal("grant refused")
	}
	v.Handle(now.Add(10*ms), probe("A", 0xC, 1))
	now = now.Add(lease / 4 * 3)
	if !grant(t, v, now, Request{From: "A", Term: 3, Nonce: 0xA, ID: 2, Renewal: true, Seq: 2}) {
		t.Fatal("the incumbent's renewal was refused")
	}
	v.Handle(now.Add(10*ms), probe("A", 0xC, 2))
	now = now.Add(lease / 4)
	if !grant(t, v, now, Request{From: "A", Term: 3, Nonce: 0xA, ID: 3, Renewal: true, Seq: 3}) {
		t.Fatal("the incumbent's renewal was refused")
	}
	now = now.Add(10 * ms)
	v.Handle(now, probe("A", 0xC, 3))
	if !slices.Equal(v.Marked(), []NodeID{"A"}) {
		t.Fatalf("marked %v, want [A]", v.Marked())
	}
	return v, now
}

// A marked name keeps the promise it holds, but gets no new grant, to
// either boot, for as long as the mark stands: no lease of silence ends
// it (that is what a split looks like). Each refusal tells the requester
// (Reply.Marked), and the voter counts the name duplicated, so its own
// candidate discounts that name's vote. dup_mark_off and marked_not_told
// turn it red.
func TestAMarkedNameGetsNoNewGrantUntilCleared(t *testing.T) {
	v, now := markA(t)
	if !grant(t, v, now.Add(lease/2), Request{From: "A", Term: 3, Nonce: 0xA, ID: 4, Renewal: true, Seq: 4}) {
		t.Fatal("the running promise to the incumbent boot didn't renew")
	}
	later := now.Add(10 * lease) // both boots long silent
	for _, r := range []Request{
		{From: "A", Term: 9, Nonce: 0xA, ID: 5, Seq: 5},
		{From: "A", Term: 9, Nonce: 0xC, ID: 2, Seq: 4},
	} {
		_, out := v.Handle(later, r)
		if len(out) != 1 || out[0].Granted {
			t.Fatalf("a marked name got a new grant ten leases on: %+v", out)
		}
		if !out[0].Marked {
			t.Fatalf("the refusal didn't say the name is marked: %+v", out[0])
		}
	}
	if !v.Duplicated(later, "A") {
		t.Fatal("the mark no longer discounts A's vote")
	}
	_, out := v.Handle(later, Request{From: "B", Term: 9, Nonce: 0xB, ID: 1, Seq: 1})
	if out != nil && len(out) == 1 && out[0].Marked {
		t.Fatal("B was told it is marked")
	}
}

// ClearDuplicate ends the mark: the operator stopped the copy. The name
// is granted again. clear_keeps_mark turns it red.
func TestClearingAMarkGrantsTheNameAgain(t *testing.T) {
	v, now := markA(t)
	if !v.ClearDuplicate("A") {
		t.Fatal("ClearDuplicate reported A unmarked")
	}
	if v.ClearDuplicate("A") {
		t.Fatal("a second clear reported a mark")
	}
	later := now.Add(3 * lease)
	if !grant(t, v, later, Request{From: "A", Term: 9, Nonce: 0xA, ID: 5, Seq: 5}) {
		t.Fatal("A refused after its mark was cleared")
	}
	if v.Duplicated(later, "A") {
		t.Fatal("A still counted duplicated after the clear")
	}
	// A copy that still runs is marked again within D and a look.
	v.Handle(later.Add(ms), probe("A", 0xC, 9))
	v.Handle(later.Add(lease/4*3), probe("A", 0xA, 9))
	v.Handle(later.Add(lease), probe("A", 0xC, 10))
	v.Handle(later.Add(lease+lease/4), probe("A", 0xA, 10))
	v.Handle(later.Add(lease+lease/2), probe("A", 0xC, 11))
	if !slices.Equal(v.Marked(), []NodeID{"A"}) {
		t.Fatalf("marked %v, want [A] again: the copy kept running after the clear", v.Marked())
	}
}

// A marked candidate stops campaigning: the voter that marked it grants it
// nothing new, and each campaign would only promise its own voter to it.
// It keeps probing (how it hears the clear) and, once that voter answers
// unmarked, campaigns again. A lease it holds still renews. Seq rises on
// every request. marked_still_campaigns turns it red.
func TestAMarkedCandidateStopsCampaigning(t *testing.T) {
	cfg := cfgA()
	c := newC(t, cfg, 0, 0)
	now := at(2 * lease)
	p := c.Probe(now)
	if p == nil || p.Seq == 0 {
		t.Fatalf("probe %+v, want a Seq", p)
	}
	rep := refused("W", p, 0, 42)
	rep.Marked = true
	c.NoteMark(rep)
	if r := c.Tick(now, "", 0, true); r != nil {
		t.Fatalf("campaigned while the witness marks it: %+v", r)
	}
	p2 := c.Probe(now.Add(lease / 4))
	if p2 == nil || p2.Seq <= p.Seq {
		t.Fatalf("probe %+v while marked, want one with a higher Seq than %d", p2, p.Seq)
	}
	c.NoteMark(refused("W", p2, 0, 42)) // the mark was cleared
	r := c.Tick(now.Add(lease/2), "", 0, true)
	if r == nil || r.Seq <= p2.Seq {
		t.Fatalf("no campaign once the mark was cleared, or no higher Seq: %+v", r)
	}

	// Leading when the mark comes: the lease still renews.
	c = newC(t, cfg, 0, 0)
	r = c.Tick(0, "", 0, true)
	c.OnReply(Instant(ms), granted("A", r, 0))
	c.OnReply(Instant(ms), granted("W", r, 42))
	if !c.Leading(Instant(ms)) {
		t.Fatal("no lease")
	}
	m := granted("W", r, 42)
	m.Marked = true
	c.NoteMark(m)
	if rn := c.Tick(Instant(lease/4), "", 0, true); rn == nil || !rn.Renewal {
		t.Fatalf("a marked leader stopped renewing: %+v", rn)
	}
}

// A grant the candidate discounts (its own voter knows the granting voter
// duplicated) is no term conflict: it must not re-term the leader, or a
// voter marked for good would push the term up every renewal for ever.
// discount_reterms turns it red.
func TestADiscountedGrantDoesNotReterm(t *testing.T) {
	cfg := Config{Self: "B", Peer: "A", Witness: "W", Lease: lease, Rho: 0.01, Incarnation: 42, Nonce: 0xB}
	c := newC(t, cfg, 0, 0)
	dup := false
	c.duplicated = func(_ Instant, name NodeID) bool { return dup && name == "A" }
	r := c.Tick(0, "", 0, true)
	c.OnReply(Instant(ms), granted("B", r, 0))
	c.OnReply(Instant(ms), granted("W", r, 42))
	if !c.Leading(Instant(ms)) {
		t.Fatal("no lease")
	}
	dup = true
	now := Instant(0)
	for i := 0; i < 4; i++ {
		now = now.Add(lease / 4)
		rn := c.Tick(now, "", 0, true)
		if rn == nil {
			t.Fatalf("renewal %d missing", i)
		}
		if rn.Term != r.Term {
			t.Fatalf("renewal %d at term %d, want %d: a discounted grant re-termed the leader", i, rn.Term, r.Term)
		}
		c.OnReply(now.Add(ms), granted("B", rn, 0))
		c.OnReply(now.Add(ms), granted("A", rn, 0)) // discounted
		c.OnReply(now.Add(ms), granted("W", rn, 42))
	}
}

// The false-mark route the symmetric rule closes: a voter that never heard
// the old boot (it has just restarted, or its link to the controller has
// just come up) hears the restarted controller's new boot first, and then
// a frame of the old boot that was still in flight, within the one-way
// delay. The new boot keeps sending; the old one never does again. A rule
// that took the boot heard first as the older one would mark the
// controller here. dup_mark_one_sided turns it red.
func TestAVoterThatMissedTheOldBootMarksNoRestart(t *testing.T) {
	t0 := at(lease)
	v := ready("W")
	v.Handle(t0, probe("A", 0xB, 1))                   // the new boot, heard first
	v.Handle(t0.Add(markD-10*ms), probe("A", 0xA, 77)) // the old boot's last frame, in flight
	for i := time.Duration(1); i <= 8; i++ {
		v.Handle(t0.Add(i*lease/4), probe("A", 0xB, uint64(1+i)))
	}
	if len(v.Marked()) != 0 {
		t.Fatalf("marked %v: a restart seen by a voter that missed the old boot", v.Marked())
	}
}

// Rule (ii) of the #1113 review: a voter whose own candidate is held by a
// mark makes no NEW grant to the other controller. A copy of a controller
// carries a copy of its voter; without the rule, the held copy's voter
// can hand the other controller a lease (the peer, restarted inside a
// split and having forgotten its own mark, leads on {itself, the copy's
// voter}) while the original renews with the witness. A promise it
// already runs to that very boot still renews. held_voter_grants turns it
// red. (In the simulator the original is held or discounts its own vote
// whenever this happens after a single restart; TestHeldVoterGrantsNothingIn
// ADoubleRestart is the schedule where the rule alone matters.)
func TestAHeldControllersVoterGrantsTheOtherNoNewLease(t *testing.T) {
	cfg := Config{Self: "A", Peer: "B", Witness: "W", Lease: lease, Rho: 0.01, Incarnation: 42, Nonce: 0xC}
	c := newC(t, cfg, 0, 0)
	v := NewVoter("A", lease, VoterState{Incarnation: 42}, 0) // the copy's voter
	NewAdoption(v, c, 0)
	now := at(2 * lease)
	if !grant(t, v, now, Request{From: "B", Term: 3, Nonce: 0xB, ID: 1, Seq: 1}) {
		t.Fatal("no grant to B before any mark")
	}
	c.NoteMark(Reply{From: "W", To: "A", Nonce: 0xC, Marked: true, VoterBoot: 7})
	if !c.Marked() {
		t.Fatal("the candidate isn't held")
	}
	if !grant(t, v, now.Add(lease/4), Request{From: "B", Term: 3, Nonce: 0xB, ID: 2, Renewal: true, Seq: 2}) {
		t.Fatal("the running promise to B's very boot didn't renew")
	}
	later := now.Add(3 * lease) // that promise has run out
	if grant(t, v, later, Request{From: "B", Term: 4, Nonce: 0xB2, ID: 1, Seq: 1}) {
		t.Fatal("a held controller's voter made a new grant to the other controller")
	}
}

// Rule (i) of the #1113 review: a hold ends only on an unmarked word from
// the very voter boot that set it. A voter that restarted has forgotten
// its mark and answers unmarked: that must not free the copy it marked
// (the witness restarted inside a split, with the copy still running,
// would grant it). The same boot's word, after a ClearDuplicate, does.
// hold_released_by_any_boot turns it red.
func TestAHoldEndsOnlyOnTheMarkingBootsWord(t *testing.T) {
	c := newC(t, cfgA(), 0, 0)
	marked := Reply{From: "W", To: "A", Nonce: 0xA1, Marked: true, VoterBoot: 7}
	c.NoteMark(marked)
	c.NoteMark(Reply{From: "W", To: "A", Nonce: 0xA1, VoterBoot: 8}) // W restarted
	if !c.Marked() {
		t.Fatal("a restarted voter's unmarked reply released the hold its earlier boot set")
	}
	c.NoteMark(Reply{From: "W", To: "A", Nonce: 0xA1, VoterBoot: 7}) // the marking boot cleared it
	if c.Marked() {
		t.Fatal("the marking boot's unmarked reply didn't release the hold")
	}
}

// A voter at its boot cap forgets the boot heard from longest ago, and
// does not ban it: an original cut from this voter while a copy restarted
// over and over comes back as a new boot, and is marked beside the copy's
// running boot (#1113 review). evicted_boot_banned turns it red.
func TestAnEvictedOriginalIsMarkedWhenItComesBack(t *testing.T) {
	v := ready("W")
	now := at(lease)
	v.Handle(now, probe("A", 0xA, 1))        // the original, then cut from W
	for b := uint64(1); b <= maxBoots; b++ { // a copy restarting eight times
		now = now.Add(lease)
		v.Handle(now, probe("A", 0x100+b, 1))
	}
	for i := uint64(2); i < 20; i++ { // the original back, beside the copy's last boot
		now = now.Add(lease / 4)
		v.Handle(now, probe("A", 0xA, i))
		v.Handle(now.Add(ms), probe("A", 0x100+maxBoots, i))
	}
	if !slices.Equal(v.Marked(), []NodeID{"A"}) {
		t.Fatalf("marked %v after two boots alternated for 18 looks: the original was banned when evicted", v.Marked())
	}
}
