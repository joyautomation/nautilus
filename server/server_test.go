package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/runtime"
)

const testProgram = `PROGRAM Test
VAR_EXTERNAL
  Level : REAL;
  SP : REAL;
  Out : REAL;
END_VAR
Out := SP - Level;
END_PROGRAM
`

func newTestRuntime(t *testing.T) *runtime.Runtime {
	t.Helper()
	drv := nio.NewMemory()
	rt, err := runtime.New(runtime.Options{
		Program: testProgram,
		Driver:  drv,
		Inputs:  []string{"Level"},
		Outputs: []string{"Out"},
		Seed:    nio.Values{"Level": 40.0, "SP": 65.0, "Out": 0.0},
	})
	if err != nil {
		t.Fatal(err)
	}
	rt.Scan()
	return rt
}

func TestStateSnapshot(t *testing.T) {
	srv := New(newTestRuntime(t))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/state", nil))

	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	var f Frame
	if err := json.Unmarshal(rec.Body.Bytes(), &f); err != nil {
		t.Fatal(err)
	}
	if f.Scans != 1 || f.TS == 0 {
		t.Errorf("frame meta = %+v", f)
	}
	if out, ok := f.Tags["Out"].(float64); !ok || out != 25.0 {
		t.Errorf("Out = %v, want 25.0", f.Tags["Out"])
	}
}

func TestWriteTag(t *testing.T) {
	rt := newTestRuntime(t)
	srv := New(rt)

	body := bytes.NewBufferString(`{"name": "SP", "value": 80.0}`)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/api/tags", body))
	if rec.Code != 204 {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	if got := rt.Tags().Real("SP"); got != 80.0 {
		t.Errorf("SP = %v", got)
	}

	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/api/tags", strings.NewReader("{}")))
	if rec.Code != 400 {
		t.Errorf("empty name accepted: %d", rec.Code)
	}

	// A non-scalar value the runtime can't store must be rejected, not
	// silently accepted with a 204 that wrote nothing.
	rec = httptest.NewRecorder()
	body = bytes.NewBufferString(`{"name": "Mode", "value": "AUTO"}`)
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/api/tags", body))
	if rec.Code != 422 {
		t.Errorf("string value status = %d, want 422", rec.Code)
	}
	if _, ok := rt.Tags().All()["Mode"]; ok {
		t.Error("rejected write should not have created the tag")
	}
}

func TestStreamDeliversFrames(t *testing.T) {
	rt := newTestRuntime(t)
	srv := New(rt, Options{Interval: 5 * time.Millisecond})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Run(ctx)

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := ts.Client().Get(ts.URL + "/api/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type = %q", ct)
	}

	// The immediate greeting frame plus at least one broadcast tick.
	sc := bufio.NewScanner(resp.Body)
	frames := 0
	for sc.Scan() && frames < 2 {
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var f Frame
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &f); err != nil {
			t.Fatalf("bad frame %q: %v", line, err)
		}
		if _, ok := f.Tags["Level"]; !ok {
			t.Fatalf("frame missing tags: %+v", f)
		}
		frames++
	}
	if frames < 2 {
		t.Fatalf("got %d frames, want 2 (scan err %v)", frames, sc.Err())
	}
}

func TestWriteAuthBaseLayer(t *testing.T) {
	srv := New(newTestRuntime(t)) // no token → same-origin-only writes
	post := func(setup func(*http.Request)) int {
		req := httptest.NewRequest("POST", "http://plc.local/api/tags",
			bytes.NewBufferString(`{"name":"SP","value":70}`))
		if setup != nil {
			setup(req)
		}
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec.Code
	}

	// No Origin (curl, the extension, server-to-server): allowed.
	if code := post(nil); code != 204 {
		t.Errorf("no-Origin write = %d, want 204", code)
	}
	// Same-origin browser page: allowed.
	if code := post(func(r *http.Request) { r.Header.Set("Origin", "http://plc.local") }); code != 204 {
		t.Errorf("same-origin write = %d, want 204", code)
	}
	// Cross-origin drive-by: refused.
	if code := post(func(r *http.Request) { r.Header.Set("Origin", "http://evil.example") }); code != 403 {
		t.Errorf("cross-origin write = %d, want 403", code)
	}
}

