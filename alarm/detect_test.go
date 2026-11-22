package alarm

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/retain"
)

// detectRig is a rig whose Options a test can shape.
func detectRig(t *testing.T, shape func(*Options), defs ...Def) *rig {
	t.Helper()
	clk, tg, ring := newClock(), tags{}, NewRing(5000)
	o := Options{Defs: defs, Read: tg.read, Now: clk.now, Journal: ring}
	if shape != nil {
		shape(&o)
	}
	e, err := New(o)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { e.Close() })
	return &rig{Engine: e, clk: clk, tags: tg, ring: ring}
}

// rows is the journal's rows of the given kinds, oldest first.
func (r *rig) rows(t *testing.T, kinds ...string) []Event {
	t.Helper()
	evs, err := r.Journal(time.Time{}, time.Time{}, Filter{Kinds: kinds, Limit: 5000})
	if err != nil {
		t.Fatal(err)
	}
	out := make([]Event, len(evs))
	for i, e := range evs {
		out[len(evs)-1-i] = e
	}
	return out
}

// bounce activates the alarm on tag at the clock's time after advancing
// by d, and returns it to normal (unack-rtn) one scan later, a second on.
func (r *rig) bounce(tag string, d time.Duration) {
	r.clk.advance(d)
	r.tags[tag] = true
	r.Evaluate()
	r.clk.advance(time.Second)
	r.tags[tag] = false
	r.Evaluate()
	r.clk.advance(-time.Second) // offsets are from activation to activation
}

func (r *rig) chatter(t *testing.T, id string) *Chatter {
	t.Helper()
	rec, ok := r.Lookup(id)
	if !ok {
		t.Fatalf("no alarm %q", id)
	}
	return rec.Chatter
}

// Three activations within sixty seconds chatter (inclusive, as the
// report counts them); the episode ends sixty seconds after its last
// qualifying activation, on the scan, journalled at that instant.
func TestChatterStartsAndEndsByTime(t *testing.T) {
	r := detectRig(t, nil, def("A", "a"))
	r.tags["a"] = false
	r.Evaluate()
	t0 := r.clk.now()
	r.bounce("a", 0)
	r.bounce("a", 30*time.Second)
	if c := r.chatter(t, "A"); c != nil {
		t.Fatalf("two activations: chattering %+v", c)
	}
	r.bounce("a", 30*time.Second) // exactly 60 s after the first: inclusive
	c := r.chatter(t, "A")
	if c == nil {
		t.Fatal("three activations in 60 s (inclusive): not chattering")
	}
	if c.SinceMs != t0.UnixMilli() || c.LastMs != t0.Add(time.Minute).UnixMilli() || c.Activations != 3 || c.Episodes != 1 || c.Count != 3 || c.WindowMs != 60000 {
		t.Errorf("chatter = %+v", c)
	}
	if s := r.Summary(); s.Chattering != 1 {
		t.Errorf("summary chattering = %d, want 1", s.Chattering)
	}
	rows := r.rows(t, KindChatter, KindChatterEnd)
	if len(rows) != 1 || rows[0].Kind != KindChatter || rows[0].ID != "A" || rows[0].Count != 3 || rows[0].SinceMs != t0.UnixMilli() || rows[0].WindowMs != 60000 {
		t.Fatalf("rows = %+v", rows)
	}
	// Sixty seconds after the last: still on (an activation now could
	// continue it); a millisecond past: over, journalled at last+60 s.
	r.clk.t = t0.Add(2 * time.Minute)
	r.Evaluate()
	if r.chatter(t, "A") == nil {
		t.Fatal("ended at exactly the window after the last activation")
	}
	r.clk.advance(time.Millisecond)
	r.Evaluate()
	if c := r.chatter(t, "A"); c != nil {
		t.Fatalf("still chattering %+v", c)
	}
	rows = r.rows(t, KindChatterEnd)
	if len(rows) != 1 || rows[0].TS != t0.Add(2*time.Minute).UnixMilli() || rows[0].Count != 3 || rows[0].SinceMs != t0.UnixMilli() {
		t.Fatalf("end rows = %+v", rows)
	}
	if s := r.Summary(); s.Chattering != 0 {
		t.Errorf("summary chattering = %d after the end", s.Chattering)
	}
}

