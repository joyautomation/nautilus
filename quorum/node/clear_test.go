package node

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/quorum"
)

// markOld marks name at voter v with two boots heard interleaved long
// enough ago (four leases) that neither is live any more: a copy that ran
// beside name's real process and has since been stopped. Under the
// voter's lock. It returns the write of the mark, for the driver.
func markOld(t *testing.T, v *quorum.Voter, now quorum.Instant, name quorum.NodeID) *quorum.Persist {
	t.Helper()
	var w *quorum.Persist
	base := now.Add(-4 * loopLease)
	d := time.Duration(float64(loopLease/4)*(1+quorum.MaxRho)) + 50*time.Millisecond
	for i, at := range []time.Duration{0, 10 * time.Millisecond, d, d + 10*time.Millisecond, d + 20*time.Millisecond, d + 30*time.Millisecond} {
		nonce := []uint64{0xA1, 0xC1}[i%2]
		if p, _ := v.Handle(base.Add(at), quorum.Request{From: name, Nonce: nonce, ID: quorum.ProbeID, Seq: uint64(1 + i/2)}); p != nil {
			w = p
		}
	}
	if !slices.Contains(v.Marked(), name) {
		t.Fatalf("the sequence didn't mark %s", name)
	}
	return w
}

// markAt marks name at the controller id's voter (markOld).
func (c *cluster) markAt(id quorum.NodeID, name quorum.NodeID) {
	e := c.elector(id)
	e.mu.Lock()
	p := markOld(c.t, e.v, e.now(), name)
	e.mu.Unlock()
	e.w.ask(p)
}

