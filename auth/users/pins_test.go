package users

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// pinStore is a Store on an empty users file, with what it tells
// collected.
func pinStore(t *testing.T) (*Store, string, func() []Change) {
	t.Helper()
	cheap(t)
	path := filepath.Join(stateDir(t), FileName)
	if err := New().Save(path, "opsadmin"); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var told []Change
	s.OnChange(func(by string, cs []Change) {
		mu.Lock()
		defer mu.Unlock()
		for _, c := range cs {
			c.Detail = by
			told = append(told, c)
		}
	})
	return s, path, func() []Change {
		mu.Lock()
		defer mu.Unlock()
		out := told
		told = nil
		return out
	}
}

// The first sign-in of a name pins it; a later one from another account
// gets the pin it does not match; the pin is on disk, the controller's
// own.
func TestPinFirstIsTrustOnFirstUse(t *testing.T) {
	s, path, told := pinStore(t)
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	p, fresh, err := s.PinFirst("jsmith@corp.example", "https://idp", "sub-1", now)
	if err != nil || !fresh || !p.Same("https://idp", "sub-1") {
		t.Fatalf("first: %+v %v %v", p, fresh, err)
	}
	p, fresh, err = s.PinFirst("jsmith@corp.example", "https://idp", "sub-2", now)
	if err != nil || fresh || !p.Same("https://idp", "sub-1") {
		t.Fatalf("second account: %+v %v %v, want the first account's pin", p, fresh, err)
	}
	if got, ok, err := s.Pin("jsmith@corp.example"); err != nil || !ok || !got.Same("https://idp", "sub-1") {
		t.Errorf("Pin: %+v %v %v", got, ok, err)
	}
	disk, err := LoadPins(PinsPath(path))
	if err != nil || !disk.Pins["jsmith@corp.example"].Same("https://idp", "sub-1") || disk.ChangedBy != "controller" {
		t.Errorf("on disk: %+v %v", disk, err)
	}
	if st, _ := os.Stat(PinsPath(path)); st.Mode().Perm() != 0o600 {
		t.Errorf("pins file mode %v", st.Mode().Perm())
	}
	// The controller's own pin is journalled by the sign-in, not told
	// again as a change to the file.
	s.Reload()
	if cs := told(); len(cs) != 0 {
		t.Errorf("own pin told as a change: %+v", cs)
	}
	for _, bad := range []string{"", "Upper@corp", "a b", strings.Repeat("a", 200)} {
		if _, _, err := s.PinFirst(bad, "https://idp", "sub", now); err == nil {
			t.Errorf("pinned %q", bad)
		}
	}
	if _, _, err := s.PinFirst("x", "", "sub", now); err == nil {
		t.Error("pinned with no issuer")
	}
}

