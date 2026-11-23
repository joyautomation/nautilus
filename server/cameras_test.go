package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/auth"
	"github.com/joyautomation/nautilus/camera"
)

// camServer is a server with auth (akhan an engineer; jsmith may read and not
// view cameras; ghost holds nothing) and the cameras given.
func camServer(t *testing.T, cams []camera.Camera, secrets map[string]string) (*Server, map[string]string) {
	t.Helper()
	co := &CameraOptions{Cameras: cams, Fetcher: &camera.Fetcher{Secret: func(n string) (string, bool) { v, ok := secrets[n]; return v, ok }},
		Lines: func(c camera.Camera) []string { return []string{"asset " + c.Asset} }}
	o := Options{}
	co.Install(&o)
	// jsmith's role holds read and not the camera permission: values, not the floor
	srv, toks, _ := patServerSettings(t, newTestRuntime(t), o, auth.Settings{
		Roles: map[string]auth.RoleDef{"blind": {Inherits: auth.Operator, Deny: []string{auth.Camera}}},
		Users: map[string][]auth.Grant{
			"akhan":  {{Role: auth.Engineer}},
			"jsmith": {{Role: "blind"}},
		},
	})
	return srv, toks
}

func camJPEG(t *testing.T) []byte {
	t.Helper()
	return camera.VirtualFrame("x", nil, time.Unix(0, 0))
}

func TestCamerasAreForReadersAndNeverTellAnAddress(t *testing.T) {
	back := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(camJPEG(t)) }))
	defer back.Close()
	srv, toks := camServer(t, []camera.Camera{
		{ID: "bin-top", Label: "Bin top", Asset: "A1", Kind: camera.KindVirtual},
		{ID: "dock", Label: "Dock", Asset: "A2", Kind: camera.KindSnapshot, URL: back.URL + "/s.jpg", Credentials: true},
	}, map[string]string{"camera.dock": "viewer:hunter2"})
	for who, view := range map[string]bool{"akhan": true, "jsmith": false} {
		body := call(t, srv, "GET", "/api/cameras", "", bearer(toks[who])).Body.String()
		if !strings.Contains(body, `"bin-top"`) || !strings.Contains(body, `"dock"`) || strings.Count(body, fmt.Sprintf(`"view":%v`, view)) != 2 {
			t.Errorf("%s's list (view %v): %s", who, view, body)
		}
		for _, secret := range []string{back.URL, "127.0.0.1", "hunter2", "camera.dock", "s.jpg"} {
			if strings.Contains(body, secret) {
				t.Errorf("the list tells %q: %s", secret, body)
			}
		}
	}
	// a person who may not read, or who is not signed in, sees no camera at all
	for name, mod := range map[string]func(*http.Request){"ghost (reads nothing)": bearer(toks["ghost"]), "anonymous": func(*http.Request) {}} {
		for _, p := range []string{"/api/cameras", "/api/cameras/bin-top/snapshot", "/api/cameras/bin-top/stream"} {
			if rec := call(t, srv, "GET", p, "", mod); rec.Code != http.StatusForbidden && rec.Code != http.StatusUnauthorized {
				t.Errorf("%s: GET %s: %d %s, want the read gate's refusal", name, p, rec.Code, rec.Body)
			}
		}
	}
	if rec := call(t, srv, "GET", "/api/cameras/nope/snapshot", "", bearer(toks["akhan"])); rec.Code != http.StatusNotFound {
		t.Errorf("an unknown camera: %d", rec.Code)
	}
}

