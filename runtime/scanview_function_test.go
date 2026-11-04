package runtime

import (
	"testing"

	"github.com/joyautomation/nautilus/lang/ir"
)

// A scan snapshots every tag its program binds, those a user FUNCTION binds
// through VAR_EXTERNAL included (nautilus#590). Left out, the FUNCTION read
// the live store mid-scan: a driver or another task's commit landing during
// the scan showed through, so one scan could see two values of one tag.
func TestScanSnapshotsAFunctionsExternals(t *testing.T) {
	p, err := Compile(`FUNCTION Scale : REAL
VAR_EXTERNAL RawLevel : REAL; PumpCmd : REAL; END_VAR
PumpCmd := RawLevel * 2.0;
Scale := PumpCmd;
END_FUNCTION

PROGRAM Main
VAR r : REAL; END_VAR
r := Scale();
END_PROGRAM`)
	if err != nil {
		t.Fatal(err)
	}
	tags := NewTags()
	tags.setAny("RawLevel", 5.0)
	if err := p.Run(tags); err != nil {
		t.Fatal(err)
	}
	snap := map[string]bool{}
	for _, n := range p.exts {
		snap[n] = true
	}
	for _, n := range []string{"RawLevel", "PumpCmd"} {
		if !snap[n] {
			t.Errorf("%s is not in the scan's snapshot list %v", n, p.exts)
		}
	}
	if v, _ := tags.ReadGlobal("PumpCmd"); v.Kind != ir.TypeReal || v.F != 10 {
		t.Errorf("PumpCmd = %+v, want REAL 10", v)
	}
}
