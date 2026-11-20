package quorum

import (
	"testing"
	"time"
)

// The voters relay their running promises (#1029 layer 2): a MAC-spoofed
// clone loads the original's voter state as its own, so the pair has four
// voters under three names, and the one voter a clone needs must hear what
// another promised.

// ready is a voter past its restart wait at lease.
func ready(self NodeID) *Voter { return NewVoter(self, lease, VoterState{}, 0) }

// Rule (a), shape 1: B's voter heard W relay its promise to A under Na.
// A clone of A (another nonce) is refused while that promise runs, plus
// the clock-rate margin, and counted a duplicate. Its request also makes
// A's second boot live here, so the real A gets no NEW grant from B either
// until the clone has been silent a lease (#1029 step 1), and then does.
func TestVoterRefusesANameAHeardPromiseHoldsUnderAnotherNonce(t *testing.T) {
	v := ready("B")
	now := at(lease)
	v.Hear(now, "W", 0xF00, Relay{To: "A", Nonce: 0xA, Left: lease})
	if !grant(t, v, now.Add(lease/4), Request{From: "A", Term: 9, Nonce: 0xA, ID: 1}) {
		t.Fatal("refused the real A, whose nonce the relayed promise names, with no clone heard")
	}
	v2 := ready("B")
	v2.Hear(now, "W", 0xF00, Relay{To: "A", Nonce: 0xA, Left: lease})
	if grant(t, v2, now.Add(lease/2), Request{From: "A", Term: 9, Nonce: 0xC, ID: 1}) {
		t.Fatal("granted a clone of A while W's relayed promise to A under another nonce ran: two leaders")
	}
	if n, who := v2.Duplicates(); n != 1 || who != "A" || v2.DuplicateCause() != DupNonce {
		t.Fatalf("duplicates %d (%s, cause %d), want 1 (A, DupNonce)", n, who, v2.DuplicateCause())
	}
	if grant(t, v2, now.Add(lease/2), Request{From: "A", Term: 9, Nonce: 0xA, ID: 2}) {
		t.Fatal("a new grant to A while two of its boots are live")
	}
	if !grant(t, v2, now.Add(lease/2+lease+ms), Request{From: "A", Term: 10, Nonce: 0xA, ID: 3}) {
		t.Fatal("still refused the real A a lease after the clone fell silent")
	}
}

// Rule (b), shape 3: W heard B's voter relay its promise to A. A request
// from B while that runs is a clone of B (a real B waits out its own
// voter's promise before it campaigns): refused and counted, with its
// cause. A is unaffected.
func TestVoterRefusesANameWhoseOwnVoterIsHeardPromisedElsewhere(t *testing.T) {
	v := ready("W")
	now := at(lease)
	v.Hear(now, "B", 0xF00, Relay{To: "A", Nonce: 0xA, Left: lease})
	if grant(t, v, now.Add(lease/2), Request{From: "B", Term: 9, Nonce: 0xB, ID: 1}) {
		t.Fatal("granted B while B's own voter was heard promised to A: a clone of the standby leads")
	}
	if n, who := v.Duplicates(); n != 1 || who != "B" || v.DuplicateCause() != DupVoterPromised {
		t.Fatalf("duplicates %d (%s, cause %d), want 1 (B, DupVoterPromised)", n, who, v.DuplicateCause())
	}
	if !grant(t, v, now.Add(lease/2), Request{From: "A", Term: 9, Nonce: 0xA, ID: 2}) {
		t.Fatal("refused A, the one B's voter promised")
	}
}

// A heard promise holds until receipt + left·(1+MaxRho): it must outlast
// the original in real time whatever the clocks' rates. A refusal past
// left, inside that margin, counts no duplicate (a fast real restart
// raises nothing); past the margin the name is granted (liveness: a stale
// relay ends).
func TestAHeardPromiseHoldsForItsMarginThenEnds(t *testing.T) {
	v := ready("W")
	now := at(lease)
	v.Hear(now, "B", 0xF00, Relay{To: "A", Nonce: 0xA, Left: lease})
	tail := now.Add(lease + time.Duration(float64(lease)*MaxRho)/2)
	if grant(t, v, tail, Request{From: "B", Term: 9, Nonce: 0xB, ID: 1}) {
		t.Fatal("granted B inside the clock-rate margin after the relayed promise: a clock running fast lets a clone in")
	}
	if n, _ := v.Duplicates(); n != 0 {
		t.Fatalf("a refusal in the margin's tail counted %d duplicates, want 0: a fast real restart would raise a false alarm", n)
	}
	end := now.Add(time.Duration(float64(lease) * (1 + MaxRho)))
	if !grant(t, v, end, Request{From: "B", Term: 9, Nonce: 0xB, ID: 2}) {
		t.Fatal("still refused B after the relayed promise and its margin ran out: a stale relay blocks the takeover")
	}
}