func TestCameraSnapshotsAreStillsFetchedWithTheControllersCredentials(t *testing.T) {
	var gotUser, gotPass string
	back := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok || p != "hunter2" {
			w.Header().Set("Www-Authenticate", `Basic realm="cam"`)
			w.WriteHeader(401)
			return
		}
		gotUser, gotPass = u, p
		w.Write(camJPEG(t))
	}))
	defer back.Close()
	srv, toks := camServer(t, []camera.Camera{
		{ID: "v", Label: "V", Asset: "A1", Kind: camera.KindVirtual},
		{ID: "dock", Asset: "A2", Kind: camera.KindSnapshot, URL: back.URL, Credentials: true},
		{ID: "bad", Asset: "A2", Kind: camera.KindSnapshot, URL: back.URL, Credentials: true},
		{ID: "down", Asset: "A2", Kind: camera.KindSnapshot, URL: "http://127.0.0.1:1/x"},
		{ID: "r", Asset: "A2", Kind: camera.KindRTSP, URL: "rtsp://10.0.0.1/s"},
	}, map[string]string{"camera.dock": "viewer:hunter2", "camera.bad": "viewer:nope"})
	eng := bearer(toks["akhan"])
	for _, id := range []string{"v", "dock"} {
		rec := call(t, srv, "GET", "/api/cameras/"+id+"/snapshot", "", eng)
		if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/jpeg" || rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s: %d %v", id, rec.Code, rec.Header())
		}
		if _, err := jpeg.Decode(bytes.NewReader(rec.Body.Bytes())); err != nil {
			t.Errorf("%s: not a JPEG: %v", id, err)
		}
	}
	if gotUser != "viewer" || gotPass != "hunter2" {
		t.Errorf("the camera saw %q/%q", gotUser, gotPass)
	}
	for id, want := range map[string]int{"bad": 502, "down": 502, "r": 404} {
		rec := call(t, srv, "GET", "/api/cameras/"+id+"/snapshot", "", eng)
		if rec.Code != want {
			t.Errorf("%s: %d %s, want %d", id, rec.Code, rec.Body, want)
		}
		for _, leak := range []string{"127.0.0.1", "hunter2", "nope", back.URL} {
			if strings.Contains(rec.Body.String(), leak) {
				t.Errorf("%s: the answer tells %q: %s", id, leak, rec.Body)
			}
		}
	}
	if rec := call(t, srv, "GET", "/api/cameras/r/stream", "", eng); rec.Code != http.StatusNotImplemented {
		t.Errorf("live video of an rtsp camera in this step: %d, want 501", rec.Code)
	}
}

// mjpegBackend is a camera that streams a frame every 50 ms until the controller
// hangs up.
func mjpegBackend(t *testing.T) *httptest.Server {
	t.Helper()
	jb := camJPEG(t)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary=cam")
		for {
			fmt.Fprintf(w, "--cam\r\nContent-Type: image/jpeg\r\nContent-Length: %d\r\n\r\n", len(jb))
			w.Write(jb)
			io.WriteString(w, "\r\n")
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
				return
			case <-time.After(50 * time.Millisecond):
			}
		}
	}))
}

// open starts a live stream and returns a reader of its frames and a stop.
func camOpen(t *testing.T, ts *httptest.Server, id, tok string) (*http.Response, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/api/cameras/"+id+"/stream", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	return resp, cancel
}

