package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/auth"
	"github.com/joyautomation/nautilus/auth/users"
)

// elevateAs gives srv an auth: block where "boss" is an administrator,
// and a users file holding boss (password "password-boss"). It opens a
// browser session for boss at t0 and returns its id, boss's personal
// access token, and a setter for the session clock.
func elevateAs(t *testing.T, srv *Server) (id, pat string, at func(time.Duration)) {
	t.Helper()
	old := users.Default
	users.Default = users.Params{Memory: 64, Time: 1, Threads: 1}
	t.Cleanup(func() { users.Default = old })
	p, err := auth.Compile(auth.Settings{Users: map[string][]auth.Grant{"boss": {{Role: auth.Administrator}}}})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	os.Chmod(dir, 0o700)
	path := filepath.Join(dir, users.FileName)
	f := users.New()
	if err := f.Add("boss", "password-boss"); err != nil {
		t.Fatal(err)
	}
	pat, _, _ = f.NewToken("boss")
	if err := f.Save(path, "opsadmin"); err != nil {
		t.Fatal(err)
	}
	store, err := users.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	srv.users, srv.policy = store, p
	srv.sessions, srv.signin = newSessions(p.Session()), newSignInGate()
	t0 := time.Now()
	now := t0
	srv.sessions.now = func() time.Time { return now }
	id, _ = srv.sessions.open("boss", credPrint(store.Current().Users["boss"].Password))
	return id, pat, func(d time.Duration) { now = t0.Add(d) }
}

// rowRequest is row's request with no credential of its own.
func rowRequest(row auditRow, mod ...func(*http.Request)) []func(*http.Request) {
	return append([]func(*http.Request){func(r *http.Request) {
		for k, v := range row.header {
			r.Header.Set(k, v)
		}
		r.Header.Del("X-Nautilus-Token")
		r.Header.Del("Authorization")
	}}, mod...)
}

func elevateMarked(body string) bool {
	var m map[string]any
	return json.Unmarshal([]byte(body), &m) == nil && m["elevate"] == true
}

// Every write route whose action needs an elevated permission asks a
// browser sign-in for its password again once auth.elevation has passed
// since it was proved: a 401 marked "elevate", nothing changed, the
// refusal journalled with the credential it was refused to. Within the
// window it is accepted. A personal access token is never asked: it is a
// credential sent with each request. A route whose permission is not
// elevated is not asked either. This pins the seam's check, the set of
// elevated permissions, and that recovery actions (clearing a force,
// rolling back) are asked like the rest (naut-lead, #708 5c).
func TestElevatedActionsNeedAFreshPassword(t *testing.T) {
	asked := map[string]bool{}
	for _, row := range auditRows() {
		if row.result != "ok" || row.person {
			continue
		}
		elevated := auth.Elevated(actionPerm[row.action])
		for _, c := range []struct {
			what string
			at   time.Duration
			pat  bool
			ask  bool
		}{
			{"fresh session", 15*time.Minute - time.Second, false, false},
			{"lapsed session", 15 * time.Minute, false, elevated},
			{"PAT, session lapsed", 15 * time.Minute, true, false},
		} {
			srv := row.server(t)
			if row.prep != nil {
				row.prep(t, srv)
			}
			id, pat, at := elevateAs(t, srv)
			at(c.at)
			cred := withCookie(id)
			if c.pat {
				cred = bearer(pat)
			}
			before, n := writeState(srv), len(srv.audit.ring.Recent())
			rec := call(t, srv, row.method, row.path, row.body, rowRequest(row, cred, ownOrigin)...)
			es := srv.audit.ring.Recent()
			if len(es)-n != 1 {
				t.Fatalf("%s, %s: %d entries", row.name, c.what, len(es)-n)
			}
			e := es[0]
			if c.ask {
				asked[row.action] = true
				if rec.Code != http.StatusUnauthorized || !elevateMarked(rec.Body.String()) || writeState(srv) != before || e.Result != "refused" {
					t.Errorf("%s, %s: %d %s (changed %v), journalled %s; want a 401 marked elevate that changes nothing", row.name, c.what, rec.Code, rec.Body, writeState(srv) != before, e.Result)
				}
			} else if rec.Code >= 300 || elevateMarked(rec.Body.String()) {
				t.Errorf("%s, %s: %d %s; want it accepted", row.name, c.what, rec.Code, rec.Body)
			}
			want := map[bool]string{false: "session", true: "pat"}[c.pat]
			if got, _ := e.Detail["credential"].(string); elevated && got != want {
				t.Errorf("%s, %s: journalled credential %q, want %q", row.name, c.what, got, want)
			} else if !elevated && e.Detail["credential"] != nil {
				t.Errorf("%s, %s: credential journalled for an action that is not elevated: %v", row.name, c.what, e.Detail)
			}
		}
	}
	// The walk reached the recovery actions and every elevated class.
	for _, a := range []string{"force.set", "force.clear", "force.clear-all", "program.edit", "program.rollback", "program.activate", "config.apply", "config.commit", "param.write", "param.import"} {
		if !asked[a] {
			t.Errorf("no row asked %s for a fresh password", a)
		}
	}
}

