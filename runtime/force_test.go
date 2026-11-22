package runtime

import (
	"fmt"
	"math/rand"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/retain"
)

// fieldDriver keeps the field's inputs and the outputs it was handed apart
// (io.Memory loops one back into the other), and counts output pushes.
type fieldDriver struct {
	mu     sync.Mutex
	in     nio.Values
	out    nio.Values
	writes int
}

func (d *fieldDriver) ReadInputs() (nio.Values, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make(nio.Values, len(d.in))
	for k, v := range d.in {
		out[k] = v
	}
	return out, nil
}

func (d *fieldDriver) WriteOutputs(v nio.Values) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.writes++
	if d.out == nil {
		d.out = nio.Values{}
	}
	for k, val := range v {
		d.out[k] = val
	}
	return nil
}

func (d *fieldDriver) set(name string, v any) {
	d.mu.Lock()
	d.in[name] = v
	d.mu.Unlock()
}

func (d *fieldDriver) output(name string) any {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.out[name]
}

// A start/stop seal-in: the program reads a field input and drives a field
// output from it — the smallest controller a force has anything to do on.
const sealInProgram = `PROGRAM Seal
VAR_EXTERNAL StartPB : BOOL; StopPB : BOOL; Motor : BOOL; Level : REAL; Alarm : BOOL; END_VAR
Motor := (StartPB OR Motor) AND NOT StopPB;
Alarm := Level > 80.0;
END_PROGRAM`

func newForceRig(t *testing.T, coord Coordinator) (*Runtime, *fieldDriver) {
	t.Helper()
	d := &fieldDriver{in: nio.Values{"StartPB": false, "StopPB": false, "Level": 10.0}}
	rt, err := New(Options{
		Program: sealInProgram,
		Driver:  d,
		Tags: []TagDef{
			Input("StartPB"), Input("StopPB"), Input("Level"),
			Output("Motor", Init(false)), Output("Alarm", Init(false)),
		},
		Coordinator: coord,
	})
	if err != nil {
		t.Fatal(err)
	}
	return rt, d
}

// A forced input overrides the driver's reading every scan: the program
// sees the forced value, however often the field re-delivers its own.
func TestForceInputOverridesDriver(t *testing.T) {
	rt, d := newForceRig(t, nil)
	rt.Scan()
	if err := rt.Tags().Force("StartPB", true); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		rt.Scan() // the driver keeps delivering StartPB = false
	}
	if !rt.Tags().Bool("StartPB") {
		t.Fatal("forced input was overwritten by the driver")
	}
	if !rt.Tags().Bool("Motor") || d.output("Motor") != true {
		t.Fatalf("logic did not react to the forced input: Motor tag %v, driver %v", rt.Tags().Bool("Motor"), d.output("Motor"))
	}
	// The table reports the field's own value as the actual one.
	fs := rt.Tags().Forces()
	if len(fs) != 1 || fs[0].Name != "StartPB" || fs[0].Value != true || fs[0].Actual != false {
		t.Fatalf("Forces() = %+v", fs)
	}
	// A numeric force on a REAL input is coerced to the tag's type.
	if err := rt.Tags().Force("Level", 95); err != nil {
		t.Fatal(err)
	}
	rt.Scan()
	if got := rt.Tags().Real("Level"); got != 95 || !rt.Tags().Bool("Alarm") {
		t.Fatalf("Level = %v, Alarm = %v under a force of 95", got, rt.Tags().Bool("Alarm"))
	}
}

