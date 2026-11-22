//go:build !unix

package users

import (
	"os"

	"github.com/joyautomation/nautilus/internal/securefile"
)

// geteuid has no meaning here; -1 is never root.
var geteuid = func() int { return -1 }

func checkOwner(path string, st os.FileInfo) error {
	return securefile.CheckOwner(path, st, 0, "naut users")
}

func lockFile(path string) (func(), error) {
	return securefile.Lock(path, false, lockWait, "naut users")
}
