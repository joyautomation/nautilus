package main

import (
	"encoding/json"
	"html"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// `naut run` with auth.oidc against a real Keycloak (#708 PR 6b), driven
// over net/http as a browser would: tools/keycloak/run.sh starts Keycloak
// with tools/keycloak/realm.json and sets NAUTILUS_TEST_KEYCLOAK to the
// realm's issuer. Without it the test skips: `go test ./...` never needs a
// provider.
//
// It checks what a fake provider cannot: the code flow with PKCE against
// Keycloak's endpoints and keys, its groups claim mapping to roles, its
// auth_time, that prompt=login and max_age=0 make it ask for the
// credentials again although its own session is live (a later auth_time),
// and that entering another account's credentials at the confirmation is
// refused.
func TestNautRunWithKeycloak(t *testing.T) {
	issuer := os.Getenv("NAUTILUS_TEST_KEYCLOAK")
	if issuer == "" {
		t.Skip("set NAUTILUS_TEST_KEYCLOAK to a Keycloak realm's issuer (tools/keycloak/run.sh does)")
	}
	bin := buildNautCLI(t, t.TempDir(), nil)
	manifest := "name: sso\ntasks:\n  - program: main.st\n    scan: 20ms\n" +
		"tags:\n  - { name: SP, role: setpoint, type: REAL, init: 1 }\n" +
		"server:\n  addr: \"127.0.0.1:0\"\n" +
		"auth:\n  oidc:\n    issuer: " + issuer + "\n    client-id: naut\n    groups:\n      eng: engineer\n      ops: operator\n"
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "nautilus.yaml"), []byte(manifest), 0o644)
	os.WriteFile(filepath.Join(dir, "main.st"), []byte("PROGRAM Main\nVAR_EXTERNAL\n SP : REAL;\nEND_VAR\nEND_PROGRAM\n"), 0o644)
	state := t.TempDir()
	os.Chmod(state, 0o700)
	cmd := exec.Command(bin, "run")
	cmd.Dir = dir
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "NAUTILUS_") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, "STATE_DIRECTORY="+state, "XDG_STATE_HOME=", "NAUTILUS_OIDC_SECRET=naut-test-secret")
	stdout, stderr := &lockedBuffer{}, &lockedBuffer{}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() { cmd.Wait(); close(exited) }()
	t.Cleanup(func() {
		cmd.Process.Kill()
		<-exited
	})
	api := "http://" + apiAddrFromBanner(t, stdout.String, exited, stderr.String)

	// The controller's browser (its cookies) and the provider's: kept
	// apart so a confirmation can be made in a provider session of
	// another account.
	noFollow := func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	client := func() *http.Client {
		jar, _ := cookiejar.New(nil)
		return &http.Client{Jar: loopbackSecureJar{jar}, CheckRedirect: noFollow, Timeout: 15 * time.Second}
	}
	ctl, idp := client(), client()
	do := func(c *http.Client, method, u string, form url.Values) (*http.Response, string) {
		t.Helper()
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, u, body)
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		resp, err := c.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v\nstderr:\n%s", method, u, err, stderr.String())
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, string(b)
	}
	formRe := regexp.MustCompile(`<form id="kc-form-login"[^>]*action="([^"]+)"`)
	// login answers Keycloak's login page at authURL with user's
	// credentials, in the provider browser p, and returns where Keycloak
	// sends the browser back (the controller's callback). It fails when
	// Keycloak does not ask (the login form is the proof it asked).
	login := func(p *http.Client, authURL, user string) string {
		t.Helper()
		resp, page := do(p, "GET", authURL, nil)
		m := formRe.FindStringSubmatch(page)
		if resp.StatusCode != http.StatusOK || m == nil {
			t.Fatalf("Keycloak did not ask for the credentials: %d %s\n%.400s", resp.StatusCode, resp.Header.Get("Location"), page)
		}
		resp, page = do(p, "POST", html.UnescapeString(m[1]), url.Values{"username": {user}, "password": {user + "-password"}, "credentialId": {""}})
		back := resp.Header.Get("Location")
		if resp.StatusCode != http.StatusFound || !strings.HasPrefix(back, api+"/auth/oidc/callback?") {
			t.Fatalf("Keycloak's answer to %s's credentials: %d %s\n%.400s", user, resp.StatusCode, back, page)
		}
		return back
	}
	// hop follows the controller's 200 page that moves the browser on.
	hopRe := regexp.MustCompile(`<a href="([^"]+)">Continue</a>`)
	hop := func(page string) string {
		t.Helper()
		m := hopRe.FindStringSubmatch(page)
		if m == nil {
			t.Fatalf("no hop in %.400s", page)
		}
		return api + html.UnescapeString(m[1])
	}
	type meSession struct {
		ElevatedUntil time.Time `json:"elevatedUntil"`
		Method        string    `json:"method"`
	}
	var me struct {
		Principal string     `json:"principal"`
		Roles     []string   `json:"roles"`
		Session   *meSession `json:"session"`
	}
	readMe := func() {
		t.Helper()
		_, b := do(ctl, "GET", api+"/api/me", nil)
		me.Session = nil
		if err := json.Unmarshal([]byte(b), &me); err != nil {
			t.Fatalf("/api/me %s: %v", b, err)
		}
	}

	// Sign in as alice (group eng: an engineer).
	resp, _ := do(ctl, "GET", api+"/auth/oidc/start?next=/api/me", nil)
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(resp.Header.Get("Location"), issuer+"/") {
		t.Fatalf("start: %d %s\nstderr:\n%s", resp.StatusCode, resp.Header.Get("Location"), stderr.String())
	}
	resp, page := do(ctl, "GET", login(idp, resp.Header.Get("Location"), "alice"), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("callback: %d %.400s\nstderr:\n%s", resp.StatusCode, page, stderr.String())
	}
	readMe()
	if me.Principal != "oidc:alice" || strings.Join(me.Roles, ",") != "engineer" || me.Session == nil || me.Session.Method != "oidc" {
		t.Fatalf("after sign-in: %+v", me)
	}
	signedIn := me.Session.ElevatedUntil

	// Confirm at Keycloak: its session is live, yet it asks again
	// (prompt=login), and the new auth_time moves elevatedUntil on. auth_time
	// counts in seconds: wait past one.
	time.Sleep(2 * time.Second)
	resp, _ = do(ctl, "GET", api+"/auth/oidc/start?elevate=1&next=/api/me", nil)
	to := resp.Header.Get("Location")
	if resp.StatusCode != http.StatusSeeOther || !strings.Contains(to, "prompt=login") || !strings.Contains(to, "max_age=0") {
		t.Fatalf("elevate start: %d %s", resp.StatusCode, to)
	}
	resp, page = do(ctl, "GET", login(idp, to, "alice"), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("elevate callback: %d %.400s", resp.StatusCode, page)
	}
	resp, page = do(ctl, "GET", hop(page), nil)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/api/me" {
		t.Fatalf("elevate finish: %d %s %.400s", resp.StatusCode, resp.Header.Get("Location"), page)
	}
	readMe()
	if me.Principal != "oidc:alice" || me.Session == nil || !me.Session.ElevatedUntil.After(signedIn) {
		t.Errorf("after the confirmation: %+v (elevatedUntil was %v): Keycloak's auth_time did not move on", me.Session, signedIn)
	}

	// Confirm with bob's credentials (a fresh provider browser, so
	// Keycloak has no session to object with): refused, and alice's
	// session is as it was.
	before := me.Session.ElevatedUntil
	resp, _ = do(ctl, "GET", api+"/auth/oidc/start?elevate=1&next=/api/me", nil)
	resp, page = do(ctl, "GET", login(client(), resp.Header.Get("Location"), "bob"), nil)
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(page, "another account") {
		t.Errorf("confirmation as bob: %d %.400s", resp.StatusCode, page)
	}
	readMe()
	if me.Principal != "oidc:alice" || me.Session == nil || !me.Session.ElevatedUntil.Equal(before) {
		t.Errorf("after bob's confirmation: %+v", me)
	}

	// The same with one browser: bob's credentials typed into the form
	// Keycloak shows while alice's session there is live. Keycloak may
	// refuse it itself, or send the browser back with bob's code, which
	// the controller refuses; either way alice's session is as it was.
	resp, _ = do(ctl, "GET", api+"/auth/oidc/start?elevate=1&next=/api/me", nil)
	resp, page = do(idp, "GET", resp.Header.Get("Location"), nil)
	m := formRe.FindStringSubmatch(page)
	if m == nil {
		t.Fatalf("Keycloak did not ask: %d %.400s", resp.StatusCode, page)
	}
	resp, page = do(idp, "POST", html.UnescapeString(m[1]), url.Values{"username": {"bob"}, "password": {"bob-password"}, "credentialId": {""}})
	if back := resp.Header.Get("Location"); resp.StatusCode == http.StatusFound && strings.HasPrefix(back, api+"/auth/oidc/callback?") {
		t.Log("Keycloak sent bob's code back over alice's session; the controller judges it")
		resp, page = do(ctl, "GET", back, nil)
		if resp.StatusCode != http.StatusForbidden || !strings.Contains(page, "another account") {
			t.Errorf("bob's code over alice's session: %d %.400s", resp.StatusCode, page)
		}
	} else {
		t.Logf("Keycloak refused bob's credentials over alice's session itself: %d", resp.StatusCode)
	}
	readMe()
	if me.Principal != "oidc:alice" || me.Session == nil || !me.Session.ElevatedUntil.Equal(before) {
		t.Errorf("after bob's credentials in alice's browser: %+v", me)
	}
}

// loopbackSecureJar is a cookie jar that treats http to a loopback address
// as a secure context, as browsers do: Keycloak sets its login cookies
// Secure, and a browser sends them to http://127.0.0.1, where Go's
// cookiejar would not.
type loopbackSecureJar struct{ http.CookieJar }

func (j loopbackSecureJar) secure(u *url.URL) *url.URL {
	if u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost") {
		c := *u
		c.Scheme = "https"
		return &c
	}
	return u
}

func (j loopbackSecureJar) SetCookies(u *url.URL, cs []*http.Cookie) {
	j.CookieJar.SetCookies(j.secure(u), cs)
}

func (j loopbackSecureJar) Cookies(u *url.URL) []*http.Cookie {
	return j.CookieJar.Cookies(j.secure(u))
}
