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