// A forced output is re-applied after the program every scan, so the
// driver is handed the forced value whatever the logic computed. Alarm is
// Level > 80 and never reads itself back, so with the level at 95 the logic
// writes TRUE on every scan the force holds it FALSE: a force laid on once
// would be overwritten by the next scan. (A seal-in is the wrong rig for
// this: it reads its coil back, so a forced FALSE makes the logic compute
// FALSE too.)
func TestForceOutputReappliedAfterLogic(t *testing.T) {
	rt, d := newForceRig(t, nil)
	d.set("Level", 95.0)
	rt.Scan()
	if !rt.Tags().Bool("Alarm") || d.output("Alarm") != true {
		t.Fatal("setup: Level 95 should drive Alarm on")
	}
	if err := rt.Tags().Force("Alarm", false); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		rt.Scan() // the logic writes Alarm := TRUE every scan
		if rt.Tags().Bool("Alarm") || d.output("Alarm") != false {
			t.Fatalf("scan %d: forced output not held: tag %v, driver %v", i+1, rt.Tags().Bool("Alarm"), d.output("Alarm"))
		}
	}
	// The force table's actual is the logic's own latest write, TRUE — not
	// the forced FALSE.
	fs := rt.Tags().Forces()
	if len(fs) != 1 || fs[0].Name != "Alarm" || fs[0].Value != false || fs[0].Actual != true {
		t.Fatalf("Forces() = %+v, want Alarm forced false over the logic's true", fs)
	}
	// Removing the force puts the logic's value back at once, and the next
	// scan hands it to the driver.
	if !rt.Tags().Unforce("Alarm") {
		t.Fatal("Unforce reported no force on Alarm")
	}
	if !rt.Tags().Bool("Alarm") {
		t.Fatal("Alarm after unforce: want the logic's true straight away")
	}
	rt.Scan()
	if d.output("Alarm") != true {
		t.Fatalf("driver Alarm = %v after unforce; want the logic's true", d.output("Alarm"))
	}
}

// Change detection: applying a force, or changing its value, is ONE change;
// re-applying the same forced value every scan over a moving input is none.
func TestForceGenerations(t *testing.T) {
	rt, d := newForceRig(t, nil)
	rt.Scan()
	tags := rt.Tags()
	g0, _ := tags.TagGeneration("Level")
	if err := tags.Force("Level", 50.0); err != nil {
		t.Fatal(err)
	}
	g1, _ := tags.TagGeneration("Level")
	if g1 == g0 {
		t.Fatal("applying a force did not move the tag's generation")
	}
	writes := d.writes
	for i, lvl := range []float64{11, 12, 13, 14} {
		d.set("Level", lvl) // the field moves underneath the force
		rt.Scan()
		if g, _ := tags.TagGeneration("Level"); g != g1 {
			t.Fatalf("scan %d: re-applying the same forced value moved the generation %d → %d", i, g1, g)
		}
	}
	if d.writes != writes {
		t.Errorf("held force caused %d output pushes; want none", d.writes-writes)
	}
	if err := tags.Force("Level", 60.0); err != nil {
		t.Fatal(err)
	}
	g2, _ := tags.TagGeneration("Level")
	if g2 == g1 {
		t.Fatal("changing the forced value did not move the generation")
	}
	// Same value again: no change.
	if err := tags.Force("Level", 60.0); err != nil {
		t.Fatal(err)
	}
	if g, _ := tags.TagGeneration("Level"); g != g2 {
		t.Error("re-forcing the same value moved the generation")
	}
	// A forced output: one push for the force, none while it holds. Level
	// is forced to 60, so the logic writes Alarm := FALSE every scan
	// against the forced TRUE; the re-applied force is no change, and the
	// driver keeps the forced value.
	if err := tags.Force("Alarm", true); err != nil {
		t.Fatal(err)
	}
	before := d.writes
	for i := 0; i < 3; i++ {
		rt.Scan()
		if d.output("Alarm") != true {
			t.Fatalf("scan %d: driver Alarm = %v; want the forced true over the logic's false", i+1, d.output("Alarm"))
		}
	}
	if d.writes-before != 1 {
		t.Errorf("forced output pushed %d times over 3 scans; want exactly 1", d.writes-before)
	}
}

