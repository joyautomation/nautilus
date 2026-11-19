//go:build !unix

package users

import (
	"errors"
	"fmt"
	"os"
	"time"
)

// keepOwner is a no-op where file ownership is not uid/gid.
func keepOwner(*os.File, string, string) error { return nil }

// othersMayRead is false where the mode bits do not say who may read.
func othersMayRead(os.FileMode) bool { return false }

// othersMayWrite is false where the mode bits do not say who may write.
func othersMayWrite(os.FileMode) bool { return false }

// checkOwner is a no-op where ownership is the file's ACL.
func checkOwner(string, os.FileInfo) error { return nil }

// lockFile takes the lock by creating path exclusively, waiting for
// another naut users to remove it.
func lockFile(path string) (func(), error) {
	deadline := time.Now().Add(lockWait)
	for {
		f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			f.Close()
			return func() { os.Remove(path) }, nil
		}
		if !errors.Is(err, os.ErrExist) || time.Now().After(deadline) {
			return nil, fmt.Errorf("%s: another naut users holds it (remove it if none is running): %w", path, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
