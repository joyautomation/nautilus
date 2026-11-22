package rationalize

import (
	"testing"
	"time"

	"github.com/joyautomation/nautilus/alarm"
)

// t0 is an aligned ten-minute boundary.
var t0 = time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)

func ev(id, kind string, at time.Duration) alarm.Event {
	return alarm.Event{TS: t0.Add(at).UnixMilli(), ID: id, Name: id + " name", Kind: kind, Priority: alarm.High}
}

func act(id string, at time.Duration) alarm.Event { return ev(id, alarm.KindActive, at) }
func rtn(id string, at time.Duration) alarm.Event { return ev(id, alarm.KindRTN, at) }

// A restart journals `active` again for a standing alarm, with no rtn in
// between: that is the same occurrence. A return to normal or a
// suppression ends it; acks and shelves do not.
func TestActivationsSkipARestartsReannunciation(t *testing.T) {
	got := Activations([]alarm.Event{
		act("A", 0),
		ev("A", alarm.KindAck, time.Second),
		act("A", time.Minute), // restart: same occurrence
		ev("A", alarm.KindShelve, 2*time.Minute),
		ev("A", alarm.KindUnshelve, 3*time.Minute),
		act("A", 4*time.Minute), // still the same
		rtn("A", 5*time.Minute),
		act("A", 6*time.Minute), // new
		ev("A", alarm.KindSuppress, 7*time.Minute),
		ev("A", alarm.KindUnsuppress, 8*time.Minute),
		act("A", 9*time.Minute), // new: the site came back
		act("B", 9*time.Minute), // another alarm is its own
	})
	want := []time.Duration{0, 6 * time.Minute, 9 * time.Minute, 9 * time.Minute}
	if len(got) != len(want) {
		t.Fatalf("got %d activations, want %d: %v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].TS != t0.Add(w).UnixMilli() {
			t.Errorf("activation %d at %v, want %v", i, time.UnixMilli(got[i].TS).Sub(t0), w)
		}
	}
}

func many(n int, id string, start, step time.Duration) []alarm.Event {
	var out []alarm.Event
	for i := range n {
		out = append(out, act(id+string(rune('a'+i%26))+string(rune('a'+i/26)), start+time.Duration(i)*step))
	}
	return out
}

// Ten in a window is not a flood; eleven is. A window's edge belongs to the
// next window, and consecutive flood windows are one flood.
func TestFloods(t *testing.T) {
	var acts []alarm.Event
	acts = append(acts, many(10, "x", 0, time.Second)...)              // window 0: 10, not a flood
	acts = append(acts, many(11, "y", 10*time.Minute, time.Second)...) // window 1: 11
	acts = append(acts, many(12, "z", 20*time.Minute, time.Second)...) // window 2: 12, joins window 1
	acts = append(acts, act("edge", 30*time.Minute))                   // window 3, not 2
	acts = append(acts, many(11, "w", 50*time.Minute, time.Second)...) // window 5: a second flood
	acts = append(acts, act("early", -time.Millisecond))               // before `from`: outside
	acts = append(acts, act("late", 60*time.Minute))                   // at `to`: outside

	rep := Floods(acts, t0, t0.Add(time.Hour), FloodWindow, FloodThreshold)
	if rep.Windows != 6 || rep.Activations != 45 {
		t.Fatalf("windows %d activations %d, want 6 and 45", rep.Windows, rep.Activations)
	}
	if rep.Max != 12 || !rep.MaxAt.Equal(t0.Add(20*time.Minute)) {
		t.Errorf("max %d at %v, want 12 at 10:20", rep.Max, rep.MaxAt)
	}
	if rep.FloodWindows != 3 || len(rep.Floods) != 2 {
		t.Fatalf("flood windows %d floods %d, want 3 and 2: %+v", rep.FloodWindows, len(rep.Floods), rep.Floods)
	}
	f := rep.Floods[0]
	if !f.Start.Equal(t0.Add(10*time.Minute)) || !f.End.Equal(t0.Add(30*time.Minute)) || f.Count != 23 || f.Peak != 12 {
		t.Errorf("first flood = %+v, want 10:10–10:30, 23 alarms, peak 12", f)
	}
	if rep.PerHour != 45 || rep.PerWindow != 7.5 || rep.FloodPercent != 50 {
		t.Errorf("rates %v/h %v/window %v%%", rep.PerHour, rep.PerWindow, rep.FloodPercent)
	}
}

// Windows start at from, not on the clock: a range from 10:05 has windows
// 10:05, 10:15, …, the last one short when the range is not whole windows,
// and a flood never reaches outside the range.
func TestFloodsWindowsStartAtFrom(t *testing.T) {
	from, to := t0.Add(5*time.Minute), t0.Add(30*time.Minute)
	acts := many(11, "x", 5*time.Minute, time.Second) // all in 10:05–10:15
	acts = append(acts, many(5, "y", 14*time.Minute+30*time.Second, time.Second)...)
	rep := Floods(acts, from, to, FloodWindow, FloodThreshold)
	if rep.Windows != 3 || len(rep.Floods) != 1 {
		t.Fatalf("windows %d floods %+v, want 3 windows and one flood", rep.Windows, rep.Floods)
	}
	if f := rep.Floods[0]; !f.Start.Equal(from) || f.Count != 16 {
		t.Errorf("flood %+v, want 16 from 10:05 (on the clock it would split across 10:00 and 10:10)", f)
	}
	if rep.PerWindow != rep.PerHour/6 {
		t.Errorf("per window %v is not per hour %v / 6", rep.PerWindow, rep.PerHour)
	}
	end := Floods(many(11, "z", 25*time.Minute, time.Second), from, to, FloodWindow, FloodThreshold)
	if f := end.Floods[0]; !f.End.Equal(to) {
		t.Errorf("a flood in the short last window ends %v, want %v", f.End, to)
	}
}

