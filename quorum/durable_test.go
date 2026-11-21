package quorum

import (
	"slices"
	"testing"
)

// Durable duplicate marks (#1029 3c-3b).

// written confirms every write v has handed out (a durable disk).
func written(v *Voter) []Reply { return v.Persisted(v.dirty, true) }

// restart is v's restart at now from what its file holds: the last state
// it wrote, through the file format. The voter's boot changes to boot.
func restart(t *testing.T, v *Voter, now Instant, boot uint64) *Voter {
	t.Helper()
	b, err := v.State().MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	var st VoterState
	if err := st.UnmarshalBinary(b); err != nil {
		t.Fatalf("the state file doesn't read back: %v", err)
	}
	r := NewVoter(v.self, lease, st, now)
	r.SetBoot(boot)
	return r
}

// A mark made durable survives the voter's restart: the restarted voter
// marks the name from its first frame, refuses it new grants and says so.
// A file with no mark is still format 5. marks_volatile and
// mark_not_restored turn it red.
func TestAMarkSurvivesTheVotersRestart(t *testing.T) {
	v, now := markA(t)
	written(v)
	if b, _ := v.State().MarshalBinary(); b[len(stateMagic)] != stateVersion {
		t.Fatalf("a marking voter's file is format %d, want %d", b[len(stateMagic)], stateVersion)
	}
	if b, _ := ready("W").State().MarshalBinary(); b[len(stateMagic)] != stateVersionPlain {
		t.Fatalf("a file with no mark is format %d, want %d", b[len(stateMagic)], stateVersionPlain)
	}
	later := now.Add(3 * lease)
	r := restart(t, v, later, 2)
	if !slices.Equal(r.Marked(), []NodeID{"A"}) {
		t.Fatalf("the restarted voter marks %v, want [A]", r.Marked())
	}
	past := later.Add(2 * lease) // its restart wait over
	_, out := r.Handle(past, Request{From: "A", Term: 9, Nonce: 0xA, ID: 7, Seq: 9})
	if len(out) != 1 || out[0].Granted || !out[0].Marked {
		t.Fatalf("the restarted voter answered A %+v: want refused and marked", out)
	}
}

// A mark is told at once, before its write lands (the prototype's "real"
// mode, told only once durable, was worse in the fsync window: 115 against
// today's 22 two-leader runs of 300); the write is owed until it does.
func TestAMarkIsToldAtOnceAndWrittenAfter(t *testing.T) {
	v, now := markA(t)
	_, out := v.Handle(now.Add(ms), probe("A", 0xA, 9))
	if len(out) != 1 || !out[0].Marked || !v.MarksOwed() {
		t.Fatalf("before its write: %+v (owed %v), want marked and owed", out, v.MarksOwed())
	}
	written(v)
	if v.MarksOwed() {
		t.Fatal("still owed after its write landed")
	}
}

// The mark's write is off the grant path: while it fails, the incumbent's
// renewals (which change nothing durable) are answered at once, and the
// write is asked again. mark_write_on_grant_path turns it red (the
// prototype lost the incumbent's lease in 300 of 300 runs that way).
func TestAFailingMarkWriteHoldsNoRenewal(t *testing.T) {
	v, now := markA(t)
	if !v.MarksOwed() || v.issued < v.dupNeed {
		t.Fatalf("the mark's write wasn't handed out (owed %v, issued %d, need %d)", v.MarksOwed(), v.issued, v.dupNeed)
	}
	p := &Persist{Seq: v.issued}
	v.Persisted(p.Seq, false) // the disk fails
	q, out := v.Handle(now.Add(lease/2), Request{From: "A", Term: 3, Nonce: 0xA, ID: 4, Renewal: true, Seq: 4})
	if len(out) != 1 || !out[0].Granted {
		t.Fatalf("the incumbent's renewal waited on the failed mark write: %+v", out)
	}
	if q == nil || q.Seq < p.Seq || len(q.State.Dups) == 0 {
		t.Fatalf("the failed mark write wasn't asked again: %+v", q)
	}
	if again := v.MarkWrite(); again != nil {
		t.Fatalf("a second mark write while one is in flight: %+v", again)
	}
}

