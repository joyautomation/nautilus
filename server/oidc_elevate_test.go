package server

import (
	"encoding/json"
	"path/filepath"

	"github.com/joyautomation/nautilus/auth/users"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

// Elevation at the OIDC provider (#708 PR 6b): oidc_elevate.go.

func sameSiteNav(r *http.Request) {
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.Header.Set("Sec-Fetch-Mode", "navigate")
	r.Header.Set("Sec-Fetch-Dest", "document")
}

func crossSiteNav(r *http.Request) {
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	r.Header.Set("Sec-Fetch-Mode", "navigate")
	r.Header.Set("Sec-Fetch-Dest", "document")
}

func header(k, v string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set(k, v) }
}

// staleOIDC signs eng@corp.example (g-eng: an engineer, who holds force)
// in at the provider with an hour-old auth_time: a session that holds the
// elevated permissions but is not fresh. Its next confirmation's
// auth_time is now.
func staleOIDC(t *testing.T, srv *Server, fake *fakeOIDC) *http.Cookie {
	t.Helper()
	fake.as("eng@corp.example", "sub-e", "g-eng")
	fake.id.AuthTime = time.Now().Add(-time.Hour)
	_, c := oidcSignIn(t, srv, fake, "/")
	if c == nil {
		t.Fatal("no session")
	}
	fake.mu.Lock()
	fake.id.AuthTime = time.Now()
	fake.mu.Unlock()
	return c
}

// elevateStart is the HMI sending the page to start?elevate=1 (same-site,
// with the session cookie when sess is set). It returns the answer and the
// binding cookie it set.
func elevateStart(t *testing.T, srv *Server, sess *http.Cookie, next string, mod ...func(*http.Request)) (*httptest.ResponseRecorder, *http.Cookie) {
	t.Helper()
	mods := []func(*http.Request){fromLoopback, sameSiteNav}
	if sess != nil {
		mods = append(mods, withCookie(sess.Value))
	}
	rec := call(t, srv, "GET", OIDCElevateURL+"&next="+url.QueryEscape(next), "", append(mods, mod...)...)
	for _, c := range rec.Result().Cookies() {
		if strings.HasPrefix(c.Name, oidcPlainCookie+"_") {
			return rec, c
		}
	}
	return rec, nil
}

var ticketRe = regexp.MustCompile(`href="` + regexp.QuoteMeta(oidcElevatedPath) + `\?t=([A-Za-z0-9_-]+)"`)

// elevateCallback is the provider sending the browser back: cross-site,
// so with the binding cookie (Lax) and no session cookie (Strict). It
// returns the answer and the ticket its hop page moves on with ("" when
// none).
func elevateCallback(t *testing.T, srv *Server, fake *fakeOIDC, binding *http.Cookie, extra ...func(*http.Request)) (*httptest.ResponseRecorder, string) {
	t.Helper()
	fake.mu.Lock()
	state := fake.last.State
	fake.mu.Unlock()
	rec := oidcCallback(t, srv, state, binding, append([]func(*http.Request){crossSiteNav}, extra...)...)
	if m := ticketRe.FindStringSubmatch(rec.Body.String()); m != nil {
		return rec, m[1]
	}
	return rec, ""
}

// elevateFinish is the hop page moving on: same-site, with the session
// cookie (when sess is set) and the binding cookie (when set).
func elevateFinish(t *testing.T, srv *Server, ticket string, sess, binding *http.Cookie, extra ...func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	mods := []func(*http.Request){fromLoopback, sameSiteNav}
	if sess != nil {
		mods = append(mods, withCookie(sess.Value))
	}
	if binding != nil {
		mods = append(mods, func(r *http.Request) { r.AddCookie(binding) })
	}
	return call(t, srv, "GET", oidcElevatedPath+"?t="+url.QueryEscape(ticket), "", append(mods, extra...)...)
}

// forceCode is what an elevated action (a force) answers sess.
func forceCode(t *testing.T, srv *Server, sess *http.Cookie) int {
	t.Helper()
	rec := call(t, srv, "POST", "/api/forces", `{"name":"SP","value":1}`, withCookie(sess.Value), ownOrigin)
	call(t, srv, "DELETE", "/api/forces/SP", "", withCookie(sess.Value), ownOrigin)
	return rec.Code
}

func sessionCount(srv *Server) int {
	srv.sessions.mu.Lock()
	defer srv.sessions.mu.Unlock()
	return len(srv.sessions.byHash)
}

