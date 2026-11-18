package facade

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/eip/logixserver"
)

// The controller the facade fronts: a controller-scope REAL, a UDT with a
// nested UDT, a program-scope DINT, and a DINT array.
func spec() *logixserver.TagSurfaceSpec {
	return &logixserver.TagSurfaceSpec{
		ControllerName: "Line1",
		Templates: []logixserver.TemplateSpec{
			{Name: "Header_Type", Members: []logixserver.MemberSpec{
				{Name: "Displacement", Datatype: "REAL"},
				{Name: "Valid", Datatype: "BOOL"},
			}},
			{Name: "Plt_Type", Members: []logixserver.MemberSpec{
				{Name: "Header", Datatype: "Header_Type"},
				{Name: "Count", Datatype: "DINT"},
			}},
		},
		Symbols: []logixserver.SymbolSpec{
			{Name: "Speed", Datatype: "REAL"},
			{Name: "TRS", Datatype: "Plt_Type"},
			{Name: "Program:MainProgram", Program: true},
			{Name: "Motor", Scope: "Program:MainProgram", Datatype: "DINT"},
		},
		Tags: []logixserver.TagSpec{
			{Path: "Speed", Datatype: "REAL"},
			{Path: "TRS.Header.Displacement", Datatype: "REAL"},
			{Path: "TRS.Header.Valid", Datatype: "BOOL"},
			{Path: "TRS.Count", Datatype: "DINT"},
			{Path: "Program:MainProgram.Motor", Datatype: "DINT"},
		},
	}
}

func startController(t *testing.T) (int, *logixserver.TagStore) {
	t.Helper()
	schema, tags, name, err := logixserver.CompileSurface(spec())
	if err != nil {
		t.Fatal(err)
	}
	store := logixserver.NewTagStore()
	for _, tc := range tags {
		store.Set(tc.Path, tc.LeafType, tc.Default)
	}
	// Bind :0 inside the server (no pick-then-bind window) and read the port back.
	srv := logixserver.NewServer(store, schema, name, "127.0.0.1:0", nil)
	if err := srv.Listen(); err != nil {
		t.Fatal(err)
	}
	_, ps, _ := net.SplitHostPort(srv.Addr())
	port, _ := strconv.Atoi(ps)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = srv.Run(ctx) }()
	return port, store
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// startFacade runs a facade over the emulator and serves its API.
func startFacade(t *testing.T) (string, *logixserver.TagStore, *Facade) {
	t.Helper()
	port, store := startController(t)
	store.UpdateValue("Speed", 10.0)
	store.UpdateValue("TRS.Count", 3.0)
	store.UpdateValue("Program:MainProgram.Motor", 7.0)

	ctx, cancel := context.WithCancel(context.Background())
	f, err := New(ctx, Options{Host: "127.0.0.1", Port: port, Poll: 30 * time.Millisecond})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { f.Run(ctx); close(done) }()
	api := httptest.NewServer(f.Handler())
	t.Cleanup(func() { api.Close(); cancel(); <-done })
	return api.URL, store, f
}

func state(t *testing.T, base string) map[string]any {
	t.Helper()
	res, err := http.Get(base + "/api/state")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var frame struct {
		Tags map[string]any `json:"tags"`
	}
	if err := json.NewDecoder(res.Body).Decode(&frame); err != nil {
		t.Fatal(err)
	}
	return frame.Tags
}