// witnessVoter runs f on the witness's voter for the pair, under its lock.
func (c *cluster) witnessVoter(f func(v *quorum.Voter, now quorum.Instant)) {
	w := c.wit.w
	w.mu.Lock()
	p := w.pairs[c.pair]
	w.mu.Unlock()
	if p == nil {
		c.t.Fatal("the witness serves no pair named for the cluster")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	f(p.v, w.now())
}

// A clear issued on one controller clears the mark at every member that
// holds one (its own voter, its peer's, the witness's), and the marked
// controller, held until then (it campaigns no more), is freed without a
// restart: its next probe gets an unmarked reply from the same voter boot
// (#1029 3c-3a). The report's re-check, a lease later, finds no mark.
func TestClearDuplicateAllClearsEveryMember(t *testing.T) {
	c := newCluster(t)
	c.startAll()
	c.commission()
	c.settledLeader(leases(20))
	c.markAt("b", "a")
	c.witnessVoter(func(v *quorum.Voter, now quorum.Instant) { markOld(t, v, now, "a") })
	c.waitFor("a to be held by the marks", leases(10), func() bool { return c.status("a").Held })
	time.Sleep(leases(2)) // every member's voter up a lease and D (Voter.Settled)
	ctx, cancel := context.WithTimeout(context.Background(), leases(10))
	defer cancel()
	rep, err := c.elector("b").ClearDuplicateAll(ctx, "a", "user:op")
	again := false
	if err != nil && raceOn && slowWrite(rep) {
		// Under -race a member's state write can miss its half-lease
		// deadline on a loaded runner (#1120): the command ends
		// INCOMPLETE, as it must, and its own advice is to run it again.
		again = true
		rep, err = c.elector("b").ClearDuplicateAll(ctx, "a", "user:op")
	}
	switch {
	case err != nil && again && nothingToClear(rep):
		// The late write landed after all: the first run cleared every
		// member, so the second finds nothing left to clear. The state
		// checks below still hold it to the result.
	case err != nil:
		t.Fatalf("clear: %v (report %+v)", err, rep)
	default:
		if !rep.Done() || len(rep.Clear) != 3 {
			t.Fatalf("report %+v: want every member asked and none marking a lease later", rep)
		}
		cleared := 0
		for _, a := range rep.Clear {
			if a.Cleared {
				cleared++
			}
		}
		if cleared != 2 {
			t.Fatalf("cleared at %d members, want 2 (b and the witness): %+v", cleared, rep.Clear)
		}
	}
	if m := c.status("b").Marked; len(m) != 0 {
		t.Fatalf("b still marks %v", m)
	}
	c.witnessVoter(func(v *quorum.Voter, now quorum.Instant) {
		if m := v.Marked(); len(m) != 0 {
			t.Fatalf("the witness still marks %v", m)
		}
	})
	c.waitFor("a's hold to end without a restart", leases(10), func() bool { return !c.status("a").Held })
	if n, _ := c.countEvents(EventDuplicateCleared); n == 0 {
		t.Fatal("no redundancy.duplicate-cleared event")
	}
}

// A member that does not answer the check phase (here the witness,
// stopped) refuses the whole command: nothing is cleared anywhere, and the
// refusal names it (naut-lead's condition (i), all or nothing).
// clear_partial_check turns it red.
func TestClearDuplicateAllNeedsEveryMember(t *testing.T) {
	c := newCluster(t)
	c.startAll()
	c.commission()
	c.settledLeader(leases(20))
	c.markAt("b", "a")
	time.Sleep(leases(2)) // every member's voter up a lease and D (Voter.Settled)
	c.stopWitness()
	ctx, cancel := context.WithTimeout(context.Background(), leases(10))
	defer cancel()
	rep, err := c.elector("a").ClearDuplicateAll(ctx, "a", "user:op")
	if !errors.Is(err, ErrClearRefused) {
		t.Fatalf("clear with the witness down: %v (report %+v); want refused", err, rep)
	}
	if len(rep.Clear) != 0 {
		t.Fatalf("a clear phase ran: %+v", rep.Clear)
	}
	if m := c.status("b").Marked; !slices.Contains(m, "a") {
		t.Fatalf("b's mark is gone (%v); nothing may be cleared when a member is silent", m)
	}
	if !strings.Contains(rep.Refused, "witness") {
		t.Fatalf("the refusal %q does not name the witness", rep.Refused)
	}
}

// While a member's voter hears two boots of the name (a copy still
// reaches it), the check phase refuses and nothing is cleared.
func TestClearDuplicateAllRefusesWhileTheCopyIsHeard(t *testing.T) {
	c := newCluster(t)
	c.startAll()
	c.commission()
	c.settledLeader(leases(20))
	c.markAt("b", "a")
	time.Sleep(leases(2)) // every member's voter up a lease and D (Voter.Settled)
	// A frame from another boot of a, now: the copy still talks to b.
	e := c.elector("b")
	e.mu.Lock()
	e.v.Handle(e.now(), quorum.Request{From: "a", Nonce: 0xC2, ID: quorum.ProbeID, Seq: 1})
	e.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), leases(10))
	defer cancel()
	rep, err := c.elector("a").ClearDuplicateAll(ctx, "a", "user:op")
	if !errors.Is(err, ErrClearRefused) || len(rep.Clear) != 0 || !strings.Contains(rep.Refused, "two processes named a") {
		t.Fatalf("clear while the copy is heard: %v, %+v; want refused before any clear, for the copy", err, rep)
	}
	if m := c.status("b").Marked; !slices.Contains(m, "a") {
		t.Fatalf("b's mark is gone: %v", m)
	}
}

// Each member re-checks its own voter when the clear reaches it: a copy
// heard there since the check refuses the clear at that member, and the
// mark stays. clear_ignores_keep turns it red. (A bare voter: this runs under
// package quorum's mutations too.)
func TestADupClearReChecksItsOwnVoter(t *testing.T) {
	v := quorum.NewVoter("b", loopLease, quorum.VoterState{}, quorum.Instant(10*time.Second))
	now := quorum.Instant(20 * time.Second)
	markOld(t, v, now, "a")
	if a := dupAnswer(v, now, "b", dupOp{Op: "clear", Name: "a"}); !a.Cleared || a.Marked {
		t.Fatalf("a mark with no live copy: %+v; want cleared", a)
	}
	markOld(t, v, now.Add(10*time.Second), "a")
	now = now.Add(10 * time.Second)
	v.Handle(now, quorum.Request{From: "a", Nonce: 0xB1, ID: quorum.ProbeID, Seq: 1})
	v.Handle(now.Add(time.Millisecond), quorum.Request{From: "a", Nonce: 0xB2, ID: quorum.ProbeID, Seq: 1})
	a := dupAnswer(v, now.Add(2*time.Millisecond), "b", dupOp{Op: "clear", Name: "a"})
	if a.Cleared || !a.Marked || a.Refused == "" {
		t.Fatalf("two live boots of a at this voter: %+v; want the clear refused and the mark kept", a)
	}
}

