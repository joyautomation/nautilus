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
	// Disable ignores min-interval.
	r := RBE{Disable: true, MinInterval: time.Hour}
	if !r.shouldPublish(st, ir.BoolVal(false), time.Unix(1, 0)) {
		t.Error("Disable must always publish")
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
