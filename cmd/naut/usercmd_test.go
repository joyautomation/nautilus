package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/joyautomation/nautilus/auth/users"
	"github.com/joyautomation/nautilus/internal/project"
)

func cheapHashes(t *testing.T) {
	t.Helper()
	old := users.Default
	users.Default = users.Params{Memory: 64, Time: 1, Threads: 1}
	t.Cleanup(func() { users.Default = old })
}

// The users file lives in the state directory: auth.state-dir, else
// $STATE_DIRECTORY, else (a developer's checkout, outside systemd only)
// $XDG_STATE_HOME/nautilus/<name>; never inside the project. Under
// systemd with no state directory, and for a built controller with none,
// there is no guess.
func TestUsersFile(t *testing.T) {
	proj := t.TempDir()
	state := t.TempDir()
	env := func(kv ...string) func(string) string {
		m := map[string]string{}
		for i := 0; i < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return func(k string) string { return m[k] }
	}
	for _, c := range []struct {
		name, stateDir string
		dev            bool
		getenv         func(string) string
		want, err      string
	}{
		{"auth.state-dir", state, true, env("STATE_DIRECTORY", "/elsewhere"), filepath.Join(state, users.FileName), ""},
		{"STATE_DIRECTORY", "", true, env("STATE_DIRECTORY", state+":/second"), filepath.Join(state, users.FileName), ""},
		{"XDG fallback", "", true, env("XDG_STATE_HOME", state), filepath.Join(state, "nautilus", "line1", users.FileName), ""},
		{"systemd without one", "", true, env("INVOCATION_ID", "abc", "XDG_STATE_HOME", state), "", "no state directory"},
		{"built without one", "", false, env("XDG_STATE_HOME", state), "", "no state directory"},
		{"inside the project", filepath.Join(proj, "state"), true, env(), "", "inside the project"},
		{"the project itself", "", true, env("STATE_DIRECTORY", proj), "", "inside the project"},
	} {
		got, err := usersFile(c.stateDir, "line1", proj, c.dev, c.getenv)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%s: %q %v, want an error with %q", c.name, got, err, c.err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%s: %q %v, want %q", c.name, got, err, c.want)
		}
	}
}

// usersProject is a checkout whose manifest grants akhan and jsmith
// roles, with its state directory outside it.
func usersProject(t *testing.T) (dir, state string) {
	t.Helper()
	dir, state = t.TempDir(), t.TempDir()
	os.Chmod(state, 0o700)
	m := "name: line1\ntasks:\n  - program: main.st\n    scan: 100ms\n" +
		"auth:\n  state-dir: " + state + "\n  users:\n    akhan: [engineer]\n    jsmith: [operator]\n"
	os.WriteFile(filepath.Join(dir, "nautilus.yaml"), []byte(m), 0o644)
	os.WriteFile(filepath.Join(dir, "main.st"), []byte("PROGRAM Main\nEND_PROGRAM\n"), 0o644)
	return dir, state
}

func nautUsers(t *testing.T, dir, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := usersMain(append([]string{"--project", dir}, args...), func(d string) (*project.Manifest, string, error) {
		m, err := project.ReadManifest(os.DirFS(d), "")
		return m, d, err
	}, true, func(string) string { return "" }, strings.NewReader(stdin), &out, &errb)
	return code, out.String(), errb.String()
}

