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
