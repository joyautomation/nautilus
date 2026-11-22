package main

import (
	"os"
	"testing"

	"github.com/joyautomation/nautilus/internal/project"
	"github.com/joyautomation/nautilus/runtime"
)

// The lift station's alarms say which piece of equipment they are on, so out
// of service takes P-101 out as a whole (#715) and not one alarm tag at a time.
func TestLiftStationAlarmsGroupByEquipment(t *testing.T) {
	p, err := project.Load(os.DirFS("../../examples/lift-station"), project.ManifestName)
	if err != nil {
		t.Skip("example does not load here:", err)
	}
	rt, err := runtime.New(p.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	defs, errs, _, err := p.CheckAlarms(rt)
	if err != nil || len(errs) != 0 {
		t.Fatalf("alarms: %v %v", err, errs)
	}
	got := map[string]string{}
	for _, d := range defs {
		got[d.ID] = d.Equipment
	}
	for id, want := range map[string]string{"P101_SealFail": "P101", "P101_VfdFault": "P101", "P102_OverTemp": "P102", "P102_LockedOut": "P102", "LSHH101": "WetWell"} {
		if got[id] != want {
			t.Errorf("%s is on %q, want %q", id, got[id], want)
		}
	}
}