func TestACameraStreamIsLiveJournalledAndCapped(t *testing.T) {
	back := mjpegBackend(t)
	defer back.Close()
	srv, toks := camServer(t, []camera.Camera{
		{ID: "one", Asset: "A1", Kind: camera.KindMJPEG, URL: back.URL, MaxViewers: 1},
		{ID: "two", Asset: "A1", Kind: camera.KindVirtual},
	}, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, stop := camOpen(t, ts, "one", toks["akhan"])
	defer stop() // ts.Close waits for every stream to end, a failing test included
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "multipart/x-mixed-replace") {
		t.Fatalf("the stream: %d %v", resp.StatusCode, resp.Header)
	}
	br := bufio.NewReader(resp.Body)
	for seen := 0; seen < 2; {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("reading frames: %v", err)
		}
		if strings.HasPrefix(line, "Content-Type: image/jpeg") {
			seen++
		}
	}
	// the list says someone is watching, and what it costs
	var live struct {
		Cameras []struct {
			ID       string `json:"id"`
			Watching int    `json:"watching"`
			Bps      int64  `json:"bps"`
		} `json:"cameras"`
	}
	rec := call(t, srv, "GET", "/api/cameras", "", bearer(toks["akhan"]))
	if err := jsonDecode(rec.Body.Bytes(), &live); err != nil {
		t.Fatal(err)
	}
	for _, c := range live.Cameras {
		if c.ID == "one" && (c.Watching != 1 || c.Bps <= 0) {
			t.Errorf("the list says watching %d, %d bit/s while a stream runs", c.Watching, c.Bps)
		}
	}
	// the camera's own cap: a second viewer is told why
	r2, stop2 := camOpen(t, ts, "one", toks["akhan"])
	if r2.StatusCode == 200 {
		stop2()
		t.Fatal("a second viewer of a one-viewer camera was let in")
	}
	b2, _ := io.ReadAll(r2.Body)
	stop2()
	if r2.StatusCode != http.StatusTooManyRequests || !strings.Contains(string(b2), "max-viewers") {
		t.Errorf("a second viewer of a one-viewer camera: %d %s", r2.StatusCode, b2)
	}
	// the viewer leaves; the place is free again and the stop is journalled
	stop()
	resp.Body.Close()
	deadline := time.Now().Add(3 * time.Second)
	var starts, stops int
	for time.Now().Before(deadline) {
		starts, stops = 0, 0
		for _, e := range srv.audit.ring.Recent() {
			if e.Action != "camera.view" || e.Target != "one" {
				continue
			}
			if e.User != "user:akhan" {
				t.Errorf("the view is journalled by %q", e.User)
			}
			switch e.Detail["event"] {
			case "start":
				starts++
			case "stop":
				stops++
				if b, _ := e.Detail["bytes"].(int64); b <= 0 {
					t.Errorf("the stop entry carries %v bytes", e.Detail["bytes"])
				}
			}
		}
		if starts == 1 && stops == 1 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if starts != 1 || stops != 1 {
		t.Fatalf("journalled %d starts and %d stops, want 1 and 1", starts, stops)
	}
	r3, stop3 := camOpen(t, ts, "one", toks["akhan"])
	defer stop3()
	if r3.StatusCode != 200 {
		t.Errorf("after the first viewer left: %d", r3.StatusCode)
	}
	r3.Body.Close()
}

func TestACameraThatRefusesTheControllerIsA502NotAnEmptyStream(t *testing.T) {
	back := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) }))
	defer back.Close()
	srv, toks := camServer(t, []camera.Camera{{ID: "m", Asset: "A1", Kind: camera.KindMJPEG, URL: back.URL, Credentials: true}}, map[string]string{"camera.m": "viewer:hunter2"})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	resp, stop := camOpen(t, ts, "m", toks["akhan"])
	defer stop()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusBadGateway || !strings.Contains(string(b), "refused the controller's credentials") || strings.Contains(string(b), "hunter2") {
		t.Errorf("%d %s", resp.StatusCode, b)
	}
	found := false
	for _, e := range srv.audit.ring.Recent() {
		if e.Action == "camera.view" && e.Result == "error" {
			found = true
		}
	}
	if !found {
		t.Error("a view that could not start is not journalled")
	}
}

func TestACameraRoutesAreNotMountedWithoutCameras(t *testing.T) {
	srv, toks, _ := patServerWith(t, Options{})
	for _, p := range []string{"/api/cameras", "/api/cameras/x/snapshot", "/api/cameras/x/stream"} {
		rec := call(t, srv, "GET", p, "", bearer(toks["akhan"]))
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "no such API endpoint") {
			t.Errorf("%s: %d %s", p, rec.Code, rec.Body)
		}
	}
}

func jsonDecode(b []byte, v any) error { return json.Unmarshal(b, v) }

