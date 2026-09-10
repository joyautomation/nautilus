// command_test.go covers the NCMD/DCMD write path — in particular the
// Sparkplug-legal PARTIAL template update: a Template command metric carrying
// only the members that changed merges into the UDT tag rather than replacing
// it. Commanding one member of a site's motor must not clobber the members
// the edge is holding live.

package sparkplug

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/runtime"
	"github.com/joyautomation/nautilus/sparkplug/spb"
)

// cmdTypesST declares the two-level-nested UDT the member-command tests use:
// Motor has a struct member (Drive : Drv), so a nested partial template
// (Motor1.Drive.Torque) is exercised alongside a flat one (Motor1.START).
const cmdTypesST = `
TYPE
	Drv : STRUCT
		Torque : REAL;
		Fault : BOOL;
	END_STRUCT;
	Motor : STRUCT
		Speed : REAL;
		START : BOOL;
		Drive : Drv;
	END_STRUCT;
END_TYPE
`

const cmdProgramST = `
PROGRAM Main
VAR_EXTERNAL
	SpeedSP : REAL;
	Motor1 : Motor;
END_VAR
END_PROGRAM
`

// newCommandNode builds a Node over a real runtime whose Motor1 UDT tag is
// seeded with distinguishable values at every level, plus a logger writing
// into buf so the log-once diagnostics are assertable.
func newCommandNode(t *testing.T, buf *bytes.Buffer) *Node {
	t.Helper()
	rt, err := runtime.New(runtime.Options{
		Program:   cmdProgramST,
		Libraries: []string{cmdTypesST},
		Driver:    nio.NewMemory(),
		Scan:      50 * time.Millisecond,
		Tags: []runtime.TagDef{
			runtime.State("SpeedSP", 12.5),
			runtime.Typed("Motor1", runtime.RoleState, "Motor"),
		},
	})
	if err != nil {
		t.Fatalf("build runtime: %v", err)
	}

	motorT, ok := rt.Types()["Motor"]
	if !ok || motorT.Struct == nil {
		t.Fatal("TYPE Motor did not compile into the runtime's type table")
	}
	m := ir.Zero(motorT)
	setFld(t, &m, "Speed", ir.RealVal(1450))
	setFld(t, &m, "START", ir.BoolVal(false))
	drvIdx := m.Struct.FieldIndex["Drive"]
	drv := m.Fld[drvIdx]
	setFld(t, &drv, "Torque", ir.RealVal(88.5))
	setFld(t, &drv, "Fault", ir.BoolVal(true))
	m.Fld[drvIdx] = drv
	rt.Tags().Set("Motor1", m)

	log := slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	n, err := New(rt, Config{GroupID: "G", EdgeNode: "E", Log: log})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return n
}

func setFld(t *testing.T, v *ir.Value, name string, val ir.Value) {
	t.Helper()
	idx, ok := v.Struct.FieldIndex[name]
	if !ok {
		t.Fatalf("struct %s has no field %s", v.Struct.Name, name)
	}
	v.Fld[idx] = val
}

// motor1 reads the Motor1 tag back as a struct value.
func motor1(t *testing.T, n *Node) ir.Value {
	t.Helper()
	v, err := n.rt.Tags().ReadGlobal("Motor1")
	if err != nil {
		t.Fatalf("read Motor1: %v", err)
	}
	if v.Kind != ir.TypeStruct {
		t.Fatalf("Motor1 is %v, want a struct", v.Kind)
	}
	return v
}

func fld(t *testing.T, v ir.Value, name string) ir.Value {
	t.Helper()
	idx, ok := v.Struct.FieldIndex[name]
	if !ok {
		t.Fatalf("struct %s has no field %s", v.Struct.Name, name)
	}
	return v.Fld[idx]
}

// memberCmd wraps one leaf metric in a Template named after the parent metric
// — the exact shape the host driver's member bindings put on the wire.
func memberCmd(metric, ref string, leaf Metric) Metric {
	return Metric{
		Name:     metric,
		Datatype: spb.DataType_Template,
		Value:    &Template{TemplateRef: ref, Metrics: []Metric{leaf}},
	}
}

// TestCommandPartialTemplateKeepsOtherMembers — an NCMD carrying only
// Motor1.START writes that member and leaves Speed, Drive.Torque and
// Drive.Fault exactly as the edge had them.
func TestCommandPartialTemplateKeepsOtherMembers(t *testing.T) {
	var buf bytes.Buffer
	n := newCommandNode(t, &buf)

	n.applyCommand(Payload{Metrics: []Metric{
		memberCmd("Motor1", "Motor", Metric{Name: "START", Datatype: spb.DataType_Boolean, Value: true}),
	}})

	got := motor1(t, n)
	if !fld(t, got, "START").B {
		t.Error("START = false, want true (the commanded member)")
	}
	if v := fld(t, got, "Speed").F; v != 1450 {
		t.Errorf("Speed = %v, want 1450 (untouched)", v)
	}
	drive := fld(t, got, "Drive")
	if v := fld(t, drive, "Torque").F; v != 88.5 {
		t.Errorf("Drive.Torque = %v, want 88.5 (untouched)", v)
	}
	if !fld(t, drive, "Fault").B {
		t.Error("Drive.Fault = false, want true (untouched)")
	}
}