// naut users manages the file auth.state-dir names: a user added from a
// script's stdin can sign in, a token is printed once and works, list
// says what the manifest and the box disagree on, and the file stays
// mode 0600 outside the project.
func TestNautUsers(t *testing.T) {
	cheapHashes(t)
	dir, state := usersProject(t)
	path := filepath.Join(state, users.FileName)
	if code, _, errs := nautUsers(t, dir, "hunter22\n", "add", "akhan"); code != 0 || !strings.Contains(errs, "add akhan") {
		t.Fatalf("add: %d %s", code, errs)
	}
	if code, _, errs := nautUsers(t, dir, "hunter22\n", "add", "ghost"); code != 0 || !strings.Contains(errs, "not in nautilus.yaml's auth.users") {
		t.Errorf("add a user the manifest does not name: %d %s", code, errs)
	}
	if code, _, errs := nautUsers(t, dir, "short\n", "add", "jsmith"); code != 1 || !strings.Contains(errs, "at least 8") {
		t.Errorf("a short password: %d %s", code, errs)
	}
	code, tok, errs := nautUsers(t, dir, "", "token", "akhan")
	tok = strings.TrimSpace(tok)
	if code != 0 || !users.IsToken(tok) || strings.Contains(errs, tok) {
		t.Fatalf("token: %d %q %s", code, tok, errs)
	}
	f, err := users.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if f.Authenticate("akhan", "hunter22") != nil {
		t.Error("akhan cannot authenticate")
	}
	if u, ok := f.TokenUser(tok); !ok || u != "akhan" {
		t.Error("the printed token is not akhan's")
	}
	if st, _ := os.Stat(path); runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", st.Mode())
	}
	if f.ChangedBy == "" {
		t.Error("who changed it is not recorded")
	}
	_, list, _ := nautUsers(t, dir, "", "list")
	for _, want := range []string{path, "akhan", "enabled", f.Users["akhan"].Tokens[0].ID, "ghost", "not in auth.users", "no password here: jsmith"} {
		if !strings.Contains(list, want) {
			t.Errorf("list lacks %q:\n%s", want, list)
		}
	}
	id := f.Users["akhan"].Tokens[0].ID
	if code, _, _ := nautUsers(t, dir, "", "revoke", "akhan", id); code != 0 {
		t.Error("revoke")
	}
	if code, _, _ := nautUsers(t, dir, "", "disable", "akhan"); code != 0 {
		t.Error("disable")
	}
	f, _ = users.Load(path)
	if _, ok := f.TokenUser(tok); ok || !f.Users["akhan"].Disabled {
		t.Error("revoke and disable did not stick")
	}
	if code, _, _ := nautUsers(t, dir, "", "add"); code != 2 {
		t.Error("add with no name")
	}
	if code, _, _ := nautUsers(t, dir, "", "frobnicate", "x"); code != 2 {
		t.Error("an unknown subcommand")
	}
	if runtime.GOOS != "windows" {
		os.Chmod(path, 0o644)
		if code, _, errs := nautUsers(t, dir, "", "list"); code != 1 || !strings.Contains(errs, "chmod 600") {
			t.Errorf("a world-readable file: %d %s", code, errs)
		}
	}
}

