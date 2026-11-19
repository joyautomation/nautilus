package users

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// cheap makes hashing fast for the tests that do not test its cost; each
// hash carries its parameters, so verification is unaffected.
func cheap(t *testing.T) {
	t.Helper()
	old := Default
	Default = Params{Memory: 64, Time: 1, Threads: 1}
	t.Cleanup(func() { Default = old })
}

// stateDir is a state directory as a controller's is: 0700.
func stateDir(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	if err := os.Chmod(d, 0o700); err != nil {
		t.Fatal(err)
	}
	return d
}

// A hash is argon2id in PHC form with the default (RFC 9106) parameters,
// verifies its password and nothing else, and is salted.
func TestHashPassword(t *testing.T) {
	h, err := HashPassword("correct horse", Default)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=65536,t=3,p=4$") {
		t.Errorf("hash %s", h)
	}
	if !verify(h, "correct horse") || verify(h, "correct horsf") || verify(h, "") {
		t.Error("verify")
	}
	h2, _ := HashPassword("correct horse", Default)
	if h == h2 {
		t.Error("two hashes of one password are equal: no salt")
	}
	for _, bad := range []string{"", "plain", "$argon2i$v=19$m=65536,t=3,p=4$c2FsdHNhbHQ$a2V5a2V5a2V5a2V5a2V5", "$argon2id$v=19$m=999999999,t=3,p=4$c2FsdHNhbHQ$a2V5a2V5a2V5a2V5a2V5a2V5"} {
		if _, err := parsePHC(bad); err == nil {
			t.Errorf("parsePHC(%q) accepted", bad)
		}
		if verify(bad, "") {
			t.Errorf("verify(%q) true", bad)
		}
	}
}

// Authenticate tells a wrong password, an unknown user and a disabled one
// apart for the journal, and accepts only the right password of an
// enabled user. User names are matched ignoring case.
func TestAuthenticate(t *testing.T) {
	cheap(t)
	f := New()
	if err := f.Add("jsmith", "hunter22"); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, pw string
		want     error
	}{
		{"jsmith", "hunter22", nil},
		{"JSmith", "hunter22", nil},
		{"jsmith", "hunter23", ErrWrongPassword},
		{"nobody", "hunter22", ErrNoUser},
	} {
		if err := f.Authenticate(c.name, c.pw); !errors.Is(err, c.want) {
			t.Errorf("%s/%s: %v, want %v", c.name, c.pw, err, c.want)
		}
	}
	f.SetDisabled("jsmith", true)
	if err := f.Authenticate("jsmith", "hunter22"); !errors.Is(err, ErrDisabled) {
		t.Errorf("disabled: %v", err)
	}
	f.SetDisabled("jsmith", false)
	if err := f.SetPassword("jsmith", "new-password"); err != nil {
		t.Fatal(err)
	}
	if f.Authenticate("jsmith", "hunter22") == nil || f.Authenticate("jsmith", "new-password") != nil {
		t.Error("passwd did not replace the password")
	}
}

func TestAddRefuses(t *testing.T) {
	cheap(t)
	f := New()
	f.Add("jsmith", "hunter22")
	for _, c := range []struct{ name, pw, want string }{
		{"jsmith", "hunter22", "exists"},
		{"JSmith", "hunter22", "lowercase"},
		{"-x", "hunter22", "starting with"},
		{"", "hunter22", "1 to 64"},
		{"akhan", "short", "at least 8"},
	} {
		if err := f.Add(c.name, c.pw); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("Add(%q): %v, want %q", c.name, err, c.want)
		}
	}
}

// A personal access token is shown once and kept as a hash; it names its
// user until revoked or the user is disabled, and nothing else passes.
func TestTokens(t *testing.T) {
	cheap(t)
	f := New()
	f.Add("akhan", "hunter22")
	f.Add("jsmith", "hunter22")
	tok, id, err := f.NewToken("akhan")
	if err != nil {
		t.Fatal(err)
	}
	if !IsToken(tok) || !strings.HasPrefix(tok, TokenPrefix+id+"_") {
		t.Fatalf("token %q id %q", tok, id)
	}
	if strings.Contains(f.Users["akhan"].Tokens[0].Hash, strings.TrimPrefix(tok, TokenPrefix+id+"_")) {
		t.Fatal("the secret is stored")
	}
	if u, ok := f.TokenUser(tok); !ok || u != "akhan" {
		t.Errorf("TokenUser = %q %v", u, ok)
	}
	for _, bad := range []string{tok + "x", tok[:len(tok)-1], TokenPrefix + id + "_", "naut_pat_zzzzzzzzzzzzzzzz_abc", "s3cret", ""} {
		if _, ok := f.TokenUser(bad); ok {
			t.Errorf("TokenUser(%q) accepted", bad)
		}
	}
	f.SetDisabled("akhan", true)
	if _, ok := f.TokenUser(tok); ok {
		t.Error("a disabled user's token is accepted")
	}
	f.SetDisabled("akhan", false)
	if err := f.Revoke("akhan", id); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.TokenUser(tok); ok {
		t.Error("a revoked token is accepted")
	}
	if err := f.Revoke("akhan", id); err == nil {
		t.Error("revoked twice")
	}
}

