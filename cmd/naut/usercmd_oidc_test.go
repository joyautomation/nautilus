package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/auth/users"
)

// oidcUsersProject is usersProject with an auth.oidc block granting
// jsmith@corp.example by name, and akhan (also a local user) by name.
func oidcUsersProject(t *testing.T) (dir, state string) {
	t.Helper()
	dir, state = t.TempDir(), t.TempDir()
	os.Chmod(state, 0o700)
	m := "name: line1\ntasks:\n  - program: main.st\n    scan: 100ms\n" +
		"auth:\n  state-dir: " + state + "\n  users:\n    akhan: [engineer]\n" +
		"  oidc:\n    issuer: https://login.example\n    client-id: naut\n    users:\n      jsmith@corp.example: engineer\n      akhan: viewer\n"
	os.WriteFile(filepath.Join(dir, "nautilus.yaml"), []byte(m), 0o644)
	os.WriteFile(filepath.Join(dir, "main.st"), []byte("PROGRAM Main\nEND_PROGRAM\n"), 0o644)
	return dir, state
}

// naut users token refuses an OIDC name (a token would outlive the
// account at the provider), however it is named; a local user of the same
// name still gets one.
func TestNautUsersTokenRefusesOIDCNames(t *testing.T) {
	cheapHashes(t)
	dir, state := oidcUsersProject(t)
	path := filepath.Join(state, users.FileName)
	for _, name := range []string{"jsmith@corp.example", "oidc:akhan", "JSmith@corp.example"} {
		code, out, errs := nautUsers(t, dir, "", "token", name)
		if code != 1 || out != "" || !strings.Contains(errs, "OIDC name") || !strings.Contains(errs, "outlive") {
			t.Errorf("token %s: %d %q %s", name, code, out, errs)
		}
	}
	// A name pinned by a sign-in, not in the manifest any more, too.
	if err := users.UpdatePins(users.PinsPath(path), "controller", func(p *users.Pins) error {
		p.Pins["left@corp.example"] = users.Pin{Issuer: "https://login.example", Subject: "s", Pinned: time.Now()}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if code, _, errs := nautUsers(t, dir, "", "token", "left@corp.example"); code != 1 || !strings.Contains(errs, "OIDC name") {
		t.Errorf("token for a pinned name: %d %s", code, errs)
	}
	if code, _, errs := nautUsers(t, dir, "hunter22\n", "add", "akhan"); code != 0 {
		t.Fatal(errs)
	}
	if code, out, errs := nautUsers(t, dir, "", "token", "akhan"); code != 0 || !users.IsToken(strings.TrimSpace(out)) {
		t.Errorf("token for the local akhan: %d %s", code, errs)
	}
}

// naut users signout records the principal's sign-out beside the users
// file: a bare name is the local user's or the OIDC name's, and one that
// is both must say which. The output says a pair's sessions live on the
// leader.
func TestNautUsersSignout(t *testing.T) {
	cheapHashes(t)
	dir, state := oidcUsersProject(t)
	path := filepath.Join(state, users.FileName)
	nautUsers(t, dir, "hunter22\n", "add", "akhan")
	for _, c := range []struct{ arg, want string }{
		{"jsmith@corp.example", "oidc:jsmith@corp.example"},
		{"user:akhan", "user:akhan"},
		{"oidc:akhan", "oidc:akhan"},
	} {
		code, _, errs := nautUsers(t, dir, "", "signout", c.arg)
		if code != 0 || !strings.Contains(errs, "signed out "+c.want) || !strings.Contains(errs, "leader") {
			t.Errorf("signout %s: %d %s", c.arg, code, errs)
		}
		s, err := users.LoadSignouts(users.SignoutsPath(path))
		if err != nil || s.Signouts[c.want].IsZero() {
			t.Errorf("signout %s: file %+v %v", c.arg, s, err)
		}
	}
	if code, _, errs := nautUsers(t, dir, "", "signout", "akhan"); code != 2 || !strings.Contains(errs, "user:akhan or oidc:akhan") {
		t.Errorf("an ambiguous name: %d %s", code, errs)
	}
}

// naut users unpin forgets a pin, and list shows the pins and the names
// not signed in yet.
func TestNautUsersUnpinAndList(t *testing.T) {
	cheapHashes(t)
	dir, state := oidcUsersProject(t)
	path := filepath.Join(state, users.FileName)
	users.UpdatePins(users.PinsPath(path), "controller", func(p *users.Pins) error {
		p.Pins["jsmith@corp.example"] = users.Pin{Issuer: "https://login.example", Subject: "sub-1", Pinned: time.Now()}
		return nil
	})
	_, list, _ := nautUsers(t, dir, "", "list")
	for _, want := range []string{"oidc:jsmith@corp.example", "sub-1", "not signed in yet: akhan"} {
		if !strings.Contains(list, want) {
			t.Errorf("list lacks %q:\n%s", want, list)
		}
	}
	if code, _, errs := nautUsers(t, dir, "", "unpin", "oidc:JSmith@corp.example"); code != 0 || !strings.Contains(errs, "unpinned oidc:jsmith@corp.example") {
		t.Fatalf("unpin: %d %s", code, errs)
	}
	p, _ := users.LoadPins(users.PinsPath(path))
	if len(p.Pins) != 0 || p.ChangedBy == "" {
		t.Errorf("after unpin: %+v", p)
	}
	if code, _, errs := nautUsers(t, dir, "", "unpin", "jsmith@corp.example"); code != 1 || !strings.Contains(errs, "not pinned") {
		t.Errorf("unpin again: %d %s", code, errs)
	}
}
