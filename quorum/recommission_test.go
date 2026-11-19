package quorum

import (
	"testing"
	"time"
)

// The two-of-three case (design §3.1, §7): B committed (1, I), I died
// for good (the witness wiped), and B pends a switch to the rebuilt
// witness's J. A lost its file. A may have led terms on {A, I} that B
// never saw, and only I knew them: B's floor misses them. A must not take
// B's switch (nor a pending one) to re-commission, or it would vote again
// in terms it led; it stays uncommissioned, grants nothing and raises
// EventRecommission. Red under uncommissioned_takes_switch.
func TestUncommissionedRefusesASwitch(t *testing.T) {
	b := newCtl(t, "B", commissioned(1, incI, 9), 0)
	now := at(2 * lease)
	b.hear(now, incJ, 0)
	b.tick(now) // B pends (2, J)
	if st := b.v.State(); st.PendingGen != 2 || st.PendingInc != incJ {
		t.Fatalf("B does not pend (2, J): %+v", st)
	}
	a := newCtl(t, "A", VoterState{}, now)
	a.hear(now, incJ, 0)
	for k := range 3 {
		tt := now.Add(lease / 4 * time.Duration(k))
		qa, _ := a.tick(tt)
		talk(tt, a, b, qa)
		rs, _ := b.tick(tt)
		talk(tt, b, a, rs)
	}
	if st := a.v.State(); st.Incarnation != 0 || st.Gen != 0 {
		t.Fatalf("A re-commissioned on B's switch: %+v", st)
	}
	if st := b.v.State(); st.Incarnation != incI || st.PendingInc != incJ {
		t.Fatalf("B moved without A: %+v", st)
	}
}

// The same, with the alarm: once A has been up a lease and B's committed
// incarnation I has not answered it, A raises EventRecommission naming
// (1, I), once; not before the lease (the probe may not have come back
// yet), and not when I answers.
func TestUncommissionedAlarmsOnADeadIncarnation(t *testing.T) {
	b := newCtl(t, "B", commissioned(1, incI, 9), 0)
	now := at(2 * lease)
	b.hear(now, incJ, 0)
	b.tick(now)
	a := newCtl(t, "A", VoterState{}, now)
	a.hear(now, incJ, 0)
	round := func(tt Instant) {
		qa, _ := a.tick(tt)
		talk(tt, a, b, qa)
		rs, _ := b.tick(tt)
		talk(tt, b, a, rs)
	}
	round(now)
	if ev := a.a.Events(); len(ev) != 0 {
		t.Fatalf("an alarm before A has been up a lease: %+v", ev)
	}
	for k := range 8 {
		round(now.Add(lease + lease/2*time.Duration(k)))
	}
	ev := a.a.Events()
	if len(ev) != 1 || ev[0] != (Event{Kind: EventRecommission, Gen: 1, Incarnation: incI}) {
		t.Fatalf("want one EventRecommission for (1, I), got %+v", ev)
	}
	// I alive and answering A: no alarm, and A re-adopts it.
	b2 := newCtl(t, "B", commissioned(1, incI, 9), 0)
	a2 := newCtl(t, "A", VoterState{}, now)
	for k := range 8 {
		tt := now.Add(lease / 2 * time.Duration(k))
		a2.hear(tt, incI, 9)
		qa, _ := a2.tick(tt)
		talk(tt, a2, b2, qa)
	}
	for _, e := range a2.a.Events() {
		if e.Kind == EventRecommission {
			t.Fatalf("an alarm with I answering: %+v", e)
		}
	}
	if _, inc := adopted(a2.v.State()); inc != incI {
		t.Fatalf("A did not re-adopt the live I: %+v", a2.v.State())
	}
}