// A relay can't say a promise runs longer than a lease.
func TestAHeardPromiseNeverRunsPastALease(t *testing.T) {
	v := ready("W")
	now := at(lease)
	v.Hear(now, "B", 0xF00, Relay{To: "A", Nonce: 0xA, Left: time.Hour})
	if !grant(t, v, now.Add(time.Duration(float64(lease)*(1+MaxRho))), Request{From: "B", Term: 9, Nonce: 0xB, ID: 1}) {
		t.Fatal("a relayed hour held the name past a lease")
	}
}

// Only a promise to another node is relayed. A voter promised to its own
// candidate (the witness-down election, or a clone's self-grant) relays
// nothing, so a clone's frames can't refuse the original anywhere.
func TestVoterRelaysOnlyAPromiseToAnotherNode(t *testing.T) {
	v := ready("A")
	now := at(lease)
	if !grant(t, v, now, Request{From: "A", Term: 3, Nonce: 0xA, ID: 1}) {
		t.Fatal("self grant refused")
	}
	if r := v.Relay(now.Add(lease / 4)); r != (Relay{}) {
		t.Fatalf("relayed a promise to its own candidate: %+v", r)
	}
	w := ready("W")
	if !grant(t, w, now, Request{From: "A", Term: 3, Nonce: 0xA, ID: 1}) {
		t.Fatal("grant refused")
	}
	if r := w.Relay(now.Add(lease / 4)); r.To != "A" || r.Nonce != 0xA || r.Left != lease-lease/4 {
		t.Fatalf("relay %+v, want A under 0xA with %v left", r, lease-lease/4)
	}
	if r := w.Relay(now.Add(lease)); r != (Relay{}) {
		t.Fatalf("relayed a promise that has run out: %+v", r)
	}
}

// A relay naming a name under another boot than the voter's own running
// promise, or than another voter's relay, is a duplicate's sign, counted.
// The voter's own running promise still renews; a new grant goes to
// neither boot while both are live (#1029 step 1).
func TestARelayConflictIsADuplicate(t *testing.T) {
	now := at(lease)
	own := ready("W")
	if !grant(t, own, now, Request{From: "A", Term: 3, Nonce: 0xA, ID: 1}) {
		t.Fatal("grant refused")
	}
	own.Hear(now, "B", 0xF00, Relay{To: "A", Nonce: 0xC, Left: lease})
	if !grant(t, own, now.Add(lease/4), Request{From: "A", Term: 3, Nonce: 0xA, ID: 2, Renewal: true}) {
		t.Fatal("a relay of A under another nonce refused the renewal of this voter's own promise to A")
	}
	if n, _ := own.Duplicates(); n == 0 {
		t.Fatal("the conflicting relay counted no duplicate")
	}

	two := ready("B")
	two.Hear(now, "W", 0xF00, Relay{To: "A", Nonce: 0xA, Left: lease})
	two.Hear(now.Add(lease/8), "Z", 0xF00, Relay{To: "A", Nonce: 0xC, Left: lease})
	if n, _ := two.Duplicates(); n == 0 {
		t.Fatal("two relays of A under two boots counted no duplicate")
	}
	for _, nonce := range []uint64{0xA, 0xC} {
		if grant(t, two, now.Add(lease/4), Request{From: "A", Term: 3, Nonce: nonce, ID: nonce}) {
			t.Fatalf("a new grant to A under %#x while two of its boots are live", nonce)
		}
	}
}

// A voter's newer relay about a name replaces its own earlier one (it
// holds one promise at a time); its own frames and a zero relay change
// nothing.
func TestARelayFromTheSameVoterReplacesItsOwn(t *testing.T) {
	v := ready("B")
	now := at(lease)
	v.Hear(now, "W", 0xF00, Relay{To: "A", Nonce: 0xA, Left: lease / 4})
	v.Hear(now.Add(lease/2), "W", 0xF00, Relay{To: "A", Nonce: 0xD, Left: lease}) // A restarted; W granted it
	if !grant(t, v, now.Add(lease/2), Request{From: "A", Term: 4, Nonce: 0xD, ID: 1}) {
		t.Fatal("refused A's new boot, which W's newer relay names")
	}
	v.Hear(now, "B", 0xF00, Relay{To: "A", Nonce: 0xE, Left: lease})
	v.Hear(now, "W", 0xF00, Relay{})
	if !grant(t, v, now.Add(lease/2), Request{From: "A", Term: 4, Nonce: 0xD, ID: 2}) {
		t.Fatal("its own relay or an empty one changed what it holds")
	}
}

