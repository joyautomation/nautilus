//go:build !js

package camera

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/camera/cameratest"
)

func ptzRow(d *cameratest.ONVIF) Camera {
	c := onvifRow(d)
	c.Credentials = false
	c.PTZ = true
	return c
}

func TestAMoveIsBoundedToldToTheDeviceAndStoppedOnRequest(t *testing.T) {
	d := cameratest.StartONVIF(t, cameratest.ONVIFOpts{Presets: [][2]string{{"1", "Home"}, {"2", "Gate\t\nview"}}})
	f := &Fetcher{}
	c := ptzRow(d)
	ctx := context.Background()
	if err := f.PTZMove(ctx, c, Velocity{Pan: 0.5, Tilt: -0.25}, 1500*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := f.PTZStop(ctx, c); err != nil {
		t.Fatal(err)
	}
	got := d.Moves()
	// the device is told the limit too (rounded up to whole seconds): a controller that dies mid-move does not leave it moving
	if len(got) != 2 || got[0] != "move 0.500 -0.250 0.000 PT2S" || got[1] != "stop" {
		t.Errorf("the device was told %q", got)
	}
	// out of range, not finite, zero, too short, too long: refused before the device is asked
	for _, v := range []Velocity{{Pan: 1.5}, {Tilt: math.NaN()}, {Zoom: math.Inf(1)}, {}} {
		if err := f.PTZMove(ctx, c, v, time.Second); err == nil {
			t.Errorf("%+v was sent", v)
		}
	}
	for _, d2 := range []time.Duration{0, time.Millisecond, MaxMove + time.Second} {
		if err := f.PTZMove(ctx, c, Velocity{Pan: 1}, d2); err == nil {
			t.Errorf("a move of %s was sent", d2)
		}
	}
	if len(d.Moves()) != 2 {
		t.Errorf("a refused move reached the device: %q", d.Moves())
	}
}

func TestPresetsAreTheDevicesAndOnlyTheDevicesAreGoneTo(t *testing.T) {
	d := cameratest.StartONVIF(t, cameratest.ONVIFOpts{Presets: [][2]string{{"1", "Home"}, {"2", "Gate\t\nview"}, {`3"><x/>`, "evil"}}})
	f := &Fetcher{}
	c := ptzRow(d)
	ctx := context.Background()
	ps, err := f.PTZPresets(ctx, c)
	if err != nil || len(ps) != 2 || ps[0].Name != "Home" || ps[1].Name != "Gateview" {
		t.Fatalf("presets %+v %v (a token that is not plain is left out, names are cleaned)", ps, err)
	}
	if err := f.PTZGoto(ctx, c, "2"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"9", "", `1"><x/>`, "1 "} {
		if err := f.PTZGoto(ctx, c, bad); err == nil {
			t.Errorf("preset %q was gone to", bad)
		}
	}
	if m := d.Moves(); len(m) != 1 || m[0] != "goto 2" {
		t.Errorf("the device was told %q", m)
	}
}

func TestACameraThatIsNotMarkedPTZOrIsNotONVIFCannotBeMoved(t *testing.T) {
	d := cameratest.StartONVIF(t, cameratest.ONVIFOpts{})
	c := ptzRow(d)
	c.PTZ = false
	f := &Fetcher{}
	if err := f.PTZMove(context.Background(), c, Velocity{Pan: 1}, time.Second); err != ErrNoPTZ {
		t.Errorf("a row without ptz: true: %v", err)
	}
	if len(d.Moves()) != 0 {
		t.Error("the device was asked")
	}
	errs := Validate([]Camera{{ID: "m", Asset: "a", Kind: KindMJPEG, URL: "http://x/y", PTZ: true}})
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "ptz") {
		t.Errorf("ptz on an mjpeg row: %v", errs)
	}
}