// The window is judged once, when the seam decides: a session whose
// window lapses while its request runs is not refused by the handler's
// own check (a 401 without the elevate marker, or a half-done request).
func TestElevationIsDecidedOnce(t *testing.T) {
	srv := auditServer(t, Options{})
	id, _, at := elevateAs(t, srv)
	at(15*time.Minute - time.Second)
	r := httptestRequest("POST", "/api/forces", `{"name":"SP","value":1}`, withCookie(id), ownOrigin)
	r = srv.withActor(r)
	if code, msg, _ := srv.preAuthorize(r, "force.set"); code != 0 {
		t.Fatalf("seam: %d %s", code, msg)
	}
	at(time.Hour) // the window lapses after the seam decided
	if code, msg := srv.authorize(withSeamDecision(r), PermForce, "SP"); code != 0 {
		t.Errorf("the handler's check after the seam's: %d %s", code, msg)
	}
	// A request resolved after the lapse is refused.
	if code, msg := srv.Authorize(httptestRequest("POST", "/x", "", withCookie(id), ownOrigin), PermForce, "SP"); code != http.StatusUnauthorized || !strings.HasPrefix(msg, ElevationNeeded) {
		t.Errorf("after the lapse: %d %s", code, msg)
	}
}

// The 401 tells a person what to do, not only an HMI: confirm the
// password, by signing in again (the way until the HMI kit prompts) or
// where the HMI asks; and for how long a password counts (naut-lead,
// #1088).
func TestElevationRefusalTellsAPerson(t *testing.T) {
	srv := auditServer(t, Options{})
	id, _, at := elevateAs(t, srv)
	at(time.Hour)
	rec := call(t, srv, "POST", "/api/forces", `{"name":"SP","value":1}`, withCookie(id), ownOrigin)
	var body struct {
		Error   string `json:"error"`
		Elevate bool   `json:"elevate"`
	}
	json.Unmarshal(rec.Body.Bytes(), &body)
	for _, want := range []string{"confirm your password", "sign in again (/signin)", "within the last 15m (auth.elevation)"} {
		if !body.Elevate || !strings.Contains(body.Error, want) {
			t.Errorf("the refusal %q does not say %q", body.Error, want)
		}
	}
	for d, want := range map[time.Duration]string{15 * time.Minute: "15m", time.Hour: "1h", 90 * time.Minute: "1h30m", 12 * time.Hour: "12h", time.Minute: "1m", 90 * time.Second: "1m30s"} {
		if got := shortDuration(d); got != want {
			t.Errorf("shortDuration(%s) = %q, want %q", d, got, want)
		}
	}
}