// Three activations just over sixty seconds apart end to end do not
// chatter.
func TestChatterWindowIsInclusiveOnly(t *testing.T) {
	r := detectRig(t, nil, def("A", "a"))
	r.tags["a"] = false
	r.Evaluate()
	r.bounce("a", 0)
	r.bounce("a", 30*time.Second)
	r.bounce("a", 30*time.Second+time.Millisecond)
	if c := r.chatter(t, "A"); c != nil {
		t.Fatalf("chattering %+v", c)
	}
}

// Overlapping windows are one episode, as rationalize.Chattering counts
// them: activations at 0, 30, 60, 90 s are one burst, not two.
func TestChatterOverlappingWindowsAreOneEpisode(t *testing.T) {
	r := detectRig(t, nil, def("A", "a"))
	r.tags["a"] = false
	r.Evaluate()
	r.bounce("a", 0)
	for range 3 {
		r.bounce("a", 30*time.Second)
	}
	c := r.chatter(t, "A")
	if c == nil || c.Episodes != 1 || c.Activations != 4 {
		t.Fatalf("chatter = %+v, want one episode of 4", c)
	}
	if rows := r.rows(t, KindChatter, KindChatterEnd); len(rows) != 1 {
		t.Fatalf("rows = %+v, want one start", rows)
	}
	// It runs a window past its last qualifying activation (90 s), not
	// past the one that started it (60 s).
	r.clk.advance(50 * time.Second)
	r.Evaluate()
	if c := r.chatter(t, "A"); c == nil || c.LastMs != r.clk.at(-50*time.Second).UnixMilli() {
		t.Fatalf("140 s: chatter = %+v, want it on until 150 s", c)
	}
}

// An episode ends by time even while the alarm is shelved or suppressed:
// neither steps.
func TestChatterEndsWhileShelvedOrSuppressed(t *testing.T) {
	for _, how := range []string{"shelved", "suppressed"} {
		t.Run(how, func(t *testing.T) {
			d := def("A", "a")
			d.Enable = "en"
			r := detectRig(t, nil, d)
			r.tags["a"], r.tags["en"] = false, true
			r.Evaluate()
			for range 3 {
				r.bounce("a", 10*time.Second)
			}
			if r.chatter(t, "A") == nil {
				t.Fatal("not chattering")
			}
			if how == "shelved" {
				if err := r.Shelve("A", r.clk.at(time.Hour), "op"); err != nil {
					t.Fatal(err)
				}
			} else {
				r.tags["en"] = false
			}
			r.clk.advance(2 * time.Minute)
			r.Evaluate()
			if c := r.chatter(t, "A"); c != nil {
				t.Fatalf("still chattering %+v", c)
			}
			if rows := r.rows(t, KindChatterEnd); len(rows) != 1 {
				t.Fatalf("end rows = %+v", rows)
			}
		})
	}
}

// floodDefs is n alarms a0..a(n-1) on tags t0.., spread over three pieces
// of equipment.
func floodDefs(n int) []Def {
	out := make([]Def, n)
	for i := range out {
		out[i] = def(fmt.Sprintf("a%d", i), fmt.Sprintf("t%d", i))
		out[i].Equipment = []string{"P101", "P102", "Skid2"}[i%3]
		out[i].Area = "North"
	}
	return out
}

