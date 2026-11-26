//go:build !js

package camera

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/url"
	"time"
)

// Pan, tilt and zoom (#724 slice 4b) over ONVIF's PTZ service. The controller
// only ever moves a camera for a bounded time (the device is told the timeout
// too), and a stop is always sent when the time is up.

// ErrNoPTZ says the camera has no pan and tilt the controller can use.
var ErrNoPTZ = errors.New("this camera has no pan and tilt")

// Bounds on a move.
const (
	// MaxMove is the longest one continuous move lasts. A held button sends a stop
	// when it is let go; this is what ends a move whose browser went away.
	MaxMove = 10 * time.Second
	// MinMove is the shortest: a nudge.
	MinMove = 100 * time.Millisecond
	// MaxPresets bounds the presets listed from a device.
	MaxPresets = 64
)

// Velocity is a move: each part from -1 (full speed one way) to 1 (the other).
type Velocity struct{ Pan, Tilt, Zoom float64 }

// Valid says each part is a finite number in range and some part is not zero.
func (v Velocity) Valid() error {
	for _, p := range []float64{v.Pan, v.Tilt, v.Zoom} {
		if math.IsNaN(p) || math.IsInf(p, 0) || p < -1 || p > 1 {
			return errors.New("each of pan, tilt and zoom is between -1 and 1")
		}
	}
	if v.Pan == 0 && v.Tilt == 0 && v.Zoom == 0 {
		return errors.New("a move needs pan, tilt or zoom: to stop, stop")
	}
	return nil
}

// Preset is a position the device remembers.
type Preset struct {
	Token string `json:"token"`
	Name  string `json:"name"`
}

func (f *Fetcher) ptzService(ctx context.Context, c Camera) (soapClient *soap, to, profile string, err error) {
	if c.Kind != KindONVIF || !c.PTZ {
		return nil, "", "", ErrNoPTZ
	}
	r, err := f.ONVIF(ctx, c)
	if err != nil {
		return nil, "", "", err
	}
	if r.PTZ == "" {
		return nil, "", "", ErrNoPTZ
	}
	dev, err := url.Parse(c.URL)
	if err != nil {
		return nil, "", "", err
	}
	return &soap{f: f, c: c, dev: dev}, r.PTZ, r.Profile, nil
}

// PTZMove starts a continuous move for d (the device is told the same limit).
func (f *Fetcher) PTZMove(ctx context.Context, c Camera, v Velocity, d time.Duration) error {
	if err := v.Valid(); err != nil {
		return err
	}
	if d < MinMove || d > MaxMove {
		return fmt.Errorf("a move lasts between %s and %s", MinMove, MaxMove)
	}
	cl, to, prof, err := f.ptzService(ctx, c)
	if err != nil {
		return err
	}
	secs := math.Ceil(d.Seconds())
	req := fmt.Sprintf(`<tptz:ContinuousMove xmlns:tptz="%s" xmlns:tt="%s"><tptz:ProfileToken>%s</tptz:ProfileToken><tptz:Velocity><tt:PanTilt x="%.3f" y="%.3f"/><tt:Zoom x="%.3f"/></tptz:Velocity><tptz:Timeout>PT%.0fS</tptz:Timeout></tptz:ContinuousMove>`,
		nsPTZ, nsSchem, prof, v.Pan, v.Tilt, v.Zoom, secs)
	return cl.call(ctx, to, req, nil)
}

// PTZStop stops pan, tilt and zoom.
func (f *Fetcher) PTZStop(ctx context.Context, c Camera) error {
	cl, to, prof, err := f.ptzService(ctx, c)
	if err != nil {
		return err
	}
	req := fmt.Sprintf(`<tptz:Stop xmlns:tptz="%s"><tptz:ProfileToken>%s</tptz:ProfileToken><tptz:PanTilt>true</tptz:PanTilt><tptz:Zoom>true</tptz:Zoom></tptz:Stop>`, nsPTZ, prof)
	return cl.call(ctx, to, req, nil)
}

// PTZPresets lists the device's presets: at most MaxPresets, names cleaned of
// control characters and cut short (they are the device's text).
func (f *Fetcher) PTZPresets(ctx context.Context, c Camera) ([]Preset, error) {
	cl, to, prof, err := f.ptzService(ctx, c)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Presets []struct {
			Token string `xml:"token,attr"`
			Name  string `xml:"Name"`
		} `xml:"Body>GetPresetsResponse>Preset"`
	}
	req := fmt.Sprintf(`<tptz:GetPresets xmlns:tptz="%s"><tptz:ProfileToken>%s</tptz:ProfileToken></tptz:GetPresets>`, nsPTZ, prof)
	if err := cl.call(ctx, to, req, &resp); err != nil {
		return nil, err
	}
	out := []Preset{}
	for _, p := range resp.Presets {
		if !validToken(p.Token) || len(out) >= MaxPresets {
			continue
		}
		name := cleanText(p.Name, 64)
		if name == "" {
			name = p.Token
		}
		out = append(out, Preset{Token: p.Token, Name: name})
	}
	return out, nil
}

// PTZGoto moves to a preset the device lists: a token the device did not offer is
// refused here, so what a browser sends is never put in a request unchecked.
func (f *Fetcher) PTZGoto(ctx context.Context, c Camera, token string) error {
	ps, err := f.PTZPresets(ctx, c)
	if err != nil {
		return err
	}
	found := false
	for _, p := range ps {
		if p.Token == token {
			found = true
		}
	}
	if !found {
		return errors.New("the camera has no such preset")
	}
	cl, to, prof, err := f.ptzService(ctx, c)
	if err != nil {
		return err
	}
	req := fmt.Sprintf(`<tptz:GotoPreset xmlns:tptz="%s"><tptz:ProfileToken>%s</tptz:ProfileToken><tptz:PresetToken>%s</tptz:PresetToken></tptz:GotoPreset>`, nsPTZ, prof, token)
	return cl.call(ctx, to, req, nil)
}
