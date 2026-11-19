package iolink

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/iolink/iodd"
)

func levelIODD(t *testing.T) *iodd.IODD {
	t.Helper()
	data, err := os.ReadFile("iodd/testdata/acme-level100-20261118-IODD1.1.xml")
	if err != nil {
		t.Fatal(err)
	}
	d, err := iodd.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestCheck(t *testing.T) {
	d := levelIODD(t)
	v, dev := d.Identity.VendorID, d.Identity.DeviceID
	for _, c := range []struct {
		name         string
		haveIDs      bool
		vendor, devc uint32
		want         State
	}{
		{"the right device", true, v, dev, Ok},
		{"no device", true, 0, 0, NoDevice},
		{"an additional device id the IODD covers", true, v, dev + 1, Ok},
		{"another model of the vendor", true, v, dev + 100, WrongDevice},
		{"another vendor, same device id", true, v + 1, dev, WrongDevice},
		{"vendor only", true, v, 0, WrongDevice},
		{"no id points", false, 0, 0, Unchecked},
		{"no id points, a device there", false, v, dev, Unchecked},
	} {
		if got := Check(d, c.haveIDs, c.vendor, c.devc); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
	if !Ok.Drives() || !Unchecked.Drives() || NoDevice.Drives() || WrongDevice.Drives() || AwaitingRearm.Drives() {
		t.Error("Drives: only Ok and Unchecked may drive a port")
	}
	if Ok.Fault() || Unchecked.Fault() || !NoDevice.Fault() || !WrongDevice.Fault() || !AwaitingRearm.Fault() {
		t.Error("Fault: NoDevice, WrongDevice and AwaitingRearm raise the identity fault")
	}
}

func gate(t *testing.T, s State) *OutGate {
	t.Helper()
	return NewOutGate(mustOut(t, levelIODD(t)), s)
}

func mustOut(t *testing.T, d *iodd.IODD) *iodd.Image {
	t.Helper()
	pd, err := d.Layout("")
	if err != nil || pd.Out == nil {
		t.Fatalf("level100 has no process data out: %v", err)
	}
	return pd.Out
}

func plan(t *testing.T, g *OutGate) OutPlan {
	t.Helper()
	p, err := g.Plan()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// setAll sets level100's three items: Teach, Mode and Lock (Teach bit 0,
// Mode bits 3..1, Lock bit 7).
func setAll(t *testing.T, g *OutGate, teach bool, mode uint64, lock bool) {
	t.Helper()
	for i, v := range []any{teach, mode, lock} {
		if err := g.Set(i, v); err != nil {
			t.Fatal(err)
		}
	}
}

// A port is armed by an explicit Rearm and by nothing else: values set
// before it are held, not sent, and a controller that has just started has
// not been told the device is the right one.
func TestOutGateSendsNothingUntilRearmed(t *testing.T) {
	g := gate(t, Ok)
	setAll(t, g, true, 5, true)
	if p := plan(t, g); p.Valid || p.Image != nil {
		t.Errorf("values set but never armed: plan %+v", p)
	}
	if g.Armed() {
		t.Error("armed with no Rearm")
	}
	if err := g.Rearm(); err != nil {
		t.Fatal(err)
	}
	if p := plan(t, g); !p.Valid || !bytes.Equal(p.Image, []byte{0x8B}) {
		t.Errorf("armed: plan %+v, want valid 8B (Teach, Mode 5, Lock)", p)
	}
	// An armed port follows the program's values.
	setAll(t, g, false, 2, false)
	if p := plan(t, g); !p.Valid || !bytes.Equal(p.Image, []byte{0x04}) {
		t.Errorf("new values: plan %+v, want valid 04", p)
	}
}

// The safety rules: a wrong or missing device gets nothing and the master's
// valid flag goes false; the right one coming back is sent nothing until
// Rearm, and then the program's CURRENT values, not the ones from before.
func TestOutGateBreakDisarmsAndRearmSendsTheCurrentValues(t *testing.T) {
	for _, bad := range []State{WrongDevice, NoDevice} {
		t.Run(bad.String(), func(t *testing.T) {
			g := gate(t, Ok)
			setAll(t, g, true, 5, true)
			_ = g.Rearm()
			if !plan(t, g).Valid {
				t.Fatal("not driving a right device")
			}
			g.SetState(bad)
			if p := plan(t, g); p.Valid || p.Image != nil {
				t.Errorf("%v: plan %+v, want no image and valid false (not zeros either)", bad, p)
			}
			if g.Armed() {
				t.Errorf("still armed with a %v on the port", bad)
			}
			if err := g.Rearm(); err == nil || !strings.Contains(err.Error(), bad.String()) {
				t.Errorf("Rearm with a %v on the port: %v, want a refusal naming the state", bad, err)
			}
			// The program goes on setting values during the break.
			setAll(t, g, false, 2, false)
			g.SetState(Ok)
			if p := plan(t, g); p.Valid || p.Image != nil {
				t.Errorf("the right device is back: plan %+v, want nothing until Rearm", p)
			}
			if err := g.Rearm(); err != nil {
				t.Fatal(err)
			}
			if p := plan(t, g); !p.Valid || !bytes.Equal(p.Image, []byte{0x04}) {
				t.Errorf("after Rearm, plan %+v, want the current values (04), not the pre-break 8B", p)
			}
		})
	}
}

// A port that starts with the device not yet checked drives nothing, and
// cannot be armed.
func TestOutGateStartsClosed(t *testing.T) {
	g := gate(t, NoDevice)
	if err := g.Rearm(); err == nil {
		t.Error("Rearm before the first identity check was accepted")
	}
	g.SetState(Ok)
	if p := plan(t, g); p.Valid {
		t.Errorf("plan %+v", p)
	}
}

// Unchecked drives like Ok, but is armed the same way.
func TestOutGateUncheckedNeedsRearmToo(t *testing.T) {
	g := gate(t, Unchecked)
	setAll(t, g, false, 0, true)
	if p := plan(t, g); p.Valid {
		t.Errorf("unchecked and never armed: plan %+v", p)
	}
	if err := g.Rearm(); err != nil {
		t.Fatal(err)
	}
	if p := plan(t, g); !p.Valid || !bytes.Equal(p.Image, []byte{0x80}) {
		t.Errorf("plan %+v, want valid 80", p)
	}
}

// A value that does not fit is refused naming the item, and the last good
// value stays; an item never set is zero when the operator arms the port.
func TestOutGateRefusesWhatDoesNotFit(t *testing.T) {
	g := gate(t, Ok)
	if err := g.Set(1, uint64(5)); err != nil {
		t.Fatal(err)
	}
	if err := g.Set(1, uint64(9)); err == nil {
		t.Fatal("Mode 9 does not fit 3 bits, yet was accepted")
	}
	_ = g.Rearm()
	if p := plan(t, g); !bytes.Equal(p.Image, []byte{0x0A}) {
		t.Errorf("plan %+v, want 0A: Mode 5 kept, Teach and Lock never set are zero", p)
	}
	if err := g.Set(7, true); err == nil {
		t.Error("a value for an item that does not exist was accepted")
	}
}