// More than ten activations in ten minutes is a flood: the summary and
// Flood say so, with the first alarms in time order and the equipment the
// activations came from. Ten is not.
func TestFloodStartsOverTheThreshold(t *testing.T) {
	r := detectRig(t, nil, floodDefs(12)...)
	for i := range 12 {
		r.tags[fmt.Sprintf("t%d", i)] = false
	}
	r.Evaluate()
	t0 := r.clk.now()
	for i := range 10 {
		r.clk.advance(time.Minute - time.Second)
		r.tags[fmt.Sprintf("t%d", i)] = true
		r.Evaluate()
	}
	if f := r.Flood(); f != nil {
		t.Fatalf("ten activations: flood %+v", f)
	}
	r.clk.advance(time.Second) // the 11th, exactly 10 min after the first: inclusive
	r.tags["t10"] = true
	r.Evaluate()
	f := r.Flood()
	if f == nil {
		t.Fatal("eleven activations in 10 min: no flood")
	}
	first := t0.Add(time.Minute - time.Second)
	if f.Count != 11 || f.Total != 11 || f.Peak != 11 || f.Threshold != 10 || f.WindowMs != 600000 || f.SinceMs != r.clk.now().UnixMilli() {
		t.Errorf("flood = %+v", f.FloodBrief)
	}
	if len(f.FirstOut) != 3 || f.FirstOut[0].ID != "a0" || f.FirstOut[1].ID != "a1" || f.FirstOut[2].ID != "a2" || f.FirstOut[0].Ms != first.UnixMilli() {
		t.Errorf("first out = %+v", f.FirstOut)
	}
	if len(f.Groups) != 3 || f.Groups[0] != (FloodGroup{Equipment: "P101", Area: "North", Count: 4}) || f.Groups[1].Equipment != "P102" || f.Groups[1].Count != 4 || f.Groups[2].Count != 3 {
		t.Errorf("groups = %+v", f.Groups)
	}
	s := r.Summary()
	if s.Flood == nil || *s.Flood != f.FloodBrief {
		t.Errorf("summary flood = %+v, want %+v", s.Flood, f.FloodBrief)
	}
	rows := r.rows(t, KindFlood, KindFloodEnd)
	if len(rows) != 1 || rows[0].ID != FloodJournalID || rows[0].Count != 11 || rows[0].Episode != f.ID ||
		rows[0].Reason != "first out: a0, a1, a2" || !strings.Contains(rows[0].Name, "11 in 10m0s (limit 10)") {
		t.Fatalf("rows = %+v", rows)
	}
}

// A flood ends once the count has stayed at or under the threshold for a
// full window: dipping under and coming back within it is the same flood.
func TestFloodHysteresis(t *testing.T) {
	r := detectRig(t, nil, floodDefs(13)...)
	var got []FloodEvent
	r.OnFlood(func(e FloodEvent) { got = append(got, e) })
	for i := range 13 {
		r.tags[fmt.Sprintf("t%d", i)] = false
	}
	r.Evaluate()
	t0 := r.clk.now()
	for i := range 11 {
		r.clk.advance(time.Second)
		r.tags[fmt.Sprintf("t%d", i)] = true
		r.Evaluate()
	}
	if r.Flood() == nil {
		t.Fatal("no flood")
	}
	// The first activation (t0+1s) leaves the window at t0+10m+1s: the
	// count is ten, at the threshold. A new one 5 minutes later is over
	// again: still the same flood.
	r.clk.t = t0.Add(15 * time.Minute)
	r.Evaluate()
	f := r.Flood()
	if f == nil || f.Count != 0 {
		t.Fatalf("under the threshold for less than a window: flood %+v", f)
	}
	id := f.ID
	// Bounce two alarms back in: 12 activations... only 2 in the window.
	r.tags["t11"] = true
	r.Evaluate()
	if f := r.Flood(); f == nil || f.ID != id || f.Total != 12 {
		t.Fatalf("flood = %+v, want the same one with 12", f)
	}
	// The count went to ten when the first activation (t0+1s) left the
	// window, at t0+10m+1s, and stayed under: the flood ends a window
	// later, at t0+20m+1s, the new activation notwithstanding.
	end := t0.Add(20*time.Minute + time.Second)
	r.clk.t = end
	r.Evaluate()
	if r.Flood() == nil {
		t.Fatal("ended at exactly a full window under the threshold")
	}
	r.clk.advance(time.Millisecond)
	r.Evaluate()
	if f := r.Flood(); f != nil {
		t.Fatalf("still flooding %+v", f)
	}
	rows := r.rows(t, KindFloodEnd)
	if len(rows) != 1 || rows[0].TS != end.UnixMilli() || rows[0].Count != 12 || rows[0].Peak != 11 || rows[0].Episode != id || rows[0].SinceMs != t0.Add(11*time.Second).UnixMilli() {
		t.Fatalf("end rows = %+v", rows)
	}
	if len(got) != 2 || got[0].Kind != "start" || got[1].Kind != "end" || got[1].EndMs != end.UnixMilli() || got[0].ID != id ||
		got[0].Version != FloodEventVersion || len(got[0].Activations) != 11 || got[0].Activations[0].ID != "a0" || len(got[0].FirstOut) != 3 {
		t.Fatalf("flood events = %+v", got)
	}
	if s := r.Summary(); s.Flood != nil {
		t.Errorf("summary flood after the end = %+v", s.Flood)
	}
}