// A restored mark knows the boots it knew when it marked, the incumbent
// first, but none is fresh until it sends a new frame: HeldBy names no
// dead incumbent after a restart, and names the live one once heard
// (so the copy is told it is a spare again). restored_boot_fresh turns
// the first half red, mark_not_restored the second.
func TestARestoredIncumbentIsNamedOnceHeardAgain(t *testing.T) {
	v, now := markA(t)
	written(v)
	later := now.Add(3 * lease)
	r := restart(t, v, later, 2)
	if got := r.HeldBy(later.Add(ms), "A", 0xC); got != 0 {
		t.Fatalf("after the restart, before any frame, HeldBy names %#x to the copy", got)
	}
	r.Handle(later.Add(10*ms), probe("A", 0xA, 20))
	if got := r.HeldBy(later.Add(20*ms), "A", 0xC); got != 0xA {
		t.Fatalf("with the incumbent heard again, HeldBy names %#x to the copy, want 0xa", got)
	}
	if got := r.HeldBy(later.Add(20*ms), "A", 0xD); got != 0 {
		t.Fatalf("HeldBy named %#x to a boot first heard after the mark", got)
	}
}

// The boots an operator's clear retired survive a restart: the copy,
// paused at the clear, is marked again by its first new frame after the
// member restarted. retired_not_restored turns it red.
func TestRetiredBootsSurviveTheVotersRestart(t *testing.T) {
	v, now := markA(t)
	v.ClearDuplicateKeeping("A", 0xA, nil, 1)
	written(v)
	if len(v.Marked()) != 0 {
		t.Fatalf("still marks %v after the clear", v.Marked())
	}
	later := now.Add(3 * lease)
	r := restart(t, v, later, 2)
	if len(r.Marked()) != 0 || r.DupGen("A") != 1 {
		t.Fatalf("after the restart: marks %v, generation %d; want none, 1", r.Marked(), r.DupGen("A"))
	}
	r.Handle(later.Add(10*ms), probe("A", 0xC, 50))
	if !slices.Equal(r.Marked(), []NodeID{"A"}) {
		t.Fatalf("the retired copy's new frame after the restart marked %v, want [A]", r.Marked())
	}
}

// A hold ends on an unmarked word from another boot of the voter only
// when that word carries a higher clear generation than the hold: a wiped
// voter (generation 0) or a copy booted from an older disk can't end it,
// an operator's clear can. hold_released_by_any_boot turns the first half
// red; clear_gen_not_bumped and release_ignores_gen the second.
func TestAHoldEndsOnlyOnALaterClear(t *testing.T) {
	v, now := markA(t)
	old := v.State()
	old.Dups = nil // a copy of the voter's disk from before the mark
	written(v)
	v.SetBoot(7)
	c := newC(t, Config{Self: "A", Peer: "B", Witness: "W", Lease: lease, Rho: 0.01, Incarnation: 42, Nonce: 0xA}, 0, 0)
	_, out := v.Handle(now.Add(ms), probe("A", 0xA, 30))
	c.NoteMark(out[0])
	if !c.Marked() {
		t.Fatalf("not held by %+v", out[0])
	}
	t1 := now.Add(3 * lease)
	// A wiped voter: no file, a new boot.
	wiped := NewVoter("W", lease, VoterState{}, t1)
	wiped.SetBoot(8)
	_, out = wiped.Handle(t1.Add(ms), probe("A", 0xA, 31))
	c.NoteMark(out[0])
	if !c.Marked() {
		t.Fatalf("a wiped voter's unmarked word (%+v) ended a durable hold", out[0])
	}
	// A copy booted from the older disk.
	cp := NewVoter("W", lease, old, t1)
	cp.SetBoot(9)
	_, out = cp.Handle(t1.Add(2*ms), probe("A", 0xA, 32))
	c.NoteMark(out[0])
	if !c.Marked() {
		t.Fatalf("a copy from an older disk (%+v) ended a durable hold", out[0])
	}
	// The voter restarts (keeps the mark), then the operator clears it.
	r := restart(t, v, t1, 10)
	t2 := t1.Add(2 * lease)
	_, out = r.Handle(t2, probe("A", 0xA, 33))
	c.NoteMark(out[0])
	if !c.Marked() || !out[0].Marked {
		t.Fatalf("the restarted voter's durable mark (%+v) didn't keep the hold", out[0])
	}
	r.ClearDuplicateKeeping("A", 0xA, nil, 1)
	written(r)
	// It restarts once more before A hears from it: another boot's word,
	// with the clear's generation.
	r = restart(t, r, t2.Add(ms), 11)
	_, out = r.Handle(t2.Add(2*ms), probe("A", 0xA, 34))
	c.NoteMark(out[0])
	if c.Marked() {
		t.Fatalf("the clear's word (%+v) from a later boot didn't end the hold", out[0])
	}
}

