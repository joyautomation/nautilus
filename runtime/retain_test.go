package runtime

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/retain"
)

// fakeStore is an in-memory retain.Store whose failures are switchable, so
// the retry path is testable without a filesystem.
type fakeStore struct {
	mu    sync.Mutex
	state retain.State
	fail  bool
	saves int
}

func (f *fakeStore) Load() (retain.State, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return retain.State{}, errors.New("store down")
	}
	return f.state, nil
}

func (f *fakeStore) Save(s retain.State) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return errors.New("store down")
	}
	f.state, f.saves = s, f.saves+1
	return nil
}

func (f *fakeStore) Kind() string { return "fake" }

// flag is a Coordinator a test flips by hand.
type flag struct{ lead bool }

func (f *flag) IsLeader() bool { return f.lead }

// counterProgram increments a retained local each scan and mirrors it to a
// global, so a test can observe both scan activity and frame resets.
const counterProgram = `PROGRAM Counter
VAR n : INT; END_VAR
VAR_EXTERNAL Count : INT; SP : REAL; END_VAR
n := n + 1;
Count := n;
END_PROGRAM`

func newRetained(t *testing.T, store retain.Store, coord Coordinator) *Runtime {
	t.Helper()
	rt, err := New(Options{
		Program: counterProgram,
		Tags: []TagDef{
			Setpoint("SP", 65.0),
			State("Count", int64(0)),
		},
		Retain:      store,
		Coordinator: coord,
	})
	if err != nil {
		t.Fatal(err)
	}
	return rt
}

// Retained values must beat the manifest's seeds: what the operator set
// last outlives what the project file said first.
func TestRetainedValueLoadsBeforeFirstScan(t *testing.T) {
	store := &fakeStore{state: retain.State{Tags: map[string]any{"SP": 72.5}}}
	rt := newRetained(t, store, nil)
	rt.Scan()
	if got := rt.Tags().Real("SP"); got != 72.5 {
		t.Fatalf("SP = %v after first scan, want the retained 72.5 over the 65.0 seed", got)
	}
}

// The store is not a back door: a tag outside the retained set is ignored
// even if a state file (or a tampered ConfigMap) names it.
func TestLoadIgnoresTagsOutsideTheRetainedSet(t *testing.T) {
	store := &fakeStore{state: retain.State{Tags: map[string]any{
		"SP":    72.5,
		"Count": int64(999), // RoleState — not retained by default
	}}}
	rt := newRetained(t, store, nil)
	rt.Scan()
	if got := rt.Tags().vals["Count"].I; got == 999 {
		t.Fatal("a non-retained tag was written from the store")
	}
}

// JSON collapses numbers to float64; an integer-kind tag must come back as
// an integer, not get silently retyped to REAL.
func TestLoadRestoresIntegerKind(t *testing.T) {
	store := &fakeStore{state: retain.State{Tags: map[string]any{"Steps": 7.0}}}
	rt, err := New(Options{
		Program: `PROGRAM P VAR_EXTERNAL Steps : INT; END_VAR END_PROGRAM`,
		Tags:    []TagDef{Setpoint("Steps", int64(3))},
		Retain:  store,
	})
	if err != nil {
		t.Fatal(err)
	}
	rt.Scan()
	v, err := rt.Tags().ReadGlobal("Steps")
	if err != nil {
		t.Fatal(err)
	}
	if v.Kind != ir.TypeInt || v.I != 7 {
		t.Fatalf("Steps = kind %v value %v/%v, want INT 7", v.Kind, v.I, v.F)
	}
}

// A standby must not scan at all — no logic, no stats, and above all no
// output writes. Suppression by absence is the redundancy safety story.
func TestStandbyDoesNotScan(t *testing.T) {
	coord := &flag{lead: false}
	rt := newRetained(t, &fakeStore{}, coord)
	for range 5 {
		rt.Scan()
	}
	if n := rt.Stats().Count; n != 0 {
		t.Fatalf("standby executed %d scans, want 0", n)
	}
	if got := rt.Tags().vals["Count"].I; got != 0 {
		t.Fatalf("standby ran logic: Count = %d", got)
	}
}

