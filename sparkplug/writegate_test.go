// writegate_test.go pins the write gate (writegate.go, edge-collector.md §6):
// an edge node is read-only until a tag says otherwise, Rebirth is the one
// command always honoured, a refusal is logged once and counted, and a
// command only reaches a tag on the node or device it was addressed to.

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

func speedSP(t *testing.T, n *Node) float64 {
	t.Helper()
	return n.rt.Tags().Real("SpeedSP")
}

func setSpeedSP(v float64) Payload {
	return Payload{Metrics: []Metric{{Name: "SpeedSP", Datatype: spb.DataType_Double, Value: v}}}
}

// TestWriteGateReadOnlyByDefault — with no WithWritable, neither a scalar
// nor a template command writes anything; both are counted and logged.
func TestWriteGateReadOnlyByDefault(t *testing.T) {
	var buf bytes.Buffer
	n := newCommandNode(t, &buf, WithWritable())

	n.applyCommand("", setSpeedSP(99))
	n.applyCommand("", Payload{Metrics: []Metric{
		memberCmd("Motor1", "Motor", Metric{Name: "START", Datatype: spb.DataType_Boolean, Value: true}),
	}})

	if v := speedSP(t, n); v != 12.5 {
		t.Errorf("SpeedSP = %v, want 12.5: a read-only node wrote it", v)
	}
	if fld(t, motor1(t, n), "START").B {
		t.Error("Motor1.START = true: a read-only node wrote a template member")
	}
	if got := n.Status().CommandsRefused; got != 2 {
		t.Errorf("CommandsRefused = %d, want 2", got)
	}
	for _, want := range []string{"tag=SpeedSP", "tag=Motor1.START", "not writable"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("log does not mention %q:\n%s", want, buf.String())
		}
	}
}

// TestWriteGateRebirthAlwaysHonoured — the gate is about writes: a node with
// nothing writable still answers Node Control/Rebirth.
func TestWriteGateRebirthAlwaysHonoured(t *testing.T) {
	n, fc, _ := newSilentLinkNode(t) // no WithWritable
	n.applyCommand("", rebirthCommand())
	waitPublished(t, fc, "NBIRTH", 2)
	if got := n.Status().CommandsRefused; got != 0 {
		t.Errorf("CommandsRefused = %d after a Rebirth, want 0", got)
	}
}

// TestWriteGateOpensMatchingTagsOnly — a glob opens the tags it matches and
// no others.
func TestWriteGateOpensMatchingTagsOnly(t *testing.T) {
	var buf bytes.Buffer
	n := newCommandNode(t, &buf, WithWritable("Speed*"))

	n.applyCommand("", setSpeedSP(61.5))
	n.applyCommand("", Payload{Metrics: []Metric{
		memberCmd("Motor1", "Motor", Metric{Name: "START", Datatype: spb.DataType_Boolean, Value: true}),
	}})

	if v := speedSP(t, n); v != 61.5 {
		t.Errorf("SpeedSP = %v, want 61.5 (matches Speed*)", v)
	}
	if fld(t, motor1(t, n), "START").B {
		t.Error("Motor1.START was written; Speed* does not match it")
	}
}

// TestWriteGateTagPatternOpensItsMembers — a pattern naming a UDT tag opens
// every member, whether the host sends a template or a flattened member name.
func TestWriteGateTagPatternOpensItsMembers(t *testing.T) {
	var buf bytes.Buffer
	n := newCommandNode(t, &buf, WithWritable("Motor1"))

	n.applyCommand("", Payload{Metrics: []Metric{
		memberCmd("Motor1", "Motor", memberCmd("Drive", "Drv",
			Metric{Name: "Torque", Datatype: spb.DataType_Double, Value: 12.25})),
		{Name: "Motor1.Speed", Datatype: spb.DataType_Double, Value: 900.0},
	}})

	got := motor1(t, n)
	if v := fld(t, fld(t, got, "Drive"), "Torque").F; v != 12.25 {
		t.Errorf("Motor1.Drive.Torque = %v, want 12.25", v)
	}
	if v := fld(t, got, "Speed").F; v != 900 {
		t.Errorf("Motor1.Speed = %v, want 900", v)
	}
	if got := n.Status().CommandsRefused; got != 0 {
		t.Errorf("CommandsRefused = %d, want 0", got)
	}
}