// A voter that started under a lease and D ago can't say which boots ran
// before it: it answers neither the check nor the clear (#1135 review: a
// just-restarted member counted as "hears nothing"). clear_unsettled
// turns it red.
func TestAJustStartedMemberAnswersNoClear(t *testing.T) {
	start := quorum.Instant(10 * time.Second)
	v := quorum.NewVoter("b", loopLease, quorum.VoterState{}, start)
	markOld(t, v, start, "a")
	early := start.Add(loopLease)
	if a := dupAnswer(v, early, "b", dupOp{Op: "check", Name: "a"}); a.Refused == "" {
		t.Fatalf("a check a lease after the start: %+v; want refused", a)
	}
	if a := dupAnswer(v, early, "b", dupOp{Op: "clear", Name: "a"}); a.Cleared || a.Refused == "" {
		t.Fatalf("a clear a lease after the start: %+v; want refused, the mark kept", a)
	}
	if a := dupAnswer(v, start.Add(2*loopLease), "b", dupOp{Op: "clear", Name: "a"}); !a.Cleared {
		t.Fatalf("a clear two leases after the start: %+v; want cleared", a)
	}
}

// A clear names the boot the check heard; a member that now hears another
// (a copy back since the check, or a clear sent by a peer without one)
// refuses it, even hearing only that one. clear_ignores_keep turns it red.
func TestAClearRefusesABootOtherThanTheOneItKeeps(t *testing.T) {
	v := quorum.NewVoter("b", loopLease, quorum.VoterState{}, quorum.Instant(10*time.Second))
	now := quorum.Instant(20 * time.Second)
	markOld(t, v, now, "a")
	v.Handle(now, quorum.Request{From: "a", Nonce: 0xD1, ID: quorum.ProbeID, Seq: 1})
	if a := dupAnswer(v, now.Add(time.Millisecond), "b", dupOp{Op: "clear", Name: "a", Keep: 0xE1}); a.Cleared || a.Refused == "" {
		t.Fatalf("a clear keeping 0xE1 while 0xD1 is heard: %+v; want refused", a)
	}
	if a := dupAnswer(v, now.Add(2*time.Millisecond), "b", dupOp{Op: "clear", Name: "a", Keep: 0xD1}); !a.Cleared {
		t.Fatalf("a clear keeping the one boot heard: %+v; want cleared", a)
	}
}

// A copy heard by one member after the clear, before any member marks it
// again (a copy behind a split), still makes the command report it: the
// re-check takes the members' boots together. recheck_marks_only turns
// it red.
func TestTheRecheckReportsACopyHeardAfterTheClear(t *testing.T) {
	c := newCluster(t)
	c.startAll()
	c.commission()
	c.settledLeader(leases(20))
	c.markAt("b", "a")
	time.Sleep(leases(2)) // every member up a lease and D since it last (re)started its voter
	ctx, cancel := context.WithTimeout(context.Background(), leases(10))
	defer cancel()
	done := make(chan struct{})
	var rep ClearReport
	var err error
	go func() { rep, err = c.elector("a").ClearDuplicateAll(ctx, "a", "user:op"); close(done) }()
	c.waitFor("b's mark cleared", leases(5), func() bool {
		select {
		case <-done:
			t.Fatalf("the command ended before the test could act: %v, %+v", err, rep)
		default:
		}
		return len(c.status("b").Marked) == 0
	})
	// One frame from a new boot of a, at b only: no mark (that takes two
	// interleaved), but two boots live across the members.
	e := c.elector("b")
	e.mu.Lock()
	e.v.Handle(e.now(), quorum.Request{From: "a", Nonce: 0xD2, ID: quorum.ProbeID, Seq: 1})
	e.mu.Unlock()
	<-done
	if !errors.Is(err, ErrClearIncomplete) || !rep.CopyHeardAfter {
		t.Fatalf("a copy heard after the clear: %v, %+v; want incomplete, the copy reported", err, rep)
	}
}