func post(t *testing.T, base, body string) (int, string) {
	t.Helper()
	res, err := http.Post(base+"/api/tags", "application/json", bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

func TestLiveValuesFromTheController(t *testing.T) {
	base, store, f := startFacade(t)
	if f.Tags() != 3 {
		t.Errorf("bound %d tags, want 3 (Speed, TRS, MainProgram_Motor); skipped %v", f.Tags(), f.Skipped())
	}
	waitFor(t, "first poll in the frame", func() bool {
		tags := state(t, base)
		return tags["Speed"] == 10.0 && tags["MainProgram_Motor"] != nil
	})
	trs, _ := state(t, base)["TRS"].(map[string]any)
	if trs == nil || fmt.Sprint(trs["Count"]) != "3" {
		t.Errorf("TRS = %#v, want a struct with Count 3", state(t, base)["TRS"])
	}

	// A change made by the controller's own logic shows on the next poll.
	store.UpdateValue("Speed", 12.5)
	waitFor(t, "controller-side change", func() bool { return state(t, base)["Speed"] == 12.5 })
}

func TestWritesReachTheController(t *testing.T) {
	base, store, _ := startFacade(t)
	waitFor(t, "first poll", func() bool { return state(t, base)["Speed"] == 10.0 })

	cases := []struct {
		body, device string
		want         any
	}{
		{`{"name": "Speed", "value": 55.5}`, "Speed", 55.5},
		{`{"name": "TRS.Header.Valid", "value": true}`, "TRS.Header.Valid", true},
		{`{"name": "TRS.Count", "value": 9}`, "TRS.Count", 9.0},
		{`{"name": "MainProgram_Motor", "value": 42}`, "Program:MainProgram.Motor", 42.0},
	}
	for _, c := range cases {
		// The controller must hold something else first, or an unchanged
		// value would look like a write that landed.
		_, before, _ := store.Resolve(c.device)
		if fmt.Sprint(before) == fmt.Sprint(c.want) {
			t.Fatalf("%s: controller already holds %v; the case cannot tell a write from no write", c.body, before)
		}
		if code, body := post(t, base, c.body); code != http.StatusNoContent {
			t.Fatalf("%s = %d %s", c.body, code, body)
		}
		_, got, _ := store.Resolve(c.device)
		if fmt.Sprint(got) != fmt.Sprint(c.want) {
			t.Errorf("%s: controller holds %v (was %v), want %v", c.body, got, before, c.want)
		}
	}
	// And the frame follows the controller, not the request.
	waitFor(t, "write read back by the poll", func() bool { return state(t, base)["Speed"] == 55.5 })
	// A frame that merely echoed the request would show 55.5 forever; a value
	// the controller's own logic sets afterwards has to replace it.
	store.UpdateValue("Speed", 61.25)
	waitFor(t, "controller-side change after the write", func() bool { return state(t, base)["Speed"] == 61.25 })
}

// The write connection carries nothing between writes, so the controller
// times it out (32 s for the Forward Open serve sends; 30 s on the
// emulator). The first write after that must still land, not answer EOF.
func TestAWriteSurvivesADroppedConnection(t *testing.T) {
	base, store, f := startFacade(t)
	waitFor(t, "first poll", func() bool { return state(t, base)["Speed"] == 10.0 })
	if code, body := post(t, base, `{"name": "Speed", "value": 1}`); code != http.StatusNoContent {
		t.Fatalf("first write = %d %s", code, body)
	}

	// The connection goes away under the writer, which has not noticed.
	f.w.mu.Lock()
	_ = f.w.ctrl.Close()
	f.w.mu.Unlock()
	if code, body := post(t, base, `{"name": "Speed", "value": 2}`); code != http.StatusNoContent {
		t.Fatalf("write after the drop = %d %s", code, body)
	}
	if _, got, _ := store.Resolve("Speed"); fmt.Sprint(got) != "2" {
		t.Errorf("controller holds Speed = %v, want 2", got)
	}
}

func TestAnIdleWriteConnectionIsReplaced(t *testing.T) {
	base, store, f := startFacade(t)
	waitFor(t, "first poll", func() bool { return state(t, base)["Speed"] == 10.0 })
	f.w.mu.Lock()
	f.w.idle = 50 * time.Millisecond
	f.w.mu.Unlock()
	if code, body := post(t, base, `{"name": "Speed", "value": 3}`); code != http.StatusNoContent {
		t.Fatalf("first write = %d %s", code, body)
	}
	f.w.mu.Lock()
	first := f.w.ctrl
	f.w.mu.Unlock()

	time.Sleep(100 * time.Millisecond)
	if code, body := post(t, base, `{"name": "Speed", "value": 4}`); code != http.StatusNoContent {
		t.Fatalf("write after idle = %d %s", code, body)
	}
	f.w.mu.Lock()
	again := f.w.ctrl
	f.w.mu.Unlock()
	if again == first {
		t.Error("the idle connection was reused, not replaced")
	}
	if _, got, _ := store.Resolve("Speed"); fmt.Sprint(got) != "4" {
		t.Errorf("controller holds Speed = %v, want 4", got)
	}
}

func TestWritesThatCannotWork(t *testing.T) {
	base, _, _ := startFacade(t)
	for body, want := range map[string]string{
		`{"name": "Nope", "value": 1}`:                 "no tag named Nope",
		`{"name": "TRS", "value": {"Count": 1}}`:       "name the member",
		`{"name": "TRS", "value": 1}`:                  "write one of its members",
		`{"name": "TRS.Nope", "value": 1}`:             "Plt_Type has no member Nope",
		`{"name": "TRS.Header.Valid", "value": "yes"}`: "BOOL write needs bool",
	} {
		code, got := post(t, base, body)
		if code != http.StatusBadRequest || !strings.Contains(got, want) {
			t.Errorf("%s = %d %q, want 400 containing %q", body, code, got, want)
		}
	}
}

// The program endpoints say what is true of a Logix controller, in words
// the extension shows as-is.
func TestProgramEndpoints(t *testing.T) {
	base, _, _ := startFacade(t)
	for _, c := range []struct {
		method, path string
		code         int
		want         string
	}{
		{"GET", "/api/program", 404, "naut logix drift"},
		{"GET", "/api/program?pou=MainProgram", 404, "naut logix drift"},
		{"PUT", "/api/program", 403, "naut logix push"},
		{"POST", "/api/program/rollback", 403, "naut logix push"},
		{"POST", "/api/program/activate", 403, "naut logix push"},
	} {
		req, _ := http.NewRequest(c.method, base+c.path, strings.NewReader(`{"source":"PROGRAM X END_PROGRAM"}`))
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var body struct{ Error string }
		_ = json.NewDecoder(res.Body).Decode(&body)
		res.Body.Close()
		if res.StatusCode != c.code || !strings.Contains(body.Error, c.want) {
			t.Errorf("%s %s = %d %q, want %d mentioning %q", c.method, c.path, res.StatusCode, body.Error, c.code, c.want)
		}
	}
}

func TestDriverStatusInTheFrame(t *testing.T) {
	base, _, _ := startFacade(t)
	waitFor(t, "connected driver", func() bool {
		res, err := http.Get(base + "/api/drivers")
		if err != nil {
			return false
		}
		defer res.Body.Close()
		var ds []struct{ State, Detail string }
		_ = json.NewDecoder(res.Body).Decode(&ds)
		return len(ds) == 1 && ds[0].State == "connected" && strings.Contains(ds[0].Detail, "Logix facade")
	})
}

// #819: the facade's own routes keep the server's Host rule. With no
// token, a request addressed to a rebound name is refused before the
// program endpoint or the tag routes answer it; by IP it is answered.
func TestFacadeRefusesARebindableHost(t *testing.T) {
	base, _, _ := startFacade(t)
	for _, path := range []string{"/api/program", "/api/state"} {
		req, _ := http.NewRequest("GET", base+path, nil)
		req.Host = "evil.example:8080"
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusForbidden {
			t.Errorf("GET %s by a rebound name = %d, want 403", path, res.StatusCode)
		}
		ip, err := http.Get(base + path) // base is 127.0.0.1:<port>
		if err != nil {
			t.Fatal(err)
		}
		ip.Body.Close()
		if ip.StatusCode == http.StatusForbidden {
			t.Errorf("GET %s by IP = 403", path)
		}
	}
}
