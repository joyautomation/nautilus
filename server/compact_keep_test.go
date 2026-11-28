//go:build !js

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	goruntime "runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/oplog"
	"github.com/joyautomation/nautilus/retain"
)

// inputOf is a CompactInput over es (the committed log, in order), each
// Lapsed as the log marks it at open.
func inputOf(es []oplog.Entry, t time.Time, through uint64) oplog.CompactInput {
	lapsed := map[uint64]bool{}
	for _, e := range es {
		if e.Kind == oplog.KindLapse {
			for _, r := range e.Refs {
				lapsed[r] = true
			}
		}
	}
	var commit uint64
	if len(es) > 0 {
		commit = es[len(es)-1].Seq
	}
	return oplog.CompactInput{T: t, Through: through, Commit: commit, Entries: func(f func(oplog.Entry) bool) {
		for _, e := range es {
			e.Lapsed = lapsed[e.Seq] && e.Kind != oplog.KindLapse
			if !f(e) {
				return
			}
		}
	}}
}

// randomLog is a committed log of every kind the models read, with lots
// of churn (acks cleared, shelves undone, substitutes ended, sessions
// signed out) and some state left standing at its end.
func randomLog(r *rand.Rand, n int, start time.Time) []oplog.Entry {
	alarms := []string{"A1", "A2", "A3", "A4"}
	equip := []string{"Pump_1", "pump_1", "Tank_2"}
	addrs := []string{"TT_101", "tt_101", "LT_7", "FT_9.Value"}
	reqs := []string{"", "", "r1", "r2"}
	hashes := []string{"h1", "h2", "h3"}
	links := []string{"", "", "", "L1", "L2", "L3"}
	pick := func(xs []string) string { return xs[r.Intn(len(xs))] }
	var es []oplog.Entry
	ts := start.UnixMilli()
	occ := map[string]string{}
	var starts, acks []uint64
	day := 24 * time.Hour.Milliseconds()
	for i := 1; i <= n; i++ {
		ts += r.Int63n(3 * time.Hour.Milliseconds())
		e := oplog.Entry{Seq: uint64(i), Term: 1, TS: ts, By: "op"}
		id := pick(alarms)
		switch k := r.Intn(19); k {
		case 0, 1:
			e.Kind, e.IDs = oplog.KindAck, []string{id}
			if r.Intn(3) == 0 {
				e.IDs = append(e.IDs, pick(alarms))
			}
			if r.Intn(4) > 0 {
				for _, a := range e.IDs {
					if occ[a] != "" {
						e.Occs = append(e.Occs, oplog.Occ{ID: a, Occ: occ[a], ActiveMs: ts - 1000})
					}
				}
			}
			e.Link = pick(links)
			acks = append(acks, e.Seq)
		case 2:
			e.Kind, e.IDs = oplog.KindAckClear, []string{id}
			if r.Intn(2) == 0 {
				e.Occs = []oplog.Occ{{ID: id, Occ: occ[id]}}
			}
		case 3:
			o := fmt.Sprintf("o%d", i)
			occ[id] = o
			e.Kind, e.Occs = oplog.KindOccurrenceStart, []oplog.Occ{{ID: id, Occ: o, ActiveMs: ts, WaitMs: r.Int63n(5000)}}
		case 4:
			e.Kind, e.Occs = oplog.KindOccurrenceEnd, []oplog.Occ{{ID: id, Occ: occ[id]}}
		case 5:
			e.Kind, e.ID, e.Until = oplog.KindShelve, id, ts+r.Int63n(20*day)
			acks = append(acks, e.Seq)
		case 6:
			e.Kind, e.ID = oplog.KindUnshelve, id
		case 7:
			e.Kind, e.ID, e.Until, e.Reason = oplog.KindOutOfService, pick(equip), ts+r.Int63n(20*day), "work"
			acks = append(acks, e.Seq)
		case 8:
			e.Kind, e.ID = oplog.KindInService, pick(equip)
			if r.Intn(2) == 0 && len(acks) > 0 {
				e.Ref = acks[r.Intn(len(acks))]
			}
		case 9:
			b := detectEntry{V: detectEntryVersion}
			switch r.Intn(4) {
			case 0:
				b.Flood = &retain.AlarmFlood{ID: fmt.Sprintf("f%d", r.Intn(3)), SinceMs: ts, FirstOut: []retain.AlarmFloodAct{{ID: id}}}
			case 1:
				b.FloodEnd = fmt.Sprintf("f%d", r.Intn(3))
			case 2:
				b.Chatter = []chatterEpisode{{ID: id, SinceMs: ts - ts%(day/4), Acts: r.Intn(9) + 1}}
			case 3:
				b.ChatterEnd = []chatterEpisode{{ID: id, SinceMs: ts - ts%(day/4)}}
			}
			e.Kind, e.Value = oplog.KindAlarmDetect, mustJSON(b)
		case 10, 11, 12:
			g := r.Int63n(7*day) + 1
			e.Kind, e.Tag, e.Value, e.GrantedMs, e.Until, e.Req = oplog.KindSubstitute, pick(addrs), mustJSON(r.Intn(3)), g, ts+g, pick(reqs)
			e.AckOnStart = r.Intn(2) == 0
			starts = append(starts, e.Seq)
		case 13:
			e.Kind = oplog.KindUnsubstitute
			if len(starts) > 0 {
				e.Ref = starts[r.Intn(len(starts))]
			}
		case 14:
			e.Kind = oplog.KindSuback
			if len(starts) > 0 {
				e.Refs = []uint64{starts[r.Intn(len(starts))]}
			}
		case 15:
			op := oplog.SessionOpen
			if r.Intn(2) == 0 {
				op = oplog.SessionEnd
			}
			e = oplog.SessionEntryOf("op", oplog.SessionEntry{Op: op, Hash: pick(hashes), User: "u", Created: ts, Expires: ts + r.Int63n(3*day)})
			e.Seq, e.Term, e.TS = uint64(i), 1, ts
		case 16:
			e.Kind, e.Value = oplog.KindNote, json.RawMessage(`{"text":"n"}`)
		case 17:
			e.Kind = "a-newer-builds-kind"
		case 18:
			// A lapse names starts, acks and shelves of an earlier term.
			e.Kind, e.By = oplog.KindLapse, "system"
			for _, pool := range [][]uint64{starts, acks} {
				if len(pool) > 0 && r.Intn(2) == 0 {
					e.Refs = append(e.Refs, pool[r.Intn(len(pool))])
				}
			}
		}
		es = append(es, e)
	}
	return es
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// TestKeepDecisionMatchesTheFullLog: for random logs and random Through,
// the keep decision's check passes (a replay of the kept entries and the
// tail gives every consumer the state the whole log gives, at T), and it
// drops something: churn is not kept.
func TestKeepDecisionMatchesTheFullLog(t *testing.T) {
	k := NewKeeper()
	dropped, total := 0, 0
	for i := 0; i < 300; i++ {
		r := rand.New(rand.NewSource(int64(i)))
		es := randomLog(r, 40+r.Intn(400), time.UnixMilli(1_700_000_000_000))
		T := time.UnixMilli(es[len(es)-1].TS)
		through := uint64(1 + r.Intn(len(es)))
		keep, err := k.Decide(context.Background(), inputOf(es, T, through))
		if err != nil {
			t.Fatalf("seed %d, through %d of %d: %v", i, through, len(es), err)
		}
		if !slices.IsSorted(keep) || len(slices.Compact(slices.Clone(keep))) != len(keep) {
			t.Fatalf("seed %d: keep list not sorted and distinct: %v", i, keep)
		}
		kept := map[uint64]bool{}
		for _, s := range keep {
			if s > through {
				t.Fatalf("seed %d: keeps %d past %d", i, s, through)
			}
			kept[s] = true
		}
		for _, e := range es {
			if e.Seq > through {
				break
			}
			switch {
			case e.Kind == oplog.KindNote && !kept[e.Seq]:
				t.Fatalf("seed %d: note %d dropped", i, e.Seq)
			case e.Kind == "a-newer-builds-kind" && !kept[e.Seq]:
				t.Fatalf("seed %d: entry %d of a kind no model claims dropped", i, e.Seq)
			case e.Kind == oplog.KindLapse && !kept[e.Seq]:
				for _, r := range e.Refs {
					if kept[r] {
						t.Fatalf("seed %d: lapse %d dropped, but %d it names kept (the log refuses the split)", i, e.Seq, r)
					}
				}
			}
		}
		dropped += int(through) - len(keep)
		total += int(through)
	}
	if dropped < total/2 {
		t.Fatalf("dropped %d of %d entries up to Through: the churn is kept", dropped, total)
	}
	t.Logf("dropped %d of %d entries up to Through", dropped, total)
}

// TestKeepDecisionRefusesAWrongModel: a model that forgets what an entry
// read keeps too little, and the check before proposing refuses, naming
// the consumer (the mutation keep_reads_ignored, without the build tag).
func TestKeepDecisionRefusesAWrongModel(t *testing.T) {
	k := NewKeeper()
	k.models = func(t time.Time) []keepModel { return []keepModel{forgetful{}} }
	r := rand.New(rand.NewSource(1))
	es := randomLog(r, 400, time.UnixMilli(1_700_000_000_000))
	_, err := k.Decide(context.Background(), inputOf(es, time.UnixMilli(es[len(es)-1].TS), uint64(len(es))))
	if !errors.Is(err, oplog.ErrCompactRefused) {
		t.Fatalf("a model that keeps nothing: %v, want ErrCompactRefused", err)
	}
	t.Log(err)
}

// forgetful keeps nothing.
type forgetful struct{}

func (forgetful) step(oplog.Entry) ([]string, []string) { return nil, nil }
func (forgetful) live(time.Time) []string               { return nil }

// TestRepeatedCompactionsMatchTheUncompactedLog: compactions one after
// another, each decided over what the last one left (its kept entries,
// Lapsed recomputed over them, as a compacted copy replays them, and the
// base's Through told to the scratch consumers), end where a replay of
// the whole uncompacted log does, at the last T.
func TestRepeatedCompactionsMatchTheUncompactedLog(t *testing.T) {
	k := NewKeeper()
	for seed := int64(0); seed < 60; seed++ {
		r := rand.New(rand.NewSource(seed))
		full := randomLog(r, 600, time.UnixMilli(1_700_000_000_000))
		cur := slices.Clone(full) // what a compacted copy holds
		var base uint64
		var T time.Time
		for step := 0; step < 4; step++ {
			hi := full[(step+1)*len(full)/5-1].Seq
			T = time.UnixMilli(full[(step+1)*len(full)/5].TS)
			through := max(base+1, hi-uint64(r.Intn(40)))
			in := inputOf(cur, T, through)
			in.Base = base
			keep, err := k.Decide(context.Background(), in)
			if err != nil {
				t.Fatalf("seed %d step %d (base %d, through %d): %v", seed, step, base, through, err)
			}
			kept := map[uint64]bool{}
			for _, s := range keep {
				kept[s] = true
			}
			var next []oplog.Entry
			for _, e := range cur {
				if e.Seq > through || kept[e.Seq] {
					next = append(next, e)
				}
			}
			cur, base = next, through
		}
		T = time.UnixMilli(full[len(full)-1].TS)
		whole, part := defaultCheckers(T), defaultCheckers(T)
		inputOf(full, T, 0).Entries(func(e oplog.Entry) bool {
			for _, c := range whole {
				c.apply(e)
			}
			return true
		})
		for _, c := range part {
			c.base(base)
		}
		inputOf(cur, T, 0).Entries(func(e oplog.Entry) bool {
			for _, c := range part {
				c.apply(e)
			}
			return true
		})
		for i, c := range whole {
			a, at := c.digest(T, base, nil)
			b, bt := part[i].digest(T, base, nil)
			if a != b {
				t.Fatalf("seed %d: %s after 4 compactions (%d of %d entries left): %s", seed, c.name(), len(cur), len(full), firstDiff(at, bt))
			}
		}
	}
}

// TestAnUnsyncedTakeoverSeesTheShelfTheLogUndid: an unsynced takeover lays
// the view's shelf, or its absence, over its own retain store's
// (AlarmLog.Held(false)). A shelve with 30 days to run, undone at once, is
// still undone after a compaction: the unshelve stays while the shelve it
// undid could be in some copy's retain store.
func TestAnUnsyncedTakeoverSeesTheShelfTheLogUndid(t *testing.T) {
	t0 := int64(1_700_000_000_000)
	es := []oplog.Entry{
		{Seq: 1, Term: 1, TS: t0, Kind: oplog.KindShelve, ID: "A1", Until: t0 + (30 * 24 * time.Hour).Milliseconds(), By: "op"},
		{Seq: 2, Term: 1, TS: t0 + 1000, Kind: oplog.KindUnshelve, ID: "A1", By: "op"},
	}
	for i := 3; i <= 40; i++ {
		es = append(es, oplog.Entry{Seq: uint64(i), Term: 1, TS: t0 + int64(i)*time.Hour.Milliseconds(), Kind: oplog.KindNote, Value: json.RawMessage(`{}`)})
	}
	retained := map[string]retain.AlarmRetain{"A1": {ShelfUntilMs: es[0].Until, ShelfBy: "op"}}
	held := func(es []oplog.Entry) retain.AlarmRetain {
		c := newAlarmCheck()
		inputOf(es, time.Time{}, 0).Entries(func(e oplog.Entry) bool { c.apply(e); return true })
		return c.l.Held(false, retained)["A1"]
	}
	whole := held(es)
	if whole.ShelfUntilMs != 0 {
		t.Fatalf("the whole log restores the undone shelf: %+v", whole)
	}
	// Two compactions: the second must still find the shelve it keeps the
	// unshelve for (Fable, 2b-1).
	cur, base := es, uint64(0)
	for _, step := range []struct {
		day     int64
		through uint64
	}{{10, 30}, {12, 38}} {
		in := inputOf(cur, time.UnixMilli(t0+step.day*(24*time.Hour).Milliseconds()), step.through)
		in.Base = base
		keep, err := NewKeeper().Decide(context.Background(), in)
		if err != nil {
			t.Fatal(err)
		}
		var next []oplog.Entry
		for _, e := range cur {
			if e.Seq > step.through || slices.Contains(keep, e.Seq) {
				next = append(next, e)
			}
		}
		cur, base = next, step.through
		if part := held(cur); part != whole {
			t.Fatalf("after compacting through %d at day %d (kept %v): the compacted log restores %+v, the whole one %+v: an undone shelf came back", step.through, step.day, keep, part, whole)
		}
	}
}

// TestAClockSteppedForwardDropsNothingInForce: entries a leader stamped
// 30 days ahead (its clock stepped, then stepped back) are judged at T,
// the copies' common time: a shelf, an out-of-service window and a session
// still in force at T stay, though their own TS is far past it.
func TestAClockSteppedForwardDropsNothingInForce(t *testing.T) {
	t0 := int64(1_700_000_000_000)
	ahead := t0 + (30 * 24 * time.Hour).Milliseconds()
	es := []oplog.Entry{
		{Seq: 1, Term: 1, TS: ahead, Kind: oplog.KindShelve, ID: "A1", Until: ahead + 3600_000, By: "op"},
		{Seq: 2, Term: 1, TS: ahead, Kind: oplog.KindOutOfService, ID: "Pump_1", Until: ahead + 3600_000, By: "op"},
	}
	s := oplog.SessionEntryOf("op", oplog.SessionEntry{Op: oplog.SessionOpen, Hash: "h", User: "u", Created: ahead, Expires: ahead + 3600_000})
	s.Seq, s.Term, s.TS = 3, 1, ahead
	es = append(es, s)
	for i := 4; i <= 20; i++ {
		es = append(es, oplog.Entry{Seq: uint64(i), Term: 1, TS: t0 + int64(i)*1000, Kind: oplog.KindAck, IDs: []string{"A2"}, By: "op"})
	}
	es = append(es, oplog.Entry{Seq: 21, Term: 1, TS: t0 + 30_000, Kind: oplog.KindAckClear, IDs: []string{"A2"}})
	keep, err := NewKeeper().Decide(context.Background(), inputOf(es, time.UnixMilli(t0+60_000), 21))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []uint64{1, 2, 3} {
		if !slices.Contains(keep, want) {
			t.Fatalf("entry %d, in force at T, dropped: kept %v", want, keep)
		}
	}
	if len(keep) != 3 {
		t.Fatalf("kept %v, want only the three in force (the acks are cleared)", keep)
	}
}

// TestTheKeepDecisionIsPaced: the keep decision's own loops (the closure
// over what is kept, and the check's digests, outside
// CompactInput.Entries) call CompactInput.Pace between steps, so the
// decision keeps to its share of a core beside the scan (oplog's
// compact_pace.go).
func TestTheKeepDecisionIsPaced(t *testing.T) {
	r := rand.New(rand.NewSource(1455))
	es := randomLog(r, 400, time.UnixMilli(1_700_000_000_000))
	in := inputOf(es, time.UnixMilli(es[len(es)-1].TS), uint64(len(es)))
	by := map[string]int{}
	in.Pace = func() {
		pcs := make([]uintptr, 32)
		frames := goruntime.CallersFrames(pcs[:goruntime.Callers(2, pcs)])
		for {
			f, more := frames.Next()
			switch {
			case strings.Contains(f.Function, "digest"):
				by["digest"]++
				return
			case strings.Contains(f.Function, "decideKeep"):
				by["closure"]++
				return
			}
			if !more {
				by["?"]++
				return
			}
		}
	}
	keep, err := NewKeeper().Decide(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if by["closure"] < len(keep) || by["digest"] == 0 {
		t.Fatalf("Pace called %v for a decision keeping %d entries", by, len(keep))
	}
}
