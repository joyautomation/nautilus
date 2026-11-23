//go:build !js

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/joyautomation/nautilus/camera"
)

// Pan, tilt and zoom (#724 slice 4b):
//
//	GET  /api/cameras/{id}/ptz   {presets: [{token, name}]}   (the camera permission)
//	POST /api/cameras/{id}/ptz   {action: move|stop|preset, ...}  (the ptz permission, elevated, audited)
//
// A move is bounded (at most camera.MaxMove): the browser sends one move for as
// long as a button is held (or a short nudge) and a stop when it is let go, and the
// controller stops the camera itself when the time is up, so a closed tab or a lost
// link never leaves one moving. The journal gets an entry for each request: a move
// is "camera.ptz" at its start and a stop at its end.

// ptzMinGap is the least time between two requests for one camera: a loop in a
// script does not become a stream of SOAP calls at the device.
const ptzMinGap = 100 * time.Millisecond

type ptzState struct {
	mu    sync.Mutex
	timer *time.Timer
	last  time.Time
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

// stopMoves stops every camera that is moving (the controller is going down).
func (co *CameraOptions) stopMoves() {
	co.mu.Lock()
	var st []*ptzState
	ids := []string{}
	for id, s := range co.ptz {
		st = append(st, s)
		ids = append(ids, id)
	}
	co.mu.Unlock()
	for i, s := range st {
		s.mu.Lock()
		active := s.timer != nil
		if s.timer != nil {
			s.timer.Stop()
			s.timer = nil
		}
		s.mu.Unlock()
		if !active {
			continue
		}
		for _, c := range co.Cameras {
			if c.ID == ids[i] {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				_ = co.Fetcher.PTZStop(ctx, c)
				cancel()
			}
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
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	ps, err := a.co.Fetcher.PTZPresets(ctx, c)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"presets": ps})
}

func readPTZ(body []byte) (ptzRequest, error) {
	var req ptzRequest
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return req, errors.New(`the body is JSON: {"action": "move", "x": 0.5, "y": 0, "zoom": 0, "ms": 500}, {"action": "stop"} or {"action": "preset", "preset": "<token>"}`)
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

func (a *cameraAPI) ptz(w http.ResponseWriter, r *http.Request) {
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
	st := a.ptzOf(c.ID)
	st.mu.Lock()
	defer st.mu.Unlock()
	if req.Action != "stop" && time.Since(st.last) < ptzMinGap {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too fast: one move at a time"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	f := a.co.Fetcher
	stopTimer := func() {
		if st.timer != nil {
			st.timer.Stop()
			st.timer = nil
		}
	}
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
		stopTimer()
		if err := f.PTZMove(ctx, c, v, d); err != nil {
			a.ptzFailed(w, err)
			return
		}
		st.last = time.Now()
		// the controller ends the move itself: the browser may be gone
		st.timer = time.AfterFunc(d, func() {
			st.mu.Lock()
			st.timer = nil
			st.mu.Unlock()
			sctx, scancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer scancel()
			_ = f.PTZStop(sctx, c)
		})
	case "stop":
		stopTimer()
		if err := f.PTZStop(ctx, c); err != nil {
			a.ptzFailed(w, err)
			return
		}
	case "preset":
		stopTimer()
		if err := f.PTZGoto(ctx, c, req.Preset); err != nil {
			a.ptzFailed(w, err)
			return
		}
		st.last = time.Now()
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": `action is "move", "stop" or "preset"`})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *cameraAPI) ptzFailed(w http.ResponseWriter, err error) {
	code := http.StatusBadGateway
	if errors.Is(err, camera.ErrNoPTZ) {
		code = http.StatusNotFound
	}
	writeJSON(w, code, map[string]string{"error": err.Error()})
}
