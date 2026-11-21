//go:build !unix

package users

import (
	"os"

	"github.com/joyautomation/nautilus/internal/securefile"
)

// geteuid has no meaning here; -1 is never root.
var geteuid = func() int { return -1 }

func keepOwner(tmp *os.File, path, dir string) error {
	return securefile.KeepOwner(tmp, path, dir, false)
}

func othersMayRead(m os.FileMode) bool  { return securefile.OthersMayRead(m) }
func othersMayWrite(m os.FileMode) bool { return securefile.OthersMayWrite(m) }

func checkOwner(path string, st os.FileInfo) error {
	return securefile.CheckOwner(path, st, 0, "naut users")
}

func lockFile(path string) (func(), error) {
	return securefile.Lock(path, false, lockWait, "naut users")
}
