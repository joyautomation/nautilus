//go:build unix

package users

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The controller reads only a users file that is its own, a plain file,
// in a directory no one else may write to: one planted by another user,
// as a symbolic link, or where others could swap it, is refused.
func TestStoreRefusesWhatIsNotItsOwn(t *testing.T) {
	cheap(t)
	dir := stateDir(t)
	path := filepath.Join(dir, FileName)
	f := New()
	f.Add("akhan", "hunter22")
	if err := f.Save(path, "opsadmin"); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err != nil {
		t.Fatalf("its own file: %v", err)
	}

	me := os.Geteuid()
	geteuid = func() int { return me + 1 }
	_, err := Open(path)
	geteuid = os.Geteuid
	if err == nil || !strings.Contains(err.Error(), "not this controller's") {
		t.Errorf("a file another uid owns: %v", err)
	}

	os.Chmod(dir, 0o770)
	if _, err := Open(path); err == nil || !strings.Contains(err.Error(), "chmod go-w") {
		t.Errorf("a state directory the group may write: %v", err)
	}
	if err := f.Save(path, "opsadmin"); err != nil {
		t.Errorf("Save does not check the directory's mode, CheckMode does: %v", err)
	}
	if err := Update(path, "opsadmin", func(*File) error { return nil }); err == nil {
		t.Error("naut users changed a file in a directory others may write")
	}
	os.Chmod(dir, 0o700)

	real := filepath.Join(dir, "elsewhere.json")
	os.Rename(path, real)
	if err := os.Symlink(real, path); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Errorf("a users file that is a symbolic link: %v", err)
	}
	if err := f.Save(path, "opsadmin"); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Errorf("saving over a symbolic link: %v", err)
	}
}

// The users lock is never followed through a symbolic link, and root
// gives only a plain file with one link the directory's owner (a lock the
// directory's owner planted as a link to another file must not be handed
// to them).
func TestLockRefusesLinks(t *testing.T) {
	dir := stateDir(t)
	lock := filepath.Join(dir, LockName)
	target := filepath.Join(t.TempDir(), "elsewhere")
	os.WriteFile(target, nil, 0o600)
	if err := os.Symlink(target, lock); err != nil {
		t.Fatal(err)
	}
	if _, err := lockFile(lock); err == nil || !strings.Contains(err.Error(), "remove it") {
		t.Errorf("a symlinked lock: %v, want a refusal naming the fix", err)
	}
	os.Remove(lock)
	if err := os.Link(target, lock); err != nil {
		t.Skip("no hard links here")
	}
	geteuid = func() int { return 0 }
	defer func() { geteuid = os.Geteuid }()
	if _, err := lockFile(lock); err == nil || !strings.Contains(err.Error(), "one link") {
		t.Errorf("root took a hard-linked lock: %v", err)
	}
	os.Remove(lock)
	unlock, err := lockFile(lock)
	if err != nil {
		t.Fatalf("a plain lock as root (own directory): %v", err)
	}
	unlock()
}
