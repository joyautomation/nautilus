package virtdev

import (
	"testing"
	"time"

	"github.com/joyautomation/nautilus/acceptance"
)

// The ABB Drives profile state machine, walked through the drive's own
// registers in virtual time (FENA manual p. 95: 0x0476 READY TO SWITCH ON,
// 0x047F OPERATING; RESET on a rising edge of bit 7).
func TestABBDriveStateMachine(t *testing.T) {
	for _, tc := range []struct {
		profile string
		status  uint16
	}{{"abb-acs550-reta01", 3}, {"abb-acs355-fmbt21", 3}, {"abb-acs580-fmbt21", 50}} {
		t.Run(tc.profile, func(t *testing.T) {
			set, err := ParseSet([]byte("devices: [{ name: M1, profile: "+tc.profile+", init: { RampS: 2.0, NominalAmps: 20.0, Load: 1.0 } }]\n"), t.TempDir())
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
			_ = rt
			u := plant.stores["m1"].(*modbusStore).unit
			sw := func() uint16 { return u.Holding(tc.status) }
			bits := func(want, mask uint16) {
				t.Helper()
				if got := sw() & mask; got != want {
					t.Fatalf("t=%s: status word %04X, want %04X under mask %04X", sch.Elapsed(), sw(), want, mask)
				}
			}

			sch.Advance(200 * time.Millisecond)
			bits(1<<6, 0x004F) // SWITCH-ON INHIBITED, nothing ready

			u.SetHolding(0, 0x0476)
			sch.Advance(200 * time.Millisecond)
			bits(1<<0, 0x0047) // READY TO SWITCH ON only

			u.SetHolding(1, 20000)
			u.SetHolding(0, 0x047F)
			sch.Advance(500 * time.Millisecond)
			bits(0x0007, 0x0047) // OPERATION ENABLED
			if sw()&(1<<8) != 0 {
				t.Errorf("AT_SETPOINT while still ramping (actual %d)", int16(u.Holding(tc.status+1)))
			}
			sch.Advance(2 * time.Second)
			bits(0x0107, 0x014F) // at setpoint
			if a := int16(u.Holding(tc.status + 1)); a != 20000 {
				t.Errorf("Actual 1 = %d, want 20000", a)
			}
			if cur := u.Holding(map[uint16]uint16{3: 103, 50: 106}[tc.status]); cur != 200 {
				t.Errorf("current register = %d (tenths of A), want 200", cur)
			}

			// A trip: FAULT, speed gone; a reset with the cause gone: back
			// to SWITCH-ON INHIBITED, then up again from 0x0476.
			if err := rt.Tags().SetPath("M1_Trip", true); err != nil {
				t.Fatal(err)
			}
			sch.Advance(100 * time.Millisecond)
			bits(1<<3, 0x000F)
			_ = rt.Tags().SetPath("M1_Trip", false)
			u.SetHolding(0, 0x04F6) // OFF1 + reset edge
			sch.Advance(100 * time.Millisecond)
			if sw()&(1<<3) != 0 {
				t.Fatalf("still TRIPPED after the reset edge: %04X", sw())
			}
			u.SetHolding(0, 0x0476)
			sch.Advance(200 * time.Millisecond)
			bits(1<<0, 0x004F)
		})
	}
}
