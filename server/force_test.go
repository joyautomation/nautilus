package server

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/leader"
	"github.com/joyautomation/nautilus/runtime"
)

func call(t *testing.T, srv *Server, method, path, body string, mod ...func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	var rd *bytes.Buffer
	if body != "" {
		rd = bytes.NewBufferString(body)
	} else {
		rd = &bytes.Buffer{}
	}
	req := httptest.NewRequest(method, path, rd)
	for _, m := range mod {
		m(req)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// captureLog swaps the default logger for one writing into a buffer, so the
// audit lines can be read back.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func TestForceAPI(t *testing.T) {
	logs := captureLog(t)
	rt := newTestRuntime(t)
	srv := New(rt)

	rec := call(t, srv, "POST", "/api/forces", `{"name":"Level","value":12}`)
	if rec.Code != 200 {
		t.Fatalf("force: %d %s", rec.Code, rec.Body)
	}
	var got forcesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || len(got.Forces) != 1 ||
		got.Forces[0].Name != "Level" || got.Forces[0].Value != 12.0 {
		t.Fatalf("force response = %s", rec.Body)
	}
	rt.Scan() // the Memory driver re-delivers Level = 40; the force holds
	if lvl := rt.Tags().Real("Level"); lvl != 12 {
		t.Fatalf("Level = %v under a force of 12", lvl)
	}
	if out := rt.Tags().Real("Out"); out != 65-12 {
		t.Fatalf("Out = %v; the logic should see the forced input", out)
	}
	if !strings.Contains(logs.String(), "force: set") || !strings.Contains(logs.String(), "tag=Level") {
		t.Errorf("no audit line for the force: %q", logs.String())
	}

	// The state frame carries the table; an operator write to the forced
	// tag is refused rather than silently swallowed.
	var f Frame
	json.Unmarshal(call(t, srv, "GET", "/api/state", "").Body.Bytes(), &f)
	if f.Forces["Level"] != 12.0 {
		t.Fatalf("frame forces = %v", f.Forces)
	}
	if rec := call(t, srv, "POST", "/api/tags", `{"name":"Level","value":5}`); rec.Code != http.StatusConflict {
		t.Fatalf("write to a forced tag = %d, want 409", rec.Code)
	}

	// Remove one; a second remove is a 404.
	if rec := call(t, srv, "DELETE", "/api/forces/Level", ""); rec.Code != 200 {
		t.Fatalf("unforce: %d %s", rec.Code, rec.Body)
	}
	if rec := call(t, srv, "DELETE", "/api/forces/Level", ""); rec.Code != 404 {
		t.Fatalf("second unforce = %d, want 404", rec.Code)
	}
	if lvl := rt.Tags().Real("Level"); lvl != 40 {
		t.Fatalf("Level = %v after unforce; want the field's 40", lvl)
	}
	f = Frame{}
	json.Unmarshal(call(t, srv, "GET", "/api/state", "").Body.Bytes(), &f)
	if f.Forces != nil {
		t.Fatalf("frame still carries forces: %v", f.Forces)
	}

	// Clear all.
	call(t, srv, "POST", "/api/forces", `{"name":"Level","value":1}`)
	call(t, srv, "POST", "/api/forces", `{"name":"SP","value":2}`)
	rec = call(t, srv, "POST", "/api/forces/clear", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"removed":2`) {
		t.Fatalf("clear: %d %s", rec.Code, rec.Body)
	}
	if !strings.Contains(logs.String(), "force: cleared all") {
		t.Error("no audit line for clear-all")
	}

	// Bad requests.
	for _, body := range []string{`{"name":"Nope","value":1}`, `{"name":"Level","value":"x"}`, `{"name":"Level"}`} {
		if rec := call(t, srv, "POST", "/api/forces", body); rec.Code != 400 {
			t.Errorf("force %s = %d, want 400", body, rec.Code)
		}
	}
	var meta metaResponse
	json.Unmarshal(call(t, srv, "GET", "/api/meta", "").Body.Bytes(), &meta)
	if !meta.Forces {
		t.Error("meta does not advertise forces")
	}
}

// Forcing is a write: the same guard as POST /api/tags.
func TestForceAuth(t *testing.T) {
	srv := New(newTestRuntime(t))
	cross := func(r *http.Request) { r.Header.Set("Origin", "http://evil.example") }
	for _, c := range []struct{ method, path, body string }{
		{"POST", "/api/forces", `{"name":"Level","value":1}`},
		{"DELETE", "/api/forces/Level", ""},
		{"POST", "/api/forces/clear", ""},
		{"POST", "/api/sfc/step", `{"step":"A"}`},
		{"POST", "/api/sfc/transition", `{"transition":"T"}`},
	} {
		if rec := call(t, srv, c.method, c.path, c.body, cross); rec.Code != http.StatusForbidden {
			t.Errorf("cross-origin %s %s = %d, want 403", c.method, c.path, rec.Code)
		}
	}
	tok := New(newTestRuntime(t), Options{AuthToken: "s3cret"})
	if rec := call(t, tok, "POST", "/api/forces", `{"name":"Level","value":1}`); rec.Code != http.StatusUnauthorized {
		t.Errorf("tokenless force = %d, want 401", rec.Code)
	}
	if rec := call(t, tok, "POST", "/api/forces", `{"name":"Level","value":1}`, func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer s3cret")
	}); rec.Code != 200 {
		t.Errorf("token force = %d, want 200", rec.Code)
	}
	// Reads stay open.
	if rec := call(t, tok, "GET", "/api/forces", ""); rec.Code != 200 {
		t.Errorf("GET /api/forces = %d", rec.Code)
	}
	if rec := call(t, srv, "OPTIONS", "/api/forces/Level", ""); !strings.Contains(rec.Header().Get("Access-Control-Allow-Methods"), "DELETE") {
		t.Error("CORS preflight does not allow DELETE")
	}
}

// A server fronting a controller it does not run cannot hold a force.
func TestForceRefusedWithTagWriter(t *testing.T) {
	srv := New(newTestRuntime(t), Options{TagWriter: func(string, any) error { return nil }})
	if rec := call(t, srv, "POST", "/api/forces", `{"name":"Level","value":1}`); rec.Code != http.StatusNotImplemented {
		t.Fatalf("force via a TagWriter server = %d, want 501", rec.Code)
	}
	var meta metaResponse
	json.Unmarshal(call(t, srv, "GET", "/api/meta", "").Body.Bytes(), &meta)
	if meta.Forces {
		t.Error("meta advertises forces on a TagWriter server")
	}
}

// On a standby the force calls go to the leader — the force table is the
// active controller's.
func TestForceProxiedFromStandby(t *testing.T) {
	var hit string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = r.Method + " " + r.URL.Path
		w.WriteHeader(200)
	}))
	defer backend.Close()
	srv := New(newTestRuntime(t), Options{Cluster: fakeCluster{status: leader.Status{
		Mode: "cluster", Pod: "plc-1", Leader: "plc-0", LeaderAddr: standbyAddr(backend),
	}}})
	call(t, srv, "POST", "/api/forces", `{"name":"Level","value":1}`)
	if hit != "POST /api/forces" {
		t.Fatalf("standby did not proxy the force (backend saw %q)", hit)
	}
	if n := srv.rt.Tags().ForceCount(); n != 0 {
		t.Fatalf("standby forced its own store (%d)", n)
	}
}

// The stream carries the table on every frame while one is active — on a
// delta stream too, where it is never gated.
func TestStreamCarriesForces(t *testing.T) {
	rt := newTestRuntime(t)
	srv := New(rt)
	c := &client{ch: make(chan []byte, 8), delta: true, blocks: true}
	srv.mu.Lock()
	srv.clients[c] = struct{}{}
	srv.mu.Unlock()
	next := func() Frame {
		srv.broadcast()
		var f Frame
		if err := json.Unmarshal(<-c.ch, &f); err != nil {
			t.Fatal(err)
		}
		return f
	}
	if f := next(); f.Forces != nil {
		t.Fatalf("forces on an unforced controller: %v", f.Forces)
	}
	if err := rt.Tags().Force("SP", 70.0); err != nil {
		t.Fatal(err)
	}
	f := next()
	if f.Forces["SP"] != 70.0 || f.Tags["SP"] != 70.0 {
		t.Fatalf("frame after force: forces %v, SP %v", f.Forces, f.Tags["SP"])
	}
	f = next() // nothing moved: a delta with no tags, but still the table
	if f.Forces["SP"] != 70.0 || len(f.Tags) != 0 {
		t.Fatalf("held force: forces %v, tags %v", f.Forces, f.Tags)
	}
	rt.Tags().UnforceAll()
	if f := next(); f.Forces != nil {
		t.Fatalf("forces after clear: %v", f.Forces)
	}
}

const chartProgram = `PROGRAM Batch
VAR_EXTERNAL Go : BOOL; END_VAR
SFC
  INITIAL_STEP Idle:
  END_STEP
  STEP Fill:
  END_STEP
  TRANSITION Start FROM Idle TO Fill := Go;
  END_TRANSITION
  TRANSITION FROM Fill TO Idle := FALSE;
  END_TRANSITION
END_SFC
END_PROGRAM`

func TestSFCAPI(t *testing.T) {
	logs := captureLog(t)
	rt, err := runtime.New(runtime.Options{Program: chartProgram, Tags: []runtime.TagDef{runtime.Setpoint("Go", false)}})
	if err != nil {
		t.Fatal(err)
	}
	rt.Scan()
	srv := New(rt)
	var charts sfcResponse
	json.Unmarshal(call(t, srv, "GET", "/api/sfc", "").Body.Bytes(), &charts)
	if len(charts.Charts) != 1 || charts.Charts[0].POU != "Batch" || len(charts.Charts[0].Steps) != 2 {
		t.Fatalf("GET /api/sfc = %+v", charts)
	}
	rec := call(t, srv, "POST", "/api/sfc/transition", `{"transition":"Start"}`)
	if rec.Code != 200 {
		t.Fatalf("fire: %d %s", rec.Code, rec.Body)
	}
	var info runtime.SFCInfo
	json.Unmarshal(rec.Body.Bytes(), &info)
	if !info.Steps[1].Active || info.Steps[0].Active {
		t.Fatalf("after firing Start: %+v", info.Steps)
	}
	if rec := call(t, srv, "POST", "/api/sfc/transition", `{"transition":"Start"}`); rec.Code != http.StatusConflict {
		t.Fatalf("firing a disabled transition = %d, want 409", rec.Code)
	}
	if rec := call(t, srv, "POST", "/api/sfc/step", `{"step":"Idle","pou":"Batch"}`); rec.Code != 200 {
		t.Fatalf("set step: %d %s", rec.Code, rec.Body)
	}
	if rec := call(t, srv, "POST", "/api/sfc/step", `{"step":"Nowhere"}`); rec.Code != 404 {
		t.Fatalf("unknown step = %d, want 404", rec.Code)
	}
	if !strings.Contains(logs.String(), "sfc: set active step") || !strings.Contains(logs.String(), "sfc: fired transition") {
		t.Errorf("missing SFC audit lines: %q", logs.String())
	}
	// An ST controller has no charts.
	st := New(newTestRuntime(t))
	if rec := call(t, st, "POST", "/api/sfc/step", `{"step":"Idle"}`); rec.Code != 404 {
		t.Fatalf("SFC command on an ST controller = %d, want 404", rec.Code)
	}
}