// A stale OIDC session confirms at the provider and acts again: the start
// asks the provider for the credentials (prompt=login, max_age=0); the
// cross-site callback verifies and parks the answer but changes nothing
// yet; the same-site step with the session's cookie refreshes THAT
// session (no new one) and goes back to next; the journal says
// auth.elevate, method oidc, under the session.
func TestOIDCElevateConfirmsThatSession(t *testing.T) {
	srv, fake, _ := oidcServer(t, Options{})
	c := staleOIDC(t, srv, fake)
	if fake.last.Login {
		t.Error("a sign-in asked the provider for a login (prompt=login)")
	}
	if code := forceCode(t, srv, c); code != http.StatusUnauthorized {
		t.Fatalf("stale session forces: %d", code)
	}
	n := sessionCount(srv)

	rec, b := elevateStart(t, srv, c, "/hmi/pump?x=1")
	if rec.Code != http.StatusSeeOther || b == nil || !strings.HasPrefix(rec.Header().Get("Location"), "https://idp.example/") {
		t.Fatalf("start: %d %s (binding %v)", rec.Code, rec.Body, b)
	}
	if !fake.last.Login {
		t.Error("the confirmation did not ask the provider for the credentials again (Login)")
	}
	cb, tk := elevateCallback(t, srv, fake, b)
	if cb.Code != http.StatusOK || tk == "" {
		t.Fatalf("callback: %d %s", cb.Code, cb.Body)
	}
	for _, ck := range cb.Result().Cookies() {
		if ck.Name == plainCookie {
			t.Errorf("the callback set a session cookie: %v", ck)
		}
	}
	if code := forceCode(t, srv, c); code != http.StatusUnauthorized {
		t.Errorf("between the two steps the session forces: %d", code)
	}
	fin := elevateFinish(t, srv, tk, c, b)
	if fin.Code != http.StatusSeeOther || fin.Header().Get("Location") != "/hmi/pump?x=1" {
		t.Fatalf("finish: %d %s (Location %q)", fin.Code, fin.Body, fin.Header().Get("Location"))
	}
	cleared := false
	for _, ck := range fin.Result().Cookies() {
		if ck.Name == b.Name && ck.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Error("the binding cookie was not cleared")
	}
	if code := forceCode(t, srv, c); code != http.StatusOK {
		t.Errorf("confirmed session forces: %d", code)
	}
	if got := sessionCount(srv); got != n {
		t.Errorf("sessions %d after the confirmation, want %d (no new session)", got, n)
	}
	var ok bool
	for _, e := range srv.audit.ring.Recent() {
		if e.Action == "auth.elevate" && e.Result == "ok" && e.User == "oidc:eng@corp.example" && e.Session == sessionHash(c.Value) && e.Detail["method"] == "oidc" {
			ok = true
		}
	}
	if !ok {
		t.Error("no auth.elevate ok, method oidc, under the session")
	}
}

// lastElevate is the newest auth.elevate entry.
func lastElevate(t *testing.T, srv *Server) (user, session, result, reason string) {
	t.Helper()
	for _, e := range srv.audit.ring.Recent() {
		if e.Action == "auth.elevate" {
			r, _ := e.Detail["reason"].(string)
			if e.Detail["method"] != "oidc" {
				t.Errorf("auth.elevate without method oidc: %v", e.Detail)
			}
			return e.User, e.Session, string(e.Result), r
		}
	}
	t.Fatal("no auth.elevate journalled")
	return
}

