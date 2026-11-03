package runtime

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

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
// even if a state file (or a tampered ConfigMap) names it. The program
// here never writes Count, so a value the load let through would still be
// there after the scan (counterProgram rewrites Count every scan and would
// hide it).
func TestLoadIgnoresTagsOutsideTheRetainedSet(t *testing.T) {
	store := &fakeStore{state: retain.State{Tags: map[string]any{
		"SP":    72.5,
		"Count": 999.0, // RoleState — not retained by default
	}}}
	rt, err := New(Options{
		Program: `PROGRAM Idle VAR_EXTERNAL Count : INT; SP : REAL; END_VAR END_PROGRAM`,
		Tags: []TagDef{
			Setpoint("SP", 65.0),
			State("Count", int64(7)),
		},
		Retain: store,
	})
	if err != nil {
		t.Fatal(err)
	}
	rt.Scan() // the first scan's takeover is what loads the store
	if got := rt.Tags().Real("SP"); got != 72.5 {
		t.Fatalf("SP = %v; the load did not run (want the retained 72.5)", got)
	}
	v, err := rt.Tags().ReadGlobal("Count")
	if err != nil {
		t.Fatal(err)
	}
	if v.I != 7 || v.F != 0 {
		t.Fatalf("Count = %+v; want its seed 7: a non-retained tag was written from the store", v)
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
	if got := rt.Tags().vals["Count"].v.I; got != 0 {
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
	if got := rt.Tags().vals["Count"].v.I; got != 3 {
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
	if got := rt.Tags().vals["Count"].v.I; got != 1 {
		t.Fatalf("Count = %d after takeover, want 1 — the frame must reset", got)
	}
}

// An online-edited program rides the store: a replica restart (or failover)
// keeps running the edit, not the binary's baked-in source.
func TestRetainedProgramApplies(t *testing.T) {
	edited := strings.Replace(counterProgram, "n := n + 1;", "n := n + 10;", 1)
	store := &fakeStore{state: retain.State{
		Programs:     map[string]string{MainTaskName: edited},
		ProgramBases: map[string]string{MainTaskName: sourceHash(counterProgram)},
	}}
	rt := newRetained(t, store, nil)
	rt.Scan()
	if got := rt.Tags().vals["Count"].v.I; got != 10 {
		t.Fatalf("Count = %d, want 10 — the retained edit should be running", got)
	}
	if !rt.Program().Dirty() {
		t.Fatal("a retained edit must read as dirty, so `naut pull` still sees it")
	}
}

// OE-005: an online edit survives a restart when a retain store is
// configured. The edit is saved by the leader's saver tick and reloaded by
// the next runtime on the same store before its first scan. A newer binary
// (a different built-in program) is a deploy, and the deploy wins (#359).
func TestOnlineEditSurvivesARestartThroughTheStore(t *testing.T) {
	store := &fakeStore{}
	rt := newRetained(t, store, nil)
	rt.Scan()
	if err := rt.Program().Swap(strings.Replace(counterProgram, "n := n + 1;", "n := n + 10;", 1)); err != nil {
		t.Fatal(err)
	}
	rt.saveRetained(nil)
	if _, ok := store.state.Programs[MainTaskName]; !ok {
		t.Fatal("the saver did not persist the online edit")
	}

	// Restart: a fresh runtime from the same built-in program and store.
	rt2 := newRetained(t, store, nil)
	rt2.Scan()
	if got := rt2.Tags().vals["Count"].v.I; got != 10 {
		t.Fatalf("Count = %d after a restart, want 10 from the online edit", got)
	}

	// A newer binary (a different built-in program) runs its own program:
	// a deploy replaces the edit.
	rt3, err := New(Options{
		Program: strings.Replace(counterProgram, "n := n + 1;", "n := n + 5;", 1),
		Tags:    []TagDef{Setpoint("SP", 65.0), State("Count", int64(0))},
		Retain:  store,
	})
	if err != nil {
		t.Fatal(err)
	}
	rt3.Scan()
	if got := rt3.Tags().vals["Count"].v.I; got != 5 {
		t.Fatalf("Count = %d on a newer binary, want 5: a deploy replaces the retained edit", got)
	}

	// A build whose program is the edit (pulled and committed) runs clean,
	// and its next save drops the retained source.
	rt4, err := New(Options{
		Program: strings.Replace(counterProgram, "n := n + 1;", "n := n + 10;", 1),
		Tags:    []TagDef{Setpoint("SP", 65.0), State("Count", int64(0))},
		Retain:  store,
	})
	if err != nil {
		t.Fatal(err)
	}
	if rt4.Program().Dirty() {
		t.Fatal("a build matching the retained edit still reads as edited")
	}
	if st := rt4.retainState(); len(st.Programs) != 0 {
		t.Fatalf("a build matching the edit still persists it: %v", st.Programs)
	}
}

// ── online edits across a restart vs. a deploy (#359) ────────────────────

// newBuild is a runtime on the given store whose binary embeds program — a
// restart when program is counterProgram, a deploy of a different build
// otherwise.
func newBuild(t *testing.T, store retain.Store, program string) *Runtime {
	t.Helper()
	rt, err := New(Options{
		Program: program,
		Tags:    []TagDef{Setpoint("SP", 65.0), State("Count", int64(0))},
		Retain:  store,
	})
	if err != nil {
		t.Fatal(err)
	}
	return rt
}

// editAndSave makes an online edit (n := n + 10) on a fresh runtime of the
// counterProgram build and lets the saver persist it, as a running
// controller does every saveInterval.
func editAndSave(t *testing.T, store *fakeStore) string {
	t.Helper()
	edited := strings.Replace(counterProgram, "n := n + 1;", "n := n + 10;", 1)
	rt := newRetained(t, store, nil)
	rt.Scan()
	if _, err := rt.Program().SwapWarm(edited); err != nil {
		t.Fatal(err)
	}
	rt.saveRetained(nil)
	if store.state.Programs[MainTaskName] != edited {
		t.Fatal("the saver did not persist the online edit")
	}
	if got, want := store.state.ProgramBases[MainTaskName], sourceHash(counterProgram); got != want {
		t.Fatalf("saved edit base = %q, want the build's program hash %q", got, want)
	}
	return edited
}

// A restart of the SAME build keeps running the online edit: the store
// reloads it before the first scan, as a PLC keeps its edits through a
// power cycle.
func TestOnlineEditSurvivesARestartOfTheSameBuild(t *testing.T) {
	store := &fakeStore{}
	editAndSave(t, store)

	rt := newBuild(t, store, counterProgram)
	rt.Scan()
	if got := rt.Tags().vals["Count"].v.I; got != 10 {
		t.Fatalf("Count = %d after a restart of the same build, want 10 from the online edit", got)
	}
	if !rt.Program().Dirty() {
		t.Fatal("the reloaded edit must read as modified, so `naut pull` still sees it")
	}
	if d := rt.Stats().RetainDiscardedEdits; len(d) != 0 {
		t.Fatalf("a restart of the same build reported discarded edits: %v", d)
	}
	if st := rt.retainState(); st.Programs[MainTaskName] == "" {
		t.Fatal("the reloaded edit is no longer persisted")
	}
}

// A deploy wins: a build whose program for the task differs from the one
// the edit was made against runs its own program, the edit is discarded
// (named in diagnostics), and the next save drops it from the store.
func TestNewDeployReplacesARetainedOnlineEdit(t *testing.T) {
	store := &fakeStore{}
	edited := editAndSave(t, store)

	deployed := strings.Replace(counterProgram, "n := n + 1;", "n := n + 5;", 1)
	rt := newBuild(t, store, deployed)
	rt.Scan()
	if got := rt.Tags().vals["Count"].v.I; got != 5 {
		t.Fatalf("Count = %d on a new build, want 5 from the deployed program, not the old edit", got)
	}
	if rt.Program().Dirty() {
		t.Fatal("the new build reads as online-edited after its deploy discarded the edit")
	}
	s := rt.Stats()
	if len(s.RetainDiscardedEdits) != 1 || s.RetainDiscardedEdits[0] != MainTaskName {
		t.Fatalf("RetainDiscardedEdits = %v, want [%s]", s.RetainDiscardedEdits, MainTaskName)
	}
	if s.RetainErrors != 0 {
		t.Fatalf("a discarded edit counted as a retain error: %+v", s.LastRetainError)
	}
	rt.saveRetained(nil)
	if len(store.state.Programs) != 0 || len(store.state.ProgramBases) != 0 {
		t.Fatalf("the discarded edit is still applied from the store: %v %v", store.state.Programs, store.state.ProgramBases)
	}
	// The safety net: the edit's source is kept, never applied, so an edit
	// nobody pulled into git before the deploy can still be recovered.
	if got := store.state.DiscardedPrograms[MainTaskName]; got != edited {
		t.Fatalf("discardedPrograms[%s] = %q, want the discarded edit's source", MainTaskName, got)
	}

	// A restart of the new build stays on the deployed program, and the
	// discarded source survives its load and save.
	rt2 := newBuild(t, store, deployed)
	rt2.Scan()
	if got := rt2.Tags().vals["Count"].v.I; got != 5 {
		t.Fatalf("Count = %d after restarting the new build, want 5", got)
	}
	rt2.saveRetained(nil)
	if got := store.state.DiscardedPrograms[MainTaskName]; got != edited {
		t.Fatal("a later save lost the discarded edit's source")
	}

	// A newer discard for the same task overwrites the older one.
	second := strings.Replace(deployed, "n := n + 5;", "n := n + 7;", 1)
	if _, err := rt2.Program().SwapWarm(second); err != nil {
		t.Fatal(err)
	}
	rt2.saveRetained(nil)
	rt3 := newBuild(t, store, counterProgram)
	rt3.Scan()
	rt3.saveRetained(nil)
	if got := store.state.DiscardedPrograms[MainTaskName]; got != second {
		t.Fatalf("discardedPrograms[%s] = %q, want the newest discarded edit", MainTaskName, got)
	}
}

// A build that contains the edit (pulled into git and shipped) has nothing
// to swap: it runs clean, nothing is reported discarded, and the next save
// clears the entry.
func TestBuildContainingTheEditClearsTheRetainedEntry(t *testing.T) {
	store := &fakeStore{}
	edited := editAndSave(t, store)

	rt := newBuild(t, store, edited)
	rt.Scan()
	if got := rt.Tags().vals["Count"].v.I; got != 10 {
		t.Fatalf("Count = %d, want 10 from the build that contains the edit", got)
	}
	if rt.Program().Dirty() {
		t.Fatal("a build matching the retained edit reads as edited")
	}
	if d := rt.Stats().RetainDiscardedEdits; len(d) != 0 {
		t.Fatalf("a build containing the edit reported it discarded: %v", d)
	}
	rt.saveRetained(nil)
	if len(store.state.Programs) != 0 {
		t.Fatalf("a build matching the edit still persists it: %v", store.state.Programs)
	}
}

// A store written before edit bases were recorded has an edit with no base.
// Nothing proves it was made against this build — a deploy may have
// happened since — so it is treated as replaced by a deploy: dropped,
// named in diagnostics, and the binary's program runs.
func TestRetainedEditWithoutABaseIsDiscarded(t *testing.T) {
	edited := strings.Replace(counterProgram, "n := n + 1;", "n := n + 10;", 1)
	store := &fakeStore{state: retain.State{Programs: map[string]string{MainTaskName: edited}}}
	rt := newRetained(t, store, nil)
	rt.Scan()
	if got := rt.Tags().vals["Count"].v.I; got != 1 {
		t.Fatalf("Count = %d, want 1 from the binary's program: a base-less edit must not run", got)
	}
	if d := rt.Stats().RetainDiscardedEdits; len(d) != 1 || d[0] != MainTaskName {
		t.Fatalf("RetainDiscardedEdits = %v, want [%s]", d, MainTaskName)
	}

	// Unless the binary already IS the edit: then it is simply cleared.
	store = &fakeStore{state: retain.State{Programs: map[string]string{MainTaskName: edited}}}
	rt = newBuild(t, store, edited)
	rt.Scan()
	if d := rt.Stats().RetainDiscardedEdits; len(d) != 0 {
		t.Fatalf("a base-less edit the build contains was reported discarded: %v", d)
	}
}

// A takeover re-reads the store. A standby of the same build picks up the
// old leader's edit; a standby of a different build (a rolling deploy)
// does not, and keeps its own program.
func TestTakeoverAppliesOnlyASameBuildEdit(t *testing.T) {
	store := &fakeStore{}
	editAndSave(t, store)

	same := newRetained(t, store, &flag{lead: true})
	same.Scan()
	if got := same.Tags().vals["Count"].v.I; got != 10 {
		t.Fatalf("same-build takeover: Count = %d, want 10 from the old leader's edit", got)
	}

	other, err := New(Options{
		Program:     strings.Replace(counterProgram, "n := n + 1;", "n := n + 5;", 1),
		Tags:        []TagDef{Setpoint("SP", 65.0), State("Count", int64(0))},
		Retain:      store,
		Coordinator: &flag{lead: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	other.Scan()
	if got := other.Tags().vals["Count"].v.I; got != 5 {
		t.Fatalf("new-build takeover: Count = %d, want 5 from its own program", got)
	}
}

// A retained program that no longer compiles must not take down the
// controller: the error surfaces in stats, the built-in program runs.
func TestBrokenRetainedProgramIsReportedNotFatal(t *testing.T) {
	store := &fakeStore{state: retain.State{
		Programs:     map[string]string{MainTaskName: "PROGRAM Broken syntax error"},
		ProgramBases: map[string]string{MainTaskName: sourceHash(counterProgram)},
	}}
	rt := newRetained(t, store, nil)
	rt.Scan()
	if got := rt.Tags().vals["Count"].v.I; got != 1 {
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
			"HiTemp": {Acked: true, AckBy: "operator1", AckMs: 1700000000000},
		},
	}}
	rt := newRetained(t, store, nil)
	eng := &fakeAlarms{held: map[string]retain.AlarmRetain{
		"LoFlow": {ShelfUntilMs: 1700000600000, ShelfBy: "operator1"},
	}}
	rt.SetAlarms(eng)

	rt.Scan() // the first scan is a takeover, which is what loads the store

	if len(eng.restored) != 1 {
		t.Fatalf("RestoreAlarms called %d times, want once on takeover", len(eng.restored))
	}
	if got := eng.restored[0]["HiTemp"]; !got.Acked || got.AckBy != "operator1" {
		t.Errorf("restored %+v, want the stored acknowledgement", got)
	}

	if got := rt.retainState().Alarms; len(got) != 1 || got["LoFlow"].ShelfBy != "operator1" {
		t.Errorf("saved alarms = %+v, want the engine's shelf", got)
	}
}

// The whole point of retaining ack: a standby taking over must not
// resurrect acknowledged alarms as unacknowledged. takeover() re-reads the
// store on the standby→leader edge, so the engine hears about it again.
func TestTakeoverRestoresAlarmsOnTheLeadershipEdge(t *testing.T) {
	f := &flag{lead: true}
	store := &fakeStore{state: retain.State{
		Alarms: map[string]retain.AlarmRetain{"HiTemp": {Acked: true, AckBy: "operator1"}},
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

// Nothing re-seeds a tag at takeover. An unretained state tag is not
// carried between replicas, so a new leader keeps its OWN copy: its seed if
// it never led, what it held when it last led otherwise. Only a restart
// starts the tag from its seed, because the process is new. A plain VAR
// restarts from its declaration at every takeover (the frame resets).
func TestTakeoverKeepsUnretainedStateTags(t *testing.T) {
	coord := &flag{lead: false}
	rt, err := New(Options{
		Program: `PROGRAM P
VAR n : INT; END_VAR
VAR_EXTERNAL Total : DINT; Calls : INT; END_VAR
n := n + 1;
Calls := n;
Total := Total + 1;
END_PROGRAM`,
		Tags:        []TagDef{State("Total", int64(10)), State("Calls", int64(0))},
		Retain:      &fakeStore{},
		Coordinator: coord,
	})
	if err != nil {
		t.Fatal(err)
	}
	read := func(name string) int64 { v, _ := rt.Tags().ReadGlobal(name); return v.I }

	// A standby that has never led holds the seed, and takes over from it.
	rt.Scan()
	coord.lead = true
	rt.Scan()
	rt.Scan()
	if got := read("Total"); got != 12 {
		t.Fatalf("Total = %d after first leading for 2 scans from seed 10, want 12", got)
	}
	// Step down, then lead again: Total carries on from this replica's own
	// copy (12), not from the seed (10); the plain VAR n starts over.
	coord.lead = false
	rt.Scan()
	coord.lead = true
	rt.Scan()
	if got := read("Total"); got != 13 {
		t.Errorf("Total = %d after a second takeover, want 13: nothing re-seeds a tag at takeover", got)
	}
	if got := read("Calls"); got != 1 {
		t.Errorf("Calls = %d after a second takeover, want 1: a plain VAR restarts from its declaration", got)
	}
}

// An orderly stop (Run's context cancelled: SIGTERM, Ctrl+C) saves once
// more after the last scan, so a setpoint written less than a save interval
// before the stop survives the restart. The saver used to return on the
// cancel with nothing written: the operator's last write was accepted, was
// live, and came back as the old value (nautilus#531, black-box F-19).
func TestRunSavesOnceMoreOnAnOrderlyStop(t *testing.T) {
	store := &fakeStore{}
	rt := newRetained(t, store, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { rt.Run(ctx); close(done) }()
	waitUntil(t, func() bool { v, err := rt.Tags().ReadGlobal("Count"); return err == nil && v.I > 0 }) // the takeover has run
	rt.Tags().Set("SP", 42.0)
	cancel() // well inside the first save interval
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after its context was cancelled")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if got := store.state.Tags["SP"]; got != 42.0 {
		t.Fatalf("store holds SP = %v after the stop (saves: %d), want the 42 written just before it", got, store.saves)
	}
}

// A replica that is not leading writes nothing on its way out either: the
// last save is the saver's tick, with the same leader check, or a stopping
// standby would overwrite the leader's state with its own seeds.
func TestRunStandbyDoesNotSaveOnStop(t *testing.T) {
	store := &fakeStore{}
	rt := newRetained(t, store, &flag{lead: false})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { rt.Run(ctx); close(done) }()
	time.Sleep(50 * time.Millisecond) // a few gated scans
	rt.Tags().Set("SP", 42.0)
	cancel()
	<-done
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.saves != 0 {
		t.Fatalf("a standby saved %d time(s) on stop: %+v", store.saves, store.state)
	}
}

// A leader that has lost the lease since its last scan (the elector stepped
// down, no scan has run since) writes nothing on its way out either: its
// takeover still says leading, so only the coordinator's answer stops a
// last-writer-wins save over the new leader's state.
func TestRunLostLeaseDoesNotSaveOnStop(t *testing.T) {
	store := &fakeStore{}
	lease := &flag{lead: true}
	rt := newRetained(t, store, lease)
	rt.Scan() // the takeover: this replica now leads
	rt.Tags().Set("SP", 42.0)
	lease.lead = false
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // stopped before any further scan
	rt.Run(ctx)
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.saves != 0 {
		t.Fatalf("a replica that lost the lease saved %d time(s) on stop: %+v", store.saves, store.state)
	}
}

// waitUntil polls cond for up to five seconds.
func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met within 5 s")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// The last save's failure is logged: a tick's failure is retried and shown
// in the diagnostics, but nothing follows the last save and the diagnostics
// end with the process.
func TestRunLogsAFailedLastSave(t *testing.T) {
	store := &fakeStore{}
	rt := newRetained(t, store, nil)
	var logged strings.Builder
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&lockedWriter{w: &logged}, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { rt.Run(ctx); close(done) }()
	waitUntil(t, func() bool { v, err := rt.Tags().ReadGlobal("Count"); return err == nil && v.I > 0 })
	rt.Tags().Set("SP", 42.0)
	store.mu.Lock()
	store.fail = true
	store.mu.Unlock()
	cancel()
	<-done
	if out := logged.String(); !strings.Contains(out, "last retain save before stopping failed") || !strings.Contains(out, "store down") {
		t.Fatalf("no log line for the failed last save; log:\n%s", out)
	}
}

// lockedWriter serializes writes from the scan and test goroutines.
type lockedWriter struct {
	mu sync.Mutex
	w  *strings.Builder
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
