//go:build !js

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/joyautomation/nautilus/camera"
)

// Pan, tilt and zoom (#724 slice 4b):
//
//	GET  /api/cameras/{id}/ptz   {presets: [{token, name}]}   (the camera permission)
//	POST /api/cameras/{id}/ptz   {action: move|preset, ...}  (the ptz permission, elevated, audited)
//	POST /api/cameras/{id}/ptz/stop                          (the camera permission, audited: a way back)
//
// A move is bounded (at most camera.MaxMove): the browser sends one move for as
// long as a button is held (or a short nudge) and a stop when it is let go, and the
// controller stops the camera itself when the time is up, so a closed tab or a lost
// link never leaves one moving. A stop needs no fresh password and no ptz: whoever
// may see the camera may stop it, as clearing a force is a way back. Each command
// carries a generation: a stop (or a newer move) makes every older command stale,
// so a move that reaches the device after a stop that overtook it is stopped again,
// and a timer that belongs to an older move does nothing.

// ptzMinGap is the least time between two moves of one camera: a loop in a
// script does not become a stream of SOAP calls at the device.
const ptzMinGap = 100 * time.Millisecond

// ptzPresetsTTL is how long a camera's preset list is kept: a page asks once, a
// script asking in a loop is one SOAP call in this time.
const ptzPresetsTTL = 5 * time.Second

// ptzBound is how long the controller works at the device on a command whose
// answer matters to safety (a stop), whatever the browser does.
const ptzBound = 10 * time.Second

type ptzState struct {
	mu     sync.Mutex // short: guards the fields, never held across a call to the device
	gen    uint64     // the newest command's number; older ones are stale
	timer  *time.Timer
	moving bool // a move was sent and no stop has been confirmed since
	last   time.Time
	dev    sync.Mutex // one move or preset at a time at the device (a stop does not take it)
	// presets
	pmu     sync.Mutex
	presets []camera.Preset
	pat     time.Time
}

type ptzRequest struct {
	Action string  `json:"action"`
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Zoom   float64 `json:"zoom"`
	Ms     int     `json:"ms"`
	Preset string  `json:"preset"`
}

func (a *cameraAPI) ptzOf(id string) *ptzState {
	a.co.mu.Lock()
	defer a.co.mu.Unlock()
	if a.co.ptz == nil {
		a.co.ptz = map[string]*ptzState{}
	}
	st := a.co.ptz[id]
	if st == nil {
		st = &ptzState{}
		a.co.ptz[id] = st
	}
	return st
}

// stopNow sends a stop on its own bounded context (the browser that asked may be
// gone) and tries twice: a camera left moving is the failure that matters.
func stopNow(f *camera.Fetcher, c camera.Camera) error {
	var err error
	for try := 0; try < 2; try++ {
		ctx, cancel := context.WithTimeout(context.Background(), ptzBound)
		err = f.PTZStop(ctx, c)
		cancel()
		if err == nil {
			return nil
		}
	}
	slog.Error("camera: a stop could not be sent: the camera may still be moving", "camera", c.ID, "error", err)
	return err
}

// arm makes the controller stop the camera in d unless a newer command has
// come: the generation says whether this timer still owns the camera.
func (st *ptzState) arm(f *camera.Fetcher, c camera.Camera, d time.Duration, gen uint64) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.gen != gen {
		return
	}
	if st.timer != nil {
		st.timer.Stop()
	}
	st.timer = time.AfterFunc(d, func() {
		st.mu.Lock()
		if st.gen != gen {
			st.mu.Unlock()
			return
		}
		st.timer = nil
		st.mu.Unlock()
		if err := stopNow(f, c); err != nil {
			st.arm(f, c, time.Second, gen) // try again: it is still ours
			return
		}
		st.mu.Lock()
		if st.gen == gen {
			st.moving = false
		}
		st.mu.Unlock()
	})
}

// next starts a new command: everything older is stale, its timer is dropped.
func (st *ptzState) next() uint64 {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.gen++
	if st.timer != nil {
		st.timer.Stop()
		st.timer = nil
	}
	return st.gen
}

// stopMoves stops every camera that may be moving (the controller is going down).
func (co *CameraOptions) stopMoves() {
	co.mu.Lock()
	byID := map[string]*ptzState{}
	for id, s := range co.ptz {
		byID[id] = s
	}
	co.mu.Unlock()
	for _, c := range co.Cameras {
		st := byID[c.ID]
		if st == nil {
			continue
		}
		st.mu.Lock()
		moving := st.moving
		st.gen++
		if st.timer != nil {
			st.timer.Stop()
			st.timer = nil
		}
		st.mu.Unlock()
		if moving {
			_ = stopNow(co.Fetcher, c)
		}
	}
}

func (a *cameraAPI) ptzCamera(w http.ResponseWriter, r *http.Request) (camera.Camera, bool) {
	c, ok := a.camera(w, r)
	if !ok {
		return c, false
	}
	if !c.PTZ || c.Kind != camera.KindONVIF {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "this camera has no pan and tilt (the row needs kind: onvif and ptz: true)"})
		return c, false
	}
	return c, true
}