// TestWriteGateMemberPatterns — patterns on member paths split one template
// command: the writable members apply, the rest are refused and untouched.
// A pattern on a nested struct member opens everything below it.
func TestWriteGateMemberPatterns(t *testing.T) {
	var buf bytes.Buffer
	n := newCommandNode(t, &buf, WithWritable("Motor1.START", "Motor1.Drive"))

	n.applyCommand("", Payload{Metrics: []Metric{{
		Name: "Motor1", Datatype: spb.DataType_Template, Value: &Template{
			TemplateRef: "Motor",
			Metrics: []Metric{
				{Name: "START", Datatype: spb.DataType_Boolean, Value: true},
				{Name: "Speed", Datatype: spb.DataType_Double, Value: 1.0},
				{Name: "Drive", Datatype: spb.DataType_Template, Value: &Template{
					TemplateRef: "Drv",
					Metrics:     []Metric{{Name: "Fault", Datatype: spb.DataType_Boolean, Value: false}},
				}},
			},
		},
	}}})

	got := motor1(t, n)
	if !fld(t, got, "START").B {
		t.Error("Motor1.START = false, want true (writable)")
	}
	if v := fld(t, got, "Speed").F; v != 1450 {
		t.Errorf("Motor1.Speed = %v, want 1450: not writable, must be untouched", v)
	}
	if fld(t, fld(t, got, "Drive"), "Fault").B {
		t.Error("Motor1.Drive.Fault = true, want false (Motor1.Drive opens it)")
	}
	if got := n.Status().CommandsRefused; got != 1 {
		t.Errorf("CommandsRefused = %d, want 1 (Speed)", got)
	}
	if !strings.Contains(buf.String(), "tag=Motor1.Speed") {
		t.Errorf("the refused member is not named in the log:\n%s", buf.String())
	}
}

// TestWriteGateNestedMemberPattern — a pattern on a leaf below a nested
// template opens that leaf only, and leaves its sibling alone.
func TestWriteGateNestedMemberPattern(t *testing.T) {
	var buf bytes.Buffer
	n := newCommandNode(t, &buf, WithWritable("Motor1.Drive.Torque"))

	n.applyCommand("", Payload{Metrics: []Metric{
		memberCmd("Motor1", "Motor", Metric{Name: "Drive", Datatype: spb.DataType_Template, Value: &Template{
			TemplateRef: "Drv",
			Metrics: []Metric{
				{Name: "Torque", Datatype: spb.DataType_Double, Value: 5.0},
				{Name: "Fault", Datatype: spb.DataType_Boolean, Value: false},
			},
		}}),
	}})

	drive := fld(t, motor1(t, n), "Drive")
	if v := fld(t, drive, "Torque").F; v != 5 {
		t.Errorf("Drive.Torque = %v, want 5", v)
	}
	if !fld(t, drive, "Fault").B {
		t.Error("Drive.Fault was written; only Drive.Torque is writable")
	}
}

// TestWriteGateRefusalLoggedOnce — a host retrying a refused write every
// scan is counted every time but logged once.
func TestWriteGateRefusalLoggedOnce(t *testing.T) {
	var buf bytes.Buffer
	n := newCommandNode(t, &buf, WithWritable())
	for i := 0; i < 5; i++ {
		n.applyCommand("", setSpeedSP(float64(i)))
	}
	if got := n.Status().CommandsRefused; got != 5 {
		t.Errorf("CommandsRefused = %d, want 5", got)
	}
	if c := strings.Count(buf.String(), "tag=SpeedSP"); c != 1 {
		t.Errorf("refusal logged %d times, want 1", c)
	}
}

