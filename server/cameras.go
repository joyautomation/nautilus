//go:build !js

package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/joyautomation/nautilus/alarm"
	"github.com/joyautomation/nautilus/audit"
	"github.com/joyautomation/nautilus/auth"
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
// which no response carries. Viewing needs read (the gate in front of every API
// read) and the camera permission: video of a plant floor shows people, so a
// contractor or a remote viewer may be given values and not the floor. Both are
// whole-controller: read is not area-scoped in the auth model and the camera
// permission is not either (per-area is a bigger question, #724). The list says
// which cameras exist and whether the person may view them, so the page can say
// "no permission" rather than show a broken image. A live stream is journalled
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
	snaps map[string]*snapCache

	// Alarm pictures (#724 slice 2): Snaps keeps them, Equipment names an
	// alarm's equipment (an alarm id to its key), Leads says this controller
	// leads (nil: it does).
	// LiveBudgetMB is the memory all live HLS streams may hold together (0: 64 MB).
	LiveBudgetMB int
	Snaps        *camera.SnapStore
	Equipment    func(alarmID string) string
	Leads        func() bool
	snapSem      chan struct{}
	semOnce      sync.Once
	inflight     map[string][]alarm.Event
	snapSeen     map[string]time.Time
	ptz          map[string]*ptzState
	snapSwept    time.Time
	hub          *camera.HLSHub
	leaseMu      sync.Mutex
	leases       map[string]*hlsLease
	leasing      bool
	lowWarned    atomic.Bool
	api          *cameraAPI
}

// Install adds the cameras to the server's options: its routes.
func (co *CameraOptions) Install(o *Options) {
	if co.Fetcher == nil {
		co.Fetcher = &camera.Fetcher{}
	}
	o.Mounts = append(o.Mounts, co.mount)
	o.ShownPermissions = append(o.ShownPermissions, auth.Camera)
}

// Stop closes the RTSP sessions the cameras have open (the controller is going down).
func (co *CameraOptions) Stop() {
	co.mu.Lock()
	h := co.hub
	co.mu.Unlock()
	if h != nil {
		h.Stop()
	}
	co.stopMoves()
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
	if co.snaps == nil {
		co.snaps = map[string]*snapCache{}
	}
	if co.hub == nil {
		co.hub = camera.NewHLSHub(co.Fetcher, camera.NewMemBudget(int64(co.LiveBudgetMB)<<20))
	}
	co.api = a
	co.mu.Unlock()
	a.mountSnaps(mux)
	mux.HandleFunc("GET /api/cameras/{id}/hls/{name}", a.hls)
	mux.HandleFunc("GET /api/cameras", a.list)
	mux.HandleFunc("GET /api/cameras/{id}/snapshot", a.snapshot)
	mux.HandleFunc("GET /api/cameras/{id}/stream", a.stream)
	mux.HandleFunc("GET /api/cameras/{id}/ptz", a.ptzPresets)
	mux.Handle("POST /api/cameras/{id}/ptz", a.s.audited("camera.ptz", a.ptzTarget, a.ptz))
}

