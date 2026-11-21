package quorum

import (
	"errors"
	"reflect"
	"testing"
)

// The spare (#1029 3c-2): a voter names the incumbent of a marked name
// (Reply.HeldBy), and a copy both other voters name another boot of its
// name for a lease demotes itself (Candidate.Spare).

// heldBy is what v's reply to a probe from name under nonce says now.
func heldBy(v *Voter, now Instant, name NodeID, nonce, seq uint64) uint64 {
	_, rs := v.Handle(now, probe(name, nonce, seq))
	return rs[0].HeldBy
}

// The incumbent is the boot heard FIRST among those still sending new
// frames: here the original (0xA, heard first) while both run, on every
// reply to either boot. held_by_last_heard names the clone instead.
func TestAVoterNamesTheFirstHeardBootTheIncumbent(t *testing.T) {
	v, now := markA(t)
	now = now.Add(ms)
	if got := heldBy(v, now, "A", 0xA, 10); got != 0xA {
		t.Fatalf("held by %#x on the original's reply, want 0xA", got)
	}
	if got := heldBy(v, now.Add(ms), "A", 0xC, 10); got != 0xA {
		t.Fatalf("held by %#x on the clone's reply, want 0xA", got)
	}
	// An unmarked name, or none: 0.
	if got := heldBy(v, now.Add(2*ms), "B", 0xB, 1); got != 0 {
		t.Fatalf("held by %#x for an unmarked name, want 0", got)
	}
}

// A boot that sent no new frame within half a lease is no incumbent: once
// the original stops, the clone is the only fresh boot and the voter
// names it. A replayed frame (no higher Seq) is no sign of life. And a
// boot first heard after the mark is never named: the voter weighs only
// the boots it marked over. held_by_stale keeps naming the dead original.
func TestAStoppedBootIsNoIncumbent(t *testing.T) {
	v, now := markA(t)
	// The original's last new frame (Seq 3) was before now; it replays it.
	v.Handle(now.Add(lease/4), probe("A", 0xA, 3))
	for i := uint64(0); i < 4; i++ {
		now = now.Add(lease / 4)
		v.Handle(now, probe("A", 0xC, 10+i))
	}
	if got := heldBy(v, now.Add(ms), "A", 0xC, 20); got != 0xC {
		t.Fatalf("held by %#x with the original silent for a lease, want the clone 0xC", got)
	}
	// A new boot of A after the mark, beside the copy still sending: never
	// named, and never told the copy holds the name.
	for i := uint64(0); i < 4; i++ {
		now = now.Add(lease / 4)
		v.Handle(now, probe("A", 0xD, 1+i))
		v.Handle(now, probe("A", 0xC, 30+i))
	}
	if got := heldBy(v, now.Add(ms), "A", 0xD, 9); got != 0 {
		t.Fatalf("told a boot first heard after the mark that %#x holds the name: an original restarted beside a copy would go spare (held_by_names_to_unknown)", got)
	}
}

// A refusal names the incumbent as of the frame it answers: an original
// whose frames reach the voter more than half a lease apart (a lossy
// link) is fresh again on its own frame, and is told it holds the name,
// not that the copy does. held_by_before_frame names the copy to it.
func TestARefusalCountsTheFrameItAnswers(t *testing.T) {
	v, now := markA(t)
	for i := uint64(0); i < 3; i++ { // the original silent for three quarters of a lease
		now = now.Add(lease / 4)
		v.Handle(now, probe("A", 0xC, 10+i))
	}
	if got := heldBy(v, now.Add(ms), "A", 0xA, 20); got != 0xA {
		t.Fatalf("held by %#x on the original's own frame, want 0xA", got)
	}
}

// A spare flag whose write failed is taken back in memory: a later write
// (an adoption) must not carry it to disk unasked.
// spare_flag_kept_after_failure keeps it.
func TestAFailedSpareWriteTakesTheFlagBack(t *testing.T) {
	v := NewVoter("A", lease, VoterState{Promised: 4, GrantedTo: "A", Incarnation: 9}, 0)
	v.MakeSpare()
	v.SpareWriteFailed()
	if v.Spare() || v.change().State.Spare {
		t.Fatal("the flag of a failed write rides a later write")
	}
}

