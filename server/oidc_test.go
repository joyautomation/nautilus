package server

import (
	"context"
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
)

// fakeOIDC is the provider side of the seam: it hands out an
// authorization URL and answers Exchange with the identity a test sets.
// It checks the server sends back the redirect URI and verifier the
// sign-in started with.
type fakeOIDC struct {
	mu       sync.Mutex
	last     OIDCAuthRequest
	id       OIDCIdentity
	err      error // Exchange's
	startErr error // AuthCodeURL's
	badNonce bool
}

func (f *fakeOIDC) Provider() string { return "Plant SSO" }

func (f *fakeOIDC) AuthCodeURL(_ context.Context, req OIDCAuthRequest) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.startErr != nil {
		return "", f.startErr
	}
	f.last = req
	return "https://idp.example/authorize?state=" + url.QueryEscape(req.State), nil
}

func (f *fakeOIDC) Exchange(_ context.Context, redirect, code, verifier string) (OIDCIdentity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return OIDCIdentity{}, f.err
	}
	if redirect != f.last.RedirectURI || verifier != f.last.Verifier || code != "code-1" {
		return OIDCIdentity{}, errors.New("fake: redirect, verifier or code mismatch")
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
	f.id = OIDCIdentity{Issuer: "https://idp.example", Subject: sub, Name: name, NameClaim: "preferred_username", Groups: groups}
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
		if c.Name == oidcPlainCookie {
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
		case oidcPlainCookie:
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
	if _, c := oidcSignIn(t, srv, fake, "/"); c == nil {
		t.Error("after unpin, the new account could not sign in")
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
	other := &http.Cookie{Name: oidcPlainCookie, Value: "someone-elses"}
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
	fake.id = OIDCIdentity{Issuer: "https://idp.example", Subject: "sub-o", Name: "many@corp.example", GroupsOverage: true}
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
	// The recheck alone (no reload's end): a session opened before the
	// recorded sign-out does not act.
	se := session{user: "jsmith", cred: credPrint(srv.users.Current().Users["jsmith"].Password)}
	id, _ := srv.sessions.start(se)
	if err := users.SignOut(users.SignoutsPath(path), "opsadmin", "user:jsmith", time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	srv.users.Reload()
	for _, c := range []string{id, lc.Value} {
		if rec := call(t, srv, "GET", "/api/me", "", withCookie(c)); !strings.Contains(rec.Body.String(), `"principal":"anonymous"`) {
			t.Errorf("a user:jsmith session still acts after its sign-out: %s", rec.Body)
		}
	}
}

// An OIDC session has no password to confirm: past auth.elevation an
// elevated action is refused with the fix (sign out and in again), and
// POST /api/elevate never tries a password against an OIDC name.
func TestOIDCElevationSaysSignInAgain(t *testing.T) {
	srv, fake, _ := oidcServer(t, Options{})
	fake.as("eng@corp.example", "sub-e", "g-eng")
	_, c := oidcSignIn(t, srv, fake, "/")
	if c == nil {
		t.Fatal("no session")
	}
	later := time.Now().Add(20 * time.Minute)
	srv.sessions.mu.Lock()
	srv.sessions.now = func() time.Time { return later }
	srv.sessions.mu.Unlock()
	rec := call(t, srv, "POST", "/api/forces", `{"name":"SP","value":1}`, withCookie(c.Value), ownOrigin)
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "sign out and sign in again") {
		t.Errorf("stale OIDC force: %d %s", rec.Code, rec.Body)
	}
	rec = call(t, srv, "POST", "/api/elevate", `{"password":"anything"}`, withCookie(c.Value), fromLoopback, ownOrigin)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "sign out and sign in again") {
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
	fake.startErr = errors.New("the OIDC provider cannot be reached: discovery at https://idp.example: dial tcp: refused")
	if rec, _ := oidcStart(t, srv, "/"); rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "local users can still sign in") {
		t.Errorf("unreachable: %d %s", rec.Code, rec.Body)
	}
	if len(srv.oidcFlows.m) != 0 {
		t.Errorf("a failed start left %d flows", len(srv.oidcFlows.m))
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
	if uris := srv.OIDCRedirectURIs(true); len(uris) != 1 || uris[0] != "https://plc.plant.example/auth/oidc/callback" {
		t.Errorf("OIDCRedirectURIs %v", uris)
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