// The start refuses, journalled and never sending the browser to the
// provider: no session (a link from another site carries no Strict
// cookie, and must not turn into a sign-in), a request that says it is
// cross-site or not a top-level navigation, a token, a local user (who
// confirms a password), and an OIDC name that holds nothing elevated.
func TestOIDCElevateStartRefusals(t *testing.T) {
	stale := staleOIDC
	local := func(t *testing.T, srv *Server, _ *fakeOIDC) *http.Cookie { return signIn(t, srv, "akhan") }
	viewer := func(t *testing.T, srv *Server, fake *fakeOIDC) *http.Cookie {
		fake.as("akhan", "sub-a")
		_, c := oidcSignIn(t, srv, fake, "/")
		if c == nil {
			t.Fatal("no viewer session")
		}
		return c
	}
	none := func(*testing.T, *Server, *fakeOIDC) *http.Cookie { return nil }
	for _, tc := range []struct {
		what   string
		sess   func(*testing.T, *Server, *fakeOIDC) *http.Cookie
		mod    func(*http.Request)
		code   int
		reason string
	}{
		{"no session (a link from another site)", none, func(*http.Request) {}, http.StatusUnauthorized, "no session"},
		{"Sec-Fetch-Site cross-site", stale, header("Sec-Fetch-Site", "cross-site"), http.StatusForbidden, "not from this controller's page"},
		{"Sec-Fetch-Site same-site (a sibling host)", stale, header("Sec-Fetch-Site", "same-site"), http.StatusForbidden, "not from this controller's page"},
		{"Sec-Fetch-Site none (a link opened from mail or chat)", stale, header("Sec-Fetch-Site", "none"), http.StatusForbidden, "not from this controller's page"},
		{"Sec-Fetch-Dest iframe", stale, header("Sec-Fetch-Dest", "iframe"), http.StatusForbidden, "not a document"},
		{"Sec-Fetch-Mode no-cors", stale, header("Sec-Fetch-Mode", "no-cors"), http.StatusForbidden, "not a navigation"},
		{"a bearer token", stale, bearer("whatever"), http.StatusBadRequest, "bearer credential"},
		{"a local user's session", local, func(*http.Request) {}, http.StatusBadRequest, "not an oidc session"},
		{"an OIDC viewer", viewer, func(*http.Request) {}, http.StatusForbidden, "no elevated permission"},
	} {
		// A server each: the journal folds repeated refusals into one.
		srv, fake, _ := oidcServer(t, Options{})
		c := tc.sess(t, srv, fake)
		before := fake.last.State
		rec, b := elevateStart(t, srv, c, "/", tc.mod)
		if rec.Code != tc.code || b != nil || rec.Header().Get("Location") != "" {
			t.Errorf("%s: %d %s (Location %q), want %d", tc.what, rec.Code, rec.Body, rec.Header().Get("Location"), tc.code)
			continue
		}
		if fake.last.State != before {
			t.Errorf("%s: a flow was started at the provider", tc.what)
		}
		if _, _, res, reason := lastElevate(t, srv); res != "refused" || reason != tc.reason {
			t.Errorf("%s: journalled %s %q, want refused %q", tc.what, res, reason, tc.reason)
		}
	}
	srv, fake, _ := oidcServer(t, Options{})
	c := staleOIDC(t, srv, fake)
	// A sign-in's start refuses a frame too (#616).
	rec := call(t, srv, "GET", "/auth/oidc/start?next=/", "", fromLoopback, header("Sec-Fetch-Dest", "iframe"))
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "frame") {
		t.Errorf("sign-in start in a frame: %d %s", rec.Code, rec.Body)
	}
	// The same-site start is fine without Fetch Metadata (an older browser).
	if rec, b := elevateStart(t, srv, c, "/", func(r *http.Request) {
		r.Header.Del("Sec-Fetch-Site")
		r.Header.Del("Sec-Fetch-Mode")
		r.Header.Del("Sec-Fetch-Dest")
	}); rec.Code != http.StatusSeeOther || b == nil {
		t.Errorf("start without Fetch Metadata: %d %s", rec.Code, rec.Body)
	}
}

// With auth.oidc.redirect-url set, the provider sends the browser back to
// that origin: a confirmation started at another is refused, naming the
// fix, rather than redirected (its session cookie is host-only).
func TestOIDCElevateStartAtRedirectURLOrigin(t *testing.T) {
	srv, _, _ := oidcServer(t, Options{OIDCRedirectURL: "https://plc.example/auth/oidc/callback"})
	if _, _, err := srv.users.PinFirst("eng@corp.example", "https://idp.example", "sub-e", time.Now()); err != nil {
		t.Fatal(err)
	}
	id, _ := srv.sessions.start(session{user: "eng@corp.example", oidc: true, issuer: "https://idp.example", sub: "sub-e", groups: []string{"g-eng"}})
	rec, _ := elevateStart(t, srv, &http.Cookie{Value: id}, "/")
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "https://plc.example") || rec.Header().Get("Location") != "" {
		t.Errorf("start away from redirect-url's origin: %d %s (Location %q)", rec.Code, rec.Body, rec.Header().Get("Location"))
	}
}