// A lost-file controller re-proposing after a restart: A heard I promise
// term 20 (terms A led on {A, I} before its file was lost), proposed
// (2, I) at floor 20, and restarted before B answered. The floor must be
// durable with the proposal: the new boot has heard nothing, and B's Ack
// carries only B's floor 9. Red under proposal_floor_volatile.
func TestProposalFloorSurvivesARestart(t *testing.T) {
	b := newCtl(t, "B", commissioned(1, incI, 9), 0)
	now := at(2 * lease)
	a := newCtl(t, "A", VoterState{}, now)
	a.hear(now, incI, 20)
	qa, _ := a.tick(now)
	var props []Adopt
	for _, m := range qa {
		for _, r := range b.recv(now, m) {
			props = append(props, a.recv(now, r)...)
		}
	}
	if st := a.v.State(); st.PendingGen != 2 || st.PendingInc != incI || len(props) == 0 {
		t.Fatalf("A does not propose (2, I): %+v %+v", st, props)
	}
	// The proposal is lost, and A restarts from its file, I now silent.
	later := now.Add(lease)
	a = newCtl(t, "A", a.v.State(), later)
	out, _ := a.tick(later)
	talk(later, a, b, out)
	if st := a.v.State(); st.Incarnation != incI || st.Floor < 20 {
		t.Fatalf("A adopted without the floor it heard: %+v", st)
	}
	if st := b.v.State(); st.Floor < 20 {
		t.Fatalf("B took a floor below A's: %+v", st)
	}
}

// The operator's way out (design §7): in the two-of-three case above, an
// audited Recommission on A names the rebuilt witness's J and a floor of
// 1000, above every term the pair could have used. The floor is durable
// before anything leaves; then the ordinary handshake adopts J, either
// way round: A takes B's pending switch to J (B's proposal reaches A
// first), or A proposes J itself (B pends nothing). Both voters end at
// the floor: neither grants term 1000, both grant above it. The
// permission is in memory: a restart before A proposed or took J needs
// the command again (the floor stays).
func TestRecommissionByHand(t *testing.T) {
	// setup: B committed (1, I), I dead; B pends (2, J) when switch. A
	// lost its file, has heard J, and has exchanged boots with B (two
	// rounds, refused).
	setup := func(t *testing.T, switching bool) (*ctl, *ctl, Instant) {
		b := newCtl(t, "B", commissioned(1, incI, 9), 0)
		now := at(2 * lease)
		if switching {
			b.hear(now, incJ, 0)
			b.tick(now)
		}
		a := newCtl(t, "A", VoterState{}, now)
		a.hear(now, incJ, 0)
		for range 2 {
			qa, _ := a.tick(now)
			talk(now, a, b, qa)
		}
		if st := a.v.State(); st.Incarnation != 0 || st.PendingGen != 0 {
			t.Fatalf("A moved before the command: %+v", st)
		}
		return a, b, now
	}
	atFloor := func(t *testing.T, a, b *ctl, now Instant) {
		t.Helper()
		for _, x := range []*ctl{a, b} {
			if st := x.v.State(); st.Incarnation != incJ || st.PendingGen != 0 || st.Floor < 1000 {
				t.Fatalf("%v did not adopt J at the floor: %+v", x.self, st)
			}
		}
		end := now.Add(10 * lease)
		if grant(t, a.v, end, Request{From: "B", Term: 1000, Nonce: 1, ID: 1}) {
			t.Fatal("A granted a term at the operator's floor")
		}
		if !grant(t, a.v, end, Request{From: "B", Term: 1001, Nonce: 1, ID: 2}) {
			t.Fatal("A refused a term above the operator's floor")
		}
		if grant(t, b.v, end, Request{From: "A", Term: 1000, Nonce: 2, ID: 3}) {
			t.Fatal("B granted a term at the operator's floor")
		}
	}

	t.Run("refused", func(t *testing.T) {
		a, b, _ := setup(t, true)
		if _, err := a.a.Recommission(incJ, 0); err != ErrNoFloor {
			t.Fatalf("a floor of 0: %v", err)
		}
		if _, err := b.a.Recommission(incJ, 1000); err != ErrCommissioned {
			t.Fatalf("a commissioned controller: %v", err)
		}
	})

	// B's re-sent proposal reaches A first: A takes it (the permission
	// in the acceptor rule), and not before its floor is durable.
	t.Run("peer proposes", func(t *testing.T) {
		a, b, now := setup(t, true)
		p, err := a.a.Recommission(incJ, 1000)
		if err != nil || p == nil || p.State.Floor != 1000 || p.State.Incarnation != 0 {
			t.Fatalf("Recommission: %+v %v", p, err)
		}
		a.hold = true
		a.write(now, p)
		rs, _ := b.tick(now)
		if len(rs) != 1 || rs[0].Kind != Propose || rs[0].Incarnation != incJ {
			t.Fatalf("B does not re-send its proposal of J: %+v", rs)
		}
		talk(now, b, a, rs)
		if st := b.v.State(); st.Incarnation != incI || st.Floor >= 1000 {
			t.Fatalf("B moved before A's floor was durable: %+v", st)
		}
		if st := a.v.State(); st.Incarnation != incJ || st.PendingGen != 0 {
			t.Fatalf("A did not take B's proposal of J: %+v", st)
		}
		a.hold = false
		talk(now, a, b, a.flush(now, true))
		atFloor(t, a, b, now)
	})

	// B pends nothing (it never heard J): A proposes J itself.
	t.Run("A proposes", func(t *testing.T) {
		a, b, now := setup(t, false)
		p, _ := a.a.Recommission(incJ, 1000)
		a.write(now, p)
		qa, _ := a.tick(now)
		talk(now, a, b, qa)
		atFloor(t, a, b, now)
	})

	// One boot only: A restarts before it proposed or took J, and
	// refuses again; the floor stays.
	t.Run("restart", func(t *testing.T) {
		a, b, now := setup(t, true)
		p, _ := a.a.Recommission(incJ, 1000)
		a.write(now, p)
		a = newCtl(t, "A", a.v.State(), now)
		for k := range 6 {
			tt := now.Add(lease / 2 * time.Duration(k))
			a.hear(tt, incJ, 0)
			qa, _ := a.tick(tt)
			talk(tt, a, b, qa)
			rs, _ := b.tick(tt)
			talk(tt, b, a, rs)
		}
		if st := a.v.State(); st.Incarnation != 0 || st.Floor != 1000 {
			t.Fatalf("after a restart A re-commissioned without the command, or lost the floor: %+v", st)
		}
	})
}

