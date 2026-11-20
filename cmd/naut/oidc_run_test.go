package main

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/auth/oidc/oidctest"
	"github.com/joyautomation/nautilus/auth/users"
)

// `naut run` with auth.oidc (#708 PR 6a), end to end against a fake
// provider through the real auth/oidc client: the secret comes from
// NAUTILUS_OIDC_SECRET (without it the controller refuses to start,
// naming it), the redirect URI to register is said at the start, and a
// browser that signs in at the provider comes back with a session as
// oidc:<name>, the name pinned in the state directory.
func TestNautRunSignsInAtTheOIDCProvider(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the CLI")
	}
	idp := oidctest.New(t, "naut", "oidc-secret")
	bin := buildNautCLI(t, t.TempDir(), nil)
	manifest := "name: sso\ntasks:\n  - program: main.st\n    scan: 20ms\n" +
		"tags:\n  - { name: SP, role: setpoint, type: REAL, init: 1 }\n" +
		"server:\n  addr: \"127.0.0.1:0\"\n" +
		"auth:\n  oidc:\n    issuer: " + idp.URL + "\n    client-id: naut\n    groups:\n      g-ops: operator\n"
	launch := func(t *testing.T, env ...string) (state string, stdout, stderr *lockedBuffer, exited chan struct{}) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "nautilus.yaml"), []byte(manifest), 0o644)
		os.WriteFile(filepath.Join(dir, "main.st"), []byte("PROGRAM Main\nVAR_EXTERNAL\n SP : REAL;\nEND_VAR\nEND_PROGRAM\n"), 0o644)
		state = t.TempDir()
		os.Chmod(state, 0o700)
		cmd := exec.Command(bin, "run")
		cmd.Dir = dir
		for _, kv := range os.Environ() {
			if !strings.HasPrefix(kv, "NAUTILUS_") {
				cmd.Env = append(cmd.Env, kv)
			}
		}
		cmd.Env = append(cmd.Env, "STATE_DIRECTORY="+state, "XDG_STATE_HOME=")
		cmd.Env = append(cmd.Env, env...)
		stdout, stderr = &lockedBuffer{}, &lockedBuffer{}
		cmd.Stdout, cmd.Stderr = stdout, stderr
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		exited = make(chan struct{})
		go func() { cmd.Wait(); close(exited) }()
		t.Cleanup(func() {
			cmd.Process.Kill()
			<-exited
		})
		return state, stdout, stderr, exited
	}

	t.Run("no secret", func(t *testing.T) {
		_, _, stderr, exited := launch(t)
		select {
		case <-exited:
		case <-time.After(30 * time.Second):
			t.Fatal("naut run started without NAUTILUS_OIDC_SECRET")
		}
		if !strings.Contains(stderr.String(), "NAUTILUS_OIDC_SECRET") {
			t.Errorf("stderr:\n%s", stderr.String())
		}
	})

	t.Run("sign in", func(t *testing.T) {
		state, stdout, stderr, exited := launch(t, "NAUTILUS_OIDC_SECRET=oidc-secret")
		api := apiAddrFromBanner(t, stdout.String, exited, stderr.String)
		jar, _ := cookiejar.New(nil)
		c := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Timeout: 10 * time.Second}
		get := func(u string) (*http.Response, string) {
			t.Helper()
			deadline := time.Now().Add(30 * time.Second)
			for {
				resp, err := c.Get(u)
				if err == nil {
					b, _ := io.ReadAll(resp.Body)
					resp.Body.Close()
					return resp, string(b)
				}
				if time.Now().After(deadline) {
					t.Fatalf("GET %s: %v; stderr:\n%s", u, err, stderr.String())
				}
				time.Sleep(50 * time.Millisecond)
			}
		}
		resp, body := get("http://" + api + "/auth/oidc/start?next=/api/me")
		to := resp.Header.Get("Location")
		if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(to, idp.URL+"/authorize?") {
			t.Fatalf("start: %d %s %s\nstderr:\n%s", resp.StatusCode, to, body, stderr.String())
		}
		back := idp.Authorize(t, to, map[string]any{"preferred_username": "Ops@Corp.Example", "groups": []string{"g-ops"}, "sub": "acct-7"})
		if !strings.HasPrefix(back, "http://"+api+"/auth/oidc/callback?") {
			t.Fatalf("redirect URI %s", back)
		}
		resp, body = get(back)
		if resp.StatusCode != http.StatusOK || !strings.Contains(body, "url=/api/me") {
			t.Fatalf("callback: %d %s", resp.StatusCode, body)
		}
		_, me := get("http://" + api + "/api/me")
		if !strings.Contains(me, `"principal":"oidc:ops@corp.example"`) || !strings.Contains(me, `"write"`) {
			t.Errorf("/api/me = %s", me)
		}
		pins, err := users.LoadPins(filepath.Join(state, users.PinsName))
		if err != nil || !pins.Pins["ops@corp.example"].Same(idp.URL, "acct-7") {
			t.Errorf("pins %+v %v", pins, err)
		}
		if !strings.Contains(stderr.String(), "register the redirect URI") {
			t.Errorf("the redirect URI to register was not said:\n%s", stderr.String())
		}
		if strings.Contains(stderr.String()+stdout.String(), "oidc-secret") {
			t.Error("the client secret reached the output")
		}
	})
}
