package quorum

import (
	"testing"
	"time"
)

// A planned switchover's RESIGN (#710 d1, resign.go).

// deliver hands r to voter vid as a driver would (Observe, YieldTo,
// Handle, instant fsync), and its replies to the candidates. It reports
// whether vid granted (took) r.
func (p *pair) deliver(now Instant, vid NodeID, r Request) bool {
	if c := p.c[vid]; c != nil && r.From != vid {
		c.Observe(r.Term)
		YieldTo(now, p.v[vid], c, r)
	}
	ps, reps := p.v[vid].Handle(now, r)
	if ps != nil {
		reps = append(reps, p.v[vid].Persisted(ps.Seq, true)...)
	}
	took := false
	for _, rep := range reps {
		took = took || rep.Granted
		if c := p.c[rep.To]; c != nil && !p.down[rep.To] {
			c.OnReply(now, rep)
		}
	}
	return took
}

// settledA starts A, then B under A's lease, and runs until B's voter
// grants A's renewals (as TestStandbyRejoinsTheLeader's standby restart).
func settledA(t *testing.T) (*pair, Instant) {
	p := newPair(t)
	p.start("A", 0)
	now := Instant(0)
	for ; now < at(9*time.Second); now += step {
		if now == at(2500*time.Millisecond) { // after A has won
			p.start("B", now)
		}
		p.step(now)
	}
	if p.leader(now) != "A" || p.v["B"].State().GrantedTo != "A" {
		t.Fatalf("not settled: leader %q, B's voter promised to %q", p.leader(now), p.v["B"].State().GrantedTo)
	}
	return p, now
}

// switchAt resigns A at now: ResignAt, ResignDrained, then the RESIGN to the witness
// first and to B's voter, as the elector sends it. A renewal A sent just
// before (in flight) lands at both after the RESIGN.
func switchAt(t *testing.T, p *pair, now Instant, to ...NodeID) Request {
	t.Helper()
	inflight := p.c["A"].Tick(now, "", 0, true)
	if inflight == nil {
		// No look due: a renewal as A would send it.
		p.c["A"].nextLook = now
		inflight = p.c["A"].Tick(now, "", 0, true)
	}
	if inflight == nil || !inflight.Renewal {
		t.Fatalf("A sent no renewal before resigning: %+v", inflight)
	}
	r := ResignAt(now, p.v["A"], p.c["A"], 77, true)
	if r == nil || !r.Resign || r.Seq <= inflight.Seq || r.Epoch != p.c["A"].epoch || r.MarkSeq != 77 || !r.MarkCurrent {
		t.Fatalf("the RESIGN: %+v (the renewal before it had Seq %d)", r, inflight.Seq)
	}
	if p.c["A"].Leading(now) {
		t.Fatal("A still leads after resigning")
	}
	ResignDrained(now, p.v["A"], p.c["A"], r) // the outputs drained (#710 d1b)
	for _, vid := range to {
		if !p.deliver(now, vid, *r) {
			t.Fatalf("%s refused the RESIGN", vid)
		}
		if p.deliver(now, vid, *inflight) {
			t.Fatalf("%s granted a renewal of the leadership that resigned", vid)
		}
	}
	return *r
}

// After a RESIGN the standby wins within a few looks, not after the
// lease; A neither renews nor campaigns in its hold; and the voters keep
// the RESIGN's mark (the final image's acknowledgement) as A's.
// resign_keeps_renewal_seen, resign_renewal_granted and resign_no_hold
// turn it red.
func TestResignHandsOverWithoutWaitingTheLease(t *testing.T) {
	p, now := settledA(t)
	r := switchAt(t, p, now, "W", "B")
	start := now
	for ; now < start.Add(ResignHold(lease)); now += step {
		p.step(now)
		if p.leader(now) == "B" {
			break
		}
		if p.c["A"].VoteTerm() != r.Term {
			t.Fatalf("A campaigned at %v, inside its hold", time.Duration(now-start))
		}
	}
	if took := time.Duration(now - start); p.leader(now) != "B" || took > lease/4 {
		t.Fatalf("B leads %v after the RESIGN (leader %q), want under lease/4 = %v", took, p.leader(now), lease/4)
	}
	for _, vid := range []NodeID{"W", "B"} {
		if m := p.v[vid].Marks("A"); m != (Mark{Known: true, Current: true, Term: r.Epoch, Scan: 77}) {
			t.Fatalf("%s's mark for A: %+v, want the RESIGN's", vid, m)
		}
	}
	// A holds off for the whole hold while B leads.
	for end := start.Add(ResignHold(lease)); now < end; now += step {
		p.step(now)
		if p.c["A"].VoteTerm() != r.Term {
			t.Fatalf("A campaigned at %v, inside its hold", time.Duration(now-start))
		}
	}
}

