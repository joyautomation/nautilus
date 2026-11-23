package runtime

import (
	"errors"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/retain"
)

// #1029 3c-3c-2a in the runtime: a controller whose operator-intent log was
// superseded by another history (a loss a person accepted elsewhere) must
// never restore the old history's substitutes: their IDs are its seqs,
// which the new history numbers again.

// staleLog is a fakeLog that says the retain store's substitutes are
// another log history's.
type staleLog struct {
	fakeLog
	stale bool
}

func (s *staleLog) RetainedStale() bool { return s.stale }

func retainedAt(clk *subClock) *fakeStore {
	return &fakeStore{state: retain.State{Substitutes: &retain.SubstituteState{
		Entries: []retain.SubstituteRetain{{ID: 5, Name: "Level", Value: 95.0, By: "op",
			StartMs: clk.Now().UnixMilli(), UntilMs: clk.Now().Add(time.Hour).UnixMilli(), GrantedMs: time.Hour.Milliseconds()}},
		HighID: 5, LastSeenMs: clk.Now().UnixMilli(),
	}}}
}

// A superseded standby takes over before its restart, its catch-up
// failing: it restores none of the retained substitutes, and says the
// catch-up failed.
func TestATakeoverRestoresNoneOfASupersededLogsSubstitutes(t *testing.T) {
	clk := noon()
	coord := lead(false)
	rt, _ := newRetainSubRig(t, coord, retainedAt(clk), clk)
	rt.Scan()
	log := &staleLog{fakeLog: fakeLog{syncErr: errors.New("superseded")}, stale: true}
	rt.SetIntentLog(log, 50*time.Millisecond)
	coord.set(true)
	scanUntilLogic(t, rt)
	if l := rt.Substitutes(); len(l) != 0 {
		t.Fatalf("restored %+v from a superseded log's retain store", l)
	}
	if !rt.Tags().Bool(SubstituteFaultTag) {
		t.Fatal("no catch-up fault")
	}
}

// Not stale: the fallback restores them as before (the test's premise).
func TestATakeoverRestoresACurrentLogsSubstitutes(t *testing.T) {
	clk := noon()
	coord := lead(false)
	rt, _ := newRetainSubRig(t, coord, retainedAt(clk), clk)
	rt.Scan()
	rt.SetIntentLog(&staleLog{fakeLog: fakeLog{syncErr: errors.New("no majority")}}, 50*time.Millisecond)
	coord.set(true)
	if m := scanUntilLogic(t, rt); m != 95 {
		t.Fatalf("first scan on Level %v; want the retained 95", m)
	}
}

// DropRetainedIntent clears the retain store's substitutes and its alarm
// acks and shelves (the old history's), and keeps the rest (the high ID,
// retained tags). stale_retained_alarms_kept turns it red.
func TestDropRetainedIntent(t *testing.T) {
	clk := noon()
	store := retainedAt(clk)
	store.state.Tags = map[string]any{"SP": 3.0}
	store.state.Alarms = map[string]retain.AlarmRetain{"Trip": {ShelfUntilMs: clk.Now().Add(time.Hour).UnixMilli(), ShelfBy: "op"}}
	store.state.AlarmFlood = &retain.AlarmFlood{ID: "flood-1", SinceMs: 1, OverMs: 1} // the old history's too
	rt, _ := newRetainSubRig(t, lead(false), store, clk)
	k, err := rt.DropRetainedIntent()
	if err != nil || k != 2 {
		t.Fatalf("dropped %d, %v", k, err)
	}
	st, _ := store.Load()
	if st.Substitutes == nil || len(st.Substitutes.Entries) != 0 || st.Substitutes.HighID != 5 || st.Tags["SP"] != 3.0 || len(st.Alarms) != 0 || st.AlarmFlood != nil {
		t.Fatalf("store after the drop: %+v", st)
	}
	if k, err := rt.DropRetainedIntent(); err != nil || k != 0 {
		t.Fatalf("a second drop: %d, %v", k, err)
	}
}

// A standby that applied a substitute from its log, whose log is then
// superseded, takes over before its restart: the substitute it applied is
// the old history's and ends at the takeover (log-superseded), whether it
// was running on the standby's view or still waiting for its input.
func TestATakeoverEndsTheSubstitutesASupersededLogApplied(t *testing.T) {
	for _, readInput := range []bool{true, false} {
		clk := noon()
		coord := lead(false)
		rt, _ := newRetainSubRig(t, coord, nil, clk)
		rt.Scan()
		if readInput {
			rt.tags.setAny("Level", ir.RealVal(10))
		}
		log := &staleLog{fakeLog: fakeLog{syncErr: errors.New("superseded")}}
		rt.SetIntentLog(log, 50*time.Millisecond)
		if err := rt.ApplySubstitute(replicated(clk, 7, "Level", 95, time.Hour)); err != nil {
			t.Fatal(err)
		}
		if readInput && len(rt.Substitutes()) != 1 {
			t.Fatalf("setup: %+v", rt.Substitutes())
		}
		log.stale = true
		events := collect(rt)
		coord.set(true)
		if m := scanUntilLogic(t, rt); m == 95 {
			t.Fatalf("input read %v: the first scan ran on the superseded log's substitute", readInput)
		}
		if l := rt.Substitutes(); len(l) != 0 {
			t.Fatalf("input read %v: still active: %+v", readInput, l)
		}
		if !hasEvent(events(), SubstituteEventStop, SubstituteReasonLogSuperseded) {
			t.Fatalf("input read %v: no log-superseded stop", readInput)
		}
	}
}
