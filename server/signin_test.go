package server

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/audit"
	"github.com/joyautomation/nautilus/auth/users"
)

// The sign-in fixture is patServer's: akhan (engineer) and jsmith
// (operator over SP), each with password "password-<name>", and ghost, a
// box user the auth: block does not name. call's requests are to
// http://localhost from 192.0.2.1.

func fromLoopback(r *http.Request) { r.RemoteAddr = "127.0.0.1:40000" }
func ownOrigin(r *http.Request)    { r.Header.Set("Origin", "http://localhost") }
func asJSON(r *http.Request)       { r.Header.Set("Content-Type", "application/json") }
func asForm(r *http.Request) {
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
}
func withCookie(id string) func(*http.Request) {
	return func(r *http.Request) { r.AddCookie(&http.Cookie{Name: sessionCookie, Value: id}) }
}

// signIn signs name in from loopback, from the controller's own page, and
// returns the session cookie.
func signIn(t *testing.T, srv *Server, name string) *http.Cookie {
	t.Helper()
	rec := call(t, srv, "POST", "/api/signin", `{"name":"`+name+`","password":"password-`+name+`"}`, fromLoopback, ownOrigin, asJSON)
	if rec.Code != http.StatusOK {
		t.Fatalf("sign-in as %s: %d %s", name, rec.Code, rec.Body)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie {
			return c
		}
	}
	t.Fatalf("sign-in as %s set no cookie", name)
	return nil
}

// A sign-in sets a cookie that is HttpOnly, SameSite=Strict, Path=/,
// host-only and lasts auth.session; Secure only over TLS. The browser then
// acts as its user: writes within the user's grants, /api/me, and a
// journal entry that names the user and the session's hash, never its id.
func TestSessionCookieActsAsItsUser(t *testing.T) {
	srv, _, _ := patServer(t)
	c := signIn(t, srv, "jsmith")
	if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || c.Domain != "" || c.Secure {
		t.Errorf("cookie %+v: want HttpOnly, SameSite=Strict, Path=/, host-only, not Secure over loopback HTTP", c)
	}
	if c.MaxAge != int((12 * time.Hour).Seconds()) {
		t.Errorf("cookie Max-Age %d, want 12h", c.MaxAge)
	}
	if len(c.Value) < 43 {
		t.Errorf("session id %q: want 256 random bits", c.Value)
	}
	rec := call(t, srv, "POST", "/api/tags", `{"name":"SP","value":42}`, withCookie(c.Value), ownOrigin)
	es := srv.audit.ring.Recent()
	if rec.Code != http.StatusNoContent || es[0].User != "user:jsmith" || es[0].Session != sessionHash(c.Value) {
		t.Errorf("write by jsmith's cookie: %d %s, journalled %+v", rec.Code, rec.Body, es[0])
	}
	if es[0].Session == c.Value {
		t.Error("the journal holds the session id itself")
	}
	// Outside jsmith's area: the user's own grants, not more.
	if rec := call(t, srv, "POST", "/api/tags", `{"name":"Out","value":1}`, withCookie(c.Value), ownOrigin); rec.Code != http.StatusForbidden {
		t.Errorf("jsmith's cookie outside the area: %d", rec.Code)
	}
	body := call(t, srv, "GET", "/api/me", "", withCookie(c.Value)).Body.String()
	if !strings.Contains(body, `"principal":"user:jsmith"`) || !strings.Contains(body, `"session":{"expires"`) {
		t.Errorf("/api/me with the cookie: %s", body)
	}

	// Over TLS the cookie is Secure.
	rec = call(t, srv, "POST", "/api/signin", `{"name":"akhan","password":"password-akhan"}`, ownOrigin, asJSON,
		func(r *http.Request) { r.TLS = &tls.ConnectionState{}; r.Header.Set("Origin", "https://localhost") })
	if cs := rec.Result().Cookies(); rec.Code != http.StatusOK || len(cs) != 1 || !cs[0].Secure {
		t.Errorf("sign-in over TLS: %d %v, want a Secure cookie", rec.Code, cs)
	}
}