// Save is mode 0600 in a 0700 directory and round-trips; Load refuses a
// file it does not understand rather than guess.
func TestSaveLoad(t *testing.T) {
	cheap(t)
	path := filepath.Join(t.TempDir(), "state", FileName)
	if f, err := Load(path); err != nil || len(f.Users) != 0 {
		t.Fatalf("an absent file: %v %v", f, err)
	}
	f := New()
	f.Add("jsmith", "hunter22")
	tok, _, _ := f.NewToken("jsmith")
	if err := f.Save(path, "opsadmin"); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(path)
	dst, _ := os.Stat(filepath.Dir(path))
	if runtime.GOOS != "windows" && (st.Mode().Perm() != 0o600 || dst.Mode().Perm() != 0o700) {
		t.Errorf("modes %v %v", st.Mode(), dst.Mode())
	}
	if err := CheckMode(path); err != nil {
		t.Error(err)
	}
	g, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if g.ChangedBy != "opsadmin" || g.Authenticate("jsmith", "hunter22") != nil {
		t.Errorf("round trip: %+v", g)
	}
	if u, ok := g.TokenUser(tok); !ok || u != "jsmith" {
		t.Error("the token did not survive a save")
	}
	if g.Digest() != f.Digest() {
		t.Error("digest changed over a save")
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Errorf("left behind: %v", entries)
	}
	for body, want := range map[string]string{
		`{"version":2,"users":{}}`:                          "version 2",
		`{"version":1,"users":{},"extra":1}`:                "unknown field",
		`{"version":1,"users":{"Bad":{"password":"x"}}}`:    "lowercase",
		`{"version":1,"users":{"ok":{"password":"plain"}}}`: "not an argon2id hash",
		`{"version":1,"users":{"ok":null}}`:                 "null",
		`{"version":1,"users":{}} {}`:                       "more than one",
	} {
		os.WriteFile(path, []byte(body), 0o600)
		if _, err := Load(path); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", body, err, want)
		}
	}
	if runtime.GOOS != "windows" {
		os.Chmod(path, 0o640)
		if err := CheckMode(path); err == nil || !strings.Contains(err.Error(), "chmod 600") {
			t.Errorf("a group-readable file: %v", err)
		}
	}
}

// The digest covers the credentials and nothing else: who saved the file
// and when do not change it; a password, a disabled flag, a token do.
func TestDigest(t *testing.T) {
	cheap(t)
	dir := stateDir(t)
	path := filepath.Join(dir, FileName)
	if d, err := Digest(path); err != nil || d != [32]byte{} {
		t.Fatalf("an absent file: %x %v", d, err)
	}
	f := New()
	f.Add("jsmith", "hunter22")
	f.Save(path, "a")
	d1, _ := Digest(path)
	f.Save(path, "b")
	if d2, _ := Digest(path); d2 != d1 {
		t.Error("who saved it changed the digest")
	}
	for _, change := range []func(){
		func() { f.SetDisabled("jsmith", true) },
		func() { f.NewToken("jsmith") },
		func() { f.SetPassword("jsmith", "other-password") },
		func() { f.Add("akhan", "hunter22") },
	} {
		before := f.Digest()
		change()
		if f.Digest() == before {
			t.Error("a credential change kept the digest")
		}
	}
	os.WriteFile(path, []byte("{"), 0o600)
	if _, err := Digest(path); err == nil {
		t.Error("a malformed file has a digest")
	}
}

func TestDiff(t *testing.T) {
	cheap(t)
	a := New()
	a.Add("jsmith", "hunter22")
	a.Add("gone", "hunter22")
	_, tid, _ := a.NewToken("jsmith")
	b, _ := Load(filepath.Join(t.TempDir(), "x")) // empty
	b.Users = map[string]*User{}
	for n, u := range a.Users {
		c := *u
		c.Tokens = append([]Token(nil), u.Tokens...)
		b.Users[n] = &c
	}
	delete(b.Users, "gone")
	b.SetPassword("jsmith", "other-password")
	b.SetDisabled("jsmith", true)
	b.Revoke("jsmith", tid)
	_, nid, _ := b.NewToken("jsmith")
	b.Add("akhan", "hunter22")
	var got []string
	for _, c := range Diff(a, b) {
		got = append(got, c.User+" "+c.What+" "+c.Token)
	}
	want := []string{"akhan added ", "gone removed ", "jsmith password ", "jsmith disabled ", "jsmith token issued " + nid, "jsmith token revoked " + tid}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("Diff:\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for _, c := range Diff(a, b) {
		if strings.Contains(c.Detail+c.Token, "argon2") {
			t.Error("a hash in a change")
		}
	}
}

