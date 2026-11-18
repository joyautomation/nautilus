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
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "http://localhost/api/state", nil))

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
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "http://localhost/api/tags", body))
	if rec.Code != 204 {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	if got := rt.Tags().Real("SP"); got != 80.0 {
		t.Errorf("SP = %v", got)
	}

	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "http://localhost/api/tags", strings.NewReader("{}")))
	if rec.Code != 400 {
		t.Errorf("empty name accepted: %d", rec.Code)
	}

	// A name no tag declares is refused with a 400 naming it, whatever the
	// value: the write would otherwise create a stray top-level tag no
	// program reads (#329). The same holds for a string and for a number.
	for _, c := range []struct{ name, body string }{
		{"Mode", `{"name": "Mode", "value": "AUTO"}`},
		{"Bias", `{"name": "Bias", "value": 2.5}`},
	} {
		rec = httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "http://localhost/api/tags", bytes.NewBufferString(c.body)))
		if rec.Code != 400 || !strings.Contains(rec.Body.String(), "undefined tag "+c.name) {
			t.Errorf("write to undeclared %s: status %d, body %q; want 400 naming it", c.name, rec.Code, rec.Body)
		}
		if _, ok := rt.Tags().All()[c.name]; ok {
			t.Errorf("a refused write created the tag %s", c.name)
		}
	}
	// A value the declared tag can't store is still rejected, not silently
	// accepted with a 204 that wrote nothing.
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "http://localhost/api/tags", bytes.NewBufferString(`{"name": "SP", "value": "AUTO"}`)))
	if rec.Code != 422 {
		t.Errorf("string value to a REAL tag: status = %d, want 422", rec.Code)
	}
	// A member path under an unknown name creates nothing: it is refused
	// with the reason, and neither the dotted name nor its root appears.
	rec = httptest.NewRecorder()
	body = bytes.NewBufferString(`{"name": "Nope.Speed", "value": 1.0}`)
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "http://localhost/api/tags", body))
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "Nope") {
		t.Errorf("member write under an unknown tag: status %d, body %q; want 400 naming it", rec.Code, rec.Body)
	}
	all := rt.Tags().All()
	for _, name := range []string{"Nope", "Nope.Speed"} {
		if _, ok := all[name]; ok {
			t.Errorf("a refused member write created tag %q", name)
		}
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
		req := httptest.NewRequest("POST", "http://192.168.1.10/api/tags",
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
	// Same-origin browser page (by the controller's address): allowed.
	if code := post(func(r *http.Request) { r.Header.Set("Origin", "http://192.168.1.10") }); code != 204 {
		t.Errorf("same-origin write = %d, want 204", code)
	}
	// Cross-origin drive-by: refused.
	if code := post(func(r *http.Request) { r.Header.Set("Origin", "http://evil.example") }); code != 403 {
		t.Errorf("cross-origin write = %d, want 403", code)
	}
}

// #811: without a token, a browser write is trusted only when addressed to
// a Host no page can rebind. A page on an attacker's domain, rebound to this
// machine, sends Host and Origin both as that domain: they match, so the
// same-origin rule alone let it write.
func TestWriteAuthRefusesARebindableHost(t *testing.T) {
	post := func(srv *Server, host, origin, token string) int {
		req := httptest.NewRequest("POST", "http://"+host+"/api/tags",
			bytes.NewBufferString(`{"name":"SP","value":70}`))
		req.Host = host
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		if token != "" {
			req.Header.Set("X-Nautilus-Token", token)
		}
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec.Code
	}
	open := New(newTestRuntime(t), Options{AllowedHosts: []string{"EPIC1.plant.lan", "hmi.local:8086", "panel.lan."}})
	for _, tc := range []struct {
		what, host, origin string
		want               int
	}{
		{"rebound name, matching Origin", "evil.example:8080", "http://evil.example:8080", 403},
		{"unconfigured LAN name", "plc.local:8080", "http://plc.local:8080", 403},
		{"mismatched Origin", "localhost:8080", "http://evil.example:8080", 403},
		{"loopback name", "localhost:8080", "http://localhost:8080", 204},
		{"a *.localhost name", "app.localhost:8080", "http://app.localhost:8080", 204},
		{"loopback address", "127.0.0.1:8080", "http://127.0.0.1:8080", 204},
		{"IPv6 loopback", "[::1]:8080", "http://[::1]:8080", 204},
		{"a LAN address", "192.168.82.10:8080", "http://192.168.82.10:8080", 204},
		{"a configured name (case-insensitive)", "epic1.PLANT.lan:8080", "http://epic1.PLANT.lan:8080", 204},
		{"a configured name given with a port", "hmi.local:8080", "http://hmi.local:8080", 204},
		{"a configured name given with a root dot", "panel.lan:8080", "http://panel.lan:8080", 204},
		{"no Origin (not a browser), by IP", "192.168.82.10:8080", "", 204},
	} {
		if got := post(open, tc.host, tc.origin, ""); got != tc.want {
			t.Errorf("%s (Host %s, Origin %q): %d, want %d", tc.what, tc.host, tc.origin, got, tc.want)
		}
	}
	// A valid token authorizes the write whatever the Host.
	locked := New(newTestRuntime(t), Options{AuthToken: "s3cret"})
	if got := post(locked, "evil.example:8080", "http://evil.example:8080", "s3cret"); got != 204 {
		t.Errorf("rebound name with the token: %d, want 204", got)
	}
	// Every state-changing route shares the rule: a force from a rebound
	// page is refused too.
	req := httptest.NewRequest("POST", "http://evil.example:8080/api/forces", bytes.NewBufferString(`{"name":"SP","value":1}`))
	req.Header.Set("Origin", "http://evil.example:8080")
	rec := httptest.NewRecorder()
	open.Handler().ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Errorf("rebound force: %d, want 403", rec.Code)
	}
}

