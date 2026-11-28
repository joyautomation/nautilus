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

// A command to an instance's from: member is refused and counted, whatever
// sparkplug.writable opens; its memory member in the same partial template
// is written (#1458 slice 3).
func TestCommandToAnInstanceFollowsMemberKinds(t *testing.T) {
	rt, err := runtime.New(runtime.Options{
		Program:   "PROGRAM Main\nVAR_EXTERNAL P101 : Pump; END_VAR\nEND_PROGRAM",
		Libraries: []string{"TYPE Pump : STRUCT Speed : REAL; RunHours : REAL; END_STRUCT; END_TYPE"},
		Driver:    nio.NewMemory(), Scan: 50 * time.Millisecond,
		Tags: []runtime.TagDef{runtime.InstanceTag("P101", "Pump", runtime.InstanceDef{Members: map[string]runtime.MemberDef{
			"Speed": {Kind: runtime.MemberFrom},
		}})},
	})
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	n, err := New(rt, Config{GroupID: "G", EdgeNode: "E", Log: slog.New(slog.NewTextHandler(&buf, nil))}, WithWritable("*"))
	if err != nil {
		t.Fatal(err)
	}
	n.applyCommand("", Payload{Metrics: []Metric{{Name: "P101", Datatype: spb.DataType_Template, Value: &Template{
		TemplateRef: "Pump",
		Metrics: []Metric{
			{Name: "Speed", Datatype: spb.DataType_Double, Value: 99.0},
			{Name: "RunHours", Datatype: spb.DataType_Double, Value: 7.0},
		},
	}}}})
	if v, _ := rt.Tags().ReadPath("P101.Speed"); v != 0.0 {
		t.Errorf("P101.Speed = %v: a command wrote a from: member", v)
	}
	if v, _ := rt.Tags().ReadPath("P101.RunHours"); v != 7.0 {
		t.Errorf("P101.RunHours = %v, want 7: the memory member must land", v)
	}
	if got := n.Status().CommandsRefused; got != 1 {
		t.Errorf("CommandsRefused = %d, want 1 (P101.Speed)", got)
	}
}