// A write already handed out after MakeSpare (a duplicate record's, #1029
// 3c-3b) carries the flag; when the flag's own write fails, the voter
// hands out a later write without it, so the disk can't end on a spare
// the controller doesn't know it is (#1144 review). spare_failure_unwritten
// turns it red.
func TestAFailedSpareWriteIsWrittenOver(t *testing.T) {
	v, now := markA(t)
	written(v)
	sp := v.MakeSpare()
	v.Handle(now.Add(lease), probe("A", 0xD, 1)) // a new boot: nothing to mark yet
	v.ClearDuplicate("A")
	rec := v.MarkWrite()
	if rec == nil || !rec.State.Spare || rec.Seq <= sp.Seq {
		t.Fatalf("no record write carrying the flag after it: %+v", rec)
	}
	v.Persisted(sp.Seq, false)
	fix := v.SpareWriteFailed()
	if fix == nil || fix.State.Spare || fix.Seq <= rec.Seq {
		t.Fatalf("the failed spare write left %+v: want a later write without the flag", fix)
	}
}

// A voter never names an incumbent for its own controller's name: its
// state may be the copy's.
func TestAVoterNamesNoIncumbentForItsOwnName(t *testing.T) {
	v := ready("A")
	v.marked = map[NodeID]bool{"A": true}
	v.boots = map[NodeID][]bootSeen{"A": {{nonce: 0xA, first: 0, last: at(lease), seq: 1}}}
	if got := v.HeldBy(at(lease), "A", 0xA); got != 0 {
		t.Fatalf("held by %#x for its own name, want 0", got)
	}
}

// word is a reply from voter from to c's boot naming by.
func word(c *Candidate, from NodeID, by uint64) Reply {
	return Reply{From: from, To: c.cfg.Self, Nonce: c.cfg.Nonce, Term: 0, ID: ProbeID, Marked: true, HeldBy: by}
}

// A copy demotes only when BOTH other voters name the same other boot,
// each for a lease without a break. One voter's word is not enough
// (spare_one_voter), nor a word just heard (spare_no_span), nor two
// voters naming different boots, nor any word naming this very boot.
func TestACopyDemotesOnlyOnBothOtherVotersWordForALease(t *testing.T) {
	cfg := cfgA()
	now := at(2 * lease)
	c := newC(t, cfg, 0, 0)
	c.NoteHeld(now, word(c, "W", 0xA0))
	c.NoteHeld(now, word(c, "B", 0xA0))
	if c.Spare(now) {
		t.Fatal("a spare on the first word")
	}
	c.NoteHeld(now.Add(lease), word(c, "W", 0xA0))
	if c.Spare(now.Add(lease)) {
		t.Fatal("a spare on one voter's word held a lease")
	}
	c.NoteHeld(now.Add(lease), word(c, "B", 0xA0))
	if !c.Spare(now.Add(lease)) {
		t.Fatal("no spare with both other voters naming 0xA0 for a lease")
	}

	// They name different boots: no.
	c = newC(t, cfg, 0, 0)
	for _, d := range []Instant{now, now.Add(lease)} {
		c.NoteHeld(d, word(c, "W", 0xA0))
		c.NoteHeld(d, word(c, "B", 0xA2))
	}
	if c.Spare(now.Add(lease)) {
		t.Fatal("a spare with the voters naming different boots")
	}

	// One names this boot: no.
	c = newC(t, cfg, 0, 0)
	for _, d := range []Instant{now, now.Add(lease)} {
		c.NoteHeld(d, word(c, "W", cfg.Nonce))
		c.NoteHeld(d, word(c, "B", 0xA0))
	}
	if c.Spare(now.Add(lease)) {
		t.Fatal("a spare though the witness names this boot")
	}

	// A changed word starts over.
	c = newC(t, cfg, 0, 0)
	c.NoteHeld(now, word(c, "W", 0xA0))
	c.NoteHeld(now, word(c, "B", 0xA0))
	c.NoteHeld(now.Add(lease/2), word(c, "B", 0))
	c.NoteHeld(now.Add(lease/2+ms), word(c, "B", 0xA0))
	c.NoteHeld(now.Add(lease), word(c, "W", 0xA0))
	c.NoteHeld(now.Add(lease), word(c, "B", 0xA0))
	if c.Spare(now.Add(lease)) {
		t.Fatal("a spare though the peer's word broke half a lease ago")
	}
}