// Deploy, users and override have no route of the server's own: a facade
// asks Authorize, which asks for a fresh password the same way, and
// WriteRefusal marks its 401. A lapsed session holding none of them is
// refused with a 403, never asked for a password it could not use.
func TestAuthorizeAsksForAFreshPassword(t *testing.T) {
	srv := auditServer(t, Options{})
	id, pat, at := elevateAs(t, srv)
	req := func(mod ...func(*http.Request)) *http.Request {
		return httptestRequest("POST", "/api/logix/deploy", "", append(mod, ownOrigin)...)
	}
	for _, perm := range []string{PermDeploy, auth.Users, auth.Override, PermEdit, PermActivate, PermConfig} {
		at(15*time.Minute - time.Second)
		if code, msg := srv.Authorize(req(withCookie(id)), perm, ""); code != 0 {
			t.Errorf("%s, fresh: %d %s", perm, code, msg)
		}
		at(15 * time.Minute)
		code, msg := srv.Authorize(req(withCookie(id)), perm, "")
		rec := httptestRecorder()
		WriteRefusal(rec, code, msg)
		if code != http.StatusUnauthorized || !elevateMarked(rec.Body.String()) {
			t.Errorf("%s, lapsed: %d %s", perm, code, rec.Body)
		}
		if code, msg := srv.Authorize(req(bearer(pat)), perm, ""); code != 0 {
			t.Errorf("%s by PAT: %d %s", perm, code, msg)
		}
	}
	// Past the window, a permission that is not elevated is not asked.
	if code, msg := srv.Authorize(req(withCookie(id)), PermWrite, "SP"); code != 0 {
		t.Errorf("write, lapsed: %d %s", code, msg)
	}
	// A refusal for any other reason is not marked.
	rec := httptestRecorder()
	WriteRefusal(rec, http.StatusUnauthorized, "no credential")
	if elevateMarked(rec.Body.String()) {
		t.Errorf("another 401 marked elevate: %s", rec.Body)
	}

	// A user without the permission is told 403, not asked to elevate.
	p, err := auth.Compile(auth.Settings{Users: map[string][]auth.Grant{"boss": {{Role: auth.Operator}}}})
	if err != nil {
		t.Fatal(err)
	}
	srv.policy = p
	if code, msg := srv.Authorize(req(withCookie(id)), PermForce, "SP"); code != http.StatusForbidden {
		t.Errorf("an operator's lapsed session asking to force: %d %s, want 403", code, msg)
	}
}

func httptestRequest(method, path, body string, mod ...func(*http.Request)) *http.Request {
	r := httptest.NewRequest(method, "http://localhost"+path, strings.NewReader(body))
	for _, m := range mod {
		m(r)
	}
	return r
}

func httptestRecorder() *httptest.ResponseRecorder { return httptest.NewRecorder() }

// clock gives srv's sessions a clock the test moves.
func clock(srv *Server) func(time.Duration) {
	t0 := time.Now()
	now := t0
	srv.sessions.now = func() time.Time { return now }
	return func(d time.Duration) { now = t0.Add(d) }
}

func elevate(t *testing.T, srv *Server, id, password string, mod ...func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	return call(t, srv, "POST", "/api/elevate", `{"password":"`+password+`"}`, append([]func(*http.Request){fromLoopback, ownOrigin, asJSON, withCookie(id)}, mod...)...)
}

