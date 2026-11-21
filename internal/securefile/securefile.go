// Package securefile writes and checks the small private files a
// controller keeps in its state directory: the users file and its side
// files (#708), and the alarm contacts and channel secrets (#715). Each is
// written atomically, mode 0600, keeping the owner of the file it replaces
// when run as root; each is read only when it is a plain file nobody but
// its owner may read, in a directory nobody else may write, owned by the
// process reading it.
//
// It imports only the standard library (docs/design/context-map.md §2).
// Callers pass the effective uid (or whether it is root), so their tests
// can be another user without this package keeping a hook.
package securefile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// WriteAtomic writes b to path: a temporary file named by pattern (as
// os.CreateTemp takes it), mode 0600, owned as KeepOwner says (root: run as
// root), fsynced, renamed over the old one, the directory fsynced. The
// directory is made, 0700, when it does not exist. A path that is a
// symbolic link is refused.
func WriteAtomic(path, pattern string, b []byte, root bool) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if st, err := os.Lstat(path); err == nil && st.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is a symbolic link: it must be a plain file", path)
	}
	tmp, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // a no-op after the rename
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := KeepOwner(tmp, path, dir, root); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
	return nil
}

// CheckMode refuses a file that anyone but its owner may read or write,
// one that is a symbolic link, and one in a directory others may write to
// (they could replace it). An absent file passes the file's checks. On
// Windows, where Go reports every file 0666 and access is the ACL's, the
// mode bits say nothing and are not checked. noun names the file in a
// refusal ("the users file"); secret says what others could read in it
// ("the password hashes").
func CheckMode(path, noun, secret string) error {
	if st, err := os.Stat(filepath.Dir(path)); err == nil && OthersMayWrite(st.Mode().Perm()) {
		return fmt.Errorf("%s is mode %04o: other users can replace %s in it; chmod go-w it", filepath.Dir(path), st.Mode().Perm(), noun)
	}
	st, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if st.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is a symbolic link: %s must be a plain file", path, noun)
	}
	if m := st.Mode().Perm(); OthersMayRead(m) {
		return fmt.Errorf("%s is mode %04o: other users can read %s; chmod 600 it", path, m, secret)
	}
	return nil
}
