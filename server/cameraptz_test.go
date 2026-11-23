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
	d := cameratest.StartONVIF(t, cameratest.ONVIFOpts{Presets: [][2]string{{"1", "Home"}, {"2", "Gate"}}})
	srv, toks := camServer(t, []camera.Camera{
		{ID: "dome", Label: "Dome", Asset: "A1", Kind: camera.KindONVIF, URL: d.URL(), PTZ: true},
		{ID: "fixed", Label: "Fixed", Asset: "A1", Kind: camera.KindONVIF, URL: d.URL()},
		{ID: "virt", Label: "Virtual", Asset: "A1", Kind: camera.KindVirtual},
	}, nil)
	return srv, toks, d
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
	if code, b := ptzPost(t, srv, "dome", `{"action":"stop"}`, tok); code != 200 {
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
		if e.Action == "camera.ptz" {
			if e.Target != "dome" || e.User != "user:akhan" || e.Result != "ok" {
				t.Errorf("entry %+v", e)
			}
			acts = append(acts, e.Detail["action"].(string))
		}
	}
	if strings.Join(acts, ",") != "preset,stop,move" {
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
		{"no such camera", "nope", `{"action":"stop"}`, 404, tok},
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
	if c, b := ptzPost(t, srv, "dome", `{"action":"stop"}`, tok); c != 200 {
		t.Errorf("stop: %d %s", c, b)
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