// With the witness down, A's own voter is the one B needs. ResignAt drops
// its promise to A in the same call; B's higher campaign would also make
// it yield (YieldTo), so this is the path a two-voter switchover takes.
func TestResignDropsTheLeadersOwnPromise(t *testing.T) {
	p, now := settledA(t)
	p.down["W"] = true
	// A and B alone keep A's lease (TestStandbyRejoinsTheLeader).
	for end := now.Add(time.Second); now < end; now += step {
		p.step(now)
	}
	if p.leader(now) != "A" {
		t.Fatal("A lost its lease with the witness down")
	}
	switchAt(t, p, now, "B")
	start := now
	for ; now < start.Add(lease) && p.leader(now) != "B"; now += step {
		p.step(now)
	}
	if took := time.Duration(now - start); p.leader(now) != "B" || took > lease/4 {
		t.Fatalf("B leads %v after the RESIGN (leader %q), want under lease/4", took, p.leader(now))
	}
}

// A voter takes a RESIGN only from the boot it promised, for the epoch it
// recorded for that boot, while the promise runs; anything else changes
// nothing, mark included. A duplicate is taken again with no change.
// resign_ignores_epoch turns it red.
func TestResignMatchesTheLeadershipItEnds(t *testing.T) {
	v := ready("W")
	now := at(lease)
	if !grant(t, v, now, Request{From: "A", Term: 5, Nonce: 0xA, ID: 1}) {
		t.Fatal("A's campaign refused")
	}
	now = now.Add(100 * time.Millisecond)
	if !grant(t, v, now, Request{From: "A", Term: 6, Nonce: 0xA, ID: 2, Seq: 2, Renewal: true, Epoch: 5, MarkSeq: 3}) {
		t.Fatal("A's re-term refused")
	}
	base := Request{From: "A", Term: 6, Nonce: 0xA, ID: 3, Seq: 3, Resign: true, Epoch: 5, MarkSeq: 9, MarkCurrent: true}
	for _, bad := range []struct {
		name string
		mod  func(*Request)
	}{
		{"the re-term's term as the epoch", func(r *Request) { r.Epoch = 6 }},
		{"an earlier epoch", func(r *Request) { r.Epoch = 4 }},
		{"another boot", func(r *Request) { r.Nonce = 0xAA }},
		{"another name", func(r *Request) { r.From = "B"; r.Nonce = 0xB }},
	} {
		r := base
		bad.mod(&r)
		if rv(t, v, now, r).Granted {
			t.Fatalf("%s: RESIGN taken", bad.name)
		}
	}
	if _, to, until := v.PromiseUntil(); to != "A" || now >= until {
		t.Fatal("a refused RESIGN dropped the promise")
	}
	if m := v.Marks("A"); m.Scan != 3 {
		t.Fatalf("a refused RESIGN changed A's mark: %+v", m)
	}
	if rv(t, v, now, Request{From: "B", Term: 7, Nonce: 0xB, ID: 1}).Granted {
		t.Fatal("B granted under A's running promise")
	}
	if !rv(t, v, now, base).Granted {
		t.Fatal("A's RESIGN refused")
	}
	if m := v.Marks("A"); m != (Mark{Known: true, Current: true, Term: 5, Scan: 9}) {
		t.Fatalf("A's mark after its RESIGN: %+v", m)
	}
	if !rv(t, v, now, base).Granted {
		t.Fatal("a duplicated RESIGN refused")
	}
	// A renewal of the resigned leadership: refused, unseen, and its mark
	// (older) is not kept.
	late := Request{From: "A", Term: 6, Nonce: 0xA, ID: 4, Seq: 2, Renewal: true, Epoch: 5, MarkSeq: 3}
	if rv(t, v, now, late).Granted {
		t.Fatal("a renewal of the resigned leadership granted")
	}
	if from, until := v.LeaderSeen(); now < until {
		t.Fatalf("the voter sees %q leading until %v after its RESIGN", from, time.Duration(until-now))
	}
	if m := v.Marks("A"); m.Scan != 9 {
		t.Fatalf("a late renewal brought back an older mark: %+v", m)
	}
	if !grant(t, v, now, Request{From: "B", Term: 7, Nonce: 0xB, ID: 2}) {
		t.Fatal("B refused after A's RESIGN")
	}
}