// KNOWN GAP (design §3.1, "The residual"): A adopted B's switch to a
// second witness copy X with its ack lost, and led on {A, X} up to term
// 30 while B, pending, counted nothing. Then A's file was lost and X
// went away; C, the real witness, still answers (promised 9). B heard X
// only when it proposed it (promised 9), not since: had it, its floor
// would carry 30 into the re-adoption. A re-adopts C, the incarnation B
// committed, at a floor that misses term 30: nobody alive knows it. The committed-incarnation rule doesn't catch this (B
// committed C, not X). Closing it means a lost controller waits whenever
// its peer pends an incarnation it can't hear, which costs the give-way
// path; naut-lead's call. This test pins today's behaviour so that a fix
// shows: it flips then.
func TestKnownGapPendingStray(t *testing.T) {
	const incX = incK
	b := newCtl(t, "B", commissioned(1, incI, 9), 0)
	now := at(2 * lease)
	b.hear(now, incX, 9)
	b.tick(now)
	if st := b.v.State(); st.PendingInc != incX {
		t.Fatalf("B does not pend X: %+v", st)
	}
	a := newCtl(t, "A", VoterState{}, now)
	for k := range 4 {
		tt := now.Add(lease / 4 * time.Duration(k))
		a.hear(tt, incI, 9)
		qa, _ := a.tick(tt)
		talk(tt, a, b, qa)
		rs, _ := b.tick(tt)
		talk(tt, b, a, rs)
	}
	st := a.v.State()
	if st.Incarnation != incI || st.Floor >= 30 {
		t.Fatalf("the gap is closed (A %+v): flip this test and update design §3.1", st)
	}
}