// TestCommandNestedPartialTemplate — a nested member path (Drive.Torque)
// arrives as a nested partial template and merges at depth, leaving its
// sibling (Drive.Fault) and the outer members alone.
func TestCommandNestedPartialTemplate(t *testing.T) {
	var buf bytes.Buffer
	n := newCommandNode(t, &buf)

	n.applyCommand(Payload{Metrics: []Metric{
		memberCmd("Motor1", "Motor", memberCmd("Drive", "Drv",
			Metric{Name: "Torque", Datatype: spb.DataType_Double, Value: 12.25})),
	}})

	got := motor1(t, n)
	drive := fld(t, got, "Drive")
	if v := fld(t, drive, "Torque").F; v != 12.25 {
		t.Errorf("Drive.Torque = %v, want 12.25", v)
	}
	if !fld(t, drive, "Fault").B {
		t.Error("Drive.Fault = false, want true (untouched)")
	}
	if v := fld(t, got, "Speed").F; v != 1450 {
		t.Errorf("Speed = %v, want 1450 (untouched)", v)
	}
}

// TestCommandTemplateUnknownMemberIgnored — a member the type does not have
// is logged once and dropped; the members that DO resolve still apply.
func TestCommandTemplateUnknownMemberIgnored(t *testing.T) {
	var buf bytes.Buffer
	n := newCommandNode(t, &buf)

	cmd := Metric{Name: "Motor1", Datatype: spb.DataType_Template, Value: &Template{
		TemplateRef: "Motor",
		Metrics: []Metric{
			{Name: "START", Datatype: spb.DataType_Boolean, Value: true},
			{Name: "HSP", Datatype: spb.DataType_Double, Value: 99.0},
		},
	}}
	n.applyCommand(Payload{Metrics: []Metric{cmd}})
	n.applyCommand(Payload{Metrics: []Metric{cmd}}) // twice: the warning is log-ONCE

	got := motor1(t, n)
	if !fld(t, got, "START").B {
		t.Error("START = false, want true — the known member must still apply")
	}
	if v := fld(t, got, "Speed").F; v != 1450 {
		t.Errorf("Speed = %v, want 1450 (untouched)", v)
	}
	if got.Struct.FieldIndex["HSP"] != 0 || len(got.Fld) != len(got.Struct.Fields) {
		// FieldIndex has no HSP entry, so the lookup yields the zero int; the
		// real assertion is that no field was appended.
		t.Errorf("unknown member changed the struct shape: %d fields", len(got.Fld))
	}
	if n := strings.Count(buf.String(), `member=HSP`); n != 1 {
		t.Errorf("unknown-member warning logged %d times, want exactly 1", n)
	}
}

// TestCommandTemplateForScalarTagIgnored — a Template aimed at a scalar tag
// is a manifest/type mismatch: log once, leave the tag alone.
func TestCommandTemplateForScalarTagIgnored(t *testing.T) {
	var buf bytes.Buffer
	n := newCommandNode(t, &buf)

	n.applyCommand(Payload{Metrics: []Metric{
		memberCmd("SpeedSP", "Motor", Metric{Name: "START", Datatype: spb.DataType_Boolean, Value: true}),
	}})

	v, err := n.rt.Tags().ReadGlobal("SpeedSP")
	if err != nil {
		t.Fatalf("read SpeedSP: %v", err)
	}
	if v.Kind != ir.TypeReal || v.F != 12.5 {
		t.Errorf("SpeedSP = %+v, want REAL 12.5 (untouched)", v)
	}
	if !strings.Contains(buf.String(), "not a UDT") {
		t.Errorf("expected a 'not a UDT' warning, got:\n%s", buf.String())
	}
}

// TestCommandTemplateForUnknownTagIgnored — a template for a tag this node
// does not have is logged once and dropped, never created.
func TestCommandTemplateForUnknownTagIgnored(t *testing.T) {
	var buf bytes.Buffer
	n := newCommandNode(t, &buf)

	n.applyCommand(Payload{Metrics: []Metric{
		memberCmd("Nope", "Motor", Metric{Name: "START", Datatype: spb.DataType_Boolean, Value: true}),
	}})

	if _, err := n.rt.Tags().ReadGlobal("Nope"); err == nil {
		t.Error("a template command created a tag that does not exist")
	}
	if !strings.Contains(buf.String(), "does not have") {
		t.Errorf("expected an unknown-tag warning, got:\n%s", buf.String())
	}
}

// TestCommandScalarStillReplaces — the scalar path is unchanged by the
// template merge: a scalar command overwrites, as it always did.
func TestCommandScalarStillReplaces(t *testing.T) {
	var buf bytes.Buffer
	n := newCommandNode(t, &buf)

	n.applyCommand(Payload{Metrics: []Metric{
		{Name: "SpeedSP", Datatype: spb.DataType_Double, Value: 61.5},
	}})

	if v := n.rt.Tags().Real("SpeedSP"); v != 61.5 {
		t.Errorf("SpeedSP = %v, want 61.5", v)
	}
}