// auth.session sets the lifetime, absolute from the sign-in: use does not
// renew it, and past it the cookie is anonymous.
func TestSessionExpires(t *testing.T) {
	srv, _, _ := patServer(t)
	now := time.Now()
	srv.sessions.now = func() time.Time { return now }
	c := signIn(t, srv, "akhan")
	now = now.Add(11 * time.Hour)
	if body := call(t, srv, "GET", "/api/me", "", withCookie(c.Value)).Body.String(); !strings.Contains(body, "user:akhan") {
		t.Fatalf("at 11h: %s", body)
	}
	now = now.Add(time.Hour)
	if body := call(t, srv, "GET", "/api/me", "", withCookie(c.Value)).Body.String(); !strings.Contains(body, `"principal":"anonymous"`) {
		t.Errorf("at 12h the session still acts: %s", body)
	}
}

// A cookie-authenticated write comes from this controller's own page: an
// Origin naming its Host. With none (SameSite ignores the port, so another
// service on the host would carry the cookie) or another, it is refused,
// and journalled. A bearer write needs no Origin, as before.
func TestCookieWriteNeedsItsOwnOrigin(t *testing.T) {
	srv, toks, _ := patServer(t)
	c := signIn(t, srv, "akhan")
	for origin, want := range map[string]int{"": http.StatusForbidden, "http://localhost:3000": http.StatusForbidden, "http://evil.example": http.StatusForbidden, "http://localhost": http.StatusNoContent} {
		rec := call(t, srv, "POST", "/api/tags", `{"name":"SP","value":7}`, withCookie(c.Value), func(r *http.Request) {
			if origin != "" {
				r.Header.Set("Origin", origin)
			}
		})
		if rec.Code != want {
			t.Errorf("cookie write with Origin %q: %d %s, want %d", origin, rec.Code, rec.Body, want)
		}
		if es := srv.audit.ring.Recent(); es[0].Action != "tag.write" || es[0].User != "user:akhan" {
			t.Errorf("Origin %q: journalled %+v", origin, es[0])
		}
	}
	// A facade's own route asks Authorize: the same rule.
	r := httptest.NewRequest("PUT", "http://localhost/api/program", nil)
	withCookie(c.Value)(r)
	if code, _ := srv.Authorize(r, PermEdit, ""); code != http.StatusForbidden {
		t.Errorf("Authorize, cookie and no Origin: %d, want 403", code)
	}
	if rec := call(t, srv, "POST", "/api/tags", `{"name":"SP","value":8}`, bearer(toks["akhan"])); rec.Code != http.StatusNoContent {
		t.Errorf("bearer write without an Origin: %d", rec.Code)
	}
}

// The cookie is never a credential to the Host rule or CORS (design §2,
// gate 1): by a Host a page could rebind, a signed-in browser is refused,
// and another origin's page is not let read.
func TestCookieNeverPassesTheHostRule(t *testing.T) {
	srv, _, _ := patServer(t)
	c := signIn(t, srv, "akhan")
	r := httptest.NewRequest("GET", "http://plc.example/api/state", nil)
	withCookie(c.Value)(r)
	if rec := serve(srv, r); rec.Code != http.StatusForbidden {
		t.Errorf("cookie by an untrusted Host: %d, want 403", rec.Code)
	}
	r = httptest.NewRequest("GET", "http://localhost/api/me", nil)
	withCookie(c.Value)(r)
	r.Header.Set("Origin", "http://hmi.example")
	if got := serve(srv, r).Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("cookie from another origin: Access-Control-Allow-Origin %q", got)
	}
}