// Both controllers uncommissioned with the real witness intact (#710 B2,
// the review of #1016): before the loss B led term 11 on {A, B} while W
// (promised 9) was cut, and both files were lost before a renewal reached
// W. No surviving voter holds 11. The pair must not re-commission on its
// own at W's floor 9, or it can lead 10 and 11 again; each raises
// EventRecommission naming no incarnation (the peer is uncommissioned
// too). Red under both_lost_recommission.
func TestBothLostWaitForTheOperator(t *testing.T) {
	a := newCtl(t, "A", VoterState{}, 0)
	b := newCtl(t, "B", VoterState{}, 0)
	for k := range 8 {
		now := at(lease/2 + lease/2*time.Duration(k))
		a.hear(now, incI, 9)
		b.hear(now, incI, 9)
		qa, _ := a.tick(now)
		talk(now, a, b, qa)
		qb, _ := b.tick(now)
		talk(now, b, a, qb)
	}
	for _, x := range []*ctl{a, b} {
		if st := x.v.State(); st.Incarnation != 0 || st.PendingGen != 0 {
			t.Fatalf("%v re-commissioned (or proposed) without an operator: %+v", x.self, st)
		}
		ev := x.a.Events()
		if len(ev) != 1 || ev[0] != (Event{Kind: EventRecommission}) {
			t.Fatalf("%v: want one EventRecommission naming no incarnation, got %+v", x.self, ev)
		}
	}
	// The operator re-commissions one of them, with a floor above 11: the
	// pair adopts I at that floor, and neither voter grants 11 or 12.
	p, err := a.a.Recommission(incI, 20)
	if err != nil {
		t.Fatal(err)
	}
	now := at(5 * lease)
	a.write(now, p)
	for k := range 4 {
		tt := now.Add(lease / 2 * time.Duration(k))
		a.hear(tt, incI, 9)
		b.hear(tt, incI, 9)
		qa, _ := a.tick(tt)
		talk(tt, a, b, qa)
	}
	for _, x := range []*ctl{a, b} {
		if st := x.v.State(); st.Incarnation != incI || st.Floor < 20 {
			t.Fatalf("%v not on I at the operator's floor: %+v", x.self, st)
		}
	}
	end := now.Add(10 * lease)
	if grant(t, b.v, end, Request{From: "A", Term: 12, Nonce: 1, ID: 1}) {
		t.Fatal("B granted a term below the operator's floor")
	}
}

// A committed incarnation that has answered never raises the alarm: the
// controller can re-adopt it (#710 B2, (b)). B pends a switch to J while
// I, its committed incarnation, still answers A; only B's proposals reach
// A (A refuses them: a switch), well past a lease. Red under
// alarm_on_heard.
func TestHeardCommittedIncarnationRaisesNoAlarm(t *testing.T) {
	b := newCtl(t, "B", commissioned(1, incI, 9), 0)
	now := at(2 * lease)
	b.hear(now, incJ, 0)
	b.tick(now) // B pends (2, J)
	a := newCtl(t, "A", VoterState{}, now)
	a.hear(now, incJ, 0)
	for range 2 { // the boots are exchanged; A, not hearing I yet, stays put
		qa, _ := a.tick(now)
		talk(now, a, b, qa)
	}
	for k := range 8 {
		tt := now.Add(lease / 2 * time.Duration(k))
		a.hear(tt, incI, 9)
		rs, _ := b.tick(tt)
		for _, m := range rs {
			a.recv(tt, m) // A's answers are lost
		}
	}
	if st := a.v.State(); st.Incarnation != 0 {
		t.Fatalf("A took B's switch: %+v", st)
	}
	for _, e := range a.a.Events() {
		if e.Kind == EventRecommission {
			t.Fatalf("an alarm although I answered: %+v", e)
		}
	}
}

