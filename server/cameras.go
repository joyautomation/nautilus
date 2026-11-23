//go:build !js

package server

import (
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/joyautomation/nautilus/audit"
	"github.com/joyautomation/nautilus/camera"
)

// Cameras (#724):
//
//	GET /api/cameras                list the cameras the person may view
//	GET /api/cameras/{id}/snapshot  a still (image/jpeg)
//	GET /api/cameras/{id}/stream    live motion-JPEG (multipart/x-mixed-replace)
//
// The browser names a camera by id and never by address: the controller
// fetches only the URLs of the manifest's rows, with the camera's credentials,
// which no response carries. Who may view a camera is the read permission on
// the whole controller, as for the live values it sits beside: read is not
// area-scoped in the auth model, so neither is a camera (a permission to view
// cameras per area is a decision for the plant's owner, #724). A live stream is journalled
// (camera.view) when it starts and when it ends, with the bytes it carried; a
// still is not, since the asset page asks for one every few seconds. Live
// viewers are capped per camera and across the controller (a slow radio link
// and a small core both run out).
//
// The camera package and these handlers are in files no browser build
// compiles; cmd/naut mounts them with Install, so the playground carries none
// of it.

// CameraOptions turn the cameras on.
type CameraOptions struct {
	Cameras []camera.Camera
	// Fetcher gets stills and relays streams, holding the credentials.
	Fetcher *camera.Fetcher
	// Lines are the lines of text a virtual camera draws, from its asset's
	// live values.
	Lines func(c camera.Camera) []string

	limit *camera.Limiter
	mu    sync.Mutex
	meter map[string]*camera.Meter
}

// Install adds the cameras to the server's options: its routes.
func (co *CameraOptions) Install(o *Options) {
	o.Mounts = append(o.Mounts, co.mount)
}

type cameraAPI struct {
	s  *Server
	co *CameraOptions
}

func (co *CameraOptions) mount(s *Server, mux *http.ServeMux) {
	a := &cameraAPI{s: s, co: co}
	co.mu.Lock()
	if co.limit == nil {
		co.limit = camera.NewLimiter(camera.MaxLive)
	}
	if co.meter == nil {
		co.meter = map[string]*camera.Meter{}
	}
	co.mu.Unlock()
	if co.Fetcher == nil {
		co.Fetcher = &camera.Fetcher{}
	}
	mux.HandleFunc("GET /api/cameras", a.list)
	mux.HandleFunc("GET /api/cameras/{id}/snapshot", a.snapshot)
	mux.HandleFunc("GET /api/cameras/{id}/stream", a.stream)
}

// cameraView is a camera as the browser is told of it: no address, no secret.
type cameraView struct {
	ID         string `json:"id"`
	Label      string `json:"label"`
	Asset      string `json:"asset"`
	Kind       string `json:"kind"`
	Interval   int    `json:"interval"`
	MaxViewers int    `json:"maxViewers"`
	Live       bool   `json:"live"`
	Snapshot   bool   `json:"snapshot"`
	Watching   int    `json:"watching"`
	// Bps is what the live relay is carrying now, for the page to show what a
	// stream costs a slow link; 0 when nobody is watching.
	Bps int64 `json:"bps"`
}

func (a *cameraAPI) find(id string) (camera.Camera, bool) {
	for _, c := range a.co.Cameras {
		if c.ID == id {
			return c, true
		}
	}
	return camera.Camera{}, false
}

func (a *cameraAPI) list(w http.ResponseWriter, r *http.Request) {
	out := []cameraView{}
	for _, c := range a.co.Cameras {
		v := cameraView{ID: c.ID, Label: c.Label, Asset: c.Asset, Kind: c.Kind, Interval: c.Interval(), MaxViewers: c.Viewers(),
			Live: c.Live(), Snapshot: c.HasSnapshot(), Watching: a.co.limit.Watching(c.ID)}
		if v.Watching > 0 {
			v.Bps = a.meterOf(c.ID).BitsPerSecond()
		}
		out = append(out, v)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"cameras": out})
}

func (a *cameraAPI) meterOf(id string) *camera.Meter {
	a.co.mu.Lock()
	defer a.co.mu.Unlock()
	m := a.co.meter[id]
	if m == nil {
		m = &camera.Meter{}
		a.co.meter[id] = m
	}
	return m
}