// A reply from a boot an operator's clear retired counts for nothing and
// marks the name again at this controller's own voter: a copy paused or
// hung at the clear answers votes once it runs again (#1135's stated
// residual). retired_reply_counted turns it red.
func TestAReplyFromARetiredBootCountsNothingAndMarks(t *testing.T) {
	now := at(2 * lease)
	v := NewVoter("B", lease, VoterState{Incarnation: 42}, 0)
	c := newC(t, Config{Self: "B", Peer: "A", Witness: "W", Lease: lease, Rho: 0.01, Incarnation: 42, Nonce: 0xB}, 0, 0)
	NewAdoption(v, c, 0)
	v.Handle(now, probe("A", 0xA, 1))
	v.Handle(now.Add(ms), probe("A", 0xC, 1))
	v.ClearDuplicateKeeping("A", 0xA, []BootSeq{{Nonce: 0xC, Seq: 1}}, 1)
	now = now.Add(3 * lease)
	r := c.Tick(now, "", 0, true)
	if r == nil {
		t.Fatal("no campaign")
	}
	rep := granted("A", r, 0)
	rep.VoterBoot = 0xC // the retired copy's voter answers
	c.OnReply(now.Add(ms), rep)
	c.OnReply(now.Add(2*ms), granted("B", r, 0)) // its own voter's grant
	if c.Leading(now.Add(3 * ms)) {
		t.Fatal("a retired boot's grant counted toward a lease")
	}
	if !slices.Equal(v.Marked(), []NodeID{"A"}) {
		t.Fatalf("the retired boot's reply marked %v at the controller's own voter, want [A]", v.Marked())
	}
}

// A voter that crashed before its mark's write landed comes back without
// the mark (the fsync window): its unmarked word from the new boot can't
// end the hold its old boot set (rule (i)), but the operator's clear,
// which raises its generation, does: no hold is left that only the held
// controller's restart ends. release_ignores_gen turns it red;
// hold_released_by_any_boot the first half.
func TestAHoldAVoterForgotEndsOnTheClear(t *testing.T) {
	v, now := markA(t)
	v.SetBoot(7)
	c := newC(t, Config{Self: "A", Peer: "B", Witness: "W", Lease: lease, Rho: 0.01, Incarnation: 42, Nonce: 0xA}, 0, 0)
	_, out := v.Handle(now.Add(ms), probe("A", 0xA, 30))
	c.NoteMark(out[0])
	if !c.Marked() {
		t.Fatalf("not held by %+v", out[0])
	}
	// The crash before the write: the file never had the mark.
	later := now.Add(3 * lease)
	r := NewVoter("W", lease, VoterState{}, later)
	r.SetBoot(8)
	_, out = r.Handle(later.Add(ms), probe("A", 0xA, 31))
	c.NoteMark(out[0])
	if !c.Marked() {
		t.Fatalf("the voter that forgot the mark ended the hold with %+v", out[0])
	}
	r.ClearDuplicateKeeping("A", 0xA, nil, 1)
	_, out = r.Handle(later.Add(2*ms), probe("A", 0xA, 32))
	c.NoteMark(out[0])
	if c.Marked() {
		t.Fatalf("the clear's word (%+v) left the hold its forgotten mark set", out[0])
	}
}