// The takeover edge: re-read the store (the old leader may have accepted
// changes), reset the program frame (warm-start from the field, not from a
// stale VM state), and only then scan.
func TestTakeoverReloadsAndResetsFrames(t *testing.T) {
	store := &fakeStore{state: retain.State{Tags: map[string]any{"SP": 65.0}}}
	coord := &flag{lead: true}
	rt := newRetained(t, store, coord)

	for range 3 {
		rt.Scan()
	}
	if got := rt.Tags().vals["Count"].I; got != 3 {
		t.Fatalf("Count = %d after 3 scans, want 3", got)
	}

	// Lose leadership; the old leader retunes while we idle.
	coord.lead = false
	rt.Scan()
	store.mu.Lock()
	store.state.Tags["SP"] = 80.0
	store.mu.Unlock()

	// Take over: the retune must be picked up and the frame must restart.
	coord.lead = true
	rt.Scan()
	if got := rt.Tags().Real("SP"); got != 80.0 {
		t.Fatalf("SP = %v after takeover, want the old leader's 80.0", got)
	}
	if got := rt.Tags().vals["Count"].I; got != 1 {
		t.Fatalf("Count = %d after takeover, want 1 — the frame must reset", got)
	}
}

// An online-edited program rides the store: a replica restart (or failover)
// keeps running the edit, not the binary's baked-in source.
func TestRetainedProgramApplies(t *testing.T) {
	edited := strings.Replace(counterProgram, "n := n + 1;", "n := n + 10;", 1)
	store := &fakeStore{state: retain.State{Programs: map[string]string{MainTaskName: edited}}}
	rt := newRetained(t, store, nil)
	rt.Scan()
	if got := rt.Tags().vals["Count"].I; got != 10 {
		t.Fatalf("Count = %d, want 10 — the retained edit should be running", got)
	}
	if !rt.Program().Dirty() {
		t.Fatal("a retained edit must read as dirty, so `nautilus pull` still sees it")
	}
}

// A retained program that no longer compiles must not take down the
// controller: the error surfaces in stats, the built-in program runs.
func TestBrokenRetainedProgramIsReportedNotFatal(t *testing.T) {
	store := &fakeStore{state: retain.State{Programs: map[string]string{MainTaskName: "PROGRAM Broken syntax error"}}}
	rt := newRetained(t, store, nil)
	rt.Scan()
	if got := rt.Tags().vals["Count"].I; got != 1 {
		t.Fatalf("Count = %d, want 1 from the built-in program", got)
	}
	s := rt.Stats()
	if s.RetainErrors == 0 || s.LastRetainError == "" {
		t.Fatalf("a broken retained program left no trace in stats: %+v", s)
	}
}

// The saver writes only on change, and a failed save retries with the same
// content next tick instead of being forgotten.
func TestSaverWritesOnChangeAndRetriesOnFailure(t *testing.T) {
	store := &fakeStore{}
	rt := newRetained(t, store, nil)
	rt.Scan()

	last := rt.saveRetained(nil)
	if store.saves != 1 {
		t.Fatalf("saves = %d after first tick, want 1", store.saves)
	}
	last = rt.saveRetained(last)
	if store.saves != 1 {
		t.Fatalf("saves = %d after unchanged tick, want still 1", store.saves)
	}

	rt.Tags().SetReal("SP", 70.0)
	store.fail = true
	last = rt.saveRetained(last)
	if store.saves != 1 {
		t.Fatal("a failing store recorded a save")
	}
	if rt.Stats().RetainErrors == 0 {
		t.Fatal("a failed save left no trace in stats")
	}
	store.fail = false
	rt.saveRetained(last)
	store.mu.Lock()
	got := store.state.Tags["SP"]
	store.mu.Unlock()
	if got != 70.0 {
		t.Fatalf("retried save wrote %v, want 70.0", got)
	}
}

// A standby never saves — the ConfigMap store is last-writer-wins, and
// leadership is the mutual exclusion that makes that safe.
func TestStandbyNeverSaves(t *testing.T) {
	store := &fakeStore{}
	coord := &flag{lead: false}
	rt := newRetained(t, store, coord)
	rt.saveRetained(nil)
	if store.saves != 0 {
		t.Fatalf("standby saved %d times", store.saves)
	}
}

// What goes in the state: retained scalars and dirty program sources —
// and nothing else. A clean program must not shadow future deployments.
func TestRetainStateContents(t *testing.T) {
	rt := newRetained(t, &fakeStore{}, nil)
	rt.Scan()
	st := rt.retainState()
	if _, ok := st.Tags["SP"]; !ok {
		t.Fatalf("state is missing the setpoint: %+v", st.Tags)
	}
	if _, ok := st.Tags["Count"]; ok {
		t.Fatal("state includes a non-retained state tag")
	}
	if len(st.Programs) != 0 {
		t.Fatal("a clean (never-edited) program was persisted — it would shadow the next deploy's logic")
	}
	if err := rt.Program().Swap(strings.Replace(counterProgram, "+ 1", "+ 2", 1)); err != nil {
		t.Fatal(err)
	}
	st = rt.retainState()
	if _, ok := st.Programs[MainTaskName]; !ok {
		t.Fatal("an online-edited program was not persisted")
	}
}