// #815: a browser page on another site cannot read the API: the server
// echoes Access-Control-Allow-Origin (with Vary: Origin) only for a page
// on this machine, the controller's own trusted host on another port, an
// allowed name, or a request with the token. It once sent "*".
func TestCORSAllowsOnlyOriginsThatCannotBeAnotherSite(t *testing.T) {
	get := func(srv *Server, method, path, host, origin, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "http://"+host+path, nil)
		req.Host = host
		req.Header.Set("Origin", origin)
		if token != "" {
			req.Header.Set("X-Nautilus-Token", token)
		}
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec
	}
	open := New(newTestRuntime(t), Options{AllowedHosts: []string{"hmi.plant.lan"}})
	for _, tc := range []struct {
		what, method, path, host, origin string
		echo                             bool
	}{
		{"a website reading tags", "GET", "/api/state", "localhost:8080", "https://evil.example", false},
		{"a website reading the program source", "GET", "/api/program", "192.168.1.10:8080", "https://evil.example", false},
		{"a website on another IP", "GET", "/api/state", "192.168.1.10:8080", "http://203.0.113.7", false},
		{"a sandboxed frame", "GET", "/api/state", "localhost:8080", "null", false},
		{"a website preflighting a write", "OPTIONS", "/api/tags", "localhost:8080", "https://evil.example", false},
		{"a rebound name", "GET", "/api/state", "evil.example:8080", "http://evil.example:3000", false},
		{"a name that starts with localhost", "GET", "/api/state", "localhost:8080", "http://localhost.evil.com", false},
		{"a name that resolves to loopback", "GET", "/api/state", "localhost:8080", "http://127.0.0.1.nip.io", false},
		{"a dev server on this machine", "GET", "/api/state", "localhost:8080", "http://localhost:5173", true},
		{"a dev server by 127.0.0.1", "GET", "/api/state", "192.168.1.10:8080", "http://127.0.0.1:5173", true},
		{"the controller's own address, another port", "GET", "/api/state", "192.168.1.10:8080", "http://192.168.1.10:3000", true},
		{"an allowed name", "GET", "/api/state", "192.168.1.10:8080", "https://hmi.plant.lan", true},
		{"a dev server's preflight", "OPTIONS", "/api/tags", "localhost:8080", "http://localhost:5173", true},
	} {
		rec := get(open, tc.method, tc.path, tc.host, tc.origin, "")
		got := rec.Header().Get("Access-Control-Allow-Origin")
		if tc.echo && got != tc.origin || !tc.echo && got != "" {
			t.Errorf("%s (Origin %s): Access-Control-Allow-Origin %q, echo want %v", tc.what, tc.origin, got, tc.echo)
		}
		if rec.Header().Get("Vary") != "Origin" {
			t.Errorf("%s: Vary %q, want Origin", tc.what, rec.Header().Get("Vary"))
		}
	}
	// Vary: Origin is sent with or without an Origin, so a cache keeps
	// one origin's answer from another.
	if v := get(open, "GET", "/api/state", "localhost:8080", "", "").Header().Get("Vary"); v != "Origin" {
		t.Errorf("no-Origin request: Vary %q, want Origin", v)
	}
	// With a token configured: a request carrying it may read from any
	// origin (the authorized cross-origin client), and its preflight is
	// answered so it can send the token header; without the token, still
	// nothing.
	locked := New(newTestRuntime(t), Options{AuthToken: "s3cret"})
	if got := get(locked, "GET", "/api/state", "localhost:8080", "https://hmi.cloud.example", "s3cret").Header().Get("Access-Control-Allow-Origin"); got != "https://hmi.cloud.example" {
		t.Errorf("token reader: %q", got)
	}
	if got := get(locked, "GET", "/api/state", "localhost:8080", "https://evil.example", "").Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("tokenless reader on a token controller: %q", got)
	}
	if got := get(locked, "OPTIONS", "/api/tags", "localhost:8080", "https://hmi.cloud.example", "").Header().Get("Access-Control-Allow-Origin"); got != "https://hmi.cloud.example" {
		t.Errorf("preflight on a token controller: %q", got)
	}
}