// POST /api/elevate proves the session's password again: the elevated
// permissions are back for auth.elevation from then, for that session
// only (another sign-in of the same user stays as it was), /api/me says
// until when, and the journal has auth.elevate with the session's hash.
func TestElevateRefreshesTheWindow(t *testing.T) {
	srv, _, _ := patServer(t)
	at := clock(srv)
	c := signIn(t, srv, "akhan")
	other := signIn(t, srv, "akhan")
	force := func(id string) *httptest.ResponseRecorder {
		return call(t, srv, "POST", "/api/forces", `{"name":"SP","value":1}`, withCookie(id), ownOrigin)
	}
	at(20 * time.Minute)
	if rec := force(c.Value); rec.Code != http.StatusUnauthorized || !elevateMarked(rec.Body.String()) {
		t.Fatalf("lapsed: %d %s", rec.Code, rec.Body)
	}
	rec := elevate(t, srv, c.Value, "password-akhan")
	want := srv.sessions.now().Add(15 * time.Minute).UTC()
	var got struct {
		User          string    `json:"user"`
		ElevatedUntil time.Time `json:"elevatedUntil"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil || got.User != "akhan" || !got.ElevatedUntil.Equal(want) {
		t.Fatalf("elevate: %d %s, want until %s", rec.Code, rec.Body, want)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Error("elevate's answer may be cached")
	}
	e := srv.audit.ring.Recent()[0]
	if e.Action != "auth.elevate" || e.Result != "ok" || e.User != "user:akhan" || e.Session != sessionHash(c.Value) {
		t.Errorf("journalled %+v", e)
	}
	if rec := force(c.Value); rec.Code >= 300 {
		t.Errorf("after elevating: %d %s", rec.Code, rec.Body)
	}
	if rec := force(other.Value); rec.Code != http.StatusUnauthorized {
		t.Errorf("the user's other sign-in was elevated too: %d %s", rec.Code, rec.Body)
	}
	me := call(t, srv, "GET", "/api/me", "", withCookie(c.Value)).Body.String()
	if !strings.Contains(me, `"elevatedUntil":"`+want.Format(time.RFC3339Nano)) || !strings.Contains(me, `"elevated":["out-of-service","force","edit","activate","deploy","config","users","override","redundancy"]`) {
		t.Errorf("/api/me: %s", me)
	}
	// The window is fixed: using it does not slide it.
	at(20*time.Minute + 15*time.Minute)
	if rec := force(c.Value); rec.Code != http.StatusUnauthorized {
		t.Errorf("15m after elevating, having used it: %d", rec.Code)
	}
	// It never outlasts the session.
	at(12*time.Hour - time.Minute)
	elevate(t, srv, c.Value, "password-akhan")
	me = call(t, srv, "GET", "/api/me", "", withCookie(c.Value)).Body.String()
	se, _ := srv.sessions.lookup(c.Value)
	if !strings.Contains(me, `"elevatedUntil":"`+se.expires.UTC().Format(time.RFC3339Nano)) {
		t.Errorf("elevated past the session's end: %s (session ends %s)", me, se.expires.UTC())
	}
}

