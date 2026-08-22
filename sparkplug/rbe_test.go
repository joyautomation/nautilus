package sparkplug

import (
	"testing"
	"time"

	"github.com/joyautomation/nautilus/lang/ir"
)

func TestRBEDeadbandAndHeartbeat(t *testing.T) {
	r := RBE{Deadband: 1.0, MinInterval: 100 * time.Millisecond, MaxInterval: time.Second}
	st := &rbeState{}
	t0 := time.Unix(0, 0)

	// First value always publishes.
	if !r.shouldPublish(st, ir.RealVal(10), t0) {
		t.Fatal("first value must publish")
	}
	st.record(ir.RealVal(10), t0)

	// Within min-interval: suppressed even on a big change.
	if r.shouldPublish(st, ir.RealVal(20), t0.Add(50*time.Millisecond)) {
		t.Error("should be rate-limited within MinInterval")
	}
	// Past min-interval, change under deadband: suppressed.
	if r.shouldPublish(st, ir.RealVal(10.5), t0.Add(200*time.Millisecond)) {
		t.Error("sub-deadband change should not publish")
	}
	// Past min-interval, change over deadband: publishes.
	if !r.shouldPublish(st, ir.RealVal(12), t0.Add(200*time.Millisecond)) {
		t.Error("over-deadband change should publish")
	}
	// Unchanged but past max-interval: heartbeat publishes.
	if !r.shouldPublish(st, ir.RealVal(10), t0.Add(1100*time.Millisecond)) {
		t.Error("heartbeat should force publish")
	}
}

func TestRBEDisableAndBoolChange(t *testing.T) {
	st := &rbeState{}
	st.record(ir.BoolVal(false), time.Unix(0, 0))
	// Non-numeric change publishes.
	if !(RBE{}).shouldPublish(st, ir.BoolVal(true), time.Unix(1, 0)) {
		t.Error("bool change should publish")
	}
	// Disable (every-change) ignores min-interval on a CHANGE…
	r := RBE{Disable: true, MinInterval: time.Hour}
	if !r.shouldPublish(st, ir.BoolVal(true), time.Unix(1, 0)) {
		t.Error("every-change must publish a transition despite min-interval")
	}
	// …but an unchanged sample stays quiet (RBE is still RBE).
	if r.shouldPublish(st, ir.BoolVal(false), time.Unix(1, 0)) {
		t.Error("every-change must not republish an unchanged value")
	}
}

// TestRBEDeadbandOnStructMembers is a regression test: numeric()/asFloat
// only understand scalars, so a Template (UDT) metric used to fall straight
// to valuesEqual — an exact deep-compare with no deadband — and ANY member
// change, even sub-deadband dither on one REAL field, republished the whole
// template. The class deadband must apply per numeric member, while a
// non-numeric member (BOOL, STRING) still forces a publish on any change.
func TestRBEDeadbandOnStructMembers(t *testing.T) {
	sd := &ir.StructDef{
		Name: "Motor",
		Fields: []ir.StructField{
			{Name: "Speed", Type: ir.RealT},
			{Name: "Running", Type: ir.BoolT},
		},
	}
	mk := func(speed float64, running bool) ir.Value {
		return ir.Value{Kind: ir.TypeStruct, Struct: sd, Fld: []ir.Value{
			ir.RealVal(speed), ir.BoolVal(running),
		}}
	}

	r := RBE{Deadband: 1.0}
	st := &rbeState{}
	t0 := time.Unix(0, 0)

	if !r.shouldPublish(st, mk(10.0, false), t0) {
		t.Fatal("first value must publish")
	}
	st.record(mk(10.0, false), t0)

	// Sub-deadband REAL member dither alone must not republish the template.
	if r.shouldPublish(st, mk(10.4, false), t0) {
		t.Error("sub-deadband member change must not republish the template")
	}

	// Over-deadband REAL member change publishes, and only then updates
	// "last published".
	if !r.shouldPublish(st, mk(11.2, false), t0) {
		t.Error("over-deadband member change must publish")
	}
	st.record(mk(11.2, false), t0)

	// A non-numeric member changing publishes even though the REAL member
	// stays within deadband of the last PUBLISHED value.
	if !r.shouldPublish(st, mk(11.3, true), t0) {
		t.Error("non-numeric member change must publish even with the numeric member under deadband")
	}

	// Nothing at all moved: quiet.
	if r.shouldPublish(st, mk(11.2, false), t0) {
		t.Error("unchanged struct must not publish")
	}
}

