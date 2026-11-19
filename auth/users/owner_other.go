//go:build !unix

package users

import "os"

// keepOwner is a no-op where file ownership is not uid/gid.
func keepOwner(*os.File, string, string) error { return nil }

// othersMayRead is false where the mode bits do not say who may read.
func othersMayRead(os.FileMode) bool { return false }