// What re-authentication refuses: another site's page, a bearer
// credential (it is always elevated), no session, and a wrong password,
// which is told one message whoever's password it was, is charged to the
// user's sign-in name bucket, and leaves the session acting; after five
// wrong ones in a row the session ends (a stolen cookie guessing). Each
// refusal is journalled.
func TestElevateRefusals(t *testing.T) {
	srv, toks, _ := patServer(t)
	clock(srv)
	c := signIn(t, srv, "akhan")
	for _, x := range []struct {
		what string
		mod  []func(*http.Request)
		code int
	}{
		{"cross-origin", []func(*http.Request){func(r *http.Request) { r.Header.Set("Origin", "http://evil") }}, http.StatusForbidden},
		{"plain HTTP from afar", []func(*http.Request){func(r *http.Request) { r.RemoteAddr = "192.0.2.1:4000" }}, http.StatusForbidden},
		{"bearer", []func(*http.Request){bearer(toks["akhan"])}, http.StatusBadRequest},
		{"no session", []func(*http.Request){func(r *http.Request) { r.Header.Del("Cookie") }}, http.StatusUnauthorized},
	} {
		n := len(srv.audit.ring.Recent())
		rec := elevate(t, srv, c.Value, "password-akhan", x.mod...)
		es := srv.audit.ring.Recent()
		if rec.Code != x.code || len(es) != n+1 || es[0].Action != "auth.elevate" || es[0].Result != "refused" {
			t.Errorf("%s: %d %s, journalled %d", x.what, rec.Code, rec.Body, len(es)-n)
		}
	}
	names := func() float64 {
		srv.signin.names.mu.Lock()
		defer srv.signin.names.mu.Unlock()
		if b := srv.signin.names.b["akhan"]; b != nil {
			return b.tokens
		}
		return srv.signin.names.burst
	}
	start := names()
	var bodies []string
	for i, pw := range []string{"nope", "password-jsmith", "password-ghost", "Password-akhan"} {
		rec := elevate(t, srv, c.Value, pw)
		bodies = append(bodies, rec.Body.String())
		if rec.Code != http.StatusUnauthorized || elevateMarked(rec.Body.String()) {
			t.Errorf("%q: %d %s", pw, rec.Code, rec.Body)
		}
		if se := srv.sessions.byHash[sessionHash(c.Value)]; se == nil || se.fails != i+1 {
			t.Errorf("%q: fails not counted: %+v", pw, se)
		}
		if me := call(t, srv, "GET", "/api/me", "", withCookie(c.Value)).Body.String(); !strings.Contains(me, "user:akhan") {
			t.Fatalf("after %d wrong passwords the session ended", i+1)
		}
	}
	// One message, whatever was wrong, and nothing of the reason.
	want, _ := json.Marshal(map[string]string{"error": elevateRefused})
	for _, b := range bodies {
		if strings.TrimSpace(b) != string(want) {
			t.Errorf("refusal %q, want %s", b, want)
		}
	}
	if spent := start - names(); spent < 3.5 {
		t.Errorf("wrong passwords spent %.1f of the name's sign-in bucket, want 4", spent)
	}
	// A right one forgets the count...
	if rec := elevate(t, srv, c.Value, "password-akhan"); rec.Code != http.StatusOK {
		t.Fatalf("right password: %d %s", rec.Code, rec.Body)
	}
	srv.signin.sessions = newLimiter(100, time.Second) // the throttle is tested apart
	for i := 1; i <= 4; i++ {
		elevate(t, srv, c.Value, "nope")
	}
	if me := call(t, srv, "GET", "/api/me", "", withCookie(c.Value)).Body.String(); !strings.Contains(me, "user:akhan") {
		t.Fatal("four wrong after a right one ended the session")
	}
	// ...and the fifth in a row ends the session, says so, and clears
	// the cookie.
	rec := elevate(t, srv, c.Value, "nope")
	e := srv.audit.ring.Recent()[0]
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "sign in again") || e.Action != "auth.session-ended" || e.Session != sessionHash(c.Value) || e.User != "user:akhan" {
		t.Errorf("fifth wrong: %d %s, journalled %+v", rec.Code, rec.Body, e)
	}
	if cs := rec.Result().Cookies(); len(cs) != 1 || cs[0].MaxAge >= 0 {
		t.Errorf("the cookie was not cleared: %+v", cs)
	}
	if me := call(t, srv, "GET", "/api/me", "", withCookie(c.Value)).Body.String(); !strings.Contains(me, `"principal":"anonymous"`) {
		t.Errorf("the session still acts after five wrong passwords: %s", me)
	}
}

// The throttle is the session's, whatever address its attempts come from:
// a thief with the cookie gains nothing by rotating addresses.
func TestElevateThrottlesBySession(t *testing.T) {
	srv, _, _ := patServer(t)
	clock(srv)
	c := signIn(t, srv, "akhan")
	for i := 0; i < maxElevateFails; i++ {
		if rec := elevate(t, srv, c.Value, "password-akhan"); rec.Code != http.StatusOK {
			t.Fatalf("attempt %d: %d %s", i+1, rec.Code, rec.Body)
		}
	}
	rec := elevate(t, srv, c.Value, "password-akhan", func(r *http.Request) { r.RemoteAddr = "[::1]:4000" })
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Errorf("past the session's bucket from another address: %d %s", rec.Code, rec.Body)
	}
	// Another session of the same user has its own.
	d := signIn(t, srv, "akhan")
	if rec := elevate(t, srv, d.Value, "password-akhan"); rec.Code != http.StatusOK {
		t.Errorf("another session: %d %s", rec.Code, rec.Body)
	}
}

// A re-authentication proves the password the session was opened with: a
// session whose password fingerprint is not the current one is not
// refreshed.
func TestElevatedNeedsTheSessionsPassword(t *testing.T) {
	ss := newSessions(time.Hour)
	t0 := time.Now()
	now := t0
	ss.now = func() time.Time { return now }
	id, se := ss.open("akhan", "cred-1")
	now = now.Add(time.Minute)
	if _, ok := ss.elevated(se.hash, "cred-2"); ok {
		t.Error("refreshed against another password")
	}
	if got, _ := ss.lookup(id); !got.authAt.Equal(t0) {
		t.Errorf("authAt moved to %s", got.authAt)
	}
	if got, ok := ss.elevated(se.hash, "cred-1"); !ok || !got.authAt.Equal(now) {
		t.Errorf("the right password: %v %s", ok, got.authAt)
	}
}