// The promises a voter heard relayed to the resigned boot go with the
// RESIGN, and a stale relay of one (made before its sender took the
// RESIGN) is not heard again inside the quiet. Here the standby's own
// voter had promised A, so the witness would refuse the standby's
// campaign (heardRefuses rule (b)). resign_keeps_heard and
// resign_relay_heard turn it red.
func TestResignClearsAndIgnoresRelaysToTheResignedBoot(t *testing.T) {
	v := ready("W")
	now := at(lease)
	if !grant(t, v, now, Request{From: "A", Term: 5, Nonce: 0xA, ID: 1}) {
		t.Fatal("A's campaign refused")
	}
	stale := Relay{To: "A", Nonce: 0xA, Left: lease}
	v.Hear(now, "B", 0xB, stale)
	if !rv(t, v, now, Request{From: "A", Term: 5, Nonce: 0xA, ID: 2, Seq: 2, Resign: true, Epoch: 5}).Granted {
		t.Fatal("A's RESIGN refused")
	}
	if rv(t, v, now, Request{From: "B", Term: 6, Nonce: 0xB, ID: 1}).Granted == false {
		t.Fatal("B's campaign refused: the relayed promise to A outlived the RESIGN")
	}
	w := ready("W")
	if !grant(t, w, now, Request{From: "A", Term: 5, Nonce: 0xA, ID: 1}) {
		t.Fatal("A's campaign refused")
	}
	if !rv(t, w, now, Request{From: "A", Term: 5, Nonce: 0xA, ID: 2, Seq: 2, Resign: true, Epoch: 5}).Granted {
		t.Fatal("A's RESIGN refused")
	}
	w.Hear(now.Add(time.Millisecond), "B", 0xB, stale) // made before B's voter took it
	if !rv(t, w, now.Add(2*time.Millisecond), Request{From: "B", Term: 6, Nonce: 0xB, ID: 1}).Granted {
		t.Fatal("B's campaign refused: a stale relay of a promise to A was heard after A resigned")
	}
	// After the quiet a relay to that boot is a new promise (A campaigned
	// again after its hold), and is heard.
	x := ready("W")
	if !grant(t, x, now, Request{From: "A", Term: 5, Nonce: 0xA, ID: 1}) {
		t.Fatal("A's campaign refused")
	}
	if !rv(t, x, now, Request{From: "A", Term: 5, Nonce: 0xA, ID: 2, Seq: 2, Resign: true, Epoch: 5}).Granted {
		t.Fatal("A's RESIGN refused")
	}
	later := now.Add(resignQuiet(lease))
	x.Hear(later, "B", 0xB, stale)
	if rv(t, x, later, Request{From: "B", Term: 6, Nonce: 0xB, ID: 1}).Granted {
		t.Fatal("a relay after the quiet was ignored")
	}
}

// A candidate that does not lead has nothing to resign, and its voter's
// promise stays.
func TestResignWithoutALeadershipIsNothing(t *testing.T) {
	v := ready("A")
	c := newC(t, cfgA(), 0, 0)
	if r := ResignAt(at(lease), v, c, 1, true); r != nil {
		t.Fatalf("a standby resigned: %+v", r)
	}
}