// Conditions that stood through a re-baseline do not count when they
// activate: a restart, a takeover's reset, a return from suppression, an
// on-delay running out after any of them, or a standalone start's grace.
// A real activation after that does.
func TestReactivationsAfterARebaselineDoNotCount(t *testing.T) {
	const n = 12
	cases := []struct {
		name  string
		shape func(*Options)
		defs  func([]Def)
		run   func(r *rig)
	}{
		{name: "start", run: func(r *rig) {
			setAll(r, n, true)
			r.Evaluate()
		}},
		{name: "on-delay", defs: func(ds []Def) {
			for i := range ds {
				ds[i].OnDelay = 5 * time.Second
			}
		}, run: func(r *rig) {
			setAll(r, n, true)
			r.Evaluate()
			r.clk.advance(5 * time.Second)
			r.Evaluate()
		}},
		{name: "takeover", run: func(r *rig) {
			setAll(r, n, false)
			r.Evaluate()
			r.ResetOperatorState()
			setAll(r, n, true)
			r.Evaluate()
			r.ResetOperatorState()
			r.RestoreAlarms(map[string]retain.AlarmRetain{})
			r.clk.advance(time.Second)
			r.Evaluate()
		}},
		{name: "unsuppress", defs: func(ds []Def) {
			for i := range ds {
				ds[i].Enable = "en"
			}
		}, run: func(r *rig) {
			setAll(r, n, false)
			r.tags["en"] = true
			r.Evaluate()
			r.tags["en"] = false
			setAll(r, n, true)
			r.Evaluate()
			r.clk.advance(time.Second)
			r.tags["en"] = true
			r.Evaluate()
		}},
		{name: "grace", shape: func(o *Options) { o.CarryGrace = time.Minute }, run: func(r *rig) {
			setAll(r, n, false) // a driver's defaults, before its first read
			r.Evaluate()
			r.clk.advance(2 * time.Second)
			setAll(r, n, true)
			r.Evaluate()
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			defs := floodDefs(n)
			if c.defs != nil {
				c.defs(defs)
			}
			r := detectRig(t, c.shape, defs...)
			c.run(r)
			if got := len(r.rows(t, KindActive)); got < n {
				t.Fatalf("%d active rows, want %d (the case did not activate them)", got, n)
			}
			if f := r.Flood(); f != nil {
				t.Fatalf("a re-baseline read as a flood: %+v", f.FloodBrief)
			}
			// After the grace (and with the conditions gone), real
			// activations count.
			r.clk.advance(2 * time.Minute)
			setAll(r, n, false)
			r.Evaluate()
			r.clk.advance(time.Second)
			setAll(r, n, true)
			r.Evaluate()
			r.clk.advance(5 * time.Second) // the on-delay case's
			r.Evaluate()
			if f := r.Flood(); f == nil || f.Count != n {
				t.Fatalf("real activations after the re-baseline: flood %+v", f)
			}
		})
	}
}