// TestCommandReachesOnlyItsOwnDevice — with SpeedSP published on device
// plc1, only a DCMD to plc1 writes it; an NCMD, or a DCMD to another device,
// is refused. A node-level tag is written by NCMD only.
func TestCommandReachesOnlyItsOwnDevice(t *testing.T) {
	var buf bytes.Buffer
	n := newCommandNode(t, &buf, WithWritable("*"),
		WithDevice(Device{ID: "plc1", Tags: []string{"SpeedSP"}}))

	n.applyCommand("", setSpeedSP(1))
	n.applyCommand("meter1", setSpeedSP(2))
	if v := speedSP(t, n); v != 12.5 {
		t.Fatalf("SpeedSP = %v: a command not addressed to plc1 wrote plc1's tag", v)
	}
	n.applyCommand("plc1", setSpeedSP(3))
	if v := speedSP(t, n); v != 3 {
		t.Errorf("SpeedSP = %v, want 3 from the DCMD to plc1", v)
	}

	start := Metric{Name: "Motor1.START", Datatype: spb.DataType_Boolean, Value: true}
	n.applyCommand("plc1", Payload{Metrics: []Metric{start}})
	if fld(t, motor1(t, n), "START").B {
		t.Error("a DCMD to plc1 wrote Motor1, which publishes at node level")
	}
	n.applyCommand("", Payload{Metrics: []Metric{start}})
	if !fld(t, motor1(t, n), "START").B {
		t.Error("an NCMD did not write the node-level Motor1.START")
	}

	if got := n.Status().CommandsRefused; got != 3 {
		t.Errorf("CommandsRefused = %d, want 3", got)
	}
	if !strings.Contains(buf.String(), "publishedOn=plc1") {
		t.Errorf("the misaddressed write does not say where the tag lives:\n%s", buf.String())
	}
}

// Tag names are case-insensitive, so the device check is too: an NCMD for
// "speedsp" or "motor1.START" names plc1's tags and is refused, not
// written from node level (#506 review).
func TestCommandDeviceCheckIgnoresCase(t *testing.T) {
	var buf bytes.Buffer
	n := newCommandNode(t, &buf, WithWritable("*"),
		WithDevice(Device{ID: "plc1", Tags: []string{"SpeedSP", "Motor1"}}))
	n.applyCommand("", Payload{Metrics: []Metric{
		{Name: "speedsp", Datatype: spb.DataType_Double, Value: 77.0},
		{Name: "motor1.START", Datatype: spb.DataType_Boolean, Value: true},
	}})
	if v := speedSP(t, n); v == 77 {
		t.Error("an NCMD for speedsp wrote plc1's SpeedSP")
	}
	if fld(t, motor1(t, n), "START").B {
		t.Error("an NCMD for motor1.START wrote plc1's Motor1")
	}
	if got := n.Status().CommandsRefused; got != 2 {
		t.Errorf("CommandsRefused = %d, want 2", got)
	}
}

func TestCommandDevice(t *testing.T) {
	for topic, want := range map[string]string{
		"spBv1.0/G/NCMD/E":        "",
		"spBv1.0/G/DCMD/E/plc1":   "plc1",
		"spBv1.0/G/DCMD/E/meter1": "meter1",
	} {
		if got := commandDevice(topic); got != want {
			t.Errorf("commandDevice(%q) = %q, want %q", topic, got, want)
		}
	}
}

// TestNewRefusesBadDevicesAndPatterns — device and gate configuration is
// checked when the node is built, not discovered on the wire.
func TestNewRefusesBadDevicesAndPatterns(t *testing.T) {
	cases := map[string]struct {
		opts []Option
		want string
	}{
		"slash in id": {[]Option{WithDevice(Device{ID: "a/b"})}, "may not contain"},
		"wildcard id": {[]Option{WithDevice(Device{ID: "a+"})}, "may not contain"},
		"empty id":    {[]Option{WithDevice(Device{})}, "needs an id"},
		"duplicate id": {[]Option{
			WithDevice(Device{ID: "plc1"}), WithDevice(Device{ID: "plc1"}),
		}, "registered twice"},
		"tag on two devices": {[]Option{
			WithDevice(Device{ID: "plc1", Tags: []string{"SpeedSP"}}),
			WithDevice(Device{ID: "plc2", Tags: []string{"SpeedSP"}}),
		}, `"SpeedSP" is on both device "plc1" and device "plc2"`},
		"bad pattern": {[]Option{WithWritable("[")}, `writable pattern "["`},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			rt := newRuntimeForGate(t)
			_, err := New(rt, Config{GroupID: "G", EdgeNode: "E"}, c.opts...)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("New error = %v, want one containing %q", err, c.want)
			}
		})
	}
}

