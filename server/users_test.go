package server

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/auth"
	"github.com/joyautomation/nautilus/auth/users"
)

// patServer is an auth: block with two users in the manifest (akhan an
// engineer, jsmith an operator over area "sp" only) and a users file on
// the box holding both, plus "ghost", whom the manifest does not name.
// It returns each user's personal access token and the file's path.
func patServer(t *testing.T) (*Server, map[string]string, string) {
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
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), users.FileName)
	f := users.New()
	toks := map[string]string{}
	for _, n := range []string{"akhan", "jsmith", "ghost"} {
		if err := f.Add(n, "password-"+n); err != nil {
			t.Fatal(err)
		}
		toks[n], _, _ = f.NewToken(n)
	}
	if err := f.Save(path, "opsadmin"); err != nil {
		t.Fatal(err)
	}
	store, err := users.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return auditServer(t, Options{Auth: p, Users: store}), toks, path
}

func bearer(tok string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+tok) }
}

// A personal access token acts as its user, with the user's grants from
// the auth: block, and the journal names the user; a user the manifest
// does not name holds nothing; a token that is no one's is refused.
func TestPersonalAccessTokenIsItsUser(t *testing.T) {
	srv, toks, _ := patServer(t)
	for _, c := range []struct {
		tok, body string
		want      int
		user      string
	}{
		{toks["akhan"], `{"name":"SP","value":70}`, http.StatusNoContent, "user:akhan"},
		{toks["jsmith"], `{"name":"SP","value":71}`, http.StatusNoContent, "user:jsmith"},
		{toks["jsmith"], `{"name":"Out","value":1}`, http.StatusForbidden, "user:jsmith"},
		{toks["ghost"], `{"name":"SP","value":72}`, http.StatusForbidden, "user:ghost"},
		{toks["akhan"] + "x", `{"name":"SP","value":73}`, http.StatusUnauthorized, "anonymous"},
	} {
		rec := call(t, srv, "POST", "/api/tags", c.body, bearer(c.tok))
		es := srv.audit.ring.Recent()
		if rec.Code != c.want || len(es) == 0 || es[0].User != c.user {
			t.Errorf("%s %s: %d %s, journalled %v; want %d by %s", c.user, c.body, rec.Code, rec.Body, es[:min(1, len(es))], c.want, c.user)
		}
	}
	if body := call(t, srv, "GET", "/api/me", "", bearer(toks["akhan"])).Body.String(); !strings.Contains(body, `"principal":"user:akhan"`) || !strings.Contains(body, `"engineer"`) {
		t.Errorf("/api/me with akhan's token: %s", body)
	}
}

// A personal access token is a credential like NAUTILUS_TOKEN: it passes
// the Host rule a page could otherwise rebind past (design §2, gate 1).
func TestPersonalAccessTokenPassesTheHostRule(t *testing.T) {
	srv, toks, _ := patServer(t)
	for tok, want := range map[string]int{toks["akhan"]: http.StatusOK, users.TokenPrefix + "0123456789abcdef_guess": http.StatusForbidden} {
		r, _ := http.NewRequest("GET", "http://plc.example/api/state", nil)
		r.Host = "plc.example"
		r.Header.Set("Authorization", "Bearer "+tok)
		r.Header.Set("Origin", "http://hmi.example")
		rec := serve(srv, r)
		if rec.Code != want {
			t.Errorf("token %.20s… by an untrusted Host: %d, want %d", tok, rec.Code, want)
		}
		if got := rec.Header().Get("Access-Control-Allow-Origin"); (want == http.StatusOK) != (got == "http://hmi.example") {
			t.Errorf("token %.20s…: Access-Control-Allow-Origin %q", tok, got)
		}
	}
	// With users the only credentials, a page on another origin's
	// preflight is still answered (its request then needs a token).
	r, _ := http.NewRequest("OPTIONS", "http://plc.example/api/tags", nil)
	r.Host = "plc.example"
	r.Header.Set("Origin", "http://hmi.example")
	r.Header.Set("Access-Control-Request-Method", "POST")
	if rec := serve(srv, r); rec.Code != http.StatusNoContent || rec.Header().Get("Access-Control-Allow-Origin") != "http://hmi.example" {
		t.Errorf("a preflight by an untrusted Host: %d %v", rec.Code, rec.Header())
	}
}

// A running server re-reads the users file by itself: a user disabled on
// the box is refused within a couple of seconds, with no restart.
func TestRunningServerReloadsTheUsersFile(t *testing.T) {
	srv, toks, path := patServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { srv.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	f, _ := users.Load(path)
	f.SetDisabled("akhan", true)
	if err := f.Save(path, "opsadmin"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for strings.Contains(call(t, srv, "GET", "/api/me", "", bearer(toks["akhan"])).Body.String(), `"principal":"user:akhan"`) {
		if time.Now().After(deadline) {
			t.Fatal("a disabled user's token still names the user 5 s later")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Disabling a user on the box stops the user's tokens at the next
// reload, and each change naut users makes is journalled by the
// operating-system user who made it, with no hash or secret.
func TestUsersFileChangesAreJournalledAndApplied(t *testing.T) {
	srv, toks, path := patServer(t)
	f, err := users.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	f.SetDisabled("akhan", true)
	_, id, _ := f.NewToken("jsmith")
	if err := f.Save(path, "night-shift"); err != nil {
		t.Fatal(err)
	}
	srv.users.Reload()
	if rec := call(t, srv, "POST", "/api/tags", `{"name":"SP","value":70}`, bearer(toks["akhan"])); rec.Code != http.StatusUnauthorized {
		t.Errorf("a disabled user's token: %d", rec.Code)
	}
	var got []string
	for _, e := range srv.audit.ring.Recent() {
		if e.Action != "users.change" {
			continue
		}
		if e.User != "os:night-shift" || e.Source != "cli" || e.Result != "ok" {
			t.Errorf("entry %+v", e)
		}
		if s := fmt.Sprint(e); strings.Contains(s, "argon2") || strings.Contains(s, users.TokenPrefix) {
			t.Errorf("a secret in %+v", e)
		}
		got = append(got, e.Target+" "+e.Detail["change"].(string))
	}
	want := map[string]bool{"user:akhan disabled": true, "user:jsmith token issued": true}
	if len(got) != 2 || !want[got[0]] || !want[got[1]] {
		t.Errorf("journalled %v", got)
	}
	for _, e := range srv.audit.ring.Recent() {
		if e.Action == "users.change" && e.Detail["change"] == "token issued" && e.Detail["token"] != id {
			t.Errorf("token id %v, want %s", e.Detail["token"], id)
		}
	}
}
