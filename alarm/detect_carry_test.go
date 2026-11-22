package alarm

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/retain"
)

// Carrying detection's episodes across a restart (#1249 slice 1b): the
// controller that continues one ends it, under its start and id, and says
// it was carried.

// restarted is a fresh engine on defs whose clock reads a's plus d: the
// same controller after a restart, with a journal of its own.
func restarted(t *testing.T, a *rig, d time.Duration, defs ...Def) *rig {
	t.Helper()
	b := detectRig(t, nil, defs...)
	b.clk.t = a.clk.now().Add(d)
	return b
}

// A chattering episode carried over a restart continues on the new
// engine: no second start row, its activations there continue it, and
// the new engine writes the end row under the episode's start.
func TestChatterCarriedOverARestart(t *testing.T) {
	a := detectRig(t, nil, def("A", "a"))
	a.tags["a"] = false
	a.Evaluate()
	t0 := a.clk.now()
	a.bounce("a", 0)
	a.bounce("a", 10*time.Second)
	a.bounce("a", 10*time.Second)
	saved := a.RetainedAlarms()
	if r := saved["A"]; r.ChatterSinceMs != t0.UnixMilli() || r.ChatterLastMs != t0.Add(20*time.Second).UnixMilli() || r.ChatterActs != 3 || r.ChatterEpisodes != 1 {
		t.Fatalf("retained %+v", r)
	}

	b := restarted(t, a, 5*time.Second, def("A", "a"))
	restoreAt := b.clk.now()
	b.tags["a"] = false
	b.RestoreAlarms(saved)
	b.Evaluate()
	c := b.chatter(t, "A")
	if c == nil || c.SinceMs != t0.UnixMilli() || c.LastMs != t0.Add(20*time.Second).UnixMilli() || c.Activations != 3 || c.Episodes != 1 || c.CarriedMs != restoreAt.UnixMilli() {
		t.Fatalf("carried chatter = %+v", c)
	}
	if s := b.Summary(); s.Chattering != 1 {
		t.Fatalf("summary chattering = %d", s.Chattering)
	}
	// Three more within a window: the same episode goes on.
	b.bounce("a", 10*time.Second)
	b.bounce("a", 10*time.Second)
	b.bounce("a", 10*time.Second)
	last := b.clk.now()
	if rows := b.rows(t, KindChatter); len(rows) != 0 {
		t.Fatalf("the carried episode started again: %+v", rows)
	}
	if c := b.chatter(t, "A"); c == nil || c.LastMs != last.UnixMilli() || c.Activations != 6 || c.SinceMs != t0.UnixMilli() {
		t.Fatalf("continued chatter = %+v", c)
	}
	b.clk.t = last.Add(time.Minute + time.Millisecond)
	b.Evaluate()
	rows := b.rows(t, KindChatterEnd)
	if len(rows) != 1 || rows[0].TS != last.Add(time.Minute).UnixMilli() || rows[0].SinceMs != t0.UnixMilli() || rows[0].Count != 6 ||
		!strings.Contains(rows[0].Reason, "carried over a restart or takeover") {
		t.Fatalf("end rows = %+v", rows)
	}
	if s := b.Summary(); s.Chattering != 0 || b.chatter(t, "A") != nil {
		t.Fatalf("still chattering after the end: %d", s.Chattering)
	}
}

// One whose window passed while the controller was stopped ends on the
// first scan, journalled when it ended, and says so.
func TestChatterCarriedPastItsWindowEndsAtOnce(t *testing.T) {
	a := detectRig(t, nil, def("A", "a"))
	a.tags["a"] = false
	a.Evaluate()
	t0 := a.clk.now()
	for range 3 {
		a.bounce("a", 10*time.Second)
	}
	b := restarted(t, a, 3*time.Hour, def("A", "a"))
	b.tags["a"] = false
	b.RestoreAlarms(a.RetainedAlarms())
	b.Evaluate()
	if c := b.chatter(t, "A"); c != nil || b.Summary().Chattering != 0 {
		t.Fatalf("a stale episode goes on: %+v", c)
	}
	rows := b.rows(t, KindChatterEnd)
	if len(rows) != 1 || rows[0].TS != t0.Add(30*time.Second+time.Minute).UnixMilli() || rows[0].SinceMs != t0.Add(10*time.Second).UnixMilli() ||
		!strings.Contains(rows[0].Reason, "ended while this controller was stopped") {
		t.Fatalf("end rows = %+v", rows)
	}
}