// A forwarding standby refers re-authentication to the leader, like
// sign-in: the session it would refresh lives there.
func TestStandbyRefersElevate(t *testing.T) {
	if !standbyRefers(httptestRequest("POST", "/api/elevate", "", bearer("x"))) {
		t.Error("a standby does not refer /api/elevate")
	}
}

// Elevation does not depend on the journal (naut-lead's condition on
// prompting for recovery actions): with the journal down and the window
// lapsed, setting a force is asked for the password (401, not the
// journal's 503), re-authentication works, and then clearing a force and
// rolling back are carried out; setting a force is still refused by the
// journal. Signing in again elevates too.
func TestElevationWithTheJournalDown(t *testing.T) {
	captureLog(t)
	j := &flaky{}
	srv, _, _ := patServerWith(t, Options{Audit: j})
	at := clock(srv)
	c := signIn(t, srv, "akhan")
	cookie := func(r *http.Request) { withCookie(c.Value)(r); ownOrigin(r) }
	if rec := call(t, srv, "POST", "/api/forces", `{"name":"SP","value":5}`, cookie); rec.Code >= 300 {
		t.Fatalf("force while fresh: %d %s", rec.Code, rec.Body)
	}
	edit(editedProgram)(t, srv)
	j.mu.Lock()
	j.down = true
	j.mu.Unlock()
	call(t, srv, "POST", "/api/tags", `{"name":"SP","value":1}`, cookie) // finds it broken
	if !srv.AuditJournalDown() {
		t.Fatal("the journal is not down")
	}
	at(time.Hour)
	if rec := call(t, srv, "POST", "/api/forces", `{"name":"SP","value":6}`, cookie); rec.Code != http.StatusUnauthorized || !elevateMarked(rec.Body.String()) {
		t.Fatalf("lapsed force with the journal down: %d %s, want the 401 asking for the password", rec.Code, rec.Body)
	}
	if rec := call(t, srv, "DELETE", "/api/forces/SP", "", cookie); rec.Code != http.StatusUnauthorized || !elevateMarked(rec.Body.String()) {
		t.Errorf("lapsed force clear: %d %s, want it asked for the password", rec.Code, rec.Body)
	}
	if rec := elevate(t, srv, c.Value, "password-akhan"); rec.Code != http.StatusOK {
		t.Fatalf("elevate with the journal down: %d %s", rec.Code, rec.Body)
	}
	for _, x := range []struct{ method, path string }{{"DELETE", "/api/forces/SP"}, {"POST", "/api/program/rollback"}} {
		rec := call(t, srv, x.method, x.path, "", cookie)
		if rec.Code >= 300 || rec.Header().Get(auditJournalHeader) != "down" {
			t.Errorf("%s %s once elevated, journal down: %d %s", x.method, x.path, rec.Code, rec.Body)
		}
	}
	if rec := call(t, srv, "POST", "/api/forces", `{"name":"SP","value":7}`, cookie); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("setting a force once elevated, journal down: %d %s, want the journal's 503", rec.Code, rec.Body)
	}

	// A fresh sign-in counts as elevated.
	at(2 * time.Hour)
	d := signIn(t, srv, "akhan")
	if rec := call(t, srv, "POST", "/api/program/rollback", "", withCookie(d.Value), ownOrigin); rec.Code == http.StatusUnauthorized {
		t.Errorf("a fresh sign-in is asked for the password: %s", rec.Body)
	}
}