func (a *cameraAPI) ptzPresets(w http.ResponseWriter, r *http.Request) {
	c, ok := a.ptzCamera(w, r)
	if !ok {
		return
	}
	st := a.ptzOf(c.ID)
	st.pmu.Lock()
	defer st.pmu.Unlock()
	if st.presets == nil || time.Since(st.pat) > ptzPresetsTTL {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		ps, err := a.co.Fetcher.PTZPresets(ctx, c)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		st.presets, st.pat = ps, time.Now()
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"presets": st.presets})
}

func readPTZ(body []byte) (ptzRequest, error) {
	var req ptzRequest
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return req, errors.New(`the body is JSON: {"action": "move", "x": 0.5, "y": 0, "zoom": 0, "ms": 500} or {"action": "preset", "preset": "<token>"}`)
	}
	return req, nil
}

func (a *cameraAPI) ptzTarget(r *http.Request, body []byte) auditTarget {
	req, _ := readPTZ(body)
	d := map[string]any{"action": req.Action}
	switch req.Action {
	case "move":
		d["x"], d["y"], d["zoom"], d["ms"] = req.X, req.Y, req.Zoom, req.Ms
	case "preset":
		d["preset"] = req.Preset
	}
	return auditTarget{target: r.PathValue("id"), detail: d}
}

func (a *cameraAPI) ptzStopTarget(r *http.Request, _ []byte) auditTarget {
	return auditTarget{target: r.PathValue("id"), detail: map[string]any{"action": "stop"}}
}

// ptz moves a camera or sends it to a preset.
func (a *cameraAPI) ptz(w http.ResponseWriter, r *http.Request) {
	// the write policy first: a request it refuses has had its body emptied, and
	// must be told 401 and not that its JSON is wrong
	if code, msg := a.s.authorizeWrite(r); code != 0 {
		writeJSON(w, code, map[string]string{"error": msg})
		return
	}
	c, ok := a.ptzCamera(w, r)
	if !ok {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<10))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the body is JSON, at most 1 KB"})
		return
	}
	req, err := readPTZ(body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	f := a.co.Fetcher
	st := a.ptzOf(c.ID)
	switch req.Action {
	case "move":
		d := time.Duration(req.Ms) * time.Millisecond
		v := camera.Velocity{Pan: req.X, Tilt: req.Y, Zoom: req.Zoom}
		if err := v.Valid(); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if d < camera.MinMove || d > camera.MaxMove {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "ms is the move's length: between 100 and 10000"})
			return
		}
		st.mu.Lock()
		tooFast := time.Since(st.last) < ptzMinGap
		if !tooFast {
			st.last = time.Now()
		}
		st.mu.Unlock()
		if tooFast {
			writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too fast: one move at a time"})
			return
		}
		gen := st.next()
		st.mu.Lock()
		st.moving = true
		st.mu.Unlock()
		st.dev.Lock()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		err := f.PTZMove(ctx, c, v, d)
		cancel()
		st.dev.Unlock()
		// whatever happened at the device (an answer lost on the way is a camera that may
		// be moving), the controller owns the stop: if this is still the newest command
		// it stops the camera when the time is up, and if a stop overtook it, now
		st.mu.Lock()
		stale := st.gen != gen
		st.mu.Unlock()
		if stale {
			_ = stopNow(f, c)
		} else {
			st.arm(f, c, d, gen)
		}
		if err != nil {
			a.ptzFailed(w, err)
			return
		}
	case "preset":
		st.next()
		st.dev.Lock()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		err := f.PTZGoto(ctx, c, req.Preset)
		cancel()
		st.dev.Unlock()
		if err != nil {
			// this command replaced an older move's timer: a camera left moving by that move is
			// stopped now, not left to the device's own limit
			_ = stopNow(f, c)
			st.mu.Lock()
			st.moving = false
			st.mu.Unlock()
			a.ptzFailed(w, err)
			return
		}
		st.mu.Lock()
		st.moving = false
		st.mu.Unlock()
	case "stop":
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a stop is POST /api/cameras/" + c.ID + "/ptz/stop"})
		return
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": `action is "move" or "preset"`})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ptzStop stops a camera. It never waits for a move at the device and is not
// rate-limited: a stop is the way back.
func (a *cameraAPI) ptzStop(w http.ResponseWriter, r *http.Request) {
	if code, msg := a.s.authorizeWrite(r); code != 0 {
		writeJSON(w, code, map[string]string{"error": msg})
		return
	}
	c, ok := a.ptzCamera(w, r)
	if !ok {
		return
	}
	st := a.ptzOf(c.ID)
	gen := st.next() // every older command is stale, a move still on its way is stopped when it lands
	if err := stopNow(a.co.Fetcher, c); err != nil {
		st.arm(a.co.Fetcher, c, time.Second, gen) // the controller keeps trying
		a.ptzFailed(w, err)
		return
	}
	st.mu.Lock()
	if st.gen == gen {
		st.moving = false
	}
	st.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *cameraAPI) ptzFailed(w http.ResponseWriter, err error) {
	code := http.StatusBadGateway
	if errors.Is(err, camera.ErrNoPTZ) {
		code = http.StatusNotFound
	}
	writeJSON(w, code, map[string]string{"error": err.Error()})
}