// floodOn starts a flood on r (floodDefs(12)): eleven activations a
// second apart.
func floodOn(t *testing.T, r *rig) {
	t.Helper()
	setAll(r, 12, false)
	r.Evaluate()
	for i := range 11 {
		r.clk.advance(time.Second)
		r.tags[fmt.Sprintf("t%d", i)] = true
		r.Evaluate()
	}
	if r.Flood() == nil {
		t.Fatal("no flood")
	}
}

// A flood carried over a restart continues on the new engine under its
// id, start and first out; activations there go on counting in it (no
// second start row), and the new engine ends it one window after the
// restore (its rings start there), journalling the end under the id.
func TestFloodCarriedOverARestart(t *testing.T) {
	a := detectRig(t, nil, floodDefs(12)...)
	floodOn(t, a)
	saved := a.RetainedFlood()
	was := a.Flood()
	if saved == nil || saved.ID != was.ID || saved.SinceMs != was.SinceMs || saved.Total != 11 || saved.Peak != 11 ||
		len(saved.FirstOut) != 3 || saved.FirstOut[0] != (retain.AlarmFloodAct{ID: "a0", Ms: was.FirstOut[0].Ms}) {
		t.Fatalf("retained %+v", saved)
	}

	b := restarted(t, a, 5*time.Second, floodDefs(12)...)
	restoreAt := b.clk.now()
	var evs []FloodEvent
	b.OnFlood(func(e FloodEvent) { evs = append(evs, e) })
	setAll(b, 12, true) // standing: none of these counts
	b.RestoreAlarms(a.RetainedAlarms())
	b.RestoreFlood(saved)
	b.Evaluate()
	f := b.Flood()
	if f == nil || f.ID != was.ID || f.SinceMs != was.SinceMs || f.Count != 0 || f.Total != 11 || f.Peak != 11 || f.CarriedMs != restoreAt.UnixMilli() {
		t.Fatalf("carried flood = %+v", f)
	}
	if len(f.FirstOut) != 3 || f.FirstOut[0].ID != "a0" || f.FirstOut[2].ID != "a2" {
		t.Fatalf("carried first out = %+v", f.FirstOut)
	}
	// New activations count in the same flood.
	setAll(b, 12, false)
	b.Evaluate()
	for i := range 3 {
		b.clk.advance(time.Second)
		b.tags[fmt.Sprintf("t%d", i)] = true
		b.Evaluate()
	}
	if f := b.Flood(); f == nil || f.ID != was.ID || f.Count != 3 || f.Total != 14 {
		t.Fatalf("flood after new activations = %+v", f)
	}
	if rows := b.rows(t, KindFlood); len(rows) != 0 || len(evs) != 0 {
		t.Fatalf("the carried flood started again: %+v %+v", rows, evs)
	}
	// Never over the threshold here: it ends one window after the restore.
	end := restoreAt.Add(10 * time.Minute)
	b.clk.t = end
	b.Evaluate()
	if b.Flood() == nil {
		t.Fatal("ended before a full window after the restore")
	}
	b.clk.advance(time.Millisecond)
	b.Evaluate()
	if f := b.Flood(); f != nil {
		t.Fatalf("still on: %+v", f)
	}
	rows := b.rows(t, KindFloodEnd)
	if len(rows) != 1 || rows[0].TS != end.UnixMilli() || rows[0].Episode != was.ID || rows[0].SinceMs != was.SinceMs || rows[0].Count != 14 ||
		!strings.Contains(rows[0].Reason, "carried over a restart or takeover") {
		t.Fatalf("end rows = %+v", rows)
	}
	if len(evs) != 1 || evs[0].Kind != "end" || evs[0].ID != was.ID || evs[0].StartMs != was.SinceMs {
		t.Fatalf("flood events = %+v", evs)
	}
	// The next flood began here: nothing about it was carried.
	floodOn(t, b)
	if f := b.Flood(); f.ID == was.ID || f.CarriedMs != 0 {
		t.Fatalf("a flood begun here reads as carried: %+v", f.FloodBrief)
	}
}