// Re-authentication waits for one of the two password-hash slots, as
// sign-in does; an attempt that got none (or was throttled) is not a
// wrong password and does not count toward ending the session.
func TestElevateWaitsForTheHash(t *testing.T) {
	srv, _, _ := patServer(t)
	clock(srv)
	c := signIn(t, srv, "akhan")
	old := signInQueue
	signInQueue = 10 * time.Millisecond
	t.Cleanup(func() { signInQueue = old })
	for range signInHashing {
		srv.signin.hashing <- struct{}{}
	}
	for i := 0; i < maxElevateFails; i++ {
		if rec := elevate(t, srv, c.Value, "nope"); rec.Code != http.StatusServiceUnavailable {
			t.Errorf("hash busy: %d %s, want 503", rec.Code, rec.Body)
		}
	}
	// Throttled now (the bucket is spent): still not counted.
	if rec := elevate(t, srv, c.Value, "nope"); rec.Code != http.StatusTooManyRequests {
		t.Errorf("past the bucket: %d %s", rec.Code, rec.Body)
	}
	if se := srv.sessions.byHash[sessionHash(c.Value)]; se == nil || se.fails != 0 {
		t.Errorf("busy or throttled attempts counted as wrong passwords: %+v", se)
	}
}

// The session the fifth wrong password ends is ended like a sign-out:
// the OnEnd hooks hear of it, so its open live streams close.
func TestElevateEndTellsTheHooks(t *testing.T) {
	srv, _, _ := patServer(t)
	clock(srv)
	c := signIn(t, srv, "akhan")
	srv.signin.sessions = newLimiter(100, time.Second)
	var ended []string
	srv.sessions.OnEnd(func(se session) { ended = append(ended, se.hash) })
	for i := 0; i < maxElevateFails; i++ {
		elevate(t, srv, c.Value, "nope")
	}
	if len(ended) != 1 || ended[0] != sessionHash(c.Value) {
		t.Errorf("hooks told %v, want the session", ended)
	}
}

// A session whose user holds no elevated permission (jsmith, an operator)
// has nothing to confirm: /api/elevate refuses it with a 403 before any
// password hashing, even with the hash slots free and the right password,
// so a viewer's sessions cannot keep the slots busy (#1088 review). It is
// journalled.
func TestElevateRefusesAUserWithNothingToElevate(t *testing.T) {
	srv, _, _ := patServer(t)
	clock(srv)
	c := signIn(t, srv, "jsmith")
	// Both slots held: a request that tried to hash would wait, then 503.
	old := signInQueue
	signInQueue = 10 * time.Millisecond
	t.Cleanup(func() { signInQueue = old })
	for range signInHashing {
		srv.signin.hashing <- struct{}{}
	}
	rec := elevate(t, srv, c.Value, "password-jsmith")
	e := srv.audit.ring.Recent()[0]
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "nothing to confirm") ||
		e.Action != "auth.elevate" || e.Result != "refused" || e.Detail["reason"] != "no elevated permission" {
		t.Errorf("an operator's elevate: %d %s, journalled %+v", rec.Code, rec.Body, e)
	}
	// Nor does it spend the session's throttle.
	srv.signin.sessions.mu.Lock()
	_, spent := srv.signin.sessions.b[sessionHash(c.Value)]
	srv.signin.sessions.mu.Unlock()
	if spent {
		t.Error("the refusal spent the session's bucket")
	}
}

// A signed-in person whose password has gone stale is refused a force and
// journalled with the tag they tried to force; the same session from
// another site's page is journalled without its body (the seam never
// reads a request from another origin).
func TestRefusedSessionJournalsItsTargetOnlyFromItsOwnPage(t *testing.T) {
	for _, c := range []struct {
		what   string
		stale  bool
		origin func(*http.Request)
		target string
	}{
		{"stale password, own page", true, ownOrigin, "SP"},
		{"fresh password, another site", false, func(r *http.Request) { r.Header.Set("Origin", "http://elsewhere.example") }, ""},
	} {
		srv := auditServer(t, Options{})
		id, _, at := elevateAs(t, srv)
		if c.stale {
			at(time.Hour)
		}
		n := len(srv.audit.ring.Recent())
		rec := call(t, srv, "POST", "/api/forces", `{"name":"SP","value":1}`, withCookie(id), c.origin)
		es := srv.audit.ring.Recent()
		if rec.Code < 400 || len(es)-n != 1 {
			t.Fatalf("%s: %d, %d entries", c.what, rec.Code, len(es)-n)
		}
		if e := es[0]; e.Result != "refused" || e.Target != c.target {
			t.Errorf("%s: %s target %q, want refused target %q", c.what, e.Result, e.Target, c.target)
		}
	}
}