// TestRBEDeadbandOnNestedStruct checks the per-member rule recurses into a
// struct field that is itself a struct (nested templates, e.g. LevelControl
// inside Motor1Speed) rather than only reaching one level deep.
func TestRBEDeadbandOnNestedStruct(t *testing.T) {
	inner := &ir.StructDef{Name: "PID", Fields: []ir.StructField{{Name: "Out", Type: ir.RealT}}}
	outer := &ir.StructDef{Name: "LevelControl", Fields: []ir.StructField{
		{Name: "Stage", Type: ir.IntT},
		{Name: "Loop", Type: &ir.Type{Kind: ir.TypeStruct, Struct: inner}},
	}}
	mk := func(stage int64, out float64) ir.Value {
		return ir.Value{Kind: ir.TypeStruct, Struct: outer, Fld: []ir.Value{
			ir.IntVal(stage),
			{Kind: ir.TypeStruct, Struct: inner, Fld: []ir.Value{ir.RealVal(out)}},
		}}
	}

	r := RBE{Deadband: 0.5}
	st := &rbeState{}
	t0 := time.Unix(0, 0)

	if !r.shouldPublish(st, mk(1, 10.0), t0) {
		t.Fatal("first value must publish")
	}
	st.record(mk(1, 10.0), t0)

	if r.shouldPublish(st, mk(1, 10.2), t0) {
		t.Error("sub-deadband change in a nested struct member must not publish")
	}
	if !r.shouldPublish(st, mk(1, 10.6), t0) {
		t.Error("over-deadband change in a nested struct member must publish")
	}
}

// TestRBEMinIntervalStillAppliesToStructs locks in the behaviour the fix
// must not break: min-interval already rate-limits Template metrics (it
// never depended on numeric()), and this must stay true after routing
// struct comparisons through the new per-member deadband path.
func TestRBEMinIntervalStillAppliesToStructs(t *testing.T) {
	sd := &ir.StructDef{Name: "Motor", Fields: []ir.StructField{{Name: "Speed", Type: ir.RealT}}}
	mk := func(speed float64) ir.Value {
		return ir.Value{Kind: ir.TypeStruct, Struct: sd, Fld: []ir.Value{ir.RealVal(speed)}}
	}

	r := RBE{MinInterval: 100 * time.Millisecond}
	st := &rbeState{}
	t0 := time.Unix(0, 0)

	if !r.shouldPublish(st, mk(10.0), t0) {
		t.Fatal("first value must publish")
	}
	st.record(mk(10.0), t0)

	if r.shouldPublish(st, mk(50.0), t0.Add(50*time.Millisecond)) {
		t.Error("a struct metric must still be rate-limited within MinInterval")
	}
	if !r.shouldPublish(st, mk(50.0), t0.Add(150*time.Millisecond)) {
		t.Error("a struct metric must publish once MinInterval has passed")
	}
}

func TestClassResolution(t *testing.T) {
	n := &Node{classRBE: map[string]RBE{DefaultClass: {}}}
	WithPublishClass("fast", RBE{MaxInterval: time.Second})(n)
	WithMetricClass("fast", "Motor*")(n)
	WithMetricClass(NoPublish, "*_scratch")(n)

	if n.classOf("MotorSpeed") != "fast" {
		t.Error("MotorSpeed should be fast")
	}
	if _, ok := n.rbeFor("tmp_scratch"); ok {
		t.Error("*_scratch should not publish")
	}
	if n.classOf("Other") != DefaultClass {
		t.Error("unmatched -> default")
	}
}