// A person who may read and not view cameras is told so, and the controller
// never reaches the camera for them: the list says view false, a still and a live
// stream are 403 with the reason, a refused live view is journalled, and the
// camera was never asked.
func TestAPersonWithoutThePermissionToViewCamerasIsToldAndTheCameraIsNeverAsked(t *testing.T) {
	var hits int
	back := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++; w.Write(camJPEG(t)) }))
	defer back.Close()
	srv, toks := camServer(t, []camera.Camera{
		{ID: "dock", Asset: "A", Kind: camera.KindSnapshot, URL: back.URL},
		{ID: "v", Asset: "A", Kind: camera.KindVirtual},
	}, nil)
	blind, eng := bearer(toks["jsmith"]), bearer(toks["akhan"])
	for _, p := range []string{"/api/cameras/dock/snapshot", "/api/cameras/v/snapshot", "/api/cameras/v/stream"} {
		rec := call(t, srv, "GET", p, "", blind)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "no permission to view cameras") {
			t.Errorf("GET %s without the permission: %d %s", p, rec.Code, rec.Body)
		}
	}
	if hits != 0 {
		t.Errorf("the camera was asked %d times for a person who may not view it", hits)
	}
	refused := false
	for _, e := range srv.audit.ring.Recent() {
		if e.Action == "camera.view" && e.Result == "refused" && e.User == "user:jsmith" {
			refused = true
		}
	}
	if !refused {
		t.Error("a refused live view is not journalled")
	}
	if rec := call(t, srv, "GET", "/api/cameras/dock/snapshot", "", eng); rec.Code != http.StatusOK || hits != 1 {
		t.Errorf("the engineer's still: %d (camera asked %d times)", rec.Code, hits)
	}
	// /api/me names the permission, since this project has cameras
	if me := call(t, srv, "GET", "/api/me", "", eng).Body.String(); !strings.Contains(me, `"camera"`) {
		t.Errorf("/api/me does not list the camera permission: %s", me)
	}
	if me := call(t, srv, "GET", "/api/me", "", blind).Body.String(); strings.Contains(me, `"camera"`) {
		t.Errorf("/api/me lists a permission the person does not hold: %s", me)
	}
}

// A still is fetched from the camera at most once a second, however many ask,
// and a camera that did not answer is not asked again at once: polling the
// still in a loop is not live video that the caps cannot see.
func TestStillsAreFetchedAtMostOnceASecondPerCamera(t *testing.T) {
	var hits, fail atomic.Int64
	back := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write(camJPEG(t))
	}))
	defer back.Close()
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fail.Add(1); w.WriteHeader(500) }))
	defer down.Close()
	srv, toks := camServer(t, []camera.Camera{
		{ID: "ok", Asset: "A", Kind: camera.KindSnapshot, URL: back.URL},
		{ID: "bad", Asset: "A", Kind: camera.KindSnapshot, URL: down.URL},
	}, nil)
	eng := bearer(toks["akhan"])
	for i := 0; i < 20; i++ {
		if rec := call(t, srv, "GET", "/api/cameras/ok/snapshot", "", eng); rec.Code != 200 {
			t.Fatalf("still %d: %d", i, rec.Code)
		}
		if rec := call(t, srv, "GET", "/api/cameras/bad/snapshot", "", eng); rec.Code != 502 {
			t.Fatalf("a failing camera: %d", rec.Code)
		}
	}
	if hits.Load() != 1 || fail.Load() != 1 {
		t.Errorf("20 requests each reached the cameras %d and %d times, want 1 and 1", hits.Load(), fail.Load())
	}
}