// The check phase: a clear that would keep nothing (every member answers,
// a member marks the name, none hears any process of it) is refused: it
// has no use, and would retire a real controller that is only paused
// (naut-lead's ruling on #1135). clear_without_keep turns it red. The
// boots to retire are every boot any member knew but the kept one.
func TestTheCheckPhaseKeepsOneBootAndRetiresTheRest(t *testing.T) {
	quiet := []DupAnswer{{Member: "a"}, {Member: "b", Marked: true, Known: []quorum.BootSeq{{Nonce: 0xA, Seq: 9}, {Nonce: 0xC, Seq: 4}}}, {Member: "witness"}}
	if _, _, _, why := checkPhase("a", quiet); !strings.Contains(why, "nothing running under a to keep") {
		t.Fatalf("no boot heard anywhere: refused %q; want nothing to keep", why)
	}
	heard := []DupAnswer{
		{Member: "a", Boots: []uint64{0xA}, Known: []quorum.BootSeq{{Nonce: 0xA, Seq: 12}}},
		{Member: "b", Marked: true, Known: []quorum.BootSeq{{Nonce: 0xA, Seq: 9}, {Nonce: 0xC, Seq: 4}}},
		{Member: "witness", Boots: []uint64{0xA}, Known: []quorum.BootSeq{{Nonce: 0xA, Seq: 11}, {Nonce: 0xE, Seq: 2}}},
	}
	keep, retire, _, why := checkPhase("a", heard)
	if why != "" || keep != 0xA {
		t.Fatalf("keep %x, refused %q; want 0xA kept", keep, why)
	}
	want := []quorum.BootSeq{{Nonce: 0xC, Seq: 4}, {Nonce: 0xE, Seq: 2}}
	if !slices.Equal(retire, want) {
		t.Fatalf("retire %+v, want %+v: every boot any member knew but the kept one", retire, want)
	}
}

// A clear that missed a member (it errored or refused in the clear phase,
// so it retired nothing) is not done, even when the re-check comes back
// clean, and the command runs again although no member marks the name any
// more: the check sees a boot still to be retired there (#1135 re-check).
// clear_done_ignores_clear and clear_rerun_refused turn it red.
func TestAClearMissedAtAMemberIsNotDoneAndRunsAgain(t *testing.T) {
	missed := ClearReport{Name: "a",
		Clear:   []DupAnswer{{Member: "a"}, {Member: "b", Cleared: true}, {Member: "witness", Err: "witness: connection refused"}},
		Recheck: []DupAnswer{{Member: "a", Boots: []uint64{0xA}}, {Member: "b", Boots: []uint64{0xA}}, {Member: "witness", Boots: []uint64{0xA}}},
	}
	if missed.Done() {
		t.Fatal("a clear the witness missed reports done")
	}
	again := []DupAnswer{
		{Member: "a", Boots: []uint64{0xA}, Known: []quorum.BootSeq{{Nonce: 0xA, Seq: 20}}},
		{Member: "b", Boots: []uint64{0xA}, Known: []quorum.BootSeq{{Nonce: 0xA, Seq: 19}, {Nonce: 0xC, Seq: 4}}, Retired: []uint64{0xC}},
		{Member: "witness", Boots: []uint64{0xA}, Known: []quorum.BootSeq{{Nonce: 0xA, Seq: 18}, {Nonce: 0xC, Seq: 5}}},
	}
	keep, retire, _, why := checkPhase("a", again)
	if why != "" || keep != 0xA || !slices.Equal(retire, []quorum.BootSeq{{Nonce: 0xC, Seq: 5}}) {
		t.Fatalf("a run after a missed member: keep %x retire %+v refused %q; want it to go ahead and retire 0xC", keep, retire, why)
	}
	again[2].Retired = []uint64{0xC}
	again[0].Retired = []uint64{0xC}
	if _, _, _, why := checkPhase("a", again); why == "" {
		t.Fatal("with every member retired and no mark, a clear went ahead: want nothing to clear")
	}
}