// A flood whose window passed while the controller was stopped ends on
// the first scan, journalled when it ended, and says so.
func TestFloodCarriedPastItsWindowEndsAtOnce(t *testing.T) {
	a := detectRig(t, nil, floodDefs(12)...)
	floodOn(t, a)
	saved := a.RetainedFlood()
	b := restarted(t, a, 3*time.Hour, floodDefs(12)...)
	setAll(b, 12, false)
	b.RestoreFlood(saved)
	b.Evaluate()
	if f := b.Flood(); f != nil {
		t.Fatalf("a stale flood goes on: %+v", f)
	}
	rows := b.rows(t, KindFloodEnd)
	if len(rows) != 1 || rows[0].TS != saved.OverMs+(10*time.Minute).Milliseconds() || rows[0].Episode != saved.ID ||
		!strings.Contains(rows[0].Reason, "ended while this controller was stopped") {
		t.Fatalf("end rows = %+v", rows)
	}
}

// Carried times come from another clock: none is taken as later than the
// restore, so a carried episode ends no later than one window after it.
func TestCarriedEpisodesEndWithinAWindowOfTheRestore(t *testing.T) {
	r := detectRig(t, nil, floodDefs(12)...)
	setAll(r, 12, false)
	now := r.clk.now()
	ahead := now.Add(time.Hour).UnixMilli()
	r.RestoreAlarms(map[string]retain.AlarmRetain{"a0": {ChatterSinceMs: ahead, ChatterLastMs: ahead, ChatterActs: 3, ChatterEpisodes: 1}})
	r.RestoreFlood(&retain.AlarmFlood{ID: "flood-x", SinceMs: ahead, OverMs: ahead, Total: 11, Peak: 11,
		FirstOut: []retain.AlarmFloodAct{{ID: "gone", Ms: ahead}, {ID: "a3", Ms: ahead}}})
	r.Evaluate()
	f := r.Flood()
	if f == nil || f.SinceMs > now.UnixMilli() || len(f.FirstOut) != 1 || f.FirstOut[0].ID != "a3" || f.FirstOut[0].Ms > now.UnixMilli() {
		t.Fatalf("carried flood = %+v", f)
	}
	if c := r.chatter(t, "a0"); c == nil || c.SinceMs > now.UnixMilli() || c.LastMs > now.UnixMilli() {
		t.Fatalf("carried chatter = %+v", c)
	}
	r.clk.t = now.Add(time.Minute + time.Millisecond)
	r.Evaluate()
	if c := r.chatter(t, "a0"); c != nil {
		t.Fatalf("chatter carried from a clock ahead outlived a window: %+v", c)
	}
	r.clk.t = now.Add(10*time.Minute + time.Millisecond)
	r.Evaluate()
	if f := r.Flood(); f != nil {
		t.Fatalf("a flood carried from a clock ahead outlived a window: %+v", f)
	}
}

// A takeover's reset forgets a carried episode as any other, and a
// second restore replaces the first without counting it twice.
func TestCarriedEpisodesResetAndReplace(t *testing.T) {
	r := detectRig(t, nil, def("A", "a"))
	r.tags["a"] = false
	ms := r.clk.now().UnixMilli()
	m := map[string]retain.AlarmRetain{"A": {ChatterSinceMs: ms, ChatterLastMs: ms, ChatterActs: 3, ChatterEpisodes: 1}}
	r.RestoreAlarms(m)
	r.RestoreAlarms(m)
	r.Evaluate()
	if s := r.Summary(); s.Chattering != 1 {
		t.Fatalf("two restores: chattering = %d", s.Chattering)
	}
	r.ResetOperatorState()
	if s := r.Summary(); s.Chattering != 0 || r.chatter(t, "A") != nil {
		t.Fatalf("reset kept a carried episode: %d", s.Chattering)
	}
}