// A live view ends when its person signs out, and when it has run for its time
// limit; a picture is never cached, sniffed or rendered by a browser, and a HEAD
// does not take a place.
func TestALiveViewEndsWhenThePersonSignsOutOrTimeRunsOut(t *testing.T) {
	oldRe, oldMax := streamRecheck, maxLiveView
	streamRecheck = 20 * time.Millisecond
	t.Cleanup(func() { streamRecheck, maxLiveView = oldRe, oldMax })
	back := mjpegBackend(t)
	defer back.Close()
	srv, toks := camServer(t, []camera.Camera{{ID: "m", Asset: "A", Kind: camera.KindMJPEG, URL: back.URL}}, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// signed in by a cookie; signing out ends the stream
	c := signIn(t, srv, "akhan")
	req, _ := http.NewRequest("GET", ts.URL+"/api/cameras/m/stream", nil)
	cookieFrom(c)(req)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("a signed-in stream: %v %v", err, resp)
	}
	if h := resp.Header.Get("Content-Security-Policy"); !strings.Contains(h, "sandbox") || resp.Header.Get("X-Content-Type-Options") != "nosniff" || resp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("a picture's headers: %v", resp.Header)
	}
	ended := make(chan struct{})
	go func() { io.Copy(io.Discard, resp.Body); close(ended) }()
	select {
	case <-ended:
		t.Fatal("the stream ended on its own")
	case <-time.After(150 * time.Millisecond):
	}
	call(t, srv, "POST", "/api/signout", "", withCookie(c.Value), ownOrigin, fromLoopback)
	select {
	case <-ended:
	case <-time.After(3 * time.Second):
		resp.Body.Close()
		t.Fatal("a signed-out person kept watching")
	}
	waitStop := func(want string) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			for _, e := range srv.audit.ring.Recent() {
				if e.Action == "camera.view" && e.Detail["event"] == "stop" && strings.Contains(fmt.Sprint(e.Detail["ended"]), want) {
					return
				}
			}
			time.Sleep(25 * time.Millisecond)
		}
		t.Errorf("no stop entry says %q", want)
	}
	waitStop("may no longer view")

	// the time limit
	maxLiveView = 200 * time.Millisecond
	r2, stop := camOpen(t, ts, "m", toks["akhan"])
	defer stop()
	done := make(chan struct{})
	go func() { io.Copy(io.Discard, r2.Body); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		stop()
		t.Fatal("a live view outlived its time limit")
	}
	waitStop("time limit")

	// HEAD is not a view
	hreq, _ := http.NewRequest("HEAD", ts.URL+"/api/cameras/m/stream", nil)
	hreq.Header.Set("Authorization", "Bearer "+toks["akhan"])
	hr, err := http.DefaultClient.Do(hreq)
	if err != nil || hr.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("HEAD on a stream: %v %v", err, hr)
	}
}

// Without sign-in everyone is "anonymous": the per-person cap must count the
// browser's address, or two live streams would be every viewer's total.
func TestWithoutSignInThePersonCapCountsTheAddressNotEveryone(t *testing.T) {
	back := mjpegBackend(t)
	defer back.Close()
	co := &CameraOptions{Cameras: []camera.Camera{{ID: "m", Asset: "A", Kind: camera.KindMJPEG, URL: back.URL, MaxViewers: 8}}}
	o := Options{}
	co.Install(&o)
	srv := New(newTestRuntime(t), o)
	h := srv.Handler()
	var wg sync.WaitGroup
	cancels := []context.CancelFunc{}
	defer func() {
		for _, c := range cancels {
			c()
		}
		wg.Wait()
	}()
	// a stream from addr that stays open; returns the status it was answered with
	open := func(addr string) int {
		ctx, cancel := context.WithCancel(context.Background())
		cancels = append(cancels, cancel)
		req := httptest.NewRequest("GET", "/api/cameras/m/stream", nil).WithContext(ctx)
		req.RemoteAddr, req.Host = addr, "localhost"
		rec := &flushRecorder{ResponseRecorder: httptest.NewRecorder(), began: make(chan struct{})}
		wg.Add(1)
		go func() { defer wg.Done(); h.ServeHTTP(rec, req) }()
		select {
		case <-rec.began: // the first byte is written: the status was set before it
			return rec.Code
		case <-time.After(3 * time.Second):
			return rec.Code
		}
	}
	if s := open("192.0.2.1:1000"); s != 200 {
		t.Fatalf("first stream: %d", s)
	}
	if s := open("192.0.2.1:1001"); s != 200 {
		t.Fatalf("second stream from the same address: %d", s)
	}
	// the same address cannot take a third; a different address is not counted against it
	if s := open("192.0.2.1:1002"); s != http.StatusTooManyRequests {
		t.Errorf("third stream from one address: %d, want 429", s)
	}
	if s := open("192.0.2.2:1000"); s != 200 {
		t.Errorf("a stream from another address: %d, want 200 (everyone shares one anonymous cap)", s)
	}
}

// flushRecorder tells when the first byte of a body is written.
type flushRecorder struct {
	*httptest.ResponseRecorder
	once  sync.Once
	began chan struct{}
}

func (f *flushRecorder) Write(p []byte) (int, error) {
	f.once.Do(func() { close(f.began) })
	return f.ResponseRecorder.Write(p)
}
