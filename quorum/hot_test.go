package quorum

import (
	"testing"
	"time"
)

// The hot takeover's check against the voters (#710 S2c-1, hot.go). A
// takes over from an image of B's state at epoch 5, sequence 40, and now
// leads in epoch 9.

func cur(seq uint64) Mark  { return Mark{Known: true, Current: true, Term: 5, Scan: seq} }
func past(seq uint64) Mark { return Mark{Known: true, Term: 5, Scan: seq} }

// won is a reply recording B at epoch be with mark m, and A at ae.
func won(from NodeID, recFrom, be uint64, m Mark, ae uint64) WonReply {
	return WonReply{From: from, RecordsFrom: recFrom, Recs: Recs{{Name: "A", Epoch: ae}, {Name: "B", Epoch: be, Mark: m}}}
}

// Each rule, and a cold reason for each way it fails.
// unknown_counts_toward_majority, own_record_ignored, mark_tie_current_wins,
// one_mark_enough and evidence_from_covering_only turn it red.
func TestHotVerdict(t *testing.T) {
	check := func(ws ...WonReply) HotCheck {
		return HotCheck{Self: "A", Peer: "B", Epoch: 9, Won: ws, Campaigns: []uint64{7, 9}}
	}
	for _, c := range []struct {
		name string
		h    HotCheck
		want string
	}{
		{"in sync, both known", check(won("A", 3, 5, cur(40), 9), won("W", 1, 5, cur(40), 9)), ""},
		{"the image past the mark", check(won("A", 3, 5, cur(39), 9), won("W", 1, 5, cur(39), 9)), ""},
		{"the newest mark is another voter's", check(won("A", 3, 5, cur(38), 9), won("W", 1, 5, cur(40), 9)), ""},
		// A rebuilt witness (no records yet) may be the only voter that saw a later grant.
		{"one voter's records unknown", check(won("A", 3, 5, cur(40), 9), won("W", 0, 5, cur(40), 9)), ColdRecordsUnknown},
		{"records from just after the image", check(won("A", 3, 5, cur(40), 9), won("W", 6, 5, cur(40), 9)), ""},
		{"records from after the image", check(won("A", 3, 5, cur(40), 9), won("W", 7, 5, cur(40), 9)), ColdRecordsUnknown},
		{"B led again after the image", check(won("A", 3, 5, cur(40), 9), won("W", 1, 8, cur(40), 9)), ColdLaterLeader},
		{"no voter granted the image's epoch", check(won("A", 3, 4, cur(40), 9), won("W", 1, 4, cur(40), 9)), ColdImageUnseen},
		{"no mark", check(won("A", 3, 5, Mark{}, 9), won("W", 1, 5, Mark{}, 9)), ColdNoMark},
		{"a mark of another epoch", check(won("A", 3, 5, Mark{Known: true, Current: true, Term: 4, Scan: 40}, 9), won("W", 1, 5, Mark{Known: true, Current: true, Term: 4, Scan: 40}, 9)), ColdMarkOtherEpoch},
		// W restarted (no mark) after A's last word to it was "behind": A's
		// own voter's older "current" is not enough (Fable, #1345 M2).
		{"one voter has no mark", check(won("A", 3, 5, cur(40), 9), won("W", 1, 5, Mark{}, 9)), ColdNoMark},
		// Evidence against hot counts from a reply that doesn't cover the
		// image's epoch too (Fable, #1345 S2).
		{"a later epoch from a non-covering reply", check(won("A", 3, 5, cur(40), 9), won("W", 1, 5, cur(40), 9), won("B", 9, 7, Mark{}, 9)), ColdLaterLeader},
		{"a newer word, behind, from a non-covering reply", check(won("A", 3, 5, cur(40), 9), won("W", 1, 5, cur(40), 9), won("B", 9, 5, past(41), 9)), ColdNotCurrent},
		{"the standby was behind", check(won("A", 3, 5, past(40), 9), won("W", 1, 5, past(40), 9)), ColdNotCurrent},
		{"the image behind the mark", check(won("A", 3, 5, cur(41), 9), won("W", 1, 5, cur(41), 9)), ColdBehindMark},
		// The old leader died between its two renewal sends: one voter has the later word.
		{"a tie: not current wins", check(won("A", 3, 5, cur(40), 9), won("W", 1, 5, past(40), 9)), ColdNotCurrent},
		// A's own campaign at 7 never led: its record is no leadership.
		{"own lost campaign recorded", check(won("A", 3, 5, cur(40), 9), won("W", 1, 5, cur(40), 7)), ""},
		// A record of A at 6 from an earlier boot (#710 S2c-3: a reply's
		// own-name record is of earlier boots): a campaign that never led,
		// since the image is of this boot, from a later leadership.
		{"an earlier boot's lost campaign", check(won("A", 3, 5, cur(40), 9), won("W", 1, 5, cur(40), 6)), ""},
	} {
		hot, why := c.h.Verdict(5, 40)
		if hot != (c.want == "") || why != c.want {
			t.Errorf("%s: hot %v %q, want %q", c.name, hot, why, c.want)
		}
	}
	// This boot led in epoch 7, after the image, before the current lease.
	h := check(won("A", 3, 5, cur(40), 9), won("W", 1, 5, cur(40), 9))
	h.LedBefore = 7
	if hot, why := h.Verdict(5, 40); hot || why != ColdOwnLeadership {
		t.Errorf("this boot led after the image: hot %v %q", hot, why)
	}
}

