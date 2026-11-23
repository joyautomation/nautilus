package server

import (
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/alarm"
	"github.com/joyautomation/nautilus/oplog"
	"github.com/joyautomation/nautilus/retain"
)

// Alarm floods and chattering across a pair's takeover (#1249 slice 1b):
// the leader keeps the view's episodes through the log, on beginnings and
// ends only; a synced takeover continues them, an unsynced one none.

// detectClock is a virtual clock for an engine, starting at the wall's
// now (the view stamps a takeover with the wall clock).
type detectClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *detectClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *detectClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// detectAlarms is newTestAlarms on a virtual clock, chattering at two
// activations a minute and flooding at more than one in ten minutes.
func detectAlarms(t *testing.T, cond map[string]any) (*alarm.Engine, *detectClock) {
	t.Helper()
	clk := &detectClock{t: time.Now()}
	var mu sync.Mutex
	eng, err := alarm.New(alarm.Options{
		Defs: []alarm.Def{
			{ID: "HiTemp", Tag: "HiTempAlm", Name: "High temperature", Priority: alarm.High, Site: "Plant", AckRequired: true, AutoClear: true},
			{ID: "LoFlow", Tag: "LoFlowAlm", Name: "Low flow", Priority: alarm.Medium, Site: "Plant", AckRequired: true, AutoClear: true},
		},
		Read: func(p string) (any, bool) {
			mu.Lock()
			defer mu.Unlock()
			v, ok := cond[p]
			return v, ok
		},
		Now:     clk.now,
		Chatter: alarm.ChatterConfig{Count: 2},
		Flood:   alarm.FloodConfig{Threshold: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { eng.Close() })
	return eng, clk
}

// detectBody decodes a KindAlarmDetect entry.
func detectBody(t *testing.T, e oplog.Entry) detectEntry {
	t.Helper()
	var b detectEntry
	if err := json.Unmarshal(e.Value, &b); err != nil {
		t.Fatal(err)
	}
	return b
}

// detectApply applies body b as an entry the leader stamped ts (epoch
// ms; omitted: 0).
func detectApply(t *testing.T, al *AlarmLog, b detectEntry, ts ...int64) {
	t.Helper()
	v, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	e := oplog.Entry{Kind: oplog.KindAlarmDetect, Value: v, By: "system"}
	if len(ts) > 0 {
		e.TS = ts[0]
	}
	if done, err := al.Apply(e); !done || err != nil {
		t.Fatalf("apply: %v %v", done, err)
	}
}

// The leader proposes an episode's beginning and its end, one entry per
// tick, never its numbers moving.
func TestLeaderSyncsEpisodes(t *testing.T) {
	var lead atomic.Bool
	lead.Store(true)
	cond := map[string]any{"HiTempAlm": false, "LoFlowAlm": false}
	eng, clk := detectAlarms(t, cond)
	al := NewAlarmLog(eng, lead.Load, noTerm)
	var mu sync.Mutex
	var got []oplog.Entry
	n, err := oplog.New(oplog.Options{Self: "a", Apply: func(e oplog.Entry) error {
		if e.Kind == oplog.KindAlarmDetect {
			mu.Lock()
			got = append(got, e)
			mu.Unlock()
		}
		_, err := al.Apply(e)
		return err
	}})
	if err != nil {
		t.Fatal(err)
	}
	al.Attach(n)
	PairAlarms{Engine: eng, Log: al}.RestoreAlarmsSynced(nil, true) // live
	entries := func() []oplog.Entry {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(got)
	}
	waitFor := func(k int) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for len(entries()) < k {
			if time.Now().After(deadline) {
				t.Fatalf("proposed %d alarm-detect entries; want %d", len(entries()), k)
			}
			al.Reconcile()
			time.Sleep(10 * time.Millisecond)
		}
	}
	bounce := func() {
		cond["HiTempAlm"] = true
		eng.Evaluate()
		clk.advance(time.Second)
		cond["HiTempAlm"] = false
		eng.Evaluate()
		clk.advance(time.Second)
	}
	eng.Evaluate()
	bounce()
	bounce() // two in a minute: chattering, and over the flood threshold
	f := eng.Flood()
	hi, _ := eng.Lookup("HiTemp")
	if f == nil || hi.Chatter == nil {
		t.Fatalf("no episodes: flood %+v chatter %+v", f, hi.Chatter)
	}
	waitFor(1)
	b := detectBody(t, entries()[0])
	if b.V != detectEntryVersion || b.Flood == nil || b.Flood.ID != f.ID || b.FloodEnd != "" ||
		len(b.Chatter) != 1 || b.Chatter[0] != (chatterEpisode{ID: "HiTemp", SinceMs: hi.Chatter.SinceMs, Acts: 2, Episodes: 1}) || len(b.ChatterEnd) != 0 {
		t.Fatalf("first entry %+v", b)
	}
	if hf := al.HeldFlood(true); hf == nil || hf.ID != f.ID || al.Held(true, nil)["HiTemp"].ChatterSinceMs != hi.Chatter.SinceMs {
		t.Fatalf("view after the start: flood %+v, alarms %+v", hf, al.Held(true, nil))
	}
	// The numbers move; nothing is proposed.
	bounce()
	for range 5 {
		al.Reconcile()
		time.Sleep(10 * time.Millisecond)
	}
	if k := len(entries()); k != 1 {
		t.Fatalf("moving numbers proposed %d entries", k)
	}
	// Both end by time (the flood a window after its count fell to the
	// threshold): one entry names what ended.
	clk.advance(21 * time.Minute)
	eng.Evaluate()
	waitFor(2)
	b = detectBody(t, entries()[1])
	if b.FloodEnd != f.ID || b.Flood != nil || len(b.ChatterEnd) != 1 || b.ChatterEnd[0].ID != "HiTemp" || b.ChatterEnd[0].SinceMs != hi.Chatter.SinceMs || len(b.Chatter) != 0 {
		t.Fatalf("second entry %+v", b)
	}
	if al.HeldFlood(true) != nil || al.Held(true, nil)["HiTemp"].ChatterSinceMs != 0 {
		t.Fatal("the view kept an ended episode")
	}
}

// A synced takeover continues the view's episodes as going at the
// takeover: the new leader ends each one window after it unless its own
// activations keep it going. An unsynced one carries none. Applying an
// entry never touches the engine, even a live leader's.
func TestTakeoverContinuesTheViewsEpisodes(t *testing.T) {
	for _, synced := range []bool{true, false} {
		cond := map[string]any{"HiTempAlm": false, "LoFlowAlm": false}
		eng, clk := detectAlarms(t, cond)
		var lead atomic.Bool
		al := NewAlarmLog(eng, lead.Load, noTerm)
		now := clk.now()
		old := now.Add(-5 * time.Minute).UnixMilli()
		detectApply(t, al, detectEntry{V: detectEntryVersion,
			Flood:   &retain.AlarmFlood{ID: "flood-1", SinceMs: old, OverMs: old, Total: 20, Peak: 15, FirstOut: []retain.AlarmFloodAct{{ID: "LoFlow", Ms: old}}},
			Chatter: []chatterEpisode{{ID: "HiTemp", SinceMs: old, Acts: 5, Episodes: 2}}}, now.UnixMilli()) // a live leader's entry, just now
		if eng.Flood() != nil {
			t.Fatal("applying an entry touched the engine")
		}
		lead.Store(true)
		PairAlarms{Engine: eng, Log: al}.RestoreAlarmsSynced(nil, synced)
		eng.Evaluate()
		f := eng.Flood()
		hi, _ := eng.Lookup("HiTemp")
		if !synced {
			if f != nil || hi.Chatter != nil {
				t.Errorf("unsynced takeover carried flood %+v chatter %+v", f, hi.Chatter)
			}
			continue
		}
		if f == nil || f.ID != "flood-1" || f.SinceMs != old || f.Total != 20 || f.Peak != 15 || f.CarriedMs == 0 || len(f.FirstOut) != 1 || f.FirstOut[0].ID != "LoFlow" {
			t.Fatalf("synced: flood %+v", f)
		}
		if hi.Chatter == nil || hi.Chatter.SinceMs != old || hi.Chatter.Activations != 5 || hi.Chatter.Episodes != 2 {
			t.Fatalf("synced: chatter %+v", hi.Chatter)
		}
		// Going at the takeover: on for a window after it, not after the
		// carried instants.
		clk.advance(59 * time.Second)
		eng.Evaluate()
		if hi, _ := eng.Lookup("HiTemp"); hi.Chatter == nil {
			t.Fatal("synced: chatter ended within a window of the takeover")
		}
		clk.advance(9 * time.Minute)
		eng.Evaluate()
		if eng.Flood() == nil {
			t.Fatal("synced: flood ended within a window of the takeover")
		}
		clk.advance(2 * time.Minute)
		eng.Evaluate()
		if eng.Flood() != nil {
			t.Fatal("synced: flood outlived a window after the takeover")
		}
		rows, err := eng.Journal(time.Time{}, time.Time{}, alarm.Filter{Kinds: []string{alarm.KindFloodEnd, alarm.KindChatterEnd}})
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 2 {
			t.Fatalf("synced: end rows %+v", rows)
		}
		for _, r := range rows {
			if r.SinceMs != old || (r.Kind == alarm.KindFloodEnd && r.Episode != "flood-1") {
				t.Errorf("synced: end row %+v; want it under the carried start and id", r)
			}
		}
	}
}

// An end names what it ends: one naming another flood or another episode
// leaves the view's. A body of a later version is not read.
func TestDetectEntriesNameWhatTheyEnd(t *testing.T) {
	eng, _ := detectAlarms(t, map[string]any{"HiTempAlm": false, "LoFlowAlm": false})
	var lead atomic.Bool
	al := NewAlarmLog(eng, lead.Load, noTerm)
	detectApply(t, al, detectEntry{V: detectEntryVersion, Flood: &retain.AlarmFlood{ID: "flood-2", SinceMs: 2, OverMs: 2},
		Chatter: []chatterEpisode{{ID: "HiTemp", SinceMs: 20}}})
	detectApply(t, al, detectEntry{V: detectEntryVersion, FloodEnd: "flood-1", ChatterEnd: []chatterEpisode{{ID: "HiTemp", SinceMs: 10}}})
	if f := al.HeldFlood(true); f == nil || f.ID != "flood-2" {
		t.Fatalf("an end of another flood ended the view's: %+v", f)
	}
	if al.Held(true, nil)["HiTemp"].ChatterSinceMs != 20 {
		t.Fatal("an end of another episode ended the view's")
	}
	detectApply(t, al, detectEntry{V: detectEntryVersion + 1, FloodEnd: "flood-2", ChatterEnd: []chatterEpisode{{ID: "HiTemp", SinceMs: 20}}})
	if al.HeldFlood(true) == nil {
		t.Fatal("a later version's body was read")
	}
	detectApply(t, al, detectEntry{V: detectEntryVersion, FloodEnd: "flood-2", ChatterEnd: []chatterEpisode{{ID: "HiTemp", SinceMs: 20}}})
	if al.HeldFlood(true) != nil || al.Held(true, nil)["HiTemp"].ChatterSinceMs != 0 {
		t.Fatal("the ends did not end the view's episodes")
	}
}

// A superseded history's episodes go with its view.
func TestDroppedViewCarriesNoEpisode(t *testing.T) {
	eng, _ := detectAlarms(t, map[string]any{"HiTempAlm": false, "LoFlowAlm": false})
	var lead atomic.Bool
	al := NewAlarmLog(eng, lead.Load, noTerm)
	detectApply(t, al, detectEntry{V: detectEntryVersion, Flood: &retain.AlarmFlood{ID: "flood-1", SinceMs: 1, OverMs: 1},
		Chatter: []chatterEpisode{{ID: "HiTemp", SinceMs: 1}}})
	al.dropView()
	if al.HeldFlood(true) != nil || al.Held(true, nil)["HiTemp"].ChatterSinceMs != 0 {
		t.Fatal("a dropped view kept an episode")
	}
}

// A pair's retainer carries no flood through the retain store: the
// runtime hands a flood slot only to an engine that is its own retainer.
func TestPairAlarmsCarryNoFloodThroughTheStore(t *testing.T) {
	if _, ok := any(PairAlarms{}).(retain.AlarmFloodRetainer); ok {
		t.Fatal("PairAlarms is a retain.AlarmFloodRetainer: the store would be a second source for a pair's flood")
	}
	if _, ok := any(&alarm.Engine{}).(retain.AlarmFloodRetainer); !ok {
		t.Fatal("the engine is no retain.AlarmFloodRetainer: a standalone restart would carry no flood")
	}
}

// Both controllers come back hours after a flood (a cold start): the view
// replayed from the log still holds the episodes, but nobody vouched for
// them since the last entry. The takeover dates them by that entry, so
// they end on the new leader's first scan, journalled when they ended,
// instead of reading as live for a window.
func TestColdStartTakeoverEndsStaleEpisodes(t *testing.T) {
	cond := map[string]any{"HiTempAlm": false, "LoFlowAlm": false}
	eng, clk := detectAlarms(t, cond)
	var lead atomic.Bool
	al := NewAlarmLog(eng, lead.Load, noTerm)
	then := clk.now().Add(-8 * time.Hour).UnixMilli()
	detectApply(t, al, detectEntry{V: detectEntryVersion,
		Flood:   &retain.AlarmFlood{ID: "flood-1", SinceMs: then, OverMs: then, Total: 11, Peak: 11},
		Chatter: []chatterEpisode{{ID: "HiTemp", SinceMs: then, Acts: 3, Episodes: 1}}}, then)
	lead.Store(true)
	PairAlarms{Engine: eng, Log: al}.RestoreAlarmsSynced(nil, true)
	eng.Evaluate()
	hi, _ := eng.Lookup("HiTemp")
	if f := eng.Flood(); f != nil || hi.Chatter != nil {
		t.Fatalf("stale episodes read as live: flood %+v chatter %+v", f, hi.Chatter)
	}
	rows, err := eng.Journal(time.Time{}, time.Time{}, alarm.Filter{Kinds: []string{alarm.KindFloodEnd, alarm.KindChatterEnd}})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("end rows %+v", rows)
	}
	for _, r := range rows {
		w := int64(60000)
		if r.Kind == alarm.KindFloodEnd {
			w = 600000
		}
		if r.TS != then+w || !strings.Contains(r.Reason, "ended while this controller was stopped") {
			t.Errorf("end row %+v; want it at the episode's end (%d), saying it ended while stopped", r, then+w)
		}
	}
}
