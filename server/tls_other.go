//go:build !unix

package server

// checkKeyMode is a no-op where the mode bits do not say who may read
// (Windows: the file's ACL does).
func checkKeyMode(string) error { return nil }