// The callback refuses, journalled under the session, and the session
// stays stale: another account at the provider; no auth_time (a provider
// error under max_age, naming the setting); an auth_time from before the
// start (the provider ignored max_age=0) or far ahead; other groups; the
// provider's error; a session signed out mid-flow; no binding cookie.
func TestOIDCElevateCallbackRefusals(t *testing.T) {
	for _, tc := range []struct {
		what   string
		set    func(f *fakeOIDC)
		mod    func(*http.Request)
		code   int
		reason string
		msg    string
	}{
		{what: "another nonce", set: func(f *fakeOIDC) { f.badNonce = true }, code: http.StatusUnauthorized, reason: "nonce mismatch"},
		{what: "another account", set: func(f *fakeOIDC) { f.id.Subject = "sub-other" }, code: http.StatusForbidden, reason: "another account", msg: "another account"},
		{what: "another issuer", set: func(f *fakeOIDC) { f.id.Issuer = "https://other.example" }, code: http.StatusForbidden, reason: "another account"},
		{what: "no auth_time", set: func(f *fakeOIDC) { f.id.AuthTime = time.Time{} }, code: http.StatusBadGateway, reason: "no auth_time", msg: "optional claim auth_time"},
		{what: "auth_time before the start", set: func(f *fakeOIDC) { f.id.AuthTime = time.Now().Add(-5 * time.Minute) }, code: http.StatusUnauthorized, reason: "auth_time before start", msg: "max_age=0"},
		{what: "auth_time an hour ahead", set: func(f *fakeOIDC) { f.id.AuthTime = time.Now().Add(time.Hour) }, code: http.StatusUnauthorized, reason: "auth_time ahead"},
		{what: "a group more", set: func(f *fakeOIDC) { f.id.Groups = []string{"g-eng", "g-ops"} }, code: http.StatusForbidden, reason: "groups changed"},
		{what: "a group less", set: func(f *fakeOIDC) { f.id.Groups = nil }, code: http.StatusForbidden, reason: "groups changed"},
		{what: "the provider's error", mod: func(r *http.Request) { r.URL.RawQuery += "&error=access_denied" }, code: http.StatusUnauthorized, reason: "provider error: access_denied", msg: "still signed in"},
	} {
		srv, fake, _ := oidcServer(t, Options{})
		c := staleOIDC(t, srv, fake)
		_, b := elevateStart(t, srv, c, "/")
		if b == nil {
			t.Fatalf("%s: no start", tc.what)
		}
		fake.mu.Lock()
		if tc.set != nil {
			tc.set(fake)
		}
		fake.mu.Unlock()
		mod := tc.mod
		if mod == nil {
			mod = func(*http.Request) {}
		}
		rec, tk := elevateCallback(t, srv, fake, b, mod)
		if rec.Code != tc.code || tk != "" || !strings.Contains(rec.Body.String(), tc.msg) {
			t.Errorf("%s: %d %s, want %d with %q", tc.what, rec.Code, rec.Body, tc.code, tc.msg)
		}
		user, sess, res, reason := lastElevate(t, srv)
		if user != "oidc:eng@corp.example" || sess != sessionHash(c.Value) || res != "refused" || reason != tc.reason {
			t.Errorf("%s: journalled %s %s %s %q, want the session's refusal %q", tc.what, user, sess, res, reason, tc.reason)
		}
		if code := forceCode(t, srv, c); code != http.StatusUnauthorized {
			t.Errorf("%s: the session forces after the refusal: %d", tc.what, code)
		}
	}
	// Signed out mid-flow: the flow dies with its session.
	srv, fake, _ := oidcServer(t, Options{})
	c := staleOIDC(t, srv, fake)
	_, b := elevateStart(t, srv, c, "/")
	if rec := call(t, srv, "POST", "/api/signout", "", withCookie(c.Value), fromLoopback, ownOrigin); rec.Code != http.StatusOK {
		t.Fatalf("sign-out: %d %s", rec.Code, rec.Body)
	}
	if rec, tk := elevateCallback(t, srv, fake, b); rec.Code != http.StatusUnauthorized || tk != "" {
		t.Errorf("callback after a sign-out: %d %s", rec.Code, rec.Body)
	}
	if _, _, _, reason := lastElevate(t, srv); reason != "session ended" {
		t.Errorf("sign-out mid-flow journalled %q", reason)
	}
	// No binding cookie: another browser.
	c = staleOIDC(t, srv, fake)
	elevateStart(t, srv, c, "/")
	if rec, tk := elevateCallback(t, srv, fake, nil); rec.Code != http.StatusBadRequest || tk != "" {
		t.Errorf("callback without the binding cookie: %d %s", rec.Code, rec.Body)
	}
	// Once only: the state is spent.
	c = staleOIDC(t, srv, fake)
	_, b = elevateStart(t, srv, c, "/")
	if _, tk := elevateCallback(t, srv, fake, b); tk == "" {
		t.Fatal("no ticket")
	}
	if rec, tk := elevateCallback(t, srv, fake, b); rec.Code != http.StatusBadRequest || tk != "" {
		t.Errorf("the callback again: %d %s", rec.Code, rec.Body)
	}
}