// #819: without a token the server answers only requests addressed to a
// Host no page can rebind, reads included: a rebound page is same-origin
// with the rebound name, sends no Origin on a GET, and CORS never applies,
// so the Host is the one thing that tells it apart.
func TestGuardHostRefusesEveryRouteOnARebindableHost(t *testing.T) {
	get := func(srv *Server, method, host, path string) int {
		req := httptest.NewRequest(method, "http://"+host+path, nil)
		req.Host = host
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec.Code
	}
	open := New(newTestRuntime(t), Options{OnlineEdits: true, AllowedHosts: []string{"epic1.plant.lan"}})
	withHMI := New(newTestRuntime(t), Options{HMI: testHMI()})
	for _, path := range []string{"/api/state", "/api/program", "/api/meta", "/api/alarms", "/api/history", "/api/forces", "/api/drivers", "/"} {
		if got := get(open, "GET", "evil.example:8080", path); got != http.StatusForbidden {
			t.Errorf("GET %s by a rebound name: %d, want 403", path, got)
		}
		for _, host := range []string{"localhost:8080", "192.168.82.10:8080", "epic1.plant.lan:8080"} {
			if got := get(open, "GET", host, path); got == http.StatusForbidden {
				t.Errorf("GET %s by %s: 403, want it answered", path, host)
			}
		}
	}
	if got := get(open, "POST", "evil.example:8080", "/api/tags"); got != http.StatusForbidden {
		t.Errorf("a no-Origin POST by a rebound name: %d, want 403", got)
	}
	// The HMI and its assets.
	if got := get(withHMI, "GET", "evil.example:8080", "/"); got != http.StatusForbidden {
		t.Errorf("the HMI by a rebound name: %d, want 403", got)
	}
	if got := get(withHMI, "GET", "localhost:8080", "/"); got != http.StatusOK {
		t.Errorf("the HMI on localhost: %d, want 200", got)
	}
	// The live stream refuses before it starts streaming (bounded, so an
	// unguarded stream fails the test instead of hanging it).
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	sreq := httptest.NewRequest("GET", "http://evil.example:8080/api/stream", nil).WithContext(ctx)
	sreq.Host = "evil.example:8080"
	srec := httptest.NewRecorder()
	open.Handler().ServeHTTP(srec, sreq)
	if srec.Code != http.StatusForbidden {
		t.Errorf("the stream by a rebound name: %d, want 403", srec.Code)
	}
	// With a token configured, reads stay ungated, as documented.
	locked := New(newTestRuntime(t), Options{AuthToken: "s3cret"})
	if got := get(locked, "GET", "evil.example:8080", "/api/state"); got != http.StatusOK {
		t.Errorf("a read on a token controller: %d, want 200", got)
	}
}