// A standby probes the witness once per look, in its restart hold too; a
// leader doesn't. The probe is a term-0 request with id ProbeID, and the
// reply to it is recognisable, so a driver feeds it only to the voter.
func TestStandbyProbesTheWitnessEveryLook(t *testing.T) {
	cfg := Config{Self: "A", Peer: "B", Witness: "W", Lease: lease, Rho: 0.01, Nonce: 0xA}
	c, err := NewCandidate(cfg, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	c.holdUntil = at(lease) // its own voter's restart wait
	p := c.Probe(0)
	if p == nil || p.Term != 0 || p.ID != ProbeID || p.From != "A" || p.Nonce != 0xA {
		t.Fatalf("probe in the hold: %+v", p)
	}
	if c.Probe(at(lease/8)) != nil {
		t.Fatal("probed twice in one look")
	}
	if c.Probe(at(lease/4)) == nil {
		t.Fatal("no probe a look later")
	}
	if !IsRelayProbe(Reply{From: "W", To: "A", Nonce: 0xA, ID: ProbeID}) || IsRelayProbe(Reply{From: "W", To: "A", Nonce: 0xA}) {
		t.Fatal("IsRelayProbe can't tell the relay probe's reply from the handshake's")
	}
	c.leading, c.leaseEnd = true, at(10*lease)
	if c.Probe(at(lease)) != nil {
		t.Fatal("a leader probed: its renewals carry its voter's promise already")
	}
}

// A voter that has just promised the other controller says so once
// (RelayChanged), and its standby candidate pushes a probe to the witness
// at once, whatever the look (PushProbe): the witness hears the new
// promise within a one-way delay, not up to a look later, when a clone
// could win it meanwhile (a clone booted while the pair elects).
func TestAPromiseToTheOtherControllerIsPushedToTheWitness(t *testing.T) {
	v := ready("B")
	now := at(lease)
	if !grant(t, v, now, Request{From: "B", Term: 2, Nonce: 0xB, ID: 1}) || v.RelayChanged(now) {
		t.Fatal("a promise to its own candidate reported as one to push")
	}
	if !grant(t, v, now.Add(lease+ms), Request{From: "A", Term: 3, Nonce: 0xA, ID: 1}) {
		t.Fatal("grant refused")
	}
	if !v.RelayChanged(now.Add(lease + ms)) {
		t.Fatal("a new promise to A not reported")
	}
	if !grant(t, v, now.Add(lease+lease/4), Request{From: "A", Term: 3, Nonce: 0xA, ID: 2, Renewal: true}) || v.RelayChanged(now.Add(lease+lease/4)) {
		t.Fatal("a renewal of the same promise reported again")
	}
	cfg := Config{Self: "B", Peer: "A", Witness: "W", Lease: lease, Rho: 0.01, Nonce: 0xB}
	c, err := NewCandidate(cfg, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if c.Probe(0) == nil {
		t.Fatal("no first probe")
	}
	if p := c.PushProbe(at(ms)); p == nil || p.ID != ProbeID || p.Term != 0 {
		t.Fatalf("push inside the look: %+v, want a probe now", p)
	}
}

// #1029 step 1, at the voter.

// A name heard under two live boots gets no NEW grant, to either boot,
// and the voter counts a duplicate; a running promise to one of those
// boots still renews (the incumbent keeps the promises that protect it).
// A lease after the other boot fell silent, the name is granted again.
func TestADuplicatedNameGetsNoNewGrant(t *testing.T) {
	v := ready("W")
	now := at(lease)
	if !grant(t, v, now, Request{From: "A", Term: 3, Nonce: 0xA, ID: 1}) {
		t.Fatal("grant refused")
	}
	if grant(t, v, now.Add(lease/8), Request{From: "A", Term: 4, Nonce: 0xC, ID: 1}) {
		t.Fatal("granted A's second boot while the first holds a running promise")
	}
	if n, _ := v.Duplicates(); n == 0 {
		t.Fatal("no duplicate counted")
	}
	if !grant(t, v, now.Add(lease/4), Request{From: "A", Term: 3, Nonce: 0xA, ID: 2, Renewal: true}) {
		t.Fatal("the incumbent's running promise didn't renew")
	}
	fresh := ready("B")
	fresh.Handle(now, Request{From: "A", Nonce: 0xA, ID: ProbeID}) // a probe: A runs as 0xA
	if grant(t, fresh, now.Add(lease/8), Request{From: "A", Term: 4, Nonce: 0xC, ID: 1}) {
		t.Fatal("a new grant to A's second boot while its first is live")
	}
	if grant(t, fresh, now.Add(lease/8), Request{From: "A", Term: 4, Nonce: 0xA, ID: 2}) {
		t.Fatal("a new grant to A's first boot while its second is live")
	}
	if !grant(t, fresh, now.Add(lease/8+lease+ms), Request{From: "A", Term: 5, Nonce: 0xA, ID: 3}) {
		t.Fatal("still refused A a lease after its other boot fell silent")
	}
}

// A voter relays a name it sees duplicated first-hand; a hearer then
// refuses new grants to that name though it heard only one boot itself.
// A name only relayed to it is not relayed on (no echo).
func TestADuplicatedNameIsRelayed(t *testing.T) {
	now := at(lease)
	b := ready("B")
	b.Handle(now, Request{From: "A", Nonce: 0xA, ID: ProbeID})
	b.Handle(now, Request{From: "A", Nonce: 0xC, ID: ProbeID})
	r := b.Relay(now.Add(ms))
	if r.Dup != "A" || r.DupLeft <= 0 {
		t.Fatalf("relay %+v, want A duplicated", r)
	}
	w := ready("W")
	w.Hear(now.Add(ms), "B", 0xF00, r)
	if grant(t, w, now.Add(lease/4), Request{From: "A", Term: 2, Nonce: 0xC, ID: 1}) {
		t.Fatal("a new grant to a name another voter relayed duplicated")
	}
	if !w.Duplicated(now.Add(lease/4), "A") {
		t.Fatal("the hearer doesn't know A is duplicated")
	}
	if e := w.Relay(now.Add(lease / 4)); e.Dup != "" {
		t.Fatalf("the hearer relays on what it only heard: %+v", e)
	}
}

// A real restart's old boot, alive only in a promise another voter
// relays, refuses the new boot's first campaign in the clock-rate margin
// after that promise but counts no duplicate: a fast restart (its hold is
// a lease, so it campaigns once the old promise is over) raises no false
// redundancy.duplicate-member.
func TestAnOldBootSeenOnlyInARelayCountsNoDuplicate(t *testing.T) {
	v := ready("W")
	now := at(lease)
	v.Hear(now, "B", 0xF00, Relay{To: "A", Nonce: 0xA, Left: lease})
	// A restarted as 0xD and campaigns as its hold ends, just past B's
	// promise to its old boot.
	if grant(t, v, now.Add(lease+lease/20), Request{From: "A", Term: 4, Nonce: 0xD, ID: 1}) {
		t.Fatal("granted A's new boot inside the margin of B's relayed promise to its old one")
	}
	if n, _ := v.Duplicates(); n != 0 {
		t.Fatalf("a real restart counted %d duplicates", n)
	}
}

// A candidate counts no grant from a voter its own voter knows runs as two
// boots, its own included (NewAdoption wires it): two processes cast that
// vote.
func TestCandidateDiscountsADuplicatedVoter(t *testing.T) {
	cfg := Config{Self: "B", Peer: "A", Witness: "W", Lease: lease, Rho: 0.01, Incarnation: 42, Nonce: 0xB}
	c, err := NewCandidate(cfg, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	v := NewVoter("B", lease, VoterState{Incarnation: 42}, 0)
	NewAdoption(v, c, 0)
	now := at(2 * lease) // past the restart hold and the quiet period
	v.Handle(now, Request{From: "A", Nonce: 0xA, ID: ProbeID})
	v.Handle(now, Request{From: "A", Nonce: 0xC, ID: ProbeID})
	r := c.Tick(now, "", 0, true)
	if r == nil {
		t.Fatal("no campaign")
	}
	c.OnReply(now.Add(ms), granted("B", r, 0))
	c.OnReply(now.Add(ms), granted("A", r, 0))
	if c.Leading(now.Add(ms)) {
		t.Fatal("led on its own vote and a duplicated voter's")
	}
	c.OnReply(now.Add(2*ms), granted("W", r, 42))
	if !c.Leading(now.Add(2 * ms)) {
		t.Fatal("no lease from its own vote and the witness's")
	}
}

// A voter promised to its own candidate doesn't yield it to the other
// controller's higher term when it would refuse that request anyway (a
// duplicated name): yielding would only drop its own round (#1079 review).
func TestNoYieldToARequestTheVoterRefuses(t *testing.T) {
	cfg := Config{Self: "B", Peer: "A", Witness: "W", Lease: lease, Rho: 0.01, Nonce: 0xB}
	c, err := NewCandidate(cfg, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	v := ready("B")
	now := at(lease)
	r := c.Tick(now, "", 0, true)
	if !grant(t, v, now, *r) {
		t.Fatal("self grant refused")
	}
	v.Handle(now, Request{From: "A", Nonce: 0xA, ID: ProbeID})
	v.Handle(now, Request{From: "A", Nonce: 0xC, ID: ProbeID})
	if YieldTo(now.Add(ms), v, c, Request{From: "A", Term: r.Term + 1, Nonce: 0xC, ID: 1}) {
		t.Fatal("yielded its own round to a request it refuses")
	}
}

// Rule (b) refuses a request from X while X's own voter is heard promised
// elsewhere, but counts a duplicate only when that relay came from another
// boot of X: one boot's own frames can arrive out of order (its campaign
// overtaken by the push probe of the promise its voter made just after),
// which is no second process (#1079 review).
func TestRuleBCountsOnlyAnotherBoot(t *testing.T) {
	now := at(lease)
	same := ready("W")
	same.Hear(now, "B", 0xB, Relay{To: "A", Nonce: 0xA, Left: lease})
	if grant(t, same, now.Add(lease/4), Request{From: "B", Term: 9, Nonce: 0xB, ID: 1}) {
		t.Fatal("granted B while its own voter was heard promised to A")
	}
	if n, _ := same.Duplicates(); n != 0 {
		t.Fatalf("a reordered frame of B's own boot counted %d duplicates", n)
	}
	other := ready("W")
	other.Hear(now, "B", 0xB, Relay{To: "A", Nonce: 0xA, Left: lease})
	if grant(t, other, now.Add(lease/4), Request{From: "B", Term: 9, Nonce: 0xC, ID: 1}) {
		t.Fatal("granted another boot of B while B's voter was heard promised to A")
	}
	if n, _ := other.Duplicates(); n != 1 {
		t.Fatalf("another boot of B counted %d duplicates, want 1", n)
	}
}

// A term granted to one boot of a name goes to no other boot of it, even
// once that promise has run out: a term is one process's (#1029 step 1).
// The other boot is told the term, and asks above it.
func TestATermIsNotGrantedToAnotherBoot(t *testing.T) {
	v := ready("A")
	now := at(lease)
	if !grant(t, v, now, Request{From: "B", Term: 30, Nonce: 0xC, ID: 1}) {
		t.Fatal("grant refused")
	}
	later := now.Add(2 * lease) // the promise has run out
	p, out := v.Handle(later, Request{From: "B", Term: 30, Nonce: 0xB, ID: 2})
	if p != nil || len(out) != 1 || out[0].Granted || out[0].Promised != 30 {
		t.Fatalf("term 30 again, to another boot of B: %+v %+v", p, out)
	}
	if !grant(t, v, later, Request{From: "B", Term: 31, Nonce: 0xB, ID: 3}) {
		t.Fatal("refused the other boot the next term")
	}
}

// One term, one boot, across the voter's restart: the grant's boot nonce
// is in the state file, so a voter restarted and past its wait still
// refuses a term it gave one boot of a name to another boot of it (the
// #1094 review's probe: grant term 30 to B under C, restart, wait three
// leases, ask term 30 under B). The boot it was granted to still renews.
func TestATermStaysOneBootsAcrossARestart(t *testing.T) {
	v := ready("A")
	now := at(lease)
	p, out := v.Handle(now, Request{From: "B", Term: 30, Nonce: 0xC, ID: 1})
	if p == nil || len(out) != 0 {
		t.Fatalf("the grant changes the state: %+v %+v", p, out)
	}
	file, _ := p.State.MarshalBinary()
	var st VoterState
	if err := st.UnmarshalBinary(file); err != nil {
		t.Fatal(err)
	}
	later := now.Add(lease)
	r := NewVoter("A", lease, st, later)
	if grant(t, r, later.Add(3*lease), Request{From: "B", Term: 30, Nonce: 0xB, ID: 2}) {
		t.Fatal("after a restart, term 30 went to a second boot of B")
	}
	r2 := NewVoter("A", lease, st, later) // (r now hears B under two boots, and refuses both)
	if !grant(t, r2, later.Add(3*lease), Request{From: "B", Term: 30, Nonce: 0xC, ID: 3}) {
		t.Fatal("after a restart, the boot term 30 was granted to was refused it")
	}
}