// The operator's floor must be above what the controller already knows
// (#710 B2, (c)): here the witness promised 30, so 30 is refused and 31
// taken. Red under recommission_any_floor.
func TestRecommissionFloorAboveTheKnown(t *testing.T) {
	a := newCtl(t, "A", VoterState{}, 0)
	a.hear(at(lease), incJ, 30)
	if _, err := a.a.Recommission(incJ, 30); err != ErrFloorTooLow {
		t.Fatalf("a floor at the witness's promise: %v", err)
	}
	p, err := a.a.Recommission(incJ, 31)
	if err != nil || p.State.Floor != 31 {
		t.Fatalf("a floor above it: %+v %v", p, err)
	}
}

// The accepted residual is visible (#710 B2, (d)): B, pending its switch
// to X, takes its uncommissioned peer's re-adoption of I and raises
// EventReadoptOverSwitch naming both.
func TestReadoptOverSwitchRaisesAnEvent(t *testing.T) {
	const incX = incK
	b := newCtl(t, "B", commissioned(1, incI, 9), 0)
	now := at(2 * lease)
	b.hear(now, incX, 9)
	b.tick(now)
	b.a.Events()
	a := newCtl(t, "A", VoterState{}, now)
	for k := range 4 {
		tt := now.Add(lease / 4 * time.Duration(k))
		a.hear(tt, incI, 9)
		qa, _ := a.tick(tt)
		talk(tt, a, b, qa)
	}
	var got []Event
	for _, e := range b.a.Events() {
		if e.Kind == EventReadoptOverSwitch {
			got = append(got, e)
		}
	}
	if st := b.v.State(); st.Incarnation != incI || len(got) != 1 || got[0].Incarnation != incI || got[0].Other != incX {
		t.Fatalf("B %+v, events %+v", st, got)
	}
}

// Both files lost, the operator re-commissions A, and A restarts with its
// proposal in flight (lost on the way). The permission is gone with the
// boot: the re-sent proposal is unmarked, and the uncommissioned B refuses
// it. The same floor again is refused (A knows it now); one above
// KnownFloor is taken, and the pending proposal leaves marked: B takes it.
func TestRecommissionAgainAfterARestart(t *testing.T) {
	a := newCtl(t, "A", VoterState{}, 0)
	b := newCtl(t, "B", VoterState{}, 0)
	round := func(now Instant) {
		a.hear(now, incI, 9)
		b.hear(now, incI, 9)
		qa, _ := a.tick(now)
		talk(now, a, b, qa)
		qb, _ := b.tick(now)
		talk(now, b, a, qb)
	}
	now := at(lease)
	round(now)
	round(now) // the boots are exchanged
	p, _ := a.a.Recommission(incI, 20)
	a.write(now, p)
	qa, _ := a.tick(now) // A's query; B's State makes A propose
	for _, m := range qa {
		for _, r := range b.recv(now, m) {
			_ = a.recv(now, r) // A's proposal is lost on the way
		}
	}
	if st := a.v.State(); st.PendingInc != incI || st.Floor != 20 {
		t.Fatalf("A does not pend I at floor 20: %+v", st)
	}
	later := now.Add(lease)
	a = newCtl(t, "A", a.v.State(), later)
	for k := range 4 {
		round(later.Add(lease / 4 * time.Duration(k)))
	}
	if st := b.v.State(); st.Incarnation != 0 {
		t.Fatalf("B took an unmarked proposal after A's restart: %+v", st)
	}
	if _, err := a.a.Recommission(incI, 20); err != ErrFloorTooLow {
		t.Fatalf("the same floor again: %v", err)
	}
	f := a.a.KnownFloor() + 1
	p, err := a.a.Recommission(incI, f)
	if err != nil {
		t.Fatal(err)
	}
	a.write(later, p)
	for k := range 4 {
		round(later.Add(lease + lease/4*time.Duration(k)))
	}
	for _, x := range []*ctl{a, b} {
		if st := x.v.State(); st.Incarnation != incI || st.Floor < f {
			t.Fatalf("%v not on I at the new floor %d: %+v", x.self, f, st)
		}
	}
}