// Only the two OTHER voters' words count, and only on replies to this
// very boot: its own voter's state may be the copy's, and a reply to an
// earlier boot says nothing of this one. spare_own_voter_counts lets its
// own voter's word stand in for the peer's.
func TestOnlyTheOtherVotersWordsToThisBootCount(t *testing.T) {
	cfg := cfgA()
	now := at(2 * lease)
	c := newC(t, cfg, 0, 0)
	old := c.cfg.Nonce + 1
	for _, d := range []Instant{now, now.Add(lease)} {
		c.NoteHeld(d, word(c, "A", 0xA0)) // its own voter
		c.NoteHeld(d, word(c, "W", 0xA0))
		r := word(c, "B", 0xA0)
		r.Nonce = old // to an earlier boot
		c.NoteHeld(d, r)
	}
	if c.Spare(now.Add(lease)) {
		t.Fatal("a spare on its own voter's word and a reply to another boot")
	}
}

// A copy holding a lease is never demoted while it holds it, whatever the
// voters say: it leads, so it can't be the copy they agree on while they
// renew it. A guard only (it never fires in the simulator): the rule may
// change. spare_while_leading demotes it.
func TestALeaseHolderIsNeverDemoted(t *testing.T) {
	cfg := cfgA()
	c := newC(t, cfg, 0, 0)
	r := c.Tick(0, "", 0, true)
	c.OnReply(Instant(ms), granted("A", r, 0))
	c.OnReply(Instant(ms), granted("W", r, 42))
	if !c.Leading(Instant(ms)) {
		t.Fatal("no lease")
	}
	// It keeps renewing for more than a lease while both other voters
	// name another boot on every reply.
	now := Instant(ms)
	for now < Instant(lease+lease/2) {
		now = now.Add(lease / 4)
		rn := c.Tick(now, "", 0, true)
		if rn == nil {
			t.Fatalf("no renewal at %v", now)
		}
		for _, from := range []NodeID{"A", "W", "B"} {
			g := granted(from, rn, 42)
			if from != "A" {
				g.Marked, g.HeldBy = true, 0xA0
				c.NoteHeld(now.Add(ms), g)
			}
			c.OnReply(now.Add(ms), g)
		}
	}
	if !c.Leading(now.Add(ms)) {
		t.Fatal("the lease ran out")
	}
	if c.Spare(now.Add(ms)) {
		t.Fatal("a leader demoted under its own lease")
	}
}

// The spare flag: MakeSpare writes it, every later write keeps it (it is
// in the voter's state), and only a spare's file is format 6, so a member
// that is no spare can still go back to an earlier release. A newer
// format reads as ErrVersion. state_always_v6 writes every file as 6.
func TestTheSpareFlagIsDurableAndOnlyASpareIsFormat6(t *testing.T) {
	v := NewVoter("A", lease, VoterState{Promised: 4, GrantedTo: "A", Incarnation: 9}, 0)
	b, _ := v.State().MarshalBinary()
	if b[len(stateMagic)] != 5 {
		t.Fatalf("a member's file is format %d, want 5", b[len(stateMagic)])
	}
	p := v.MakeSpare()
	if p == nil || !p.State.Spare {
		t.Fatalf("MakeSpare wrote %+v", p)
	}
	b, _ = p.State.MarshalBinary()
	if b[len(stateMagic)] != 6 {
		t.Fatalf("a spare's file is format %d, want 6", b[len(stateMagic)])
	}
	var back VoterState
	if err := back.UnmarshalBinary(b); err != nil || !reflect.DeepEqual(back, p.State) {
		t.Fatalf("round trip: %+v %v", back, err)
	}
	if NewVoter("A", lease, back, 0).Spare() != true {
		t.Fatal("a restarted voter forgot it is a spare")
	}
	// A later write (an adoption, a grant) keeps the flag.
	if q := v.change(); !q.State.Spare {
		t.Fatal("a later write dropped the spare flag")
	}
	if q := v.MakeSpare(); q == nil || !q.State.Spare || q.Seq <= p.Seq {
		t.Fatalf("asked again (its write failed), MakeSpare wrote %+v", q)
	}
	if q := v.Unspare(); q == nil || q.State.Spare {
		t.Fatalf("Unspare wrote %+v", q)
	}
	// Format 9 (a later release): ErrVersion, never read as this one.
	b[len(stateMagic)] = stateVersion + 1
	var x VoterState
	if err := x.UnmarshalBinary(sealCRC(b[:len(b)-4])); !errors.Is(err, ErrVersion) {
		t.Fatalf("a format-9 file: %v, want ErrVersion", err)
	}
	// A bad flag byte is corrupt.
	b[len(stateMagic)] = 6
	b[len(b)-5] = 2
	if err := x.UnmarshalBinary(sealCRC(b[:len(b)-4])); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("a flag byte of 2: %v, want ErrCorrupt", err)
	}
}
