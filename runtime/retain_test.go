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

// A retained STRUCT tag must both persist (retainState) and restore
// (loadRetained) member by member: setAny has no case for a map, so before
// this fix a struct tag's operator setpoints silently reverted to the
// manifest's seed on every restart or takeover.
const motorLib = `
TYPE
  Motor : STRUCT
    Speed : REAL;
    Starts : INT;
    Name : STRING;
  END_STRUCT;
END_TYPE
`

const motorProg = `PROGRAM P VAR_EXTERNAL P101 : Motor; END_VAR END_PROGRAM`

func TestRetainStatePersistsStructTag(t *testing.T) {
	rt, err := New(Options{
		Program:   motorProg,
		Libraries: []string{motorLib},
		Tags: []TagDef{
			Typed("P101", RoleSetpoint, "Motor",
				Init(map[string]any{"Speed": 42.0})),
		},
		Retain: &fakeStore{},
	})
	if err != nil {
		t.Fatal(err)
	}
	st := rt.retainState()
	got, ok := st.Tags["P101"].(map[string]any)
	if !ok {
		t.Fatalf("retainState did not persist struct tag P101: %+v", st.Tags)
	}
	if got["Speed"] != 42.0 {
		t.Fatalf("P101.Speed = %v, want 42.0", got["Speed"])
	}
}

func TestRetainedStructTagRestoresBeforeFirstScan(t *testing.T) {
	store := &fakeStore{state: retain.State{Tags: map[string]any{
		"P101": map[string]any{"Speed": 88.0, "Starts": 5.0},
	}}}
	rt, err := New(Options{
		Program:   motorProg,
		Libraries: []string{motorLib},
		Tags: []TagDef{
			Typed("P101", RoleSetpoint, "Motor",
				Init(map[string]any{"Speed": 42.0, "Name": "seed"})),
		},
		Retain: store,
	})
	if err != nil {
		t.Fatal(err)
	}
	rt.Scan()
	v, err := rt.Tags().ReadGlobal("P101")
	if err != nil {
		t.Fatal(err)
	}
	if v.Kind != ir.TypeStruct {
		t.Fatalf("P101 kind = %v, want TypeStruct", v.Kind)
	}
	all := rt.Tags().All()["P101"].(map[string]any)
	if all["Speed"] != 88.0 {
		t.Fatalf("P101.Speed = %v, want the retained 88.0 over the 42.0 seed", all["Speed"])
	}
	if got, ok := all["Starts"].(int64); !ok || got != 5 {
		t.Fatalf("P101.Starts = %v (kind %T), want the retained int 5, not a float64", all["Starts"], all["Starts"])
	}
	// A member the retained map didn't name (Name) must keep the seed's
	// value, not zero out — the retained restore is a merge, same as
	// SeedFromInit/SetPath.
	if all["Name"] != "seed" {
		t.Fatalf("P101.Name = %v, want the seed's %q to survive the merge", all["Name"], "seed")
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

// ── alarms ─────────────────────────────────────────────────────────────

// fakeAlarms is a retain.AlarmRetainer a test drives by hand — the alarm
// engine's shape without the alarm package, which is exactly the
// dependency direction retain.AlarmRetainer exists to preserve.
type fakeAlarms struct {
	mu       sync.Mutex
	held     map[string]retain.AlarmRetain
	restored []map[string]retain.AlarmRetain
}

func (f *fakeAlarms) RetainedAlarms() map[string]retain.AlarmRetain {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.held
}

func (f *fakeAlarms) RestoreAlarms(m map[string]retain.AlarmRetain) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.restored = append(f.restored, m)
}

// An ack is a decision a person made, so it persists beside the setpoints
// — and comes back on the takeover that starts the next leadership term.
func TestAlarmStateRoundTripsThroughRetain(t *testing.T) {
	store := &fakeStore{state: retain.State{
		Alarms: map[string]retain.AlarmRetain{
			"HiTemp": {Acked: true, AckBy: "rchon", AckMs: 1700000000000},
		},
	}}
	rt := newRetained(t, store, nil)
	eng := &fakeAlarms{held: map[string]retain.AlarmRetain{
		"LoFlow": {ShelfUntilMs: 1700000600000, ShelfBy: "rchon"},
	}}
	rt.SetAlarms(eng)

	rt.Scan() // the first scan is a takeover, which is what loads the store

	if len(eng.restored) != 1 {
		t.Fatalf("RestoreAlarms called %d times, want once on takeover", len(eng.restored))
	}
	if got := eng.restored[0]["HiTemp"]; !got.Acked || got.AckBy != "rchon" {
		t.Errorf("restored %+v, want the stored acknowledgement", got)
	}

	if got := rt.retainState().Alarms; len(got) != 1 || got["LoFlow"].ShelfBy != "rchon" {
		t.Errorf("saved alarms = %+v, want the engine's shelf", got)
	}
}

// The whole point of retaining ack: a standby taking over must not
// resurrect acknowledged alarms as unacknowledged. takeover() re-reads the
// store on the standby→leader edge, so the engine hears about it again.
func TestTakeoverRestoresAlarmsOnTheLeadershipEdge(t *testing.T) {
	f := &flag{lead: true}
	store := &fakeStore{state: retain.State{
		Alarms: map[string]retain.AlarmRetain{"HiTemp": {Acked: true, AckBy: "rchon"}},
	}}
	rt := newRetained(t, store, f)
	eng := &fakeAlarms{}
	rt.SetAlarms(eng)

	rt.Scan()
	f.lead = false
	rt.Scan() // standby: no takeover, no scan
	f.lead = true
	rt.Scan() // and back: a fresh takeover

	if len(eng.restored) != 2 {
		t.Fatalf("RestoreAlarms called %d times, want one per acquisition of leadership", len(eng.restored))
	}
}

// A project with no alarms writes exactly what it wrote before alarms
// existed: retain.State.Alarms is omitempty, and nothing registers a
// retainer, so an existing store loads and saves unchanged.
func TestNoAlarmRetainerLeavesTheStateAlone(t *testing.T) {
	store := &fakeStore{}
	rt := newRetained(t, store, nil)
	rt.Scan()
	if got := rt.retainState().Alarms; got != nil {
		t.Errorf("a runtime with no alarm engine saved %+v", got)
	}
}