// Only the replies of the round that won count: an earlier round's,
// though still kept, can predate a leadership between them.
// mark_from_losing_round turns it red.
func TestHotCheckReadsTheWinningRoundOnly(t *testing.T) {
	c := newC(t, cfgA(), 0, 0)
	c.Count(42, 0)
	r1 := c.Tick(0, "", 0, true)
	// The witness refuses the first campaign, with B in sync then.
	w := refused("W", r1, r1.Term+1, 42)
	w.RecordsFrom, w.Recs = 1, Recs{{Name: "B", Epoch: 5, Mark: cur(40)}}
	c.OnReply(at(10*ms), w)
	c.OnReply(at(10*ms), refused("A", r1, r1.Term+1, 0))
	var r2 *Request
	var sent time.Duration
	for i := 1; r2 == nil && i < 100; i++ {
		sent = time.Duration(i) * 10 * ms
		r2 = c.Tick(at(sent), "", 0, true)
	}
	if r2 == nil || r2.Term <= r1.Term {
		t.Fatalf("no second campaign: %+v", r2)
	}
	// It wins on A and B, whose word is that B fell behind.
	for _, from := range []NodeID{"A", "B"} {
		g := granted(from, r2, 0)
		g.RecordsFrom, g.Recs = 1, Recs{{Name: "B", Epoch: 5, Mark: past(39)}}
		c.OnReply(at(sent+10*ms), g) // the first round is still kept
	}
	if !c.Leading(at(sent + 10*ms)) {
		t.Fatal("not leading")
	}
	h := c.HotCheck()
	if len(h.Won) != 2 || h.Epoch != r2.Term {
		t.Fatalf("the check: %+v", h)
	}
	if hot, why := h.Verdict(5, 40); hot || why != ColdNotCurrent {
		t.Fatalf("hot %v %q; want cold on the winning round's word (%s)", hot, why, ColdNotCurrent)
	}
}

