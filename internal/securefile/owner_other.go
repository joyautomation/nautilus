//go:build !unix

package securefile

import (
	"errors"
	"fmt"
	"os"
	"time"
)

// KeepOwner is a no-op where file ownership is not uid/gid.
func KeepOwner(*os.File, string, string, bool) error { return nil }

// OthersMayRead is false where the mode bits do not say who may read.
func OthersMayRead(os.FileMode) bool { return false }

// OthersMayWrite is false where the mode bits do not say who may write.
func OthersMayWrite(os.FileMode) bool { return false }

// CheckOwner is a no-op where ownership is the file's ACL.
func CheckOwner(string, os.FileInfo, int, string) error { return nil }

// Lock takes the lock by creating path exclusively, waiting for another
// holder (tool) to remove it.
func Lock(path string, _ bool, wait time.Duration, tool string) (func(), error) {
	deadline := time.Now().Add(wait)
	for {
		f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			f.Close()
			return func() { os.Remove(path) }, nil
		}
		if !errors.Is(err, os.ErrExist) || time.Now().After(deadline) {
			return nil, fmt.Errorf("%s: another %s holds it (remove it if none is running): %w", path, tool, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
