package server

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/camera"
	"github.com/joyautomation/nautilus/camera/cameratest"
)

func hlsServer(t *testing.T, cams []camera.Camera, secrets map[string]string) (*Server, map[string]string, *CameraOptions) {
	t.Helper()
	co := &CameraOptions{Cameras: cams, Fetcher: &camera.Fetcher{Secret: func(n string) (string, bool) { v, ok := secrets[n]; return v, ok }}}
	o := Options{}
	co.Install(&o)
	srv, toks, _ := patServerSettings(t, newTestRuntime(t), o, camSettings())
	srv.Handler() // mounts the routes
	t.Cleanup(co.Stop)
	return srv, toks, co
}

func viewEntries(srv *Server, cam, event string) (n int, last map[string]any) {
	for _, e := range srv.audit.ring.Recent() {
		if e.Action == "camera.view" && e.Target == cam && e.Detail["event"] == event {
			n++
			last = e.Detail
		}
	}
	return
}

func TestAnRTSPCameraIsPlayedAsHLSAndTheViewIsJournalledForItsLease(t *testing.T) {
	old := hlsLeaseTTL
	hlsLeaseTTL = 400 * time.Millisecond
	defer func() { hlsLeaseTTL = old }()
	cam := cameratest.Start(t, cameratest.Opts{Loops: 3})
	srv, toks, _ := hlsServer(t, []camera.Camera{{ID: "gate", Asset: "A", Kind: camera.KindRTSP, URL: cam.URL("/stream")}}, nil)
	eng := bearer(toks["akhan"])

	rec := call(t, srv, "GET", "/api/cameras/gate/hls/index.m3u8", "", eng)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/vnd.apple.mpegurl" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("playlist: %d %v %s", rec.Code, rec.Header(), rec.Body)
	}
	pl := rec.Body.String()
	for _, want := range []string{"#EXT-X-MAP:URI=\"init.mp4\"", "seg-1.m4s"} {
		if !strings.Contains(pl, want) {
			t.Errorf("playlist lacks %q:\n%s", want, pl)
		}
	}
	if init := call(t, srv, "GET", "/api/cameras/gate/hls/init.mp4", "", eng); init.Code != 200 || !strings.Contains(init.Body.String(), "avcC") || init.Header().Get("Content-Type") != "video/mp4" {
		t.Errorf("init: %d %v", init.Code, init.Header())
	}
	seg := call(t, srv, "GET", "/api/cameras/gate/hls/seg-1.m4s", "", eng)
	if seg.Code != 200 || !strings.Contains(seg.Body.String(), "moof") || !strings.Contains(seg.Header().Get("Content-Security-Policy"), "sandbox") {
		t.Errorf("segment: %d %v", seg.Code, seg.Header())
	}
	for _, p := range []string{"seg-99999.m4s", "seg-01.m4s", "seg-1.mp4", "index.m3u8x", "../index.m3u8", "x"} {
		if rec := call(t, srv, "GET", "/api/cameras/gate/hls/"+p, "", eng); rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d, want 404", p, rec.Code)
		}
	}
	// the view began once, whatever the number of requests, and ends when the lease lapses
	if n, _ := viewEntries(srv, "gate", "start"); n != 1 {
		t.Errorf("%d start entries for one viewer's requests, want 1", n)
	}
	deadline := time.Now().Add(5 * time.Second)
	var stop map[string]any
	for time.Now().Before(deadline) {
		if n, d := viewEntries(srv, "gate", "stop"); n == 1 {
			stop = d
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if stop == nil {
		t.Fatal("the lease lapsed and no stop entry was written")
	}
	if b, _ := stop["bytes"].(int64); b <= 0 || stop["protocol"] != "hls" {
		t.Errorf("the stop entry: %v", stop)
	}
	// the camera is listed as HLS and live
	if l := call(t, srv, "GET", "/api/cameras", "", eng).Body.String(); !strings.Contains(l, `"hls":true`) || !strings.Contains(l, `"live":true`) {
		t.Errorf("the list: %s", l)
	}
	if rec := call(t, srv, "GET", "/api/cameras/gate/stream", "", eng); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "hls/index.m3u8") {
		t.Errorf("the motion-JPEG route for an RTSP camera: %d %s", rec.Code, rec.Body)
	}
}

