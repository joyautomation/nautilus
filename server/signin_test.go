package server

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/audit"
	"github.com/joyautomation/nautilus/auth/users"
	"github.com/joyautomation/nautilus/leader"
)

// The sign-in fixture is patServer's: akhan (engineer) and jsmith
// (operator over SP), each with password "password-<name>", and ghost, a
// box user the auth: block does not name. call's requests are to
// http://localhost from 192.0.2.1.

func fromLoopback(r *http.Request) { r.RemoteAddr = "127.0.0.1:40000" }
func ownOrigin(r *http.Request)    { r.Header.Set("Origin", "http://localhost") }
func asJSON(r *http.Request)       { r.Header.Set("Content-Type", "application/json") }

// overTLS is a request that reached the controller over TLS, from its own
// https page.
func overTLS(r *http.Request) {
	r.TLS = &tls.ConnectionState{}
	r.Header.Set("Origin", "https://localhost")
}
func asForm(r *http.Request) {
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
}
func withCookie(id string) func(*http.Request) {
	return func(r *http.Request) { r.AddCookie(&http.Cookie{Name: plainCookie, Value: id}) }
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
		if c.Name == plainCookie {
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
	rec = call(t, srv, "POST", "/api/signin", `{"name":"akhan","password":"password-akhan"}`, asJSON, overTLS)
	cs := rec.Result().Cookies()
	if rec.Code != http.StatusOK || len(cs) != 1 || cs[0].Name != secureCookie || !cs[0].Secure || cs[0].Path != "/" || cs[0].Domain != "" {
		t.Fatalf("sign-in over TLS: %d %+v, want __Host-nautilus_session, Secure, Path=/, no Domain", rec.Code, cs)
	}
	// It acts over TLS, by that name only: a plain-HTTP cookie on the same
	// host (any service there may set one) is not read by a secure request.
	tlsMe := func(name string) string {
		return call(t, srv, "GET", "/api/me", "", overTLS, func(r *http.Request) { r.AddCookie(&http.Cookie{Name: name, Value: cs[0].Value}) }).Body.String()
	}
	if !strings.Contains(tlsMe(secureCookie), "user:akhan") {
		t.Errorf("the __Host- cookie over TLS: %s", tlsMe(secureCookie))
	}
	if body := tlsMe(plainCookie); !strings.Contains(body, `"principal":"anonymous"`) {
		t.Errorf("the session id under the plain name over TLS acts: %s", body)
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
	// Anonymous may not read here (anonymous: none): the unknown cookie's
	// read is the anonymous refusal, a 401 that says to sign in.
	if rec := call(t, srv, "GET", "/api/state", "", withCookie("forgotten")); rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "sign in") {
		t.Errorf("a read with an unknown cookie: %d %s, want the anonymous 401", rec.Code, rec.Body)
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
		{"TLS", overTLS, http.StatusOK},
		{"a trusted proxy's https", func(r *http.Request) {
			r.RemoteAddr = "10.0.0.5:1"
			r.Header.Set("X-Forwarded-Proto", "https")
			r.Header.Set("Origin", "https://localhost")
		}, http.StatusOK},
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
		return func(r *http.Request) { r.RemoteAddr = addr + ":1"; overTLS(r) }
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
	rec := call(t, srv, "GET", "/signin?next=/hmi/pumps", "", fromLoopback)
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
	if rec.Code != http.StatusOK || len(cs) != 1 || cs[0].Name != plainCookie || cs[0].MaxAge >= 0 {
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
	first, _ := ss.open("akhan", "")
	for range maxUserSessions - 1 {
		ss.open("akhan", "")
	}
	other, _ := ss.open("jsmith", "")
	if _, ok := ss.lookup(first); !ok || len(ended) != 0 {
		t.Fatalf("at the limit: first live %v, ended %d", ok, len(ended))
	}
	ss.open("akhan", "")
	if _, ok := ss.lookup(first); ok {
		t.Error("past the limit the oldest session lives on")
	}
	if len(ended) != 1 || ended[0] != sessionHash(first) {
		t.Errorf("ended %v, want the oldest", ended)
	}
	if _, ok := ss.lookup(other); !ok {
		t.Error("another user's session ended")
	}
	if n := ss.endPrincipal("user:akhan"); n != maxUserSessions || len(ended) != 1+maxUserSessions {
		t.Errorf("endPrincipal: %d ended, %d told", n, len(ended))
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

// flipCluster is a cluster whose status a test changes.
type flipCluster struct {
	mu sync.Mutex
	st leader.Status
}

func (f *flipCluster) Status() leader.Status { f.mu.Lock(); defer f.mu.Unlock(); return f.st }
func (f *flipCluster) set(st leader.Status)  { f.mu.Lock(); f.st = st; f.mu.Unlock() }

var (
	leading   = leader.Status{Mode: "quorum", IsLeader: true, Pod: "a", Leader: "a", LeaderAddr: "10.0.0.7:8086"}
	standing  = leader.Status{Mode: "quorum", Pod: "a", Leader: "b", LeaderAddr: "10.0.0.8:8086"}
	electing  = leader.Status{Mode: "quorum", Pod: "a"}
	signInRow = `{"name":"akhan","password":"password-akhan"}`
)

// A clustered controller that leads signs browsers in itself: only a
// forwarding standby refers them. Its sessions keep acting while the pair
// knows no leader (an election, or its own gate closed) and can be
// signed out there; a new sign-in waits for the election.
func TestClusteredLeaderSignsIn(t *testing.T) {
	held := leader.Status{Mode: "quorum", Pod: "a", Leader: "a", LeaderAddr: "10.0.0.7:8086"}
	for name, st := range map[string]leader.Status{"no leader known": electing, "lease held, gate closed": held} {
		srv, _, _ := patServer(t)
		fc := &flipCluster{st: leading}
		srv.cluster = fc
		c := signIn(t, srv, "akhan")
		fc.set(st)
		if body := call(t, srv, "GET", "/api/me", "", withCookie(c.Value)).Body.String(); !strings.Contains(body, "user:akhan") {
			t.Errorf("%s: /api/me with the cookie: %s", name, body)
		}
		if rec := call(t, srv, "POST", "/api/signin", signInRow, fromLoopback, ownOrigin, asJSON); rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s: sign-in: %d, want 503 until there is a leader", name, rec.Code)
		}
		if rec := call(t, srv, "POST", "/api/signout", "", withCookie(c.Value), ownOrigin); rec.Code != http.StatusOK {
			t.Errorf("%s: sign-out: %d %s", name, rec.Code, rec.Body)
		}
		fc.set(leading)
		if body := call(t, srv, "GET", "/api/me", "", withCookie(c.Value)).Body.String(); !strings.Contains(body, `"principal":"anonymous"`) {
			t.Errorf("%s: the signed-out session acts: %s", name, body)
		}
	}
}

// A controller that stops leading ends the sessions it issued: when it
// leads again they do not come back (a failover signs browser users out,
// and a sign-out at the other leader meanwhile could not have revoked
// them). Its Run tick sees the change with no request; a forwarded
// request sees it too.
func TestDemotedLeaderEndsItsSessions(t *testing.T) {
	for _, how := range []string{"a request", "the tick"} {
		srv, _, _ := patServer(t)
		fc := &flipCluster{st: leading}
		srv.cluster = fc
		c := signIn(t, srv, "akhan")
		fc.set(standing)
		if how == "a request" {
			if rec := call(t, srv, "GET", "/api/state", "", withCookie(c.Value)); rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("on the standby: %d", rec.Code)
			}
		} else {
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() { srv.Run(ctx); close(done) }()
			deadline := time.Now().Add(5 * time.Second)
			for srv.sessions.endPrincipal("user:nobody"); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
				if _, ok := srv.sessions.lookup(c.Value); !ok {
					break
				}
			}
			cancel()
			<-done
		}
		fc.set(leading)
		if body := call(t, srv, "GET", "/api/me", "", withCookie(c.Value)).Body.String(); !strings.Contains(body, `"principal":"anonymous"`) {
			t.Errorf("by %s: leading again, the old session acts: %s", how, body)
		}
		found := false
		for _, e := range srv.audit.ring.Recent() {
			found = found || (e.Action == "auth.sessions-ended" && e.Detail["sessions"] == 1)
		}
		if !found {
			t.Errorf("by %s: the ended sessions are not journalled", how)
		}
	}
}

// The journal names who was authorized, resolved once at the seam: a
// session that ends while the handler runs does not turn the entry
// anonymous.
func TestEntryKeepsTheAuthorizedUser(t *testing.T) {
	srv, _, _ := patServer(t)
	c := signIn(t, srv, "akhan")
	srv.unforceAllChecked = func() { srv.sessions.endPrincipal("user:akhan") }
	rec := call(t, srv, "POST", "/api/forces/clear", "", withCookie(c.Value), ownOrigin)
	e := srv.audit.ring.Recent()[0]
	if rec.Code >= 400 || e.Action != "force.clear-all" || e.User != "user:akhan" || e.Session != sessionHash(c.Value) {
		t.Errorf("clear-all whose session ended meanwhile: %d %s, journalled %+v", rec.Code, rec.Body, e)
	}
}

// The Origin must be this controller's origin as the request reached it,
// scheme included: over TLS an http page (one an on-path attacker answers
// on the LAN, and a browser without schemeful same-site sends the Strict
// cookie from) is refused for a write, a sign-in and a sign-out. The
// scheme's default port counts as given.
func TestOwnOriginIncludesTheScheme(t *testing.T) {
	srv, _, _ := patServer(t)
	rec := call(t, srv, "POST", "/api/signin", signInRow, asJSON, overTLS)
	if rec.Code != http.StatusOK {
		t.Fatalf("sign-in over TLS: %d %s", rec.Code, rec.Body)
	}
	id := rec.Result().Cookies()[0].Value
	sc := func(r *http.Request) { r.AddCookie(&http.Cookie{Name: secureCookie, Value: id}) }
	httpPage := func(r *http.Request) { r.Header.Set("Origin", "http://localhost") }
	for _, c := range []struct {
		name, method, path, body string
		mod                      []func(*http.Request)
	}{
		{"write", "POST", "/api/tags", `{"name":"SP","value":5}`, []func(*http.Request){overTLS, httpPage, sc}},
		{"sign-in", "POST", "/api/signin", signInRow, []func(*http.Request){asJSON, overTLS, httpPage}},
		{"sign-out", "POST", "/api/signout", "", []func(*http.Request){overTLS, httpPage, sc}},
	} {
		if rec := call(t, srv, c.method, c.path, c.body, c.mod...); rec.Code != http.StatusForbidden {
			t.Errorf("%s over TLS from an http page: %d %s, want 403", c.name, rec.Code, rec.Body)
		}
	}
	if body := call(t, srv, "GET", "/api/me", "", overTLS, sc).Body.String(); !strings.Contains(body, "user:akhan") {
		t.Fatalf("the refused sign-out ended the session: %s", body)
	}
	// From its own https page, Host with or without the default port.
	for _, host := range []string{"localhost", "localhost:443"} {
		rec := call(t, srv, "POST", "/api/tags", `{"name":"SP","value":6}`, overTLS, sc, func(r *http.Request) { r.Host = host })
		if rec.Code != http.StatusNoContent {
			t.Errorf("write over TLS from https://localhost, Host %s: %d %s", host, rec.Code, rec.Body)
		}
	}
	// And over plain HTTP an https Origin is not this controller's either.
	c := signIn(t, srv, "akhan")
	if rec := call(t, srv, "POST", "/api/tags", `{"name":"SP","value":7}`, withCookie(c.Value), func(r *http.Request) { r.Header.Set("Origin", "https://localhost") }); rec.Code != http.StatusForbidden {
		t.Errorf("a plain-HTTP write from an https Origin: %d", rec.Code)
	}
}

// The page never shows the password form where sign-in would refuse the
// password: a password typed over plain HTTP is on the wire before any
// refusal. It says why instead.
func TestSignInPageHasNoFormOverPlainHTTP(t *testing.T) {
	srv, _, _ := patServer(t)
	for _, c := range []struct {
		name string
		mod  func(*http.Request)
		form bool
	}{
		{"plain HTTP from the network", func(*http.Request) {}, false},
		{"plain HTTP through a proxy on loopback", func(r *http.Request) { fromLoopback(r); r.Header.Set("X-Forwarded-For", "192.0.2.4") }, false},
		{"loopback", fromLoopback, true},
		{"TLS", overTLS, true},
	} {
		body := call(t, srv, "GET", "/signin", "", c.mod).Body.String()
		if got := strings.Contains(body, `type="password"`) || strings.Contains(body, "<form"); got != c.form {
			t.Errorf("%s: form shown %v, want %v: %s", c.name, got, c.form, body)
		}
		if !c.form && !strings.Contains(body, "needs HTTPS") {
			t.Errorf("%s: the page does not say why: %s", c.name, body)
		}
	}
	// Nor after a refused form post over plain HTTP.
	body := call(t, srv, "POST", "/api/signin", "name=akhan&password=x", asForm, ownOrigin).Body.String()
	if strings.Contains(body, `type="password"`) {
		t.Errorf("the refusal page over plain HTTP shows the form: %s", body)
	}
}

// An IPv6 client is throttled by its /64 (a host is handed a whole /64
// and could rotate through it); IPv4 by its address.
func TestSignInThrottleKeys(t *testing.T) {
	for in, want := range map[string]string{
		"192.0.2.7":            "192.0.2.7",
		"::ffff:192.0.2.7":     "192.0.2.7",
		"2001:db8:1:2:3:4:5:6": "2001:db8:1:2::/64",
		"2001:db8:1:2:ffff::1": "2001:db8:1:2::/64",
		"2001:db8:1:3::1":      "2001:db8:1:3::/64",
		"fe80::1%eth0":         "fe80::/64",
		"not-an-address":       "not-an-address",
	} {
		if got := addrKey(in); got != want {
			t.Errorf("addrKey(%q) = %q, want %q", in, got, want)
		}
	}
	srv, _, _ := patServer(t)
	from := func(addr string) func(*http.Request) {
		return func(r *http.Request) { r.RemoteAddr = "[" + addr + "]:1"; overTLS(r) }
	}
	for i := range 10 {
		addr := fmt.Sprintf("2001:db8:1:2::%x", i+1)
		if rec := call(t, srv, "POST", "/api/signin", `{"name":"x`+fmt.Sprint(i)+`","password":"guess"}`, asJSON, from(addr)); rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d", i+1, rec.Code)
		}
	}
	if rec := call(t, srv, "POST", "/api/signin", signInRow, asJSON, from("2001:db8:1:2::ff")); rec.Code != http.StatusTooManyRequests {
		t.Errorf("an 11th address in the same /64: %d, want 429", rec.Code)
	}
	if rec := call(t, srv, "POST", "/api/signin", signInRow, asJSON, from("2001:db8:1:3::1")); rec.Code != http.StatusOK {
		t.Errorf("another /64: %d", rec.Code)
	}
}

// A limiter past limiterSweep buckets drops the full ones a few at a time
// (sweepBatch per new key), never the whole map at once; past limiterMax
// it drops some anyway, so made-up keys cannot grow it without bound.
func TestLimiterSweepIsAmortised(t *testing.T) {
	l := newLimiter(2, time.Second)
	now := time.Now()
	l.now = func() time.Time { return now }
	for i := range limiterSweep {
		l.take(fmt.Sprint("k", i))
	}
	now = now.Add(time.Hour) // every bucket full again
	l.take("new")
	if got, want := len(l.b), limiterSweep-sweepBatch+1; got != want {
		t.Errorf("after one new key: %d buckets, want %d (a sweep of %d)", got, want, sweepBatch)
	}
	// Keys whose buckets never fill (each spent at once, the clock still).
	l = newLimiter(1, time.Hour)
	l.now = func() time.Time { return now }
	for i := range limiterMax + 500 {
		l.take(fmt.Sprint("k", i))
	}
	if len(l.b) > limiterMax {
		t.Errorf("%d buckets, want at most %d", len(l.b), limiterMax)
	}
}

// A session remembers the password hash its user proved: a sign-in that
// raced a password change (the change's revocation ran before the
// session existed) acts no more once the change is in the file.
func TestSessionDiesWithItsPassword(t *testing.T) {
	srv, _, path := patServer(t)
	c := signIn(t, srv, "akhan")
	// The race, made certain: the revocation never sees this session.
	srv.users.OnChange(func(string, []users.Change) {})
	if err := users.Update(path, "opsadmin", func(f *users.File) error { return f.SetPassword("akhan", "new-password") }); err != nil {
		t.Fatal(err)
	}
	srv.users.Reload()
	if body := call(t, srv, "GET", "/api/me", "", withCookie(c.Value)).Body.String(); !strings.Contains(body, `"principal":"anonymous"`) {
		t.Errorf("a session proved against the old password acts: %s", body)
	}
	// A change that is not akhan's password (jsmith's) leaves akhan's
	// new session alone.
	rec := call(t, srv, "POST", "/api/signin", `{"name":"akhan","password":"new-password"}`, fromLoopback, ownOrigin, asJSON)
	id := rec.Result().Cookies()[0].Value
	if err := users.Update(path, "opsadmin", func(f *users.File) error { return f.SetPassword("jsmith", "other-password") }); err != nil {
		t.Fatal(err)
	}
	srv.users.Reload()
	if body := call(t, srv, "GET", "/api/me", "", withCookie(id)).Body.String(); !strings.Contains(body, "user:akhan") {
		t.Errorf("another user's change ended akhan's session: %s", body)
	}
}
