package server

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/auth"
	"github.com/joyautomation/nautilus/auth/users"
	"github.com/joyautomation/nautilus/internal/oidcseam"
)

// fakeOIDC is the provider side of the seam: it hands out an
// authorization URL and answers Exchange with the identity a test sets.
// It checks the server sends back the redirect URI and verifier the
// sign-in started with.
type fakeOIDC struct {
	mu       sync.Mutex
	last     oidcseam.AuthRequest
	id       oidcseam.Identity
	err      error // Exchange's
	startErr error // AuthCodeURL's
	badNonce bool
}

func (f *fakeOIDC) Provider() string { return "Plant SSO" }

func (f *fakeOIDC) AuthCodeURL(_ context.Context, req oidcseam.AuthRequest) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.startErr != nil {
		return "", f.startErr
	}
	f.last = req
	return "https://idp.example/authorize?state=" + url.QueryEscape(req.State), nil
}

func (f *fakeOIDC) Exchange(_ context.Context, redirect, code, verifier string) (oidcseam.Identity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return oidcseam.Identity{}, f.err
	}
	if redirect != f.last.RedirectURI || verifier != f.last.Verifier || code != "code-1" {
		return oidcseam.Identity{}, errors.New("fake: redirect, verifier or code mismatch")
	}
	id := f.id
	id.Nonce = f.last.Nonce
	if f.badNonce {
		id.Nonce = "another-nonce"
	}
	return id, nil
}

func (f *fakeOIDC) as(name, sub string, groups ...string) {
	f.mu.Lock()
	f.id = oidcseam.Identity{Issuer: "https://idp.example", Subject: sub, Name: name, NameClaim: "preferred_username", Groups: groups}
	f.mu.Unlock()
}