// camera answers for the request's camera, or says there is none (and reports
// false). The read gate in front of every API read has already refused a
// person who may not read.
func (a *cameraAPI) camera(w http.ResponseWriter, r *http.Request) (camera.Camera, bool) {
	c, ok := a.find(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such camera"})
		return camera.Camera{}, false
	}
	return c, true
}

func (a *cameraAPI) snapshot(w http.ResponseWriter, r *http.Request) {
	c, ok := a.camera(w, r)
	if !ok {
		return
	}
	if !c.HasSnapshot() {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "this camera has no snapshot source: name a snapshot-url in its row"})
		return
	}
	var lines []string
	if c.Kind == camera.KindVirtual && a.co.Lines != nil {
		lines = a.co.Lines(c)
	}
	var b []byte
	var err error
	if c.Kind == camera.KindVirtual {
		b = camera.VirtualFrame(c.Label, lines, time.Now())
	} else {
		b, err = a.co.Fetcher.Snapshot(r.Context(), c)
	}
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Write(b)
}

func (a *cameraAPI) stream(w http.ResponseWriter, r *http.Request) {
	c, ok := a.camera(w, r)
	if !ok {
		return
	}
	if !c.Live() {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "live video of a " + c.Kind + " camera is not built in yet: its snapshots work"})
		return
	}
	release, err := a.co.limit.Acquire(c)
	if err != nil {
		code := http.StatusServiceUnavailable
		if errors.Is(err, camera.ErrBusy) {
			code = http.StatusTooManyRequests
		}
		writeJSON(w, code, map[string]string{"error": err.Error()})
		return
	}
	defer release()
	m := a.meterOf(c.ID)
	if a.co.limit.Watching(c.ID) == 1 {
		m.Reset() // the first viewer: the rate is this stream's
	}
	fl, _ := w.(http.Flusher)
	flush := func() {
		if fl != nil {
			fl.Flush()
		}
	}
	who, from, sess := a.s.principalOf(r), r.RemoteAddr, a.s.sessionOfEntry(r)
	started := time.Now()
	// the answer begins with the first frame, so a camera that refuses the
	// controller is a 502 with a reason, not an empty stream
	out := &streamWriter{w: w, begin: func() {
		w.Header().Set("Content-Type", camera.MJPEGContentType)
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusOK)
		a.s.audit.record(audit.Entry{Action: "camera.view", Target: c.ID, User: who, Source: audit.SourceHMI, Result: audit.ResultOK, From: from, Session: sess,
			Detail: map[string]any{"event": "start", "asset": c.Asset}})
	}}
	var lines func() []string
	if c.Kind == camera.KindVirtual && a.co.Lines != nil {
		lines = func() []string { return a.co.Lines(c) }
	}
	rerr := a.co.Fetcher.Relay(r.Context(), c, lines, out, flush, m)
	if !out.began {
		a.s.audit.record(audit.Entry{Action: "camera.view", Target: c.ID, User: who, Source: audit.SourceHMI, Result: audit.ResultError, From: from, Session: sess,
			Detail: map[string]any{"event": "start", "asset": c.Asset, "error": errText(rerr)}})
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": errText(rerr)})
		return
	}
	d := map[string]any{"event": "stop", "asset": c.Asset, "seconds": int(time.Since(started).Seconds()), "bytes": out.n}
	if rerr != nil {
		d["ended"] = rerr.Error()
	}
	a.s.audit.record(audit.Entry{Action: "camera.view", Target: c.ID, User: who, Source: audit.SourceHMI, Result: audit.ResultOK, From: from, Session: sess, Detail: d})
}

func errText(err error) string {
	if err == nil {
		return "the camera sent no picture"
	}
	return err.Error()
}

// streamWriter starts the response at the first byte and counts what it carries.
type streamWriter struct {
	w     http.ResponseWriter
	begin func()
	began bool
	n     int64
}

func (s *streamWriter) Write(p []byte) (int, error) {
	if !s.began {
		s.began = true
		s.begin()
	}
	n, err := s.w.Write(p)
	s.n += int64(n)
	return n, err
}
