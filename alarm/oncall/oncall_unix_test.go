//go:build unix

package oncall

import (
	"os"
	"strings"
	"testing"
)

// The controller reads only files that are its own, plain, mode 0600, in
// a directory no one else may write: a file others may read, a symbolic
// link, or one another uid owns is refused, and the store's lookups fail
// (FailConfig upstream) rather than read it.
func TestStoreRefusesWhatIsNotItsOwn(t *testing.T) {
	dir := stateDir(t)
	path := ContactsPath(dir)
	if err := UpdateContacts(path, "x", func(c *Contacts) error {
		c.Contacts["alice"] = &Contact{SMS: "+15550101"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	os.Chmod(path, 0o644)
	if _, err := st.Address("alice", SMS); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Errorf("a contacts file others may read: %v", err)
	}
	os.Chmod(path, 0o600)
	if a, err := st.Address("alice", SMS); err != nil || a != "+15550101" {
		t.Errorf("after chmod 600: %q %v", a, err)
	}

	me := os.Geteuid()
	geteuid = func() int { return me + 1 }
	_, err = LoadContacts(path)
	geteuid = os.Geteuid
	if err == nil || !strings.Contains(err.Error(), "not this controller's") {
		t.Errorf("a file another uid owns: %v", err)
	}

	os.Chmod(dir, 0o770)
	if _, err := LoadContacts(path); err == nil || !strings.Contains(err.Error(), "chmod go-w") {
		t.Errorf("a directory the group may write: %v", err)
	}
	os.Chmod(dir, 0o700)

	real := path + ".real"
	os.Rename(path, real)
	if err := os.Symlink(real, path); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadContacts(path); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Errorf("a symlinked contacts file: %v", err)
	}
	if err := UpdateContacts(path, "x", func(*Contacts) error { return nil }); err == nil {
		t.Error("saved over a symbolic link")
	}
	sp := SecretsPath(dir)
	os.WriteFile(sp, []byte(`{"version":1,"secrets":{"sms.token":"t"}}`), 0o640)
	if _, err := LoadSecrets(sp); err == nil || !strings.Contains(err.Error(), "credentials") {
		t.Errorf("a secrets file the group may read: %v", err)
	}
}