// newRuntimeForGate is the command tests' runtime, for building a second
// Node over it.
func newRuntimeForGate(t *testing.T) *runtime.Runtime {
	t.Helper()
	var buf bytes.Buffer
	return newCommandNode(t, &buf).rt
}

// A command goes through the runtime's write front door, as HTTP and OPC
// UA writes do: a value the tag's declared type cannot hold is refused,
// never stored as a new type, and a forced tag keeps its force. Before,
// an NCMD carrying a String for the REAL SpeedSP turned it into a STRING
// tag, and a Boolean then into a BOOL.
func TestCommandWritesGoThroughTheFrontDoor(t *testing.T) {
	var buf bytes.Buffer
	n := newCommandNode(t, &buf, WithWritable("SpeedSP", "Motor1.*"))
	tags := n.rt.Tags()

	for _, m := range []Metric{
		{Name: "SpeedSP", Datatype: spb.DataType_String, Value: "fast"},
		{Name: "SpeedSP", Datatype: spb.DataType_Boolean, Value: true},
		{Name: "Motor1.Speed", Datatype: spb.DataType_String, Value: "fast"},
	} {
		n.applyCommand("", Payload{Metrics: []Metric{m}})
	}
	if v, err := tags.ReadGlobal("SpeedSP"); err != nil || v.Kind != ir.TypeReal || v.F != 12.5 {
		t.Errorf("SpeedSP after a String and a Boolean command = %+v (%v), want REAL 12.5", v, err)
	}
	if fld(t, motor1(t, n), "Speed").Kind != ir.TypeReal || fld(t, motor1(t, n), "Speed").F != 1450 {
		t.Errorf("Motor1.Speed after a String command = %+v, want REAL 1450", fld(t, motor1(t, n), "Speed"))
	}
	if got := n.Status().CommandsRefused; got != 3 {
		t.Errorf("CommandsRefused = %d, want 3", got)
	}
	if !strings.Contains(buf.String(), "does not fit the tag's type") {
		t.Errorf("log does not say why:\n%s", buf.String())
	}

	// A value the type holds is still written: an integer into a REAL.
	n.applyCommand("", Payload{Metrics: []Metric{{Name: "SpeedSP", Datatype: spb.DataType_Int64, Value: int64(40)}}})
	if v := speedSP(t, n); v != 40 {
		t.Errorf("SpeedSP after an Int64 40 command = %v, want 40", v)
	}

	// A forced tag is not written underneath its force.
	if err := tags.Force("SpeedSP", 7.0); err != nil {
		t.Fatal(err)
	}
	n.applyCommand("", setSpeedSP(99))
	tags.Unforce("SpeedSP")
	if v := speedSP(t, n); v != 40 {
		t.Errorf("SpeedSP after a command while forced, then unforced = %v, want 40 (the command refused)", v)
	}
	if !strings.Contains(buf.String(), "the tag is forced") {
		t.Errorf("log does not mention the force:\n%s", buf.String())
	}
}