// Removing a force returns the tag to its actual value at once, and the
// next scans run normally; UnforceAll clears the table.
func TestUnforceRestoresActual(t *testing.T) {
	rt, d := newForceRig(t, nil)
	rt.Scan()
	tags := rt.Tags()
	_ = tags.Force("Level", 99.0)
	_ = tags.Force("StopPB", true)
	d.set("Level", 42.0)
	rt.Scan()
	if !tags.Unforce("Level") {
		t.Fatal("Unforce reported no force on Level")
	}
	if got := tags.Real("Level"); got != 42 {
		t.Fatalf("Level = %v after unforce; want the field's 42 straight away", got)
	}
	if tags.Unforce("Level") {
		t.Error("a second Unforce reported a force")
	}
	d.set("Level", 43.0)
	rt.Scan()
	if got := tags.Real("Level"); got != 43 {
		t.Fatalf("Level = %v; the driver should own it again", got)
	}
	if n := tags.UnforceAll(); n != 1 {
		t.Fatalf("UnforceAll removed %d; want 1 (StopPB)", n)
	}
	if tags.ForceCount() != 0 || tags.ForcedValues() != nil || tags.Bool("StopPB") {
		t.Fatalf("table not empty after UnforceAll: %v", tags.Forces())
	}
}

// Forces belong to the active controller: a takeover (and a step-down)
// drops them, so a standby that becomes leader never inherits — and a
// flapping leader never resurrects — a force table.
func TestTakeoverDropsForces(t *testing.T) {
	coord := &flag{lead: true}
	rt, d := newForceRig(t, coord)
	rt.Scan()
	_ = rt.Tags().Force("StartPB", true)
	rt.Scan()
	if rt.Tags().ForceCount() != 1 {
		t.Fatal("setup: force not applied")
	}
	coord.lead = false
	rt.Scan() // steps down
	if rt.Tags().ForceCount() != 0 {
		t.Fatal("stepping down kept the force table")
	}
	_ = rt.Tags().Force("StartPB", true) // e.g. a stale API call on the old leader
	coord.lead = true
	rt.Scan() // takeover
	if n := rt.Tags().ForceCount(); n != 0 {
		t.Fatalf("takeover kept %d forces", n)
	}
	d.set("StartPB", false)
	rt.Scan()
	if rt.Tags().Bool("StartPB") {
		t.Fatal("input still forced after takeover")
	}
}

// Member forces on a struct tag: the forced member holds while the rest of
// the struct follows its writers, and an operator member write lands in the
// ACTUAL value rather than copying the force into it.
func TestForceStructMember(t *testing.T) {
	sd := &ir.StructDef{
		Name:       "Pump",
		Fields:     []ir.StructField{{Name: "Run"}, {Name: "Speed"}},
		FieldIndex: map[string]int{"Run": 0, "Speed": 1},
	}
	mk := func(run bool, speed float64) ir.Value {
		return ir.Value{Kind: ir.TypeStruct, Struct: sd, Fld: []ir.Value{ir.BoolVal(run), ir.RealVal(speed)}}
	}
	tags := NewTags()
	tags.Set("P1", mk(false, 10))
	if err := tags.Force("P1.Speed", 55); err != nil {
		t.Fatal(err)
	}
	tags.Set("P1", mk(true, 20)) // a driver delivery
	if v, _ := tags.ReadPath("P1.Speed"); v != 55.0 {
		t.Fatalf("P1.Speed = %v; want forced 55", v)
	}
	if v, _ := tags.ReadPath("P1.Run"); v != true {
		t.Fatalf("P1.Run = %v; the unforced member should follow the write", v)
	}
	if err := tags.SetPath("P1.Run", false); err != nil {
		t.Fatal(err)
	}
	if got := tags.ForcedOverlap("P1"); got != "P1.Speed" {
		t.Errorf("ForcedOverlap(P1) = %q", got)
	}
	if got := tags.ForcedOverlap("P1.Run"); got != "" {
		t.Errorf("ForcedOverlap(P1.Run) = %q; the member is not forced", got)
	}
	tags.Unforce("P1.Speed")
	if v, _ := tags.ReadPath("P1.Speed"); v != 20.0 {
		t.Fatalf("P1.Speed = %v after unforce; want the delivered 20", v)
	}
	if v, _ := tags.ReadPath("P1.Run"); v != false {
		t.Fatalf("P1.Run = %v; the operator write was lost", v)
	}
}