// oidcServer is patServer's (akhan engineer, jsmith operator over SP)
// with an OIDC provider: group g-ops is an operator over SP, g-eng an
// engineer, and the names jsmith@corp.example (engineer) and akhan
// (viewer: the OIDC akhan is not the local one) are granted by name.
func oidcServer(t *testing.T, o Options) (*Server, *fakeOIDC, string) {
	t.Helper()
	old := users.Default
	users.Default = users.Params{Memory: 64, Time: 1, Threads: 1}
	t.Cleanup(func() { users.Default = old })
	p, err := auth.Compile(auth.Settings{
		Areas: map[string][]string{"sp": {"SP"}},
		Users: map[string][]auth.Grant{
			"akhan":  {{Role: auth.Engineer}},
			"jsmith": {{Role: auth.Operator, Areas: []string{"sp"}}},
		},
		OIDC: true,
		OIDCGroups: map[string][]auth.Grant{
			"g-ops": {{Role: auth.Operator, Areas: []string{"sp"}}},
			"g-eng": {{Role: auth.Engineer}},
		},
		OIDCUsers: map[string][]auth.Grant{
			"jsmith@corp.example": {{Role: auth.Engineer}},
			"akhan":               {{Role: auth.Viewer}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	os.Chmod(dir, 0o700)
	path := filepath.Join(dir, users.FileName)
	f := users.New()
	for _, n := range []string{"akhan", "jsmith"} {
		if err := f.Add(n, "password-"+n); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Save(path, "opsadmin"); err != nil {
		t.Fatal(err)
	}
	store, err := users.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeOIDC{}
	o.Auth, o.Users, o.OIDC = p, store, fake
	return auditServer(t, o), fake, path
}

// oidcStart opens /auth/oidc/start from loopback and returns its answer
// and the binding cookie it set.
func oidcStart(t *testing.T, srv *Server, next string, mod ...func(*http.Request)) (*httptest.ResponseRecorder, *http.Cookie) {
	t.Helper()
	rec := call(t, srv, "GET", "/auth/oidc/start?next="+url.QueryEscape(next), "", append([]func(*http.Request){fromLoopback}, mod...)...)
	for _, c := range rec.Result().Cookies() {
		if strings.HasPrefix(c.Name, oidcPlainCookie+"_") {
			return rec, c
		}
	}
	return rec, nil
}

// oidcCallback is the browser coming back from the provider with state
// and the binding cookie.
func oidcCallback(t *testing.T, srv *Server, state string, binding *http.Cookie, extra ...func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	mod := []func(*http.Request){fromLoopback}
	if binding != nil {
		mod = append(mod, func(r *http.Request) { r.AddCookie(binding) })
	}
	return call(t, srv, "GET", "/auth/oidc/callback?code=code-1&state="+url.QueryEscape(state), "", append(mod, extra...)...)
}

// oidcSignIn runs a whole sign-in as the identity fake answers with, and
// returns the callback's answer and the session cookie (nil when none).
func oidcSignIn(t *testing.T, srv *Server, fake *fakeOIDC, next string) (*httptest.ResponseRecorder, *http.Cookie) {
	t.Helper()
	rec, binding := oidcStart(t, srv, next)
	if rec.Code != http.StatusSeeOther || binding == nil {
		t.Fatalf("start: %d %s (binding %v)", rec.Code, rec.Body, binding)
	}
	fake.mu.Lock()
	state := fake.last.State
	fake.mu.Unlock()
	cb := oidcCallback(t, srv, state, binding)
	for _, c := range cb.Result().Cookies() {
		if c.Name == plainCookie && c.Value != "" {
			return cb, c
		}
	}
	return cb, nil
}

func lastEntry(t *testing.T, srv *Server) map[string]any {
	t.Helper()
	es := srv.audit.ring.Recent()
	if len(es) == 0 {
		t.Fatal("nothing journalled")
	}
	e := es[0]
	return map[string]any{"user": e.User, "action": e.Action, "result": e.Result, "target": e.Target, "detail": e.Detail, "session": e.Session}
}

// A sign-in at the provider starts a session as oidc:<name>: the start
// sends the browser to the provider with PKCE, state and nonce held here;
// the callback answers 200 with a page that moves on (never a redirect),
// sets the Strict session cookie and clears the binding cookie; the
// session writes within its groups' grants; the journal names oidc:<name>
// and says the name was pinned.
func TestOIDCSignInStartsASession(t *testing.T) {
	srv, fake, path := oidcServer(t, Options{})
	fake.as("Ops.One@Corp.Example", "sub-1", "g-ops", "unknown-group")
	rec, binding := oidcStart(t, srv, "/hmi/pumps")
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "https://idp.example/authorize") {
		t.Fatalf("start: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if binding == nil || !binding.HttpOnly || binding.SameSite != http.SameSiteLaxMode || binding.Path != "/" {
		t.Fatalf("binding cookie %+v: want HttpOnly, SameSite=Lax, Path=/", binding)
	}
	req := fake.last
	if req.RedirectURI != "http://localhost/auth/oidc/callback" || len(req.State) < 43 || len(req.Nonce) < 43 || len(req.Verifier) < 43 {
		t.Errorf("auth request %+v", req)
	}
	cb := oidcCallback(t, srv, req.State, binding)
	if cb.Code != http.StatusOK {
		t.Fatalf("callback: %d %s", cb.Code, cb.Body)
	}
	body := cb.Body.String()
	if !strings.Contains(body, `http-equiv="refresh" content="0;url=/hmi/pumps"`) || !strings.Contains(body, `href="/hmi/pumps"`) || strings.Contains(body, "<script") {
		t.Errorf("hop page: %s", body)
	}
	if cb.Header().Get("Referrer-Policy") != "no-referrer" || cb.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("hop headers %v", cb.Header())
	}
	var session *http.Cookie
	cleared := false
	for _, c := range cb.Result().Cookies() {
		switch c.Name {
		case plainCookie:
			session = c
		case binding.Name:
			cleared = c.MaxAge < 0
		}
	}
	if session == nil || !session.HttpOnly || session.SameSite != http.SameSiteStrictMode {
		t.Fatalf("session cookie %+v", session)
	}
	if !cleared {
		t.Error("the binding cookie was not cleared")
	}
	e := lastEntry(t, srv)
	d := e["detail"].(map[string]any)
	if e["user"] != "oidc:ops.one@corp.example" || e["action"] != "auth.signin" || e["result"] != "ok" || d["pinned"] != true || d["method"] != "oidc" || d["subject"] != "sub-1" {
		t.Errorf("journalled %+v", e)
	}
	pins, err := users.LoadPins(users.PinsPath(path))
	if err != nil || !pins.Pins["ops.one@corp.example"].Same("https://idp.example", "sub-1") {
		t.Errorf("pins %+v %v", pins, err)
	}
	// Within the group's grant (operator over SP), and not past it.
	if rec := call(t, srv, "POST", "/api/tags", `{"name":"SP","value":42}`, withCookie(session.Value), ownOrigin); rec.Code != http.StatusNoContent {
		t.Errorf("write SP: %d %s", rec.Code, rec.Body)
	}
	if es := srv.audit.ring.Recent(); es[0].User != "oidc:ops.one@corp.example" || es[0].Session != sessionHash(session.Value) {
		t.Errorf("write journalled %+v", es[0])
	}
	if rec := call(t, srv, "POST", "/api/tags", `{"name":"Out","value":1}`, withCookie(session.Value), ownOrigin); rec.Code != http.StatusForbidden {
		t.Errorf("write Out (outside sp): %d %s", rec.Code, rec.Body)
	}
	me := call(t, srv, "GET", "/api/me", "", withCookie(session.Value))
	if !strings.Contains(me.Body.String(), `"principal":"oidc:ops.one@corp.example"`) {
		t.Errorf("/api/me %s", me.Body)
	}
}

// A name is pinned to the account of its first sign-in: another account
// at the provider under the same name is refused, and journalled, until
// naut users unpin; the unpin ends the first account's session at once,
// and the next sign-in pins the name afresh.
func TestOIDCSameNameAnotherAccountIsRefused(t *testing.T) {
	srv, fake, path := oidcServer(t, Options{})
	fake.as("jsmith@corp.example", "sub-1")
	_, first := oidcSignIn(t, srv, fake, "/")
	if first == nil {
		t.Fatal("first sign-in failed")
	}
	fake.as("JSmith@corp.example", "sub-2")
	cb, c := oidcSignIn(t, srv, fake, "/")
	if cb.Code != http.StatusForbidden || c != nil || !strings.Contains(cb.Body.String(), "naut users unpin jsmith@corp.example") {
		t.Fatalf("another sub: %d %s", cb.Code, cb.Body)
	}
	e := lastEntry(t, srv)
	if e["result"] != "refused" || e["target"] != "oidc:jsmith@corp.example" || e["detail"].(map[string]any)["reason"] != "pinned to another account" {
		t.Errorf("journalled %+v", e)
	}
	// The first account's session still acts.
	if rec := call(t, srv, "GET", "/api/me", "", withCookie(first.Value)); !strings.Contains(rec.Body.String(), "oidc:jsmith@corp.example") {
		t.Fatalf("first session: %s", rec.Body)
	}
	if err := users.UpdatePins(users.PinsPath(path), "opsadmin", func(p *users.Pins) error { return p.Unpin("jsmith@corp.example") }); err != nil {
		t.Fatal(err)
	}
	srv.users.Reload()
	if rec := call(t, srv, "GET", "/api/me", "", withCookie(first.Value)); !strings.Contains(rec.Body.String(), `"principal":"anonymous"`) {
		t.Errorf("after unpin, the old account's session still acts: %s", rec.Body)
	}
	var unpinned bool
	for _, e := range srv.audit.ring.Recent() {
		if e.Action == "users.change" && e.Target == "oidc:jsmith@corp.example" && e.Detail["change"] == "unpinned" && e.User == "os:opsadmin" {
			unpinned = true
		}
	}
	if !unpinned {
		t.Error("the unpin was not journalled as users.change by os:opsadmin")
	}
	// A session of the old account that raced the unpin (opened after
	// the reload ended the others) acts no more: each request checks the
	// pin.
	raced, _ := srv.sessions.start(session{user: "jsmith@corp.example", oidc: true, issuer: "https://idp.example", sub: "sub-1"})
	if rec := call(t, srv, "GET", "/api/me", "", withCookie(raced)); !strings.Contains(rec.Body.String(), `"principal":"anonymous"`) {
		t.Errorf("an unpinned account's raced session acts: %s", rec.Body)
	}
	_, c = oidcSignIn(t, srv, fake, "/")
	if c == nil {
		t.Fatal("after unpin, the new account could not sign in")
	}
	// The name is pinned to sub-2 now: a raced sub-1 session still acts
	// no more.
	if rec := call(t, srv, "GET", "/api/me", "", withCookie(raced)); !strings.Contains(rec.Body.String(), `"principal":"anonymous"`) {
		t.Errorf("a session of the account the name is no longer pinned to acts: %s", rec.Body)
	}
}

// The state is used once, only by the browser that started the sign-in,
// within ten minutes; the provider's own refusal and a wrong nonce are
// refusals too, and none starts a session.
func TestOIDCCallbackRefusals(t *testing.T) {
	srv, fake, _ := oidcServer(t, Options{})
	fake.as("jsmith@corp.example", "sub-1")

	start := func() (string, *http.Cookie) {
		_, b := oidcStart(t, srv, "/")
		return fake.last.State, b
	}
	// Each case from its own loopback address: the journal coalesces an
	// address's refusals past the first few.
	n := 1
	from := func() func(*http.Request) {
		n++
		addr := "127.0.0." + strconv.Itoa(n) + ":40000"
		return func(r *http.Request) { r.RemoteAddr = addr }
	}
	noSession := func(what string, rec *httptest.ResponseRecorder, code int, reason string) {
		t.Helper()
		if rec.Code != code {
			t.Errorf("%s: %d %s, want %d", what, rec.Code, rec.Body, code)
		}
		for _, c := range rec.Result().Cookies() {
			if c.Name == plainCookie && c.Value != "" {
				t.Errorf("%s: a session cookie was set", what)
			}
		}
		e := lastEntry(t, srv)
		if e["result"] != "refused" || e["action"] != "auth.signin" || e["detail"].(map[string]any)["reason"] != reason {
			t.Errorf("%s: journalled %+v, want reason %q", what, e, reason)
		}
	}

	st, b := start()
	if rec := oidcCallback(t, srv, st, b, from()); rec.Code != http.StatusOK {
		t.Fatalf("first use: %d", rec.Code)
	}
	noSession("replayed state", oidcCallback(t, srv, st, b, from()), http.StatusBadRequest, "unknown or expired state")

	st, _ = start()
	noSession("no binding cookie", oidcCallback(t, srv, st, nil, from()), http.StatusBadRequest, "binding cookie missing or wrong")
	// The state is spent by the failed attempt: the right cookie later is
	// too late.
	st, b = start()
	other := &http.Cookie{Name: b.Name, Value: "someone-elses"}
	noSession("another browser's binding cookie", oidcCallback(t, srv, st, other, from()), http.StatusBadRequest, "binding cookie missing or wrong")
	noSession("the state after a failed attempt", oidcCallback(t, srv, st, b, from()), http.StatusBadRequest, "unknown or expired state")

	st, b = start()
	srv.oidcFlows.now = func() time.Time { return time.Now().Add(oidcFlowLife + time.Second) }
	noSession("expired state", oidcCallback(t, srv, st, b, from()), http.StatusBadRequest, "unknown or expired state")
	srv.oidcFlows.now = time.Now

	st, b = start()
	rec := call(t, srv, "GET", "/auth/oidc/callback?error=access_denied%3Cscript%3E&state="+url.QueryEscape(st), "", fromLoopback, from(), func(r *http.Request) { r.AddCookie(b) })
	noSession("provider error", rec, http.StatusUnauthorized, "provider error: access_deniedscript")

	fake.badNonce = true
	st, b = start()
	noSession("nonce mismatch", oidcCallback(t, srv, st, b, from()), http.StatusUnauthorized, "nonce mismatch")
	fake.badNonce = false

	fake.err = errors.New("the ID token does not verify: oidc: id token issued to client \"x\"")
	st, b = start()
	noSession("token does not verify", oidcCallback(t, srv, st, b, from()), http.StatusUnauthorized, fake.err.Error())
	fake.err = nil

	fake.as("jsmith@corp.example", "")
	st, b = start()
	noSession("no subject", oidcCallback(t, srv, st, b, from()), http.StatusUnauthorized, "no issuer or no subject")

	fake.as("j smith", "sub-1")
	st, b = start()
	noSession("a name with a space", oidcCallback(t, srv, st, b, from()), http.StatusForbidden, `username claim: OIDC name "j smith": it may hold only lowercase letters, digits and - _ . @ + ' (and start with a letter or digit)`)

	fake.as("", "sub-1")
	st, b = start()
	noSession("no username claim", oidcCallback(t, srv, st, b, from()), http.StatusForbidden, "no username claim preferred_username")
}

// Grants come from auth.oidc.groups by the exact id and from
// auth.oidc.users by name, never from auth.users: an OIDC name and a local
// user that read alike are two principals. A sign-in none grants anything
// is refused with the fix, and so is an Entra groups overage.
func TestOIDCGrantsAreItsOwn(t *testing.T) {
	srv, fake, _ := oidcServer(t, Options{})

	// oidc:akhan is a viewer; user:akhan, an engineer, lends it nothing.
	fake.as("akhan", "sub-a")
	_, c := oidcSignIn(t, srv, fake, "/")
	if c == nil {
		t.Fatal("oidc akhan could not sign in")
	}
	if rec := call(t, srv, "POST", "/api/tags", `{"name":"SP","value":1}`, withCookie(c.Value), ownOrigin); rec.Code != http.StatusForbidden {
		t.Errorf("oidc:akhan wrote with user:akhan's grants: %d", rec.Code)
	}
	// And a local sign-in out of oidc:akhan's session: only its own
	// name's sessions are capped and ended.
	local := signIn(t, srv, "akhan")
	if n := srv.sessions.endPrincipal("oidc:akhan"); n != 1 {
		t.Errorf("ended %d oidc:akhan sessions, want 1", n)
	}
	if rec := call(t, srv, "GET", "/api/me", "", withCookie(local.Value)); !strings.Contains(rec.Body.String(), `"principal":"user:akhan"`) {
		t.Errorf("ending oidc:akhan ended user:akhan: %s", rec.Body)
	}

	// The group ids match exactly: a different case, or the group's display
	// name, is not the id.
	for _, g := range []string{"G-OPS", "Operators"} {
		fake.as("someone@corp.example", "sub-"+g, g)
		cb, c := oidcSignIn(t, srv, fake, "/")
		if c != nil || cb.Code != http.StatusForbidden || !strings.Contains(cb.Body.String(), "auth.oidc.groups") || !strings.Contains(cb.Body.String(), g) {
			t.Errorf("group %q: %d %s", g, cb.Code, cb.Body)
		}
	}
	fake.as("someone@corp.example", "sub-x")
	if cb, _ := oidcSignIn(t, srv, fake, "/"); cb.Code != http.StatusForbidden || !strings.Contains(cb.Body.String(), "carries no groups") {
		t.Errorf("no groups: %d %s", cb.Code, cb.Body)
	}
	fake.mu.Lock()
	fake.id = oidcseam.Identity{Issuer: "https://idp.example", Subject: "sub-o", Name: "many@corp.example", GroupsOverage: true}
	fake.mu.Unlock()
	if cb, _ := oidcSignIn(t, srv, fake, "/"); cb.Code != http.StatusForbidden || !strings.Contains(cb.Body.String(), "overage") {
		t.Errorf("overage: %d %s", cb.Code, cb.Body)
	}
	// A refused sign-in pins nothing.
	if _, ok, _ := srv.users.Pin("someone@corp.example"); ok {
		t.Error("a sign-in granted nothing pinned its name")
	}
	// A name granted by auth.oidc.users signs in without any group.
	fake.as("jsmith@corp.example", "sub-j")
	if _, c := oidcSignIn(t, srv, fake, "/"); c == nil {
		t.Error("auth.oidc.users' jsmith could not sign in")
	}
}

// naut users signout ends a principal's sessions within a second (the
// store's reload), journalled; a session that began before the sign-out
// never acts again, even if the end raced it.
func TestSignoutEndsSessions(t *testing.T) {
	srv, fake, path := oidcServer(t, Options{})
	fake.as("jsmith@corp.example", "sub-1")
	_, oc := oidcSignIn(t, srv, fake, "/")
	lc := signIn(t, srv, "jsmith")
	if err := users.SignOut(users.SignoutsPath(path), "opsadmin", "oidc:jsmith@corp.example", time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	srv.users.Reload()
	if rec := call(t, srv, "GET", "/api/me", "", withCookie(oc.Value)); !strings.Contains(rec.Body.String(), `"principal":"anonymous"`) {
		t.Errorf("signed-out OIDC session still acts: %s", rec.Body)
	}
	if rec := call(t, srv, "GET", "/api/me", "", withCookie(lc.Value)); !strings.Contains(rec.Body.String(), `"principal":"user:jsmith"`) {
		t.Errorf("signing out oidc:jsmith@corp.example ended user:jsmith: %s", rec.Body)
	}
	var journalled bool
	for _, e := range srv.audit.ring.Recent() {
		if e.Action == "users.change" && e.Target == "oidc:jsmith@corp.example" && e.Detail["change"] == "signed out" && e.Detail["sessions"] == 1 {
			journalled = true
		}
	}
	if !journalled {
		t.Error("the sign-out was not journalled with the session it ended")
	}
	// The recheck alone: a session that raced the sign-out (opened after
	// the reload ended the others, but before the sign-out's moment) does
	// not act either.
	if err := users.SignOut(users.SignoutsPath(path), "opsadmin", "user:jsmith", time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	srv.users.Reload()
	id, _ := srv.sessions.start(session{user: "jsmith", cred: credPrint(srv.users.Current().Users["jsmith"].Password)})
	for _, c := range []string{id, lc.Value} {
		if rec := call(t, srv, "GET", "/api/me", "", withCookie(c)); !strings.Contains(rec.Body.String(), `"principal":"anonymous"`) {
			t.Errorf("a user:jsmith session still acts after its sign-out: %s", rec.Body)
		}
	}
}

// An OIDC session is fresh from when the person last entered
// credentials at the provider (auth_time), not from the callback: a token
// without auth_time, or one riding an hour-old session at the provider,
// holds no elevated permission. Past auth.elevation an elevated action is
// refused with the fix, and POST /api/elevate never tries a password
// against an OIDC name.
func TestOIDCElevationSaysSignInAgain(t *testing.T) {
	srv, fake, _ := oidcServer(t, Options{})
	for _, c := range []struct {
		what     string
		authTime time.Time
		want     int
	}{
		{"no auth_time", time.Time{}, http.StatusUnauthorized},
		{"auth_time an hour ago", time.Now().Add(-time.Hour), http.StatusUnauthorized},
		{"auth_time now", time.Now(), http.StatusOK},
		{"auth_time a minute ahead (within the 2 min skew)", time.Now().Add(time.Minute), http.StatusOK},
		{"auth_time an hour ahead (past the skew)", time.Now().Add(time.Hour), http.StatusUnauthorized},
	} {
		fake.as("eng@corp.example", "sub-e", "g-eng")
		fake.id.AuthTime = c.authTime
		_, ck := oidcSignIn(t, srv, fake, "/")
		if ck == nil {
			t.Fatalf("%s: no session", c.what)
		}
		rec := call(t, srv, "POST", "/api/forces", `{"name":"SP","value":1}`, withCookie(ck.Value), ownOrigin)
		if rec.Code != c.want {
			t.Errorf("%s: force %d %s, want %d", c.what, rec.Code, rec.Body, c.want)
		}
		call(t, srv, "DELETE", "/api/forces/SP", "", withCookie(ck.Value), ownOrigin)
	}
	fake.as("eng@corp.example", "sub-e", "g-eng")
	fake.id.AuthTime = time.Now()
	_, c := oidcSignIn(t, srv, fake, "/")
	if c == nil {
		t.Fatal("no session")
	}
	later := time.Now().Add(20 * time.Minute)
	srv.sessions.mu.Lock()
	srv.sessions.now = func() time.Time { return later }
	srv.sessions.mu.Unlock()
	rec := call(t, srv, "POST", "/api/forces", `{"name":"SP","value":1}`, withCookie(c.Value), ownOrigin)
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "then sign in again") || strings.Contains(rec.Body.String(), `"elevate"`) {
		t.Errorf("stale OIDC force: %d %s (want the fix, and no password prompt)", rec.Code, rec.Body)
	}
	rec = call(t, srv, "POST", "/api/elevate", `{"password":"anything"}`, withCookie(c.Value), fromLoopback, ownOrigin)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "then sign in again") {
		t.Errorf("elevate on an OIDC session: %d %s", rec.Code, rec.Body)
	}
}

// The start refuses what it must: an untrusted transport, a provider out
// of reach (with the local-user hint), and a controller without OIDC.
// With redirect-url set, a start at another origin goes to that one.
func TestOIDCStartRefusals(t *testing.T) {
	srv, fake, _ := oidcServer(t, Options{})
	if rec := call(t, srv, "GET", "/auth/oidc/start", ""); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "needs HTTPS") {
		t.Errorf("plain HTTP from afar: %d %s", rec.Code, rec.Body)
	}
	// An image another page loads is no sign-in, and starts none.
	img := func(r *http.Request) {
		r.Header.Set("Sec-Fetch-Mode", "no-cors")
		r.Header.Set("Sec-Fetch-Dest", "image")
	}
	before := len(srv.oidcFlows.m)
	if rec, _ := oidcStart(t, srv, "/", img); rec.Code != http.StatusForbidden || len(srv.oidcFlows.m) != before {
		t.Errorf("a start by an image: %d (flows %d -> %d)", rec.Code, before, len(srv.oidcFlows.m))
	}
	nav := func(r *http.Request) { r.Header.Set("Sec-Fetch-Mode", "navigate") }
	if rec, _ := oidcStart(t, srv, "/", nav); rec.Code != http.StatusSeeOther {
		t.Errorf("a navigation's start: %d %s", rec.Code, rec.Body)
	}
	fake.startErr = errors.New("the OIDC provider cannot be reached: discovery at https://idp.example: dial tcp: refused")
	held := len(srv.oidcFlows.m)
	if rec, _ := oidcStart(t, srv, "/"); rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "local users can still sign in") {
		t.Errorf("unreachable: %d %s", rec.Code, rec.Body)
	}
	if len(srv.oidcFlows.m) != held {
		t.Errorf("a failed start left a flow (%d -> %d)", held, len(srv.oidcFlows.m))
	}
	fake.startErr = nil

	cfg, fake2, _ := oidcServer(t, Options{OIDCRedirectURL: "https://plc.example/auth/oidc/callback"})
	rec, _ := oidcStart(t, cfg, "/x")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "https://plc.example/auth/oidc/start?next=%2Fx" {
		t.Errorf("start at another origin: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	_ = fake2

	plain := auditServer(t, Options{})
	if rec := call(t, plain, "GET", "/auth/oidc/start", "", fromLoopback); rec.Code != http.StatusNotFound {
		t.Errorf("no OIDC: %d", rec.Code)
	}
}

// The redirect URI is built only from a Host the Host rule trusts.
func TestOIDCRedirectURIFromTrustedHostOnly(t *testing.T) {
	srv, _, _ := oidcServer(t, Options{AllowedHosts: []string{"plc.plant.example"}})
	for host, want := range map[string]string{
		"plc.plant.example":      "http://plc.plant.example/auth/oidc/callback",
		"10.0.0.5:8086":          "http://10.0.0.5:8086/auth/oidc/callback",
		"localhost":              "http://localhost/auth/oidc/callback",
		"evil.example":           "",
		"plc.plant.example.evil": "",
	} {
		r := httptest.NewRequest("GET", "http://"+host+"/auth/oidc/start", nil)
		got, ok := srv.oidcRedirectURI(r)
		if got != want || ok != (want != "") {
			t.Errorf("Host %s: %q %v, want %q", host, got, ok, want)
		}
	}
	// What naut run prints is what a sign-in sends, port included.
	for _, c := range []struct {
		tls  bool
		port string
	}{{true, "8443"}, {false, "8086"}, {true, "443"}, {false, "80"}} {
		scheme := "http"
		if c.tls {
			scheme = "https"
		}
		r := httptest.NewRequest("GET", scheme+"://plc.plant.example:"+c.port+"/auth/oidc/start", nil)
		if c.port == "443" || c.port == "80" {
			r.Host = "plc.plant.example"
		}
		if c.tls {
			r.TLS = &tls.ConnectionState{}
		}
		used, _ := srv.oidcRedirectURI(r)
		if uris := srv.OIDCRedirectURIs(c.tls, c.port); len(uris) != 1 || uris[0] != used {
			t.Errorf("%s port %s: printed %v, a sign-in sends %s", scheme, c.port, uris, used)
		}
	}
}

// The sign-in page offers the provider, and the read gate lets both hops
// through without a session.
func TestSignInPageOffersTheProvider(t *testing.T) {
	srv, _, _ := oidcServer(t, Options{})
	rec := call(t, srv, "GET", "/signin?next=/hmi", "", fromLoopback)
	body := rec.Body.String()
	if !strings.Contains(body, `href="/auth/oidc/start?next=%2fhmi"`) && !strings.Contains(body, `href="/auth/oidc/start?next=%2Fhmi"`) {
		t.Errorf("no provider link: %s", body)
	}
	if !strings.Contains(body, "Sign in with Plant SSO") || !strings.Contains(body, `action="/api/signin"`) {
		t.Errorf("sign-in page: %s", body)
	}
	nav := func(r *http.Request) { r.Header.Set("Sec-Fetch-Mode", "navigate"); r.Header.Set("Accept", "text/html") }
	if rec := call(t, srv, "GET", "/auth/oidc/callback?state=x", "", fromLoopback, nav); rec.Code == http.StatusSeeOther {
		t.Errorf("the read gate sent the callback to /signin")
	}
}

// Every doubt about the pins is a no: a pins file that cannot be read
// refuses each OIDC sign-in and ends each OIDC session's acting, never
// reads as "no pins yet".
func TestOIDCPinsUnreadableFailClosed(t *testing.T) {
	srv, fake, path := oidcServer(t, Options{})
	fake.as("jsmith@corp.example", "sub-1")
	_, c := oidcSignIn(t, srv, fake, "/")
	if c == nil {
		t.Fatal("no session")
	}
	if err := os.WriteFile(users.PinsPath(path), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv.users.Reload()
	if rec := call(t, srv, "GET", "/api/me", "", withCookie(c.Value)); !strings.Contains(rec.Body.String(), `"principal":"anonymous"`) {
		t.Errorf("session acts with an unreadable pins file: %s", rec.Body)
	}
	fake.as("other@corp.example", "sub-2", "g-ops")
	if cb, c := oidcSignIn(t, srv, fake, "/"); c != nil || cb.Code != http.StatusServiceUnavailable {
		t.Errorf("sign-in with an unreadable pins file: %d %s", cb.Code, cb.Body)
	}
}

// What the callback refuses on the provider's side: a groups claim no
// grant can be read from, a provider out of reach at the exchange (503,
// the local-user hint); and a sign-in ends the session the browser held.
func TestOIDCCallbackMore(t *testing.T) {
	srv, fake, _ := oidcServer(t, Options{})
	fake.as("x@corp.example", "sub-1", "g-ops", "bad\nid")
	if cb, c := oidcSignIn(t, srv, fake, "/"); c != nil || cb.Code != http.StatusForbidden || !strings.Contains(cb.Body.String(), "groups claim") {
		t.Errorf("a control character in a group id: %d %s", cb.Code, cb.Body)
	}
	many := make([]string, maxOIDCGroups+1)
	for i := range many {
		many[i] = "g-" + strconv.Itoa(i)
	}
	fake.as("x@corp.example", "sub-1", many...)
	if cb, c := oidcSignIn(t, srv, fake, "/"); c != nil || cb.Code != http.StatusForbidden {
		t.Errorf("too many groups: %d %s", cb.Code, cb.Body)
	}
	fake.err = errors.Join(oidcseam.ErrUnreachable, errors.New("token endpoint: dial tcp: refused"))
	if cb, c := oidcSignIn(t, srv, fake, "/"); c != nil || cb.Code != http.StatusServiceUnavailable || !strings.Contains(cb.Body.String(), "cannot be reached") {
		t.Errorf("unreachable at the exchange: %d %s", cb.Code, cb.Body)
	}
	fake.err = nil

	// Fixation: the session the browser held before is ended.
	old := signIn(t, srv, "jsmith")
	fake.as("x@corp.example", "sub-1", "g-ops")
	rec, binding := oidcStart(t, srv, "/")
	if rec.Code != http.StatusSeeOther {
		t.Fatal(rec.Code)
	}
	cb := oidcCallback(t, srv, fake.last.State, binding, func(r *http.Request) { r.AddCookie(old) })
	if cb.Code != http.StatusOK {
		t.Fatalf("callback: %d %s", cb.Code, cb.Body)
	}
	if _, ok := srv.sessions.lookup(old.Value); ok {
		t.Error("the session the browser held before the sign-in still lives")
	}

	// An acknowledgement by an OIDC session is recorded under its name,
	// not whatever the client typed.
	var sc *http.Cookie
	for _, c := range cb.Result().Cookies() {
		if c.Name == plainCookie && c.Value != "" {
			sc = c
		}
	}
	req := httptest.NewRequest("POST", "http://localhost/", nil)
	req.AddCookie(sc)
	if got := srv.recordBy(req, "someone-else"); got != "oidc:x@corp.example" {
		t.Errorf("recordBy = %q", got)
	}
}

// The table of sign-ins under way is bounded: past maxOIDCFlows a start is
// refused, once the expired ones are swept.
func TestOIDCFlowsBounded(t *testing.T) {
	f := newOIDCFlows()
	for i := 0; i < maxOIDCFlows; i++ {
		if !f.put(strconv.Itoa(i), oidcFlow{addr: "a" + strconv.Itoa(i/maxOIDCFlowsPerAddr)}) {
			t.Fatalf("refused at %d", i)
		}
	}
	if f.put("one-more", oidcFlow{addr: "fresh"}) {
		t.Error("took one past the cap")
	}
	// An address at its share gives way: its oldest sign-in ends, the new
	// one is kept, the table stays full.
	if !f.put("a0-again", oidcFlow{addr: "a0"}) || len(f.m) != maxOIDCFlows {
		t.Errorf("an address at its share was refused (%d flows)", len(f.m))
	}
	f.now = func() time.Time { return time.Now().Add(oidcFlowLife) }
	if !f.put("after-expiry", oidcFlow{addr: "fresh"}) || len(f.m) != 1 {
		t.Errorf("expired flows not swept: %d", len(f.m))
	}
	// One address holds only its share: the flood ends its own oldest,
	// never refuses, and others still start.
	g := newOIDCFlows()
	base := time.Now()
	for i := 0; i <= maxOIDCFlowsPerAddr; i++ {
		at := base.Add(time.Duration(i) * time.Second)
		g.now = func() time.Time { return at }
		if !g.put("x"+strconv.Itoa(i), oidcFlow{addr: "flooder"}) {
			t.Fatalf("an address's start %d was refused", i)
		}
	}
	if _, ok := g.m["x0"]; ok || len(g.m) != maxOIDCFlowsPerAddr {
		t.Errorf("the oldest did not give way: %d flows, x0 kept %v", len(g.m), ok)
	}
	if !g.put("y", oidcFlow{addr: "someone"}) {
		t.Error("a flooding address kept another from starting")
	}
}

// A username claim that only lowers into another name (U+0130 to 'i',
// the Kelvin sign to 'k') is refused, never folded into the ASCII name and
// its grant; so are an issuer or subject the pins file cannot hold.
func TestOIDCNamesAreNotFolded(t *testing.T) {
	srv, fake, _ := oidcServer(t, Options{})
	for _, name := range []string{"JSM\u0130TH@corp.example", "a\u212Ahan"} {
		fake.as(name, "attacker", "g-ops")
		cb, c := oidcSignIn(t, srv, fake, "/")
		if c != nil || cb.Code != http.StatusForbidden {
			t.Errorf("%q: %d %s", name, cb.Code, cb.Body)
		}
	}
	if _, ok, _ := srv.users.Pin("jsmith@corp.example"); ok {
		t.Error("a look-alike pinned the ASCII name")
	}
	fake.as("x@corp.example", "sub\x1b[31m", "g-ops")
	if cb, c := oidcSignIn(t, srv, fake, "/"); c != nil || cb.Code != http.StatusUnauthorized {
		t.Errorf("a subject with an escape: %d %s", cb.Code, cb.Body)
	}
}

// A second tab's start does not overwrite the first's binding cookie:
// both sign-ins complete.
func TestOIDCTwoSignInsUnderWay(t *testing.T) {
	srv, fake, _ := oidcServer(t, Options{})
	fake.as("x@corp.example", "sub-1", "g-ops")
	_, c1 := oidcStart(t, srv, "/one")
	first := fake.last
	_, c2 := oidcStart(t, srv, "/two")
	if c1.Name == c2.Name {
		t.Fatalf("one cookie name for two sign-ins: %s", c1.Name)
	}
	// The provider answers the first tab (its nonce and verifier).
	fake.mu.Lock()
	fake.last = first
	fake.mu.Unlock()
	cb := oidcCallback(t, srv, first.State, c1, func(r *http.Request) { r.AddCookie(c2) })
	if cb.Code != http.StatusOK || !strings.Contains(cb.Body.String(), "url=/one") {
		t.Errorf("the first tab's sign-in after a second start: %d %s", cb.Code, cb.Body)
	}
}

// With redirect-url naming https at this very host, a request that
// arrived as http (a TLS proxy server.trusted-proxies does not name) is
// refused with the fix, not sent round in a loop.
func TestOIDCStartNoRedirectLoop(t *testing.T) {
	srv, _, _ := oidcServer(t, Options{OIDCRedirectURL: "https://localhost/auth/oidc/callback"})
	rec, _ := oidcStart(t, srv, "/")
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "server.trusted-proxies") {
		t.Errorf("%d %s %s", rec.Code, rec.Header().Get("Location"), rec.Body)
	}
}