// Residual S3 (#710 S2c-1, Fable on #1345): B led in epoch 5 (the image),
// then again in 8 under the OLD witness incarnation while A was cut off.
// The witness was rebuilt and adopted at floor 8; its first grant, a
// campaign of A's at 6 before the adoption, set its RecordsFrom to 6, so
// its reply claims to cover epoch 5 and knows nothing of 8. A's own voter
// never heard of 8 either. The check must not take the witness's word for
// any epoch at or below the floor. witness_covers_below_floor turns it red.
func TestHotVerdictWitnessBelowItsFloor(t *testing.T) {
	check := func(floor uint64, ws ...WonReply) HotCheck {
		return HotCheck{Self: "A", Peer: "B", Epoch: 9, Won: ws, Campaigns: []uint64{6, 9}, Witness: "W", WitnessFloor: floor}
	}
	for _, c := range []struct {
		name string
		h    HotCheck
		want string
	}{
		// The coverage rule alone: in the S3 history a rebuilt witness
		// can't hold a mark of B's epoch 5 (its marks come from renewals
		// it granted, all at 8 or later), so the two realistic rows
		// below are cold on the marks too.
		{"the S3 history, coverage only", check(8, won("A", 3, 5, cur(40), 9), won("W", 6, 5, cur(40), 9)), ColdWitnessRebuilt},
		{"the S3 history, the witness with no mark", check(8, won("A", 3, 5, cur(40), 9), won("W", 6, 5, Mark{}, 9)), ColdWitnessRebuilt},
		{"the S3 history, the witness's mark at 8", check(8, won("A", 3, 5, cur(40), 9), won("W", 6, 5, Mark{Known: true, Current: true, Term: 8, Scan: 3}, 9)), ColdWitnessRebuilt},
		{"a floor below the image's epoch", check(4, won("A", 3, 5, cur(40), 9), won("W", 1, 5, cur(40), 9)), ""},
		{"no floor (no adoption wired)", check(0, won("A", 3, 5, cur(40), 9), won("W", 6, 5, cur(40), 9)), ""},
		// Records that start above the floor are the new incarnation's own.
		{"records from above the floor", check(4, won("A", 3, 5, cur(40), 9), won("W", 6, 5, cur(40), 9)), ""},
		// 0 covers nothing, and the floor does not turn it into coverage.
		{"no records stays unknown", check(8, won("A", 3, 5, cur(40), 9), won("W", 0, 5, cur(40), 9)), ColdRecordsUnknown},
		// Only the witness: the controllers' voters keep their records.
		{"a controller's voter below the floor", check(8, won("A", 3, 5, cur(40), 9), won("B", 3, 5, cur(40), 9)), ""},
		// Both controllers' voters elected A: hot despite the rebuilt witness.
		{"both controllers' voters", check(8, won("A", 3, 5, cur(40), 9), won("B", 3, 5, cur(40), 9), won("W", 6, 5, cur(40), 9)), ""},
	} {
		hot, why := c.h.Verdict(5, 40)
		if hot != (c.want == "") || why != c.want {
			t.Errorf("%s: hot %v %q, want %q", c.name, hot, why, c.want)
		}
	}
	// The hand-back floor reads coverage the same way, and has no marks
	// rule to fall back on: the witness's word can't put the floor at 5
	// when B may have led at 8 (A's own records of earlier boots: none).
	h := check(8, won("A", 3, 5, Mark{}, 0), won("W", 6, 5, Mark{}, 0))
	if floor, ok, why := h.HandBackFloor(); ok || why != HandBackRecordsUnknown {
		t.Errorf("hand-back: floor %d ok %v %q, want refused as %s", floor, ok, why, HandBackRecordsUnknown)
	}
	// With the floor below the image's epoch the witness's word stands.
	if floor, ok, _ := check(4, won("A", 3, 5, Mark{}, 0), won("W", 6, 5, Mark{}, 0)).HandBackFloor(); !ok || floor != 5 {
		t.Errorf("hand-back with a floor below: floor %d ok %v, want 5", floor, ok)
	}
}

// The adoption gives the candidate its voter's term floor for the witness
// check, read when the check is made: a floor an adoption or a
// recommission raises later is the one the check uses (#710 S3).
// witness_floor_unwired turns it red.
func TestAdoptionGivesTheHotCheckTheFloor(t *testing.T) {
	x := newCtl(t, "A", VoterState{Gen: 1, Incarnation: incI, Floor: 8}, 0)
	if x.c.ownFloor == nil || x.c.ownFloor() != 8 {
		t.Fatal("the candidate does not read its voter's floor")
	}
	x.v.st.Floor = 12 // raised later
	if got := x.c.ownFloor(); got != 12 {
		t.Fatalf("floor %d after a raise, want 12: read when the check is made", got)
	}
}

// Residual S3 through a real candidate and its adoption (the hand-back
// path, which has no marks rule to fall back on): A's voter's floor is 8
// (the adoption of the rebuilt witness incarnation incI), B led in 5 and,
// unknown to A's voter, in 8. A wins on its own voter and the witness,
// whose records start at 6. Its hand-back floor must not be 5.
// witness_floor_unwired and witness_covers_below_floor turn it red.
func TestHandBackFloorAfterAWitnessRebuild(t *testing.T) {
	x := newCtl(t, "A", VoterState{Gen: 1, Incarnation: incI, Floor: 8, Promised: 8}, 0)
	var r *Request
	var sent time.Duration
	for i := 1; r == nil && i < 400; i++ {
		sent = time.Duration(i) * 10 * ms
		r = x.c.Tick(at(sent), "", 0, true)
	}
	if r == nil || r.Term <= 8 {
		t.Fatalf("no campaign above the floor: %+v", r)
	}
	own := granted("A", r, 0)
	own.RecordsFrom, own.Recs = 3, Recs{{Name: "B", Epoch: 5}}
	wit := granted("W", r, incI)
	wit.RecordsFrom, wit.Recs = 6, Recs{{Name: "B", Epoch: 5}}
	x.c.OnReply(at(sent+10*ms), own)
	x.c.OnReply(at(sent+10*ms), wit)
	if !x.c.Leading(at(sent + 10*ms)) {
		t.Fatal("not leading")
	}
	if floor, ok, why := x.c.HotCheck().HandBackFloor(); ok || why != HandBackRecordsUnknown {
		t.Fatalf("hand-back floor %d ok %v %q: the rebuilt witness's records can't say B did not lead after 5", floor, ok, why)
	}
}
