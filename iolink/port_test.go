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
	if !Ok.Drives() || !Unchecked.Drives() || NoDevice.Drives() || WrongDevice.Drives() {
		t.Error("Drives: only Ok and Unchecked may drive a port")
	}
	if Ok.Fault() || Unchecked.Fault() || !NoDevice.Fault() || !WrongDevice.Fault() {
		t.Error("Fault: only NoDevice and WrongDevice raise the identity fault")
	}
}

func gate(t *testing.T, s State) *OutGate {
	t.Helper()
	out := mustOut(t, levelIODD(t))
	return NewOutGate(out, s)
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

// Items of level100's process data out: Teach (bit 0), Mode (bits 3..1),
// Lock (bit 7); Mode 5 with Lock set is 0x8A.
func TestOutGateDrivesOnlyWhatWasWritten(t *testing.T) {
	g := gate(t, Ok)
	if p := plan(t, g); p.Valid || p.Image != nil {
		t.Errorf("nothing written yet, plan %+v", p)
	}
	if err := g.Write(1, uint64(5)); err != nil {
		t.Fatal(err)
	}
	if err := g.Write(2, true); err != nil {
		t.Fatal(err)
	}
	if p := plan(t, g); !p.Valid || !bytes.Equal(p.Image, []byte{0x8A}) {
		t.Errorf("plan %+v, want valid 8A (Teach, never written, is zero)", p)
	}
}

// The safety rules: a wrong or missing device gets nothing and the master's
// valid flag goes false; the right one coming back is NOT sent the old
// image.
func TestOutGateWrongDeviceGetsNothingAndNothingReturns(t *testing.T) {
	for _, bad := range []State{WrongDevice, NoDevice} {
		t.Run(bad.String(), func(t *testing.T) {
			g := gate(t, Ok)
			_ = g.Write(1, uint64(5))
			if !plan(t, g).Valid {
				t.Fatal("not driving a right device")
			}
			g.SetState(bad)
			if p := plan(t, g); p.Valid || p.Image != nil {
				t.Errorf("%v: plan %+v, want no image and valid false (not zeros either)", bad, p)
			}
			if err := g.Write(1, uint64(2)); err == nil || !strings.Contains(err.Error(), bad.String()) {
				t.Errorf("a write to a %v port: %v, want a refusal naming the state", bad, err)
			}
			g.SetState(Ok)
			if p := plan(t, g); p.Valid || p.Image != nil {
				t.Errorf("the right device is back: plan %+v, want nothing until a program writes again", p)
			}
			if err := g.Write(1, uint64(2)); err != nil {
				t.Fatal(err)
			}
			// Only the fresh write: the Mode 5 from before the break is gone.
			if p := plan(t, g); !p.Valid || !bytes.Equal(p.Image, []byte{0x04}) {
				t.Errorf("after a fresh write, plan %+v, want valid 04", p)
			}
		})
	}
}

// A port that starts with the device not yet checked drives nothing.
func TestOutGateStartsClosed(t *testing.T) {
	g := gate(t, NoDevice)
	if err := g.Write(0, true); err == nil {
		t.Error("a write before the first identity check was accepted")
	}
	g.SetState(Ok)
	if p := plan(t, g); p.Valid {
		t.Errorf("plan %+v", p)
	}
}

// Without id points nothing can be checked: it writes as told, and never
// is forced off.
func TestOutGateUncheckedWritesAsTold(t *testing.T) {
	g := gate(t, Unchecked)
	if err := g.Write(2, true); err != nil {
		t.Fatal(err)
	}
	if p := plan(t, g); !p.Valid || !bytes.Equal(p.Image, []byte{0x80}) {
		t.Errorf("plan %+v, want valid 80", p)
	}
}

// A value that does not fit is refused on the write, naming the item, and
// leaves the last good value in place.
func TestOutGateRefusesWhatDoesNotFit(t *testing.T) {
	g := gate(t, Ok)
	_ = g.Write(1, uint64(5))
	if err := g.Write(1, uint64(9)); err == nil {
		t.Fatal("Mode 9 does not fit 3 bits, yet was accepted")
	}
	if p := plan(t, g); !bytes.Equal(p.Image, []byte{0x0A}) {
		t.Errorf("plan %+v after a refused write, want the earlier 0A", p)
	}
	if err := g.Write(7, true); err == nil {
		t.Error("a write to an item that does not exist was accepted")
	}
}