// The clear's generation goes above every member's and every hold's, so a
// legitimate clear always frees the held controller on the next word of
// each voter, even when a member restored from a backup carries an older
// generation than the one its mark set the hold under (naut-lead's
// liveness pin on #1029 3c-3b). The restored member's own unmarked word,
// older, frees nothing, as a wiped voter's wouldn't. clear_gen_ignores_holds
// turns it red: the clear's generation falls at or under the hold's, and
// the controller stays held until it restarts.
func TestAClearFreesAHoldWhenAMemberIsRestoredFromABackup(t *testing.T) {
	lease := time.Second
	now := quorum.Instant(10 * time.Second)
	c, err := quorum.NewCandidate(quorum.Config{Self: "a", Peer: "b", Witness: "witness", Lease: lease, Rho: 0.01, Incarnation: 7, Nonce: 0xA}, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	ask := func(v *quorum.Voter, at quorum.Instant, seq uint64) quorum.Reply {
		_, out := v.Handle(at, quorum.Request{From: "a", Nonce: 0xA, ID: quorum.ProbeID, Seq: seq})
		c.NoteMark(out[0])
		return out[0]
	}
	// The witness marked a under generation 5 (two clears it took that the
	// others missed), durably: the hold is set under 5.
	w := quorum.NewVoter("witness", lease, quorum.VoterState{Dups: []quorum.DupRecord{{Name: "a", Marked: true, Gen: 5}}}, now)
	w.SetBoot(1)
	if r := ask(w, now, 1); !r.Marked || r.ClearGen != 5 || !c.Marked() {
		t.Fatalf("not held under generation 5: %+v", r)
	}
	// The witness is restored from a backup taken at generation 3, before
	// it marked: its unmarked word is older than the hold, and frees nothing.
	later := now.Add(3 * lease)
	w = quorum.NewVoter("witness", lease, quorum.VoterState{Dups: []quorum.DupRecord{{Name: "a", Gen: 3}}}, later)
	w.SetBoot(2)
	if r := ask(w, later, 2); r.Marked || !c.Marked() {
		t.Fatalf("the restored witness's older word (%+v) freed the hold, or it marked", r)
	}
	// Every member answers the check with generation 3; the held
	// controller says its hold's.
	check := []DupAnswer{
		{Member: "a", Boots: []uint64{0xA}, Gen: 3, HoldGen: c.HoldGen()},
		{Member: "b", Boots: []uint64{0xA}, Marked: true, Gen: 3},
		{Member: "witness", Boots: []uint64{0xA}, Gen: w.DupGen("a")},
	}
	keep, _, gen, why := checkPhase("a", check)
	if why != "" || keep != 0xA {
		t.Fatalf("check refused %q (keep %x)", why, keep)
	}
	w.ClearDuplicateKeeping("a", keep, nil, gen)
	if r := ask(w, later.Add(lease/4), 3); c.Marked() {
		t.Fatalf("the clear's word (%+v, generation %d) left the hold set under %d", r, gen, c.HoldGen())
	}
}

// A mark is kept on disk (#1029 3c-3b): b's voter, restarted, still marks
// a, so a stays held; a restart no longer clears it. The clear does,
// without a restart of a.
func TestAMarkSurvivesARestartAndTheClearEndsIt(t *testing.T) {
	c := newCluster(t)
	c.startAll()
	c.commission()
	c.settledLeader(leases(20))
	c.markAt("b", "a")
	c.waitFor("a to be held by b's mark", leases(10), func() bool { return c.status("a").Held })
	time.Sleep(leases(1)) // the mark's write lands
	c.kill("b")
	c.start("b")
	c.waitFor("b's restarted voter to mark a again from its file", leases(10), func() bool {
		return slices.Contains(c.status("b").Marked, "a")
	})
	time.Sleep(leases(3))
	if !c.status("a").Held {
		t.Fatal("a was freed by b's restart")
	}
	ctx, cancel := context.WithTimeout(context.Background(), leases(10))
	defer cancel()
	rep, err := c.elector("a").ClearDuplicateAll(ctx, "a", "user:op")
	if err != nil {
		t.Fatalf("clear: %v (report %+v)", err, rep)
	}
	c.waitFor("a's hold to end without a restart", leases(10), func() bool { return !c.status("a").Held })
}

// A hold can outlive every member's mark: the only voter that marked a
// crashed before its write (or was wiped), and comes back unmarked, with
// nothing pending. The check still goes ahead, counting the held
// controller as marked, and the clear's generation frees it (#1144
// review). clear_ignores_held turns it red: "nothing to clear", and a
// stays held until it restarts.
func TestAClearFreesAHoldNoMemberMarksAnyMore(t *testing.T) {
	lease := loopLease
	now := quorum.Instant(10 * time.Second)
	c, err := quorum.NewCandidate(quorum.Config{Self: "a", Peer: "b", Witness: "witness", Lease: lease, Rho: 0.01, Incarnation: 7, Nonce: 0xA}, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	ask := func(v *quorum.Voter, at quorum.Instant, seq uint64) quorum.Reply {
		_, out := v.Handle(at, quorum.Request{From: "a", Nonce: 0xA, ID: quorum.ProbeID, Seq: seq})
		c.NoteMark(out[0])
		return out[0]
	}
	w := quorum.NewVoter("witness", lease, quorum.VoterState{}, 0)
	w.SetBoot(1)
	markOld(t, w, now, "a")
	if r := ask(w, now, 9); !r.Marked || !c.Marked() {
		t.Fatalf("not held: %+v", r)
	}
	// The witness crashes before the mark's write: back with no record.
	later := now.Add(3 * lease)
	w = quorum.NewVoter("witness", lease, quorum.VoterState{}, later)
	w.SetBoot(2)
	settled := later.Add(2 * lease)
	ask(w, settled, 10)
	if !c.Marked() {
		t.Fatal("the forgetting witness's word freed the hold")
	}
	b := quorum.NewVoter("b", lease, quorum.VoterState{}, later)
	ctl := quorum.NewVoter("a", lease, quorum.VoterState{}, later)
	for _, v := range []*quorum.Voter{w, b, ctl} {
		v.Handle(settled, quorum.Request{From: "a", Nonce: 0xA, ID: quorum.ProbeID, Seq: 11})
	}
	check := []DupAnswer{
		dupAnswer(ctl, settled, "a", dupOp{Op: "check", Name: "a"}),
		dupAnswer(b, settled, "b", dupOp{Op: "check", Name: "a"}),
		dupAnswer(w, settled, "witness", dupOp{Op: "check", Name: "a"}),
	}
	check[0].Held, check[0].HoldGen = c.Marked(), c.HoldGen() // as dupLocal says for its own name
	keep, retire, gen, why := checkPhase("a", check)
	if why != "" || keep != 0xA {
		t.Fatalf("the check refused %q (keep %x): a held controller no member marks is something to clear", why, keep)
	}
	if len(retire) != 0 {
		t.Fatalf("retired %+v: no member knew another boot", retire)
	}
	dupAnswer(w, settled.Add(time.Millisecond), "witness", dupOp{Op: "clear", Name: "a", Keep: keep, Gen: gen})
	if r := ask(w, settled.Add(2*time.Millisecond), 12); c.Marked() {
		t.Fatalf("the clear's word (%+v) left the hold", r)
	}
}

// The re-check counts a controller still held as not done: the command
// says incomplete, and the operator runs it again (#1144 re-check).
// done_ignores_held turns it red.
func TestAClearIsNotDoneWhileTheControllerIsHeld(t *testing.T) {
	rep := ClearReport{Name: "a",
		Clear:   []DupAnswer{{Member: "a", Cleared: true}, {Member: "b"}, {Member: "witness"}},
		Recheck: []DupAnswer{{Member: "a", Boots: []uint64{0xA}, Held: true}, {Member: "b", Boots: []uint64{0xA}}, {Member: "witness", Boots: []uint64{0xA}}},
	}
	if rep.Done() {
		t.Fatal("a held controller at the re-check reported done")
	}
	rep.Recheck[0].Held = false
	if !rep.Done() {
		t.Fatal("with nothing held or marked, not done")
	}
}

// slowWrite: a member answered the clear but its write missed the deadline.
// nothingToClear reports a clear refused because no member marks the
// name and every member has retired the boots it knew: a rerun after a
// first run that cleared everywhere but answered late.
func nothingToClear(rep ClearReport) bool {
	if !strings.Contains(rep.Refused, "nothing to clear") {
		return false
	}
	for _, a := range rep.Check {
		if a.Marked || a.Err != "" || a.Refused != "" {
			return false
		}
	}
	return len(rep.Check) == 3
}

func slowWrite(rep ClearReport) bool {
	for _, a := range rep.Clear {
		if strings.Contains(a.Err, "did not finish in time") {
			return true
		}
	}
	return false
}