// cameraView is a camera as the browser is told of it: no address, no secret.
type cameraView struct {
	ID         string `json:"id"`
	Label      string `json:"label"`
	Asset      string `json:"asset"`
	Kind       string `json:"kind"`
	Interval   int    `json:"interval"`
	MaxViewers int    `json:"maxViewers"`
	// View says whether the person may view cameras: false, and the page says
	// they have no permission instead of asking for a picture.
	View bool `json:"view"`
	Live bool `json:"live"`
	// Hls says live video is HLS: play /api/cameras/{id}/hls/index.m3u8 with an HLS
	// player, not motion-JPEG.
	Hls bool `json:"hls"`
	// Ptz says the camera pans and tilts: the page shows its pad to a person who holds
	// the ptz permission.
	Ptz      bool `json:"ptz"`
	Snapshot bool `json:"snapshot"`
	Watching int  `json:"watching"`
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

// noView is what a person without the camera permission is told.
const noView = "no permission to view cameras"

// mayView reports whether the request's principal holds the camera permission;
// always without an auth: block.
func (a *cameraAPI) mayView(r *http.Request) bool { return a.mayViewActor(a.s.actorOf(r)) }

func (a *cameraAPI) mayViewActor(act actor) bool {
	if a.s.policy == nil {
		return true
	}
	return a.s.policy.Can(act.pr, auth.Camera, auth.All).Allowed
}

func (a *cameraAPI) list(w http.ResponseWriter, r *http.Request) {
	out := []cameraView{}
	may := a.mayView(r)
	for _, c := range a.co.Cameras {
		v := cameraView{View: may, ID: c.ID, Label: c.Label, Asset: c.Asset, Kind: c.Kind, Interval: c.Interval(), MaxViewers: c.Viewers(),
			Live: c.Live(), Hls: c.HLS(), Ptz: c.PTZ && c.Kind == camera.KindONVIF, Snapshot: c.HasSnapshot(), Watching: a.co.limit.Watching(c.ID)}
		if may && v.Watching > 0 {
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
	if !a.mayView(r) {
		// a refused live view is journalled; the still the page asks for every few
		// seconds is not (a refusal flood is coalesced by the recorder either way)
		if strings.HasSuffix(r.URL.Path, "/stream") || strings.HasSuffix(r.URL.Path, "/hls/index.m3u8") {
			a.s.audit.record(audit.Entry{Action: "camera.view", Target: c.ID, User: a.s.principalOf(r), Source: audit.SourceHMI, Result: audit.ResultRefused,
				From: a.s.clientAddr(r), Session: a.s.sessionOfEntry(r), Detail: map[string]any{"event": "start", "asset": c.Asset, "reason": noView, "class": audit.ClassAuth}})
		}
		writeJSON(w, http.StatusForbidden, map[string]string{"error": noView})
		return camera.Camera{}, false
	}
	return c, true
}

// A still is fetched at most once a second per camera, whoever asks: the page
// asks every few seconds, and a reader who polls in a loop must not turn the
// camera into live video the caps do not see, nor open a session at the camera
// for each request. A camera that did not answer is not asked again for two.
const (
	snapshotTTL    = time.Second
	snapshotErrTTL = 2 * time.Second
)

type snapCache struct {
	mu  sync.Mutex
	at  time.Time
	b   []byte
	err error
}

func (a *cameraAPI) cached(c camera.Camera) ([]byte, error) {
	a.co.mu.Lock()
	sc := a.co.snaps[c.ID]
	if sc == nil {
		sc = &snapCache{}
		a.co.snaps[c.ID] = sc
	}
	a.co.mu.Unlock()
	sc.mu.Lock()
	defer sc.mu.Unlock()
	ttl := snapshotTTL
	if sc.err != nil {
		ttl = snapshotErrTTL
	}
	if !sc.at.IsZero() && time.Since(sc.at) < ttl {
		return sc.b, sc.err
	}
	// not the request's context: the person who asked first leaving must not
	// fail the ones waiting on the same fetch (the fetch has its own timeout)
	sc.b, sc.err = a.co.Fetcher.Snapshot(context.Background(), c)
	sc.at = time.Now()
	return sc.b, sc.err
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
	var b []byte
	var err error
	if c.Kind == camera.KindVirtual {
		var lines []string
		if a.co.Lines != nil {
			lines = a.co.Lines(c)
		}
		b = camera.VirtualFrame(c.Label, lines, time.Now())
	} else {
		b, err = a.cached(c)
	}
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	pictureHeaders(w, "image/jpeg")
	w.Write(b)
}

// pictureHeaders are what a picture from a camera is answered with: never
// cached, never sniffed, and sandboxed should a browser be made to render it.
func pictureHeaders(w http.ResponseWriter, contentType string) {
	h := w.Header()
	h.Set("Content-Type", contentType)
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "sandbox; default-src 'none'")
}

// maxLiveView is how long one live view runs: the page asks again if the person
// is still watching, and a stalled or forgotten tab does not hold a place for
// ever (a variable so a test need not wait).
var maxLiveView = 15 * time.Minute

// liveWriteTimeout bounds one write to the browser: a client that stops reading
// does not hold its place.
const liveWriteTimeout = 10 * time.Second

func (a *cameraAPI) stream(w http.ResponseWriter, r *http.Request) {
	c, ok := a.camera(w, r)
	if !ok {
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "a live stream is a GET"})
		return
	}
	if c.HLS() {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "this camera's live video is HLS: /api/cameras/" + c.ID + "/hls/index.m3u8"})
		return
	}
	if !c.Live() {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "live video of a " + c.Kind + " camera is not built in yet: its snapshots work"})
		return
	}
	who := a.s.principalOf(r)
	// without sign-in everyone is "anonymous": the per-person cap then counts the
	// browser's address, or two live streams would be every viewer's total
	holder := who
	if who == "anonymous" {
		holder = who + "@" + a.s.clientAddr(r)
	}
	release, err := a.co.limit.Acquire(c, holder)
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
	// The view ends when its time is up, when the viewer leaves, and when the
	// person may no longer read or view cameras (signed out, disabled, a role
	// changed): checked every second, as /api/stream is.
	ctx, cancel := context.WithTimeout(r.Context(), maxLiveView)
	defer cancel()
	var revoked atomic.Bool
	go func() {
		t := time.NewTicker(streamRecheck)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if !a.stillMay(r) {
					revoked.Store(true)
					cancel()
					return
				}
			}
		}
	}()
	rc := http.NewResponseController(w)
	flush := func() { _ = rc.Flush() }
	// A virtual camera shows nothing of the plant, so its views are not journalled
	// (a demo opened and closed in a loop would otherwise fill the journal).
	journal := c.Kind != camera.KindVirtual
	from, sess := a.s.clientAddr(r), a.s.sessionOfEntry(r)
	started := time.Now()
	// the answer begins with the first frame, so a camera that refuses the
	// controller is a 502 with a reason, not an empty stream
	out := &streamWriter{w: w, rc: rc, begin: func() {
		pictureHeaders(w, camera.MJPEGContentType)
		w.WriteHeader(http.StatusOK)
		if journal {
			a.s.audit.record(audit.Entry{Action: "camera.view", Target: c.ID, User: who, Source: audit.SourceHMI, Result: audit.ResultOK, From: from, Session: sess,
				Detail: map[string]any{"event": "start", "asset": c.Asset}})
		}
	}}
	var lines func() []string
	if c.Kind == camera.KindVirtual && a.co.Lines != nil {
		lines = func() []string { return a.co.Lines(c) }
	}
	rerr := a.co.Fetcher.Relay(ctx, c, lines, out, flush, m)
	if !out.began {
		if journal {
			a.s.audit.record(audit.Entry{Action: "camera.view", Target: c.ID, User: who, Source: audit.SourceHMI, Result: audit.ResultError, From: from, Session: sess,
				Detail: map[string]any{"event": "start", "asset": c.Asset, "error": errText(rerr)}})
		}
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": errText(rerr)})
		return
	}
	if !journal {
		return
	}
	d := map[string]any{"event": "stop", "asset": c.Asset, "seconds": int(time.Since(started).Seconds()), "bytes": out.n}
	switch {
	case revoked.Load():
		d["ended"] = "the person may no longer view this camera"
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		d["ended"] = "the live view reached its time limit"
	case rerr != nil:
		d["ended"] = rerr.Error()
	}
	a.s.audit.record(audit.Entry{Action: "camera.view", Target: c.ID, User: who, Source: audit.SourceHMI, Result: audit.ResultOK, From: from, Session: sess, Detail: d})
}

// stillMay reports whether the request's person may still read and view
// cameras, resolved afresh: a session's expiry, a revoked or disabled user, a
// role changed to one without the camera permission.
func (a *cameraAPI) stillMay(r *http.Request) bool {
	return a.s.streamMayRead(r) && a.mayViewActor(a.s.resolveActor(r))
}

func errText(err error) string {
	if err == nil {
		return "the camera sent no picture"
	}
	return err.Error()
}

// streamWriter starts the response at the first byte, counts what it carries and
// bounds each write.
type streamWriter struct {
	w     http.ResponseWriter
	rc    *http.ResponseController
	begin func()
	began bool
	n     int64
}

func (s *streamWriter) Write(p []byte) (int, error) {
	if !s.began {
		s.began = true
		s.begin()
	}
	_ = s.rc.SetWriteDeadline(time.Now().Add(liveWriteTimeout))
	n, err := s.w.Write(p)
	s.n += int64(n)
	return n, err
}