// The second step refuses, and the session stays stale: a ticket used
// twice or expired; no session cookie; another session's cookie (the
// callback for X arriving on Y); a session signed out between the steps;
// a cross-site request; no binding cookie.
func TestOIDCElevateFinishRefusals(t *testing.T) {
	type setup struct {
		srv  *Server
		fake *fakeOIDC
		c, b *http.Cookie
		tk   string
	}
	ready := func(t *testing.T) setup {
		srv, fake, _ := oidcServer(t, Options{})
		c := staleOIDC(t, srv, fake)
		_, b := elevateStart(t, srv, c, "/")
		_, tk := elevateCallback(t, srv, fake, b)
		if tk == "" {
			t.Fatal("no ticket")
		}
		return setup{srv, fake, c, b, tk}
	}
	t.Run("twice", func(t *testing.T) {
		s := ready(t)
		if rec := elevateFinish(t, s.srv, s.tk, s.c, s.b); rec.Code != http.StatusSeeOther {
			t.Fatalf("first: %d %s", rec.Code, rec.Body)
		}
		if rec := elevateFinish(t, s.srv, s.tk, s.c, s.b); rec.Code != http.StatusBadRequest {
			t.Errorf("second: %d %s", rec.Code, rec.Body)
		}
	})
	t.Run("expired", func(t *testing.T) {
		s := ready(t)
		s.srv.oidcTickets.mu.Lock()
		s.srv.oidcTickets.now = func() time.Time { return time.Now().Add(oidcTicketLife) }
		s.srv.oidcTickets.mu.Unlock()
		if rec := elevateFinish(t, s.srv, s.tk, s.c, s.b); rec.Code != http.StatusBadRequest {
			t.Errorf("expired: %d %s", rec.Code, rec.Body)
		}
		if code := forceCode(t, s.srv, s.c); code != http.StatusUnauthorized {
			t.Errorf("forces after an expired ticket: %d", code)
		}
	})
	t.Run("no session cookie", func(t *testing.T) {
		s := ready(t)
		if rec := elevateFinish(t, s.srv, s.tk, nil, s.b); rec.Code != http.StatusForbidden {
			t.Errorf("%d %s", rec.Code, rec.Body)
		}
		if code := forceCode(t, s.srv, s.c); code != http.StatusUnauthorized {
			t.Errorf("forces: %d", code)
		}
		// Spent: the right cookie afterwards does not complete it.
		if rec := elevateFinish(t, s.srv, s.tk, s.c, s.b); rec.Code != http.StatusBadRequest {
			t.Errorf("after a refusal: %d %s", rec.Code, rec.Body)
		}
	})
	t.Run("another session's cookie", func(t *testing.T) {
		s := ready(t)
		// Y: the same account, another sign-in (another tab's, or one
		// that replaced X's cookie in this browser).
		s.fake.mu.Lock()
		s.fake.id.AuthTime = time.Now().Add(-time.Hour)
		s.fake.mu.Unlock()
		_, y := oidcSignIn(t, s.srv, s.fake, "/")
		if y == nil {
			t.Fatal("no second session")
		}
		if rec := elevateFinish(t, s.srv, s.tk, y, s.b); rec.Code != http.StatusForbidden {
			t.Errorf("%d %s", rec.Code, rec.Body)
		}
		if _, _, _, reason := lastElevate(t, s.srv); reason != "another session" {
			t.Errorf("journalled %q", reason)
		}
		for name, ck := range map[string]*http.Cookie{"X": s.c, "Y": y} {
			if code := forceCode(t, s.srv, ck); code != http.StatusUnauthorized {
				t.Errorf("%s forces: %d", name, code)
			}
		}
	})
	t.Run("another person's session", func(t *testing.T) {
		s := ready(t)
		other := signIn(t, s.srv, "akhan")
		if rec := elevateFinish(t, s.srv, s.tk, other, s.b); rec.Code != http.StatusForbidden {
			t.Errorf("%d %s", rec.Code, rec.Body)
		}
		var found bool
		for _, e := range s.srv.audit.ring.Recent() {
			if e.Action == "auth.elevate" && e.Detail["by"] == "user:akhan" && e.Detail["bySession"] == sessionHash(other.Value) && e.User == "oidc:eng@corp.example" && e.Session == sessionHash(s.c.Value) {
				found = true
			}
		}
		if !found {
			t.Error("the refusal does not name who presented the ticket beside whose it was")
		}
	})
	t.Run("signed out between the steps", func(t *testing.T) {
		s := ready(t)
		call(t, s.srv, "POST", "/api/signout", "", withCookie(s.c.Value), fromLoopback, ownOrigin)
		if rec := elevateFinish(t, s.srv, s.tk, s.c, s.b); rec.Code != http.StatusUnauthorized {
			t.Errorf("%d %s", rec.Code, rec.Body)
		}
		if _, _, _, reason := lastElevate(t, s.srv); reason != "session ended" {
			t.Errorf("journalled %q", reason)
		}
	})
	t.Run("cross-site", func(t *testing.T) {
		s := ready(t)
		if rec := elevateFinish(t, s.srv, s.tk, s.c, s.b, header("Sec-Fetch-Site", "cross-site")); rec.Code != http.StatusForbidden {
			t.Errorf("%d %s", rec.Code, rec.Body)
		}
		if code := forceCode(t, s.srv, s.c); code != http.StatusUnauthorized {
			t.Errorf("forces: %d", code)
		}
	})
	t.Run("no binding cookie", func(t *testing.T) {
		s := ready(t)
		if rec := elevateFinish(t, s.srv, s.tk, s.c, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("%d %s", rec.Code, rec.Body)
		}
		if code := forceCode(t, s.srv, s.c); code != http.StatusUnauthorized {
			t.Errorf("forces: %d", code)
		}
	})
}

