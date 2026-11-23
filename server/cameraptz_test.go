//go:build !js

package server

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/camera"
	"github.com/joyautomation/nautilus/camera/cameratest"
)

func ptzServer(t *testing.T) (*Server, map[string]string, *cameratest.ONVIF) {
	t.Helper()
	return ptzServerWith(t, cameratest.ONVIFOpts{})
}

func ptzServerWith(t *testing.T, o cameratest.ONVIFOpts) (*Server, map[string]string, *cameratest.ONVIF) {
	t.Helper()
	o.Presets = [][2]string{{"1", "Home"}, {"2", "Gate"}}
	d := cameratest.StartONVIF(t, o)
	srv, toks := camServer(t, []camera.Camera{
		{ID: "dome", Label: "Dome", Asset: "A1", Kind: camera.KindONVIF, URL: d.URL(), PTZ: true},
		{ID: "fixed", Label: "Fixed", Asset: "A1", Kind: camera.KindONVIF, URL: d.URL()},
		{ID: "virt", Label: "Virtual", Asset: "A1", Kind: camera.KindVirtual},
	}, nil)
	return srv, toks, d
}

func ptzStop(t *testing.T, srv *Server, id string, mod ...func(*http.Request)) (int, string) {
	t.Helper()
	rec := call(t, srv, "POST", "/api/cameras/"+id+"/ptz/stop", "", append([]func(*http.Request){ownOrigin}, mod...)...)
	return rec.Code, rec.Body.String()
}

// waitMoves waits until the device has been told n things (or gives up).
func waitMoves(d *cameratest.ONVIF, n int) []string {
	deadline := time.Now().Add(4 * time.Second)
	for len(d.Moves()) < n && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	return d.Moves()
}

func ptzPost(t *testing.T, srv *Server, id, body string, mod ...func(*http.Request)) (int, string) {
	t.Helper()
	rec := call(t, srv, "POST", "/api/cameras/"+id+"/ptz", body, append([]func(*http.Request){ownOrigin}, mod...)...)
	return rec.Code, rec.Body.String()
}