func setAll(r *rig, n int, v bool) {
	for i := range n {
		r.tags[fmt.Sprintf("t%d", i)] = v
	}
}

// A takeover's reset forgets a frozen engine's episodes and rings
// (journalling nothing: they were an earlier leadership's).
func TestResetForgetsEpisodes(t *testing.T) {
	r := detectRig(t, nil, floodDefs(12)...)
	setAll(r, 12, false)
	r.Evaluate()
	for range 3 {
		r.bounce("t0", 5*time.Second)
	}
	r.clk.advance(time.Second)
	setAll(r, 12, true)
	r.Evaluate()
	if r.chatter(t, "a0") == nil || r.Flood() == nil {
		t.Fatal("no episodes to forget")
	}
	r.ResetOperatorState()
	if r.chatter(t, "a0") != nil || r.Flood() != nil || r.Summary().Chattering != 0 {
		t.Fatal("reset kept an episode")
	}
	if rows := r.rows(t, KindChatterEnd, KindFloodEnd); len(rows) != 0 {
		t.Fatalf("reset journalled %+v", rows)
	}
}

// Scans during a chattering episode and a standing flood allocate
// nothing: detection's time checks are free.
func TestDetectionScanDoesNotAllocate(t *testing.T) {
	r := detectRig(t, nil, floodDefs(12)...)
	setAll(r, 12, false)
	r.Evaluate()
	for range 3 {
		r.bounce("t0", 5*time.Second)
	}
	r.clk.advance(time.Second)
	setAll(r, 12, true)
	r.Evaluate()
	if r.chatter(t, "a0") == nil || r.Flood() == nil {
		t.Fatal("no episodes standing")
	}
	if n := testing.AllocsPerRun(100, r.Evaluate); n != 0 {
		t.Errorf("Evaluate allocates %v per run with episodes standing, want 0", n)
	}
	if n := testing.AllocsPerRun(100, func() { _ = r.Rev() }); n != 0 {
		t.Errorf("Rev allocates %v", n)
	}
}

// Detection rows are not state changes: no notifier, no OnEvent.
func TestDetectionRowsReachNoNotifier(t *testing.T) {
	var heard []string
	r := detectRig(t, nil, def("A", "a"))
	r.OnEvent(func(e Event) { heard = append(heard, e.Kind) })
	r.tags["a"] = false
	r.Evaluate()
	for range 3 {
		r.bounce("a", 5*time.Second)
	}
	r.clk.advance(5 * time.Minute)
	r.Evaluate()
	for _, k := range heard {
		if k == KindChatter || k == KindChatterEnd {
			t.Fatalf("OnEvent heard %q", k)
		}
	}
	if rows := r.rows(t, KindChatter, KindChatterEnd); len(rows) != 2 {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestDetectConfigValidate(t *testing.T) {
	bad := []Options{
		{Chatter: ChatterConfig{Count: 1}},
		{Chatter: ChatterConfig{Window: Duration(time.Millisecond)}},
		{Flood: FloodConfig{Threshold: -1}},
		{Flood: FloodConfig{Threshold: FloodRing}},
		{Flood: FloodConfig{Window: Duration(time.Millisecond)}},
	}
	for _, o := range bad {
		o.Read = tags{}.read
		if _, err := New(o); err == nil {
			t.Errorf("New(%+v %+v) accepted", o.Chatter, o.Flood)
		}
	}
	r := detectRig(t, func(o *Options) {
		o.Chatter = ChatterConfig{Window: Duration(10 * time.Second), Count: 2}
	}, def("A", "a"))
	r.tags["a"] = false
	r.Evaluate()
	r.bounce("a", 0)
	r.bounce("a", 10*time.Second)
	if c := r.chatter(t, "A"); c == nil || c.Count != 2 || c.WindowMs != 10000 {
		t.Fatalf("configured chatter = %+v", c)
	}
}