// A confirmation under way is the session's: sign-ins started by anyone
// at the same address (their cap is per address) do not evict it.
func TestOIDCElevateFlowIsTheSessions(t *testing.T) {
	srv, fake, _ := oidcServer(t, Options{})
	c := staleOIDC(t, srv, fake)
	_, b := elevateStart(t, srv, c, "/")
	fake.mu.Lock()
	saved := fake.last
	fake.mu.Unlock()
	srv.signin.oidc = newLimiter(1000, time.Second)
	for i := 0; i < maxOIDCFlowsPerAddr+2; i++ {
		if rec, _ := oidcStart(t, srv, "/"); rec.Code != http.StatusSeeOther {
			t.Fatalf("sign-in start %d: %d %s", i, rec.Code, rec.Body)
		}
	}
	fake.mu.Lock()
	fake.last = saved
	fake.mu.Unlock()
	if rec, tk := elevateCallback(t, srv, fake, b); tk == "" {
		t.Errorf("the confirmation was evicted by sign-ins from the same address: %d %s", rec.Code, rec.Body)
	}
}

// /api/me says how this controller signs people in, the provider's name,
// and how a session is confirmed again.
func TestMeSaysSignInMethods(t *testing.T) {
	srv, fake, _ := oidcServer(t, Options{})
	var me struct {
		SignInMethods []string `json:"signInMethods"`
		Provider      string   `json:"provider"`
		Session       *struct {
			Method string `json:"method"`
		} `json:"session"`
	}
	get := func(mod ...func(*http.Request)) {
		t.Helper()
		me.Session = nil
		if err := json.Unmarshal(call(t, srv, "GET", "/api/me", "", mod...).Body.Bytes(), &me); err != nil {
			t.Fatal(err)
		}
	}
	get()
	if strings.Join(me.SignInMethods, ",") != "password,oidc" || me.Provider != "Plant SSO" || me.Session != nil {
		t.Errorf("anonymous: %+v", me)
	}
	get(withCookie(staleOIDC(t, srv, fake).Value))
	if me.Session == nil || me.Session.Method != "oidc" {
		t.Errorf("OIDC session: %+v", me.Session)
	}
	get(withCookie(signIn(t, srv, "akhan").Value))
	if me.Session == nil || me.Session.Method != "password" {
		t.Errorf("local session: %+v", me.Session)
	}
	plain, _, _ := patServer(t)
	me.SignInMethods, me.Provider = nil, ""
	if err := json.Unmarshal(call(t, plain, "GET", "/api/me", "").Body.Bytes(), &me); err != nil {
		t.Fatal(err)
	}
	if strings.Join(me.SignInMethods, ",") != "password" || me.Provider != "" {
		t.Errorf("no OIDC: %+v", me)
	}
}

