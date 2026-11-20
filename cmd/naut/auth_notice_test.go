package main

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/joyautomation/nautilus/internal/project"
	"github.com/joyautomation/nautilus/runtime"
	"github.com/joyautomation/nautilus/server"
)

// A controller with an auth: block says, loudly, what the block does not
// do yet; one without says nothing.
func TestAuthNotice(t *testing.T) {
	load := func(block string) *project.Project {
		p, err := project.Load(fstest.MapFS{
			"nautilus.yaml": &fstest.MapFile{Data: []byte("name: p\ntasks:\n  - program: main.st\n    scan: 100ms\n" + block)},
			"main.st":       &fstest.MapFile{Data: []byte("PROGRAM Main\nEND_PROGRAM\n")},
		}, "")
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	if msg := authNotice(load("")); msg != "" {
		t.Errorf("no auth: block: %q", msg)
	}
	msg := authNotice(load("auth:\n  token-role: operator\n"))
	if strings.Contains(msg, "NOT GATED") || strings.Contains(msg, "Sparkplug") ||
		!strings.Contains(msg, "OPC UA reads are not gated") || strings.Contains(msg, "OPC UA server and") {
		t.Errorf("auth: block: %q", msg)
	}
	// sparkplug.writable open and no auth.sparkplug: every command write
	// is now refused, and the notice says how to allow them.
	const sp = "sparkplug:\n  group-id: g\n  writable: [\"*\"]\n"
	msg = authNotice(load(sp + "auth:\n  token-role: operator\n"))
	if !strings.Contains(msg, "every Sparkplug command write is refused") || !strings.Contains(msg, "sparkplug: operator") ||
		!strings.Contains(msg, "restart naut run") || !strings.Contains(msg, "OPC UA reads are not gated") {
		t.Errorf("writable, no grant: %q", msg)
	}
	// A grant whose roles cannot write (viewer) allows no write either:
	// the same warning, naming a role without write (#1107 review).
	if msg := authNotice(load(sp + "auth:\n  sparkplug: viewer\n")); !strings.Contains(msg, "every Sparkplug command write is refused") ||
		!strings.Contains(msg, "a role without write") {
		t.Errorf("writable, viewer grant: %q", msg)
	}
	for _, block := range []string{
		sp + "auth:\n  sparkplug: operator\n",                             // granted
		"sparkplug:\n  group-id: g\n" + "auth:\n  token-role: operator\n", // nothing writable
	} {
		if msg := authNotice(load(block)); strings.Contains(msg, "Sparkplug") {
			t.Errorf("%q: %q", block, msg)
		}
	}
}

// A service token's secret comes from NAUTILUS_TOKEN_<NAME>; unset, or
// shared with NAUTILUS_TOKEN or another token, naut run refuses to start,
// naming the variables and never a value.
func TestServiceTokenSecrets(t *testing.T) {
	env := map[string]string{"NAUTILUS_TOKEN_HISTORIAN": "h-secret", "NAUTILUS_TOKEN_PIT_SCRIPT": "p-secret", "NAUTILUS_TOKEN_DUP": "admin-secret"}
	got, err := serviceTokenSecrets([]string{"historian", "pit_script"}, "admin-secret", func(k string) string { return env[k] })
	if err != nil || got["historian"] != "h-secret" || got["pit_script"] != "p-secret" || len(got) != 2 {
		t.Fatalf("secrets = %v, %v", got, err)
	}
	for _, c := range []struct {
		names []string
		want  string
	}{
		{[]string{"historian", "absent"}, "set NAUTILUS_TOKEN_ABSENT"},
		{[]string{"dup"}, "NAUTILUS_TOKEN_DUP is the same secret as NAUTILUS_TOKEN"},
	} {
		_, err := serviceTokenSecrets(c.names, "admin-secret", func(k string) string { return env[k] })
		if err == nil || !strings.Contains(err.Error(), c.want) || strings.Contains(err.Error(), "secret\"") || strings.Contains(err.Error(), "admin-secret") {
			t.Errorf("%v: %v, want %q", c.names, err, c.want)
		}
	}
	env["NAUTILUS_TOKEN_TWIN"] = "h-secret"
	if _, err := serviceTokenSecrets([]string{"historian", "twin"}, "", func(k string) string { return env[k] }); err == nil || strings.Contains(err.Error(), "h-secret") {
		t.Errorf("two tokens, one secret: %v", err)
	}
}

// naut logix serve and naut siemens serve have no browser sign-in: under
// an auth: block they say that every request, reads included, needs a
// token, and no longer that reads are open.
func TestAuthFacadeNotice(t *testing.T) {
	if strings.Contains(authFacadeNotice, "NOT GATED") || !strings.Contains(authFacadeNotice, "every request needs a token") ||
		!strings.Contains(authFacadeNotice, "reads included") || !strings.Contains(authFacadeNotice, "anonymous: viewer") {
		t.Errorf("authFacadeNotice = %q", authFacadeNotice)
	}
}

// naut run hands its OPC UA server the tag API's sign-in and journal only
// under an auth: block (#708 PR 8a): without one the server runs as
// before, offering no UserName token and journalling nothing.
func TestOPCUAIdentityOnlyUnderAuth(t *testing.T) {
	load := func(block string) *project.Project {
		p, err := project.Load(fstest.MapFS{
			"nautilus.yaml": &fstest.MapFile{Data: []byte("name: p\ntasks:\n  - program: main.st\n    scan: 100ms\n" + block)},
			"main.st":       &fstest.MapFile{Data: []byte("PROGRAM Main\nEND_PROGRAM\n")},
		}, "")
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	newServer := func(p *project.Project) *server.Server {
		rt, err := runtime.New(p.Runtime)
		if err != nil {
			t.Fatal(err)
		}
		return server.New(rt, server.Options{})
	}
	plain := load("")
	if id := opcuaIdentity(plain, newServer(plain)); id != nil {
		t.Errorf("no auth: block, an identity: %v", id)
	}
	guarded := load("auth:\n  token-role: operator\n")
	if id := opcuaIdentity(guarded, newServer(guarded)); id == nil {
		t.Error("an auth: block, no identity")
	}
}

// naut run hands its Sparkplug node the tag API's auth: block and journal
// only under an auth: block (#708 PR 8b): without one the node gates
// commands with sparkplug.writable alone and journals nothing.
func TestSparkplugIdentityOnlyUnderAuth(t *testing.T) {
	load := func(block string) *project.Project {
		p, err := project.Load(fstest.MapFS{
			"nautilus.yaml": &fstest.MapFile{Data: []byte("name: p\ntasks:\n  - program: main.st\n    scan: 100ms\n" + block)},
			"main.st":       &fstest.MapFile{Data: []byte("PROGRAM Main\nEND_PROGRAM\n")},
		}, "")
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	newServer := func(p *project.Project) *server.Server {
		rt, err := runtime.New(p.Runtime)
		if err != nil {
			t.Fatal(err)
		}
		return server.New(rt, server.Options{})
	}
	plain := load("")
	if id := sparkplugIdentity(plain, newServer(plain)); id != nil {
		t.Errorf("no auth: block, an identity: %v", id)
	}
	guarded := load("auth:\n  sparkplug: operator\n")
	if id := sparkplugIdentity(guarded, newServer(guarded)); id == nil {
		t.Error("an auth: block, no identity")
	}
}
