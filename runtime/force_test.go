package runtime

import (
	"strings"
	"sync"
	"testing"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
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
// driver is handed the forced value whatever the logic computed.
func TestForceOutputReappliedAfterLogic(t *testing.T) {
	rt, d := newForceRig(t, nil)
	d.set("StartPB", true)
	rt.Scan()
	d.set("StartPB", false)
	rt.Scan()
	if d.output("Motor") != true {
		t.Fatal("setup: the seal-in should hold Motor on")
	}
	if err := rt.Tags().Force("Motor", false); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		rt.Scan() // the logic writes Motor := TRUE every scan (sealed)
	}
	if rt.Tags().Bool("Motor") || d.output("Motor") != false {
		t.Fatalf("forced output not held: tag %v, driver %v", rt.Tags().Bool("Motor"), d.output("Motor"))
	}
	// The logic's own result is what the force table reports as actual.
	// (The seal-in reads Motor back, and the forced FALSE breaks it — the
	// same thing a forced output does to a seal-in on any PLC.)
	if fs := rt.Tags().Forces(); len(fs) != 1 || fs[0].Actual == nil {
		t.Fatalf("Forces() = %+v, want the logic's value as actual", fs)
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
	// A forced output: one push for the force, none while it holds.
	if err := tags.Force("Motor", true); err != nil {
		t.Fatal(err)
	}
	before := d.writes
	rt.Scan()
	rt.Scan()
	rt.Scan()
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