// A RESIGN the standby never acts on (it is down, or the RESIGN to it is
// lost): the old leader holds off campaigning for the whole hold, then
// leads again, cold, in a new epoch. resign_no_hold turns it red.
func TestAResignedLeaderHoldsOffThenLeadsAgain(t *testing.T) {
	p, now := settledA(t)
	p.down["B"] = true
	r := switchAt(t, p, now, "W")
	start := now
	for end := start.Add(ResignHold(lease)); now < end; now += step {
		p.step(now)
		if p.c["A"].VoteTerm() != r.Term || p.leader(now) != "" {
			t.Fatalf("A campaigned or led at %v, inside its %v hold", time.Duration(now-start), ResignHold(lease))
		}
	}
	for end := now.Add(lease); now < end && p.leader(now) != "A"; now += step {
		p.step(now)
	}
	if p.leader(now) != "A" || p.c["A"].Term() <= r.Epoch {
		t.Fatalf("after its hold A leads %v in epoch %d, want a new epoch above %d", p.leader(now) == "A", p.c["A"].Term(), r.Epoch)
	}
}

// The resigned leader's own voter heard the standby's voter relay its
// promise to the leader (it granted the leader's renewals). ResignAt drops
// that relay and ignores stale ones for the quiet, so the leader's voter
// grants the standby's campaign at once (heardRefuses rule (b) would
// refuse it for up to a lease). resign_own_keeps_heard turns it red.
func TestResignClearsRelaysAtTheLeadersOwnVoter(t *testing.T) {
	p, now := settledA(t)
	relay := Relay{To: "A", Nonce: p.c["A"].cfg.Nonce, Left: lease}
	p.v["A"].Hear(now, "B", p.c["B"].cfg.Nonce, relay)
	ResignDrained(now, p.v["A"], p.c["A"], ResignAt(now, p.v["A"], p.c["A"], 1, true))
	p.v["A"].Hear(now.Add(time.Millisecond), "B", p.c["B"].cfg.Nonce, relay) // in flight before B's voter took it
	camp := Request{From: "B", Term: p.c["B"].VoteTerm() + 10, Nonce: p.c["B"].cfg.Nonce, ID: 999, Seq: 1 << 40}
	if !rv(t, p.v["A"], now.Add(2*time.Millisecond), camp).Granted {
		t.Fatal("the resigned leader's own voter refused the standby's campaign")
	}
}

// A renewal round A sent before it resigned is granted late, after its
// resign hold ended early (its peer handed the lead back): the grants
// match no round, so A does not lead on promises the voters dropped.
// resign_keeps_rounds turns it red.
func TestALateRenewalGrantDoesNotReLeadAfterTheHoldEnds(t *testing.T) {
	p, now := settledA(t)
	c := p.c["A"]
	c.nextLook = now
	ren := c.Tick(now, "", 0, true)
	if ren == nil || !ren.Renewal {
		t.Fatalf("no renewal: %+v", ren)
	}
	if ResignAt(now, p.v["A"], c, 1, true) == nil {
		t.Fatal("A did not resign")
	}
	later := now.Add(5 * time.Millisecond)
	if !c.EndResignHold(later) {
		t.Fatal("no resign hold to end")
	}
	c.OnReply(later, granted("A", ren, 0))
	c.OnReply(later, granted("W", ren, 42))
	if c.Leading(later) {
		t.Fatal("a late grant of a renewal sent before the resign made A lead again")
	}
}

// A long-delayed RESIGN of an earlier leadership reaches a voter in the
// window where the boot's later leadership's first renewal is granted but
// not yet durable (so its mark, the epoch the voter knows, is still the
// old one): the voter heard that renewal's higher Seq, and refuses.
// resign_ignores_seq turns it red.
func TestAResignOlderThanARenewalHeardIsRefused(t *testing.T) {
	v := ready("W")
	now := at(lease)
	if !grant(t, v, now, Request{From: "A", Term: 5, Nonce: 0xA, ID: 1, Seq: 1}) {
		t.Fatal("A's campaign refused")
	}
	now = now.Add(100 * time.Millisecond)
	if !grant(t, v, now, Request{From: "A", Term: 5, Nonce: 0xA, ID: 2, Seq: 2, Renewal: true, Epoch: 5, MarkSeq: 3}) {
		t.Fatal("A's renewal refused")
	}
	// A resigned epoch 5 (Seq 3, lost on the way here), and led again in
	// epoch 9, whose campaign this voter missed; its first renewal here
	// moves the term, so the grant waits on a write.
	p, out := v.Handle(now, Request{From: "A", Term: 9, Nonce: 0xA, ID: 7, Seq: 7, Renewal: true, Epoch: 9})
	if p == nil || len(out) != 0 {
		t.Fatalf("the re-term's grant did not wait on its write: %v %+v", p, out)
	}
	if rv(t, v, now, Request{From: "A", Term: 5, Nonce: 0xA, ID: 3, Seq: 3, Resign: true, Epoch: 5}).Granted {
		t.Fatal("a RESIGN of epoch 5 took the promise that backs epoch 9")
	}
}