// Only a reply matched to one of this boot's rounds from a retired boot
// marks: a late or replayed one, its round long gone, proves nothing
// (#1144 review). retired_reply_unmatched turns it red.
func TestALateReplyFromARetiredBootMarksNothing(t *testing.T) {
	now := at(2 * lease)
	v := NewVoter("B", lease, VoterState{Incarnation: 42}, 0)
	c := newC(t, Config{Self: "B", Peer: "A", Witness: "W", Lease: lease, Rho: 0.01, Incarnation: 42, Nonce: 0xB}, 0, 0)
	NewAdoption(v, c, 0)
	v.Handle(now, probe("A", 0xA, 1))
	v.Handle(now.Add(ms), probe("A", 0xC, 1))
	v.ClearDuplicateKeeping("A", 0xA, []BootSeq{{Nonce: 0xC, Seq: 1}}, 1)
	late := Reply{From: "A", To: "B", Term: 7, Nonce: 0xB, ID: 999, Granted: true, Promised: 7, VoterBoot: 0xC}
	c.OnReply(now.Add(3*lease), late)
	if len(v.Marked()) != 0 {
		t.Fatalf("a late reply, matched to no round, marked %v", v.Marked())
	}
}

// A hold's generation never drops below what this candidate already saw
// from that voter: a word from an older disk of it (a backup restored
// after a wipe) can't end a hold the wiped boot set (#1144 review).
// hold_gen_reset_on_boot turns it red.
func TestAHoldIsSetUnderTheHighestGenerationSeen(t *testing.T) {
	c := newC(t, cfgA(), 0, 0)
	r := func(boot, gen uint64, marked bool) Reply {
		return Reply{From: "W", To: "A", Nonce: 0xA1, Marked: marked, VoterBoot: boot, ClearGen: gen}
	}
	// Schedule 1: W took a clear (gen 1), is wiped (gen 0) and marks; then
	// the pre-wipe disk is restored and answers unmarked at gen 1.
	c.NoteMark(r(1, 1, false))
	c.NoteMark(r(2, 0, true))
	c.NoteMark(r(3, 1, false))
	if !c.Marked() {
		t.Fatal("the restored pre-wipe disk's word (gen 1) ended the hold its wiped boot set")
	}
	// Schedule 2: held under 5; an older copy marks at 1, then answers
	// unmarked at 3.
	c = newC(t, cfgA(), 0, 0)
	c.NoteMark(r(1, 5, true))
	c.NoteMark(r(2, 1, true))
	c.NoteMark(r(3, 3, false))
	if !c.Marked() {
		t.Fatal("an older copy's word (gen 3) ended a hold set under 5")
	}
	c.NoteMark(r(4, 6, false)) // the clear
	if c.Marked() {
		t.Fatal("the clear's word (gen 6) didn't end the hold")
	}
}

// HoldGen covers every generation this candidate saw, not only its holds':
// a hold set between the clear's check and its clear phase starts at the
// highest seen (genSeen), so the clear's generation, above HoldGen, still
// ends it (#1144 re-check). hold_gen_ignores_seen turns it red.
func TestAHoldSetBetweenTheCheckAndTheClearEndsOnTheClear(t *testing.T) {
	c := newC(t, cfgA(), 0, 0)
	r := func(boot, gen uint64, marked bool) Reply {
		return Reply{From: "W", To: "A", Nonce: 0xA1, Marked: marked, VoterBoot: boot, ClearGen: gen}
	}
	c.NoteMark(r(1, 5, false)) // seen 5, not held
	// The witness is restored from a backup at 3: every member says 3 at
	// the check, and the controller its HoldGen.
	gen := max(uint64(3), c.HoldGen()) + 1
	c.NoteMark(r(2, 3, true)) // it marks before the clear phase lands: held under 5
	if !c.Marked() {
		t.Fatal("not held")
	}
	c.NoteMark(r(5, gen, false)) // the clear's word, from a later boot
	if c.Marked() {
		t.Fatalf("the clear's word (generation %d) left a hold set between the check and the clear", gen)
	}
}
