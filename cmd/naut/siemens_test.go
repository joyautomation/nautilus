package main

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/siemens/facade"
	"github.com/joyautomation/nautilus/siemens/webapi"
	"github.com/joyautomation/nautilus/siemens/webapi/webapitest"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

var pw = env(map[string]string{"NAUTILUS_SIEMENS_PASSWORD": "pw"})

func TestSiemensServeRefusesWhatWouldBeUnsafe(t *testing.T) {
	none := t.TempDir()
	for _, c := range []struct {
		name, host, user, pin string
		getenv                func(string) string
		want                  string
	}{
		{"no host", "", "naut", "ab", pw, "--host is required"},
		{"no user", "192.0.2.10", "", "ab", pw, "--user is required"},
		{"no password", "192.0.2.10", "naut", "ab", env(nil), "NAUTILUS_SIEMENS_PASSWORD"},
		{"no pin", "192.0.2.10", "naut", "", pw, "naut siemens cert --host 192.0.2.10"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := resolveSiemensServe(c.host, c.user, c.pin, none, nil, time.Second, "127.0.0.1:0", c.getenv)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want …%s…", err, c.want)
			}
		})
	}
}

func TestSiemensServeTakesDefaultsFromTheProject(t *testing.T) {
	dir := t.TempDir()
	m := `name: s7
tasks:
  - program: main.st
    scan: 100ms
target:
  siemens:
    station:
      cpu: {name: PLC_1, order: 6ES7 510-1DK03-0AB0, firmware: V3.1, address: 192.0.2.10, subnet-mask: 255.255.255.0, profinet-name: plc-1}
    online: {user: naut, cert-sha256: "` + strings.Repeat("ab", 32) + `"}
`
	if err := os.WriteFile(filepath.Join(dir, "nautilus.yaml"), []byte(m), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := resolveSiemensServe("", "", "", dir, nil, time.Second, "127.0.0.1:0", pw)
	if err != nil {
		t.Fatal(err)
	}
	if c.host != "192.0.2.10" || c.user != "naut" || c.pin != strings.Repeat("ab", 32) || c.password != "pw" {
		t.Fatalf("%+v", c)
	}
	if len(c.roots) != 1 || c.roots[0] != (facade.Root{DB: "Tags"}) {
		t.Fatalf("default roots: %+v", c.roots)
	}
	// Flags win over the project.
	c, err = resolveSiemensServe("192.0.2.99", "other", "cd", dir, []facade.Root{{DB: "Main_DB", Prefix: "Main"}}, time.Second, "127.0.0.1:0", pw)
	if err != nil || c.host != "192.0.2.99" || c.user != "other" || c.pin != "cd" || c.roots[0].Prefix != "Main" {
		t.Fatalf("%+v %v", c, err)
	}
}

func TestParseRoot(t *testing.T) {
	for in, want := range map[string]facade.Root{
		"Tags":             {DB: "Tags"},
		"Main_DB=Main":     {DB: "Main_DB", Prefix: "Main"},
		" Main_DB = Main ": {DB: "Main_DB", Prefix: "Main"},
	} {
		if got, err := parseRoot(in); err != nil || got != want {
			t.Errorf("parseRoot(%q) = %+v %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "=Main", `"Tags"`, "Main_DB.x=Main", "Main_DB=Ma in"} {
		if _, err := parseRoot(bad); err == nil {
			t.Errorf("parseRoot(%q): want an error", bad)
		}
	}
}

func TestSiemensServeListensOnLoopbackUnlessThereIsAToken(t *testing.T) {
	fs, _ := newSiemensServeFlags()
	if def := fs.Lookup("listen").DefValue; !isLoopback(def) {
		t.Fatalf("the default --listen %q is not loopback", def)
	}
	none := t.TempDir()
	for _, l := range []string{":8080", "0.0.0.0:8080", "[::]:8080", "192.0.2.5:8080"} {
		if _, err := resolveSiemensServe("192.0.2.10", "naut", "ab", none, nil, time.Second, l, pw); err == nil || !strings.Contains(err.Error(), "NAUTILUS_TOKEN") {
			t.Errorf("--listen %s without a token: %v", l, err)
		}
		withToken := env(map[string]string{"NAUTILUS_SIEMENS_PASSWORD": "pw", "NAUTILUS_TOKEN": "t"})
		if _, err := resolveSiemensServe("192.0.2.10", "naut", "ab", none, nil, time.Second, l, withToken); err != nil {
			t.Errorf("--listen %s with a token: %v", l, err)
		}
	}
	for _, l := range []string{"127.0.0.1:8080", "localhost:8080", "[::1]:8080"} {
		if !isLoopback(l) {
			t.Errorf("%s is loopback", l)
		}
	}
}

func TestSiemensFacadeOptionsCarryTheToken(t *testing.T) {
	o := siemensFacadeOptions(siemensServeConfig{token: "secret-token", host: "h"}, nil)
	if o.Server.AuthToken != "secret-token" {
		t.Fatalf("the API's bearer token was dropped: %q", o.Server.AuthToken)
	}
}

// TestSiemensServeLogsOut: a stopped serve, or one that fails after its
// first browse, leaves no Web API session on the CPU.
func TestSiemensServeLogsOut(t *testing.T) {
	s := webapitest.New()
	s.Users["naut"] = webapitest.User{Password: "pw", Permissions: []string{"read_value"}}
	s.Vars[`"Tags"`] = &webapitest.Var{Datatype: "struct"}
	s.Vars[`"Tags".Level`] = &webapitest.Var{Datatype: "real", Value: 1.0}
	url, pin := s.Start(t)
	newClient := func() *webapi.Client {
		c, err := webapi.New(url, webapi.Options{User: "naut", Password: "pw", CertSHA256: pin})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	cfg := siemensServeConfig{host: url, listen: "127.0.0.1:0", poll: 10 * time.Millisecond, roots: []facade.Root{{DB: "Tags"}}}

	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan string, 1)
	done := make(chan int, 1)
	go func() { done <- serveSiemens(ctx, cfg, newClient(), io.Discard, io.Discard, ready) }()
	addr := <-ready
	res, err := http.Get("http://" + addr + "/api/state")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if s.Sessions() != 1 {
		t.Fatalf("sessions while serving = %d", s.Sessions())
	}
	cancel()
	if code := <-done; code != 0 {
		t.Fatalf("exit %d", code)
	}
	if n := s.Sessions(); n != 0 {
		t.Fatalf("sessions after stop = %d, want 0 (logged out)", n)
	}

	bad := cfg
	bad.roots = []facade.Root{{DB: "Nope"}}
	if code := serveSiemens(context.Background(), bad, newClient(), io.Discard, io.Discard, nil); code != 1 {
		t.Fatalf("a failed browse: exit %d", code)
	}
	if n := s.Sessions(); n != 0 {
		t.Fatalf("sessions after a failed start = %d, want 0", n)
	}
}

// TestSiemensServeEndsOpenStreamsOnStop: VS Code holds /api/stream open;
// stopping serve must end it at once (not after Shutdown's 5 s timeout)
// and still log the Web API session out.
func TestSiemensServeEndsOpenStreamsOnStop(t *testing.T) {
	s := webapitest.New()
	s.Users["naut"] = webapitest.User{Password: "pw", Permissions: []string{"read_value"}}
	s.Vars[`"Tags"`] = &webapitest.Var{Datatype: "struct"}
	s.Vars[`"Tags".Level`] = &webapitest.Var{Datatype: "real", Value: 1.0}
	url, pin := s.Start(t)
	c, err := webapi.New(url, webapi.Options{User: "naut", Password: "pw", CertSHA256: pin})
	if err != nil {
		t.Fatal(err)
	}
	cfg := siemensServeConfig{host: url, listen: "127.0.0.1:0", poll: 10 * time.Millisecond, roots: []facade.Root{{DB: "Tags"}}}
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan string, 1)
	done := make(chan int, 1)
	go func() { done <- serveSiemens(ctx, cfg, c, io.Discard, io.Discard, ready) }()
	addr := <-ready

	res, err := http.Get("http://" + addr + "/api/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	buf := make([]byte, 64)
	if _, err := res.Body.Read(buf); err != nil {
		t.Fatalf("the stream sent nothing: %v", err)
	}

	start := time.Now()
	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("exit %d", code)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("serve waited on the open stream instead of ending it")
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("stopping took %s with a stream open", took)
	}
	if n := s.Sessions(); n != 0 {
		t.Fatalf("sessions after stop = %d, want 0", n)
	}
}

// #708: the project's auth: block reaches the facade's server, service
// tokens and their secrets included, as under naut run; a service token
// without NAUTILUS_TOKEN_<NAME> refuses to serve.
func TestSiemensServeEnforcesTheAuthBlock(t *testing.T) {
	dir := t.TempDir()
	m := "name: s7\ntasks:\n  - program: main.st\n    scan: 100ms\nauth:\n  token-role: viewer\n  service-tokens:\n    script: operator\n"
	if err := os.WriteFile(filepath.Join(dir, "nautilus.yaml"), []byte(m), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveSiemensServe("192.0.2.10", "naut", "ab", dir, nil, time.Second, "127.0.0.1:0", pw); err == nil || !strings.Contains(err.Error(), "NAUTILUS_TOKEN_SCRIPT") {
		t.Fatalf("a service token without a secret: %v", err)
	}
	c, err := resolveSiemensServe("192.0.2.10", "naut", "ab", dir, nil, time.Second, "127.0.0.1:0",
		env(map[string]string{"NAUTILUS_SIEMENS_PASSWORD": "pw", "NAUTILUS_TOKEN": "admin", "NAUTILUS_TOKEN_SCRIPT": "s-secret"}))
	if err != nil {
		t.Fatal(err)
	}
	o := siemensFacadeOptions(c, nil)
	if o.Server.Auth == nil || o.Server.ServiceTokens["script"] != "s-secret" || o.Server.AuthToken != "admin" {
		t.Fatalf("the facade's server options: auth %v, service tokens %v", o.Server.Auth != nil, len(o.Server.ServiceTokens))
	}
}
