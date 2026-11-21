//go:build unix

package users

import (
	"os"

	"github.com/joyautomation/nautilus/internal/securefile"
)

// The file-safety helpers live in internal/securefile, shared with the
// alarm contacts (#715); these keep the users package's names and its
// geteuid hook.

// keepOwner gives the new file at tmp the owner of the file it replaces
// when run as root (securefile.KeepOwner).
func keepOwner(tmp *os.File, path, dir string) error {
	return securefile.KeepOwner(tmp, path, dir, geteuid() == 0)
}

// geteuid is os.Geteuid, a variable so a test can be another user.
var geteuid = os.Geteuid

func othersMayRead(m os.FileMode) bool  { return securefile.OthersMayRead(m) }
func othersMayWrite(m os.FileMode) bool { return securefile.OthersMayWrite(m) }

// checkOwner refuses a file this process does not own.
func checkOwner(path string, st os.FileInfo) error {
	return securefile.CheckOwner(path, st, geteuid(), "naut users")
}

// lockFile takes an exclusive lock on path (created 0600), waiting for
// another naut users to finish.
func lockFile(path string) (func(), error) {
	return securefile.Lock(path, geteuid() == 0, lockWait, "naut users")
}
