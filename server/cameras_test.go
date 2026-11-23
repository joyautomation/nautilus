package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/camera"
)

// camServer is a server with auth (akhan an engineer; jsmith an operator; ghost
// holds nothing) and the cameras given.
func camServer(t *testing.T, cams []camera.Camera, secrets map[string]string) (*Server, map[string]string) {
	t.Helper()
	co := &CameraOptions{Cameras: cams, Fetcher: &camera.Fetcher{Secret: func(n string) (string, bool) { v, ok := secrets[n]; return v, ok }},
		Lines: func(c camera.Camera) []string { return []string{"asset " + c.Asset} }}
	o := Options{}
	co.Install(&o)
	srv, toks, _ := patServerWith(t, o)
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
	for _, who := range []string{"akhan", "jsmith"} {
		body := call(t, srv, "GET", "/api/cameras", "", bearer(toks[who])).Body.String()
		if !strings.Contains(body, `"bin-top"`) || !strings.Contains(body, `"dock"`) {
			t.Errorf("%s's list: %s", who, body)
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
	srv, toks := camServer(t, []camera.Camera{
		{ID: "one", Asset: "A1", Kind: camera.KindVirtual, MaxViewers: 1},
		{ID: "two", Asset: "A1", Kind: camera.KindVirtual},
	}, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, stop := camOpen(t, ts, "one", toks["akhan"])
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
	r2, stop2 := camOpen(t, ts, "one", toks["jsmith"])
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