// PinFirst decides a new pin on the file as it is under the lock: a pin
// written since the last reload (by hand, by another process) wins over
// the cache's "not pinned", and a change made meanwhile is told (as
// someone else's) before the new pin is made.
func TestPinFirstRereadsUnderTheLock(t *testing.T) {
	s, path, told := pinStore(t)
	now := time.Now()
	if _, _, err := s.PinFirst("other", "https://idp", "sub-o", now); err != nil {
		t.Fatal(err)
	}
	if err := UpdatePins(PinsPath(path), "opsadmin", func(p *Pins) error {
		delete(p.Pins, "other")
		p.Pins["jsmith"] = Pin{Issuer: "https://idp", Subject: "sub-1", Pinned: now}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	p, fresh, err := s.PinFirst("jsmith", "https://idp", "sub-2", now)
	if err != nil || fresh || !p.Same("https://idp", "sub-1") {
		t.Fatalf("a pin on disk, not yet reloaded: %+v %v %v, want sub-1's", p, fresh, err)
	}
	cs := told()
	got := map[string]string{}
	for _, c := range cs {
		got[c.Principal] = c.What + " by " + c.Detail
	}
	if len(cs) != 2 || got["oidc:other"] != "unpinned by opsadmin" || got["oidc:jsmith"] != "pinned by opsadmin" {
		t.Errorf("told %+v", cs)
	}
	// An unpin reaches PinFirst through the reload.
	if err := UpdatePins(PinsPath(path), "opsadmin", func(p *Pins) error { return p.Unpin("jsmith") }); err != nil {
		t.Fatal(err)
	}
	s.Reload()
	if p, fresh, err := s.PinFirst("jsmith", "https://idp", "sub-2", now); err != nil || !fresh || !p.Same("https://idp", "sub-2") {
		t.Errorf("after an unpin and a reload: %+v %v %v", p, fresh, err)
	}
	if err := UpdatePins(PinsPath(path), "opsadmin", func(p *Pins) error { return p.Unpin("nobody") }); !errors.Is(err, ErrNotPinned) {
		t.Errorf("unpin of an unpinned name: %v", err)
	}
}

// A change by someone else to either side file is told at the next
// reload: a pin removed or changed, a sign-out recorded.
func TestStoreTellsSideFileChanges(t *testing.T) {
	s, path, told := pinStore(t)
	now := time.Now()
	s.PinFirst("a@x", "https://idp", "sub-a", now)
	s.PinFirst("b@x", "https://idp", "sub-b", now)
	told()
	if err := UpdatePins(PinsPath(path), "opsadmin", func(p *Pins) error {
		delete(p.Pins, "a@x")
		p.Pins["b@x"] = Pin{Issuer: "https://idp", Subject: "sub-other", Pinned: now}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := SignOut(SignoutsPath(path), "opsadmin", "user:alice", now); err != nil {
		t.Fatal(err)
	}
	s.Reload()
	got := map[string]string{}
	for _, c := range told() {
		got[c.Principal] = c.What + " by " + c.Detail
	}
	want := map[string]string{"oidc:a@x": "unpinned by opsadmin", "oidc:b@x": "pin changed by opsadmin", "user:alice": "signed out by opsadmin"}
	if len(got) != len(want) {
		t.Fatalf("told %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: %q, want %q", k, got[k], v)
		}
	}
	if at, ok, err := s.SignedOut("user:alice"); err != nil || !ok || !at.Equal(now.UTC()) {
		t.Errorf("SignedOut: %v %v %v", at, ok, err)
	}
	// Signing out again is a new sign-out, told again.
	SignOut(SignoutsPath(path), "opsadmin", "user:alice", now.Add(time.Minute))
	s.Reload()
	if cs := told(); len(cs) != 1 || cs[0].What != "signed out" {
		t.Errorf("second sign-out told %+v", cs)
	}
}

// A side file that cannot be read, cannot be parsed or that others may
// read leaves the store answering nothing: no users, no pin, no sign-out
// known (so every session is refused), until it is fixed.
func TestSideFilesFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name  string
		file  func(string) string
		write func(string) error
	}{
		{"pins unparseable", PinsPath, func(p string) error { return os.WriteFile(p, []byte("{"), 0o600) }},
		{"pins with an unknown field", PinsPath, func(p string) error {
			return os.WriteFile(p, []byte(`{"version":1,"pins":{},"extra":1}`), 0o600)
		}},
		{"pins others may read", PinsPath, func(p string) error {
			return os.WriteFile(p, []byte(`{"version":1,"pins":{}}`), 0o644)
		}},
		{"signouts unparseable", SignoutsPath, func(p string) error { return os.WriteFile(p, []byte("[]"), 0o600) }},
		{"signouts naming no principal", SignoutsPath, func(p string) error {
			return os.WriteFile(p, []byte(`{"version":1,"signouts":{"alice":"2026-01-01T00:00:00Z"}}`), 0o600)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, path, _ := pinStore(t)
			s.PinFirst("a@x", "https://idp", "sub-a", time.Now())
			if err := tc.write(tc.file(path)); err != nil {
				t.Fatal(err)
			}
			os.Chmod(tc.file(path), 0o644&os.FileMode(0o777)) // keep what write set
			s.Reload()
			if s.Err() == nil {
				t.Fatal("no error")
			}
			if _, _, err := s.Pin("a@x"); !errors.Is(err, ErrUnavailable) {
				t.Errorf("Pin: %v", err)
			}
			if _, _, err := s.SignedOut("user:a"); !errors.Is(err, ErrUnavailable) {
				t.Errorf("SignedOut: %v", err)
			}
			if _, _, err := s.PinFirst("b@x", "https://idp", "sub-b", time.Now()); err == nil {
				t.Error("PinFirst pinned with a side file unreadable")
			}
			if _, err := Open(path); err == nil {
				t.Error("Open took it")
			}
		})
	}
}

// What changed in the side files while the controller was stopped is told
// at its start, marked so.
func TestCatchUpTellsSideFilesWhileStopped(t *testing.T) {
	s, path, told := pinStore(t)
	s.PinFirst("a@x", "https://idp", "sub-a", time.Now())
	s.CatchUp()
	told()
	if err := UpdatePins(PinsPath(path), "opsadmin", func(p *Pins) error { return p.Unpin("a@x") }); err != nil {
		t.Fatal(err)
	}
	SignOut(SignoutsPath(path), "opsadmin", "oidc:b@x", time.Now())
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var got []Change
	s2.OnChange(func(_ string, cs []Change) { got = append(got, cs...) })
	s2.CatchUp()
	whats := map[string]bool{}
	for _, c := range got {
		if !c.Stopped {
			t.Errorf("%+v not marked stopped", c)
		}
		whats[c.Principal+" "+c.What] = true
	}
	if !whats["oidc:a@x unpinned"] || !whats["oidc:b@x signed out"] || len(got) != 2 {
		t.Errorf("caught up %+v", got)
	}
	// And told once: a second start finds nothing new.
	got = nil
	s3, _ := Open(path)
	s3.OnChange(func(_ string, cs []Change) { got = append(got, cs...) })
	s3.CatchUp()
	if len(got) != 0 {
		t.Errorf("told again: %+v", got)
	}
}

// A sign-out older than the longest session is dropped at the next one.
func TestSignOutPrunes(t *testing.T) {
	path := filepath.Join(stateDir(t), SignoutsName)
	now := time.Now()
	SignOut(path, "x", "user:old", now.Add(-9*24*time.Hour))
	SignOut(path, "x", "user:new", now)
	s, err := LoadSignouts(path)
	if err != nil || len(s.Signouts) != 1 || s.Signouts["user:new"].IsZero() {
		t.Errorf("%+v %v", s, err)
	}
	if err := SignOut(path, "x", "user:Upper", now); err == nil {
		t.Error("a principal not lowercase was taken")
	}
}

// A lock the controller cannot open (left by root by an earlier release)
// refuses a new pin with the fix; a name pinned already needs no lock.
func TestPinFirstLockUnopenable(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("file modes")
	}
	s, path, _ := pinStore(t)
	now := time.Now()
	if _, _, err := s.PinFirst("known", "https://idp", "sub-k", now); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(filepath.Dir(path), LockName)
	if err := os.Chmod(lock, 0); err != nil {
		t.Fatal(err)
	}
	if p, _, err := s.PinFirst("known", "https://idp", "sub-k", now); err != nil || !p.Same("https://idp", "sub-k") {
		t.Errorf("a pinned name needed the lock: %v", err)
	}
	if _, _, err := s.PinFirst("new", "https://idp", "sub-n", now); err == nil || !strings.Contains(err.Error(), "chown") {
		t.Errorf("err = %v, want the fix", err)
	}
}

// A pins file whose issuer or subject holds a control character (what
// naut users list would print to a terminal) is refused, and so is pinning
// one.
func TestPinValuesArePrintable(t *testing.T) {
	path := filepath.Join(stateDir(t), PinsName)
	os.WriteFile(path, []byte(`{"version":1,"pins":{"a":{"issuer":"https://idp","subject":"s\u001b[2J","pinned":"2026-01-01T00:00:00Z"}}}`), 0o600)
	if _, err := LoadPins(path); err == nil {
		t.Error("a subject with an escape loaded")
	}
	s, _, _ := pinStore(t)
	if _, _, err := s.PinFirst("a", "https://idp", "s\x1b", time.Now()); err == nil {
		t.Error("pinned a subject with an escape")
	}
}
