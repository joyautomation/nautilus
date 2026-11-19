//go:build !unix

package node

// syncDir does nothing where a directory can't be opened for sync
// (Windows): the rename is as durable as the platform makes it. Only the
// witness runs there; controllers are Linux-only.
func syncDir(string) error { return nil }

// lock does nothing off unix: the witness's operator keeps one process per
// state directory by hand.
func lock(string) (func(), error) { return func() {}, nil }