func TestForceRefusals(t *testing.T) {
	tags := NewTags()
	tags.SetBool("B", false)
	for addr, v := range map[string]any{
		"Nope": true,  // unknown tag
		"B":    "yes", // wrong type
		"B.X":  true,  // member of a scalar
	} {
		if err := tags.Force(addr, v); err == nil {
			t.Errorf("Force(%s, %v) succeeded", addr, v)
		}
	}
	if tags.ForceCount() != 0 {
		t.Fatal("a refused force left an entry")
	}
}

const chartProgram = `PROGRAM Chart
VAR_EXTERNAL Go : BOOL; Done : BOOL; InFill : BOOL; END_VAR
SFC
  INITIAL_STEP Idle:
  END_STEP
  STEP Fill:
    N InFill;
  END_STEP
  STEP Drain:
  END_STEP
  TRANSITION Start FROM Idle TO Fill := Go;
  END_TRANSITION
  TRANSITION FROM Fill TO Drain := Done;
  END_TRANSITION
  TRANSITION Back FROM Drain TO Idle := TRUE;
  END_TRANSITION
END_SFC
END_PROGRAM`

func newChart(t *testing.T) *Runtime {
	t.Helper()
	rt, err := New(Options{
		Program: chartProgram,
		Tags:    []TagDef{Setpoint("Go", false), Setpoint("Done", false), State("InFill", false)},
	})
	if err != nil {
		t.Fatal(err)
	}
	return rt
}

func activeSteps(t *testing.T, rt *Runtime) string {
	t.Helper()
	info, err := rt.Program().SFC()
	if err != nil {
		t.Fatal(err)
	}
	var on []string
	for _, s := range info.Steps {
		if s.Active {
			on = append(on, s.Name)
		}
	}
	return strings.Join(on, ",")
}

// Set Active Step jumps the chart once (Codesys "set step"): the target's
// actions run, and the chart evolves normally from there.
func TestSFCSetStep(t *testing.T) {
	rt := newChart(t)
	rt.Scan()
	if got := activeSteps(t, rt); got != "Idle" {
		t.Fatalf("active = %s; want Idle", got)
	}
	name, err := rt.Program().SetStep("fill") // case-insensitive
	if err != nil || name != "Fill" {
		t.Fatalf("SetStep = %q, %v", name, err)
	}
	rt.Scan()
	if got := activeSteps(t, rt); got != "Fill" {
		t.Fatalf("active = %s after the jump; want Fill", got)
	}
	if !rt.Tags().Bool("InFill") {
		t.Fatal("the step's N action did not run after the jump")
	}
	// Not held: the chart moves on by itself.
	rt.Tags().SetBool("Done", true)
	rt.Scan()
	if got := activeSteps(t, rt); got != "Drain" {
		t.Fatalf("active = %s; the chart should evolve normally after a jump", got)
	}
	rt.Scan()
	if rt.Tags().Bool("InFill") {
		t.Fatal("leaving Fill should reset its N action")
	}
	if _, err := rt.Program().SetStep("Nowhere"); err == nil {
		t.Fatal("SetStep accepted an unknown step")
	}
}