// After a quick switchover back the old leader leads again within the
// relay quiet: once this voter grants that boot's campaign, relays of
// promises to it are new ones, and heard. resign_quiet_outlives_grant
// turns it red.
func TestTheRelayQuietEndsWhenTheBootIsGrantedAgain(t *testing.T) {
	v := ready("W")
	now := at(lease)
	if !grant(t, v, now, Request{From: "A", Term: 5, Nonce: 0xA, ID: 1, Seq: 1}) {
		t.Fatal("A's campaign refused")
	}
	if !rv(t, v, now, Request{From: "A", Term: 5, Nonce: 0xA, ID: 2, Seq: 2, Resign: true, Epoch: 5}).Granted {
		t.Fatal("A's RESIGN refused")
	}
	if !grant(t, v, now.Add(time.Millisecond), Request{From: "B", Term: 6, Nonce: 0xB, ID: 1, Seq: 1}) {
		t.Fatal("B's campaign refused")
	}
	// B resigns back; A campaigns again (same boot) and this voter grants it.
	soon := now.Add(50 * time.Millisecond)
	if !rv(t, v, soon, Request{From: "B", Term: 6, Nonce: 0xB, ID: 2, Seq: 2, Resign: true, Epoch: 6}).Granted {
		t.Fatal("B's RESIGN refused")
	}
	if !grant(t, v, soon.Add(time.Millisecond), Request{From: "A", Term: 7, Nonce: 0xA, ID: 3, Seq: 3}) {
		t.Fatal("A's new campaign refused")
	}
	// B's voter promises A now, and relays it: heard, so B (whose own
	// voter is promised elsewhere) gets no grant here (rule (b)).
	v.Hear(soon.Add(2*time.Millisecond), "B", 0xB, Relay{To: "A", Nonce: 0xA, Left: lease})
	if rv(t, v, soon.Add(lease+10*time.Millisecond), Request{From: "B", Term: 9, Nonce: 0xB, ID: 3, Seq: 3}).Granted {
		t.Fatal("a relay of a new promise to A, after this voter granted A again, was ignored")
	}
}

// A RESIGN that arrives after the voter's promise to that boot ran out is
// refused: there is nothing of that leadership to drop, and taking it
// would keep a stale mark and quiet relays for nothing.
// resign_after_promise turns it red.
func TestAResignAfterThePromiseRanOutIsRefused(t *testing.T) {
	v := ready("W")
	now := at(lease)
	if !grant(t, v, now, Request{From: "A", Term: 5, Nonce: 0xA, ID: 1, Seq: 1}) {
		t.Fatal("A's campaign refused")
	}
	late := now.Add(lease + time.Millisecond)
	if rv(t, v, late, Request{From: "A", Term: 5, Nonce: 0xA, ID: 2, Seq: 2, Resign: true, Epoch: 5, MarkSeq: 9}).Granted {
		t.Fatal("a RESIGN taken after the promise ran out")
	}
	if v.ResignTook() {
		t.Fatal("the voter says it took a RESIGN it refused")
	}
}

// leadSeq keeps a name's boots in the order last heard: a live boot that
// keeps sending is never the one a full list drops.
func TestLeadSeqDropsTheBootHeardLongestAgo(t *testing.T) {
	v := ready("W")
	v.noteLeadSeq(Request{From: "A", Term: 5, Nonce: 0xA, Seq: 7})
	for i := uint64(1); i <= 2*maxLeadSeqBoots; i++ {
		v.noteLeadSeq(Request{From: "A", Term: 5, Nonce: 0x100 + i, Seq: 1})
		v.noteLeadSeq(Request{From: "A", Term: 5, Nonce: 0xA, Seq: 3}) // the live boot, again
	}
	if got := v.leadSeqOf("A", 0xA); got != 7 {
		t.Fatalf("the live boot's Seq: %d, want 7", got)
	}
}

