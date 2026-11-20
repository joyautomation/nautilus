package server

import (
	"bufio"
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/auth"
	"github.com/joyautomation/nautilus/auth/users"
	"github.com/joyautomation/nautilus/leader"
)

// readRoutes is every GET pattern under /api/ the server registers, read
// from its source as writeRoutes reads the write routes, so a read route
// added later is gated by the test without being listed here.
func readRoutes(t *testing.T) []string {
	t.Helper()
	fset := token.NewFileSet()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) != 2 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || (sel.Sel.Name != "Handle" && sel.Sel.Name != "HandleFunc") {
				return true
			}
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			pattern, _ := strconv.Unquote(lit.Value)
			if method, p, ok := strings.Cut(pattern, " "); ok && method == "GET" && strings.HasPrefix(p, "/api/") {
				out = append(out, p)
			}
			return true
		})
	}
	if len(out) < 20 {
		t.Fatalf("found %d read routes: the scan is broken", len(out))
	}
	return out
}

// concrete fills a pattern's wildcards.
func concrete(p string) string {
	r := strings.NewReplacer("{port}", "LEVEL1", "{ref}", "V_Setpoint", "{id}", "s1")
	return r.Replace(p)
}

// readPolicy is an auth: block with a viewer token, a token whose role has
// every permission but read, and anonymous as given.
func readPolicy(t *testing.T, srv *Server, anonymous string) {
	t.Helper()
	p, err := auth.Compile(auth.Settings{
		Anonymous: anonymous,
		Roles:     map[string]auth.RoleDef{"blind": {Inherits: auth.Administrator, Deny: []string{auth.Read}}},
		ServiceTokens: map[string][]auth.Grant{
			"viewer": {{Role: auth.Viewer}},
			"blind":  {{Role: "blind"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	srv.policy, srv.authToken = p, "admin-secret"
	srv.services = map[string]string{"viewer": "v-secret", "blind": "b-secret"}
}

// gated reports a refusal by the read gate, as opposed to whatever the
// route itself answers (404 with no alarm engine, 400 for a bad query…).
func gated(rec *httptest.ResponseRecorder) bool {
	return rec.Code == http.StatusUnauthorized || rec.Code == http.StatusForbidden
}

// With an auth: block every API read needs read, the live stream among
// them: anonymous gets 401 (anonymous: none, the default), a role without
// read 403, an unknown token 401 even where anonymous may read. A refusal
// carries the error and nothing of the route's. /api/me alone answers
// everyone. Without an auth: block nothing is refused.
func TestEveryReadRouteIsGated(t *testing.T) {
	routes := readRoutes(t)
	// A stream that is let in runs until its client leaves: this one
	// leaves after 300 ms, so an allowed stream answers 200 and a stream
	// wrongly let in fails its row rather than hanging the suite.
	strip := func(r *http.Request) {
		r.Header.Del("X-Nautilus-Token")
		if r.URL.Path == "/api/stream" {
			ctx, cancel := context.WithTimeout(r.Context(), 300*time.Millisecond)
			t.Cleanup(cancel)
			*r = *r.WithContext(ctx)
		}
	}
	for _, anon := range []string{"", "none", "viewer"} {
		srv := auditServer(t, Options{})
		readPolicy(t, srv, anon)
		for _, p := range routes {
			path := concrete(p)
			for _, method := range []string{"GET", "HEAD"} {
				// No credential: /api/me answers; under anonymous: viewer a
				// read is allowed (the stream is then not opened here).
				switch {
				case p == "/api/me":
					if rec := call(t, srv, method, path, "", strip); gated(rec) {
						t.Errorf("anonymous %q: %s %s: %d, want /api/me to answer anyone", anon, method, path, rec.Code)
					}
					continue
				case anon == "viewer":
					if p == "/api/audit" { // it needs audit too
						break
					}
					if rec := call(t, srv, method, path, "", strip); gated(rec) {
						t.Errorf("anonymous: viewer: %s %s: %d %s", method, path, rec.Code, rec.Body)
					}
				default:
					rec := call(t, srv, method, path, "", strip)
					if rec.Code != http.StatusUnauthorized || (method == "GET" && !strings.Contains(rec.Body.String(), "sign in")) || rec.Header().Get("Cache-Control") != "no-store" {
						t.Errorf("anonymous %q: %s %s: %d %s (Cache-Control %q), want a no-store 401", anon, method, path, rec.Code, rec.Body, rec.Header().Get("Cache-Control"))
					}
				}
				if rec := call(t, srv, method, path, "", strip, bearer("guess")); rec.Code != http.StatusUnauthorized {
					t.Errorf("anonymous %q: an unknown token: %s %s: %d, want 401", anon, method, path, rec.Code)
				}
				if rec := call(t, srv, method, path, "", strip, bearer("b-secret")); rec.Code != http.StatusForbidden {
					t.Errorf("anonymous %q: a role without read: %s %s: %d, want 403", anon, method, path, rec.Code)
				} else if method == "GET" && rec.Body.String() != `{"error":"`+srv.policy.Can(auth.Service("blind"), auth.Read, auth.All).Reason+`"}`+"\n" {
					t.Errorf("a refusal carries more than its error: %s", rec.Body)
				}
				if p == "/api/audit" {
					continue // it needs audit as well
				}
				if rec := call(t, srv, method, path, "", strip, bearer("v-secret")); gated(rec) {
					t.Errorf("a viewer: %s %s: %d %s", method, path, rec.Code, rec.Body)
				}
			}
		}
	}
	srv := auditServer(t, Options{})
	for _, p := range routes {
		if rec := call(t, srv, "GET", concrete(p), "", strip); gated(rec) {
			t.Errorf("no auth: block: GET %s: %d %s", p, rec.Code, rec.Body)
		}
	}
}

// A signed-in browser reads by its cookie; a cookie the controller does
// not know reads nothing.
func TestCookieReads(t *testing.T) {
	srv, _, _ := patServer(t)
	c := signIn(t, srv, "jsmith")
	if rec := call(t, srv, "GET", "/api/state", "", withCookie(c.Value)); rec.Code != http.StatusOK {
		t.Errorf("a signed-in read: %d %s", rec.Code, rec.Body)
	}
	if rec := call(t, srv, "GET", "/api/state", "", withCookie("forgotten")); rec.Code != http.StatusUnauthorized {
		t.Errorf("an unknown cookie's read: %d", rec.Code)
	}
}

// A facade wraps the server's handler, already guarded, in GuardHost again
// with routes of its own (the Logix program source): the gate covers those
// too, and a reader passes both.
func TestFacadeRoutesAreGated(t *testing.T) {
	srv := auditServer(t, Options{})
	readPolicy(t, srv, "")
	served := 0
	mux := http.NewServeMux()
	mux.Handle("/", srv.Handler())
	mux.HandleFunc("GET /api/program", func(w http.ResponseWriter, _ *http.Request) { served++; w.Write([]byte("PROGRAM Main")) })
	h := srv.GuardHost(mux)
	get := func(mod ...func(*http.Request)) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "http://localhost/api/program", nil)
		for _, m := range mod {
			m(r)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}
	if rec := get(); rec.Code != http.StatusUnauthorized || served != 0 {
		t.Errorf("anonymous: %d, served %d", rec.Code, served)
	}
	if rec := get(bearer("v-secret")); rec.Code != http.StatusOK || served != 1 {
		t.Errorf("a viewer: %d, served %d", rec.Code, served)
	}
	r := httptest.NewRequest("GET", "http://localhost/api/state", nil)
	r.Header.Set("Authorization", "Bearer v-secret")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Errorf("a viewer through both guards: %d", rec.Code)
	}
}

// On the leader /api/cluster needs read like every read. On a forwarding
// standby, which answers /api/cluster and /api/history itself and holds
// no sessions, a browser's cookie gets the leader's address (503), never
// the standby's data; a bearer reader is answered there.
func TestClusterReadsAndTheStandby(t *testing.T) {
	srv, toks, _ := patServer(t)
	fc := &flipCluster{st: leading}
	srv.cluster = fc
	srv.leaderURLs = map[string]string{"b": "https://plc-b.example:8443"}
	if rec := call(t, srv, "GET", "/api/cluster", ""); rec.Code != http.StatusUnauthorized || strings.Contains(rec.Body.String(), "isLeader") {
		t.Errorf("the leader, no credential: %d %s, want 401", rec.Code, rec.Body)
	}
	if rec := call(t, srv, "GET", "/api/cluster", "", bearer(toks["akhan"])); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "isLeader") {
		t.Errorf("the leader, a PAT: %d %s", rec.Code, rec.Body)
	}
	fc.set(standing)
	for _, p := range []string{"/api/cluster", "/api/history?tags=SP", "/api/state"} {
		rec := call(t, srv, "GET", p, "", withCookie("a-session-of-the-leader"))
		if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Location") != "https://plc-b.example:8443/" || strings.Contains(rec.Body.String(), "isLeader") {
			t.Errorf("the standby, %s with a cookie: %d %q %s, want the referral", p, rec.Code, rec.Header().Get("Location"), rec.Body)
		}
		if rec := call(t, srv, "GET", p, ""); rec.Code != http.StatusUnauthorized {
			t.Errorf("the standby, %s with nothing: %d, want 401", p, rec.Code)
		}
	}
	if rec := call(t, srv, "GET", "/api/cluster", "", bearer(toks["akhan"])); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"isLeader":false`) {
		t.Errorf("the standby, a PAT: %d %s", rec.Code, rec.Body)
	}
	// A bad token beside the cookie is the bad token's 401, not a referral.
	if rec := call(t, srv, "GET", "/api/cluster", "", withCookie("a-session-of-the-leader"), bearer("guess")); rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "invalid credential") {
		t.Errorf("the standby, a cookie and a bad token: %d %s, want 401", rec.Code, rec.Body)
	}
	// A page opened there goes to the sign-in page, which names the leader.
	if rec := call(t, srv, "GET", "/", "", withCookie("a-session-of-the-leader"), navigate); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/signin?next=%2F" {
		t.Errorf("the standby, a page with a cookie: %d %q, want the sign-in page", rec.Code, rec.Header().Get("Location"))
	}
	// Not a forwarding standby (no leader known): there is nobody to refer
	// to, so the cookie it does not know is anonymous.
	fc.set(electing)
	if rec := call(t, srv, "GET", "/api/cluster", "", withCookie("a-session-of-the-leader")); rec.Code != http.StatusUnauthorized {
		t.Errorf("electing, an unknown cookie: %d, want 401", rec.Code)
	}
}

// /healthz and /readyz answer with no credential and by any Host, under
// an auth: block that lets anonymous read nothing, and say one word:
// /healthz 200 "ok" on every replica; /readyz 200 "ok" on the controller
// that serves (no redundancy, or the leader), 503 "not ready" on a
// standby, while no leader is known, and on a lease holder whose gate is
// closed.
func TestProbes(t *testing.T) {
	held := leader.Status{Mode: "quorum", Pod: "a", Leader: "a", LeaderAddr: "10.0.0.7:8086"}
	cases := []struct {
		name  string
		st    *leader.Status
		ready bool
	}{
		{"no redundancy", nil, true},
		{"leader", &leading, true},
		{"standby", &standing, false},
		{"no leader known", &electing, false},
		{"lease held, gate closed", &held, false},
	}
	for _, c := range cases {
		srv, _, _ := patServer(t)
		if c.st != nil {
			srv.cluster = fakeCluster{status: *c.st}
		}
		for _, p := range []string{"/healthz", "/readyz"} {
			r := httptest.NewRequest("GET", "http://rebound.example"+p, nil)
			r.AddCookie(&http.Cookie{Name: plainCookie, Value: "stale"})
			rec := serve(srv, r)
			code, body := http.StatusOK, "ok"
			if p == "/readyz" && !c.ready {
				code, body = http.StatusServiceUnavailable, "not ready"
			}
			if rec.Code != code || rec.Body.String() != body {
				t.Errorf("%s: %s: %d %q, want %d %q", c.name, p, rec.Code, rec.Body, code, body)
			}
			if rec.Header().Get("Location") != "" || len(rec.Result().Cookies()) != 0 ||
				rec.Header().Get("Content-Type") != "text/plain; charset=utf-8" || rec.Header().Get("Cache-Control") != "no-store" {
				t.Errorf("%s: %s says more: %v", c.name, p, rec.Header())
			}
			if rec := serve(srv, httptest.NewRequest("HEAD", "http://localhost"+p, nil)); rec.Code != code {
				t.Errorf("%s: HEAD %s: %d", c.name, p, rec.Code)
			}
		}
	}
	// The HMI's SPA fallback does not answer them with its index.html.
	srv := New(newTestRuntime(t), Options{HMI: testHMI()})
	if rec := call(t, srv, "GET", "/healthz", ""); rec.Body.String() != "ok" {
		t.Errorf("with an HMI: /healthz = %q", rec.Body)
	}
}

func navigate(r *http.Request) { r.Header.Set("Sec-Fetch-Mode", "navigate") }

// A browser opening a page with no session is sent to the sign-in page,
// which brings it back; a browser that sends no Sec-Fetch headers (plain
// HTTP by address) by its Accept. A page's own files are fetched as
// before, the sign-in page opens, a next that would leave the controller
// becomes "/", and a signed-in browser gets the page. A signed-in user who
// may not read gets 403, not a loop through the sign-in page, and a
// controller with no sign-in (a facade) a 401.
func TestPagesNeedASession(t *testing.T) {
	srv, _, _ := patServer(t)
	for _, c := range []struct {
		name, path string
		mod        func(*http.Request)
		code       int
		location   string
	}{
		{"navigate", "/?view=tags", navigate, http.StatusSeeOther, "/signin?next=%2F%3Fview%3Dtags"},
		{"by Accept", "/", func(r *http.Request) { r.Header.Set("Accept", "text/html,*/*") }, http.StatusSeeOther, "/signin?next=%2F"},
		{"away", "//evil.example/", navigate, http.StatusSeeOther, "/signin?next=%2F"}, // safeNext's fallback
		{"a page's script", "/assets/favicon.svg", func(r *http.Request) { r.Header.Set("Sec-Fetch-Mode", "no-cors") }, 0, ""},
		{"the sign-in page", "/signin", navigate, http.StatusOK, ""},
	} {
		rec := call(t, srv, "GET", c.path, "", c.mod)
		if c.code == 0 {
			if rec.Code == http.StatusSeeOther || gated(rec) {
				t.Errorf("%s: %d", c.name, rec.Code)
			}
			continue
		}
		if rec.Code != c.code || rec.Header().Get("Location") != c.location {
			t.Errorf("%s: %d %q, want %d %q", c.name, rec.Code, rec.Header().Get("Location"), c.code, c.location)
		}
		if c.code == http.StatusSeeOther && rec.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s: a redirect a cache may keep: %q", c.name, rec.Header().Get("Cache-Control"))
		}
	}
	c := signIn(t, srv, "akhan")
	if rec := call(t, srv, "GET", "/", "", navigate, withCookie(c.Value)); rec.Code != http.StatusOK {
		t.Errorf("signed in: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	p, err := auth.Compile(auth.Settings{
		Roles: map[string]auth.RoleDef{"blind": {Inherits: auth.Engineer, Deny: []string{auth.Read}}},
		Users: map[string][]auth.Grant{"akhan": {{Role: "blind"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	srv.policy = p
	if rec := call(t, srv, "GET", "/", "", navigate, withCookie(c.Value)); rec.Code != http.StatusForbidden {
		t.Errorf("signed in, no read: %d %q, want 403", rec.Code, rec.Header().Get("Location"))
	}

	facade := New(newTestRuntime(t))
	readPolicy(t, facade, "")
	if rec := call(t, facade, "GET", "/", "", navigate); rec.Code != http.StatusUnauthorized {
		t.Errorf("no sign-in here: %d %q, want 401", rec.Code, rec.Header().Get("Location"))
	}
}

// openAuthStream opens /api/stream on ts with mod applied, reads its first
// frame, and returns a channel closed when the server ends the stream.
func openAuthStream(t *testing.T, ts *httptest.Server, mod ...func(*http.Request)) (status int, ended <-chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/api/stream", nil)
	for _, m := range mod {
		m(req)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return resp.StatusCode, nil
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
		}
	}()
	return resp.StatusCode, done
}

func cookieFrom(c *http.Cookie) func(*http.Request) {
	return func(r *http.Request) { r.AddCookie(&http.Cookie{Name: plainCookie, Value: c.Value}) }
}

func endsWithin(t *testing.T, what string, ended <-chan struct{}, d time.Duration) {
	t.Helper()
	select {
	case <-ended:
	case <-time.After(d):
		t.Errorf("%s: the stream is still open after %v", what, d)
	}
}

func staysOpen(t *testing.T, what string, ended <-chan struct{}, d time.Duration) {
	t.Helper()
	select {
	case <-ended:
		t.Errorf("%s: the stream ended", what)
	case <-time.After(d):
	}
}

// The live stream is gated like any read, and opens with a cookie. A
// sign-out closes that session's streams at once (OnEnd: the periodic
// re-check is an hour away here) and no other's.
func TestSignOutClosesItsStreams(t *testing.T) {
	old := streamRecheck
	streamRecheck = time.Hour
	t.Cleanup(func() { streamRecheck = old })
	srv, _, _ := patServer(t)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close) // after the streams' cancels, which run first
	if code, _ := openAuthStream(t, ts); code != http.StatusUnauthorized {
		t.Errorf("an anonymous stream: %d, want 401", code)
	}
	a, j := signIn(t, srv, "akhan"), signIn(t, srv, "jsmith")
	_, akhan := openAuthStream(t, ts, cookieFrom(a))
	_, jsmith := openAuthStream(t, ts, cookieFrom(j))
	if akhan == nil || jsmith == nil {
		t.Fatal("a signed-in stream did not open")
	}
	call(t, srv, "POST", "/api/signout", "", withCookie(a.Value), ownOrigin, fromLoopback)
	endsWithin(t, "akhan signed out", akhan, 2*time.Second)
	staysOpen(t, "jsmith, still signed in", jsmith, 200*time.Millisecond)
}

// The streams' periodic re-check ends what no hook announces: a session
// that reached its absolute expiry, and a personal access token revoked
// on the box. A stream whose credential holds stays open.
func TestStreamRecheck(t *testing.T) {
	old := streamRecheck
	streamRecheck = 20 * time.Millisecond
	t.Cleanup(func() { streamRecheck = old })
	srv, toks, path := patServer(t)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close) // after the streams' cancels, which run first

	c := signIn(t, srv, "akhan")
	_, expiring := openAuthStream(t, ts, cookieFrom(c))
	_, pat := openAuthStream(t, ts, bearer(toks["jsmith"]))
	_, kept := openAuthStream(t, ts, bearer(toks["akhan"]))
	if expiring == nil || pat == nil || kept == nil {
		t.Fatal("a stream did not open")
	}
	staysOpen(t, "a live session", expiring, 100*time.Millisecond)
	srv.sessions.mu.Lock()
	srv.sessions.now = func() time.Time { return time.Now().Add(13 * time.Hour) }
	srv.sessions.mu.Unlock()
	endsWithin(t, "the session expired", expiring, 2*time.Second)

	f, err := users.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	id, _, _ := strings.Cut(strings.TrimPrefix(toks["jsmith"], users.TokenPrefix), "_")
	if err := f.Revoke("jsmith", id); err != nil {
		t.Fatal(err)
	}
	if err := f.Save(path, "opsadmin"); err != nil {
		t.Fatal(err)
	}
	srv.users.Reload()
	endsWithin(t, "jsmith's token revoked", pat, 2*time.Second)
	staysOpen(t, "akhan's token", kept, 100*time.Millisecond)
}

// A session ended after the gate let its stream in, before the stream was
// registered where a sign-out could find it, still ends the stream: it
// checks once it is registered.
func TestStreamChecksOnceRegistered(t *testing.T) {
	old := streamRecheck
	streamRecheck = time.Hour
	t.Cleanup(func() { streamRecheck = old })
	srv, _, _ := patServer(t)
	c := signIn(t, srv, "akhan")
	srv.sessions.end(c.Value)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	r := httptest.NewRequest("GET", "http://localhost/api/stream", nil).WithContext(ctx)
	r.AddCookie(&http.Cookie{Name: plainCookie, Value: c.Value})
	done := make(chan struct{})
	go func() { srv.handleStream(httptest.NewRecorder(), r); close(done) }()
	select {
	case <-done:
		if ctx.Err() != nil {
			t.Error("the stream ran until its client left")
		}
	case <-time.After(3 * time.Second):
		t.Error("the stream of an ended session is still open")
	}
}

// The read gate answers before withCORS, so its refusals carry the CORS
// answer themselves: a page on an allowed origin (a dev server on
// loopback) reads why it was refused, the 401, the 403 and a standby's
// referral alike, while a page on any other origin is told nothing.
func TestReadGateRefusalsCarryCORS(t *testing.T) {
	srv, toks, _ := patServer(t)
	fc := &flipCluster{st: leading}
	srv.cluster = fc
	srv.leaderURLs = map[string]string{"b": "https://plc-b.example:8443"}
	from := func(origin string) func(*http.Request) {
		return func(r *http.Request) { r.Header.Set("Origin", origin) }
	}
	const dev = "http://localhost:5173"
	// withCORS's rule: an allowed origin, or any origin for a request that
	// carries a valid credential (a PAT whose user holds no role here).
	cases := []struct {
		name     string
		code     int
		anywhere bool
		mod      []func(*http.Request)
	}{
		{"anonymous", http.StatusUnauthorized, false, nil},
		{"invalid credential", http.StatusUnauthorized, false, []func(*http.Request){bearer("guess")}},
		{"ghost holds no role", http.StatusForbidden, true, []func(*http.Request){bearer(toks["ghost"])}},
	}
	for _, c := range cases {
		for _, origin := range []string{dev, "https://elsewhere.example"} {
			rec := call(t, srv, "GET", "/api/state", "", append(c.mod, from(origin))...)
			if rec.Code != c.code {
				t.Fatalf("%s from %s: %d %s, want %d", c.name, origin, rec.Code, rec.Body, c.code)
			}
			got := rec.Header().Get("Access-Control-Allow-Origin")
			if (origin == dev || c.anywhere) && got != origin {
				t.Errorf("%s from %s: Access-Control-Allow-Origin %q, want %q", c.name, origin, got, origin)
			}
			if origin != dev && !c.anywhere && got != "" {
				t.Errorf("%s from another site: Access-Control-Allow-Origin %q, want none", c.name, got)
			}
			if !strings.Contains(strings.Join(rec.Header().Values("Vary"), ","), "Origin") {
				t.Errorf("%s from %s: no Vary: Origin", c.name, origin)
			}
		}
	}
	fc.set(standing)
	for _, origin := range []string{dev, "https://elsewhere.example"} {
		rec := call(t, srv, "GET", "/api/state", "", withCookie("a-session-of-the-leader"), from(origin))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("the standby's referral from %s: %d %s", origin, rec.Code, rec.Body)
		}
		got := rec.Header().Get("Access-Control-Allow-Origin")
		if (origin == dev) != (got == dev) || (origin != dev && got != "") {
			t.Errorf("the standby's referral from %s: Access-Control-Allow-Origin %q", origin, got)
		}
	}
}

// /api/me says whether this controller offers sign-in (a page must not
// send anyone to a /signin that is not there) and what time it is here
// (a page judges elevatedUntil by the controller's clock, not its own).
func TestMeSaysSignInAndNow(t *testing.T) {
	type me struct {
		Auth   bool      `json:"auth"`
		SignIn bool      `json:"signIn"`
		Now    time.Time `json:"now"`
	}
	read := func(srv *Server) me {
		t.Helper()
		var m me
		rec := call(t, srv, "GET", "/api/me", "")
		if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
			t.Fatalf("/api/me: %v %s", err, rec.Body)
		}
		return m
	}
	before := time.Now().Add(-time.Second)
	withUsers, _, _ := patServer(t)
	if m := read(withUsers); !m.Auth || !m.SignIn || m.Now.Before(before) || m.Now.After(time.Now().Add(time.Second)) {
		t.Errorf("with users: %+v, want auth, signIn and the controller's time", m)
	}
	facade := New(newTestRuntime(t))
	enforce(t, facade) // an auth: block, service tokens, no users file
	if m := read(facade); !m.Auth || m.SignIn || m.Now.IsZero() {
		t.Errorf("an auth: block without users: %+v, want signIn false", m)
	}
	open := New(newTestRuntime(t))
	if m := read(open); m.Auth || m.SignIn || m.Now.IsZero() {
		t.Errorf("no auth: block: %+v, want no auth, no signIn, the time", m)
	}
}
