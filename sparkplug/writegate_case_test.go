// writegate_case_test.go pins that sparkplug.writable is matched against
// the DECLARED spelling of the tag or member a command names, not the
// host's (#619). Tag and member names are case-insensitive, so a command
// for "speedSP" writes SpeedSP; matching the patterns against what the
// host sent let a respelling open a tag they keep closed. OPC UA matches
// its canonical address the same way.

package sparkplug

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/runtime"
	"github.com/joyautomation/nautilus/sparkplug/spb"
)

func speedCmd(name string, v float64) Payload {
	return Payload{Metrics: []Metric{{Name: name, Datatype: spb.DataType_Double, Value: v}}}
}

// A respelled name does not open a closed tag: "speed*" does not match the
// declared SpeedSP, so "speedSP" is refused as "SpeedSP" is.
func TestWriteGateRespellingDoesNotOpenAClosedTag(t *testing.T) {
	var buf bytes.Buffer
	n := newCommandNode(t, &buf, WithWritable("speed*"))

	n.applyCommand("", speedCmd("SpeedSP", 1))
	n.applyCommand("", speedCmd("speedSP", 2))
	n.applyCommand("", speedCmd("speedsp", 3))
	if v := speedSP(t, n); v != 12.5 {
		t.Errorf("SpeedSP = %v, want 12.5: a respelled command got past speed*", v)
	}
	if got := n.Status().CommandsRefused; got != 3 {
		t.Errorf("CommandsRefused = %d, want 3", got)
	}
}

// A respelling does not close an open tag: "SpeedSP" opens the declared
// SpeedSP whatever case the host writes it in.
func TestWriteGateRespellingDoesNotCloseAnOpenTag(t *testing.T) {
	var buf bytes.Buffer
	n := newCommandNode(t, &buf, WithWritable("SpeedSP"))

	n.applyCommand("", speedCmd("speedsp", 7))
	if v := speedSP(t, n); v != 7 {
		t.Errorf("SpeedSP = %v, want 7: speedsp names the open SpeedSP", v)
	}
	if got := n.Status().CommandsRefused; got != 0 {
		t.Errorf("CommandsRefused = %d, want 0\n%s", got, buf.String())
	}
}