// Fire Transition takes one transition once, condition or not — and only
// when the chart enables it.
func TestSFCFireTransition(t *testing.T) {
	rt := newChart(t)
	rt.Scan()
	info, _ := rt.Program().SFC()
	if len(info.Transitions) != 3 || info.Transitions[1].ID != "t13" || !info.Transitions[0].Enabled {
		t.Fatalf("transitions = %+v", info.Transitions)
	}
	// Fill → Drain is not enabled from Idle.
	if _, err := rt.Program().FireTransition("t13"); err == nil || !strings.Contains(err.Error(), "not enabled") {
		t.Fatalf("firing a disabled transition: %v", err)
	}
	if _, err := rt.Program().FireTransition("start"); err != nil { // Go is FALSE
		t.Fatal(err)
	}
	rt.Scan()
	if got := activeSteps(t, rt); got != "Fill" {
		t.Fatalf("active = %s after firing Start; want Fill", got)
	}
	rt.Scan()
	if got := activeSteps(t, rt); got != "Fill" {
		t.Fatalf("active = %s; a fired transition must not stay forced", got)
	}
	// The unnamed transition by its t<line> id.
	if id, err := rt.Program().FireTransition("t13"); err != nil || id != "t13" {
		t.Fatalf("FireTransition(t15) = %q, %v", id, err)
	}
	if got := activeSteps(t, rt); got != "Drain" {
		t.Fatalf("active = %s; want Drain", got)
	}
}

func TestSFCProgramResolution(t *testing.T) {
	rt := newChart(t)
	if p, err := rt.SFCProgram("", "Fill", ""); err != nil || p != rt.Program() {
		t.Fatalf("SFCProgram by step: %v", err)
	}
	if _, err := rt.SFCProgram("", "Nowhere", ""); err == nil {
		t.Fatal("resolved a step no chart has")
	}
	st, _ := newForceRig(t, nil)
	if _, err := st.SFCProgram("", "Fill", ""); err == nil || !strings.Contains(err.Error(), "no SFC") {
		t.Fatalf("an ST-only controller: %v", err)
	}
	if _, err := st.Program().SetStep("Fill"); err == nil {
		t.Fatal("SetStep on an ST program succeeded")
	}
	if n := len(rt.SFCCharts()); n != 1 {
		t.Fatalf("SFCCharts = %d", n)
	}
}

// A forced setpoint is retained at its ACTUAL value: the force is not
// retained, so neither may its value sneak into the store under the tag.
func TestRetainSavesActualNotForced(t *testing.T) {
	store := &fakeStore{}
	rt := newRetained(t, store, nil)
	rt.Scan()
	rt.Tags().SetReal("SP", 70)
	if err := rt.Tags().Force("SP", 99.0); err != nil {
		t.Fatal(err)
	}
	st := rt.retainState()
	if got := st.Tags["SP"]; got != 70.0 {
		t.Fatalf("retained SP = %v; want the actual 70, not the forced 99", got)
	}
}