// naut run with an auth: block opens the users file and refuses to start
// on one others may read, or with no state directory under systemd.
func TestOpenUsers(t *testing.T) {
	cheapHashes(t)
	state := t.TempDir()
	os.Chmod(state, 0o700)
	load := func(block string) *project.Project {
		p, err := project.Load(fstest.MapFS{
			"nautilus.yaml": &fstest.MapFile{Data: []byte("name: line1\ntasks:\n  - program: main.st\n    scan: 100ms\nauth:\n  users:\n    akhan: [engineer]\n" + block)},
			"main.st":       &fstest.MapFile{Data: []byte("PROGRAM Main\nEND_PROGRAM\n")},
		}, "")
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	f := users.New()
	f.Add("akhan", "hunter22")
	tok, _, _ := f.NewToken("akhan")
	f.Save(filepath.Join(state, users.FileName), "opsadmin")
	s, err := openUsers(load("  state-dir: "+state+"\n"), "", func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if u, ok := s.TokenUser(tok); !ok || u != "akhan" {
		t.Error("the store does not hold akhan's token")
	}
	if _, err := openUsers(load(""), "", func(k string) string { return map[string]string{"INVOCATION_ID": "x"}[k] }); err == nil || !strings.Contains(err.Error(), "no state directory") {
		t.Errorf("under systemd without a state directory: %v", err)
	}
	if runtime.GOOS != "windows" {
		os.Chmod(filepath.Join(state, users.FileName), 0o640)
		if _, err := openUsers(load("  state-dir: "+state+"\n"), "", func(string) string { return "" }); err == nil || !strings.Contains(err.Error(), "chmod 600") {
			t.Errorf("a group-readable file: %v", err)
		}
	}
	// A block that grants no user a role needs no users file, so a box
	// with no state directory (cron, a container) starts as before.
	noUsers, err := project.Load(fstest.MapFS{
		"nautilus.yaml": &fstest.MapFile{Data: []byte("name: line1\ntasks:\n  - program: main.st\n    scan: 100ms\nauth:\n  token-role: operator\n")},
		"main.st":       &fstest.MapFile{Data: []byte("PROGRAM Main\nEND_PROGRAM\n")},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if s, err := openUsers(noUsers, "", func(k string) string { return map[string]string{"INVOCATION_ID": "x"}[k] }); s != nil || err != nil {
		t.Errorf("no users in the block: %v %v, want neither a store nor an error", s, err)
	}
}

// naut-lead's repro on #1041: passwd a waits at its prompt while disable b
// and revoke b run; when passwd finishes, b stays disabled and its token
// stays revoked (the password is read before the file, and the change is
// made under the lock).
func TestNautUsersPromptDoesNotLoseOtherChanges(t *testing.T) {
	cheapHashes(t)
	dir, state := usersProject(t)
	for _, n := range []string{"akhan", "jsmith"} {
		if code, _, errs := nautUsers(t, dir, "hunter22\n", "add", n); code != 0 {
			t.Fatalf("add %s: %s", n, errs)
		}
	}
	_, tok, _ := nautUsers(t, dir, "", "token", "jsmith")
	tok = strings.TrimSpace(tok)
	path := filepath.Join(state, users.FileName)
	f, _ := users.Load(path)
	id := f.Users["jsmith"].Tokens[0].ID

	pr, pw := io.Pipe()
	done := make(chan int, 1)
	go func() {
		var out, errb bytes.Buffer
		done <- usersMain([]string{"--project", dir, "passwd", "akhan"}, func(d string) (*project.Manifest, string, error) {
			m, err := project.ReadManifest(os.DirFS(d), "")
			return m, d, err
		}, true, func(string) string { return "" }, pr, &out, &errb)
	}()
	time.Sleep(100 * time.Millisecond) // passwd is at its prompt
	if code, _, errs := nautUsers(t, dir, "", "disable", "jsmith"); code != 0 {
		t.Fatalf("disable while passwd waits: %s", errs)
	}
	if code, _, errs := nautUsers(t, dir, "", "revoke", "jsmith", id); code != 0 {
		t.Fatalf("revoke while passwd waits: %s", errs)
	}
	io.WriteString(pw, "new-password\n")
	pw.Close()
	if code := <-done; code != 0 {
		t.Fatal("passwd failed")
	}
	f, _ = users.Load(path)
	if f.Authenticate("akhan", "new-password") != nil {
		t.Error("passwd did not change akhan's password")
	}
	if _, ok := f.TokenUser(tok); ok || !f.Users["jsmith"].Disabled {
		t.Error("passwd saved a copy read before its prompt: jsmith is enabled again or the revoked token is back")
	}
}

// --file is held to the same rule as the state directory: never inside
// the project.
func TestNautUsersFileOutsideTheProject(t *testing.T) {
	cheapHashes(t)
	dir, _ := usersProject(t)
	inside := filepath.Join(dir, "secrets", users.FileName)
	if code, _, errs := nautUsers(t, dir, "hunter22\n", "--file", inside, "add", "akhan"); code != 1 || !strings.Contains(errs, "inside the project") {
		t.Errorf("--file inside the project: %d %s", code, errs)
	}
	if _, err := os.Stat(inside); err == nil {
		t.Error("a users file was written inside the project")
	}
	outside := filepath.Join(t.TempDir(), "state", users.FileName)
	if code, _, errs := nautUsers(t, dir, "hunter22\n", "--file", outside, "add", "akhan"); code != 0 {
		t.Errorf("--file outside the project: %d %s", code, errs)
	}
}

// SUDO_USER names who ran naut users only when it runs as root, where
// sudo set it; anyone else could put any name in it.
func TestOSUserBelievesSudoOnlyAsRoot(t *testing.T) {
	env := func(k string) string { return map[string]string{"SUDO_USER": "mallory"}[k] }
	if got := osUser(env, 0); got != "mallory" {
		t.Errorf("as root under sudo: %q", got)
	}
	if got := osUser(env, 1000); got == "mallory" {
		t.Errorf("as uid 1000 with SUDO_USER set by hand: %q", got)
	}
}
