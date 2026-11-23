package project

import (
	"testing"

	"github.com/joyautomation/nautilus/internal/fence"
	nio "github.com/joyautomation/nautilus/io"
)

type labelled struct {
	nio.Memory
	fence.CutLabel
}

// #710: the loader tells a driver its name for the writes-cut counter, and
// whether the project has several drivers, so a source is "driver/id" only
// where the id alone could be ambiguous.
func TestLabelCutsNamesSourcesAmongSeveralDrivers(t *testing.T) {
	var one, many labelled
	labelCuts(&one, "bus", false)
	labelCuts(&many, "bus", true)
	if got := one.CutName("plc1"); got != "plc1" {
		t.Errorf("a lone driver names %q, want plc1", got)
	}
	if got := many.CutName("plc1"); got != "bus/plc1" {
		t.Errorf("one of several drivers names %q, want bus/plc1", got)
	}
	labelCuts(&nio.Memory{}, "bus", true) // a driver with no label: nothing to do
}
