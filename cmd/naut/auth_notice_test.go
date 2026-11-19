package main

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/joyautomation/nautilus/internal/project"
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
	if !strings.Contains(msg, "WARNING") || !strings.Contains(msg, "reads are NOT GATED") {
		t.Errorf("auth: block: %q", msg)
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