// #819: New reads NAUTILUS_ALLOWED_HOSTS itself, so every embedder (naut
// run, the serve commands, an SDK program calling server.New) honours the
// setting its 403 names; and a request Host's root dot is dropped as a
// configured name's is.
func TestNewReadsAllowedHostsFromTheEnvironment(t *testing.T) {
	t.Setenv("NAUTILUS_ALLOWED_HOSTS", " epic1.plant.lan, ,hmi.local:8086 ,ok.example. ")
	srv := New(newTestRuntime(t)) // no Options: an SDK program's default
	for host, want := range map[string]int{
		"epic1.plant.lan:8080": 200, "hmi.local:8080": 200, "ok.example:8080": 200,
		"ok.example.:8080": 200, "epic1.plant.lan.:8080": 200, "other.plant.lan:8080": 403,
	} {
		req := httptest.NewRequest("GET", "http://"+host+"/api/state", nil)
		req.Host = host
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("GET by %s: %d, want %d", host, rec.Code, want)
		}
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
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "http://localhost/", nil))
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
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "http://localhost/nope", nil))
	if rec.Code != 404 {
		t.Errorf("GET /nope status = %d, want 404", rec.Code)
	}
}

func TestCORSPreflight(t *testing.T) {
	srv := New(newTestRuntime(t))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("OPTIONS", "http://localhost/api/tags", nil)
	req.Header.Set("Origin", "http://localhost:5173") // a dev server on this machine
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != 204 || rec.Header().Get("Access-Control-Allow-Origin") != "http://localhost:5173" {
		t.Errorf("preflight: code=%d headers=%v", rec.Code, rec.Header())
	}
}

func TestFrameCarriesScanDiagnostics(t *testing.T) {
	srv := New(newTestRuntime(t))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "http://localhost/api/state", nil))
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
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "http://localhost/api/meta", nil))
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

// A browser page on another origin preflights every non-simple request. The
// preflight has to allow each method a route answers, or the browser never
// sends the request: PUT /api/program (an online edit) and DELETE
// /api/forces/{name} as well as the GETs and POSTs.
func TestCORSPreflightAllowsEveryMethodTheAPIServes(t *testing.T) {
	srv := New(newTestRuntime(t), Options{OnlineEdits: true})
	for _, tc := range []struct{ method, path string }{
		{"GET", "/api/state"},
		{"POST", "/api/tags"},
		{"PUT", "/api/program"},
		{"DELETE", "/api/forces/Level"},
	} {
		rec := call(t, srv, "OPTIONS", tc.path, "", func(r *http.Request) {
			r.Header.Set("Origin", "http://localhost:5173")
			r.Header.Set("Access-Control-Request-Method", tc.method)
		})
		if rec.Code != http.StatusNoContent {
			t.Errorf("preflight for %s %s = %d, want 204", tc.method, tc.path, rec.Code)
		}
		allowed := strings.Split(rec.Header().Get("Access-Control-Allow-Methods"), ",")
		ok := false
		for _, m := range allowed {
			ok = ok || strings.TrimSpace(m) == tc.method
		}
		if !ok {
			t.Errorf("preflight for %s %s allows %q, not %s", tc.method, tc.path, rec.Header().Get("Access-Control-Allow-Methods"), tc.method)
		}
	}
}
