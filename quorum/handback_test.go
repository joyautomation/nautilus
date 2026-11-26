package quorum

import (
	"errors"
	"testing"
	"time"
)

// The bad-retain hand-back's pieces in the vote protocol (#710 S2c-3,
// handback.go and the epoch records' earlier-boot value).

// A campaign's grant hides nothing of the asker's earlier boots: the
// reply to a boot reports, as the asker's own record, the highest epoch
// granted to an EARLIER boot of its name, whatever this boot campaigned
// in since. The record keeps it across a restart. A boot that led in
// epoch 5 restarts and campaigns at 9, then 11: both replies say 5.
// prior_boot_overwritten and own_record_after_grant turn it red.
func TestAReplyTellsTheAskerOfItsEarlierBootsOnly(t *testing.T) {
	v := ready("W")
	now := at(lease)
	if !grant(t, v, now, Request{From: "A", Term: 5, Nonce: 0xA1, ID: 1}) {
		t.Fatal("boot 1's campaign refused")
	}
	own := func(term uint64, id uint64) uint64 {
		t.Helper()
		now = now.Add(2 * lease) // the last promise has run out
		p, out := v.Handle(now, Request{From: "A", Term: term, Nonce: 0xA2, ID: id})
		if p != nil {
			out = append(out, v.Persisted(p.Seq, true)...)
		}
		if len(out) != 1 || !out[0].Granted {
			t.Fatalf("boot 2's campaign at %d: %+v", term, out)
		}
		r, ok := out[0].Recs.Of("A")
		if !ok {
			t.Fatalf("no record of A in %+v", out[0].Recs)
		}
		return r.Epoch
	}
	if e := own(9, 1); e != 5 {
		t.Fatalf("boot 2's first campaign is told A's earlier epoch %d, want 5", e)
	}
	if e := own(11, 2); e != 5 {
		t.Fatalf("boot 2's second campaign is told A's earlier epoch %d, want 5 (its own failed campaign hid boot 1's leadership)", e)
	}
	if v.EpochOf("A") != 11 {
		t.Fatalf("A's record %d, want 11", v.EpochOf("A"))
	}
	r := restart(t, v, now.Add(time.Second), 2)
	if got := r.st.Epochs; len(got) != 1 || got[0] != (EpochRecord{Name: "A", Epoch: 11, Nonce: 0xA2, Prior: 5}) {
		t.Fatalf("after a restart: %+v", got)
	}
	// The peer sees every boot's: B asks, A's record is 11.
	later := now.Add(3 * lease)
	p, out := r.Handle(later, Request{From: "B", Term: 12, Nonce: 0xB, ID: 1})
	if p != nil {
		out = append(out, r.Persisted(p.Seq, true)...)
	}
	if len(out) != 1 {
		t.Fatalf("B's campaign: %+v", out)
	}
	if rec, ok := out[0].Recs.Of("A"); !ok || rec.Epoch != 11 {
		t.Fatalf("B is told A's record %+v, want every boot's (11)", out[0].Recs)
	}
}

// Format 10 carries a record's boot, its earlier-boot epoch and the
// hand-back time, and is written only once one is set: records without a
// boot (as a file read from format 9 holds until the next grant) stay in
// format 9. Both read back whole.
func TestStateFormatTenOnlyOnceSet(t *testing.T) {
	nine := VoterState{Promised: 9, GrantedTo: "A", Incarnation: 42, GrantNonce: 0xA, RecordsFrom: 3,
		Epochs: []EpochRecord{{Name: "A", Epoch: 9}, {Name: "B", Epoch: 7}}}
	ten := nine
	ten.Epochs = []EpochRecord{{Name: "A", Epoch: 9, Nonce: 0xA, Prior: 6}, {Name: "B", Epoch: 7}}
	handed := VoterState{Promised: 9, GrantedTo: "A", Incarnation: 42, GrantNonce: 0xA, HandedBackMs: 1_700_000_000_000}
	for _, c := range []struct {
		name string
		st   VoterState
		ver  byte
	}{{"records with no boot", nine, stateVersionEpoch}, {"a record's boot", ten, stateVersion}, {"a hand-back, no records", handed, stateVersion}} {
		b, err := c.st.MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		if b[len(stateMagic)] != c.ver {
			t.Errorf("%s: format %d, want %d", c.name, b[len(stateMagic)], c.ver)
		}
		var got VoterState
		if err := got.UnmarshalBinary(b); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got.HandedBackMs != c.st.HandedBackMs || got.RecordsFrom != c.st.RecordsFrom || len(got.Epochs) != len(c.st.Epochs) {
			t.Fatalf("%s: read back %+v, want %+v", c.name, got, c.st)
		}
		for i := range got.Epochs {
			if got.Epochs[i] != c.st.Epochs[i] {
				t.Fatalf("%s: record %d read back %+v, want %+v", c.name, i, got.Epochs[i], c.st.Epochs[i])
			}
		}
	}
	// A record whose earlier-boot epoch is above its own is damage.
	bad := ten
	bad.Epochs = []EpochRecord{{Name: "A", Epoch: 9, Nonce: 0xA, Prior: 10}}
	b, _ := bad.MarshalBinary()
	if err := new(VoterState).UnmarshalBinary(b); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("a prior above the epoch: %v, want ErrCorrupt", err)
	}
}