// A single controller with a retain store takes over on its first scan, but
// it has no standby and no lease: a force made through the API before that
// scan (the API answers first) holds, rather than being accepted and then
// dropped. Brief 002's build check caught it: a level forced right after
// start never reached the logic.
func TestForceBeforeFirstScanHolds(t *testing.T) {
	d := &fieldDriver{in: nio.Values{"StartPB": false, "StopPB": false, "Level": 10.0}}
	rt, err := New(Options{
		Program: sealInProgram,
		Driver:  d,
		Tags: []TagDef{
			Input("StartPB", Init(false)), Input("StopPB", Init(false)), Input("Level", Init(10.0)),
			Output("Motor", Init(false)), Output("Alarm", Init(false)),
		},
		Retain: retain.New("", filepath.Join(t.TempDir(), "retain.json")),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.Tags().Force("StartPB", true); err != nil {
		t.Fatal(err)
	}
	rt.Scan() // the first scan: takeover
	if n := rt.Tags().ForceCount(); n != 1 {
		t.Fatalf("the first scan dropped the force table (%d left)", n)
	}
	if !rt.Tags().Bool("Motor") {
		t.Fatal("the forced StartPB never reached the logic")
	}
}

// A caller that must remove only the force it put there (an I/O checkout,
// #719) takes its generation from ForceIfFree and removes it with UnforceIf:
// a force set or changed since has another generation and stays; an address
// already forced (itself, a member, the tag around it) is refused; plain Force
// and Unforce are as they were.
func TestForceGenerationsAreOwnership(t *testing.T) {
	tags := NewTags()
	tags.Set("Out", 0.0)
	gen, name, err := tags.ForceIfFree("Out", 5.0)
	if err != nil || gen == 0 || name != "Out" {
		t.Fatalf("ForceIfFree: %d %q %v", gen, name, err)
	}
	if g, ok := tags.ForceGen("out"); !ok || g != gen {
		t.Errorf("ForceGen = %d %v, want %d", g, ok, gen)
	}
	if _, _, err := tags.ForceIfFree("Out", 6.0); err == nil || !strings.Contains(err.Error(), "already forced") {
		t.Errorf("a second ForceIfFree on a forced address: %v", err)
	}
	// someone else changes the force: another generation, so UnforceIf leaves it
	if err := tags.Force("Out", 9.0); err != nil {
		t.Fatal(err)
	}
	if tags.UnforceIf("Out", gen) {
		t.Fatal("UnforceIf removed a force that was set again since")
	}
	if g, _ := tags.ForceGen("Out"); g == gen {
		t.Error("a changed force kept its generation")
	}
	g2, _ := tags.ForceGen("Out")
	if !tags.UnforceIf("Out", g2) {
		t.Error("UnforceIf did not remove the force it was given the generation of")
	}
	if tags.UnforceIf("Out", g2) || len(tags.Forces()) != 0 {
		t.Errorf("a second UnforceIf, or leftovers: %v", tags.Forces())
	}
	// plain Force and Unforce are unchanged
	tags.Force("Out", 1.0)
	if !tags.Unforce("Out") {
		t.Error("Unforce")
	}
}

// ForceIfFree sees a force on a member when the whole tag is asked for and on
// the tag when a member is.
func TestForceIfFreeSeesOverlaps(t *testing.T) {
	sd := &ir.StructDef{Name: "Pump", Fields: []ir.StructField{{Name: "Run"}, {Name: "Speed"}}, FieldIndex: map[string]int{"Run": 0, "Speed": 1}}
	tags := NewTags()
	tags.Set("P1", ir.Value{Kind: ir.TypeStruct, Struct: sd, Fld: []ir.Value{ir.BoolVal(false), ir.RealVal(1)}})
	if err := tags.Force("P1.Run", true); err != nil {
		t.Fatal(err)
	}
	if _, _, err := tags.ForceIfFree("P1", ir.Value{}); err == nil || !strings.Contains(err.Error(), "already forced") {
		t.Errorf("the whole tag over a forced member: %v", err)
	}
	if _, _, err := tags.ForceIfFree("P1.Speed", 5.0); err != nil {
		t.Errorf("another member is free: %v", err)
	}
}

// The table keeps a force under its own spelling of the address (member names
// as the struct declares them, one index per bracket); ForceIfFree returns that
// name, so ForceGen and UnforceIf find the force however the caller typed it,
// and the overlap check sees a force set under another spelling.
func TestForceIfFreeNamesTheForceTheWayTheTableDoes(t *testing.T) {
	sd := &ir.StructDef{Name: "Pump", Fields: []ir.StructField{{Name: "Run"}, {Name: "Speed"}}, FieldIndex: map[string]int{"Run": 0, "Speed": 1}}
	tags := NewTags()
	tags.Set("P1", ir.Value{Kind: ir.TypeStruct, Struct: sd, Fld: []ir.Value{ir.BoolVal(false), ir.RealVal(1)}})
	arr := ir.Value{Kind: ir.TypeArray, Arr: []ir.Value{ir.RealVal(0), ir.RealVal(0), ir.RealVal(0)}}
	tags.Set("A", arr)
	for _, c := range []struct{ typed, table string }{{"p1.speed", "P1.Speed"}, {"A[ 1]", "A[1]"}, {"A[01]", "A[1]"}, {"A[+1]", "A[1]"}} {
		gen, name, err := tags.ForceIfFree(c.typed, 5.0)
		if err != nil {
			t.Fatalf("%q: %v", c.typed, err)
		}
		if name != c.table {
			t.Errorf("%q is kept as %q, want %q", c.typed, name, c.table)
		}
		if g, ok := tags.ForceGen(name); !ok || g != gen {
			t.Errorf("%q: ForceGen(%q) = %d %v, want %d", c.typed, name, g, ok, gen)
		}
		// another spelling of the same address is forced already
		if _, _, err := tags.ForceIfFree(c.typed, 6.0); err == nil || !strings.Contains(err.Error(), "already forced") {
			t.Errorf("%q forced twice: %v", c.typed, err)
		}
		if !tags.UnforceIf(name, gen) {
			t.Errorf("%q: UnforceIf(%q) did not remove it", c.typed, name)
		}
	}
	if len(tags.Forces()) != 0 {
		t.Errorf("forces left: %v", tags.Forces())
	}
}

// An operator's force typed with an odd index ("A[01]") is seen by a force
// typed the plain way, and the other way round: one spelling of an element.
func TestForceIndexSpellingsAreOneAddress(t *testing.T) {
	tags := NewTags()
	tags.Set("A", ir.Value{Kind: ir.TypeArray, Arr: []ir.Value{ir.RealVal(0), ir.RealVal(0), ir.RealVal(0)}})
	if err := tags.Force("A[01]", 1.0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := tags.ForceIfFree("A[1]", 7.0); err == nil || !strings.Contains(err.Error(), "already forced") {
		t.Errorf("a plain A[1] over an operator's A[01]: %v", err)
	}
	if v, _ := tags.ReadPath("A[1]"); v != 1.0 {
		t.Errorf("A[1] = %v, want the operator's 1", v)
	}
}

// The force table keys every entry by one canonical address, whichever way it
// was typed. This generates spellings of the same element (spaces, a sign,
// leading zeros, "[i,j]" or "[i][j]", the case of the tag and of members) and
// checks, for every pair of them: one entry, never two or one with two names;
// the generation found through any spelling is the one the force was given;
// another spelling cannot force it again (ForceIfFree is refused) and removes it
// (Unforce, UnforceIf); and elements that differ stay different.
func TestForceSpellingsOfOneElementAreOneEntry(t *testing.T) {
	for seed := int64(1); seed <= 25; seed++ {
		forceSpellings(t, seed)
	}
}

func forceSpellings(t *testing.T, seed int64) {
	sd := &ir.StructDef{Name: "Pump", Fields: []ir.StructField{{Name: "Run"}, {Name: "Speed"}}, FieldIndex: map[string]int{"Run": 0, "Speed": 1}}
	row := func(n int) ir.Value {
		a := make([]ir.Value, n)
		for i := range a {
			a[i] = ir.RealVal(0)
		}
		return ir.Value{Kind: ir.TypeArray, Arr: a}
	}
	grid := ir.Value{Kind: ir.TypeArray, Arr: []ir.Value{row(4), row(4), row(4)}}
	fresh := func() *Tags {
		tags := NewTags()
		tags.Set("A", row(5))
		tags.Set("Gridtag", grid)
		tags.Set("P1", ir.Value{Kind: ir.TypeStruct, Struct: sd, Fld: []ir.Value{ir.BoolVal(false), ir.RealVal(1)}})
		return tags
	}
	rng := rand.New(rand.NewSource(seed))
	idx := func(n int) string {
		forms := []string{"%d", "+%d", "0%d", "00%d", " %d", "%d ", " %d ", "+0%d"}
		return fmt.Sprintf(forms[rng.Intn(len(forms))], n)
	}
	caseOf := func(s string) string {
		switch rng.Intn(3) {
		case 0:
			return strings.ToLower(s)
		case 1:
			return strings.ToUpper(s)
		}
		return s
	}
	// a generator of spellings of one element, by shape
	spell := map[string]func() string{
		"A[2]": func() string { return caseOf("A") + "[" + idx(2) + "]" },
		"Gridtag[1][2]": func() string {
			if rng.Intn(2) == 0 {
				return caseOf("Gridtag") + "[" + idx(1) + "," + idx(2) + "]"
			}
			return caseOf("Gridtag") + "[" + idx(1) + "][" + idx(2) + "]"
		},
		"P1.Speed": func() string { return caseOf("P1") + "." + caseOf("Speed") },
	}
	for canon, gen := range spell {
		spellings := map[string]bool{}
		for i := 0; i < 400 && len(spellings) < 25; i++ { // some shapes have few spellings
			spellings[gen()] = true
		}
		for x := range spellings {
			tags := fresh()
			g1, name, err := tags.ForceIfFree(x, 5.0)
			if err != nil {
				continue // a spelling the table refuses is no spelling of an entry
			}
			if name != canon {
				t.Errorf("%q is kept as %q, want %q", x, name, canon)
			}
			if n := len(tags.Forces()); n != 1 {
				t.Fatalf("%q: %d entries after one force", x, n)
			}
			for y := range spellings {
				if g, ok := tags.ForceGen(y); ok != (tags.ForceKey(y) == canon) || (ok && g != g1) {
					// y resolved to the element: it must find the force by the same generation
					if tags.ForceKey(y) == canon {
						t.Errorf("force typed %q, looked up as %q: gen %d %v, want %d", x, y, g, ok, g1)
					}
				}
				if tags.ForceKey(y) != canon {
					continue // refused as an address: nothing to compare
				}
				if got := tags.ForcedOverlap(y); got != canon {
					t.Fatalf("force typed %q: ForcedOverlap(%q) = %q, want %q", x, y, got, canon)
				}
				if _, _, err := tags.ForceIfFree(y, 6.0); err == nil || !strings.Contains(err.Error(), "already forced") {
					t.Fatalf("force typed %q then ForceIfFree(%q): %v, want already forced", x, y, err)
				}
				if n := len(tags.Forces()); n != 1 {
					t.Fatalf("force typed %q then %q: %d entries", x, y, n)
				}
				if tags.UnforceIf(y, g1+1000) {
					t.Fatalf("UnforceIf(%q) with the wrong generation removed the force typed %q", y, x)
				}
			}
			// any one spelling removes it; plain Unforce too
			var ys []string
			for y := range spellings {
				if tags.ForceKey(y) == canon {
					ys = append(ys, y)
				}
			}
			y := ys[rng.Intn(len(ys))]
			if rng.Intn(2) == 0 {
				if !tags.UnforceIf(y, g1) {
					t.Fatalf("UnforceIf(%q, %d) did not remove the force typed %q", y, g1, x)
				}
			} else if !tags.Unforce(y) {
				t.Fatalf("Unforce(%q) did not remove the force typed %q", y, x)
			}
			if n := len(tags.Forces()); n != 0 {
				t.Fatalf("%d entries left after removing the force typed %q via %q", n, x, y)
			}
		}
	}
	// elements that differ stay different, however typed
	for _, other := range []string{"A[2]", "A[ 02]", "A[+3]", "A[4]", "A[0]"} {
		tags := fresh()
		if _, _, err := tags.ForceIfFree("A[1]", 1.0); err != nil {
			t.Fatal(err)
		}
		if _, _, err := tags.ForceIfFree(other, 1.0); err != nil {
			t.Errorf("%q wrongly taken as the forced A[1]: %v", other, err)
		}
	}
	tags2 := fresh()
	tags2.ForceIfFree("Gridtag[1,2]", 1.0)
	if _, _, err := tags2.ForceIfFree("Gridtag[2,1]", 1.0); err != nil {
		t.Errorf("Gridtag[2,1] taken as Gridtag[1,2]: %v", err)
	}
}