// #710 d1b (Fable): between the resign and its drain, sends the lease let
// start may still be on the wire, so the resigner's own voter keeps its
// promise and does not yield it. B's voter heard none of A's renewals
// (its own promise is free), so with A's voter's promise freed B would
// lead beside those sends. Only once the drain is proven (ResignDrained)
// does it grant B; a drain that never ends frees it where A's lease would
// have ended: a failover. resign_frees_own_before_drain turns it red.
func TestTheOwnVoterKeepsItsPromiseUntilTheDrain(t *testing.T) {
	p, now := settledA(t)
	lend := p.c["A"].leaseEnd
	r := ResignAt(now, p.v["A"], p.c["A"], 1, true)
	if r == nil {
		t.Fatal("A did not resign")
	}
	camp := Request{From: "B", Term: p.c["B"].VoteTerm() + 10, Nonce: p.c["B"].cfg.Nonce, ID: 999, Seq: 1 << 40}
	if p.deliver(now.Add(time.Millisecond), "A", camp) {
		t.Fatal("A's own voter granted B's campaign before A's outputs were drained")
	}
	if lend <= now.Add(2*time.Millisecond) {
		t.Fatal("A's lease was already over")
	}
	// Undrained, the promise runs where the lease would have ended.
	camp.ID, camp.Seq = 1000, camp.Seq+1
	if p.deliver(lend.Add(-time.Millisecond), "A", camp) {
		t.Fatal("A's own voter granted B's campaign inside the lease A resigned, its drain unproven")
	}
	camp.ID, camp.Seq = 1001, camp.Seq+1
	if !p.deliver(lend.Add(lease), "A", camp) {
		t.Fatal("A's own voter still refuses B's campaign a lease after A's lease ended")
	}

	p2, now2 := settledA(t)
	r2 := ResignAt(now2, p2.v["A"], p2.c["A"], 1, true)
	ResignDrained(now2, p2.v["A"], p2.c["A"], r2)
	camp2 := Request{From: "B", Term: p2.c["B"].VoteTerm() + 10, Nonce: p2.c["B"].cfg.Nonce, ID: 999, Seq: 1 << 40}
	if !p2.deliver(now2.Add(time.Millisecond), "A", camp2) {
		t.Fatal("A's own voter refused B's campaign after the drain")
	}
}

// Fable re-check (#710 d1b): the resigner stalls after its drain past its
// resign hold and campaigns again; its own voter's promise now backs that
// campaign. A late ResignDrained must free nothing (and report false, so
// no RESIGN leaves), or both controllers could lead. A ResignDrained in
// time frees the promise. resign_drained_unchecked turns it red.
func TestALateDrainFreesNothing(t *testing.T) {
	p, now := settledA(t)
	lend := p.c["A"].leaseEnd
	r := ResignAt(now, p.v["A"], p.c["A"], 1, true)
	if r == nil {
		t.Fatal("A did not resign")
	}
	before := p.v["A"].promiseUntil
	if ResignDrained(lend, p.v["A"], p.c["A"], r) {
		t.Fatal("a drain that ended at the resigned lease's end freed the own voter's promise")
	}
	if p.v["A"].promiseUntil != before || p.v["A"].st.GrantedTo != "A" {
		t.Fatalf("a late drain changed A's own voter: promise until %v (was %v), to %q", p.v["A"].promiseUntil, before, p.v["A"].st.GrantedTo)
	}

	p2, now2 := settledA(t)
	r2 := ResignAt(now2, p2.v["A"], p2.c["A"], 1, true)
	if !ResignDrained(now2.Add(time.Millisecond), p2.v["A"], p2.c["A"], r2) || p2.v["A"].promiseUntil != 0 {
		t.Fatal("a drain in time did not free the own voter's promise")
	}
}