// The crash-loop guard: once per window, on the wall clock, a time in the
// future ignored. handback_guard_off and handback_future_held turn it red.
func TestHandBackGuard(t *testing.T) {
	const l = 500 * time.Millisecond
	w := HandBackEvery(l)
	if w != time.Minute {
		t.Fatalf("window at a 500 ms lease: %s, want the 1 min floor", w)
	}
	if HandBackEvery(5*time.Second) != 100*time.Second {
		t.Fatalf("window at a 5 s lease: %s, want 20 leases", HandBackEvery(5*time.Second))
	}
	now := int64(1_700_000_000_000)
	for _, c := range []struct {
		name string
		last int64
		want bool
	}{
		{"never", 0, false},
		{"just now", now - 1, true},
		{"inside the window", now - w.Milliseconds() + 1, true},
		{"past the window", now - w.Milliseconds(), false},
		{"in the future (the clock went back)", now + 10_000, false},
	} {
		if got := HandBackHeld(c.last, now, l); got != c.want {
			t.Errorf("%s: held %v, want %v", c.name, got, c.want)
		}
	}
}

// The hand-back's floor: the newest earlier leadership the winning round
// knows, the peer's or this controller's own earlier boots', and only
// when two winning replies cover every grant above it. A now leads in 9.
// handback_own_record_ignored and handback_unknown_counts turn it red.
func TestHandBackFloor(t *testing.T) {
	rep := func(from NodeID, recFrom, a, b uint64) WonReply {
		return WonReply{From: from, RecordsFrom: recFrom, Recs: Recs{{Name: "A", Epoch: a}, {Name: "B", Epoch: b}}}
	}
	check := func(ws ...WonReply) HotCheck { return HotCheck{Self: "A", Peer: "B", Epoch: 9, Won: ws} }
	for _, c := range []struct {
		name  string
		h     HotCheck
		floor uint64
		why   string
	}{
		{"B led last", check(rep("A", 1, 3, 5), rep("W", 1, 3, 5)), 5, ""},
		// A led 7 in an earlier boot, B away: B's file at 5 misses it.
		{"A led last, in an earlier boot", check(rep("A", 1, 7, 5), rep("W", 1, 0, 5)), 7, ""},
		{"one voter knows of A's leadership", check(rep("A", 1, 0, 5), rep("W", 1, 7, 5)), 7, ""},
		{"a rebuilt witness", check(rep("A", 1, 3, 5), rep("W", 0, 0, 0)), 0, HandBackRecordsUnknown},
		{"records from after the floor", check(rep("A", 1, 3, 5), rep("W", 7, 0, 0)), 0, HandBackRecordsUnknown},
		{"records from just after the floor", check(rep("A", 1, 3, 5), rep("W", 6, 0, 0)), 5, ""},
		// An older build's voter reports A's current epoch as its own record.
		{"a voter tells A of its current epoch", check(rep("A", 1, 3, 5), rep("W", 1, 9, 5)), 0, HandBackRecordsUnknown},
		{"not leading", HotCheck{Self: "A", Peer: "B"}, 0, HandBackNotLeading},
	} {
		floor, ok, why := c.h.HandBackFloor()
		if floor != c.floor || ok != (c.why == "") || why != c.why {
			t.Errorf("%s: floor %d ok %v %q; want %d %q", c.name, floor, ok, why, c.floor, c.why)
		}
	}
	// All three voters answered and cover: a grant only one of them made
	// (B's own voter, B's lost campaign at 6 in a split vote) is no
	// leadership, so the floor is the second-highest record. With one
	// voter's records short of that, it is the highest again.
	// handback_floor_max_only turns it red.
	for _, c := range []struct {
		name  string
		h     HotCheck
		floor uint64
	}{
		{"one voter's lost campaign of B", check(rep("A", 1, 0, 5), rep("B", 1, 0, 6), rep("W", 1, 0, 5)), 5},
		{"one voter's lost campaign of A's earlier boot", check(rep("A", 1, 7, 5), rep("B", 1, 0, 5), rep("W", 1, 0, 5)), 5},
		{"a leadership two voters granted", check(rep("A", 1, 0, 5), rep("B", 1, 0, 6), rep("W", 1, 0, 6)), 6},
		{"one voter covers too little for the second", check(rep("A", 1, 0, 5), rep("B", 1, 0, 6), rep("W", 7, 0, 0)), 6},
	} {
		if floor, ok, why := c.h.HandBackFloor(); !ok || floor != c.floor {
			t.Errorf("%s: floor %d ok %v %q, want %d", c.name, floor, ok, why, c.floor)
		}
	}
	// A covering reply that names a third controller above the floor
	// leaves the peer's record there unknown.
	third := check(rep("A", 1, 0, 5), WonReply{From: "W", RecordsFrom: 1, Recs: Recs{{Name: "A"}, {Name: "C", Epoch: 8}}})
	if _, ok, why := third.HandBackFloor(); ok || why != HandBackRecordsUnknown {
		t.Errorf("a third name above the floor: ok %v %q", ok, why)
	}
	// This boot's own earlier leadership counts too.
	h := check(rep("A", 1, 0, 5), rep("W", 1, 0, 5))
	h.LedBefore = 8
	if floor, ok, _ := h.HandBackFloor(); !ok || floor != 8 {
		t.Fatalf("this boot led 8: floor %d ok %v, want 8", floor, ok)
	}
}