// A cookie the controller does not know (a restart forgot it) is
// anonymous, not an error; a bad bearer credential stays a 401 and never
// falls back to a good cookie.
func TestUnknownCookieIsAnonymous(t *testing.T) {
	srv, _, _ := patServer(t)
	rec := call(t, srv, "GET", "/api/me", "", withCookie("forgotten"))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"principal":"anonymous"`) {
		t.Errorf("/api/me with an unknown cookie: %d %s", rec.Code, rec.Body)
	}
	if rec := call(t, srv, "GET", "/api/state", "", withCookie("forgotten")); rec.Code != http.StatusOK {
		t.Errorf("a read with an unknown cookie: %d", rec.Code)
	}
	c := signIn(t, srv, "akhan")
	rec = call(t, srv, "POST", "/api/tags", `{"name":"SP","value":9}`, withCookie(c.Value), ownOrigin, bearer(users.TokenPrefix+"0123456789abcdef_guess"))
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "invalid credential") {
		t.Errorf("a bad bearer beside a good cookie: %d %s, want 401 invalid credential", rec.Code, rec.Body)
	}
}

// Every refused name and password is told the same thing with the same
// status; the journal records which it was, and names the user only when
// the name has a user name's form.
func TestSignInRefusalsLookAlike(t *testing.T) {
	srv, _, path := patServer(t)
	if err := users.Update(path, "opsadmin", func(f *users.File) error { return f.SetDisabled("jsmith", true) }); err != nil {
		t.Fatal(err)
	}
	srv.users.Reload()
	var first string
	for _, c := range []struct{ name, password, reason, target string }{
		{"akhan", "wrong-password", "wrong password", "user:akhan"},
		{"nobody", "password-nobody", "no such user", "user:nobody"},
		{"jsmith", "password-jsmith", "the user is disabled", "user:jsmith"},
		{"ghost", "password-ghost", errNoGrant.Error(), "user:ghost"},
		{"Not A Name!", "x", "no such user", ""},
	} {
		rec := call(t, srv, "POST", "/api/signin", `{"name":"`+c.name+`","password":"`+c.password+`"}`, fromLoopback, ownOrigin, asJSON)
		if rec.Code != http.StatusUnauthorized || len(rec.Result().Cookies()) != 0 {
			t.Errorf("%s: %d %v, want 401 and no cookie", c.name, rec.Code, rec.Result().Cookies())
		}
		if first == "" {
			first = rec.Body.String()
		} else if rec.Body.String() != first {
			t.Errorf("%s is told %s, the others %s", c.name, rec.Body, first)
		}
		e := srv.audit.ring.Recent()[0]
		if e.Action != "auth.signin" || e.Result != audit.ResultRefused || e.User != "anonymous" || e.Target != c.target || e.Detail["reason"] != c.reason {
			t.Errorf("%s: journalled %+v, want reason %q target %q", c.name, e, c.reason, c.target)
		}
	}
}

// A refusal costs what a sign-in costs: each goes through the password
// hash's slots, the grant checked after it. With both slots taken and the
// client gone, ghost (right password, no grant) gets "busy", as everyone
// would, not an early 401.
func TestEveryRefusalWaitsForTheHash(t *testing.T) {
	srv, _, _ := patServer(t)
	for range signInHashing {
		srv.signin.hashing <- struct{}{}
	}
	for _, name := range []string{"ghost", "nobody", "akhan"} {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		rec := call(t, srv, "POST", "/api/signin", `{"name":"`+name+`","password":"password-`+name+`"}`, fromLoopback, ownOrigin, asJSON,
			func(r *http.Request) { *r = *r.WithContext(ctx) })
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s with the hash busy: %d %s, want 503", name, rec.Code, rec.Body)
		}
	}
}

// Password sign-in needs TLS (the controller's own, or a trusted proxy's
// https), or loopback with no forwarding header at all.
func TestSignInNeedsTLSOrLoopback(t *testing.T) {
	srv, _, _ := patServer(t)
	srv.proxies = []netip.Prefix{netip.MustParsePrefix("10.0.0.5/32")}
	ok := `{"name":"akhan","password":"password-akhan"}`
	for _, c := range []struct {
		name string
		mod  func(*http.Request)
		want int
	}{
		{"plain HTTP from the network", func(*http.Request) {}, http.StatusForbidden},
		{"loopback", fromLoopback, http.StatusOK},
		{"loopback with X-Forwarded-For", func(r *http.Request) { fromLoopback(r); r.Header.Set("X-Forwarded-For", "192.0.2.4") }, http.StatusForbidden},
		{"loopback with X-Forwarded-Proto", func(r *http.Request) { fromLoopback(r); r.Header.Set("X-Forwarded-Proto", "https") }, http.StatusForbidden},
		{"loopback with Forwarded", func(r *http.Request) { fromLoopback(r); r.Header.Set("Forwarded", "for=192.0.2.4") }, http.StatusForbidden},
		{"TLS", func(r *http.Request) { r.TLS = &tls.ConnectionState{} }, http.StatusOK},
		{"a trusted proxy's https", func(r *http.Request) { r.RemoteAddr = "10.0.0.5:1"; r.Header.Set("X-Forwarded-Proto", "https") }, http.StatusOK},
		{"an untrusted proxy's https", func(r *http.Request) { r.RemoteAddr = "10.0.0.6:1"; r.Header.Set("X-Forwarded-Proto", "https") }, http.StatusForbidden},
	} {
		rec := call(t, srv, "POST", "/api/signin", ok, ownOrigin, asJSON, c.mod)
		if rec.Code != c.want {
			t.Errorf("%s: %d %s, want %d", c.name, rec.Code, rec.Body, c.want)
		}
	}
}

// Sign-in is accepted only from the controller's own page (login CSRF: a
// form elsewhere must not sign the operator in as someone else).
func TestSignInNeedsItsOwnOrigin(t *testing.T) {
	srv, _, _ := patServer(t)
	for _, origin := range []string{"", "null", "http://evil.example", "http://localhost:8080"} {
		rec := call(t, srv, "POST", "/api/signin", `{"name":"akhan","password":"password-akhan"}`, fromLoopback, asJSON, func(r *http.Request) {
			if origin != "" {
				r.Header.Set("Origin", origin)
			}
		})
		if rec.Code != http.StatusForbidden || len(rec.Result().Cookies()) != 0 {
			t.Errorf("Origin %q: %d %v, want 403 and no cookie", origin, rec.Code, rec.Result().Cookies())
		}
	}
}

// Attempts are throttled per address (tightly) and per name (loosely)
// before any hashing: a flood on akhan from one address delays that
// address, never akhan from another. Throttling only slows: the bucket
// refills. (Refusals past the first few from one address are journalled
// as a summary, as every refusal flood is.)
func TestSignInThrottles(t *testing.T) {
	srv, _, _ := patServer(t)
	now := time.Now()
	clock := func() time.Time { return now }
	srv.signin.addrs.now, srv.signin.names.now = clock, clock
	from := func(addr string) func(*http.Request) {
		return func(r *http.Request) { r.RemoteAddr = addr + ":1"; r.TLS = &tls.ConnectionState{} }
	}
	attempt := func(addr, pw string) *httptest.ResponseRecorder {
		return call(t, srv, "POST", "/api/signin", `{"name":"akhan","password":"`+pw+`"}`, ownOrigin, asJSON, from(addr))
	}
	for i := range 10 {
		if rec := attempt("192.0.2.50", "guess"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d", i+1, rec.Code)
		}
	}
	rec := attempt("192.0.2.50", "guess")
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Errorf("the 11th attempt from one address: %d Retry-After %q, want 429", rec.Code, rec.Header().Get("Retry-After"))
	}
	if rec := attempt("192.0.2.51", "password-akhan"); rec.Code != http.StatusOK {
		t.Errorf("akhan from another address during the flood: %d %s", rec.Code, rec.Body)
	}
	// Throttled before hashing: with the hash busy it is still a 429.
	for range signInHashing {
		srv.signin.hashing <- struct{}{}
	}
	if rec := attempt("192.0.2.50", "guess"); rec.Code != http.StatusTooManyRequests {
		t.Errorf("throttled with the hash busy: %d, want 429", rec.Code)
	}
	for range signInHashing {
		<-srv.signin.hashing
	}
	now = now.Add(6 * time.Second)
	if rec := attempt("192.0.2.50", "password-akhan"); rec.Code != http.StatusOK {
		t.Errorf("after the refill: %d", rec.Code)
	}
}

// A sign-in's redirect stays on this controller.
func TestSafeNext(t *testing.T) {
	for in, want := range map[string]string{
		"":                       "/",
		"/hmi/pumps?x=1#a":       "/hmi/pumps?x=1#a",
		"//evil.example/":        "/",
		"/\\evil.example/":       "/",
		"https://evil.example/":  "/",
		"javascript:alert(1)":    "/",
		"evil.example":           "/",
		"/ok\r\nSet-Cookie: x=1": "/",
		"/%2F%2Fevil.example":    "/%2F%2Fevil.example",
	} {
		if got := safeNext(in); got != want {
			t.Errorf("safeNext(%q) = %q, want %q", in, got, want)
		}
	}
}

// The form flow: the page posts name, password and next; success is a 303
// to next (on this controller), a refusal the page again with the message.
func TestSignInForm(t *testing.T) {
	srv, _, _ := patServer(t)
	rec := call(t, srv, "GET", "/signin?next=/hmi/pumps", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `value="/hmi/pumps"`) || !strings.Contains(rec.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Errorf("GET /signin: %d %q %s", rec.Code, rec.Header().Get("Content-Security-Policy"), rec.Body)
	}
	if strings.Contains(rec.Header().Get("Referrer-Policy"), "no-referrer") {
		t.Error("Referrer-Policy no-referrer makes the form's POST send Origin: null")
	}
	form := url.Values{"name": {"akhan"}, "password": {"password-akhan"}, "next": {"/hmi/pumps"}}.Encode()
	rec = call(t, srv, "POST", "/api/signin", form, fromLoopback, ownOrigin, asForm)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/hmi/pumps" || len(rec.Result().Cookies()) != 1 {
		t.Errorf("form sign-in: %d Location %q", rec.Code, rec.Header().Get("Location"))
	}
	form = url.Values{"name": {"akhan"}, "password": {"secret-typo"}, "next": {"//evil.example"}}.Encode()
	rec = call(t, srv, "POST", "/api/signin", form, fromLoopback, ownOrigin, asForm)
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), signInRefused) || strings.Contains(rec.Body.String(), "secret-typo") || strings.Contains(rec.Body.String(), "evil.example") {
		t.Errorf("refused form: %d %s", rec.Code, rec.Body)
	}
}

// Sign-out ends the session on the controller (the cookie is refused
// afterwards, not just forgotten by the browser), clears the cookie and is
// journalled. From another page it is refused and the session lives on.
func TestSignOutRevokes(t *testing.T) {
	srv, _, _ := patServer(t)
	c := signIn(t, srv, "akhan")
	if rec := call(t, srv, "POST", "/api/signout", "", withCookie(c.Value)); rec.Code != http.StatusForbidden {
		t.Errorf("sign-out without an Origin: %d", rec.Code)
	}
	if body := call(t, srv, "GET", "/api/me", "", withCookie(c.Value)).Body.String(); !strings.Contains(body, "user:akhan") {
		t.Fatalf("the refused sign-out ended the session: %s", body)
	}
	rec := call(t, srv, "POST", "/api/signout", "", withCookie(c.Value), ownOrigin)
	cs := rec.Result().Cookies()
	if rec.Code != http.StatusOK || len(cs) != 1 || cs[0].Name != sessionCookie || cs[0].MaxAge >= 0 {
		t.Errorf("sign-out: %d %v, want the cookie cleared", rec.Code, cs)
	}
	e := srv.audit.ring.Recent()[0]
	if e.Action != "auth.signout" || e.User != "user:akhan" || e.Session != sessionHash(c.Value) {
		t.Errorf("sign-out journalled %+v", e)
	}
	if rec := call(t, srv, "POST", "/api/tags", `{"name":"SP","value":3}`, withCookie(c.Value), ownOrigin); rec.Code != http.StatusUnauthorized {
		t.Errorf("the signed-out cookie writes: %d", rec.Code)
	}
}

// Signing in again replaces the session the browser held (a fresh id each
// time; the old one ends).
func TestSignInReplacesTheBrowsersSession(t *testing.T) {
	srv, _, _ := patServer(t)
	old := signIn(t, srv, "akhan")
	rec := call(t, srv, "POST", "/api/signin", `{"name":"jsmith","password":"password-jsmith"}`, fromLoopback, ownOrigin, asJSON, withCookie(old.Value))
	if rec.Code != http.StatusOK || rec.Result().Cookies()[0].Value == old.Value {
		t.Fatalf("second sign-in: %d", rec.Code)
	}
	if body := call(t, srv, "GET", "/api/me", "", withCookie(old.Value)).Body.String(); !strings.Contains(body, `"principal":"anonymous"`) {
		t.Errorf("the replaced session still acts: %s", body)
	}
}

// Disabling a user, changing their password or removing them ends their
// sessions at once (enabling them again does not bring one back), and the
// users.change entry says how many; other users' sessions are untouched.
func TestUsersChangeEndsSessions(t *testing.T) {
	srv, _, path := patServer(t)
	for _, c := range []struct {
		what string
		do   func(*users.File) error
		undo func(*users.File) error
	}{
		{"disabled", func(f *users.File) error { return f.SetDisabled("jsmith", true) }, func(f *users.File) error { return f.SetDisabled("jsmith", false) }},
		{"password", func(f *users.File) error { return f.SetPassword("jsmith", "new-password") }, func(f *users.File) error { return f.SetPassword("jsmith", "password-jsmith") }},
		{"removed", func(f *users.File) error { delete(f.Users, "jsmith"); return nil }, func(f *users.File) error { return f.Add("jsmith", "password-jsmith") }},
	} {
		js, ak := signIn(t, srv, "jsmith"), signIn(t, srv, "akhan")
		for _, fn := range []func(*users.File) error{c.do, c.undo} {
			if err := users.Update(path, "opsadmin", fn); err != nil {
				t.Fatal(err)
			}
			srv.users.Reload()
		}
		if body := call(t, srv, "GET", "/api/me", "", withCookie(js.Value)).Body.String(); !strings.Contains(body, `"principal":"anonymous"`) {
			t.Errorf("%s: jsmith's session survived: %s", c.what, body)
		}
		if body := call(t, srv, "GET", "/api/me", "", withCookie(ak.Value)).Body.String(); !strings.Contains(body, "user:akhan") {
			t.Errorf("%s: akhan's session ended too: %s", c.what, body)
		}
		found := false
		for _, e := range srv.audit.ring.Recent() {
			if e.Action == "users.change" && e.Detail["change"] == c.what && e.Detail["sessions"] == 1 {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s: no users.change entry with sessions: 1", c.what)
		}
	}
}

// One user holds maxUserSessions at most: a sign-in past it ends the
// oldest. Each end is told (5b-2 closes the session's streams from it).
func TestSessionsPerUserAreBounded(t *testing.T) {
	ss := newSessions(time.Hour)
	var ended []string
	ss.OnEnd(func(se session) { ended = append(ended, se.hash) })
	first, _ := ss.open("akhan")
	for range maxUserSessions - 1 {
		ss.open("akhan")
	}
	other, _ := ss.open("jsmith")
	if _, ok := ss.lookup(first); !ok || len(ended) != 0 {
		t.Fatalf("at the limit: first live %v, ended %d", ok, len(ended))
	}
	ss.open("akhan")
	if _, ok := ss.lookup(first); ok {
		t.Error("past the limit the oldest session lives on")
	}
	if len(ended) != 1 || ended[0] != sessionHash(first) {
		t.Errorf("ended %v, want the oldest", ended)
	}
	if _, ok := ss.lookup(other); !ok {
		t.Error("another user's session ended")
	}
	if n := ss.endUser("akhan"); n != maxUserSessions || len(ended) != 1+maxUserSessions {
		t.Errorf("endUser: %d ended, %d told", n, len(ended))
	}
}

// Sign-in and sign-out journal themselves (they are outside the audited
// seam: TestEveryWriteRouteIsAudited exempts exactly them), each with one
// entry, and work while the journal cannot be written.
func TestSignInAndOutJournalThemselves(t *testing.T) {
	srv, _, _ := patServer(t)
	srv.audit.unavailable = "disk full"
	n := len(srv.audit.ring.Recent())
	c := signIn(t, srv, "akhan")
	call(t, srv, "POST", "/api/signin", `{"name":"akhan","password":"nope"}`, fromLoopback, ownOrigin, asJSON)
	call(t, srv, "POST", "/api/signout", "", withCookie(c.Value), ownOrigin, fromLoopback)
	es := srv.audit.ring.Recent()
	if len(es)-n != 3 {
		t.Fatalf("%d entries, want 3: %+v", len(es)-n, es)
	}
	got := []string{es[2].Action + " " + es[2].Result, es[1].Action + " " + es[1].Result, es[0].Action + " " + es[0].Result}
	if strings.Join(got, ", ") != "auth.signin ok, auth.signin refused, auth.signout ok" {
		t.Errorf("entries %v", got)
	}
	for _, e := range es[:3] {
		if e.From != "127.0.0.1" || e.Source != audit.SourceHMI {
			t.Errorf("entry from %q source %q", e.From, e.Source)
		}
	}
}

// Without users to sign in (no auth: block, or one that grants no user),
// sign-in says so, and the page does too.
func TestSignInNotSetUp(t *testing.T) {
	srv := New(newTestRuntime(t))
	rec := call(t, srv, "POST", "/api/signin", `{"name":"akhan","password":"x"}`, fromLoopback, ownOrigin, asJSON)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "auth.users") {
		t.Errorf("sign-in without users: %d %s", rec.Code, rec.Body)
	}
	if body := call(t, srv, "GET", "/signin", "").Body.String(); !strings.Contains(body, "not set up") {
		t.Errorf("the page without users: %s", body)
	}
}

// A session acts only while its user is in the users file and enabled: a
// file the controller cannot trust (others may read it) leaves no local
// user, so the cookie is anonymous until it is fixed.
func TestSessionNeedsTheUsersFile(t *testing.T) {
	srv, _, path := patServer(t)
	c := signIn(t, srv, "akhan")
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	srv.users.Reload()
	if srv.users.Err() == nil {
		t.Fatal("a users file others may read was accepted")
	}
	if body := call(t, srv, "GET", "/api/me", "", withCookie(c.Value)).Body.String(); !strings.Contains(body, `"principal":"anonymous"`) {
		t.Errorf("with the users file unreadable the session acts: %s", body)
	}
	os.Chmod(path, 0o600)
	srv.users.Reload()
	if body := call(t, srv, "GET", "/api/me", "", withCookie(c.Value)).Body.String(); !strings.Contains(body, "user:akhan") {
		t.Errorf("with the file fixed: %s", body)
	}
}