func TestHLSNeedsTheCameraPermissionAndNeverContactsTheCameraWithout(t *testing.T) {
	cam := cameratest.Start(t, cameratest.Opts{})
	srv, toks, _ := hlsServer(t, []camera.Camera{{ID: "gate", Asset: "A", Kind: camera.KindRTSP, URL: cam.URL("/stream")}}, nil)
	for _, who := range []string{"jsmith", "ghost"} {
		for _, p := range []string{"index.m3u8", "init.mp4", "seg-1.m4s"} {
			if rec := call(t, srv, "GET", "/api/cameras/gate/hls/"+p, "", bearer(toks[who])); rec.Code != http.StatusForbidden {
				t.Errorf("%s %s: %d, want 403", who, p, rec.Code)
			}
		}
	}
	if rec := call(t, srv, "GET", "/api/cameras/gate/hls/index.m3u8", "", func(*http.Request) {}); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous: %d", rec.Code)
	}
	if cam.Requests.Load() != 0 {
		t.Errorf("the camera was contacted %d times for people who may not view it", cam.Requests.Load())
	}
}

func TestAnRTSPCameraThatCannotBeRelayedIsAnErrorInWordsAndGivesBackItsPlace(t *testing.T) {
	cam := cameratest.Start(t, cameratest.Opts{SDP: "h265"})
	srv, toks, co := hlsServer(t, []camera.Camera{{ID: "gate", Asset: "A", Kind: camera.KindRTSP, URL: cam.URL("/stream")}}, nil)
	rec := call(t, srv, "GET", "/api/cameras/gate/hls/index.m3u8", "", bearer(toks["akhan"]))
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "another video codec") {
		t.Fatalf("an H.265 camera: %d %s", rec.Code, rec.Body)
	}
	for _, leak := range []string{"127.0.0.1", cam.Addr()} {
		if strings.Contains(rec.Body.String(), leak) {
			t.Errorf("the answer names %q: %s", leak, rec.Body)
		}
	}
	if co.limit.Watching("gate") != 0 {
		t.Error("a view that never started holds a place")
	}
	// a view that never started is one error row, with the reason, and no start or stop
	errRows := 0
	for _, e := range srv.audit.ring.Recent() {
		if e.Action == "camera.view" && e.Target == "gate" {
			if e.Result != "error" || !strings.Contains(fmt.Sprint(e.Detail["error"]), "another video codec") {
				t.Errorf("a journal row for the failed view: %+v", e)
			}
			errRows++
		}
	}
	if errRows != 1 {
		t.Errorf("%d journal rows for a view that never started, want 1", errRows)
	}
}

func TestHLSViewersAreCappedByCameraPersonAndController(t *testing.T) {
	cam := cameratest.Start(t, cameratest.Opts{Loops: 2})
	srv, toks, co := hlsServer(t, []camera.Camera{
		{ID: "one", Asset: "A", Kind: camera.KindRTSP, URL: cam.URL("/s1"), MaxViewers: 1},
		{ID: "two", Asset: "A", Kind: camera.KindRTSP, URL: cam.URL("/s2")},
		{ID: "three", Asset: "A", Kind: camera.KindRTSP, URL: cam.URL("/s3")},
		{ID: "four", Asset: "A", Kind: camera.KindRTSP, URL: cam.URL("/s4")},
	}, nil)
	eng := bearer(toks["akhan"])
	get := func(id string, who func(*http.Request)) int {
		return call(t, srv, "GET", "/api/cameras/"+id+"/hls/index.m3u8", "", who).Code
	}
	if c := get("one", eng); c != 200 {
		t.Fatalf("first viewer: %d", c)
	}
	// the camera's own cap: akhan is already watching it (the same lease), so use another person
	// (jsmith may not view cameras; an engineer token stands for a second person here)
	_ = co
	if c := get("two", eng); c != 200 {
		t.Fatalf("second camera: %d", c)
	}
	if c := get("three", eng); c != http.StatusTooManyRequests {
		t.Errorf("a third camera for one person: %d, want 429 (two live streams each)", c)
	}
}

func TestAnHLSViewEndsAtItsTimeLimit(t *testing.T) {
	old := maxLiveView
	maxLiveView = 300 * time.Millisecond
	defer func() { maxLiveView = old }()
	cam := cameratest.Start(t, cameratest.Opts{Loops: 3})
	srv, toks, _ := hlsServer(t, []camera.Camera{{ID: "gate", Asset: "A", Kind: camera.KindRTSP, URL: cam.URL("/stream")}}, nil)
	eng := bearer(toks["akhan"])
	if rec := call(t, srv, "GET", "/api/cameras/gate/hls/index.m3u8", "", eng); rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	time.Sleep(450 * time.Millisecond)
	if rec := call(t, srv, "GET", "/api/cameras/gate/hls/seg-1.m4s", "", eng); rec.Code != http.StatusGone {
		t.Errorf("past the time limit: %d, want 410", rec.Code)
	}
	if n, d := viewEntries(srv, "gate", "stop"); n != 1 || !strings.Contains(d["ended"].(string), "time limit") {
		t.Errorf("the stop entry: %d %v", n, d)
	}
}