// Three activations exactly sixty seconds apart end to end chatter; three
// in sixty-one seconds and two in sixty do not. Overlapping bursts are one
// episode; bursts apart are two.
func TestChattering(t *testing.T) {
	acts := []alarm.Event{
		act("exact", 0), act("exact", 30*time.Second), act("exact", 60*time.Second),
		act("slow", 0), act("slow", 30*time.Second), act("slow", 61*time.Second),
		act("two", 0), act("two", 10*time.Second),
		// four inside a minute, overlapping a window that reaches 70 s
		// (one burst), then three more an hour later (a second).
		act("burst", 0), act("burst", 10*time.Second), act("burst", 20*time.Second),
		act("burst", 30*time.Second), act("burst", 70*time.Second),
		act("burst", time.Hour), act("burst", time.Hour+time.Second), act("burst", time.Hour+2*time.Second),
	}
	got := Chattering(acts, ChatterWindow, ChatterCount)
	if len(got) != 2 {
		t.Fatalf("chattering = %+v, want burst and exact", got)
	}
	b, e := got[0], got[1]
	if b.ID != "burst" || b.Episodes != 2 || b.Activations != 8 || b.Peak != 4 || !b.First.Equal(t0) {
		t.Errorf("burst = %+v, want 2 episodes, 8 activations, peak 4 from t0", b)
	}
	if e.ID != "exact" || e.Episodes != 1 || e.Peak != 3 {
		t.Errorf("exact = %+v, want one episode, peak 3", e)
	}
}

// The top n by activations, ties broken by id, with each one's share.
func TestBadActors(t *testing.T) {
	acts := []alarm.Event{
		act("b", 0), act("b", time.Minute), act("b", 2*time.Minute),
		act("c", 0), act("a", 0), act("d", 0),
	}
	got := BadActors(acts, 3)
	ids := []string{got[0].ID, got[1].ID, got[2].ID}
	if len(got) != 3 || ids[0] != "b" || ids[1] != "a" || ids[2] != "c" {
		t.Fatalf("bad actors = %v, want [b a c]", ids)
	}
	if got[0].Count.Count != 3 || got[0].Percent != 50 {
		t.Errorf("b = %+v, want 3 (50%%)", got[0])
	}
}

// An alarm active for more than a day at asOf is stale; one that went
// back to normal, or has been active exactly a day, is not; a restart's
// repeated `active` does not reset the clock.
func TestStaleAlarms(t *testing.T) {
	asOf := t0.Add(48 * time.Hour)
	events := []alarm.Event{
		act("old", 0),
		act("old", 30*time.Hour), // restart
		act("cleared", 0), rtn("cleared", time.Hour),
		act("day", 24*time.Hour), // exactly 24 h at asOf: not stale
		act("older", -time.Hour),
		act("future", 49*time.Hour), // after asOf
	}
	got := StaleAlarms(events, asOf, StaleAfter)
	if len(got) != 2 || got[0].ID != "older" || got[1].ID != "old" {
		t.Fatalf("stale = %+v, want older then old", got)
	}
	if got[1].For != 48*time.Hour || !got[1].Since.Equal(t0) {
		t.Errorf("old = %+v, want 48 h since t0 (the restart does not reset it)", got[1])
	}
}

func withOcc(e alarm.Event, occ string) alarm.Event { e.Occurrence = occ; return e }

// Rows that name their occurrence (#715) count as they come: a flap
// through unack-rtn is an activation (chattering), and a restart that
// continued the occurrence journalled none. A restart that did not
// continue it began a new one, which counts.
func TestActivationsWithOccurrences(t *testing.T) {
	got := Activations([]alarm.Event{
		act("A", 0),                            // before occurrences
		act("A", time.Minute),                  // its restart: not counted
		withOcc(act("A", 2*time.Minute), "o1"), // upgraded and restarted: a new occurrence
		withOcc(rtn("A", 3*time.Minute), "o1"),
		withOcc(act("A", 4*time.Minute), "o1"), // flap through unack-rtn: counts
		withOcc(act("A", 5*time.Minute), "o2"), // a new occurrence, no rtn journalled between
	})
	want := []time.Duration{0, 2 * time.Minute, 4 * time.Minute, 5 * time.Minute}
	if len(got) != len(want) {
		t.Fatalf("got %d activations, want %d: %v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].TS != t0.Add(w).UnixMilli() {
			t.Errorf("activation %d at %v, want %v", i, time.UnixMilli(got[i].TS).Sub(t0), w)
		}
	}
}

// A carried occurrence's age runs through a restart; the fresh engine's
// startup suppression (no occurrence) does not end it; a new occurrence
// with no end between starts the clock again.
func TestStaleAlarmsWithOccurrences(t *testing.T) {
	asOf := t0.Add(48 * time.Hour)
	events := []alarm.Event{
		withOcc(act("kept", 0), "k1"),
		ev("kept", alarm.KindSuppress, 30*time.Hour), // restart, site not yet birthed
		ev("kept", alarm.KindUnsuppress, 30*time.Hour+time.Second),
		withOcc(act("new", 0), "n1"),
		withOcc(act("new", 40*time.Hour), "n2"), // the restart did not continue n1
		withOcc(act("gone", 0), "g1"),
		withOcc(ev("gone", alarm.KindSuppress, time.Hour), "g1"), // a real suppression ends it
	}
	got := StaleAlarms(events, asOf, StaleAfter)
	if len(got) != 1 || got[0].ID != "kept" || got[0].For != 48*time.Hour {
		t.Fatalf("stale = %+v, want kept for 48 h only", got)
	}
}
