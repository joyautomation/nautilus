package virtdev

import (
	"testing"
	"time"

	"github.com/joyautomation/nautilus/acceptance"
)

// The MCT4's loop 1 through its registers (MCT4 4.3 manual Rev. G, App.
// A.1), in virtual time: auto settles on the setpoint while there is a
// flame, manual holds the %Out the controller wrote (a register both sides
// write), a sensor break reads error 39 and drops the output.
func TestMCT4Loop(t *testing.T) {
	set, err := ParseSet([]byte("devices: [{ name: TIC, profile: fdc-mct4, init: { FlameOn: true, HeatRate: 10.0 } }]\n"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	plant, err := NewPlant(set)
	if err != nil {
		t.Fatal(err)
	}
	rt, sch, err := acceptance.NewRuntime(plant.Options())
	if err != nil {
		t.Fatalf("%v\n%s", err, plant.Program())
	}
	st := plant.stores["tic"].(*modbusStore)
	reg := func(a uint16) int16 { return int16(st.unit.Holding(a)) }

	st.unit.SetHolding(36, 400) // SP 400 °F
	sch.Advance(10 * time.Minute)
	if pv := reg(35); pv < 395 || pv > 405 {
		t.Fatalf("auto: PV %d after 10 min, want 400 +-5", pv)
	}
	if out := reg(37); out <= 0 || out >= 10000 {
		t.Errorf("auto: %%Out register %d, want between 0 and 100.00", out)
	}
	if s := st.unit.Holding(38); s&(1<<2) == 0 || s&(1<<4) != 0 {
		t.Errorf("auto: status %04X, want Static on, Manual off", s)
	}

	// Manual: the controller's %Out holds, written over the loop's.
	st.unit.SetHolding(9, 1)
	st.unit.SetHolding(37, 2500) // 25.00 %
	sch.Advance(5 * time.Second)
	if out := reg(37); out != 2500 {
		t.Errorf("manual: %%Out register %d, want the 2500 written", out)
	}
	if s := st.unit.Holding(38); s&(1<<4) == 0 {
		t.Errorf("manual: status %04X, want Manual on", s)
	}

	// Back to auto, then a sensor break: error 39, no output.
	st.unit.SetHolding(9, 0)
	sch.Advance(5 * time.Second)
	if err := rt.Tags().SetPath("TIC_SensorBreak", true); err != nil {
		t.Fatal(err)
	}
	sch.Advance(time.Second)
	if e := st.unit.Holding(39); e != 39 {
		t.Errorf("sensor break: error %d, want 39", e)
	}
	if out := reg(37); out != 0 {
		t.Errorf("sensor break: %%Out %d, want 0", out)
	}

	// No flame: the temperature falls whatever the output.
	_ = rt.Tags().SetPath("TIC_SensorBreak", false)
	_ = rt.Tags().SetPath("TIC_FlameOn", false)
	before := reg(35)
	sch.Advance(2 * time.Minute)
	if after := reg(35); after >= before {
		t.Errorf("no flame: PV %d -> %d, want it falling", before, after)
	}
	if errs := plant.Errors(); len(errs) > 0 {
		t.Errorf("store errors: %v", errs)
	}
}