// A template command is one front-door write per member it carries, so a
// member is judged exactly as the same member sent by its member path:
// a value its type cannot hold is refused (before, a String into the REAL
// Speed was stored as 0), and a forced member is refused without taking
// the template's other members with it.
func TestTemplateCommandMembersGoThroughTheFrontDoor(t *testing.T) {
	var buf bytes.Buffer
	n := newCommandNode(t, &buf, WithWritable("Motor1"))
	tags := n.rt.Tags()

	n.applyCommand("", Payload{Metrics: []Metric{
		tmplCmd("Motor1", "Motor",
			Metric{Name: "Speed", Datatype: spb.DataType_String, Value: "fast"},
			Metric{Name: "START", Datatype: spb.DataType_Boolean, Value: true}),
	}})
	if s := fld(t, motor1(t, n), "Speed"); s.Kind != ir.TypeReal || s.F != 1450 {
		t.Errorf("Motor1.Speed after a String member = %+v, want REAL 1450", s)
	}
	if !fld(t, motor1(t, n), "START").B {
		t.Error("Motor1.START was not written: one refused member took the others with it")
	}
	if got := n.Status().CommandsRefused; got != 1 {
		t.Errorf("CommandsRefused = %d, want 1 (Speed)", got)
	}

	if err := tags.Force("Motor1.Speed", 10.0); err != nil {
		t.Fatal(err)
	}
	n.applyCommand("", Payload{Metrics: []Metric{
		tmplCmd("Motor1", "Motor",
			Metric{Name: "Speed", Datatype: spb.DataType_Double, Value: 99.0},
			Metric{Name: "START", Datatype: spb.DataType_Boolean, Value: false}),
	}})
	if fld(t, motor1(t, n), "START").B {
		t.Error("Motor1.START still true: a forced member refused the whole template")
	}
	tags.Unforce("Motor1.Speed")
	if s := fld(t, motor1(t, n), "Speed").F; s != 1450 {
		t.Errorf("Motor1.Speed after a command while forced, then unforced = %v, want 1450", s)
	}
	if !strings.Contains(buf.String(), "the tag is forced") {
		t.Errorf("log does not mention the force:\n%s", buf.String())
	}
}

// A declared output no scan has written yet is not in the store. A command
// to it is written onto a zero of its declared type, by the same strict
// rule, as an HTTP write is; a wrong type is refused; and a name that is
// not declared at all is refused rather than created (before, an NCMD for
// "Nope" made a junk tag).
func TestCommandToADeclaredOutputNotYetWritten(t *testing.T) {
	rt, err := runtime.New(runtime.Options{
		Program:   cmdProgramST,
		Libraries: []string{cmdTypesST},
		Driver:    nio.NewMemory(),
		Scan:      50 * time.Millisecond,
		Tags: []runtime.TagDef{
			runtime.State("SpeedSP", 12.5),
			runtime.Typed("Motor1", runtime.RoleState, "Motor"),
			runtime.Typed("Cmd", runtime.RoleOutput, "REAL"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	n, err := New(rt, Config{GroupID: "G", EdgeNode: "E", Log: slog.New(slog.NewTextHandler(&buf, nil))}, WithWritable("*"))
	if err != nil {
		t.Fatal(err)
	}
	tags := rt.Tags()
	if _, err := tags.ReadGlobal("Cmd"); err == nil {
		t.Fatal("precondition: Cmd is already in the store")
	}
	n.applyCommand("", Payload{Metrics: []Metric{{Name: "Cmd", Datatype: spb.DataType_String, Value: "go"}}})
	if _, err := tags.ReadGlobal("Cmd"); err == nil {
		t.Error("a String command created the REAL output Cmd")
	}
	n.applyCommand("", Payload{Metrics: []Metric{{Name: "Cmd", Datatype: spb.DataType_Double, Value: 2.5}}})
	if v, err := tags.ReadGlobal("Cmd"); err != nil || v.Kind != ir.TypeReal || v.F != 2.5 {
		t.Errorf("Cmd after a Double 2.5 command = %+v (%v), want REAL 2.5", v, err)
	}
	n.applyCommand("", Payload{Metrics: []Metric{{Name: "Nope", Datatype: spb.DataType_Double, Value: 1.0}}})
	if _, err := tags.ReadGlobal("Nope"); err == nil {
		t.Error("a command for an undeclared name created a tag")
	}
}

// tmplCmd is a Template command metric for a UDT tag carrying several leaves.
func tmplCmd(metric, ref string, leaves ...Metric) Metric {
	return Metric{Name: metric, Datatype: spb.DataType_Template, Value: &Template{TemplateRef: ref, Metrics: leaves}}
}
