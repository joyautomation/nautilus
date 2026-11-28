package server

import (
	"testing"

	"github.com/gopcua/opcua/ua"
	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/runtime"
)

// The front door's refusal of an instance's from: member reaches an OPC UA
// client as Bad_NotWritable, as an input's does (#1458 slice 3).
func TestInstanceReadOnlyMemberIsBadNotWritable(t *testing.T) {
	rt, err := runtime.New(runtime.Options{
		Program:   "PROGRAM Main\nVAR_EXTERNAL P101 : Pump; END_VAR\nEND_PROGRAM",
		Libraries: []string{"TYPE Pump : STRUCT Speed : REAL; RunHours : REAL; END_STRUCT; END_TYPE"},
		Driver:    nio.NewMemory(),
		Tags: []runtime.TagDef{runtime.InstanceTag("P101", "Pump", runtime.InstanceDef{Members: map[string]runtime.MemberDef{
			"Speed": {Kind: runtime.MemberFrom},
		}})},
	})
	if err != nil {
		t.Fatal(err)
	}
	errs := rt.Tags().Write([]runtime.WriteOp{{Path: "P101.Speed", Value: 1.0}, {Path: "P101.RunHours", Value: 2.0}})
	if got := frontDoorStatus(errs[0]); got != ua.StatusBadNotWritable {
		t.Errorf("a from: member's write maps to %v, want Bad_NotWritable", got)
	}
	if got := frontDoorStatus(errs[1]); got != ua.StatusGood {
		t.Errorf("a memory member's write maps to %v, want Good", got)
	}
}