// The store re-reads a changed file, reports each change by whom, and
// fails closed: an unreadable or group-readable file leaves no users.
func TestStore(t *testing.T) {
	cheap(t)
	path := filepath.Join(stateDir(t), FileName)
	f := New()
	f.Add("jsmith", "hunter22")
	tok, _, _ := f.NewToken("jsmith")
	f.Save(path, "opsadmin")
	var changes []string
	var errs []error
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s.OnChange(func(by string, cs []Change) {
		for _, c := range cs {
			changes = append(changes, by+":"+c.User+" "+c.What)
		}
	})
	recovered := 0
	s.OnHealth(func(err error) {
		if err != nil {
			errs = append(errs, err)
		} else {
			recovered++
		}
	})
	if u, ok := s.TokenUser(tok); !ok || u != "jsmith" {
		t.Fatal("token at open")
	}
	s.Reload()
	if len(changes) != 0 {
		t.Errorf("an unchanged file reported %v", changes)
	}
	f.SetDisabled("jsmith", true)
	f.Save(path, "night-shift")
	s.Reload()
	if _, ok := s.TokenUser(tok); ok {
		t.Error("a disabled user's token passes after the reload")
	}
	if strings.Join(changes, ",") != "night-shift:jsmith disabled" {
		t.Errorf("changes %v", changes)
	}
	f.SetDisabled("jsmith", false)
	f.Save(path, "opsadmin")
	s.Reload()
	// A file that cannot be parsed: no users, told once however long it
	// lasts; fixed, the users are back, and nothing is told changed.
	os.WriteFile(path, []byte("{"), 0o600)
	s.Reload()
	if _, ok := s.TokenUser(tok); ok || s.Err() == nil || len(errs) != 1 {
		t.Errorf("a malformed file: token accepted %v, err %v, reported %v", ok, s.Err(), errs)
	}
	s.Reload()
	s.Reload()
	if len(errs) != 1 {
		t.Errorf("a failure that lasts is told %d times, want once", len(errs))
	}
	before := len(changes)
	f.Save(path, "opsadmin")
	s.Reload()
	if _, ok := s.TokenUser(tok); !ok || s.Err() != nil {
		t.Error("the store did not recover once the file was fixed")
	}
	if len(changes) != before {
		t.Errorf("recovering from a failure told changes %v: the credentials did not change", changes[before:])
	}
	if recovered != 1 {
		t.Errorf("the recovery is told %d times, want once", recovered)
	}
	if runtime.GOOS == "windows" {
		return // the mode bits say nothing there (CheckMode)
	}
	os.Chmod(path, 0o644)
	s.Reload()
	if _, ok := s.TokenUser(tok); ok || len(errs) != 2 {
		t.Errorf("a world-readable file: token accepted %v, reported %v", ok, errs)
	}
	os.Chmod(path, 0o600)
	s.Reload()
	if _, ok := s.TokenUser(tok); !ok {
		t.Error("the store did not recover once the mode was fixed")
	}
	os.Chmod(path, 0o640)
	if _, err := Open(path); err == nil {
		t.Error("Open accepted a group-readable file")
	}
}

// Two naut users runs never save over each other: the second waits for
// the first's lock, then changes the file the first saved.
func TestUpdateSerializesWriters(t *testing.T) {
	cheap(t)
	path := filepath.Join(stateDir(t), FileName)
	f := New()
	f.Add("a", "password-a")
	f.Add("b", "password-b")
	tok, id, _ := f.NewToken("b")
	f.Save(path, "opsadmin")

	started, release := make(chan struct{}), make(chan struct{})
	first, second := make(chan error, 1), make(chan error, 1)
	go func() {
		first <- Update(path, "one", func(f *File) error {
			close(started)
			<-release
			return f.SetPassword("a", "new-password")
		})
	}()
	<-started
	go func() {
		second <- Update(path, "two", func(f *File) error {
			if err := f.SetDisabled("b", true); err != nil {
				return err
			}
			return f.Revoke("b", id)
		})
	}()
	select {
	case err := <-second:
		t.Fatalf("the second writer finished while the first held the file: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if err := <-second; err != nil {
		t.Fatal(err)
	}
	g, _ := Load(path)
	if g.Authenticate("a", "new-password") != nil {
		t.Error("the first writer's change is lost")
	}
	if _, ok := g.TokenUser(tok); ok || !g.Users["b"].Disabled {
		t.Error("the second writer's change is lost: b is enabled or its token back")
	}
}