func TestACameraIsMovedBoundedStoppedAndJournalled(t *testing.T) {
	srv, toks, d := ptzServer(t)
	tok := bearer(toks["akhan"])
	if code, b := ptzPost(t, srv, "dome", `{"action":"move","x":0.5,"y":-1,"zoom":0,"ms":150}`, tok); code != 200 {
		t.Fatalf("move: %d %s", code, b)
	}
	// the controller ends the move itself when its time is up: a closed tab leaves nothing moving
	deadline := time.Now().Add(3 * time.Second)
	for len(d.Moves()) < 2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if m := d.Moves(); len(m) != 2 || m[0] != "move 0.500 -1.000 0.000 PT1S" || m[1] != "stop" {
		t.Errorf("the device was told %q", m)
	}
	time.Sleep(ptzMinGap)
	if code, b := ptzStop(t, srv, "dome", tok); code != 200 {
		t.Fatalf("stop: %d %s", code, b)
	}
	// presets are the device's, listed to a viewer and gone to by name
	rec := call(t, srv, "GET", "/api/cameras/dome/ptz", "", tok)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"name":"Home"`) || strings.Contains(rec.Body.String(), "10.9.9.9") {
		t.Errorf("presets: %d %s", rec.Code, rec.Body)
	}
	time.Sleep(ptzMinGap)
	if code, b := ptzPost(t, srv, "dome", `{"action":"preset","preset":"2"}`, tok); code != 200 {
		t.Fatalf("preset: %d %s", code, b)
	}
	if m := d.Moves(); m[len(m)-1] != "goto 2" {
		t.Errorf("the device was told %q", m)
	}
	// each request is a journal entry: who, which camera, what
	var acts []string
	for _, e := range srv.audit.ring.Recent() {
		if e.Action == "camera.ptz" || e.Action == "camera.ptz-stop" {
			if e.Target != "dome" || e.User != "user:akhan" || e.Result != "ok" {
				t.Errorf("entry %+v", e)
			}
			acts = append(acts, e.Action+":"+e.Detail["action"].(string))
		}
	}
	if strings.Join(acts, ",") != "camera.ptz:preset,camera.ptz-stop:stop,camera.ptz:move" {
		t.Errorf("journalled %v", acts)
	}
}

func TestPTZRefusesWhatItShouldWithoutAskingTheCamera(t *testing.T) {
	srv, toks, d := ptzServer(t)
	tok := bearer(toks["akhan"])
	for _, c := range []struct {
		what, id, body string
		want           int
		mod            func(*http.Request)
	}{
		{"a person who may not view cameras", "dome", `{"action":"move","x":1,"ms":500}`, 403, bearer(toks["jsmith"])},
		{"a camera not marked ptz", "fixed", `{"action":"move","x":1,"ms":500}`, 404, tok},
		{"a camera of another kind", "virt", `{"action":"move","x":1,"ms":500}`, 404, tok},
		{"no such camera", "nope", `{"action":"move","x":1,"ms":500}`, 404, tok},
		{"a stop sent as an action", "dome", `{"action":"stop"}`, 400, tok},
		{"out of range", "dome", `{"action":"move","x":2,"ms":500}`, 400, tok},
		{"no movement", "dome", `{"action":"move","ms":500}`, 400, tok},
		{"too short", "dome", `{"action":"move","x":1,"ms":5}`, 400, tok},
		{"too long", "dome", `{"action":"move","x":1,"ms":60000}`, 400, tok},
		{"an unknown field", "dome", `{"action":"move","x":1,"ms":500,"url":"http://evil"}`, 400, tok},
		{"an unknown action", "dome", `{"action":"reboot"}`, 400, tok},
		{"not JSON", "dome", `x`, 400, tok},
		{"a preset the device lacks", "dome", `{"action":"preset","preset":"9"}`, 502, tok},
	} {
		code, b := ptzPost(t, srv, c.id, c.body, c.mod)
		if code != c.want {
			t.Errorf("%s: %d %s, want %d", c.what, code, b, c.want)
		}
		time.Sleep(ptzMinGap)
	}
	for _, m := range d.Moves() {
		if !strings.HasPrefix(m, "stop") {
			t.Errorf("a refused request moved the camera: %q", m)
		}
	}
}

func TestMovesCannotBeSentFasterThanOneAtATime(t *testing.T) {
	srv, toks, _ := ptzServer(t)
	tok := bearer(toks["akhan"])
	time.Sleep(ptzMinGap)
	c1, _ := ptzPost(t, srv, "dome", `{"action":"move","x":1,"ms":5000}`, tok)
	c2, b := ptzPost(t, srv, "dome", `{"action":"move","x":-1,"ms":5000}`, tok)
	if c1 != 200 || c2 != http.StatusTooManyRequests {
		t.Errorf("two moves at once: %d, %d %s", c1, c2, b)
	}
	// a stop is never refused for being quick
	for i := 0; i < 3; i++ {
		if c, b := ptzStop(t, srv, "dome", tok); c != 200 {
			t.Errorf("stop %d: %d %s", i, c, b)
		}
	}
}

// One elevation covers a session of moves: an operator signed in with a password
// pans again and again inside the window without being asked, and is asked once it lapses.
func TestOneElevationCoversASessionOfMoves(t *testing.T) {
	srv, _, d := ptzServer(t)
	id, _, at := elevateAs(t, srv)
	for i, minutes := range []int{0, 3, 7, 14} {
		at(time.Duration(minutes) * time.Minute)
		time.Sleep(ptzMinGap)
		if code, b := ptzPost(t, srv, "dome", `{"action":"move","x":0.5,"ms":200}`, withCookie(id)); code != 200 {
			t.Fatalf("move %d at %d min: %d %s", i, minutes, code, b)
		}
	}
	at(16 * time.Minute)
	time.Sleep(ptzMinGap)
	n := len(d.Moves())
	code, b := ptzPost(t, srv, "dome", `{"action":"move","x":0.5,"ms":200}`, withCookie(id))
	if code != http.StatusUnauthorized || !elevateMarked(b) {
		t.Errorf("after the window: %d %s", code, b)
	}
	time.Sleep(300 * time.Millisecond)
	for _, m := range d.Moves()[n:] {
		if strings.HasPrefix(m, "move") {
			t.Errorf("a refused move reached the camera: %q", m)
		}
	}
}

// A stop is a way back: whoever may see the camera stops it, with no ptz permission and no fresh
// password; a move needs both.
func TestAStopNeedsNeitherThePTZPermissionNorAFreshPassword(t *testing.T) {
	srv, _, d := ptzServer(t)
	id, _, at := elevateAs(t, srv)
	at(16 * time.Minute) // the password has lapsed
	if code, b := ptzPost(t, srv, "dome", `{"action":"move","x":1,"ms":500}`, withCookie(id)); code != http.StatusUnauthorized || !elevateMarked(b) {
		t.Errorf("a move after the window: %d %s", code, b)
	}
	if code, b := ptzStop(t, srv, "dome", withCookie(id)); code != 200 {
		t.Errorf("a stop after the window: %d %s", code, b)
	}
	if m := d.Moves(); len(m) != 1 || m[0] != "stop" {
		t.Errorf("the device was told %q", m)
	}
	// no credential at all is a 401, not a 400 about a body
	if code, _ := ptzPost(t, srv, "dome", `{"action":"move","x":1,"ms":500}`); code != http.StatusUnauthorized {
		t.Errorf("a move with no credential: %d", code)
	}
}

// Whatever fails, the camera is still stopped when the move's time is up.
func TestAFailedRequestNeverCancelsTheStopOfAMoveAlreadyRunning(t *testing.T) {
	srv, toks, d := ptzServer(t)
	tok := bearer(toks["akhan"])
	if code, b := ptzPost(t, srv, "dome", `{"action":"move","x":1,"ms":1200}`, tok); code != 200 {
		t.Fatal(code, b)
	}
	time.Sleep(ptzMinGap)
	// a preset the device lacks: refused, and the older move's stop is the controller's still
	if code, _ := ptzPost(t, srv, "dome", `{"action":"preset","preset":"9"}`, tok); code != 502 {
		t.Fatalf("preset: %d", code)
	}
	m := waitMoves(d, 2)
	if len(m) < 2 || m[len(m)-1] != "stop" {
		t.Errorf("nothing stopped the camera: %q", m)
	}
}

func TestAMoveWhoseAnswerWasLostIsStoppedAnyway(t *testing.T) {
	srv, toks, d := ptzServerWith(t, cameratest.ONVIFOpts{Misbehave: "move-500"})
	tok := bearer(toks["akhan"])
	if code, _ := ptzPost(t, srv, "dome", `{"action":"move","x":1,"ms":300}`, tok); code != 502 {
		t.Fatalf("move: %d", code)
	}
	m := waitMoves(d, 2)
	if len(m) != 2 || m[0] != "move-lost" || m[1] != "stop" {
		t.Errorf("the camera may have moved and nothing stopped it: %q", m)
	}
}

// A stop that overtakes its move (the move was slow on the way) is not undone by it: the move,
// landing late, is stopped again.
func TestAStopThatOvertakesItsMoveIsNotUndoneByIt(t *testing.T) {
	srv, toks, d := ptzServerWith(t, cameratest.ONVIFOpts{MoveDelay: 400 * time.Millisecond})
	tok := bearer(toks["akhan"])
	done := make(chan int, 1)
	go func() { c, _ := ptzPost(t, srv, "dome", `{"action":"move","x":1,"ms":5000}`, tok); done <- c }()
	time.Sleep(100 * time.Millisecond)
	start := time.Now()
	if code, b := ptzStop(t, srv, "dome", tok); code != 200 {
		t.Fatalf("stop: %d %s", code, b)
	}
	// a stop does not wait for a move that is hung at the device
	if time.Since(start) > 300*time.Millisecond {
		t.Errorf("the stop waited %s behind the move", time.Since(start))
	}
	<-done
	m := waitMoves(d, 3)
	if len(m) < 3 || m[len(m)-1] != "stop" {
		t.Errorf("the late move was not stopped: %q", m)
	}
	// and the stale move armed no timer that stops a later one early
	time.Sleep(50 * time.Millisecond)
	time.Sleep(ptzMinGap)
	n := len(d.Moves())
	if code, _ := ptzPost(t, srv, "dome", `{"action":"move","x":-1,"ms":5000}`, tok); code != 200 {
		t.Fatal(code)
	}
	time.Sleep(700 * time.Millisecond)
	if got := d.Moves(); len(got) != n+1 {
		t.Errorf("the next move was stopped early by an older timer: %q", got[n:])
	}
}

func TestAViewerMayStopACameraAndMayNotMoveIt(t *testing.T) {
	srv, toks, d := ptzServer(t)
	vera := bearer(toks["vera"])
	if code, _ := ptzPost(t, srv, "dome", `{"action":"move","x":1,"ms":500}`, vera); code != http.StatusForbidden {
		t.Errorf("a viewer's move: %d", code)
	}
	if code, b := ptzStop(t, srv, "dome", vera); code != 200 {
		t.Errorf("a viewer's stop: %d %s", code, b)
	}
	if m := d.Moves(); len(m) != 1 || m[0] != "stop" {
		t.Errorf("the device was told %q", m)
	}
	// and a person who may not see the camera may not stop it either
	if code, _ := ptzStop(t, srv, "dome", bearer(toks["jsmith"])); code != http.StatusForbidden {
		t.Errorf("a person who may not view cameras: %d", code)
	}
}