// A facade's refusal for want of a fresh OIDC sign-in sends the page to
// the provider as the server's own do; any other refusal does not.
func TestWriteRefusalSendsOIDCToTheProvider(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteRefusal(rec, http.StatusUnauthorized, ElevationAtProvider+": edit needs it")
	var body map[string]any
	json.Unmarshal(rec.Body.Bytes(), &body)
	if body["elevate"] != "oidc" || body["url"] != OIDCElevateURL || !strings.HasPrefix(body["error"].(string), ElevationAtProvider) {
		t.Errorf("401: %v", body)
	}
	rec = httptest.NewRecorder()
	WriteRefusal(rec, http.StatusForbidden, ElevationAtProvider)
	body = nil
	json.Unmarshal(rec.Body.Bytes(), &body)
	if _, ok := body["elevate"]; ok {
		t.Errorf("403: %v", body)
	}
}

// A confirmation never moves a session's authAt back, and applies only to
// an OIDC session of the same account.
func TestSessionsElevatedOIDC(t *testing.T) {
	ss := newSessions(time.Hour)
	_, se := ss.start(session{user: "e", oidc: true, issuer: "i", sub: "s", authAt: time.Now()})
	_, local := ss.start(session{user: "l", cred: "c"})
	old := time.Now().Add(-10 * time.Minute)
	if got, ok := ss.elevatedOIDC(se.hash, "i", "s", old); !ok || !got.authAt.Equal(se.authAt) {
		t.Errorf("moved back: %v %v", ok, got.authAt)
	}
	for _, c := range []struct{ hash, iss, sub string }{{se.hash, "i", "other"}, {se.hash, "other", "s"}, {local.hash, "", ""}, {"nope", "i", "s"}} {
		if _, ok := ss.elevatedOIDC(c.hash, c.iss, c.sub, time.Now()); ok {
			t.Errorf("applied to %v", c)
		}
	}
}

// A token that repeats a group id signs in (its groups are kept sorted
// and without repeats) and confirms too: the confirmation's groups are
// compared in the same form.
func TestOIDCElevateRepeatedGroup(t *testing.T) {
	srv, fake, _ := oidcServer(t, Options{})
	fake.as("eng@corp.example", "sub-e", "g-eng", "g-eng")
	fake.id.AuthTime = time.Now().Add(-time.Hour)
	_, c := oidcSignIn(t, srv, fake, "/")
	if c == nil {
		t.Fatal("no session")
	}
	fake.mu.Lock()
	fake.id.AuthTime = time.Now()
	fake.mu.Unlock()
	_, b := elevateStart(t, srv, c, "/")
	rec, tk := elevateCallback(t, srv, fake, b)
	if tk == "" {
		t.Fatalf("refused: %d %s", rec.Code, rec.Body)
	}
	if rec := elevateFinish(t, srv, tk, c, b); rec.Code != http.StatusSeeOther {
		t.Errorf("finish: %d %s", rec.Code, rec.Body)
	}
}

// The session's authAt is the provider's auth_time, not the moment the
// browser came back: elevatedUntil is auth_time + auth.elevation. And the
// 2 min skew is the bound: an auth_time 1 min before the start counts, 3
// min does not.
func TestOIDCElevateAuthAtIsAuthTime(t *testing.T) {
	for _, tc := range []struct {
		ago time.Duration
		ok  bool
	}{{90 * time.Second, true}, {3 * time.Minute, false}} {
		srv, fake, _ := oidcServer(t, Options{})
		c := staleOIDC(t, srv, fake)
		_, b := elevateStart(t, srv, c, "/")
		at := time.Now().Add(-tc.ago).Truncate(time.Second)
		fake.mu.Lock()
		fake.id.AuthTime = at
		fake.mu.Unlock()
		_, tk := elevateCallback(t, srv, fake, b)
		if (tk != "") != tc.ok {
			t.Errorf("auth_time %v before the start: ticket %v, want %v", tc.ago, tk != "", tc.ok)
			continue
		}
		if !tc.ok {
			continue
		}
		elevateFinish(t, srv, tk, c, b)
		var me struct {
			Session struct {
				ElevatedUntil time.Time `json:"elevatedUntil"`
			} `json:"session"`
		}
		json.Unmarshal(call(t, srv, "GET", "/api/me", "", withCookie(c.Value)).Body.Bytes(), &me)
		if want := at.Add(srv.policy.Elevation()); !me.Session.ElevatedUntil.Equal(want) {
			t.Errorf("elevatedUntil %v, want auth_time + elevation = %v", me.Session.ElevatedUntil, want)
		}
	}
}