func TestWriteAuthToken(t *testing.T) {
	srv := New(newTestRuntime(t), Options{AuthToken: "s3cret"})
	post := func(setup func(*http.Request)) int {
		req := httptest.NewRequest("POST", "http://plc.local/api/tags",
			bytes.NewBufferString(`{"name":"SP","value":70}`))
		if setup != nil {
			setup(req)
		}
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec.Code
	}

	// Same-origin but no token: refused now that a token is required.
	if code := post(func(r *http.Request) { r.Header.Set("Origin", "http://plc.local") }); code != 401 {
		t.Errorf("no-token write = %d, want 401", code)
	}
	// Correct token via either header: allowed, even cross-origin.
	if code := post(func(r *http.Request) {
		r.Header.Set("Origin", "http://dashboard.example")
		r.Header.Set("X-Nautilus-Token", "s3cret")
	}); code != 204 {
		t.Errorf("X-Nautilus-Token write = %d, want 204", code)
	}
	if code := post(func(r *http.Request) { r.Header.Set("Authorization", "Bearer s3cret") }); code != 204 {
		t.Errorf("Bearer write = %d, want 204", code)
	}
	// Wrong token: refused.
	if code := post(func(r *http.Request) { r.Header.Set("X-Nautilus-Token", "nope") }); code != 401 {
		t.Errorf("wrong-token write = %d, want 401", code)
	}
}

func TestLandingPage(t *testing.T) {
	srv := New(newTestRuntime(t))

	// "/" serves the dashboard.
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 200 {
		t.Fatalf("GET / status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("GET / content-type = %q", ct)
	}
	if !strings.Contains(rec.Body.String(), "nautilus") || !strings.Contains(rec.Body.String(), "/api/stream") {
		t.Error("landing page missing expected content")
	}

	// An unknown path still 404s (the catch-all only serves exactly "/").
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/nope", nil))
	if rec.Code != 404 {
		t.Errorf("GET /nope status = %d, want 404", rec.Code)
	}
}

func TestCORSPreflight(t *testing.T) {
	srv := New(newTestRuntime(t))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("OPTIONS", "/api/tags", nil))
	if rec.Code != 204 || rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf("preflight: code=%d headers=%v", rec.Code, rec.Header())
	}
}

func TestFrameCarriesScanDiagnostics(t *testing.T) {
	srv := New(newTestRuntime(t))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/state", nil))
	var f Frame
	if err := json.Unmarshal(rec.Body.Bytes(), &f); err != nil {
		t.Fatal(err)
	}
	s := f.Scan
	if s.Count != 1 || s.TargetMs != 100 || s.LastMs <= 0 || s.ExecUs <= 0 {
		t.Errorf("scan stats wrong: %+v", s)
	}
	if len(s.Recent) != 1 || len(s.Histogram) != 15 {
		t.Errorf("history wrong: recent=%d histogram=%d", len(s.Recent), len(s.Histogram))
	}
	if !s.IOHealthy {
		t.Error("memory driver should report healthy IO")
	}
}

func TestMetaEndpoint(t *testing.T) {
	drv := nio.NewMemory()
	rt, err := runtime.New(runtime.Options{
		Program: testProgram,
		Driver:  drv,
		Inputs:  []string{"Level"},
		Outputs: []string{"Out"},
		Seed:    nio.Values{"Level": 40.0, "SP": 65.0, "Out": 0.0},
		Meta: map[string]runtime.TagMeta{
			"Level": {Desc: "Tank level", Unit: "%"},
			"SP":    {Desc: "Setpoint", Unit: "°C"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := New(rt)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/meta", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	var m metaResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if m.Tags["Level"].Desc != "Tank level" || m.Tags["Level"].Unit != "%" {
		t.Errorf("Level meta = %+v", m.Tags["Level"])
	}
	if len(m.Inputs) != 1 || m.Inputs[0] != "Level" || len(m.Outputs) != 1 || m.Outputs[0] != "Out" {
		t.Errorf("io lists = %v / %v", m.Inputs, m.Outputs)
	}
	if m.ScanTargetMs != 100 {
		t.Errorf("ScanTargetMs = %v, want 100", m.ScanTargetMs)
	}
}