// A discounted voter's reply (two processes cast its vote, or a retired
// boot of it answered) never vouches for a hot takeover: it is left out
// of the winning round's replies, so a check it alone would complete
// stays cold (naut-lead's S5 ruling on #1345). Both sources of the
// discount. discounted_vouches turns it red.
func TestADiscountedReplyNeverVouches(t *testing.T) {
	for _, src := range []string{"duplicated", "retired"} {
		t.Run(src, func(t *testing.T) {
			c := newC(t, cfgA(), 0, 0)
			c.Count(42, 0)
			switch src {
			case "duplicated":
				c.duplicated = func(_ Instant, name NodeID) bool { return name == "B" }
			case "retired":
				c.retiredReply = func(_ Instant, name NodeID, _ uint64) bool { return name == "B" }
			}
			r := c.Tick(0, "", 0, true)
			if r == nil {
				t.Fatal("no campaign")
			}
			// A's own voter knows nothing; the witness and B vouch for B's
			// image at epoch 5, sequence 40.
			c.OnReply(at(ms), granted("A", r, 0))
			for _, from := range []NodeID{"B", "W"} {
				g := granted(from, r, 0)
				if from == "W" {
					g.Incarnation = 42
				}
				g.RecordsFrom, g.Recs = 1, Recs{{Name: "B", Epoch: 5, Mark: cur(40)}}
				c.OnReply(at(ms), g)
			}
			if !c.Leading(at(ms)) {
				t.Fatal("not leading on A and W")
			}
			h := c.HotCheck()
			for _, w := range h.Won {
				if w.From == "B" {
					t.Fatalf("the discounted reply of B is in the winning round: %+v", h.Won)
				}
			}
			if hot, why := h.Verdict(5, 40); hot || why != ColdRecordsUnknown {
				t.Fatalf("hot %v %q; want cold (%s): only the discounted voter's word made two", hot, why, ColdRecordsUnknown)
			}
		})
	}
}