// naut users signout (or unpin) on the controller refuses a confirmation
// under way, at either step.
func TestOIDCElevateRevokedMidFlow(t *testing.T) {
	revoke := map[string]func(t *testing.T, srv *Server, path string){
		"signout": func(t *testing.T, srv *Server, path string) {
			if err := users.SignOut(users.SignoutsPath(path), "opsadmin", "oidc:eng@corp.example", time.Now().Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			srv.users.Reload()
		},
		"unpin": func(t *testing.T, srv *Server, path string) {
			if err := os.Remove(filepath.Join(filepath.Dir(path), users.PinsName)); err != nil {
				t.Fatal(err)
			}
			srv.users.Reload()
		},
	}
	for name, rv := range revoke {
		for _, step := range []int{1, 2} {
			srv, fake, path := oidcServer(t, Options{})
			c := staleOIDC(t, srv, fake)
			_, b := elevateStart(t, srv, c, "/")
			if step == 1 {
				rv(t, srv, path)
			}
			rec, tk := elevateCallback(t, srv, fake, b)
			if step == 1 {
				if tk != "" || rec.Code != http.StatusUnauthorized {
					t.Errorf("%s before step 1: %d %s", name, rec.Code, rec.Body)
				}
				continue
			}
			rv(t, srv, path)
			if rec := elevateFinish(t, srv, tk, c, b); rec.Code != http.StatusUnauthorized {
				t.Errorf("%s before step 2: %d %s", name, rec.Code, rec.Body)
			}
			if _, _, _, reason := lastElevate(t, srv); reason != "session ended" {
				t.Errorf("%s before step 2: journalled %q", name, reason)
			}
		}
	}
}

// The limits: starts are throttled per session; a full ticket table
// refuses (busy), never grows.
func TestOIDCElevateLimits(t *testing.T) {
	srv, fake, _ := oidcServer(t, Options{})
	c := staleOIDC(t, srv, fake)
	srv.signin.sessions = newLimiter(1, time.Hour)
	if rec, b := elevateStart(t, srv, c, "/"); b == nil {
		t.Fatalf("first start: %d %s", rec.Code, rec.Body)
	}
	if rec, _ := elevateStart(t, srv, c, "/"); rec.Code != http.StatusTooManyRequests {
		t.Errorf("second start: %d %s", rec.Code, rec.Body)
	}
	srv.signin.sessions = newLimiter(1000, time.Second)
	_, b := elevateStart(t, srv, c, "/")
	for i := 0; i < maxOIDCFlows; i++ {
		srv.oidcTickets.put(oidcTicket{})
	}
	if rec, tk := elevateCallback(t, srv, fake, b); tk != "" || rec.Code != http.StatusServiceUnavailable {
		t.Errorf("full ticket table: %d %s", rec.Code, rec.Body)
	}
}

// The callback rechecks the session as every request does
// (sessionStands), not only that it is in memory: one whose pin no longer
// names its account is refused, though nothing ended it yet.
func TestOIDCElevateCallbackRechecksTheSession(t *testing.T) {
	srv, fake, _ := oidcServer(t, Options{})
	c := staleOIDC(t, srv, fake)
	_, b := elevateStart(t, srv, c, "/")
	srv.sessions.mu.Lock()
	srv.sessions.byHash[sessionHash(c.Value)].sub = "sub-elsewhere"
	srv.sessions.mu.Unlock()
	if rec, tk := elevateCallback(t, srv, fake, b); tk != "" || rec.Code != http.StatusUnauthorized {
		t.Errorf("callback for a session that no longer stands: %d %s", rec.Code, rec.Body)
	}
}