// The documented shape: Pump* does not open a tag declared pumpHouseDoor,
// so a host's PumpHouseDoor is refused; PumpSpeed, which Pump* does match,
// is still written in any case.
func TestWriteGatePumpPatternAndADeclaredLowerCaseTag(t *testing.T) {
	rt, err := runtime.New(runtime.Options{
		Program:   cmdProgramST,
		Libraries: []string{cmdTypesST},
		Driver:    nio.NewMemory(),
		Scan:      50 * time.Millisecond,
		Tags: []runtime.TagDef{
			runtime.State("SpeedSP", 12.5),
			runtime.Typed("Motor1", runtime.RoleState, "Motor"),
			runtime.State("pumpHouseDoor", false),
			runtime.State("PumpSpeed", 10.0),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	n, err := New(rt, Config{GroupID: "G", EdgeNode: "E", Log: slog.New(slog.NewTextHandler(&buf, nil))},
		WithWritable("Pump*"))
	if err != nil {
		t.Fatal(err)
	}
	n.applyCommand("", Payload{Metrics: []Metric{
		{Name: "PumpHouseDoor", Datatype: spb.DataType_Boolean, Value: true},
		{Name: "pumpspeed", Datatype: spb.DataType_Double, Value: 20.0},
	}})
	tags := rt.Tags()
	if tags.Bool("pumpHouseDoor") {
		t.Error("pumpHouseDoor = true: Pump* opened a tag declared pumpHouseDoor")
	}
	if v := tags.Real("PumpSpeed"); v != 20 {
		t.Errorf("PumpSpeed = %v, want 20 (Pump* matches its declared name)", v)
	}
	if got := n.Status().CommandsRefused; got != 1 {
		t.Errorf("CommandsRefused = %d, want 1\n%s", got, buf.String())
	}
}

// A template command and a flat member name are gated on the declared
// member path: Motor1.START, not the host's motor1.start.
func TestWriteGateMemberRespelling(t *testing.T) {
	start := func(tag, member string) Payload {
		return Payload{Metrics: []Metric{
			memberCmd(tag, "Motor", Metric{Name: member, Datatype: spb.DataType_Boolean, Value: true}),
		}}
	}
	flat := Payload{Metrics: []Metric{{Name: "motor1.start", Datatype: spb.DataType_Boolean, Value: true}}}

	// Closed: a pattern in the host's spelling opens nothing declared.
	for name, p := range map[string]Payload{"template": start("motor1", "start"), "flat": flat} {
		t.Run("closed/"+name, func(t *testing.T) {
			var buf bytes.Buffer
			n := newCommandNode(t, &buf, WithWritable("motor1.start", "motor1"))
			n.applyCommand("", p)
			if fld(t, motor1(t, n), "START").B {
				t.Error("Motor1.START = true: motor1.start opened the declared Motor1.START")
			}
			if got := n.Status().CommandsRefused; got != 1 {
				t.Errorf("CommandsRefused = %d, want 1", got)
			}
		})
	}
	// Open: the declared spelling opens every respelling of it.
	for name, p := range map[string]Payload{"template": start("motor1", "start"), "flat": flat} {
		t.Run("open/"+name, func(t *testing.T) {
			var buf bytes.Buffer
			n := newCommandNode(t, &buf, WithWritable("Motor1.START"))
			n.applyCommand("", p)
			if !fld(t, motor1(t, n), "START").B {
				t.Errorf("Motor1.START = false: a respelling closed it\n%s", buf.String())
			}
			if got := n.Status().CommandsRefused; got != 0 {
				t.Errorf("CommandsRefused = %d, want 0", got)
			}
		})
	}
	// Nested: a member below a member resolves level by level.
	t.Run("nested", func(t *testing.T) {
		var buf bytes.Buffer
		n := newCommandNode(t, &buf, WithWritable("Motor1.Drive.Fault"))
		n.applyCommand("", Payload{Metrics: []Metric{{
			Name: "MOTOR1", Datatype: spb.DataType_Template,
			Value: &Template{TemplateRef: "Motor", Metrics: []Metric{{
				Name: "drive", Datatype: spb.DataType_Template,
				Value: &Template{TemplateRef: "Drv", Metrics: []Metric{
					{Name: "fault", Datatype: spb.DataType_Boolean, Value: false},
					{Name: "torque", Datatype: spb.DataType_Double, Value: 1.0},
				}},
			}}},
		}}})
		drive := fld(t, motor1(t, n), "Drive")
		if fld(t, drive, "Fault").B {
			t.Errorf("Drive.Fault = true: the open drive.fault was not written\n%s", buf.String())
		}
		if v := fld(t, drive, "Torque").F; v != 88.5 {
			t.Errorf("Drive.Torque = %v, want 88.5 (not writable)", v)
		}
		if got := n.Status().CommandsRefused; got != 1 {
			t.Errorf("CommandsRefused = %d, want 1", got)
		}
	})
}

// A DCMD is gated the same way as an NCMD.
func TestWriteGateDeviceCommandRespelling(t *testing.T) {
	dev := WithDevice(Device{ID: "plc1", Tags: []string{"SpeedSP"}})

	var buf bytes.Buffer
	n := newCommandNode(t, &buf, WithWritable("speed*"), dev)
	n.applyCommand("plc1", speedCmd("speedSP", 2))
	if v := speedSP(t, n); v != 12.5 {
		t.Errorf("SpeedSP = %v, want 12.5: a respelled DCMD got past speed*", v)
	}

	n = newCommandNode(t, &buf, WithWritable("SpeedSP"), dev)
	n.applyCommand("plc1", speedCmd("speedsp", 4))
	if v := speedSP(t, n); v != 4 {
		t.Errorf("SpeedSP = %v, want 4: a respelled DCMD closed the open SpeedSP", v)
	}
}

// An unknown name is still refused, however the patterns read.
func TestWriteGateUnknownNameStillRefused(t *testing.T) {
	var buf bytes.Buffer
	n := newCommandNode(t, &buf, WithWritable("*"))
	n.applyCommand("", Payload{Metrics: []Metric{
		{Name: "Nope", Datatype: spb.DataType_Double, Value: 1.0},
		{Name: "motor1.nope", Datatype: spb.DataType_Boolean, Value: true},
	}})
	if _, err := n.rt.Tags().ReadGlobal("Nope"); err == nil {
		t.Error("a command for an undeclared name created a tag")
	}
	if got := n.Status().CommandsRefused; got != 2 {
		t.Errorf("CommandsRefused = %d, want 2", got)
	}
}

// newFoldNode is a node over a runtime with an output no scan has written
// (pumpInlet), a global only the program declares (Gain) and an input
// (Limit), for the fold tests below.
func newFoldNode(t *testing.T, patterns ...string) (*Node, *bytes.Buffer) {
	t.Helper()
	rt, err := runtime.New(runtime.Options{
		Program: "PROGRAM Main\nVAR_EXTERNAL\n\tpumpInlet : REAL;\n\tGain : REAL;\n\tLimit : REAL;\nEND_VAR\nEND_PROGRAM\n",
		Driver:  nio.NewMemory(), Scan: 50 * time.Millisecond,
		Tags: []runtime.TagDef{
			runtime.Typed("pumpInlet", runtime.RoleOutput, "REAL"),
			runtime.Input("Limit", runtime.Init(4.5)),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	n, err := New(rt, Config{GroupID: "G", EdgeNode: "E", Log: slog.New(slog.NewTextHandler(&buf, nil))},
		WithWritable(patterns...))
	if err != nil {
		t.Fatal(err)
	}
	return n, &buf
}

// The store folds a name with ir.NameKey (upper case), under which the
// dotless ı of "Pumpınlet" is the I of the declared output pumpInlet;
// strings.EqualFold does not see them as one. The gate must judge the tag
// the write lands on: Pump* does not open pumpInlet, so "Pumpınlet" is
// refused, and nothing is created under that spelling.
func TestWriteGateDotlessIDoesNotOpenAnUnwrittenOutput(t *testing.T) {
	n, buf := newFoldNode(t, "Pump*")
	tags := n.rt.Tags()
	if _, err := tags.ReadGlobal("pumpInlet"); err == nil {
		t.Fatal("precondition: pumpInlet is already in the store")
	}
	n.applyCommand("", speedCmd("PumpInlet", 1))
	n.applyCommand("", speedCmd("Pumpınlet", 2))
	if _, err := tags.ReadGlobal("pumpInlet"); err == nil {
		t.Errorf("a command got past Pump* to the output pumpInlet: %v\n%s", snapKeys(tags.Snapshot()), buf)
	}
	if got := n.Status().CommandsRefused; got != 2 {
		t.Errorf("CommandsRefused = %d, want 2", got)
	}

	// Opened by its declared name, the output is created under that name
	// whatever the host's spelling.
	n, buf = newFoldNode(t, "pump*")
	n.applyCommand("", speedCmd("PUMPıNLET", 3))
	snap := n.rt.Tags().Snapshot()
	if v, ok := snap["pumpInlet"]; !ok || v.F != 3 {
		t.Errorf("pumpInlet not written under its declared name: %v\n%s", snapKeys(snap), buf)
	}
}

// A global only the program declares (no tag definition, not an output) is
// not created by a command under the host's spelling: before, "g*" let an
// NCMD for "gain" create a tag "gain" that the program and the next birth
// then saw, and "G*" refused it.
func TestCommandDoesNotCreateAProgramOnlyGlobal(t *testing.T) {
	for _, pat := range []string{"g*", "G*", "*"} {
		n, buf := newFoldNode(t, pat)
		tags := n.rt.Tags()
		if _, err := tags.ReadGlobal("Gain"); err == nil {
			t.Fatal("precondition: Gain is already in the store")
		}
		n.applyCommand("", speedCmd("gain", 5))
		if _, err := tags.ReadGlobal("gain"); err == nil {
			t.Errorf("writable %q: an NCMD for gain created a tag: %v\n%s", pat, snapKeys(tags.Snapshot()), buf)
		}
		if got := n.Status().CommandsRefused; got != 1 {
			t.Errorf("writable %q: CommandsRefused = %d, want 1", pat, got)
		}
	}
}

// The input refusal folds names as the store does: "Lımit" names the input
// Limit and is refused, whatever writable opens.
func TestCommandToAnInputRefusedUnderDotlessI(t *testing.T) {
	n, buf := newFoldNode(t, "*")
	n.applyCommand("", speedCmd("Lımit", 42))
	if v := n.rt.Tags().Real("Limit"); v != 4.5 {
		t.Errorf("Limit = %v, want 4.5: a command wrote the input\n%s", v, buf)
	}
	if !strings.Contains(buf.String(), "an input") {
		t.Errorf("the refusal does not say the tag is an input:\n%s", buf)
	}
}
